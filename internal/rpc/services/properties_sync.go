package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/auth"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/pkg/logger"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"gorm.io/gorm"
)

// Per-object RBAC check, satisfied by rbac.Enforcer
type syncEnforcer interface {
	Enforce(roles []string, resource optionsv1.ResourceType, action optionsv1.ActionType, objectID string) (bool, error)
}

// Restart hooks, satisfied by lifecycle.Manager
type syncLifecycle interface {
	Busy(serverID string) bool
	Restart(ctx context.Context, serverID string) error
}

const (
	// Caps one apply request
	maxSyncServers = 1000
	// Activity source for writes made by a sync
	syncSource = "defaults_sync"
	// Restarts run this many at a time
	syncRestartWorkers = 2
	// Bounds the save loop once detached from the request
	syncApplyTimeout = 5 * time.Minute
	// Bounds one queued restart
	syncRestartTimeout = 30 * time.Minute
)

// Rule based exclusion reasons
const (
	syncReasonSystem       = "Managed by the panel for each server"
	syncReasonEphemeral    = "One-shot action, not a setting"
	syncReasonSecret       = "Secrets are never copied between servers"
	syncReasonPort         = "Ports are unique to each server"
	syncReasonLaunch       = "Launch arguments can run arbitrary code and depend on each server"
	syncReasonJVMFlags     = "JVM flags depend on each server's Java version"
	syncReasonCommands     = "Runs console commands on the server"
	syncReasonAuth         = "Authentication setting that can lock players out or allow impersonation"
	syncReasonUnset        = "No default value set"
	syncReasonUnclassified = "Not reviewed for syncing yet"
)

// Categories describing what a server runs or how it is reached
var syncExcludedCategories = map[string]string{
	"management": "Management endpoint is unique to each server",
	"proxy":      "Proxy routing is unique to each server",
	"curseforge": "Modpack and loader source is unique to each server",
	"modrinth":   "Modpack and loader source is unique to each server",
}

// Fields never synced, keyed by property key
var syncExcludedKeys = map[string]string{
	"uid":                     "Changing it breaks ownership of existing server files",
	"gid":                     "Changing it breaks ownership of existing server files",
	"jmxHost":                 "Host is unique to each server",
	"enableJmx":               "Opens a remote JVM management port",
	"enableAgent":             "Panel agent is chosen per server",
	"jvmOpts":                 syncReasonLaunch,
	"jvmXxOpts":               syncReasonLaunch,
	"jvmDdOpts":               syncReasonLaunch,
	"extraArgs":               syncReasonLaunch,
	"useAikarFlags":           syncReasonJVMFlags,
	"useMeowiceFlags":         syncReasonJVMFlags,
	"useZgcFlags":             syncReasonJVMFlags,
	"useFlareFlags":           syncReasonJVMFlags,
	"useSimdFlags":            syncReasonJVMFlags,
	"customServer":            "Server jar is unique to each server",
	"customJarExec":           "Server jar is unique to each server",
	"customServerProperties":  "Free-form pairs can carry per-server ports",
	"icon":                    "Server icon is unique to each server",
	"overrideIcon":            "Only meaningful with the server's own icon",
	"serverName":              "Server identity",
	"level":                   "Changing it loads a different world",
	"seed":                    "World identity",
	"rconPort":                syncReasonPort,
	"queryPort":               syncReasonPort,
	"enableRcon":              "The panel console, metrics and backups need RCON",
	"rconCmdsStartup":         syncReasonCommands,
	"rconCmdsOnConnect":       syncReasonCommands,
	"rconCmdsFirstConnect":    syncReasonCommands,
	"rconCmdsOnDisconnect":    syncReasonCommands,
	"rconCmdsLastDisconnect":  syncReasonCommands,
	"onlineMode":              syncReasonAuth,
	"enforceSecureProfile":    syncReasonAuth,
	"preventProxyConnections": syncReasonAuth,
	"acceptsTransfers":        "Proxy transfers depend on each server's routing",
}

// Fields reviewed as safe to sync, anything else stays excluded
var syncAllowedKeys = map[string]bool{
	"tz": true,
	// Server
	"motd": true, "offlineMotd": true, "bugReportLink": true, "serverPropertiesEscapeUnicode": true,
	"stopDuration": true, "stopServerAnnounceDelay": true,
	// Game
	"difficulty": true, "mode": true, "pvp": true, "hardcore": true, "forceGamemode": true,
	"allowNether": true, "allowFlight": true, "announcePlayerAchievements": true,
	"enableCommandBlock": true, "snooperEnabled": true, "maxBuildHeight": true,
	"spawnAnimals": true, "spawnMonsters": true, "spawnNpcs": true, "spawnProtection": true,
	"viewDistance": true, "simulationDistance": true, "playerIdleTimeout": true,
	"syncChunkWrites": true, "enableStatus": true, "entityBroadcastRangePercentage": true,
	"functionPermissionLevel": true, "networkCompressionThreshold": true, "opPermissionLevel": true,
	"useNativeTransport": true, "enableQuery": true, "broadcastConsoleToOps": true,
	"hideOnlinePlayers": true, "logIps": true, "maxChainedNeighborUpdates": true,
	"pauseWhenEmptySeconds": true, "rateLimit": true, "statusHeartbeatInterval": true,
	// World
	"maxWorldSize": true, "generateStructures": true, "levelType": true, "generatorSettings": true,
	"regionFileCompression": true,
	// Resource pack
	"resourcePack": true, "resourcePackSha1": true, "resourcePackId": true,
	"resourcePackEnforce": true, "resourcePackPrompt": true,
	// Players
	"ops": true, "enableWhitelist": true, "whitelist": true, "overrideWhitelist": true,
	"enforceWhitelist": true, "broadcastRconToOps": true,
	// Idle handling
	"enableAutopause": true, "autopauseTimeoutEst": true, "autopauseTimeoutInit": true,
	"enableAutostop": true, "autostopTimeoutEst": true, "autostopTimeoutInit": true,
	// Doctor
	"doctorEnabled": true, "doctorMode": true, "doctorInstallDeps": true,
}

// Syncable fields that need an explicit confirmation
var syncSensitiveKeys = map[string]bool{
	"ops": true,
}

// Fields only meaningful together, synced as a unit
var syncLinkedGroups = [][]string{
	{"resourcePack", "resourcePackSha1", "resourcePackId"},
}

// Player list fields merged per list mode
var syncListKeys = map[string]bool{
	"whitelist": true,
	"ops":       true,
}

// One default field eligible for syncing
type syncField struct {
	prop     protometa.Prop
	key      string
	label    string
	category string
	value    string
	set      bool // False for a linked partner the sync clears
	list     bool
}

// Reason a property never syncs, empty when it may
func syncExclusion(p protometa.Prop) string {
	key := p.Field.JSONName()
	switch {
	case p.Meta.System:
		return syncReasonSystem
	case p.Meta.Ephemeral:
		return syncReasonEphemeral
	case p.Meta.Input == "password":
		return syncReasonSecret
	}
	if reason, ok := syncExcludedKeys[key]; ok {
		return reason
	}
	if reason, ok := syncExcludedCategories[p.Meta.Category]; ok {
		return reason
	}
	if !syncAllowedKeys[key] {
		return syncReasonUnclassified
	}
	return ""
}

// Keys synced together with key, excluding itself
func syncPartners(key string) []string {
	for _, group := range syncLinkedGroups {
		if slices.Contains(group, key) {
			return slices.DeleteFunc(slices.Clone(group), func(k string) bool { return k == key })
		}
	}
	return nil
}

// Default value of one field, blank strings and empty lists read as unset
func syncDefault(m protoreflect.Message, p protometa.Prop) (string, bool) {
	value, set := protometa.ScalarString(m, p.Field)
	if !set {
		return "", false
	}
	if p.Field.Kind() == protoreflect.StringKind {
		if syncListKeys[p.Field.JSONName()] && len(splitPlayerList(value)) == 0 {
			return "", false
		}
		if strings.TrimSpace(value) == "" {
			return "", false
		}
	}
	return value, true
}

// Splits defaults into syncable fields and exclusions
func syncableFields(defaults *v1.ServerProperties) ([]syncField, []*v1.DefaultsSyncExclusion) {
	m := defaults.ProtoReflect()
	labels := map[string]string{}
	for _, c := range protometa.Categories(m.Descriptor()) {
		labels[c.Slug] = c.Label
	}
	props := protometa.Props(m.Descriptor())
	setKeys := map[string]bool{}
	for _, p := range props {
		if _, set := syncDefault(m, p); set && syncExclusion(p) == "" {
			setKeys[p.Field.JSONName()] = true
		}
	}

	var fields []syncField
	var excluded []*v1.DefaultsSyncExclusion
	for _, p := range props {
		key := p.Field.JSONName()
		label := p.Meta.Label
		if label == "" {
			label = key
		}
		reason := syncExclusion(p)
		value, set := syncDefault(m, p)
		// An unset partner of a set field syncs as a clear
		partnered := slices.ContainsFunc(syncPartners(key), func(k string) bool { return setKeys[k] })
		if reason == "" && !set && !partnered {
			reason = syncReasonUnset
		}
		if reason != "" {
			excluded = append(excluded, &v1.DefaultsSyncExclusion{Key: key, Label: label, Reason: reason})
			continue
		}
		fields = append(fields, syncField{
			prop:     p,
			key:      key,
			label:    label,
			category: labels[p.Meta.Category],
			value:    value,
			set:      set,
			list:     syncListKeys[key],
		})
	}
	return fields, excluded
}

// Fingerprint of every syncable default, changes when defaults are edited
func syncDefaultsVersion(fields []syncField) string {
	h := sha256.New()
	for _, f := range fields {
		fmt.Fprintf(h, "%s\x00%t\x00%s\x00", f.key, f.set, f.value)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Picks requested fields plus their linked partners, empty keys means all
func selectSyncFields(fields []syncField, excluded []*v1.DefaultsSyncExclusion, keys []string) ([]syncField, error) {
	if len(keys) == 0 {
		return fields, nil
	}
	byKey := make(map[string]syncField, len(fields))
	for _, f := range fields {
		byKey[f.key] = f
	}
	reasons := make(map[string]string, len(excluded))
	for _, e := range excluded {
		reasons[e.Key] = e.Reason
	}
	seen := map[string]bool{}
	var out []syncField
	for _, key := range keys {
		if seen[key] {
			continue
		}
		f, ok := byKey[key]
		if !ok {
			if reason, known := reasons[key]; known {
				return nil, fmt.Errorf("property %s cannot be synced: %s", key, strings.ToLower(reason))
			}
			return nil, fmt.Errorf("unknown property key %s", key)
		}
		seen[key] = true
		out = append(out, f)
		for _, partner := range syncPartners(key) {
			if pf, ok := byKey[partner]; ok && !seen[partner] {
				seen[partner] = true
				out = append(out, pf)
			}
		}
	}
	// Declaration order keeps previews stable
	slices.SortStableFunc(out, func(a, b syncField) int { return a.prop.Field.Index() - b.prop.Field.Index() })
	return out, nil
}

// Splits comma or newline separated player lists
func splitPlayerList(s string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' }) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Merges default entries into a server list, names compare case-insensitively
func mergePlayerList(current, defaults []string, mode v1.ListSyncMode) (merged, added, removed []string) {
	has := func(list []string, name string) bool {
		return slices.ContainsFunc(list, func(e string) bool { return strings.EqualFold(e, name) })
	}
	var base []string
	for _, name := range current {
		if !has(base, name) {
			base = append(base, name)
		}
	}
	// An empty default never wipes a list
	if mode == v1.ListSyncMode_LIST_SYNC_MODE_MIRROR && len(defaults) > 0 {
		for _, name := range base {
			if !has(defaults, name) {
				removed = append(removed, name)
			}
		}
		for _, name := range defaults {
			if !has(merged, name) {
				merged = append(merged, name)
				if !has(base, name) {
					added = append(added, name)
				}
			}
		}
		return merged, added, removed
	}
	merged = base
	for _, name := range defaults {
		if !has(merged, name) {
			merged = append(merged, name)
			added = append(added, name)
		}
	}
	return merged, added, nil
}

// Fields whose server value differs from the default
func diffSyncFields(current *v1.ServerProperties, fields []syncField, mode v1.ListSyncMode) []*v1.DefaultsSyncChange {
	m := current.ProtoReflect()
	var changes []*v1.DefaultsSyncChange
	for _, f := range fields {
		old, set := protometa.ScalarString(m, f.prop.Field)
		switch {
		case !f.set:
			// Linked partner cleared so it matches the default
			if !set {
				continue
			}
			changes = append(changes, &v1.DefaultsSyncChange{Key: f.key, OldValue: old, OldSet: true})
		case f.list:
			merged, added, removed := mergePlayerList(splitPlayerList(old), splitPlayerList(f.value), mode)
			if len(added) == 0 && len(removed) == 0 {
				continue
			}
			changes = append(changes, &v1.DefaultsSyncChange{
				Key:      f.key,
				OldValue: old,
				OldSet:   set,
				NewValue: strings.Join(merged, ","),
				Added:    added,
				Removed:  removed,
			})
		default:
			if set && old == f.value {
				continue
			}
			changes = append(changes, &v1.DefaultsSyncChange{Key: f.key, OldValue: old, OldSet: set, NewValue: f.value})
		}
	}
	return changes
}

// Update map for the normal properties save path
func syncUpdates(changes []*v1.DefaultsSyncChange) map[string]string {
	updates := make(map[string]string, len(changes))
	for _, c := range changes {
		updates[c.Key] = c.NewValue
	}
	return updates
}

// Warns where removed players linger in the server's list files
func syncNotes(current *v1.ServerProperties, changes []*v1.DefaultsSyncChange) []string {
	after := proto.Clone(current).(*v1.ServerProperties)
	if err := applyPropertyUpdates(after, syncUpdates(changes)); err != nil {
		return nil
	}
	var notes []string
	for _, c := range changes {
		if len(c.Removed) == 0 {
			continue
		}
		switch c.Key {
		case "whitelist":
			if after.OverrideWhitelist == nil || !*after.OverrideWhitelist {
				notes = append(notes, "Removed players stay in whitelist.json until Override Whitelist is on or they are removed in game")
			}
		case "ops":
			notes = append(notes, "Removed operators stay in ops.json until they are de-opped in game")
		}
	}
	return notes
}

// Decides how a saved server picks up its new values
func syncRestartPlan(server *v1.Server, busy, restartNow, canRestart bool) (v1.DefaultsSyncRestart, string) {
	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_CREATING, v1.ServerStatus_SERVER_STATUS_PROVISIONING:
		return v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_STALE_START, "A start in progress used the old values, restart the server once it is up"
	case v1.ServerStatus_SERVER_STATUS_STARTING:
		if busy {
			return v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_STALE_START, "A start in progress used the old values, restart the server once it is up"
		}
	}
	if busy {
		return v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_BUSY, "Another action is in progress, restart the server manually once it finishes"
	}
	if !restartsOnSave(server) {
		return v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEXT_START, "Applies on next start"
	}
	if restartNow && canRestart {
		return v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_QUEUED, "Restart queued"
	}
	return v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_NEEDED, "Restart the server to apply"
}

// Restarts servers a few at a time, closes the channel when all finished
func runSyncRestarts(ctx context.Context, lc syncLifecycle, ids []string, workers int, log *logger.Logger) <-chan struct{} {
	done := make(chan struct{})
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range min(workers, len(ids)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				rctx, cancel := context.WithTimeout(ctx, syncRestartTimeout)
				if err := lc.Restart(rctx, id); err != nil {
					log.Error("Failed to restart server %s after defaults sync: %v", id, err)
				}
				cancel()
			}
		}()
	}
	go func() {
		for _, id := range ids {
			jobs <- id
		}
		close(jobs)
		wg.Wait()
		close(done)
	}()
	return done
}

// Server visibility and editability for the calling user
type syncAccess struct {
	visible  bool
	editable bool
}

// Resolves whether the caller may see and update one server
func syncAccessFor(ctx context.Context, enforcer syncEnforcer, server *v1.Server) (syncAccess, error) {
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return syncAccess{}, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	if enforcer == nil {
		return syncAccess{}, connect.NewError(connect.CodeInternal, errors.New("permission checks unavailable"))
	}
	visible, err := enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_SERVERS, optionsv1.ActionType_ACTION_TYPE_READ, server.Id)
	if err != nil {
		return syncAccess{}, connect.NewError(connect.CodeInternal, err)
	}
	editable, err := enforcer.Enforce(user.Roles, optionsv1.ResourceType_RESOURCE_TYPE_SERVER_PROPERTIES, optionsv1.ActionType_ACTION_TYPE_UPDATE, server.Id)
	if err != nil {
		return syncAccess{}, connect.NewError(connect.CodeInternal, err)
	}
	return syncAccess{visible: visible || editable, editable: editable}, nil
}

const syncReasonNoAccess = "You don't have permission to update this server's properties"

// Loads a server's properties, defaults when the row is missing
func (s *PropertiesService) syncCurrentProperties(ctx context.Context, serverID string) (*v1.ServerProperties, error) {
	current, err := s.store.GetServerProperties(ctx, serverID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return s.store.CreateDefaultServerProperties(serverID), nil
	}
	return current, err
}

// Loaded defaults with the requested fields picked out
type syncSelection struct {
	all      []syncField
	selected []syncField
	excluded []*v1.DefaultsSyncExclusion
	version  string
}

// Loads defaults and the requested syncable fields
func (s *PropertiesService) syncFields(ctx context.Context, keys []string) (*syncSelection, error) {
	defaults, _, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		s.log.Error("Failed to get global settings: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get global settings"))
	}
	all, excluded := syncableFields(defaults)
	selected, err := selectSyncFields(all, excluded, keys)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &syncSelection{all: all, selected: selected, excluded: excluded, version: syncDefaultsVersion(all)}, nil
}

func (s *PropertiesService) syncBusy(serverID string) bool {
	return s.restarter != nil && s.restarter.Busy(serverID)
}

// Diffs defaults against every visible server
func (s *PropertiesService) PreviewDefaultsSync(ctx context.Context, req *connect.Request[v1.PreviewDefaultsSyncRequest]) (*connect.Response[v1.PreviewDefaultsSyncResponse], error) {
	msg := req.Msg
	sel, err := s.syncFields(ctx, msg.Keys)
	if err != nil {
		return nil, err
	}

	servers, err := s.store.ListServers(ctx)
	if err != nil {
		s.log.Error("Failed to list servers: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list servers"))
	}

	resp := &v1.PreviewDefaultsSyncResponse{Excluded: sel.excluded, Servers: []*v1.DefaultsSyncServer{}, DefaultsVersion: sel.version}
	for _, f := range sel.all {
		resp.Fields = append(resp.Fields, &v1.DefaultsSyncField{
			Key:          f.key,
			Label:        f.label,
			Category:     f.category,
			DefaultValue: f.value,
			DefaultSet:   f.set,
			List:         f.list,
			Type:         f.prop.Meta.Input,
			Sensitive:    syncSensitiveKeys[f.key],
			Linked:       syncPartners(f.key),
		})
	}

	for _, server := range servers {
		access, err := syncAccessFor(ctx, s.enforcer, server)
		if err != nil {
			return nil, err
		}
		if !access.visible {
			continue
		}
		entry := &v1.DefaultsSyncServer{ServerId: server.Id, ServerName: server.Name}
		if !access.editable {
			entry.SkipReason = syncReasonNoAccess
			resp.Servers = append(resp.Servers, entry)
			continue
		}
		current, err := s.syncCurrentProperties(ctx, server.Id)
		if err != nil {
			s.log.Error("Failed to get properties for server %s: %v", server.Id, err)
			entry.SkipReason = "Failed to read server properties"
			resp.Servers = append(resp.Servers, entry)
			continue
		}
		entry.Selectable = true
		entry.Changes = diffSyncFields(current, sel.selected, msg.ListMode)
		entry.Notes = syncNotes(current, entry.Changes)
		entry.Running = restartsOnSave(server)
		entry.Busy = s.syncBusy(server.Id)
		resp.Servers = append(resp.Servers, entry)
	}

	return connect.NewResponse(resp), nil
}

// Writes the chosen default fields onto the chosen servers
func (s *PropertiesService) ApplyDefaultsSync(ctx context.Context, req *connect.Request[v1.ApplyDefaultsSyncRequest]) (*connect.Response[v1.ApplyDefaultsSyncResponse], error) {
	msg := req.Msg
	if len(msg.Keys) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("choose at least one property"))
	}
	if len(msg.ServerIds) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("choose at least one server"))
	}
	if len(msg.ServerIds) > maxSyncServers {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d servers per request", maxSyncServers))
	}
	if msg.DefaultsVersion == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("defaults version is required, preview the sync first"))
	}
	if auth.GetUserFromContext(ctx) == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}

	// A client disconnect must not half-apply the batch
	by := metrics.SourceFrom(ctx)
	base := metrics.WithSource(detach(ctx), syncSource)
	applyCtx, cancel := context.WithTimeout(base, syncApplyTimeout)
	defer cancel()

	sel, err := s.syncFields(applyCtx, msg.Keys)
	if err != nil {
		return nil, err
	}
	if sel.version != msg.DefaultsVersion {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("defaults changed since the preview, review again"))
	}
	if !msg.ConfirmSensitive {
		for _, f := range sel.selected {
			if syncSensitiveKeys[f.key] {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("syncing %s needs explicit confirmation", f.label))
			}
		}
	}

	ids := make([]string, 0, len(msg.ServerIds))
	for _, id := range msg.ServerIds {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}

	results := make([]*v1.DefaultsSyncResult, 0, len(ids))
	var restarts []string
	applied := 0
	for _, id := range ids {
		result := s.applySyncToServer(applyCtx, id, sel.selected, msg, by)
		results = append(results, result)
		if result.Outcome == v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED {
			applied++
			if result.Restart == v1.DefaultsSyncRestart_DEFAULTS_SYNC_RESTART_QUEUED {
				restarts = append(restarts, id)
			}
		}
	}

	if len(restarts) > 0 {
		runSyncRestarts(metrics.WithTrace(base), s.restarter, restarts, syncRestartWorkers, s.log)
	}
	s.log.Info("Applied default properties to %d of %d servers, %d restarts queued", applied, len(ids), len(restarts))
	return connect.NewResponse(&v1.ApplyDefaultsSyncResponse{Results: results}), nil
}

// Applies the chosen fields to one server, never aborting the batch
func (s *PropertiesService) applySyncToServer(ctx context.Context, id string, fields []syncField, msg *v1.ApplyDefaultsSyncRequest, by string) *v1.DefaultsSyncResult {
	result := &v1.DefaultsSyncResult{ServerId: id}
	fail := func(reason string) *v1.DefaultsSyncResult {
		result.Outcome = v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_FAILED
		result.Reason = reason
		return result
	}
	if err := ctx.Err(); err != nil {
		return fail("Timed out before this server was reached")
	}

	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		result.Outcome = v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_SKIPPED
		result.Reason = "Server not found"
		return result
	}
	access, err := syncAccessFor(ctx, s.enforcer, server)
	if err != nil {
		s.log.Error("Permission check failed for server %s: %v", id, err)
		return fail("Permission check failed")
	}
	if !access.editable {
		// Unreadable servers look missing so names never leak
		result.Outcome = v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_SKIPPED
		result.Reason = "Server not found"
		if access.visible {
			result.ServerName = server.Name
			result.Reason = syncReasonNoAccess
		}
		return result
	}
	result.ServerName = server.Name

	current, err := s.syncCurrentProperties(ctx, server.Id)
	if err != nil {
		s.log.Error("Failed to get properties for server %s: %v", server.Id, err)
		return fail("Failed to read server properties")
	}
	changes := diffSyncFields(current, fields, msg.ListMode)
	if len(changes) == 0 {
		result.Outcome = v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_UNCHANGED
		return result
	}

	// Same save path as a manual edit, restarts are planned below instead
	_, changed, err := s.saveServerProperties(ctx, server, syncUpdates(changes), false, metrics.Attrs{"by": by})
	if err != nil {
		var cerr *connect.Error
		if errors.As(err, &cerr) {
			return fail(cerr.Message())
		}
		return fail(err.Error())
	}
	if !changed {
		result.Outcome = v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_UNCHANGED
		return result
	}
	result.Outcome = v1.DefaultsSyncOutcome_DEFAULTS_SYNC_OUTCOME_APPLIED
	for _, c := range changes {
		result.Keys = append(result.Keys, c.Key)
	}
	result.Restart, result.RestartNote = syncRestartPlan(server, s.syncBusy(server.Id), msg.RestartRunning, s.restarter != nil)
	return result
}
