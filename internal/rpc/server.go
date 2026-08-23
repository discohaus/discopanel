package rpc

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/grpcreflect"
	"github.com/nickheyer/discopanel/internal/auth"
	"github.com/nickheyer/discopanel/internal/command"
	"github.com/nickheyer/discopanel/internal/config"
	storage "github.com/nickheyer/discopanel/internal/db"
	"github.com/nickheyer/discopanel/internal/docker"
	"github.com/nickheyer/discopanel/internal/events"
	"github.com/nickheyer/discopanel/internal/metrics"
	"github.com/nickheyer/discopanel/internal/module"
	"github.com/nickheyer/discopanel/internal/proxy"
	"github.com/nickheyer/discopanel/internal/rbac"
	"github.com/nickheyer/discopanel/internal/rpc/handlers"
	"github.com/nickheyer/discopanel/internal/rpc/services"
	"github.com/nickheyer/discopanel/internal/scheduler"
	"github.com/nickheyer/discopanel/internal/ws"
	"github.com/nickheyer/discopanel/pkg/download"
	"github.com/nickheyer/discopanel/pkg/logger"
	v1 "github.com/nickheyer/discopanel/pkg/proto/discopanel/v1"
	"github.com/nickheyer/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/nickheyer/discopanel/pkg/upload"
	web "github.com/nickheyer/discopanel/web/discopanel"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Server represents the Connect RPC server
type Server struct {
	store            *storage.Store
	docker           *docker.Client
	sender           *command.Sender
	config           *config.Config
	log              *logger.Logger
	handler          http.Handler
	proxyManager     *proxy.Manager
	authManager      *auth.Manager
	enforcer         *rbac.Enforcer
	oidcHandler      *auth.OIDCHandler
	logStreamer      *logger.LogStreamer
	scheduler        *scheduler.Scheduler
	metricsCollector *metrics.Collector
	moduleManager    *module.Manager
	bus              *events.Bus
	uploadManager    *upload.Manager
	downloadManager  *download.Manager
	wsHub            *ws.Hub
}

// Creates new Connect RPC server
func NewServer(store *storage.Store, docker *docker.Client, sender *command.Sender, cfg *config.Config, proxyManager *proxy.Manager, sched *scheduler.Scheduler, metricsCollector *metrics.Collector, moduleManager *module.Manager, bus *events.Bus, log *logger.Logger) *Server {
	// Initialize RBAC enforcer
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		log.Error("Failed to initialize RBAC enforcer: %v", err)
	}
	if enforcer != nil {
		if err := enforcer.SeedDefaultPolicies(cfg.Auth.AnonymousAccess); err != nil {
			log.Error("Failed to seed default policies: %v", err)
		}
	}

	// Initialize auth manager
	authManager, err := auth.NewManager(store, enforcer, &cfg.Auth)
	if err != nil {
		log.Error("Failed to initialize auth manager: %v", err)
	}

	// Initialize OIDC handler
	oidcHandler, err := auth.NewOIDCHandler(authManager, store, &cfg.Auth.OIDC, log)
	if err != nil {
		log.Warn("Failed to initialize OIDC handler: %v", err)
		oidcHandler, _ = auth.NewOIDCHandler(authManager, store, &config.OIDCConfig{}, log)
	}

	// Initialize log streamer
	logStreamer := logger.NewLogStreamer(docker.GetDockerClient(), log, 10000)
	docker.SetLogStreamer(logStreamer)

	// Initialize upload manager
	uploadTTL := time.Duration(cfg.Upload.SessionTTL) * time.Minute
	uploadManager := upload.NewManager(cfg.Storage.TempDir, uploadTTL, cfg.Upload.MaxUploadSize, log)

	// Initialize download manager
	downloadManager := download.NewManager(cfg.Storage.TempDir, uploadTTL, log)

	// Initialize WebSocket hub
	wsHub := ws.NewHub(logStreamer, authManager, enforcer, store, docker, sender, log)
	go wsHub.Run()

	s := &Server{
		store:            store,
		docker:           docker,
		sender:           sender,
		config:           cfg,
		log:              log,
		proxyManager:     proxyManager,
		authManager:      authManager,
		enforcer:         enforcer,
		oidcHandler:      oidcHandler,
		logStreamer:      logStreamer,
		scheduler:        sched,
		metricsCollector: metricsCollector,
		moduleManager:    moduleManager,
		bus:              bus,
		uploadManager:    uploadManager,
		downloadManager:  downloadManager,
		wsHub:            wsHub,
	}

	s.setupHandler()
	return s
}

// Setup all Connect RPC handlers
func (s *Server) setupHandler() {
	mux := http.NewServeMux()

	// Configure Connect options
	interceptors := []connect.Interceptor{
		s.loggingInterceptor(),
		s.authInterceptor(),
	}

	opts := []connect.HandlerOption{
		connect.WithInterceptors(interceptors...),
		// Enable gRPC, gRPC-Web, and Connect protocols
		connect.WithHandlerOptions(
			connect.WithCompression("gzip", nil, nil),
		),
	}

	// Register all service handlers
	s.registerServices(mux, opts)

	// Add reflection for gRPC clients
	reflector := grpcreflect.NewStaticReflector(
		discopanelv1connect.AuthServiceName,
		discopanelv1connect.ConfigServiceName,
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
	mux.Handle("/api/v1/download/", handlers.NewDownloadStreamHandler(s.downloadManager, s.authManager, s.enforcer, s.log))

	// Serve dynamic OpenAPI spec
	mux.HandleFunc("/api/v1/openapi.yaml", handlers.NewOpenAPIHandler(s.log, s.authManager.IsAnyAuthEnabled))

	// Serve frontend for non-RPC routes
	s.setupFrontend(mux)

	// h2c HTTP/2 cleartext
	s.handler = h2c.NewHandler(mux, &http2.Server{})
}

// Registers all Connect RPC service handlers
func (s *Server) registerServices(mux *http.ServeMux, opts []connect.HandlerOption) {
	// Create service instances
	authService := services.NewAuthService(s.store, s.authManager, s.enforcer, s.oidcHandler, s.log)
	configService := services.NewConfigService(s.store, s.config, s.docker, s.proxyManager, s.log)
	fileService := services.NewFileService(s.store, s.docker, s.uploadManager, s.downloadManager, s.log)
	minecraftService := services.NewMinecraftService(s.store, s.docker, s.log)
	modService := services.NewModService(s.store, s.docker, s.uploadManager, s.log)
	modpackService := services.NewModpackService(s.store, s.config, s.uploadManager, s.log)
	proxyService := services.NewProxyService(s.store, s.docker, s.proxyManager, s.config, s.logStreamer, s.log)
	serverService := services.NewServerService(s.store, s.docker, s.sender, s.config, s.proxyManager, s.logStreamer, s.metricsCollector, s.moduleManager, s.bus, s.log)
	// Route lazy wake-ups through ServerService so they use the same lifecycle
	// events and container handling as a manual server start.
	if s.proxyManager != nil {
		s.proxyManager.SetWakeHandler(func(ctx context.Context, serverID string) error {
			server, err := s.store.GetServer(ctx, serverID)
			if err != nil {
				return err
			}
			if server.ContainerID == "" {
				return fmt.Errorf("server has no container")
			}

			status, err := s.docker.GetContainerStatus(ctx, server.ContainerID)
			if err != nil {
				return err
			}

			switch status {
			case storage.StatusRunning, storage.StatusStarting:
				return nil
			case storage.StatusStopped:
				_, err = serverService.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverID}))
				return err
			default:
				return fmt.Errorf("server cannot be woken while in %s state", status)
			}
		})
	}
	supportService := services.NewSupportService(s.store, s.docker, s.config, s.log)
	taskService := services.NewTaskService(s.store, s.scheduler, s.log)
	userService := services.NewUserService(s.store, s.authManager, s.log)
	roleService := services.NewRoleService(s.store, s.enforcer, s.log)
	moduleService := services.NewModuleService(s.store, s.docker, s.moduleManager, s.proxyManager, s.authManager, s.config, s.logStreamer, s.log)
	uploadService := services.NewUploadService(s.uploadManager, s.config, s.log)

	// Register service handlers
	authPath, authHandler := discopanelv1connect.NewAuthServiceHandler(authService, opts...)
	mux.Handle(authPath, authHandler)

	configPath, configHandler := discopanelv1connect.NewConfigServiceHandler(configService, opts...)
	mux.Handle(configPath, configHandler)

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

			// Public procedures - no auth required
			if rbac.PublicProcedures[procedure] {
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

			// Authenticated-only procedures (no specific resource permission needed)
			if rbac.AuthenticatedOnlyProcedures[procedure] {
				return next(ctx, req)
			}

			// Check resource permission
			if perm, ok := rbac.ProcedurePermissions[procedure]; ok {
				if s.enforcer != nil {
					objectID := "*"
					if perm.ObjectIDField != "" {
						objectID = extractObjectID(req, perm.ObjectIDField)
					}
					allowed, err := s.enforcer.Enforce(user.Roles, perm.Resource, perm.Action, objectID)
					if err != nil {
						s.log.Error("RBAC enforcement error: %v", err)
						return nil, connect.NewError(connect.CodeInternal, err)
					}
					if !allowed {
						return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("insufficient permissions for %s/%s", perm.Resource, perm.Action))
					}
				}
			}

			return next(ctx, req)
		}
	}
}

// pollingProcedures lists endpoints that are called frequently and should be excluded from logging.
var pollingProcedures = []string{
	"/discopanel.v1.AuthService/GetAuthStatus",
	"/discopanel.v1.ServerService/ListServers",
	"/discopanel.v1.ServerService/GetServer",
	"/discopanel.v1.ServerService/GetServerLogs",
	"/discopanel.v1.ProxyService/GetProxyStatus",
	"/discopanel.v1.SupportService/GetApplicationLogs",
	"/discopanel.v1.UploadService/UploadChunk",
	"/discopanel.v1.UploadService/GetUploadStatus",
	"/discopanel.v1.FileService/GetExtractionStatus",
}

// Checks if a procedure is a polling endpoint or high-frequency endpoint
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

		// Try to serve the file directly (static assets like JS, CSS, images)
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}

		file, err := fs.Open(path)
		if err == nil {
			defer file.Close()
			stat, _ := file.Stat()
			http.ServeContent(w, r, path, stat.ModTime(), file)
			return
		}

		// Serve index.html for client-side routing
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

// extractObjectID extracts a named string field from a protobuf request message
// using reflection. Falls back to "*" if the field is missing or empty.
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

// RecoveryKey returns the current recovery key from the auth manager.
func (s *Server) RecoveryKey() string {
	return s.authManager.GetRecoveryKey()
}

// Starts log streaming for a container
func (s *Server) StartLogStreaming(containerID string) error {
	return s.logStreamer.StartStreaming(containerID)
}
