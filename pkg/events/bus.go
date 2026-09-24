package events

import (
	"context"
	"sync"

	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// A single server lifecycle event
type Event struct {
	Type     v1.TriggeredEventType
	ServerId string
	Data     map[string]string
}

// Reacts to emitted event, runs serialized on its own queue
type Handler func(ctx context.Context, event Event)

// Queued deliveries tolerated before Emit blocks on a slow handler
const handlerQueueSize = 256

// One queued delivery for a handler
type delivery struct {
	ctx   context.Context
	event Event
}

// One registered handler and its serialized queue
type subscriber struct {
	queue chan delivery
}

// A generic fan-out event dispatcher
type Bus struct {
	log  *logger.Logger
	mu   sync.RWMutex
	subs []*subscriber
}

// Creates an empty event bus
func NewBus(log *logger.Logger) *Bus {
	return &Bus{log: log}
}

// Registers a handler to receive every emitted event in order
func (b *Bus) Subscribe(h Handler) {
	if h == nil {
		return
	}
	sub := &subscriber{queue: make(chan delivery, handlerQueueSize)}
	go sub.run(h, b.log)
	b.mu.Lock()
	b.subs = append(b.subs, sub)
	b.mu.Unlock()
}

// Drains the queue, isolating handler panics from the emitter
func (s *subscriber) run(h Handler, log *logger.Logger) {
	for d := range s.queue {
		deliver(h, d, log)
	}
}

// Runs one handler call behind a panic guard
func deliver(h Handler, d delivery, log *logger.Logger) {
	defer func() {
		if r := recover(); r != nil && log != nil {
			log.Error("event bus: handler panic on %s: %v", d.event.Type, r)
		}
	}()
	h(d.ctx, d.event)
}

// Queues an event for all registered handlers in registration order
func (b *Bus) Emit(ctx context.Context, event Event) {
	b.mu.RLock()
	subs := make([]*subscriber, len(b.subs))
	copy(subs, b.subs)
	b.mu.RUnlock()

	if b.log != nil {
		b.log.Debug("event bus: %s for server %s (%d handlers)", event.Type, event.ServerId, len(subs))
	}

	// Handlers outlive the emitting request, keep values drop cancellation
	d := delivery{ctx: context.WithoutCancel(ctx), event: event}
	for _, s := range subs {
		select {
		case s.queue <- d:
		default:
			if b.log != nil {
				b.log.Warn("event bus: slow subscriber, waiting to deliver %s", event.Type)
			}
			s.queue <- d
		}
	}
}
