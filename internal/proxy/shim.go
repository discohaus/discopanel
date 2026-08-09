package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync"
	"time"

	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcproto"
	"github.com/discohaus/discopanel/pkg/mcproto/session"
	"github.com/discohaus/discopanel/pkg/minecraft"
)

// Login phase budget for one shim client
const shimLoginTimeout = 30 * time.Second

// Runtime shared by every shim session
type ShimRuntime struct {
	auth    *session.ServerAuth
	logger  *logger.Logger
	intents *IntentTable

	hub hubState

	dialMu     sync.Mutex
	puppetDial PuppetDialer
}

// Builds the shim runtime with one shared keypair
func NewShimRuntime(online bool, log *logger.Logger, intents *IntentTable) (*ShimRuntime, error) {
	auth, err := session.NewServerAuth(online)
	if err != nil {
		return nil, err
	}
	return &ShimRuntime{auth: auth, logger: log, intents: intents}, nil
}

// Serves one hub join, mediating or refusing
func (sh *ShimRuntime) serve(s *ListenerSocket, clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket, route Route, login *mcproto.LoginStart, stats *RouteStats) {
	protocol := int32(handshake.ProtocolVersion)

	if route.McProtocol != 0 && protocol == route.McProtocol {
		sh.mediateMatched(s, clientConn, br, handshake, route, login, stats)
		return
	}

	if sh.serveCrossFamily(s, clientConn, br, handshake, route, login, stats) {
		return
	}

	s.kick(clientConn, handshake, kickLobbyVersion(route.McVersion))
}

// Authenticates then splices a version matched client
func (sh *ShimRuntime) mediateMatched(s *ListenerSocket, clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket, route Route, login *mcproto.LoginStart, stats *RouteStats) {
	clientConn.SetDeadline(time.Now().Add(shimLoginTimeout))

	ctx, cancel := context.WithTimeout(s.ctx, shimLoginTimeout)
	result, err := sh.auth.Authenticate(ctx, br, clientConn, int32(handshake.ProtocolVersion), login)
	cancel()
	if err != nil {
		sh.logger.Info("Hub auth refused %s from %s: %v", login.Name, clientConn.RemoteAddr(), err)
		if result != nil {
			kickStream(result.W, handshake, kickAuthFailed())
		} else {
			s.kick(clientConn, handshake, kickAuthFailed())
		}
		return
	}

	backendAddr := route.BackendAddr()
	backendConn, err := dialBackendWithRetry(s.ctx, backendAddr, 10*time.Second)
	if err != nil {
		sh.logger.Error("Hub dial failed for %s: %v", backendAddr, err)
		kickStream(result.W, handshake, kickNotAccepting())
		return
	}
	defer backendConn.Close()

	backendConn.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if route.ProxyProtocol {
		if err := WriteProxyV2Header(backendConn, clientConn.RemoteAddr(), clientConn.LocalAddr()); err != nil {
			sh.logger.Error("Hub proxy header failed for %s: %v", backendAddr, err)
			return
		}
	}

	rewriteHandshakeAddress(handshake, route.BackendPort, route.PreserveHost)
	if err := mcproto.WriteHandshakePacket(backendConn, handshake); err != nil {
		sh.logger.Error("Hub handshake write failed for %s: %v", backendAddr, err)
		return
	}
	if err := login.Replay(backendConn); err != nil {
		sh.logger.Error("Hub login replay failed for %s: %v", backendAddr, err)
		return
	}

	clientConn.SetDeadline(time.Time{})
	backendConn.SetDeadline(time.Time{})
	stats.ActiveConns.Add(1)
	toBackend, toClient := relayStreams(result.R, result.W, clientConn, backendConn)
	stats.BytesToBackend.Add(toBackend)
	stats.BytesToClient.Add(toClient)
	stats.ActiveConns.Add(-1)
}

// Copies both directions through the shim ciphers
func relayStreams(clientR io.Reader, clientW io.Writer, clientConn, backendConn net.Conn) (toBackend, toClient int64) {
	done := make(chan struct{}, 2)
	go func() {
		n, _ := io.Copy(backendConn, clientR)
		toBackend = n
		closeWrite(backendConn)
		done <- struct{}{}
	}()
	go func() {
		n, _ := io.Copy(clientW, backendConn)
		toClient = n
		closeWrite(clientConn)
		done <- struct{}{}
	}()

	<-done
	timer := time.AfterFunc(halfCloseGrace, func() {
		now := time.Now()
		clientConn.SetDeadline(now)
		backendConn.SetDeadline(now)
	})
	<-done
	timer.Stop()
	return toBackend, toClient
}

// Sends a kick over an already wrapped stream
func kickStream(w io.Writer, handshake *mcproto.HandshakePacket, screen minecraft.Text) {
	reason, err := json.Marshal(screen.Render(int(handshake.ProtocolVersion)))
	if err != nil {
		return
	}
	mcproto.WriteLoginDisconnectJSON(w, reason)
}
