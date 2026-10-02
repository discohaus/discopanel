package rpc

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/diagnostics"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/module"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/internal/rpc/handlers"
	"github.com/discohaus/discopanel/internal/rpc/services"
	"github.com/discohaus/discopanel/internal/scheduler"
	"github.com/discohaus/discopanel/internal/telemetry"
	"github.com/discohaus/discopanel/internal/ws"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/agent/v1/agentv1connect"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"github.com/discohaus/discopanel/pkg/transfer"
	web "github.com/discohaus/discopanel/web/discopanel"
	"github.com/nickheyer/protogorm"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Represents the Connect RPC server
type Server struct {
	store            *storage.Store
	docker           *docker.Client
	sender           *command.Sender
	config           *config.Config
	rec              *metrics.Recorder
	log              *logger.Logger
	handler          http.Handler
	proxyManager     *proxy.Manager
	authManager      *auth.Manager
	enforcer         *rbac.Enforcer
	oidcHandler      *auth.OIDCHandler
	logStreamer      *logger.LogStreamer
	scheduler        *scheduler.Scheduler
	lifecycle        *lifecycle.Manager
	metricsCollector *metrics.Collector
	moduleManager    *module.Manager
	bus              *events.Bus
	agentHub         *metrics.Hub
	uploadManager    *transfer.UploadManager
	downloadManager  *transfer.DownloadManager
	completion       *command.Completion
	wsHub            *ws.Hub
	diagnostics      *diagnostics.Runner
	telemetry        *telemetry.Sender
}

// Creates new Connect RPC server
func NewServer(store *storage.Store, docker *docker.Client, sender *command.Sender, cfg *config.Config, proxyManager *proxy.Manager, sched *scheduler.Scheduler, lifecycleManager *lifecycle.Manager, metricsCollector *metrics.Collector, moduleManager *module.Manager, bus *events.Bus, agentHub *metrics.Hub, rec *metrics.Recorder, log *logger.Logger) (*Server, error) {
	// RBAC init failure is fatal, authz must never silently vanish
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		return nil, fmt.Errorf("failed to initialize RBAC enforcer: %w", err)
	}
	if err := enforcer.SeedDefaultPolicies(cfg.Auth.AnonymousAccess); err != nil {
		return nil, fmt.Errorf("failed to seed default policies: %w", err)
	}

	// Initialize auth manager
	authManager, err := auth.NewManager(store, enforcer, &cfg.Auth)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize auth manager: %w", err)
	}

	// Initialize OIDC handler
	oidcHandler := auth.NewOIDCHandler(authManager, store, &cfg.Auth.OIDC, log)

	// Initialize log streamer
	logStreamer := logger.NewLogStreamer(docker.GetDockerClient(), log, 10000)
	lifecycleManager.SetLogStreamer(logStreamer)
	moduleManager.SetLogStreamer(logStreamer)
	moduleManager.SetTokenMinter(authManager)
	sender.SetJournal(rec, logStreamer)

	// Initialize upload manager
	uploadTTL := time.Duration(cfg.Upload.SessionTTL) * time.Minute
	uploadManager := transfer.NewUploadManager(cfg.Storage.TempDir, uploadTTL, cfg.Upload.MaxUploadSize, log)

	// Initialize download manager
	downloadManager := transfer.NewDownloadManager(cfg.Storage.TempDir, uploadTTL, log)

	// Initialize single global command completion engine manager
	completion := command.NewCompletion(log, store, sender, metricsCollector, bus)

	// Initialize WebSocket hub
	wsHub := ws.NewHub(logStreamer, authManager, enforcer, store, docker, sender, metricsCollector, rec, log, completion)
	go wsHub.Run()

	// Self checks and release probes, started by main once serving
	diag := diagnostics.NewRunner(store, docker, cfg, proxyManager, log)
	diag.SetOIDCSource(oidcHandler)

	// Hub heartbeat, feeds the release check, started by main once serving
	heartbeat := telemetry.New(store, docker, cfg, diag, log)
	diag.SetReleaseSource(heartbeat)

	s := &Server{
		store:            store,
		docker:           docker,
		sender:           sender,
		config:           cfg,
		rec:              rec,
		log:              log,
		proxyManager:     proxyManager,
		authManager:      authManager,
		enforcer:         enforcer,
		oidcHandler:      oidcHandler,
		logStreamer:      logStreamer,
		scheduler:        sched,
		lifecycle:        lifecycleManager,
		metricsCollector: metricsCollector,
		moduleManager:    moduleManager,
		bus:              bus,
		agentHub:         agentHub,
		uploadManager:    uploadManager,
		downloadManager:  downloadManager,
		completion:       completion,
		wsHub:            wsHub,
		diagnostics:      diag,
		telemetry:        heartbeat,
	}

	s.setupHandler()
	return s, nil
}

// Setup all Connect RPC handlers
func (s *Server) setupHandler() {
	mux := http.NewServeMux()

	// Register all service handlers
	s.registerServices(mux, s.handlerOptions())

	// Add reflection for gRPC clients
	reflector := grpcreflect.NewStaticReflector(
		discopanelv1connect.AuthServiceName,
		discopanelv1connect.PropertiesServiceName,
		discopanelv1connect.FileServiceName,
		discopanelv1connect.MinecraftServiceName,
		discopanelv1connect.ModServiceName,
		discopanelv1connect.ModpackServiceName,
		discopanelv1connect.ModuleServiceName,
		discopanelv1connect.ProxyServiceName,
		discopanelv1connect.RoleServiceName,
		discopanelv1connect.ServerServiceName,
		discopanelv1connect.SupportServiceName,
		discopanelv1connect.TaskServiceName,
		discopanelv1connect.UploadServiceName,
		discopanelv1connect.UserServiceName,
		agentv1connect.AgentServiceName,
	)
	mux.Handle(grpcreflect.NewHandlerV1(reflector))
	mux.Handle(grpcreflect.NewHandlerV1Alpha(reflector))

	// Register WebSocket handler
	mux.Handle("/ws", s.wsHub)

	// Register OIDC HTTP handlers
	if s.oidcHandler != nil && s.oidcHandler.IsEnabled() {
		mux.HandleFunc("/api/v1/auth/oidc/login", s.oidcHandler.HandleLogin)
		mux.HandleFunc("/api/v1/auth/oidc/callback", s.oidcHandler.HandleCallback)
	}

	// Streaming file upload endpoint
	mux.Handle("/api/v1/upload/", handlers.NewUploadStreamHandler(s.uploadManager, s.authManager, s.enforcer, s.log))

	// Streaming file download endpoint
	mux.Handle("/api/v1/download/", handlers.NewDownloadStreamHandler(s.downloadManager, s.log))

	// Admin heap profile for memory spikes, bearer auth only
	mux.Handle("/api/v1/debug/heap", handlers.NewHeapProfileHandler(s.authManager, s.log))
	// Health check endpoint
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Serve dynamic OpenAPI spec
	mux.HandleFunc("/api/v1/openapi.yaml", handlers.NewOpenAPIHandler(s.log, s.authManager.IsAnyAuthEnabled))

	// Serve frontend for non-RPC routes
	s.setupFrontend(mux)

	// Serves h2c HTTP/2 cleartext
	s.handler = h2c.NewHandler(mux, &http2.Server{})
}

// Builds the interceptor chain and body limits every service shares
func (s *Server) handlerOptions() []connect.HandlerOption {
	interceptors := []connect.Interceptor{
		s.loggingInterceptor(),
		s.authInterceptor(),
		s.redactInterceptor(),
	}
	return []connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		// Bodies are read before auth so the cap guards everyone
		connect.WithReadMaxBytes(s.readMaxBytes()),
		// Enable gRPC, gRPC-Web, and Connect protocols
		connect.WithHandlerOptions(
			connect.WithCompression("gzip", nil, nil),
		),
	}
}

// Largest rpc message, inline file or chunk plus base64 growth
func (s *Server) readMaxBytes() int {
	largest := int64(services.MaxInlineFileBytes)
	if int64(s.config.Upload.MaxChunkSize) > largest {
		largest = int64(s.config.Upload.MaxChunkSize)
	}
	return int(largest + largest/3 + 1<<20)
}

// Registers all Connect RPC service handlers
func (s *Server) registerServices(mux *http.ServeMux, opts []connect.HandlerOption) {
	// Create service instances
	authService := services.NewAuthService(s.store, s.authManager, s.enforcer, s.oidcHandler, s.moduleManager, s.log)
	propertiesService := services.NewPropertiesService(s.store, s.config, s.docker, s.lifecycle, s.rec, s.log)
	fileService := services.NewFileService(s.store, s.docker, s.uploadManager, s.downloadManager, s.rec, s.log)
	minecraftService := services.NewMinecraftService(s.store, s.docker, s.log)
	modService := services.NewModService(s.store, s.docker, s.config, s.uploadManager, s.rec, s.log)
	modpackService := services.NewModpackService(s.store, s.config, s.uploadManager, s.log)
	proxyService := services.NewProxyService(s.store, s.docker, s.proxyManager, s.moduleManager, s.config, s.rec, s.log)
	serverService := services.NewServerService(s.store, s.docker, s.sender, s.config, s.proxyManager, s.lifecycle, s.authManager, s.logStreamer, s.metricsCollector, s.moduleManager, s.bus, s.uploadManager, s.completion, s.rec, s.log)
	supportService := services.NewSupportService(s.store, s.docker, s.config, s.diagnostics, s.telemetry, s.downloadManager, s.log)
	taskService := services.NewTaskService(s.store, s.scheduler, s.rec, s.log)
	userService := services.NewUserService(s.store, s.authManager, s.log)
	roleService := services.NewRoleService(s.store, s.enforcer, s.log)
	moduleService := services.NewModuleService(s.store, s.docker, s.moduleManager, s.proxyManager, s.authManager, s.config, s.logStreamer, s.metricsCollector, s.rec, s.log)
	uploadService := services.NewUploadService(s.uploadManager, s.config, s.log)

	// Register service handlers
	authPath, authHandler := discopanelv1connect.NewAuthServiceHandler(authService, opts...)
	mux.Handle(authPath, authHandler)

	propertiesPath, propertiesHandler := discopanelv1connect.NewPropertiesServiceHandler(propertiesService, opts...)
	mux.Handle(propertiesPath, propertiesHandler)

	filePath, fileHandler := discopanelv1connect.NewFileServiceHandler(fileService, opts...)
	mux.Handle(filePath, fileHandler)

	minecraftPath, minecraftHandler := discopanelv1connect.NewMinecraftServiceHandler(minecraftService, opts...)
	mux.Handle(minecraftPath, minecraftHandler)

	modPath, modHandler := discopanelv1connect.NewModServiceHandler(modService, opts...)
	mux.Handle(modPath, modHandler)

	modpackPath, modpackHandler := discopanelv1connect.NewModpackServiceHandler(modpackService, opts...)
	mux.Handle(modpackPath, modpackHandler)

	proxyPath, proxyHandler := discopanelv1connect.NewProxyServiceHandler(proxyService, opts...)
	mux.Handle(proxyPath, proxyHandler)

	serverPath, serverHandler := discopanelv1connect.NewServerServiceHandler(serverService, opts...)
	mux.Handle(serverPath, serverHandler)

	supportPath, supportHandler := discopanelv1connect.NewSupportServiceHandler(supportService, opts...)
	mux.Handle(supportPath, supportHandler)

	taskPath, taskHandler := discopanelv1connect.NewTaskServiceHandler(taskService, opts...)
	mux.Handle(taskPath, taskHandler)

	userPath, userHandler := discopanelv1connect.NewUserServiceHandler(userService, opts...)
	mux.Handle(userPath, userHandler)

	rolePath, roleHandler := discopanelv1connect.NewRoleServiceHandler(roleService, opts...)
	mux.Handle(rolePath, roleHandler)

	modulePath, moduleHandler := discopanelv1connect.NewModuleServiceHandler(moduleService, opts...)
	mux.Handle(modulePath, moduleHandler)

	uploadPath, uploadHandler := discopanelv1connect.NewUploadServiceHandler(uploadService, opts...)
	mux.Handle(uploadPath, uploadHandler)

	// Agent auth is in-handler, unary interceptors skip bidi streams
	agentService := services.NewAgentService(s.store, s.agentHub, s.log)
	agentPath, agentHandler := agentv1connect.NewAgentServiceHandler(agentService)
	mux.Handle(agentPath, agentHandler)
}

// The HTTP handler for the server
func (s *Server) Handler() http.Handler {
	return s.handler
}

// Creates a Connect interceptor for logging
func (s *Server) loggingInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			// Skip logging for polling endpoints
			if !s.isPollingProcedure(req.Spec().Procedure) {
				s.log.Info("RPC %s %s", req.Peer().Addr, req.Spec().Procedure)
			}
			return next(ctx, req)
		}
	}
}

// Creates a Connect interceptor for authentication and authorization
func (s *Server) authInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure
			perm := protometa.Perm(procedure)

			// Public procedures - no auth required
			if perm.GetPublic() {
				return next(ctx, req)
			}

			// Authenticate via shared auth logic
			user, err := s.authManager.AuthenticateFromHeader(ctx, req.Header().Get("Authorization"))
			if err != nil {
				s.log.Debug("Auth: Token validation failed for %s: %v", procedure, err)
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}

			// Set user in context
			ctx = auth.WithUser(ctx, user)
			// Ledger events in one request share user and trace
			ctx = metrics.WithTrace(metrics.WithSource(ctx, user.Username))

			// Authenticated-only procedures (no specific resource permission needed)
			if perm.GetSession() {
				return next(ctx, req)
			}

			// Unannotated procedures fail closed
			if perm == nil {
				s.log.Error("RBAC annotation missing for %s", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("no permission annotation for %s", procedure))
			}

			objectID := "*"
			if perm.ObjectIdField != "" {
				objectID = extractObjectID(req, perm.ObjectIdField)
				if objectID != "*" {
					resolved, err := s.resolveScopeObject(ctx, perm.Scope, objectID)
					if err != nil {
						return nil, connect.NewError(connect.CodeNotFound, err)
					}
					objectID = resolved
				}
			}
			allowed, err := s.enforcer.Enforce(user.Roles, perm.Resource, perm.Action, objectID)
			if err != nil {
				s.log.Error("RBAC enforcement error: %v", err)
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			if !allowed {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("insufficient permissions for %s/%s", protometa.Name(perm.Resource), protometa.Name(perm.Action)))
			}

			return next(ctx, req)
		}
	}
}

// Lists high-frequency endpoints excluded from logging
var pollingProcedures = []string{
	discopanelv1connect.AuthServiceGetAuthStatusProcedure,
	discopanelv1connect.ServerServiceListServersProcedure,
	discopanelv1connect.ServerServiceGetServerProcedure,
	discopanelv1connect.ServerServiceGetServerLogsProcedure,
	discopanelv1connect.ProxyServiceGetProxyStatusProcedure,
	discopanelv1connect.SupportServiceGetApplicationLogsProcedure,
	discopanelv1connect.SupportServiceGetDiagnosticsProcedure,
	discopanelv1connect.SupportServiceGetVersionStatusProcedure,
	discopanelv1connect.UploadServiceUploadChunkProcedure,
	discopanelv1connect.UploadServiceGetUploadStatusProcedure,
	discopanelv1connect.FileServiceGetExtractionStatusProcedure,
	discopanelv1connect.ServerServiceGetServerPerformanceReportProcedure,
	discopanelv1connect.ServerServiceGetServerActionsProcedure,
	discopanelv1connect.ModuleServiceGetModuleLogsProcedure,
	discopanelv1connect.ModuleServiceListModulesProcedure,
	discopanelv1connect.ModuleServiceListModulePromptsProcedure,
	discopanelv1connect.ModuleServiceGetModuleStatusSnapshotProcedure,
	discopanelv1connect.ModuleServiceGetResolvedAliasesProcedure,
	discopanelv1connect.PropertiesServiceGetGlobalSettingsProcedure,
	discopanelv1connect.PropertiesServiceGetServerPropertiesProcedure,
}

// Reports whether a procedure is a polling endpoint
func (s *Server) isPollingProcedure(procedure string) bool {
	return slices.Contains(pollingProcedures, procedure)
}

// Frontend serving
func (s *Server) setupFrontend(mux *http.ServeMux) {
	// Get frontend source
	fs := s.getFrontendFS()
	if fs == nil {
		s.log.Warn("No frontend found - API only mode")
		return
	}

	// Serve frontend for root path
	mux.Handle("/", s.createFrontendHandler(fs))
}

// Get frontend fs
func (s *Server) getFrontendFS() http.FileSystem {
	// Try embedded frontend first
	if buildFS, err := web.BuildFS(); err == nil {
		s.log.Info("Using embedded frontend")
		return http.FS(buildFS)
	}
	return nil
}

// Create frontend handler
func (s *Server) createFrontendHandler(fs http.FileSystem) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only serve frontend for non-Connect paths
		if isConnectPath(r.URL.Path) {
			http.NotFound(w, r)
			return
		}

		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		cleanPath := strings.TrimPrefix(path, "/")

		// Set content headers Go cannot infer on its own.
		// The stdlib mime table has no .webmanifest entry and the alpine
		// runtime ships no /etc/mime.types, so the manifest would be sniffed
		// as text/plain. Browsers then ignore it and install the app as a bare
		// shortcut with an address bar instead of a standalone window.
		setContentHeaders := func(targetPath string) {
			if strings.HasSuffix(targetPath, ".webmanifest") {
				w.Header().Set("Content-Type", "application/manifest+json")
			} else if strings.HasSuffix(targetPath, ".js") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			} else if strings.HasSuffix(targetPath, ".mjs") {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			}
			if targetPath == "service-worker.js" {
				w.Header().Set("Service-Worker-Allowed", "/")
			}
		}

		file, err := fs.Open(path)
		if err == nil {
			defer file.Close()
			stat, statErr := file.Stat()
			if statErr == nil && !stat.IsDir() {
				setContentHeaders(cleanPath)
				http.ServeContent(w, r, path, stat.ModTime(), file)
				return
			}
		}

		// Never serve SPA index.html fallback for unmatched /api/ paths
		if strings.HasPrefix(path, "/api/") {
			http.NotFound(w, r)
			return
		}

		// Serve index.html for client-side routing fallback
		indexFile, err := fs.Open("/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer indexFile.Close()

		stat, _ := indexFile.Stat()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		http.ServeContent(w, r, "/index.html", stat.ModTime(), indexFile)
	}
}

// Checks if a path is a Connect RPC path
func isConnectPath(path string) bool {
	// Connect paths start with service names
	connectPrefixes := []string{
		"/discopanel.v1.",
		"/discopanel.agent.",
		"/grpc.reflection.",
		"/connect.",
	}

	for _, prefix := range connectPrefixes {
		if len(path) > len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// Clears secrets a handler forgot to redact
func (s *Server) redactInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err != nil || resp == nil {
				return resp, err
			}
			if msg, ok := resp.Any().(proto.Message); ok {
				if n := protogorm.Scrub(msg); n > 0 {
					s.log.Error("Redact backstop cleared %d secret fields on %s", n, req.Spec().Procedure)
				}
			}
			return resp, nil
		}
	}
}

func (s *Server) resolveScopeObject(ctx context.Context, scope optionsv1.ObjectScope, objectID string) (string, error) {
	switch scope {
	case optionsv1.ObjectScope_OBJECT_SCOPE_TASK:
		task, err := s.store.GetScheduledTask(ctx, objectID)
		if err != nil {
			return "", fmt.Errorf("task not found")
		}
		return task.ServerId, nil
	case optionsv1.ObjectScope_OBJECT_SCOPE_TASK_EXECUTION:
		execution, err := s.store.GetTaskExecution(ctx, objectID)
		if err != nil {
			return "", fmt.Errorf("task execution not found")
		}
		return execution.ServerId, nil
	case optionsv1.ObjectScope_OBJECT_SCOPE_MODULE:
		mod, err := s.store.GetModule(ctx, objectID)
		if err != nil {
			return "", fmt.Errorf("module not found")
		}
		return mod.ServerId, nil
	default:
		return objectID, nil
	}
}

// Extracts a named string field from a protobuf request message
func extractObjectID(req connect.AnyRequest, fieldName string) string {
	msg, ok := req.Any().(proto.Message)
	if !ok {
		return "*"
	}
	fd := msg.ProtoReflect().Descriptor().Fields().ByName(protoreflect.Name(fieldName))
	if fd == nil {
		return "*"
	}
	val := msg.ProtoReflect().Get(fd)
	if str := val.String(); str != "" {
		return str
	}
	return "*"
}

// Returns the current recovery key from the auth manager
func (s *Server) RecoveryKey() string {
	return s.authManager.GetRecoveryKey()
}

// Exposes the streamer for cross-component wiring
func (s *Server) LogStreamer() *logger.LogStreamer {
	return s.logStreamer
}

// Exposes the diagnostics runner for startup wiring
func (s *Server) Diagnostics() *diagnostics.Runner {
	return s.diagnostics
}

// Exposes the heartbeat sender for startup wiring
func (s *Server) Telemetry() *telemetry.Sender {
	return s.telemetry
}

// Exposes the OIDC handler so main can stop its retries
func (s *Server) OIDC() *auth.OIDCHandler {
	return s.oidcHandler
}

// Attaches a servers container output to its log stream
func (s *Server) StartLogStreaming(serverID, containerID string) error {
	return s.logStreamer.StartStreaming(serverID, containerID)
}
