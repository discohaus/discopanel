package services

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// Panel setting that replaces minecraft.max_total_memory_mb when present
const memoryQuotaSettingKey = "memory_quota_max_total_mb"

// Sanity ceiling for the cap, 1 PB in MB
const maxMemoryQuotaMB = 1 << 30

// Container limit docker applies to a module without one
const defaultModuleMemoryMB = 512

// Kind of container a quota row belongs to
type memoryOwner string

const (
	memoryOwnerServer memoryOwner = "server"
	memoryOwnerModule memoryOwner = "module"
)

// One server or module in the quota
type memoryKey struct {
	kind memoryOwner
	id   string
}

// One in-flight allocation, keyed by request so concurrent requests for the
// same server or module never replace each other
type memoryHold struct {
	key memoryKey
	mb  int64
}

// MemoryQuota enforces the admin cap on memory allocated across all servers
// and modules. Checks are serialized and every passed check holds its memory
// until the row persists, so concurrent requests cannot share one headroom.
type MemoryQuota struct {
	store       *storage.Store
	cfg         *config.Config
	authManager *auth.Manager
	log         *logger.Logger

	mu      sync.Mutex
	pending map[*memoryHold]struct{}
}

func NewMemoryQuota(store *storage.Store, cfg *config.Config, authManager *auth.Manager, log *logger.Logger) *MemoryQuota {
	return &MemoryQuota{store: store, cfg: cfg, authManager: authManager, log: log, pending: make(map[*memoryHold]struct{})}
}

// Memory a server's container may use in MB. Every server counts, running or
// not, because the cap limits what is allocated. A docker memory override
// above the server memory raises the container limit, so it counts instead.
func allocatedMemoryMB(server *v1.Server) int64 {
	mb := int64(server.GetMemory())
	if limit := server.GetDockerOverrides().GetMemoryLimit(); limit > mb {
		return limit
	}
	return mb
}

// Memory a module's container may use in MB, docker defaults an unset limit
func moduleMemoryMB(module *v1.Module) int64 {
	if module.GetMemory() <= 0 {
		return defaultModuleMemoryMB
	}
	return int64(module.GetMemory())
}

// Sums every allocation except the one at skip
func sumAllocations(rows map[memoryKey]int64, skip memoryKey) int64 {
	var total int64
	for key, mb := range rows {
		if key != skip {
			total += mb
		}
	}
	return total
}

// Rejects requestedMB when it does not fit next to othersMB, 0 cap is unlimited
func checkMemoryQuota(capMB, othersMB, requestedMB int64) error {
	if capMB <= 0 || othersMB+requestedMB <= capMB {
		return nil
	}
	return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(
		"this would allocate %s of the %s memory limit (%s in use by other servers and modules)",
		formatGB(othersMB+requestedMB), formatGB(capMB), formatGB(othersMB)))
}

// Formats MB as GB with at most two decimals, like the memory slider
func formatGB(mb int64) string {
	gb := math.Round(float64(mb)/1024*100) / 100
	return strconv.FormatFloat(gb, 'f', -1, 64) + " GB"
}

// Effective cap in MB, the panel setting wins over the config value
func (q *MemoryQuota) limit(ctx context.Context) (capMB int64, overridden bool, err error) {
	row, err := q.store.GetSystemSetting(ctx, memoryQuotaSettingKey)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return q.cfg.Minecraft.MaxTotalMemoryMB, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	v, err := strconv.ParseInt(row.Value, 10, 64)
	if err != nil || v < 0 {
		return 0, false, fmt.Errorf("invalid %s value %q", memoryQuotaSettingKey, row.Value)
	}
	return v, true, nil
}

// Administrators may exceed the cap, same rule as host bind mounts. With auth
// disabled every caller is the system admin, so the cap binds everyone there
// and going past it means raising the limit in settings.
func (q *MemoryQuota) exempt(ctx context.Context) bool {
	if !q.authManager.IsAnyAuthEnabled() {
		return false
	}
	return q.authManager.Can(ctx, optionsv1.ResourceType_RESOURCE_TYPE_SETTINGS, optionsv1.ActionType_ACTION_TYPE_UPDATE)
}

// Persisted allocation of every server and module
func (q *MemoryQuota) allocations(ctx context.Context) (map[memoryKey]int64, error) {
	servers, err := q.store.ListServers(ctx)
	if err != nil {
		return nil, err
	}
	modules, err := q.store.ListModules(ctx)
	if err != nil {
		return nil, err
	}
	rows := make(map[memoryKey]int64, len(servers)+len(modules))
	for _, server := range servers {
		rows[memoryKey{memoryOwnerServer, server.Id}] = allocatedMemoryMB(server)
	}
	for _, module := range modules {
		rows[memoryKey{memoryOwnerModule, module.Id}] = moduleMemoryMB(module)
	}
	return rows, nil
}

// Checks mb for key against capMB when enforce is set and holds it until the
// returned release runs. A row with pending holds counts at the largest of
// its stored value and those holds, since any of them may be the last write.
func (q *MemoryQuota) hold(listRows func() (map[memoryKey]int64, error), key memoryKey, mb, capMB int64, enforce bool) (func(), error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	rows, err := listRows()
	if err != nil {
		return nil, err
	}
	own := mb
	for h := range q.pending {
		if h.key == key {
			own = max(own, h.mb)
			continue
		}
		rows[h.key] = max(rows[h.key], h.mb)
	}
	if enforce {
		if err := checkMemoryQuota(capMB, sumAllocations(rows, key), own); err != nil {
			return nil, err
		}
	}

	h := &memoryHold{key: key, mb: mb}
	if q.pending == nil {
		q.pending = make(map[*memoryHold]struct{})
	}
	q.pending[h] = struct{}{}
	return func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		delete(q.pending, h)
	}, nil
}

// Enforces the cap for a server or module about to be persisted with mb.
// prevMB is the stored allocation on update and -1 on create; an update that
// does not raise the allocation always passes. Callers run release once the
// row is saved or the request failed.
func (q *MemoryQuota) reserve(ctx context.Context, key memoryKey, mb, prevMB int64) (release func(), err error) {
	if prevMB >= 0 && mb <= prevMB {
		return func() {}, nil
	}
	capMB, _, err := q.limit(ctx)
	if err != nil {
		q.log.Error("Failed to load memory quota: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check memory quota"))
	}
	enforce := capMB > 0 && !q.exempt(ctx)
	release, err = q.hold(func() (map[memoryKey]int64, error) {
		return q.allocations(ctx)
	}, key, mb, capMB, enforce)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return nil, err
		}
		q.log.Error("Failed to list allocations for memory quota: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check memory quota"))
	}
	return release, nil
}

// Holds a server's allocation, see reserve
func (q *MemoryQuota) reserveServer(ctx context.Context, server *v1.Server, prevMB int64) (func(), error) {
	return q.reserve(ctx, memoryKey{memoryOwnerServer, server.Id}, allocatedMemoryMB(server), prevMB)
}

// Holds a module's allocation, see reserve
func (q *MemoryQuota) reserveModule(ctx context.Context, module *v1.Module, prevMB int64) (func(), error) {
	return q.reserve(ctx, memoryKey{memoryOwnerModule, module.Id}, moduleMemoryMB(module), prevMB)
}

// Total memory allocated to module containers in MB
func (q *MemoryQuota) modulesMB(ctx context.Context) (int64, error) {
	modules, err := q.store.ListModules(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, module := range modules {
		total += moduleMemoryMB(module)
	}
	return total, nil
}

// Builds the memory quota view for the settings page
func (s *ServerService) memoryQuota(ctx context.Context) (*v1.GetMemoryQuotaResponse, error) {
	capMB, overridden, err := s.quota.limit(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.quota.allocations(ctx)
	if err != nil {
		return nil, err
	}
	modulesMB, err := s.quota.modulesMB(ctx)
	if err != nil {
		return nil, err
	}
	return &v1.GetMemoryQuotaResponse{
		MaxTotalMb:       capMB,
		ConfigMaxTotalMb: s.config.Minecraft.MaxTotalMemoryMB,
		Overridden:       overridden,
		AllocatedMb:      sumAllocations(rows, memoryKey{}),
		HostTotalMb:      s.hostTotalMB(ctx),
		ModulesMb:        modulesMB,
		AuthDisabled:     !s.authManager.IsAnyAuthEnabled(),
	}, nil
}

// Returns the cap on memory allocated across all servers
func (s *ServerService) GetMemoryQuota(ctx context.Context, req *connect.Request[v1.GetMemoryQuotaRequest]) (*connect.Response[v1.GetMemoryQuotaResponse], error) {
	quota, err := s.memoryQuota(ctx)
	if err != nil {
		s.log.Error("Failed to load memory quota: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load memory quota"))
	}
	return connect.NewResponse(quota), nil
}

// Sets the cap or drops the panel setting to fall back to the config
func (s *ServerService) UpdateMemoryQuota(ctx context.Context, req *connect.Request[v1.UpdateMemoryQuotaRequest]) (*connect.Response[v1.UpdateMemoryQuotaResponse], error) {
	msg := req.Msg
	if !msg.UseConfig && (msg.MaxTotalMb < 0 || msg.MaxTotalMb > maxMemoryQuotaMB) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("memory limit must be between 0 and %d MB", maxMemoryQuotaMB))
	}

	var err error
	if msg.UseConfig {
		err = s.store.DeleteSystemSetting(ctx, memoryQuotaSettingKey)
	} else {
		err = s.store.UpdateSystemSetting(ctx, &v1.SystemSetting{Key: memoryQuotaSettingKey, Value: strconv.FormatInt(msg.MaxTotalMb, 10)})
	}
	if err != nil {
		s.log.Error("Failed to save memory quota: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save memory quota"))
	}

	quota, err := s.memoryQuota(ctx)
	if err != nil {
		s.log.Error("Failed to load memory quota: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load memory quota"))
	}
	return connect.NewResponse(&v1.UpdateMemoryQuotaResponse{Quota: quota}), nil
}
