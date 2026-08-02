package services

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	storage "github.com/discohaus/discopanel/internal/db"
	"github.com/discohaus/discopanel/internal/rbac"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/proto/discopanel/v1/discopanelv1connect"
	"github.com/discohaus/discopanel/pkg/protometa"
)

var _ discopanelv1connect.RoleServiceHandler = (*RoleService)(nil)

type RoleService struct {
	store    *storage.Store
	enforcer *rbac.Enforcer
	log      *logger.Logger
}

func NewRoleService(store *storage.Store, enforcer *rbac.Enforcer, log *logger.Logger) *RoleService {
	return &RoleService{
		store:    store,
		enforcer: enforcer,
		log:      log,
	}
}

func (s *RoleService) ListRoles(ctx context.Context, req *connect.Request[v1.ListRolesRequest]) (*connect.Response[v1.ListRolesResponse], error) {
	roles, err := s.store.ListRoles(ctx)
	if err != nil {
		s.log.Error("Failed to list roles: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list roles"))
	}

	protoRoles := make([]*v1.Role, 0, len(roles))
	for _, role := range roles {
		role.Permissions = s.enforcer.GetPermissionsForRole(role.Name)
		protoRoles = append(protoRoles, role)
	}

	return connect.NewResponse(&v1.ListRolesResponse{
		Roles: protoRoles,
	}), nil
}

func (s *RoleService) GetRole(ctx context.Context, req *connect.Request[v1.GetRoleRequest]) (*connect.Response[v1.GetRoleResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role ID is required"))
	}

	role, err := s.store.GetRole(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("role not found"))
	}

	role.Permissions = s.enforcer.GetPermissionsForRole(role.Name)

	return connect.NewResponse(&v1.GetRoleResponse{
		Role: role,
	}), nil
}

func (s *RoleService) CreateRole(ctx context.Context, req *connect.Request[v1.CreateRoleRequest]) (*connect.Response[v1.CreateRoleResponse], error) {
	msg := req.Msg

	if msg.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role name is required"))
	}

	role := &v1.Role{
		Id:          uuid.New().String(),
		Name:        msg.Name,
		Description: msg.Description,
		IsSystem:    false,
		IsDefault:   msg.IsDefault,
	}

	if err := s.store.CreateRole(ctx, role); err != nil {
		s.log.Error("Failed to create role: %v", err)
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("failed to create role"))
	}

	// Set initial permissions if provided
	if len(msg.Permissions) > 0 {
		for _, p := range msg.Permissions {
			if p.ObjectId == "" {
				p.ObjectId = "*"
			}
		}
		if err := s.enforcer.SetPermissionsForRole(role.Name, msg.Permissions); err != nil {
			s.log.Error("Failed to set permissions for role %s: %v", role.Name, err)
		}
	}

	role.Permissions = s.enforcer.GetPermissionsForRole(role.Name)

	return connect.NewResponse(&v1.CreateRoleResponse{
		Role: role,
	}), nil
}

func (s *RoleService) UpdateRole(ctx context.Context, req *connect.Request[v1.UpdateRoleRequest]) (*connect.Response[v1.UpdateRoleResponse], error) {
	msg := req.Msg

	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role ID is required"))
	}

	role, err := s.store.GetRole(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("role not found"))
	}

	if role.IsSystem {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot modify system role"))
	}

	oldName := role.Name
	if msg.Name != nil && *msg.Name != "" {
		role.Name = *msg.Name
	}
	if msg.Description != nil {
		role.Description = *msg.Description
	}
	if msg.IsDefault != nil {
		role.IsDefault = *msg.IsDefault
	}

	if role.Name != oldName {
		// Rename must follow through to assignments and policies
		if err := s.store.RenameRole(ctx, role, oldName); err != nil {
			s.log.Error("Failed to rename role: %v", err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to rename role"))
		}
		if err := s.enforcer.RenameRole(oldName, role.Name); err != nil {
			s.log.Error("Failed to move permissions for renamed role %s: %v", role.Name, err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to move role permissions"))
		}
	} else if err := s.store.UpdateRole(ctx, role); err != nil {
		s.log.Error("Failed to update role: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update role"))
	}

	role.Permissions = s.enforcer.GetPermissionsForRole(role.Name)

	return connect.NewResponse(&v1.UpdateRoleResponse{
		Role: role,
	}), nil
}

func (s *RoleService) DeleteRole(ctx context.Context, req *connect.Request[v1.DeleteRoleRequest]) (*connect.Response[v1.DeleteRoleResponse], error) {
	msg := req.Msg

	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role ID is required"))
	}

	role, err := s.store.GetRole(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("role not found"))
	}

	if role.IsSystem {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("cannot delete system role"))
	}

	// Remove all permissions for this role
	_ = s.enforcer.SetPermissionsForRole(role.Name, nil)

	if err := s.store.DeleteRole(ctx, msg.Id); err != nil {
		s.log.Error("Failed to delete role: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete role"))
	}

	return connect.NewResponse(&v1.DeleteRoleResponse{
		Message: "role deleted",
	}), nil
}

func (s *RoleService) GetPermissionMatrix(ctx context.Context, req *connect.Request[v1.GetPermissionMatrixRequest]) (*connect.Response[v1.GetPermissionMatrixResponse], error) {
	matrix := s.enforcer.GetPermissionMatrix()

	rolePermsMap := make(map[string]*v1.RolePermissions)
	for roleName, perms := range matrix {
		rolePermsMap[roleName] = &v1.RolePermissions{
			Permissions: perms,
		}
	}

	// Build resource_actions from rpc perm annotations
	actionSet := make(map[optionsv1.ResourceType]map[optionsv1.ActionType]bool)
	protometa.RangePerms(func(perm *optionsv1.RpcPerm) {
		if perm.Resource == optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
			return
		}
		if actionSet[perm.Resource] == nil {
			actionSet[perm.Resource] = make(map[optionsv1.ActionType]bool)
		}
		actionSet[perm.Resource][perm.Action] = true
	})
	allActions := protometa.Values[optionsv1.ActionType]()
	protoRA := make([]*v1.ResourceActions, 0, len(actionSet))
	for _, res := range protometa.Values[optionsv1.ResourceType]() {
		acts, ok := actionSet[res]
		if !ok {
			continue
		}
		ra := &v1.ResourceActions{Resource: res}
		for _, a := range allActions {
			if acts[a] {
				ra.Actions = append(ra.Actions, a)
			}
		}
		protoRA = append(protoRA, ra)
	}

	resp := &v1.GetPermissionMatrixResponse{
		ResourceActions: protoRA,
		RolePermissions: rolePermsMap,
	}

	// Populates scopeable objects driven by ProcedurePermissions ObjectIDField
	if req.Msg.IncludeObjects {
		type idName struct{ id, name string }

		// Fetchers keyed by scope source resource
		fetchers := map[optionsv1.ResourceType]func() []idName{
			optionsv1.ResourceType_RESOURCE_TYPE_SERVERS: func() []idName {
				items, err := s.store.ListServers(ctx)
				if err != nil {
					return nil
				}
				out := make([]idName, len(items))
				for i, x := range items {
					out[i] = idName{x.Id, x.Name}
				}
				return out
			},
			optionsv1.ResourceType_RESOURCE_TYPE_MODULES: func() []idName {
				items, err := s.store.ListModules(ctx)
				if err != nil {
					return nil
				}
				out := make([]idName, len(items))
				for i, x := range items {
					out[i] = idName{x.Id, x.Name}
				}
				return out
			},
			optionsv1.ResourceType_RESOURCE_TYPE_MODULE_TEMPLATES: func() []idName {
				items, err := s.store.ListModuleTemplates(ctx)
				if err != nil {
					return nil
				}
				out := make([]idName, len(items))
				for i, x := range items {
					out[i] = idName{x.Id, x.Name}
				}
				return out
			},
			optionsv1.ResourceType_RESOURCE_TYPE_PROXY: func() []idName {
				items, err := s.store.ListProxyListeners(ctx)
				if err != nil {
					return nil
				}
				out := make([]idName, len(items))
				for i, x := range items {
					out[i] = idName{x.Id, x.Name}
				}
				return out
			},
			optionsv1.ResourceType_RESOURCE_TYPE_TASKS: func() []idName {
				items, err := s.store.ListScheduledTasks(ctx)
				if err != nil {
					return nil
				}
				out := make([]idName, len(items))
				for i, x := range items {
					out[i] = idName{x.Id, x.Name}
				}
				return out
			},
			optionsv1.ResourceType_RESOURCE_TYPE_MODPACKS: func() []idName {
				items, _, err := s.store.ListIndexedModpacks(ctx, 0, -1)
				if err != nil {
					return nil
				}
				out := make([]idName, len(items))
				for i, x := range items {
					out[i] = idName{x.Id, x.Name}
				}
				return out
			},
		}

		// Collects needed source resources, fetches each once
		fetched := make(map[optionsv1.ResourceType][]idName)
		allResources := protometa.Values[optionsv1.ResourceType]()
		for _, res := range allResources {
			if source := protometa.ScopeSource(res); source != optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
				if _, done := fetched[source]; !done {
					if fn, ok := fetchers[source]; ok {
						fetched[source] = fn()
					}
				}
			}
		}

		// Emits ScopeableObjects in stable resource order
		var objects []*v1.ScopeableObject
		for _, resource := range allResources {
			source := protometa.ScopeSource(resource)
			if source == optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
				continue
			}
			for _, obj := range fetched[source] {
				objects = append(objects, &v1.ScopeableObject{
					Id:          obj.id,
					Name:        obj.name,
					Resource:    resource,
					ScopeSource: source,
				})
			}
		}
		resp.AvailableObjects = objects
	}

	return connect.NewResponse(resp), nil
}

func (s *RoleService) UpdatePermissions(ctx context.Context, req *connect.Request[v1.UpdatePermissionsRequest]) (*connect.Response[v1.UpdatePermissionsResponse], error) {
	msg := req.Msg

	if msg.RoleName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("role name is required"))
	}

	for _, p := range msg.Permissions {
		if p.ObjectId == "" {
			p.ObjectId = "*"
		}
	}

	if err := s.enforcer.SetPermissionsForRole(msg.RoleName, msg.Permissions); err != nil {
		s.log.Error("Failed to update permissions for role %s: %v", msg.RoleName, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update permissions"))
	}

	return connect.NewResponse(&v1.UpdatePermissionsResponse{
		Message: "permissions updated",
	}), nil
}
