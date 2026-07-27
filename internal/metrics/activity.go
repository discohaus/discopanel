package metrics

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/logger"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Structured details attached to one ledger event
type Attrs map[string]string

type sourceKey struct{}
type traceKey struct{}

// Tags the context with the acting user or subsystem
func WithSource(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, sourceKey{}, source)
}

// Reads the acting party, untagged contexts read as panel
func SourceFrom(ctx context.Context) string {
	if source, ok := ctx.Value(sourceKey{}).(string); ok && source != "" {
		return source
	}
	return "panel"
}

// Stamps a fresh trace id unless one already exists
func WithTrace(ctx context.Context) context.Context {
	if TraceFrom(ctx) != "" {
		return ctx
	}
	return WithTraceID(ctx, newTraceID())
}

// Ties the context to a known operation id
func WithTraceID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, traceKey{}, id)
}

func TraceFrom(ctx context.Context) string {
	if id, ok := ctx.Value(traceKey{}).(string); ok {
		return id
	}
	return ""
}

func newTraceID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// One chokepoint for the per-server activity ledger
// Records every action with context source and trace
type Recorder struct {
	store *storage.Store
	log   *logger.Logger

	mu      sync.RWMutex
	console func(serverID, line string)
}

func NewRecorder(store *storage.Store, log *logger.Logger) *Recorder {
	return &Recorder{store: store, log: log}
}

// Wires the server console echo used by Announce
func (r *Recorder) SetConsoleSink(sink func(serverID, line string)) {
	r.mu.Lock()
	r.console = sink
	r.mu.Unlock()
}

// Writes one ledger event, survives a cancelled caller context
func (r *Recorder) Record(ctx context.Context, serverID string, kind v1.ServerActionKind, attrs Attrs, format string, args ...any) {
	r.record(ctx, serverID, kind, attrs, fmt.Sprintf(format, args...))
}

// Records and echoes the line into the server console
func (r *Recorder) Announce(ctx context.Context, serverID string, kind v1.ServerActionKind, attrs Attrs, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.mu.RLock()
	console := r.console
	r.mu.RUnlock()
	if console != nil {
		console(serverID, SourceFrom(ctx)+": "+msg)
	}
	r.record(ctx, serverID, kind, attrs, msg)
}

func (r *Recorder) record(ctx context.Context, serverID string, kind v1.ServerActionKind, attrs Attrs, msg string) {
	action := &v1.ServerAction{
		ServerId: serverID,
		Source:   SourceFrom(ctx),
		Kind:     kind,
		Message:  msg,
		Attrs:    attrs,
		TraceId:  TraceFrom(ctx),
	}
	if err := r.store.AppendServerAction(context.WithoutCancel(ctx), action); err != nil {
		r.log.Error("activity: ledger append failed for server %s: %v", serverID, err)
	}
}
