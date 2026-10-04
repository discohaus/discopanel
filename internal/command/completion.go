package command

import (
	"context"
	"fmt"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/events"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcconsole"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Serves cached completion engines for panel servers
type Completion struct {
	engineCache *EngineCache
	store       *storage.Store
	sender      *Sender
	logger      *logger.Logger
	collector   *metrics.Collector
}

func NewCompletion(log *logger.Logger, store *storage.Store, sender *Sender, collector *metrics.Collector, bus *events.Bus) *Completion {
	c := &Completion{
		engineCache: NewEngineCache(),
		logger:      log,
		store:       store,
		sender:      sender,
		collector:   collector,
	}

	if bus != nil {
		bus.Subscribe(func(ctx context.Context, event events.Event) {
			switch event.Type {
			case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START,
				v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
				v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
				v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_DELETE:
				c.engineCache.RemoveEngine(event.ServerId)
			}
		})
	}

	return c
}

// Explains why completion cannot serve, nil when it can
func checkSupport(server *v1.Server) error {
	switch mcconsole.HelpSyntaxFor(server.ModLoader) {
	case mcconsole.HelpSyntaxVanilla:
		if server.McVersion != "" && minecraft.CompareGameVersions(server.McVersion, mcconsole.VanillaHelpMinVersion) < 0 {
			return fmt.Errorf("vanilla completion is only supported for Minecraft version %s or newer (server version: %s)", mcconsole.VanillaHelpMinVersion, server.McVersion)
		}
		return nil
	case mcconsole.HelpSyntaxBukkit:
		return nil
	default:
		return fmt.Errorf("unsupported mod loader: %v", server.ModLoader)
	}
}

func (c *Completion) IsAvailable(ctx context.Context, serverID string) (bool, error) {
	server, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return false, fmt.Errorf("failed to fetch server properties: %w", err)
	}
	return checkSupport(server) == nil, nil
}

func (c *Completion) GetCompletion(ctx context.Context, serverID string, cmd string) ([]*mcconsole.Token, error) {
	engine, ok := c.engineCache.GetEngine(serverID)
	if !ok || engine == nil {
		var err error
		engine, err = c.CreateEngine(ctx, serverID)
		if err != nil {
			c.logger.Warn("Failed to create completion engine: serverId=%s, err=%v", serverID, err)
			return nil, err
		}
		c.engineCache.SetEngine(serverID, engine)
	}

	return engine.GetPredictions(cmd)
}

func (c *Completion) CreateEngine(ctx context.Context, serverID string) (mcconsole.CompletionEngine, error) {
	server, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch server properties: %w", err)
	}
	if err := checkSupport(server); err != nil {
		return nil, err
	}

	// Cached engine outlives the request that built it
	engineCtx := context.WithoutCancel(ctx)
	commands := mcconsole.CommandFunc(func(command string) (string, error) {
		return c.sender.SendCommand(engineCtx, serverID, command)
	})
	players := mcconsole.PlayerListFunc(func() ([]string, error) {
		m := c.collector.GetMetrics(serverID)
		if m == nil {
			return []string{}, nil
		}
		return m.PlayerSample, nil
	})
	return mcconsole.NewCompletionEngine(server.ModLoader, commands, players)
}
