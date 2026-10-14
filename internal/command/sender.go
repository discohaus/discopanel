package command

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcconsole"

	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/go-rcon"
)

var (
	ErrEmptyCommand   = errors.New("command is required")
	ErrEmptyMessage   = errors.New("message is required")
	ErrServerNotFound = errors.New("server not found")
	ErrNoContainer    = errors.New("server has no container")
	ErrNotRunning     = errors.New("server is not running")
)

// Runtime agent hub's console path, used when RCON cannot serve
type ConsoleAgent interface {
	Connected(serverID string) bool
	SendConsole(ctx context.Context, serverID, command string) error
	SendChat(ctx context.Context, serverID, sender, message string) error
}

type Sender struct {
	store    *storage.Store
	docker   *docker.Client
	config   *config.Config
	agent    ConsoleAgent
	rec      *metrics.Recorder
	streamer *logger.LogStreamer
}

func NewSender(store *storage.Store, dockerClient *docker.Client, cfg *config.Config) *Sender {
	return &Sender{
		store:  store,
		docker: dockerClient,
		config: cfg,
	}
}

// Wires agent hub after construction due to dependency order
func (s *Sender) SetAgent(agent ConsoleAgent) {
	s.agent = agent
}

// Wires ledger and console echo after construction
func (s *Sender) SetJournal(rec *metrics.Recorder, streamer *logger.LogStreamer) {
	s.rec = rec
	s.streamer = streamer
}

// Dials, authenticates, and sends one RCON command bounded by ctx
func SendCommand(ctx context.Context, RCONHost string, RCONPort int, RCONPassword string, command string) (string, error) {
	var dialer net.Dialer
	sock, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(RCONHost, strconv.Itoa(RCONPort)))
	if err != nil {
		return "", fmt.Errorf("failed to establish connection: %w", err)
	}
	// Reads and writes stop at the context deadline
	if deadline, ok := ctx.Deadline(); ok {
		_ = sock.SetDeadline(deadline)
	}
	// Cancellation closes the socket so the reader goroutine ends too
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = sock.Close()
		case <-done:
		}
	}()

	// Failed logins close the socket inside NewConn
	conn, err := rcon.NewConn(sock, RCONPassword, rcon.CharSetLatin1)
	if err != nil {
		return "", ctxOrErr(ctx, err)
	}
	defer conn.Close()

	output, err := conn.SendCommand(command)
	if err != nil {
		return "", ctxOrErr(ctx, err)
	}
	// A dead socket ends the reader silently, treat as failure
	if conn.IsClosed() {
		return "", ctxOrErr(ctx, errors.New("rcon connection closed before the response completed"))
	}
	return output, nil
}

// Prefers the context's own error once it has fired
func ctxOrErr(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	// Socket deadlines fire a hair before the context timer does
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return err
}

// Loads a server and refuses anything not running
func (s *Sender) runningServer(ctx context.Context, serverID string) (*v1.Server, error) {
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, ErrServerNotFound
	}
	if server.ContainerId == "" {
		return nil, ErrNoContainer
	}
	status, err := s.docker.GetContainerStatus(ctx, server.ContainerId)
	if err != nil || (status != v1.ServerStatus_SERVER_STATUS_RUNNING && status != v1.ServerStatus_SERVER_STATUS_UNHEALTHY) {
		return nil, ErrNotRunning
	}
	return server, nil
}

// Gates, echoes, sends, and records one console command
func (s *Sender) Run(ctx context.Context, serverID, cmd string, silent bool) (string, error) {
	if cmd == "" {
		return "", ErrEmptyCommand
	}
	server, err := s.runningServer(ctx, serverID)
	if err != nil {
		return "", err
	}

	commandTime := time.Now()
	if !silent && s.streamer != nil {
		s.streamer.AddCommandEntry(server.Id, cmd, commandTime)
	}

	output, err := s.SendCommand(ctx, server.Id, cmd)
	if err == nil {
		s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_COMMAND_RUN, metrics.Attrs{"command": cmd}, "ran command %q", cmd)
	}
	if !silent && s.streamer != nil && (output != "" || err != nil) {
		s.streamer.AddCommandOutput(server.Id, output, err == nil, commandTime)
	}
	return output, err
}

// Shows a chat line in game without echo or ledger
func (s *Sender) Chat(ctx context.Context, serverID, sender, message string) error {
	if strings.TrimSpace(message) == "" {
		return ErrEmptyMessage
	}
	if sender == "" {
		sender = "Panel"
	}
	server, err := s.runningServer(ctx, serverID)
	if err != nil {
		return err
	}
	// Supervisor tellraw skips RCON and its connection spam
	if s.agent != nil && s.agent.Connected(server.Id) {
		return s.agent.SendChat(ctx, server.Id, sender, message)
	}
	_, err = s.SendCommand(ctx, server.Id, mcconsole.TellrawCommand(sender, message))
	return err
}

// Falls back to agent console, stdin has no captured response
func (s *Sender) sendViaAgent(ctx context.Context, serverID, command string) (string, error) {
	if err := s.agent.SendConsole(ctx, serverID, command); err != nil {
		return "", err
	}
	return "", nil
}

func (s *Sender) SendCommand(ctx context.Context, serverID string, command string) (string, error) {
	server, err := s.store.GetServer(ctx, serverID)

	if err != nil {
		return "", fmt.Errorf("server container not found")
	}
	if server.ContainerId == "" {
		return "", fmt.Errorf("server container not found")
	}

	serverCfg, err := s.store.GetServerProperties(ctx, serverID)
	if err != nil {
		return "", fmt.Errorf("failed to load server config: %w", err)
	}

	agentAvailable := s.agent != nil && s.agent.Connected(serverID)

	if serverCfg.EnableRcon != nil && !*serverCfg.EnableRcon {
		if agentAvailable {
			return s.sendViaAgent(ctx, serverID, command)
		}
		return "", fmt.Errorf("rcon is disabled for this server")
	}

	// Decoded global row backs any unset per server values
	rconPort := 25575
	var rconPassword string
	if global, _, gerr := s.store.GetGlobalSettings(ctx); gerr == nil && global != nil {
		if global.RconPort != nil {
			rconPort = int(*global.RconPort)
		}
		if global.RconPassword != nil {
			rconPassword = *global.RconPassword
		}
	}
	if serverCfg.RconPort != nil {
		rconPort = int(*serverCfg.RconPort)
	}
	if serverCfg.RconPassword != nil {
		rconPassword = *serverCfg.RconPassword
	}

	ip, err := s.docker.ContainerIP(ctx, server.ContainerId)
	if err != nil {
		if agentAvailable {
			return s.sendViaAgent(ctx, serverID, command)
		}
		return "", fmt.Errorf("failed to resolve container ip: %w", err)
	}

	// Run command in dedicated context with timeout
	rconCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, err := SendCommand(rconCtx, ip, rconPort, rconPassword, command)
	if err != nil {
		// RCON preferred but a booting server falls back to stdin
		if agentAvailable {
			return s.sendViaAgent(ctx, serverID, command)
		}
		return "", fmt.Errorf("rcon command failed: %w", err)
	}

	return output, nil
}
