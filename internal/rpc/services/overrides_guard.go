package services

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	"github.com/discohaus/discopanel/internal/auth"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
)

// reservedLabelPrefix marks container labels the panel relies on to find and
// manage its own containers. Users may never set them.
const reservedLabelPrefix = "discopanel."

// validateDockerOverrides checks a server's docker overrides before they are
// stored. Any of these options can hand a container root on the host (the
// panel owns the docker socket), so only administrators may set them:
// privileged mode, added capabilities, host devices, security options, and
// host or container network namespaces. prev is the stored value on update
// (nil on create) so an editor can still save a server an admin configured,
// as long as the dangerous fields are left unchanged.
func validateDockerOverrides(ctx context.Context, am *auth.Manager, dataDir string, next, prev *v1.DockerOverrides) error {
	if next == nil {
		return nil
	}
	for key := range next.GetLabels() {
		if strings.HasPrefix(strings.ToLower(key), reservedLabelPrefix) {
			return connect.NewError(connect.CodeInvalidArgument,
				fmt.Errorf("docker label %q is reserved: keys starting with %q are used internally", key, reservedLabelPrefix))
		}
	}
	if err := validateBindSources(ctx, am, dataDir, next.GetVolumes()); err != nil {
		return err
	}
	if am.Can(ctx, optionsv1.ResourceType_RESOURCE_TYPE_SETTINGS, optionsv1.ActionType_ACTION_TYPE_UPDATE) {
		return nil
	}
	if field := escalatingChange(next, prev); field != "" {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("changing docker %s needs administrator rights", field))
	}
	return nil
}

// escalatingChange names the first host-escalating option that next sets
// differently from prev, or "" when nothing dangerous changed.
func escalatingChange(next, prev *v1.DockerOverrides) string {
	if next.GetPrivileged() && !prev.GetPrivileged() {
		return "privileged mode"
	}
	if addsAny(next.GetCapAdd(), prev.GetCapAdd()) {
		return "added capabilities"
	}
	if addsAny(next.GetDevices(), prev.GetDevices()) {
		return "device mappings"
	}
	if addsAny(next.GetSecurityOpt(), prev.GetSecurityOpt()) {
		return "security options"
	}
	if nm := next.GetNetworkMode(); nm != prev.GetNetworkMode() && hostNamespaceNetwork(nm) {
		return "network mode " + nm
	}
	return ""
}

// hostNamespaceNetwork reports network modes that share another namespace.
func hostNamespaceNetwork(mode string) bool {
	m := strings.ToLower(strings.TrimSpace(mode))
	return m == "host" || strings.HasPrefix(m, "container:")
}

// addsAny reports whether next holds an entry prev does not. Removing or
// reordering entries is always allowed.
func addsAny(next, prev []string) bool {
	for _, v := range next {
		if !slices.Contains(prev, v) {
			return true
		}
	}
	return false
}
