package proxy

import (
	"context"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

var usedPortsMu sync.Mutex
var usedPorts = map[int]bool{}

// Grabs an os assigned port then frees it
func freePort(t *testing.T) int {
	t.Helper()
	for range 50 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("free port probe failed %v", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		// Process wide set stops reuse across tests
		usedPortsMu.Lock()
		fresh := !usedPorts[port]
		usedPorts[port] = true
		usedPortsMu.Unlock()
		if fresh {
			return port
		}
	}
	t.Fatal("no fresh port after many probes")
	return 0
}

// Reads one tcp socket under the manager lock
func lockedTCPSock(m *Manager, port int) *ListenerSocket {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tcpSockets[port]
}

// Reads one udp socket under the manager lock
func lockedUDPSock(m *Manager, port int) *UDPProxy {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.udpSockets[port]
}

// Reads one listener id under the manager lock
func lockedListenerID(m *Manager, port int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listenerIDs[port]
}

// Reconciles listeners and fails the test on error
func mustSync(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.SyncListeners(context.Background()); err != nil {
		t.Fatalf("sync failed %v", err)
	}
}

// Manager on a real store with no docker behind it
func testManager(t *testing.T) (*Manager, *db.Store) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "proxy.db")
	cfg.Database.AutoMigrate = true
	cfg.Server.Host = "127.0.0.1"
	cfg.Server.Port = strconv.Itoa(freePort(t))
	cfg.Proxy.Enabled = true
	cfg.Proxy.PortRangeMin = freePort(t)
	store, err := db.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store open failed %v", err)
	}
	t.Cleanup(func() { store.Close() })
	m, err := NewManager(store, nil, cfg, logger.New())
	if err != nil {
		t.Fatalf("manager create failed %v", err)
	}
	m.SetPanelBackend(freePort(t))
	t.Cleanup(func() { _ = m.Stop() })
	return m, store
}

// Default listener row after a reconcile pass
func defaultListener(t *testing.T, store *db.Store) *v1.ProxyListener {
	t.Helper()
	listeners, err := store.ListProxyListeners(context.Background())
	if err != nil {
		t.Fatalf("list listeners failed %v", err)
	}
	for _, l := range listeners {
		if l.IsDefault {
			return l
		}
	}
	t.Fatal("no default listener row")
	return nil
}

// Bare server row shaped like real creates
func seedServer(t *testing.T, store *db.Store, id string, mutate func(*v1.Server)) *v1.Server {
	t.Helper()
	server := &v1.Server{
		Id:        id,
		Name:      id,
		ModLoader: v1.ModLoader_MOD_LOADER_VANILLA,
		McVersion: "1.21.1",
		Status:    v1.ServerStatus_SERVER_STATUS_STOPPED,
		DataPath:  t.TempDir(),
	}
	if mutate != nil {
		mutate(server)
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatalf("create server failed %v", err)
	}
	return server
}

// First sync bootstraps rows and sockets, reruns change nothing
func TestSyncListenersBootstrap(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()

	mustSync(t, m)

	panelPort := m.panelWebPort()
	if _, err := store.GetProxyListener(ctx, PanelListenerID); err != nil {
		t.Fatalf("panel row missing %v", err)
	}
	def := defaultListener(t, store)
	if !def.Enabled || def.Id == PanelListenerID {
		t.Fatalf("default row wrong: %+v", def)
	}

	panelSock := lockedTCPSock(m, panelPort)
	defSock := lockedTCPSock(m, int(def.Port))
	if panelSock == nil || !panelSock.IsRunning() {
		t.Fatal("panel socket must run")
	}
	if defSock == nil || !defSock.IsRunning() {
		t.Fatal("default socket must run")
	}

	// Named panel routes ride, catch all defaults off
	named, catchAll := false, false
	for _, route := range panelSock.Routes() {
		if route.OwnerKind != OwnerPanel {
			continue
		}
		if route.Hostname == "" {
			catchAll = true
		} else {
			named = true
		}
	}
	if !named {
		t.Fatal("named panel routes missing")
	}
	if catchAll {
		t.Fatal("catch all must stay off by default")
	}

	mustSync(t, m)
	if lockedTCPSock(m, panelPort) != panelSock || lockedTCPSock(m, int(def.Port)) != defSock {
		t.Fatal("unchanged sync must keep socket instances")
	}
}

// Disabled rows lose sockets and release their ports
func TestSyncListenersDisableStopsSocket(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()

	mustSync(t, m)
	def := defaultListener(t, store)
	port := int(def.Port)

	def.Enabled = false
	if err := store.UpdateProxyListener(ctx, def); err != nil {
		t.Fatalf("disable failed %v", err)
	}
	mustSync(t, m)
	if lockedTCPSock(m, port) != nil {
		t.Fatal("disabled listener socket must stop")
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("port must be released: %v", err)
	}
	ln.Close()

	def.Enabled = true
	if err := store.UpdateProxyListener(ctx, def); err != nil {
		t.Fatalf("enable failed %v", err)
	}
	mustSync(t, m)
	if sock := lockedTCPSock(m, port); sock == nil || !sock.IsRunning() {
		t.Fatal("re-enabled listener socket must return")
	}
}

// Server hostname routes replace wholesale on every pass
func TestSyncListenersServesServerRoutes(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()

	mustSync(t, m)
	def := defaultListener(t, store)

	server := seedServer(t, store, "srv-routes", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_STARTING
		s.ProxyHostnames = []string{"alt.example.com", "smp.example.com"}
		s.ProxyListenerId = def.Id
	})
	mustSync(t, m)

	sock := lockedTCPSock(m, int(def.Port))
	names := make(map[string]v1.ProxyRouteState)
	for _, route := range sock.Routes() {
		if route.ServerID == server.Id {
			names[route.Hostname] = route.State
		}
	}
	if len(names) != 2 {
		t.Fatalf("want both hostname routes, got %v", names)
	}
	if names["smp.example.com"] != v1.ProxyRouteState_PROXY_ROUTE_STATE_STARTING {
		t.Fatalf("starting server must route as starting, got %v", names)
	}

	// Dropped hostnames leave on the next pass
	server.ProxyHostnames = []string{"smp.example.com"}
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatalf("update failed %v", err)
	}
	mustSync(t, m)
	for _, route := range sock.Routes() {
		if route.ServerID == server.Id && route.Hostname == "alt.example.com" {
			t.Fatal("removed hostname route must die")
		}
	}
}

// Disabling the proxy keeps only the panel socket alive
func TestSyncListenersPanelSurvivesDisable(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()

	mustSync(t, m)
	def := defaultListener(t, store)
	panelPort := m.panelWebPort()

	if err := m.ApplyConfig(ctx, &v1.ProxyConfig{Enabled: false, CatchAll: true}); err != nil {
		t.Fatalf("disable failed %v", err)
	}
	if lockedTCPSock(m, int(def.Port)) != nil {
		t.Fatal("disabled proxy must stop listener sockets")
	}
	if sock := lockedTCPSock(m, panelPort); sock == nil || !sock.IsRunning() {
		t.Fatal("panel socket must survive the disable")
	}

	if err := m.ApplyConfig(ctx, &v1.ProxyConfig{Enabled: true, CatchAll: true}); err != nil {
		t.Fatalf("enable failed %v", err)
	}
	if sock := lockedTCPSock(m, int(def.Port)); sock == nil || !sock.IsRunning() {
		t.Fatal("listener socket must return on enable")
	}
}

// Relay ports auto create rows and follow backend liveness
func TestSyncListenersUDPRelay(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()

	mustSync(t, m)

	udpPort := freePort(t)
	server := seedServer(t, store, "srv-udp", func(s *v1.Server) {
		s.Status = v1.ServerStatus_SERVER_STATUS_RUNNING
		s.ContainerId = "c-udp"
		s.AdditionalPorts = []*v1.NetworkPort{{
			Name:          "voice",
			HostPort:      int32(udpPort),
			ContainerPort: 24454,
			Protocol:      v1.ModuleProtocol_MODULE_PROTOCOL_UDP,
			ProxyEnabled:  true,
		}}
	})
	// Docker is absent so the address cache stands in
	m.ipCacheMu.Lock()
	m.ipCache[server.ContainerId] = ipEntry{ip: "172.18.0.5", at: time.Now()}
	m.ipCacheMu.Unlock()

	mustSync(t, m)

	var autoRow *v1.ProxyListener
	listeners, err := store.ListProxyListeners(ctx)
	if err != nil {
		t.Fatalf("list listeners failed %v", err)
	}
	for _, l := range listeners {
		if int(l.Port) == udpPort {
			autoRow = l
		}
	}
	if autoRow == nil || !autoRow.AutoCreated {
		t.Fatal("relay demand must auto create its listener row")
	}
	up := lockedUDPSock(m, udpPort)
	if up == nil || !up.IsRunning() {
		t.Fatal("udp relay socket must run")
	}
	route, ok := up.Route()
	if !ok || route.BackendHost != "172.18.0.5" || route.BackendPort != 24454 {
		t.Fatalf("relay route wrong: %+v", route)
	}

	// Stopped backend keeps the row but drops the relay
	if err := store.UpdateServerFields(ctx, server.Id, map[string]any{"status": v1.ServerStatus_SERVER_STATUS_STOPPED}); err != nil {
		t.Fatalf("stop server failed %v", err)
	}
	mustSync(t, m)
	if lockedUDPSock(m, udpPort) != nil {
		t.Fatal("relay without a live backend must stop")
	}
	if lockedTCPSock(m, udpPort) == nil {
		t.Fatal("claimed port keeps its listener socket")
	}

	// Dropped ports retire the idle auto row and socket
	server.Status = v1.ServerStatus_SERVER_STATUS_STOPPED
	server.ContainerId = ""
	server.AdditionalPorts = nil
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatalf("clear ports failed %v", err)
	}
	mustSync(t, m)
	listeners, err = store.ListProxyListeners(ctx)
	if err != nil {
		t.Fatalf("list listeners failed %v", err)
	}
	for _, l := range listeners {
		if int(l.Port) == udpPort {
			t.Fatal("idle auto row must be removed")
		}
	}
	if lockedTCPSock(m, udpPort) != nil {
		t.Fatal("retired row must drop its socket")
	}
}

// Moving a listener port moves its socket
func TestSyncListenersPortMove(t *testing.T) {
	m, store := testManager(t)
	ctx := context.Background()

	mustSync(t, m)
	def := defaultListener(t, store)
	oldPort := int(def.Port)
	newPort := freePort(t)

	def.Port = int32(newPort)
	if err := store.UpdateProxyListener(ctx, def); err != nil {
		t.Fatalf("move failed %v", err)
	}
	mustSync(t, m)
	if lockedTCPSock(m, oldPort) != nil {
		t.Fatal("old port socket must stop")
	}
	sock := lockedTCPSock(m, newPort)
	if sock == nil || !sock.IsRunning() {
		t.Fatal("new port socket must run")
	}
	if lockedListenerID(m, newPort) != def.Id {
		t.Fatal("listener id must follow the port")
	}
}
