package command

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"

	"github.com/jltobler/go-rcon"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

var (
	ErrEmptyCommand   = errors.New("command is required")
	ErrServerNotFound = errors.New("server not found")
	ErrNoContainer    = errors.New("server has no container")
	ErrNotRunning     = errors.New("server is not running")
)

// Runtime agent hub's console path, used when RCON cannot serve
type ConsoleAgent interface {
	Connected(serverID string) bool
	SendConsole(ctx context.Context, serverID, command string) error
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

type rconResult struct {
	output string
	err    error
}

func SendCommand(ctx context.Context, RCONHost string, RCONPort int, RCONPassword string, command string) (string, error) {
	// initialize Client
	rconClient := rcon.NewClient(fmt.Sprintf("rcon://%s:%d", RCONHost, RCONPort), RCONPassword)

	// run Command in a goroutine to allow for timeout handling
	resultCh := make(chan rconResult, 1)
	go func() {
		output, sendErr := rconClient.Send(command)
		resultCh <- rconResult{output: output, err: sendErr}
	}()

	// wait for either the command result or a timeout
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-resultCh:
		if result.err != nil {
			return "", result.err
		}
		return result.output, nil
	}
}

// Gates, echoes, sends, and records one console command
func (s *Sender) Run(ctx context.Context, serverID, cmd string, silent bool) (string, error) {
	if cmd == "" {
		return "", ErrEmptyCommand
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return "", ErrServerNotFound
	}
	if server.ContainerId == "" {
		return "", ErrNoContainer
	}
	status, err := s.docker.GetContainerStatus(ctx, server.ContainerId)
	if err != nil || (status != v1.ServerStatus_SERVER_STATUS_RUNNING && status != v1.ServerStatus_SERVER_STATUS_UNHEALTHY) {
		return "", ErrNotRunning
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

	rconPort := 25575
	if v, ok := s.config.Minecraft.GlobalConfig["rconPort"]; ok && v != nil {
		switch t := v.(type) {
		case int:
			rconPort = t
		case int64:
			rconPort = int(t)
		case float64:
			rconPort = int(t)
		case string:
			if p, err := strconv.Atoi(t); err == nil {
				rconPort = p
			}
		}
	}
	if serverCfg.RconPort != nil {
		rconPort = int(*serverCfg.RconPort)
	}

	var rconPassword string
	if v, ok := s.config.Minecraft.GlobalConfig["rconPassword"]; ok && v != nil {
		if p, ok := v.(string); ok {
			rconPassword = p
		} else {
			rconPassword = fmt.Sprint(v)
		}
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
