package proxy

import (
	"bufio"
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

// Serves one hub join, mirroring or refusing
// Every client rides the mirror whatever its version
func (sh *ShimRuntime) serve(s *ListenerSocket, clientConn net.Conn, br *bufio.Reader, handshake *mcproto.HandshakePacket, route Route, login *mcproto.LoginStart, stats *RouteStats) {
	if sh.mediateHub(s, clientConn, br, handshake, route, login, stats) {
		return
	}
	s.kick(clientConn, handshake, kickLobbyVersion(route.McVersion))
}

// Sends a kick over an already wrapped stream
func kickStream(w io.Writer, handshake *mcproto.HandshakePacket, screen minecraft.Text) {
	reason, err := json.Marshal(screen.Render(int(handshake.ProtocolVersion)))
	if err != nil {
		return
	}
	mcproto.WriteLoginDisconnectJSON(w, reason)
}
