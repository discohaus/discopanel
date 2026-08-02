package proxy

import (
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

// Serves the http lane of a listener socket by Host header
type httpLane struct {
	routesMap    map[string]*Route
	routesMutex  sync.RWMutex
	proxies      map[string]*httputil.ReverseProxy
	proxiesMutex sync.Mutex
	server       *http.Server
	serverMutex  sync.Mutex
	logger       *logger.Logger
}

// Creates the http lane for one socket
func newHTTPLane(log *logger.Logger) *httpLane {
	return &httpLane{
		routesMap: make(map[string]*Route),
		proxies:   make(map[string]*httputil.ReverseProxy),
		logger:    log,
	}
}

// Serves sniffed connections handed over by the mux
func (p *httpLane) start(feed *connFeed) {
	p.serverMutex.Lock()
	defer p.serverMutex.Unlock()
	// Header stalls drop, bodies stay open for long streams
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	// Agent streams arrive as cleartext http2
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	p.server.Protocols = protocols
	go func(server *http.Server) {
		if err := server.Serve(feed); err != nil && err != http.ErrServerClosed && err != net.ErrClosed {
			p.logger.Error("HTTP lane error: %v", err)
		}
	}(p.server)
}

// Drops in-flight requests, hijacked relays drain on their own
func (p *httpLane) stop() {
	p.serverMutex.Lock()
	defer p.serverMutex.Unlock()
	if p.server != nil {
		p.server.Close()
		p.server = nil
	}
}

// Checks if this is a WebSocket upgrade request
func isWebSocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// Implements http.Handler for routing requests
func (p *httpLane) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Same normalizer as route keys, trailing dots included
	hostname := normalizeHostname(r.Host)

	// Find the route, empty hostname key is the catch all
	p.routesMutex.RLock()
	route, exists := p.routesMap[hostname]
	if !exists {
		route, exists = p.routesMap[""]
	}
	p.routesMutex.RUnlock()

	if !exists {
		p.logger.Debug("No route found for hostname: %s", hostname)
		http.Error(w, fmt.Sprintf("DiscoPanel routes nothing at %s on this port", hostname), http.StatusBadGateway)
		return
	}

	// Handle WebSocket upgrade separately
	if isWebSocketRequest(r) {
		p.handleWebSocket(w, r, route)
		return
	}

	backendAddr := net.JoinHostPort(route.BackendHost, fmt.Sprintf("%d", route.BackendPort))
	p.proxyFor(route, backendAddr).ServeHTTP(w, r)
}

// Returns the cached reverse proxy for a backend
func (p *httpLane) proxyFor(route *Route, backendAddr string) *httputil.ReverseProxy {
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
	// Panel backend speaks cleartext http2 for agent streams
	if route.OwnerKind == OwnerPanel {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		proxy.Transport = &http.Transport{Protocols: protocols}
	}
	p.proxies[backendAddr] = proxy
	return proxy
}

// Drops cached reverse proxies after route changes
func (p *httpLane) dropProxies() {
	p.proxiesMutex.Lock()
	p.proxies = make(map[string]*httputil.ReverseProxy)
	p.proxiesMutex.Unlock()
}

// Handles WebSocket upgrade requests
func (p *httpLane) handleWebSocket(w http.ResponseWriter, r *http.Request, route *Route) {
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

// Replaces the lane's route table
func (p *httpLane) setRoutes(routes map[string]*Route) {
	p.routesMutex.Lock()
	p.routesMap = routes
	p.routesMutex.Unlock()
	p.dropProxies()
}

// Installs or replaces one route
func (p *httpLane) upsert(route *Route) {
	p.routesMutex.Lock()
	p.routesMap[route.Hostname] = route
	p.routesMutex.Unlock()
	p.dropProxies()
	p.logger.Info("HTTP lane added route: hostname=%s backend=%s:%d", route.Hostname, route.BackendHost, route.BackendPort)
}

// Removes a routing rule
func (p *httpLane) remove(hostname string) {
	p.routesMutex.Lock()
	_, existed := p.routesMap[hostname]
	delete(p.routesMap, hostname)
	p.routesMutex.Unlock()
	if existed {
		p.dropProxies()
		p.logger.Info("HTTP lane removed route: hostname=%s", hostname)
	}
}

// True when the lane serves nothing
func (p *httpLane) empty() bool {
	p.routesMutex.RLock()
	defer p.routesMutex.RUnlock()
	return len(p.routesMap) == 0
}

// Returns a copy of all current routes
func (p *httpLane) routes() []Route {
	p.routesMutex.RLock()
	defer p.routesMutex.RUnlock()

	out := make([]Route, 0, len(p.routesMap))
	for _, v := range p.routesMap {
		out = append(out, *v)
	}
	return out
}
