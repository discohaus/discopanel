package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
)

// Handles HTTP reverse proxying keyed by Host header
type HTTPProxy struct {
	server       *http.Server
	routes       map[string]*Route
	routesMutex  sync.RWMutex
	proxies      map[string]*httputil.ReverseProxy
	proxiesMutex sync.Mutex
	logger       *logger.Logger
	listenAddr   string
	running      bool
	runningMutex sync.RWMutex
}

// Creates a new HTTP reverse proxy instance
func NewHTTPProxy(cfg *Config) *HTTPProxy {
	p := &HTTPProxy{
		routes:     make(map[string]*Route),
		proxies:    make(map[string]*httputil.ReverseProxy),
		logger:     cfg.Logger,
		listenAddr: cfg.ListenAddr,
	}

	p.server = &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: p,
	}

	return p
}

// Checks if this is a WebSocket upgrade request
func isWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// Implements http.Handler for routing requests
func (p *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Extract hostname from Host header
	hostname := strings.ToLower(strings.Split(r.Host, ":")[0])

	// Find the route, empty hostname key is the catch all
	p.routesMutex.RLock()
	route, exists := p.routes[hostname]
	if !exists {
		route, exists = p.routes[""]
	}
	p.routesMutex.RUnlock()

	if !exists {
		p.logger.Debug("No route found for hostname: %s", hostname)
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	// Handle WebSocket upgrade separately
	if isWebSocketRequest(r) {
		p.handleWebSocket(w, r, route)
		return
	}

	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	p.proxyFor(backendAddr).ServeHTTP(w, r)
}

// Returns the cached reverse proxy for a backend
func (p *HTTPProxy) proxyFor(backendAddr string) *httputil.ReverseProxy {
	p.proxiesMutex.Lock()
	defer p.proxiesMutex.Unlock()

	if proxy, ok := p.proxies[backendAddr]; ok {
		return proxy
	}

	target := &url.URL{Scheme: "http", Host: backendAddr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.logger.Error("Proxy error for %s: %v", r.Host, err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	p.proxies[backendAddr] = proxy
	return proxy
}

// Drops cached reverse proxies after route changes
func (p *HTTPProxy) dropProxies() {
	p.proxiesMutex.Lock()
	p.proxies = make(map[string]*httputil.ReverseProxy)
	p.proxiesMutex.Unlock()
}

// Handles WebSocket upgrade requests
func (p *HTTPProxy) handleWebSocket(w http.ResponseWriter, r *http.Request, route *Route) {
	// Hijack the client connection
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.logger.Error("WebSocket: ResponseWriter doesn't support hijacking")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	clientConn, clientRW, err := hijacker.Hijack()
	if err != nil {
		p.logger.Error("WebSocket: Failed to hijack connection: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	// Connect to backend
	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	backendConn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
	if err != nil {
		p.logger.Error("WebSocket: Failed to connect to backend %s: %v", backendAddr, err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer backendConn.Close()

	// Forward the original HTTP upgrade request to backend
	if clientIP, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		r.Header.Set("X-Forwarded-For", clientIP)
	}
	proto := "http"
	if r.TLS != nil {
		proto = "https"
	}
	r.Header.Set("X-Forwarded-Proto", proto)
	if err := r.Write(backendConn); err != nil {
		p.logger.Error("WebSocket: Failed to forward upgrade request: %v", err)
		return
	}

	// Flush client bytes buffered ahead of the raw relay
	if buffered := clientRW.Reader.Buffered(); buffered > 0 {
		pending, _ := clientRW.Reader.Peek(buffered)
		if _, err := backendConn.Write(pending); err != nil {
			p.logger.Error("WebSocket: Failed to flush buffered client data: %v", err)
			return
		}
		clientRW.Reader.Discard(buffered)
	}

	p.logger.Debug("WebSocket connection established: %s -> %s", r.RemoteAddr, backendAddr)
	relay(clientConn, backendConn)
}

// Adds a new routing rule
func (p *HTTPProxy) AddRoute(serverID, hostname, backendHost string, backendPort int) {
	p.routesMutex.Lock()
	hostname = strings.ToLower(strings.Split(hostname, ":")[0])
	p.routes[hostname] = &Route{
		ServerID:    serverID,
		Hostname:    hostname,
		BackendHost: backendHost,
		BackendPort: backendPort,
	}
	p.routesMutex.Unlock()

	p.dropProxies()
	p.logger.Info("HTTP proxy added route: hostname=%s backend=%s:%d", hostname, backendHost, backendPort)
}

// Removes a routing rule
func (p *HTTPProxy) RemoveRoute(hostname string) {
	p.routesMutex.Lock()
	hostname = strings.ToLower(strings.Split(hostname, ":")[0])
	delete(p.routes, hostname)
	p.routesMutex.Unlock()

	p.dropProxies()
	p.logger.Info("HTTP proxy removed route: hostname=%s", hostname)
}

// Updates the backend for a route
func (p *HTTPProxy) UpdateRoute(hostname, backendHost string, backendPort int) {
	p.routesMutex.Lock()
	hostname = strings.ToLower(strings.Split(hostname, ":")[0])
	if route, exists := p.routes[hostname]; exists {
		route.BackendHost = backendHost
		route.BackendPort = backendPort
		p.logger.Info("HTTP proxy updated route: hostname=%s backend=%s:%d", hostname, backendHost, backendPort)
	}
	p.routesMutex.Unlock()

	p.dropProxies()
}

// Returns a copy of all current routes
func (p *HTTPProxy) GetRoutes() map[string]*Route {
	p.routesMutex.RLock()
	defer p.routesMutex.RUnlock()

	routes := make(map[string]*Route)
	for k, v := range p.routes {
		routeCopy := *v
		routes[k] = &routeCopy
	}
	return routes
}

// Starts the HTTP proxy server
func (p *HTTPProxy) Start() error {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()

	if p.running {
		return fmt.Errorf("HTTP proxy already running")
	}

	listener, err := net.Listen("tcp", p.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", p.listenAddr, err)
	}

	p.running = true

	go func() {
		if err := p.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			p.logger.Error("HTTP proxy error: %v", err)
		}
	}()

	p.logger.Info("HTTP proxy started on %s", p.listenAddr)
	return nil
}

// Stops the HTTP proxy server
func (p *HTTPProxy) Stop() error {
	p.runningMutex.Lock()
	defer p.runningMutex.Unlock()

	if !p.running {
		return nil
	}

	p.running = false

	if err := p.server.Shutdown(context.Background()); err != nil {
		return fmt.Errorf("failed to shutdown HTTP proxy: %w", err)
	}

	p.logger.Info("HTTP proxy stopped")
	return nil
}

// Returns whether the proxy is running
func (p *HTTPProxy) IsRunning() bool {
	p.runningMutex.RLock()
	defer p.runningMutex.RUnlock()
	return p.running
}
