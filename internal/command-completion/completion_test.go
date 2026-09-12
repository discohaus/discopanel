package commandcompletion

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
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
