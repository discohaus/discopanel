package services

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/docker"
	"github.com/discohaus/discopanel/internal/lifecycle"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/config"
	"github.com/discohaus/discopanel/pkg/logger"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

var _ discopanelv1connect.PropertiesServiceHandler = (*PropertiesService)(nil)

type PropertiesService struct {
	store     *storage.Store
	config    *config.Config
	docker    *docker.Client
	lifecycle *lifecycle.Manager
	rec       *metrics.Recorder
	log       *logger.Logger
}

// Creates new config service
func NewPropertiesService(store *storage.Store, cfg *config.Config, docker *docker.Client, lifecycleManager *lifecycle.Manager, rec *metrics.Recorder, log *logger.Logger) *PropertiesService {
	return &PropertiesService{
		store:     store,
		config:    cfg,
		docker:    docker,
		lifecycle: lifecycleManager,
		rec:       rec,
		log:       log,
	}
}

// Gets server config
func (s *PropertiesService) GetServerProperties(ctx context.Context, req *connect.Request[v1.GetServerPropertiesRequest]) (*connect.Response[v1.GetServerPropertiesResponse], error) {
	msg := req.Msg

	// Get server to ensure it exists
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Ensure config is synced with server
	if err := s.store.SyncServerPropertiesWithServer(ctx, server); err != nil {
		s.log.Error("Failed to sync server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to sync server properties"))
	}

	// Get the synced config
	config, err := s.store.GetServerProperties(ctx, msg.ServerId)
	if err != nil {
		s.log.Error("Failed to get server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get server properties"))
	}

	// Convert to categorized format
	categories, err := buildPropertyCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format properties"))
	}

	return connect.NewResponse(&v1.GetServerPropertiesResponse{
		Categories: categories,
	}), nil
}

// Updates server config
func (s *PropertiesService) UpdateServerProperties(ctx context.Context, req *connect.Request[v1.UpdateServerPropertiesRequest]) (*connect.Response[v1.UpdateServerPropertiesResponse], error) {
	msg := req.Msg

	// Get server info
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Get existing config
	config, err := s.store.GetServerProperties(ctx, msg.ServerId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			config = s.store.CreateDefaultServerProperties(msg.ServerId)
		} else {
			s.log.Error("Failed to get server config: %v", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get server properties"))
		}
	}

	// Apply updates w/ reflection
	if err := applyPropertyUpdates(config, msg.Updates); err != nil {
		s.log.Error("Failed to apply config updates: %v", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("failed to apply property updates"))
	}

	// Save updated config
	if err := s.store.UpdateServerProperties(ctx, config); err != nil {
		s.log.Error("Failed to save server config: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save server properties"))
	}
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_PROPERTIES_UPDATE, metrics.Attrs{"changed": strconv.Itoa(len(msg.Updates))}, "updated server properties (%d changed)", len(msg.Updates))

	// Restarts running servers so new config applies
	if server.ContainerId != "" && s.lifecycle != nil {
		s.applyPropertiesToRunningServer(ctx, server)
	}

	// Reconciles proxy route right away without server start
	if s.lifecycle != nil {
		s.lifecycle.SyncProxyRoute(ctx, msg.ServerId)
	}

	// Return updated config
	categories, err := buildPropertyCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format properties"))
	}

	return connect.NewResponse(&v1.UpdateServerPropertiesResponse{
		Categories: categories,
	}), nil
}

// Gets global settings
func (s *PropertiesService) GetGlobalSettings(ctx context.Context, req *connect.Request[v1.GetGlobalSettingsRequest]) (*connect.Response[v1.GetGlobalSettingsResponse], error) {
	config, _, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		s.log.Error("Failed to get global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get global settings"))
	}

	categories, err := buildPropertyCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format properties"))
	}

	return connect.NewResponse(&v1.GetGlobalSettingsResponse{
		Categories: categories,
	}), nil
}

// Updates global settings
func (s *PropertiesService) UpdateGlobalSettings(ctx context.Context, req *connect.Request[v1.UpdateGlobalSettingsRequest]) (*connect.Response[v1.UpdateGlobalSettingsResponse], error) {
	msg := req.Msg
	config, _, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		s.log.Error("Failed to get global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get global settings"))
	}

	if err := applyPropertyUpdates(config, msg.Updates); err != nil {
		s.log.Error("Failed to apply config updates: %v", err)
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("failed to apply property updates"))
	}

	if err := s.store.UpdateGlobalSettings(ctx, config); err != nil {
		s.log.Error("Failed to save global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save global settings"))
	}

	categories, err := buildPropertyCategories(config)
	if err != nil {
		s.log.Error("Failed to build config categories: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to format properties"))
	}

	return connect.NewResponse(&v1.UpdateGlobalSettingsResponse{
		Categories: categories,
	}), nil
}

// Restarts running server so saved properties take effect
func (s *PropertiesService) applyPropertiesToRunningServer(reqCtx context.Context, server *v1.Server) {
	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_RUNNING, v1.ServerStatus_SERVER_STATUS_STARTING, v1.ServerStatus_SERVER_STATUS_UNHEALTHY, v1.ServerStatus_SERVER_STATUS_PAUSED:
		go func() {
			ctx, cancel := context.WithTimeout(detach(reqCtx), 30*time.Minute)
			defer cancel()
			if err := s.lifecycle.Restart(ctx, server.Id); err != nil {
				s.log.Error("Failed to restart server %s after config update: %v", server.Name, err)
			}
		}()
	}
}

// Maps updates onto fields by json name
func applyPropertyUpdates(config proto.Message, updates map[string]string) error {
	m := config.ProtoReflect()
	fields := m.Descriptor().Fields()
	for key, strValue := range updates {
		fd := fields.ByJSONName(key)
		if fd == nil {
			continue
		}
		if err := protometa.SetScalarString(m, fd, strValue); err != nil {
			return fmt.Errorf("invalid value for key %s: %v", key, err)
		}
	}
	return nil
}

// Reads one settings field by its property key
func propertyValueByKey(config proto.Message, key string) string {
	m := config.ProtoReflect()
	fd := m.Descriptor().Fields().ByJSONName(key)
	if fd == nil {
		return ""
	}
	value, _ := protometa.ScalarString(m, fd)
	return value
}

func buildPropertyCategories(config proto.Message) ([]*v1.PropertyCategory, error) {
	m := config.ProtoReflect()
	declared := protometa.Categories(m.Descriptor())
	categories := make([]*v1.PropertyCategory, len(declared))
	slugIndex := make(map[string]int, len(declared))
	for i, c := range declared {
		categories[i] = &v1.PropertyCategory{Name: c.Label, Properties: []*v1.ServerProperty{}}
		slugIndex[c.Slug] = i
	}

	for _, p := range protometa.Props(m.Descriptor()) {
		categoryIndex, ok := slugIndex[p.Meta.Category]
		if !ok {
			continue
		}

		key := p.Field.JSONName()
		value, _ := protometa.ScalarString(m, p.Field)

		env := p.Meta.Env
		if env == "" {
			// Falls back to prop key for display
			env = p.Meta.Prop
		}
		label := p.Meta.Label
		if label == "" {
			label = key
		}

		prop := &v1.ServerProperty{
			Key:         key,
			Label:       label,
			Value:       value,
			Type:        p.Meta.Input,
			Description: p.Meta.Desc,
			Required:    p.Meta.Required,
			System:      p.Meta.System,
			Ephemeral:   p.Meta.Ephemeral,
			EnvVar:      env,
		}

		// Only set default when annotation specifies one
		if p.Meta.DefaultValue != "" {
			def := p.Meta.DefaultValue
			prop.DefaultValue = &def
		}

		if p.Meta.Input == "select" {
			prop.Options = p.Meta.Options
			// Pack loader choices derive from the loader registry
			if key == "modrinthLoader" {
				prop.Options = minecraft.PackLoaderNames()
			}
		}

		categories[categoryIndex].Properties = append(categories[categoryIndex].Properties, prop)
	}

	// Filter empty
	var nonEmptyCategories []*v1.PropertyCategory
	for _, cat := range categories {
		if len(cat.Properties) > 0 {
			nonEmptyCategories = append(nonEmptyCategories, cat)
		}
	}

	return nonEmptyCategories, nil
}
