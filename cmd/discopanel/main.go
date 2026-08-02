package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"


	"github.com/discohaus/discopanel/internal/alias"
	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/module"
	"github.com/discohaus/discopanel/internal/provisioner"
	"github.com/discohaus/discopanel/internal/proxy"
	"github.com/discohaus/discopanel/internal/rpc"
	"github.com/discohaus/discopanel/internal/scheduler"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func main() {
	var configPath = flag.String("config", "", "Path to config file, default locations searched when empty")
	flag.Parse()

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Init logger
	log := logger.NewWithConfig(&cfg.Logging)
	defer log.Close()

	// Create required directories
	dirs := []string{
		cfg.Storage.DataDir,
		cfg.Storage.BackupDir,
		cfg.Storage.TempDir,
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			log.Fatal("Failed to create directory %s: %v", dir, err)
		}
	}

	// Initialize storage w/ migrations and seeding
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		log.Fatal("Failed to initialize storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Initialize Docker client with configuration
	dockerClient, err := docker.NewClient(cfg.Docker.Host, log, docker.ClientConfig{
		APIVersion:   cfg.Docker.Version,
		NetworkName:  cfg.Docker.NetworkName,
		RuntimeImage: cfg.Docker.RuntimeImage,
		DNS:          cfg.Docker.DNS,
		Labels:       cfg.Docker.Labels,
	})
	if err != nil {
		log.Fatal("Failed to initialize Docker client: %v", err)
	}
	defer dockerClient.Close()

	// Ensure Docker network exists
	if err := dockerClient.EnsureNetwork(); err != nil {
		log.Error("Failed to ensure Docker network: %v", err)
	}

	// Clean up orphaned containers on startup
	log.Info("Checking for orphaned containers...")
	servers, err := store.ListServers(ctx)
	if err != nil {
		log.Error("Failed to list servers for cleanup: %v", err)
	}
	modules, err := store.ListModules(ctx)
	if err != nil {
		log.Error("Failed to list modules for cleanup: %v", err)
	}

	// Build map of tracked container IDs
	trackedIDs := make(map[string]bool)
	for _, server := range servers {
		if server.ContainerId != "" {
			trackedIDs[server.ContainerId] = true
		}
	}
	for _, module := range modules {
		if module.ContainerId != "" {
			trackedIDs[module.ContainerId] = true
		}
	}

	// Clean up orphaned containers
	if err := dockerClient.CleanupOrphanedContainers(ctx, trackedIDs, log); err != nil {
		log.Error("Failed to cleanup orphaned containers: %v", err)
	}

	// Load proxy configuration from database
	proxyConfig, isNew, err := store.GetProxyConfig(ctx)
	if err != nil {
		log.Warn("Failed to load proxy config from database, using file config: %v", err)
	} else {
		if isNew {
			proxyConfig.Enabled = cfg.Proxy.Enabled
			err = store.SaveProxyConfig(ctx, proxyConfig)
			if err != nil {
				log.Error("Failed to set proxy configs from startup configuration values: %v", err)
			}
		} else {
			cfg.Proxy.Enabled = proxyConfig.Enabled
		}

		log.Info("Loaded proxy configuration from database: enabled=%v, hostnames=%v",
			cfg.Proxy.Enabled, proxyConfig.Hostnames)
	}

	// Initialize proxy manager
	proxyManager := proxy.NewManager(store, dockerClient, cfg, log)

	// Aliases resolve host.hostname through the proxy manager
	alias.SetHostnameSource(proxyManager.PanelHostname)

	// Panel http serves on loopback behind the panel socket
	panelLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal("Failed to bind the panel backend: %v", err)
	}
	proxyManager.SetPanelBackend(panelLn.Addr().(*net.TCPAddr).Port)

	// Start proxy manager, the panel socket must come up
	if err := proxyManager.Start(); err != nil {
		log.Fatal("Failed to start proxy manager: %v", err)
	}
	defer proxyManager.Stop()

	// Initialize command sender
	sender := command.NewSender(store, dockerClient, cfg)

	// Initialize the central event bus
	eventBus := events.NewBus(log)

	// One recorder owns the per-server activity ledger
	rec := metrics.NewRecorder(store, log)

	// Initialize metrics collector, the panel side health source
	metricsCollector := metrics.NewCollector(store, dockerClient, cfg, eventBus, log, metrics.DefaultConfig())
	dockerClient.SetHealthChecker(metricsCollector)
	metricsCollector.SetProxyTrafficSource(proxyManager.GetRouteStats)

	// Agent hub feeds telemetry and serves console commands
	agentHub := metrics.NewHub(metricsCollector, eventBus, rec, log)
	sender.SetAgent(agentHub)

	// Lifecycle manager owns all start stop pause transitions
	prov := provisioner.New(store, dockerClient, cfg, rec, log)
	lifecycleManager := lifecycle.NewManager(store, dockerClient, prov, sender, proxyManager, eventBus, cfg, rec, log)
	lifecycleManager.SetPlayerCounter(metricsCollector)

	// Proxy answers pings for paused servers, wakes logins
	proxyManager.SetServerGate(lifecycleManager)

	// Initialize task scheduler
	taskScheduler := scheduler.NewScheduler(store, dockerClient, sender, lifecycleManager, cfg, metricsCollector, rec, log, scheduler.Config{
		CheckInterval: time.Duration(cfg.Docker.SyncInterval) * time.Second, // Use same interval as container status monitor
	})

	// Start the scheduler
	if err := taskScheduler.Start(); err != nil {
		log.Error("Failed to start task scheduler: %v", err)
	}
	defer taskScheduler.Stop()

	// Initialize module manager, started after rpc wiring below
	moduleManager := module.NewManager(store, dockerClient, sender, cfg, proxyManager, log)

	// Event consumers register on the bus here
	eventBus.Subscribe(moduleManager.HandleServerEvent)
	eventBus.Subscribe(taskScheduler.HandleServerEvent)
	eventBus.Subscribe(lifecycleManager.HandleServerEvent)

	// Start the metrics collector now that consumers are subscribed
	if err := metricsCollector.Start(); err != nil {
		log.Error("Failed to start metrics collector: %v", err)
	}
	defer metricsCollector.Stop()

	// Start the idle watcher (autopause/autostop policies)
	lifecycleManager.StartIdleWatcher()
	defer lifecycleManager.StopIdleWatcher()

	// Initialize RPC server with full configuration
	rpcServer, err := rpc.NewServer(store, dockerClient, sender, cfg, proxyManager, taskScheduler, lifecycleManager, metricsCollector, moduleManager, eventBus, agentHub, rec, log)
	if err != nil {
		log.Fatal("Failed to initialize RPC server: %v", err)
	}

	// Rpc wiring set the token minter, safe to seed now
	if err := moduleManager.Start(); err != nil {
		log.Error("Failed to start module manager: %v", err)
	}
	defer moduleManager.Stop()

	// Provision progress lands in the server console
	if streamer := rpcServer.LogStreamer(); streamer != nil {
		prov.SetProgressSink(streamer.AddSystemEntry)
		agentHub.SetConsoleSink(streamer.AddSystemEntry)
		rec.SetConsoleSink(streamer.AddSystemEntry)
	}

	// Print recovery key
	if key := rpcServer.RecoveryKey(); key != "" {
		fmt.Fprintf(os.Stderr, "\n=======================================================================\n")
		fmt.Fprintf(os.Stderr, "RECOVERY KEY (use to reset panel access if locked out)\n")
		fmt.Fprintf(os.Stderr, "%s\n", key)
		fmt.Fprintf(os.Stderr, "=======================================================================\n\n")
		keyPath := filepath.Join(cfg.Storage.DataDir, "recovery.key")
		if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
			log.Error("Failed to write recovery key file: %v", err)
		}
	}

	// Auto-start servers that have auto_start enabled
	log.Info("Checking for servers with auto-start enabled...")
	autoStartServers, err := store.ListServers(ctx)
	if err != nil {
		log.Warn("Failed to auto-start server instances due to error: %v\n", err)
	}

	for i := range autoStartServers {
		if autoStartServers[i].AutoStart && !autoStartServers[i].Detached {
			server := autoStartServers[i]
			log.Info("Auto-starting server: %s", server.Name)
			go func() {
				// Wait a moment for everything to initialize
				time.Sleep(2 * time.Second)

				// Already-running containers just need their log stream reattached
				if server.ContainerId != "" {
					if status, err := dockerClient.GetContainerStatus(ctx, server.ContainerId); err == nil &&
						(status == v1.ServerStatus_SERVER_STATUS_RUNNING || status == v1.ServerStatus_SERVER_STATUS_STARTING) {
						if err := rpcServer.StartLogStreaming(server.Id, server.ContainerId); err != nil {
							log.Error("Failed to start log streaming for running server %s: %v", server.Name, err)
						}
						if err := proxyManager.SyncServerRoutes(ctx, server); err != nil {
							log.Error("Failed to update proxy routes for %s: %v", server.Name, err)
						}
						return
					}
				}

				startCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
				defer cancel()
				if err := lifecycleManager.Start(metrics.WithTrace(metrics.WithSource(startCtx, "autostart")), server.Id); err != nil {
					log.Error("Failed to auto-start server %s: %v", server.Name, err)
					return
				}
				log.Info("Successfully auto-started server: %s", server.Name)
			}()
		}
	}

	// Clean expired sessions on startup, then periodically
	if err := store.CleanExpiredSessions(ctx, time.Now().UTC()); err != nil {
		log.Error("Failed to clean expired sessions on startup: %v", err)
	}
	stopSessionCleanup := make(chan struct{})
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := store.CleanExpiredSessions(context.Background(), time.Now().UTC()); err != nil {
					log.Error("Failed to clean expired sessions: %v", err)
				}
			case <-stopSessionCleanup:
				return
			}
		}
	}()

	// Start container status monitor
	stopMonitor := make(chan struct{})
	go func() {
		ticker := time.NewTicker(time.Duration(cfg.Docker.SyncInterval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Update status for all servers with containers
				ctx := context.Background()
				servers, err := store.ListServers(ctx)
				if err != nil {
					continue
				}

				// Port route rebuilds batch into one sync per tick
				needSync := false
				for _, server := range servers {
					if server.ContainerId == "" {
						continue
					}
					status, err := dockerClient.GetContainerStatus(ctx, server.ContainerId)
					if err != nil || server.Status == status {
						continue
					}
					server.Status = status
					if err := store.UpdateServerFields(ctx, server.Id, map[string]any{"status": status}); err != nil {
						log.Error("Failed to update server status: %v", err)
					}
					// Updates proxy route on status change when proxied
					if len(server.ProxyHostnames) > 0 {
						if err := proxyManager.UpdateServerRoute(server); err != nil {
							log.Error("Failed to update proxy route for %s: %v", server.Name, err)
						}
					}
					if proxy.HasProxyPorts(server.AdditionalPorts) {
						needSync = true
					}
				}

				// Crashed modules must drop their routes too
				modules, err := store.ListModules(ctx)
				if err == nil {
					for _, mod := range modules {
						if mod.ContainerId == "" {
							continue
						}
						status, serr := moduleManager.StatusForModule(ctx, mod)
						if serr != nil || mod.Status == status {
							continue
						}
						mod.Status = status
						if err := store.UpdateModule(ctx, mod); err != nil {
							log.Error("Failed to update module status: %v", err)
						}
						if proxy.HasProxyPorts(mod.Ports) {
							needSync = true
						}
					}
				}

				if needSync {
					if err := proxyManager.SyncListeners(ctx); err != nil {
						log.Error("Failed to sync proxy routes: %v", err)
					}
				}
			case <-stopMonitor:
				return
			}
		}
	}()

	// No body deadlines, agent streams stay open for hours
	srv := &http.Server{
		Handler:           rpcServer.Handler(),
		ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout) * time.Second,
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeout) * time.Second,
	}

	// Panel socket terminates tls and relays plain http here
	go func() {
		log.Info("Starting DiscoPanel on %s:%s", cfg.Server.Host, cfg.Server.Port)
		if err := srv.Serve(panelLn); err != nil && err != http.ErrServerClosed {
			log.Fatal("Failed to start server: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	log.Info("Shutting down server...")
	close(stopSessionCleanup)
	close(stopMonitor)

	// Graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Stop managed servers in parallel through the lifecycle owner
	log.Info("Checking for managed containers...")
	managedServers, lsErr := store.ListServers(ctx)
	if lsErr != nil {
		log.Error("Unable to list managed containers prior to shutdown: %v", lsErr)
	}

	var stopWG sync.WaitGroup
	for _, server := range managedServers {
		if server.Detached {
			log.Info("Skipping shutdown of detached server: %s", server.Name)
			continue
		}
		if server.Status != v1.ServerStatus_SERVER_STATUS_RUNNING {
			continue
		}
		stopWG.Go(func() {
			log.Info("Stopping managed server: %s", server.Name)
			stopCtx, stopCancel := context.WithTimeout(metrics.WithTrace(metrics.WithSource(ctx, "system")), 25*time.Second)
			defer stopCancel()
			if err := lifecycleManager.Stop(stopCtx, server.Id); err != nil {
				log.Error("Failed to stop server %s: %v", server.Name, err)
			}
		})
	}
	stopWG.Wait()

	if err := srv.Shutdown(ctx); err != nil {
		log.Error("Server forced to shutdown: %v", err)
	}

	log.Info("Server stopped\n")
}
