package rbac

import (
	"fmt"
	"strings"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"gorm.io/gorm"
)

// Casbin rows hold canonical enum names, star means any

// Casbin column for a resource
func resourceName(r optionsv1.ResourceType) string {
	if r == optionsv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
		return "*"
	}
	return protometa.Name(r)
}

// Casbin column for an action
func actionName(a optionsv1.ActionType) string {
	if a == optionsv1.ActionType_ACTION_TYPE_UNSPECIFIED {
		return "*"
	}
	return protometa.Name(a)
}

// Permission row from one casbin policy, unspecified means any
func permission(p []string) *v1.Permission {
	perm := &v1.Permission{ObjectId: p[3]}
	if p[1] != "*" {
		perm.Resource, _ = protometa.FromName[optionsv1.ResourceType](p[1])
	}
	if p[2] != "*" {
		perm.Action, _ = protometa.FromName[optionsv1.ActionType](p[2])
	}
	return perm
}

// Wraps Casbin enforcer with convenience methods for RBAC
type Enforcer struct {
	enforcer *casbin.Enforcer
}

// Creates Casbin RBAC enforcer backed by GORM database
func NewEnforcer(db *gorm.DB) (*Enforcer, error) {
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, fmt.Errorf("failed to create casbin adapter: %w", err)
	}

	// RBAC model with resource/action/object_id
	m, err := model.NewModelFromString(`
[request_definition]
r = sub, res, act, obj

[policy_definition]
p = sub, res, act, obj

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = g(r.sub, p.sub) && (p.res == "*" || r.res == p.res) && (p.act == "*" || r.act == p.act) && (p.obj == "*" || r.obj == p.obj)
`)
	if err != nil {
		return nil, fmt.Errorf("failed to create casbin model: %w", err)
	}

	e, err := casbin.NewEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("failed to create casbin enforcer: %w", err)
	}

	if err := e.LoadPolicy(); err != nil {
		return nil, fmt.Errorf("failed to load casbin policy: %w", err)
	}

	return &Enforcer{enforcer: e}, nil
}

// One seeded grant of an action on a resource
type grant struct {
	resource optionsv1.ResourceType
	action   optionsv1.ActionType
}

// Every readable resource, the base browse grant set
func readGrants(resources ...optionsv1.ResourceType) []grant {
	grants := make([]grant, len(resources))
	for i, r := range resources {
		grants[i] = grant{resource: r, action: optionsv1.ActionType_ACTION_TYPE_READ}
	}
	return grants
}

// Ensures default roles have their base permissions
func (e *Enforcer) SeedDefaultPolicies(anonymousEnabled bool) error {
	browse := []optionsv1.ResourceType{
		optionsv1.ResourceType_RESOURCE_TYPE_SERVERS,
		optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES,
		optionsv1.ResourceType_RESOURCE_TYPE_MODS,
		optionsv1.ResourceType_RESOURCE_TYPE_MODPACKS,
		optionsv1.ResourceType_RESOURCE_TYPE_MODULES,
		optionsv1.ResourceType_RESOURCE_TYPE_MODULE_TEMPLATES,
		optionsv1.ResourceType_RESOURCE_TYPE_FILES,
		optionsv1.ResourceType_RESOURCE_TYPE_TASKS,
		optionsv1.ResourceType_RESOURCE_TYPE_PROXY,
	}
	serverOps := []grant{
		{optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_START},
		{optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_STOP},
		{optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_RESTART},
	}

	policies := map[string][]grant{
		"admin": {{}}, // Unspecified pair seeds the star wildcard row
		"user": append(append(readGrants(browse...), serverOps...),
			grant{optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_COMMAND},
		),
		"module": readGrants(
			optionsv1.ResourceType_RESOURCE_TYPE_SERVERS,
			optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES,
			optionsv1.ResourceType_RESOURCE_TYPE_MODPACKS,
		),
		"doctor": append(readGrants(
			optionsv1.ResourceType_RESOURCE_TYPE_SERVERS,
			optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES,
			optionsv1.ResourceType_RESOURCE_TYPE_SETTINGS,
			optionsv1.ResourceType_RESOURCE_TYPE_MODPACKS,
		), serverOps...),
		"anonymous": readGrants(browse...),
	}

	for role, grants := range policies {
		existing, err := e.enforcer.GetFilteredPolicy(0, role)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			continue
		}

		for _, g := range grants {
			if _, err := e.enforcer.AddPolicy(role, resourceName(g.resource), actionName(g.action), "*"); err != nil {
				return err
			}
		}
	}

	return e.enforcer.SavePolicy()
}

// True if any role allows action on resource/object
func (e *Enforcer) Enforce(roles []string, resource optionsv1.ResourceType, action optionsv1.ActionType, objectID string) (bool, error) {
	for _, role := range roles {
		allowed, err := e.enforcer.Enforce(role, resourceName(resource), actionName(action), objectID)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	return false, nil
}

// Returns all permissions currently assigned to role
func (e *Enforcer) GetPermissionsForRole(role string) []*v1.Permission {
	policies, err := e.enforcer.GetFilteredPolicy(0, role)
	if err != nil {
		return nil
	}
	perms := make([]*v1.Permission, 0, len(policies))
	for _, p := range policies {
		if len(p) >= 4 {
			perms = append(perms, permission(p))
		}
	}
	return perms
}

// Replaces all permissions for a role, admin role blocked
func (e *Enforcer) SetPermissionsForRole(role string, perms []*v1.Permission) error {
	// Don't modify admin role
	if strings.ToLower(role) == "admin" {
		return fmt.Errorf("cannot modify admin role permissions")
	}

	// Remove existing permissions
	if _, err := e.enforcer.RemoveFilteredPolicy(0, role); err != nil {
		return err
	}

	// Add new permissions
	for _, p := range perms {
		objectID := p.ObjectId
		if objectID == "" {
			objectID = "*"
		}
		_, err := e.enforcer.AddPolicy(role, resourceName(p.Resource), actionName(p.Action), objectID)
		if err != nil {
			return err
		}
	}

	return e.enforcer.SavePolicy()
}

// Moves a role's policies onto a new name
func (e *Enforcer) RenameRole(oldName, newName string) error {
	if strings.EqualFold(oldName, "admin") || strings.EqualFold(newName, "admin") {
		return fmt.Errorf("cannot modify admin role permissions")
	}
	policies, err := e.enforcer.GetFilteredPolicy(0, oldName)
	if err != nil {
		return err
	}
	if _, err := e.enforcer.RemoveFilteredPolicy(0, oldName); err != nil {
		return err
	}
	for _, p := range policies {
		if len(p) < 4 {
			continue
		}
		if _, err := e.enforcer.AddPolicy(newName, p[1], p[2], p[3]); err != nil {
			return err
		}
	}
	return e.enforcer.SavePolicy()
}

// Maps each role to its permission slices
func (e *Enforcer) GetPermissionMatrix() map[string][]*v1.Permission {
	policies, err := e.enforcer.GetPolicy()
	if err != nil {
		return nil
	}
	matrix := make(map[string][]*v1.Permission)
	for _, p := range policies {
		if len(p) >= 4 {
			matrix[p[0]] = append(matrix[p[0]], permission(p))
		}
	}
	return matrix
}
