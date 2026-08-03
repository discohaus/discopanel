// Package proxy routes and relays traffic to backends
package proxy

import (
	"context"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Maps a hostname to a backend server
type Route struct {
	ServerID    string
	Hostname    string
	BackendHost string
	BackendPort int

	// Lane the route serves on its socket
	Protocol v1.ModuleProtocol

	// Attribution for the topology surface
	OwnerKind string
	OwnerID   string
	PortName  string

	// Selects relay, synthetic status, or wake handling
	State v1.ProxyRouteState
	// Lets a login cold-start an offline server
	Wakeable bool
	// Sends a PROXY v2 header before the handshake
	ProxyProtocol bool
	// Keeps the client-sent hostname in the handshake
	PreserveHost bool
	// Synthesized status line for non-online states
	Motd string
	// Fills the synthesized status player cap
	MaxPlayers int
}

// Dial address for the route's backend
func (r *Route) BackendAddr() string {
	return net.JoinHostPort(r.BackendHost, strconv.Itoa(r.BackendPort))
}

// Carries status ping data for a paused server
type SleepingServer struct {
	Motd       string
	MaxPlayers int
}

// Lets the proxy wake paused and stopped servers
type ServerGate interface {
	// Returns sleeping status data when the server is paused
	SleepingInfo(serverID string) (*SleepingServer, bool)
	// Resumes a paused server so a login can proceed
	WakeServer(ctx context.Context, serverID string) error
	// Begins an async cold start for wake logins
	StartServer(serverID string) error
}

// Holds proxy configuration
type Config struct {
	ListenAddr string // Host and port to listen on
	Logger     *logger.Logger
	Gate       ServerGate
	Certs      CertSource
}

const (
	// Bounds how long a client may take to handshake
	handshakeTimeout = 10 * time.Second

	// Short wait for bytes that disambiguate 0xfe openers
	legacyPeekGrace = 250 * time.Millisecond

	// Bounds a single backend connection attempt
	backendDialTimeout = 5 * time.Second

	// Drain window after the first relay direction finishes
	halfCloseGrace = 60 * time.Second
)

// Copies both directions with half-close and drain grace
func relay(client, backend net.Conn) (toBackend, toClient int64) {
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn, count *int64) {
		n, _ := io.Copy(dst, src)
		*count = n
		closeWrite(dst)
		done <- struct{}{}
	}
	go pipe(backend, client, &toBackend)
	go pipe(client, backend, &toClient)

	<-done
	timer := time.AfterFunc(halfCloseGrace, func() {
		now := time.Now()
		client.SetDeadline(now)
		backend.SetDeadline(now)
	})
	<-done
	timer.Stop()
	return toBackend, toClient
}

// Accepts connections and dispatches each to a goroutine
func acceptLoop(ctx context.Context, listener net.Listener, log *logger.Logger, handle func(net.Conn)) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
			}
			log.Error("Failed to accept connection: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		go handle(conn)
	}
}

// Half-closes the write side or fully closes as fallback
func closeWrite(conn net.Conn) {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := conn.(closeWriter); ok {
		cw.CloseWrite()
		return
	}
	conn.Close()
}

// Connects to a backend with timeout and keep-alive
func dialBackend(ctx context.Context, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: backendDialTimeout, KeepAlive: 30 * time.Second}
	return d.DialContext(ctx, "tcp", addr)
}

// Dials a backend with retries until the deadline
func dialBackendWithRetry(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, lastErr
		}
		d := net.Dialer{Timeout: min(backendDialTimeout, remaining), KeepAlive: 30 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			return conn, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
