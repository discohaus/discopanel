package commandcompletion

import (
	"context"
	"fmt"

	paper "github.com/discohaus/discopanel/internal/command-completion/paper"
	vanilla "github.com/discohaus/discopanel/internal/command-completion/vanilla"
	db "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/events"
	log "github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"

	"github.com/discohaus/discopanel/internal/command"
	"github.com/discohaus/discopanel/internal/command-completion/engine"
)

type FactoryContext struct {
	CommandProvider    engine.CommandProvider
	PlayerListProvider engine.PlayListProvider
}

var registry = map[v1.ModLoader]func(ctx FactoryContext) engine.CompletionEngine{
	v1.ModLoader_MOD_LOADER_VANILLA: func(ctx FactoryContext) engine.CompletionEngine {
		return vanilla.CreateVanillaEngine(ctx.CommandProvider, ctx.PlayerListProvider)
	},
	v1.ModLoader_MOD_LOADER_PAPER: func(ctx FactoryContext) engine.CompletionEngine {
		return paper.CreatePaperEngine(ctx.CommandProvider)
	},
}

type Completion struct {
	engineCache *EngineCache
	store       *db.Store
	sender      *command.Sender
	logger      *log.Logger
	collector   *metrics.Collector
}

func NewCompletion(logger *log.Logger, store *db.Store, sender *command.Sender, collector *metrics.Collector, bus *events.Bus) *Completion {
	c := &Completion{
		engineCache: NewEngineCache(),
		logger:      logger,
		store:       store,
		sender:      sender,
		collector:   collector,
	}

	bus.Subscribe(func(ctx context.Context, event events.Event) {
		switch event.Type {
		case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START,
			v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART,
			v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
			v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_DELETE:
			c.engineCache.RemoveEngine(event.ServerId)
		}
	})

	return c
}

func (c *Completion) GetCompletion(ctx context.Context, serverID string, cmd string) ([]*engine.Token, error) {
	engine, ok := c.engineCache.GetEngine(serverID)
	if !ok || engine == nil {
		var err error
		engine, err = c.CreateEngine(ctx, serverID)
		if err != nil {
			c.logger.Warn("Failed to create completion engine", "serverId", serverID, "err", err)
			return nil, err
		}
		c.engineCache.SetEngine(serverID, engine)
	}

	return engine.GetPredictions(cmd)
}

type CommandFunc func(command string) (string, error)

func (f CommandFunc) Execute(command string) (string, error) {
	return f(command)
}

type PlayerListFunc func() ([]string, error)

func (f PlayerListFunc) GetPlayers() ([]string, error) {
	return f()
}

func (c *Completion) CreateEngine(ctx context.Context, serverID string) (engine.CompletionEngine, error) {
	props, err := c.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch server properties: %w", err)
	}

	creator, exists := registry[props.ModLoader]
	if !exists {
		return nil, fmt.Errorf("unsupported mod loader: %v", props.ModLoader)
	}

	eng := creator(FactoryContext{
		CommandProvider: CommandFunc(func(command string) (string, error) {
			return c.sender.SendCommand(ctx, serverID, command)
		}),

		PlayerListProvider: PlayerListFunc(func() ([]string, error) {
			m := c.collector.GetMetrics(serverID)
			if m == nil {
				return []string{}, nil
			}
			return m.PlayerSample, nil
		}),
	})

	return eng, nil
}
