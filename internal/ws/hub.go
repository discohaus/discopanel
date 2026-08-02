package ws

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/internal/command"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"google.golang.org/protobuf/proto"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second

	// Time allowed to read next pong from peer
	pongWait = 60 * time.Second

	// Send pings to peer, must stay under pongWait
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer
	maxMessageSize = 512 * 1024 // 512KB

	// Cadence of live metrics pushes to subscribed clients
	metricsPushInterval = 5 * time.Second
)

// Manages WebSocket connections and log subscriptions
type Hub struct {
	logStreamer *logger.LogStreamer
	authManager *auth.Manager
	enforcer    *rbac.Enforcer
	store       *storage.Store
	docker      *docker.Client
	log         *logger.Logger
	sender      *command.Sender
	metrics     *metrics.Collector
	rec         *metrics.Recorder

	upgrader websocket.Upgrader

	// Active clients
	clients   map[*Client]bool
	clientsMu sync.RWMutex

	// Register/unregister channels
	register   chan *Client
	unregister chan *Client
}

// Represents a single WebSocket connection
type Client struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	done chan struct{}

	// Authentication
	user          *v1.User
	authenticated bool

	// Maps serverId to its log channel
	subscriptions   map[string]chan *v1.LogEntry
	subscriptionsMu sync.RWMutex

	// Metrics subscriptions by server id
	metricsSubs map[string]bool
}

// Creates a new WebSocket hub
func NewHub(logStreamer *logger.LogStreamer, authManager *auth.Manager, enforcer *rbac.Enforcer, store *storage.Store, docker *docker.Client, sender *command.Sender, metricsCollector *metrics.Collector, rec *metrics.Recorder, log *logger.Logger) *Hub {
	return &Hub{
		logStreamer: logStreamer,
		authManager: authManager,
		enforcer:    enforcer,
		store:       store,
		docker:      docker,
		log:         log,
		sender:      sender,
		metrics:     metricsCollector,
		rec:         rec,
		upgrader: websocket.Upgrader{
			// Same-origin check blocks cross-site hijack, non-browser clients pass through
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" {
					return true
				}
				u, err := url.Parse(origin)
				if err != nil {
					return false
				}
				return strings.EqualFold(u.Host, r.Host)
			},
		},
		clients:    make(map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

// Starts the hub's main loop
func (h *Hub) Run() {
	ticker := time.NewTicker(metricsPushInterval)
	defer ticker.Stop()

	for {
		select {
		case client := <-h.register:
			h.clientsMu.Lock()
			h.clients[client] = true
			h.clientsMu.Unlock()
			h.log.Debug("WebSocket client connected")

		case client := <-h.unregister:
			h.clientsMu.Lock()
			delete(h.clients, client)
			h.clientsMu.Unlock()
			h.log.Debug("WebSocket client disconnected")

		case <-ticker.C:
			h.pushMetrics()
		}
	}
}

// Fans one live sample per server to its clients
func (h *Hub) pushMetrics() {
	if h.metrics == nil {
		return
	}
	h.clientsMu.RLock()
	clients := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		clients = append(clients, c)
	}
	h.clientsMu.RUnlock()

	encoded := make(map[string][]byte)
	for _, c := range clients {
		for _, serverID := range c.metricsSubscriptions() {
			data, seen := encoded[serverID]
			if !seen {
				data = h.encodeMetrics(serverID)
				encoded[serverID] = data
			}
			if data != nil {
				c.sendRaw(data)
			}
		}
	}
}

// Marshals current live sample for one server, nil if down
func (h *Hub) encodeMetrics(serverID string) []byte {
	if !h.metrics.ServerAlive(serverID) {
		return nil
	}
	m := h.metrics.GetMetrics(serverID)
	if m == nil {
		return nil
	}
	msg := &v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_METRICS,
		Payload: &v1.WebSocketServerMessage_Metrics{Metrics: &v1.MetricsMessage{
			ServerId: serverID,
			Sample:   m.Sample(serverID),
		}},
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		h.log.Error("Failed to marshal metrics message: %v", err)
		return nil
	}
	return data
}

// Handles WebSocket upgrade requests
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Error("WebSocket upgrade failed: %v", err)
		return
	}

	client := &Client{
		hub:           h,
		conn:          conn,
		send:          make(chan []byte, 256),
		done:          make(chan struct{}),
		subscriptions: make(map[string]chan *v1.LogEntry),
		metricsSubs:   make(map[string]bool),
	}

	h.register <- client

	// Start read/write pumps
	go client.writePump()
	go client.readPump()
}

// Reads messages from the WebSocket connection
func (c *Client) readPump() {
	defer func() {
		c.cleanup()
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.log.Error("WebSocket read error: %v", err)
			}
			break
		}

		c.handleMessage(message)
	}
}

// Writes messages to the WebSocket connection
func (c *Client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case <-c.done:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			c.conn.WriteMessage(websocket.CloseMessage, []byte{})
			return

		case message := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
				return
			}

		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Processes incoming WebSocket messages
func (c *Client) handleMessage(data []byte) {
	msg := &v1.WebSocketClientMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		c.hub.log.Error("Failed to unmarshal WebSocket message: %v", err)
		c.sendError("invalid message format")
		return
	}

	switch msg.Type {
	case v1.WSMessageType_WS_MESSAGE_TYPE_AUTH:
		c.handleAuth(msg.GetAuth())
	case v1.WSMessageType_WS_MESSAGE_TYPE_SUBSCRIBE:
		c.handleSubscribe(msg.GetSubscribe())
	case v1.WSMessageType_WS_MESSAGE_TYPE_UNSUBSCRIBE:
		c.handleUnsubscribe(msg.GetUnsubscribe())
	case v1.WSMessageType_WS_MESSAGE_TYPE_COMMAND:
		c.handleCommand(msg.GetCommand())
	case v1.WSMessageType_WS_MESSAGE_TYPE_PING:
		c.sendPong()
	default:
		c.sendError("unknown message type")
	}
}

// Authenticates the client
func (c *Client) handleAuth(msg *v1.AuthMessage) {
	if msg == nil {
		c.sendAuthFail("missing auth message")
		return
	}

	// No auth providers enabled, grants full admin access
	if !c.hub.authManager.IsAnyAuthEnabled() {
		c.user = c.hub.authManager.SystemUser()
		c.authenticated = true
		c.sendAuthOk()
		return
	}

	ctx := context.Background()

	if msg.Token != "" {
		var user *v1.User
		var err error
		if strings.HasPrefix(msg.Token, "dp_") {
			user, err = c.hub.authManager.ValidateApiToken(ctx, msg.Token)
		} else {
			user, err = c.hub.authManager.ValidateSession(ctx, msg.Token)
		}
		if err != nil {
			// Presented tokens must validate, anonymous is for tokenless connects
			c.sendAuthFail("invalid token")
			return
		}
		c.user = user
		c.authenticated = true
		c.sendAuthOk()
	} else if c.hub.authManager.IsAnonymousAccessEnabled() {
		c.user = c.hub.authManager.AnonymousUser()
		c.authenticated = true
		c.sendAuthOk()
	} else {
		c.sendAuthFail("authentication required")
	}
}

// Subscribes to server logs
func (c *Client) handleSubscribe(msg *v1.SubscribeMessage) {
	if !c.authenticated {
		c.sendError("not authenticated")
		return
	}

	if msg == nil || msg.ServerId == "" {
		c.sendError("missing server_id")
		return
	}

	// Check permission
	allowed, err := c.hub.enforcer.Enforce(c.user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_READ, msg.ServerId)
	if err != nil || !allowed {
		c.sendError("permission denied")
		return
	}

	// Validate the server exists
	ctx := context.Background()
	server, err := c.hub.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		c.sendError("server not found")
		return
	}

	// A metrics subscription skips the log machinery entirely
	if msg.Metrics {
		c.subscriptionsMu.Lock()
		c.metricsSubs[msg.ServerId] = true
		c.subscriptionsMu.Unlock()
		if data := c.hub.encodeMetrics(msg.ServerId); data != nil {
			c.sendRaw(data)
		}
		c.sendSubscribed(msg.ServerId)
		return
	}

	tail := int(msg.Tail)
	if tail <= 0 {
		tail = 500
	}

	// Subscription keyed by server id, lives for the connection
	c.subscriptionsMu.Lock()
	if _, exists := c.subscriptions[msg.ServerId]; !exists {
		ch := c.hub.logStreamer.Subscribe(msg.ServerId)
		c.subscriptions[msg.ServerId] = ch
		go c.forwardLogs(msg.ServerId, ch)
	}
	c.subscriptionsMu.Unlock()

	// Attach a follow if container exists but none is active
	if server.ContainerId != "" {
		if err := c.hub.logStreamer.StartStreaming(msg.ServerId, server.ContainerId); err != nil {
			c.hub.log.Warn("Failed to start log streaming for server %s: %v", msg.ServerId, err)
		}
	}

	// Send initial logs
	logs := c.hub.logStreamer.GetLogs(msg.ServerId, tail)
	c.sendLogs(msg.ServerId, logs)

	// Confirm subscription
	c.sendSubscribed(msg.ServerId)
}

// Forwards log entries from streamer to the client
func (c *Client) forwardLogs(serverId string, ch chan *v1.LogEntry) {
	for entry := range ch {
		c.sendLog(serverId, entry)
	}
}

// Unsubscribes from server logs or metrics
func (c *Client) handleUnsubscribe(msg *v1.UnsubscribeMessage) {
	if msg == nil || msg.ServerId == "" {
		c.sendError("missing server_id")
		return
	}

	c.subscriptionsMu.Lock()
	if msg.Metrics {
		delete(c.metricsSubs, msg.ServerId)
	} else if ch, exists := c.subscriptions[msg.ServerId]; exists {
		delete(c.subscriptions, msg.ServerId)
		c.hub.logStreamer.Unsubscribe(msg.ServerId, ch)
	}
	c.subscriptionsMu.Unlock()

	c.sendUnsubscribed(msg.ServerId)
}

// Lists the server ids this client wants metrics for
func (c *Client) metricsSubscriptions() []string {
	c.subscriptionsMu.RLock()
	defer c.subscriptionsMu.RUnlock()
	ids := make([]string, 0, len(c.metricsSubs))
	for id := range c.metricsSubs {
		ids = append(ids, id)
	}
	return ids
}

// Executes a command on the server
func (c *Client) handleCommand(msg *v1.CommandMessage) {
	if !c.authenticated {
		c.sendError("not authenticated")
		return
	}

	if msg == nil || msg.ServerId == "" || msg.Command == "" {
		c.sendError("missing server_id or command")
		return
	}

	silent := false
	if msg.Silent != nil {
		silent = *msg.Silent
	}

	// Check command permission
	if c.user != nil {
		allowed, err := c.hub.enforcer.Enforce(c.user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_COMMAND, msg.ServerId)
		if err != nil || !allowed {
			c.sendCommandResult(msg.ServerId, false, "", "permission denied")
			return
		}
	}

	ctx := metrics.WithTrace(context.Background())
	if c.user != nil && c.user.Username != "" {
		ctx = metrics.WithSource(ctx, c.user.Username)
	}

	output, err := c.hub.sender.Run(ctx, msg.ServerId, msg.Command, silent)
	if err != nil {
		c.sendCommandResult(msg.ServerId, false, "", err.Error())
		return
	}

	c.sendCommandResult(msg.ServerId, true, output, "")
}

// Removes all subscriptions when client disconnects
func (c *Client) cleanup() {
	c.subscriptionsMu.Lock()
	defer c.subscriptionsMu.Unlock()

	close(c.done)
	for serverId, ch := range c.subscriptions {
		c.hub.logStreamer.Unsubscribe(serverId, ch)
	}
	c.subscriptions = make(map[string]chan *v1.LogEntry)
	c.metricsSubs = make(map[string]bool)
}

// Queues pre-marshaled bytes, drops when client lags or left
func (c *Client) sendRaw(data []byte) {
	select {
	case <-c.done:
	case c.send <- data:
	default:
		// Channel full, skip
	}
}

// Marshals and sends a server message
func (c *Client) sendMessage(msg *v1.WebSocketServerMessage) {
	data, err := proto.Marshal(msg)
	if err != nil {
		c.hub.log.Error("Failed to marshal WebSocket message: %v", err)
		return
	}

	c.sendRaw(data)
}

func (c *Client) sendAuthOk() {
	userId := ""
	username := ""
	if c.user != nil {
		userId = c.user.Id
		username = c.user.Username
	}
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_AUTH_OK,
		Payload: &v1.WebSocketServerMessage_AuthOk{
			AuthOk: &v1.AuthOkMessage{
				UserId:   userId,
				Username: username,
			},
		},
	})
}

func (c *Client) sendAuthFail(errMsg string) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_AUTH_FAIL,
		Payload: &v1.WebSocketServerMessage_AuthFail{
			AuthFail: &v1.AuthFailMessage{
				Error: errMsg,
			},
		},
	})
}

func (c *Client) sendSubscribed(serverId string) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_SUBSCRIBED,
		Payload: &v1.WebSocketServerMessage_Subscribed{
			Subscribed: &v1.SubscribedMessage{
				ServerId: serverId,
			},
		},
	})
}

func (c *Client) sendUnsubscribed(serverId string) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_UNSUBSCRIBED,
		Payload: &v1.WebSocketServerMessage_Unsubscribed{
			Unsubscribed: &v1.UnsubscribedMessage{
				ServerId: serverId,
			},
		},
	})
}

func (c *Client) sendLogs(serverId string, logs []*v1.LogEntry) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_LOGS,
		Payload: &v1.WebSocketServerMessage_Logs{
			Logs: &v1.LogsMessage{
				ServerId: serverId,
				Logs:     logs,
			},
		},
	})
}

func (c *Client) sendLog(serverId string, log *v1.LogEntry) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_LOG,
		Payload: &v1.WebSocketServerMessage_Log{
			Log: &v1.LogMessage{
				ServerId: serverId,
				Log:      log,
			},
		},
	})
}

func (c *Client) sendCommandResult(serverId string, success bool, output, errMsg string) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_COMMAND_RESULT,
		Payload: &v1.WebSocketServerMessage_CommandResult{
			CommandResult: &v1.CommandResultMessage{
				ServerId: serverId,
				Success:  success,
				Output:   output,
				Error:    errMsg,
			},
		},
	})
}

func (c *Client) sendError(errMsg string) {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_ERROR,
		Payload: &v1.WebSocketServerMessage_Error{
			Error: &v1.ErrorMessage{
				Error: errMsg,
			},
		},
	})
}

func (c *Client) sendPong() {
	c.sendMessage(&v1.WebSocketServerMessage{
		Type: v1.WSMessageType_WS_MESSAGE_TYPE_PONG,
	})
}
