package command

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/mcconsole"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

func setupTestCompletion(t *testing.T) (*Completion, *db.Store) {
	t.Helper()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "completion_test.db")
	cfg.Database.AutoMigrate = true
	store, err := db.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("Failed to create SQLite store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	comp := NewCompletion(logger.New(), store, nil, nil, nil)
	return comp, store
}

func TestCreateEngine_VersionCheck(t *testing.T) {
	comp, store := setupTestCompletion(t)
	ctx := context.Background()

	tests := []struct {
		name        string
		serverId    string
		modLoader   v1.ModLoader
		mcVersion   string
		expectError bool
	}{
		{
			name:        "Vanilla 1.12.2 -> error",
			serverId:    "srv-vanilla-112",
			modLoader:   v1.ModLoader_MOD_LOADER_VANILLA,
			mcVersion:   "1.12.2",
			expectError: true,
		},
		{
			name:        "Forge 1.7.10 -> error",
			serverId:    "srv-forge-1710",
			modLoader:   v1.ModLoader_MOD_LOADER_FORGE,
			mcVersion:   "1.7.10",
			expectError: true,
		},
		{
			name:        "Fabric 1.13 -> allowed",
			serverId:    "srv-fabric-113",
			modLoader:   v1.ModLoader_MOD_LOADER_FABRIC,
			mcVersion:   "1.13",
			expectError: false,
		},
		{
			name:        "Vanilla 1.20.4 -> allowed",
			serverId:    "srv-vanilla-120",
			modLoader:   v1.ModLoader_MOD_LOADER_VANILLA,
			mcVersion:   "1.20.4",
			expectError: false,
		},
		{
			name:        "Paper 1.12.2 -> allowed (Paper uses paper engine)",
			serverId:    "srv-paper-112",
			modLoader:   v1.ModLoader_MOD_LOADER_PAPER,
			mcVersion:   "1.12.2",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &v1.Server{
				Id:        tt.serverId,
				Name:      tt.name,
				ModLoader: tt.modLoader,
				McVersion: tt.mcVersion,
				Status:    v1.ServerStatus_SERVER_STATUS_STOPPED,
				DataPath:  t.TempDir(),
			}
			if err := store.CreateServer(ctx, server); err != nil {
				t.Fatalf("Failed to seed server: %v", err)
			}

			engine, err := comp.CreateEngine(ctx, tt.serverId)
			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error creating engine for server %s (version %s, loader %v), but got nil", tt.serverId, tt.mcVersion, tt.modLoader)
				}
				if engine != nil {
					t.Errorf("Expected nil engine on error, got %v", engine)
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error creating engine for server %s: %v", tt.serverId, err)
				}
				if engine == nil {
					t.Errorf("Expected engine for server %s, got nil", tt.serverId)
				}
			}
		})
	}
}

func TestIsAvailable(t *testing.T) {
	comp, store := setupTestCompletion(t)
	ctx := context.Background()

	tests := []struct {
		name            string
		serverId        string
		modLoader       v1.ModLoader
		mcVersion       string
		expectAvailable bool
	}{
		{
			name:            "Vanilla 1.12.2 -> not available",
			serverId:        "srv-vanilla-112-avail",
			modLoader:       v1.ModLoader_MOD_LOADER_VANILLA,
			mcVersion:       "1.12.2",
			expectAvailable: false,
		},
		{
			name:            "Forge 1.7.10 -> not available",
			serverId:        "srv-forge-1710-avail",
			modLoader:       v1.ModLoader_MOD_LOADER_FORGE,
			mcVersion:       "1.7.10",
			expectAvailable: false,
		},
		{
			name:            "Fabric 1.13 -> available",
			serverId:        "srv-fabric-113-avail",
			modLoader:       v1.ModLoader_MOD_LOADER_FABRIC,
			mcVersion:       "1.13",
			expectAvailable: true,
		},
		{
			name:            "Vanilla 1.20.4 -> available",
			serverId:        "srv-vanilla-120-avail",
			modLoader:       v1.ModLoader_MOD_LOADER_VANILLA,
			mcVersion:       "1.20.4",
			expectAvailable: true,
		},
		{
			name:            "Paper 1.12.2 -> available (Paper uses paper engine)",
			serverId:        "srv-paper-112-avail",
			modLoader:       v1.ModLoader_MOD_LOADER_PAPER,
			mcVersion:       "1.12.2",
			expectAvailable: true,
		},
		{
			name:            "Unknown mod loader -> not available",
			serverId:        "srv-unknown-avail",
			modLoader:       v1.ModLoader_MOD_LOADER_UNSPECIFIED,
			mcVersion:       "1.20.4",
			expectAvailable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := &v1.Server{
				Id:        tt.serverId,
				Name:      tt.name,
				ModLoader: tt.modLoader,
				McVersion: tt.mcVersion,
				Status:    v1.ServerStatus_SERVER_STATUS_STOPPED,
				DataPath:  t.TempDir(),
			}
			if err := store.CreateServer(ctx, server); err != nil {
				t.Fatalf("Failed to seed server: %v", err)
			}

			available, err := comp.IsAvailable(ctx, tt.serverId)
			if err != nil {
				t.Fatalf("Unexpected error calling IsAvailable: %v", err)
			}
			if available != tt.expectAvailable {
				t.Errorf("Expected IsAvailable=%v for server %s (loader %v, version %s), got %v", tt.expectAvailable, tt.serverId, tt.modLoader, tt.mcVersion, available)
			}
		})
	}
}

// Engine stub that races on a plain map without external locking
type racyEngine struct {
	calls map[string]int
}

func (e *racyEngine) GetPredictions(command string) ([]*mcconsole.Token, error) {
	e.calls[command]++
	return []*mcconsole.Token{{Text: command}}, nil
}

func TestGetCompletion_SerializesEngineAccess(t *testing.T) {
	comp, _ := setupTestCompletion(t)
	engine := &racyEngine{calls: make(map[string]int)}
	comp.engineCache.SetEngine("srv-shared", engine)

	const workers = 32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := "cmd" + strconv.Itoa(i%4)
			tokens, err := comp.GetCompletion(context.Background(), "srv-shared", cmd)
			if err != nil {
				t.Errorf("GetCompletion error: %v", err)
				return
			}
			if len(tokens) != 1 || tokens[0].Text != cmd {
				t.Errorf("GetCompletion(%q) = %v, want one token %q", cmd, tokens, cmd)
			}
		}(i)
	}
	wg.Wait()

	total := 0
	for _, n := range engine.calls {
		total += n
	}
	if total != workers {
		t.Errorf("engine saw %d calls, want %d", total, workers)
	}
}
