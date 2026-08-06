package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
)

// Context key marking tls terminated requests
type secureConnKey struct{}

// True when the panel itself terminated the request
func panelTerminated(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	secure, _ := r.Context().Value(secureConnKey{}).(bool)
	return secure
}

// Rewrites forwarding identity headers for one outbound hop
func (p *httpLane) stampForwarded(in *http.Request, header http.Header) {
	// Spoofed edge claims die unless the edge is trusted
	if !p.trustedEdge {
		for _, name := range []string{"Forwarded", "X-Real-Ip", "X-Forwarded-Port", "X-Forwarded-Server", "X-Forwarded-Ssl", "Via"} {
			header.Del(name)
		}
	}
	prior := in.Header.Values("X-Forwarded-For")
	switch clientIP, _, err := net.SplitHostPort(in.RemoteAddr); {
	case err != nil:
		header.Del("X-Forwarded-For")
	case p.trustedEdge && len(prior) > 0:
		header.Set("X-Forwarded-For", strings.Join(prior, ", ")+", "+clientIP)
	default:
		header.Set("X-Forwarded-For", clientIP)
	}
	header.Set("X-Forwarded-Host", in.Host)
	switch proto := in.Header.Get("X-Forwarded-Proto"); {
	case panelTerminated(in):
		header.Set("X-Forwarded-Proto", "https")
	case p.trustedEdge && proto != "":
		header.Set("X-Forwarded-Proto", proto)
	default:
		header.Set("X-Forwarded-Proto", "http")
	}
}

// Keys the proxy cache by backend and transport flavor
type proxyKey struct {
	addr string
	h2c  bool
}

// Serves a listener socket http lane by host header
type httpLane struct {
	routesMap    map[string]*Route
	routesMutex  sync.RWMutex
	proxies      map[proxyKey]*httputil.ReverseProxy
	proxiesMutex sync.Mutex
	server       *http.Server
	serverMutex  sync.Mutex
	logger       *logger.Logger
	trustedEdge  bool
	stats        func(serverID string) *RouteStats
}

// Creates the http lane for one socket
func newHTTPLane(log *logger.Logger, trustedEdge bool, stats func(string) *RouteStats) *httpLane {
	return &httpLane{
		routesMap:   make(map[string]*Route),
		proxies:     make(map[proxyKey]*httputil.ReverseProxy),
		logger:      log,
		trustedEdge: trustedEdge,
		stats:       stats,
	}
}

// Counts bytes the backend writes to the client
type countingWriter struct {
	http.ResponseWriter
	n *atomic.Int64
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.n.Add(int64(n))
	return n, err
}

// Lets ResponseController reach flush on the real writer
func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Counts request body bytes headed to the backend
type countingReader struct {
	io.ReadCloser
	n *atomic.Int64
}

func (r *countingReader) Read(b []byte) (int, error) {
	n, err := r.ReadCloser.Read(b)
	r.n.Add(int64(n))
	return n, err
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
	// Terminated conns stamp their requests as secure
	p.server.ConnContext = func(ctx context.Context, c net.Conn) context.Context {
		if rc, ok := c.(*replayConn); ok && rc.secure {
			return context.WithValue(ctx, secureConnKey{}, true)
		}
		return ctx
	}
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
	hostname := normalizeWireHostname(r.Host)

	// Find the route, empty hostname key is the catch all
	p.routesMutex.RLock()
	route, exists := p.routesMap[hostname]
	if !exists {
		route, exists = p.routesMap[""]
	}
	p.routesMutex.RUnlock()

	if !exists {
		p.logger.Debug("No route found for hostname: %s", hostname)
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}

	st := p.stats(route.ServerID)
	st.TotalConns.Add(1)
	st.ActiveConns.Add(1)
	defer st.ActiveConns.Add(-1)

	// Handle WebSocket upgrade separately
	if isWebSocketRequest(r) {
		p.handleWebSocket(w, r, route, st)
		return
	}

	r.Body = &countingReader{ReadCloser: r.Body, n: &st.BytesToBackend}
	p.proxyFor(route).ServeHTTP(&countingWriter{ResponseWriter: w, n: &st.BytesToClient}, r)
}

// Cache key for a route's backend transport
func routeProxyKey(route *Route) proxyKey {
	return proxyKey{addr: route.BackendAddr(), h2c: route.OwnerKind == OwnerPanel}
}

// Returns the cached reverse proxy for a backend
func (p *httpLane) proxyFor(route *Route) *httputil.ReverseProxy {
	key := routeProxyKey(route)

	p.proxiesMutex.Lock()
	defer p.proxiesMutex.Unlock()

	if proxy, ok := p.proxies[key]; ok {
		return proxy
	}

	target := &url.URL{Scheme: "http", Host: key.addr}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Hand rolled, SetXForwarded overwrites the edge scheme
			p.stampForwarded(pr.In, pr.Out.Header)
			pr.Out.Host = pr.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.logger.Error("Proxy error for %s: %v", r.Host, err)
			http.Error(w, "Bad Gateway", http.StatusBadGateway)
		},
	}
	// Panel backend speaks cleartext http2 for agent streams
	if key.h2c {
		protocols := new(http.Protocols)
		protocols.SetUnencryptedHTTP2(true)
		proxy.Transport = &http.Transport{Protocols: protocols}
	}
	p.proxies[key] = proxy
	return proxy
}

// Evicts cached proxies no current route needs
func (p *httpLane) pruneProxies() {
	p.routesMutex.RLock()
	live := make(map[proxyKey]bool, len(p.routesMap))
	for _, route := range p.routesMap {
		live[routeProxyKey(route)] = true
	}
	p.routesMutex.RUnlock()

	p.proxiesMutex.Lock()
	defer p.proxiesMutex.Unlock()
	for key, proxy := range p.proxies {
		if live[key] {
			continue
		}
		delete(p.proxies, key)
		// Custom transports strand conns unless idles close
		if t, ok := proxy.Transport.(*http.Transport); ok && t != nil {
			t.CloseIdleConnections()
		}
	}
}

// Handles WebSocket upgrade requests
func (p *httpLane) handleWebSocket(w http.ResponseWriter, r *http.Request, route *Route, st *RouteStats) {
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
	backendAddr := route.BackendAddr()
	backendConn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
	if err != nil {
		p.logger.Error("WebSocket: Failed to connect to backend %s: %v", backendAddr, err)
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer backendConn.Close()

	// Forward the original HTTP upgrade request to backend
	p.stampForwarded(r, r.Header)
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
		st.BytesToBackend.Add(int64(buffered))
	}

	p.logger.Debug("WebSocket connection established: %s -> %s", r.RemoteAddr, backendAddr)
	st.countRelay(clientConn, backendConn)
}

// Replaces the lane's route table
func (p *httpLane) setRoutes(routes map[string]*Route) {
	p.routesMutex.Lock()
	p.routesMap = routes
	p.routesMutex.Unlock()
	p.pruneProxies()
}

// Removes a routing rule
func (p *httpLane) remove(hostname string) {
	p.routesMutex.Lock()
	_, existed := p.routesMap[hostname]
	delete(p.routesMap, hostname)
	p.routesMutex.Unlock()
	if existed {
		p.pruneProxies()
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
