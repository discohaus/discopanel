package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/discohaus/discopanel/internal/metrics"
	"github.com/discohaus/discopanel/internal/modupdates"
	"github.com/discohaus/discopanel/pkg/files"
	"github.com/discohaus/discopanel/pkg/hub"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Upstream calls for one check share this deadline
const modUpdateCheckTimeout = 90 * time.Second

// One jar download and swap gets this long
const modUpdateApplyTimeout = 5 * time.Minute

// Serialises update and rollback runs per server
type serverLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (l *serverLocks) lock(serverID string) func() {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*sync.Mutex{}
	}
	m, ok := l.locks[serverID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[serverID] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Mods dir of a server, or the error ListMods reports
func modsDirOf(server *v1.Server) (string, error) {
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return "", connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}
	return modsDir, nil
}

// Ids one apply call may name
const maxApplyIDs = 256

// Drops repeats and blanks, a repeated id would back up the fresh jar
// over the real backup
func uniqueIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// A server's jar folders opened as roots. The data dir is mounted into
// the game container, so every entry in it may be planted: the roots keep
// reads, writes and renames from following links out of these folders.
type jarDirs struct {
	modsDir  string
	enabled  *os.Root
	disabled *os.Root
}

func (d *jarDirs) Close() {
	for _, r := range []*os.Root{d.enabled, d.disabled} {
		if r != nil {
			r.Close()
		}
	}
}

// Open folders, for name clash checks
func (d *jarDirs) all() []*os.Root {
	var out []*os.Root
	for _, r := range []*os.Root{d.enabled, d.disabled} {
		if r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (d *jarDirs) rootFor(j modupdates.Jar) *os.Root {
	if j.Enabled {
		return d.enabled
	}
	return d.disabled
}

// Opens the mods dir and its disabled twin, refusing symlinked folders
func openJarDirs(server *v1.Server) (*jarDirs, error) {
	modsDir, err := modsDirOf(server)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(server.DataPath, modsDir)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("mods folder is outside the server"))
	}
	d := &jarDirs{modsDir: modsDir}
	for i, name := range []string{rel, rel + "_disabled"} {
		root, err := openJarDir(server.DataPath, name)
		if err != nil {
			d.Close()
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		if i == 0 {
			d.enabled = root
		} else {
			d.disabled = root
		}
	}
	return d, nil
}

// One folder under the data dir as a root, nil when it does not exist
func openJarDir(dataPath, rel string) (*os.Root, error) {
	full, err := files.ResolveUnder(dataPath, rel)
	if err != nil {
		return nil, fmt.Errorf("%s escapes the server folder", rel)
	}
	info, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is a link or not a folder, refusing to touch it", rel)
	}
	data, err := os.OpenRoot(dataPath)
	if err != nil {
		return nil, err
	}
	defer data.Close()
	return data.OpenRoot(rel)
}

// Every listed jar, an enabled copy hides its disabled twin
func serverJars(serverID string, d *jarDirs, loader v1.ModLoader) (map[string]modupdates.Jar, []string) {
	jars := map[string]modupdates.Jar{}
	var order []string
	for _, scan := range []struct {
		root    *os.Root
		dir     string
		enabled bool
	}{{d.enabled, d.modsDir, true}, {d.disabled, d.modsDir + "_disabled", false}} {
		if scan.root == nil {
			continue
		}
		dir, err := scan.root.Open(".")
		if err != nil {
			continue
		}
		entries, err := dir.ReadDir(-1)
		dir.Close()
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !minecraft.IsValidModFile(e.Name(), loader) {
				continue
			}
			id := modEntryID(serverID, e.Name())
			if _, dup := jars[id]; dup {
				continue
			}
			// Entry info is an lstat, links never pass
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			jars[id] = modupdates.Jar{Root: scan.root, Dir: scan.dir, Name: e.Name(), Enabled: scan.enabled, Size: info.Size(), ModTime: info.ModTime()}
			order = append(order, id)
		}
	}
	return jars, order
}

// Jars a pack or the Modrinth projects setting reinstalls, with the reason.
// Read from the container writable data dir, so it only ever hides updates.
func packManagedJars(server *v1.Server, modsDir string) (map[string]string, bool) {
	managed := map[string]string{}
	modpack := minecraft.LoaderFor(server.ModLoader).Category == optionsv1.ModLoaderCategory_MOD_LOADER_CATEGORY_MODPACK
	rel, err := filepath.Rel(server.DataPath, modsDir)
	if err != nil {
		return managed, modpack
	}
	prefix := filepath.ToSlash(rel) + "/"
	if m, err := runtimespec.ReadManifest(server.DataPath); err == nil && m != nil {
		modpack = modpack || m.Modpack != nil
		for _, f := range m.PackFiles {
			if name, ok := strings.CutPrefix(f, prefix); ok && !strings.Contains(name, "/") {
				managed[name] = "managed by the modpack"
			}
		}
	}
	var state struct {
		Projects map[string]struct {
			FileName string `json:"file_name"`
		} `json:"projects"`
	}
	if data, err := os.ReadFile(filepath.Join(server.DataPath, runtimespec.StateDir, "modrinth-projects.json")); err == nil && json.Unmarshal(data, &state) == nil {
		for _, p := range state.Projects {
			if p.FileName != "" {
				managed[p.FileName] = "managed by the Modrinth projects setting"
			}
		}
	}
	return managed, modpack
}

// Modrinth always, CurseForge with a key or through the index
func (s *ModService) updateSources(ctx context.Context) []modupdates.Source {
	sources := []modupdates.Source{modupdates.NewModrinthSource(s.config.Server.UserAgent)}
	apiKey := ""
	if global, _, err := s.store.GetGlobalSettings(ctx); err == nil && global != nil && global.CfApiKey != nil {
		apiKey = *global.CfApiKey
	}
	if apiKey != "" || hub.IndexEnabled() {
		sources = append(sources, modupdates.NewCurseForgeSource(apiKey, s.config.Server.UserAgent))
	}
	return sources
}

// One graded check over a server's open jar folders
type modUpdateCheck struct {
	dirs    *jarDirs
	jars    map[string]modupdates.Jar
	order   []string
	results map[string]*modupdates.Result
	target  modupdates.Target
	managed map[string]string
	modpack bool
}

// Grades the selected jars, every jar when ids is empty; caller closes dirs
func (s *ModService) checkModUpdates(ctx context.Context, server *v1.Server, includeBeta bool, ids []string) (*modUpdateCheck, error) {
	dirs, err := openJarDirs(server)
	if err != nil {
		return nil, err
	}
	c := &modUpdateCheck{dirs: dirs}
	c.jars, c.order = serverJars(server.Id, dirs, server.ModLoader)
	if len(ids) > 0 {
		c.order = slices.DeleteFunc(c.order, func(id string) bool { return !slices.Contains(ids, id) })
	}
	c.managed, c.modpack = packManagedJars(server, dirs.modsDir)
	c.target = modupdates.Target{
		McVersion:   server.McVersion,
		Loaders:     modupdates.TargetLoaders(server.ModLoader, server.DataPath, dirs.modsDir),
		IncludeBeta: includeBeta,
	}
	list := make([]modupdates.Jar, 0, len(c.order))
	for _, id := range c.order {
		list = append(list, c.jars[id])
	}
	ctx, cancel := context.WithTimeout(ctx, modUpdateCheckTimeout)
	defer cancel()
	results := s.updates.Check(ctx, s.updateSources(ctx), list, c.target)
	c.results = make(map[string]*modupdates.Result, len(results))
	for i, id := range c.order {
		c.results[id] = results[i]
	}
	return c, nil
}

// Private backup store for a server's jar folder
func (s *ModService) backupsFor(server *v1.Server, modsDir string) (modupdates.Backups, error) {
	b, err := modupdates.BackupsFor(s.config.Storage.DataDir, server.Id, modsDir)
	if err != nil {
		return b, connect.NewError(connect.CodeInternal, err)
	}
	return b, nil
}

// Server keeps running the old jars until it restarts
func needsRestart(server *v1.Server) bool {
	switch server.Status {
	case v1.ServerStatus_SERVER_STATUS_STARTING, v1.ServerStatus_SERVER_STATUS_RUNNING,
		v1.ServerStatus_SERVER_STATUS_UNHEALTHY, v1.ServerStatus_SERVER_STATUS_PAUSED:
		return true
	}
	return false
}

func (s *ModService) CheckModUpdates(ctx context.Context, req *connect.Request[v1.CheckModUpdatesRequest]) (*connect.Response[v1.CheckModUpdatesResponse], error) {
	server, err := getServer(ctx, s.store, req.Msg.ServerId)
	if err != nil {
		return nil, err
	}
	c, err := s.checkModUpdates(ctx, server, req.Msg.IncludeBeta, nil)
	if err != nil {
		return nil, err
	}
	defer c.dirs.Close()
	backups, err := s.backupsFor(server, c.dirs.modsDir)
	if err != nil {
		return nil, err
	}
	resp := &v1.CheckModUpdatesResponse{McVersion: c.target.McVersion, Loaders: c.target.Loaders, Modpack: c.modpack}
	for _, id := range c.order {
		u := modUpdateProto(id, c.results[id])
		u.ManagedBy = c.managed[u.FileName]
		if rec, ok := backups.Lookup(u.FileName); ok {
			u.RollbackFileName = rec.Previous
			u.RollbackVersion = rec.FromVersion
		}
		resp.Updates = append(resp.Updates, u)
	}
	return connect.NewResponse(resp), nil
}

func (s *ModService) ApplyModUpdates(ctx context.Context, req *connect.Request[v1.ApplyModUpdatesRequest]) (*connect.Response[v1.ApplyModUpdatesResponse], error) {
	msg := req.Msg
	ids := uniqueIDs(msg.ModIds)
	if len(ids) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("mod_ids is required"))
	}
	if len(ids) > maxApplyIDs {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d mods per request", maxApplyIDs))
	}
	server, err := getServer(ctx, s.store, msg.ServerId)
	if err != nil {
		return nil, err
	}
	defer s.updateLocks.lock(server.Id)()

	// Fresh server side check, the client never names urls or files
	c, err := s.checkModUpdates(ctx, server, msg.IncludeBeta, ids)
	if err != nil {
		return nil, err
	}
	defer c.dirs.Close()
	backups, err := s.backupsFor(server, c.dirs.modsDir)
	if err != nil {
		return nil, err
	}
	resp := &v1.ApplyModUpdatesResponse{RestartRequired: needsRestart(server)}
	for _, id := range ids {
		out := &v1.ModUpdateOutcome{ModId: id}
		resp.Outcomes = append(resp.Outcomes, out)
		r, ok := c.results[id]
		if !ok {
			out.Error = "mod not found"
			continue
		}
		out.FileName = r.Jar.Name
		if reason := c.managed[r.Jar.Name]; reason != "" {
			out.Error = "not updated, " + reason
			continue
		}
		if r.Status != modupdates.StatusAvailable || r.Latest == nil {
			out.Error = "no compatible update available"
			if r.Err != "" {
				out.Error = r.Err
			}
			continue
		}
		newName, err := s.applyOne(ctx, server, c.dirs, r, backups)
		if err != nil {
			s.log.Error("Failed to update %s on %s: %v", r.Jar.Name, server.Id, err)
			out.Error = err.Error()
			if newName == "" {
				continue
			}
		}
		out.Updated = true
		out.NewFileName = newName
		out.NewVersion = r.Latest.Version
	}
	return connect.NewResponse(resp), nil
}

// Downloads, verifies and swaps one jar, then carries its toggle row
func (s *ModService) applyOne(ctx context.Context, server *v1.Server, dirs *jarDirs, r *modupdates.Result, backups modupdates.Backups) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, modUpdateApplyTimeout)
	defer cancel()
	root := dirs.rootFor(r.Jar)
	if root == nil {
		return "", errors.New("jar folder is gone")
	}
	stage, err := os.MkdirTemp(s.config.Storage.TempDir, "mod-update-")
	if err != nil {
		return "", fmt.Errorf("failed to stage the download: %w", err)
	}
	defer os.RemoveAll(stage)
	from := ""
	if r.Current != nil {
		from = r.Current.Version
	}
	newName, err := modupdates.Apply(ctx, root, r.Jar, from, r.Latest, backups, stage, dirs.all(), s.downloader.Fetch)
	if newName == "" {
		return "", err
	}
	s.carryModChoice(ctx, server, r.Jar.Name, newName)
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_UPDATE,
		metrics.Attrs{"file": newName, "from": r.Jar.Name, "version": r.Latest.Version},
		"updated %s to %s", r.Jar.Name, newName)
	return newName, err
}

func (s *ModService) RollbackModUpdate(ctx context.Context, req *connect.Request[v1.RollbackModUpdateRequest]) (*connect.Response[v1.RollbackModUpdateResponse], error) {
	msg := req.Msg
	server, err := getServer(ctx, s.store, msg.ServerId)
	if err != nil {
		return nil, err
	}
	defer s.updateLocks.lock(server.Id)()

	dirs, err := openJarDirs(server)
	if err != nil {
		return nil, err
	}
	defer dirs.Close()
	jars, _ := serverJars(server.Id, dirs, server.ModLoader)
	jar, ok := jars[msg.ModId]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
	}
	backups, err := s.backupsFor(server, dirs.modsDir)
	if err != nil {
		return nil, err
	}
	restored, err := modupdates.Rollback(dirs.rootFor(jar), jar, backups, dirs.all())
	if restored == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	if err != nil {
		s.log.Error("Rollback of %s on %s finished with: %v", jar.Name, server.Id, err)
	}
	s.carryModChoice(ctx, server, jar.Name, restored)
	s.rec.Record(ctx, server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MOD_UPDATE,
		metrics.Attrs{"file": restored, "from": jar.Name, "reason": "rollback"},
		"rolled %s back to %s", jar.Name, restored)
	return connect.NewResponse(&v1.RollbackModUpdateResponse{FileName: restored, RestartRequired: needsRestart(server)}), nil
}

// Moves a stored enable choice onto the replacement file name
func (s *ModService) carryModChoice(ctx context.Context, server *v1.Server, oldName, newName string) {
	if oldName == newName {
		return
	}
	rows, err := s.store.ListServerMods(ctx, server.Id)
	if err != nil {
		s.log.Error("Failed to load mod choices: %v", err)
		return
	}
	for _, row := range rows {
		if row.FileName != oldName {
			continue
		}
		if err := s.store.DeleteMod(ctx, row.Id); err != nil {
			s.log.Error("Failed to clear mod row: %v", err)
		}
		mod := &v1.Mod{
			Id:          modEntryID(server.Id, newName),
			ServerId:    server.Id,
			FileName:    newName,
			DisplayName: modDisplayName(newName),
			Enabled:     row.Enabled,
		}
		if err := s.store.SaveModChoice(ctx, mod); err != nil {
			s.log.Error("Failed to save mod choice for %s: %v", newName, err)
		}
		return
	}
}

func modUpdateProto(id string, r *modupdates.Result) *v1.ModUpdate {
	u := &v1.ModUpdate{
		ModId:       id,
		FileName:    r.Jar.Name,
		Enabled:     r.Jar.Enabled,
		Source:      r.Source,
		ProjectId:   r.ProjectID,
		ProjectName: r.ProjectName,
		ProjectUrl:  webLink(r.ProjectURL),
		Current:     releaseProto(r.Current),
		Latest:      releaseProto(r.Latest),
		Error:       r.Err,
	}
	switch r.Status {
	case modupdates.StatusUpToDate:
		u.Status = v1.ModUpdateStatus_MOD_UPDATE_STATUS_UP_TO_DATE
	case modupdates.StatusAvailable:
		u.Status = v1.ModUpdateStatus_MOD_UPDATE_STATUS_UPDATE_AVAILABLE
	case modupdates.StatusNoCompatible:
		u.Status = v1.ModUpdateStatus_MOD_UPDATE_STATUS_NO_COMPATIBLE_VERSION
	case modupdates.StatusError:
		u.Status = v1.ModUpdateStatus_MOD_UPDATE_STATUS_ERROR
	default:
		u.Status = v1.ModUpdateStatus_MOD_UPDATE_STATUS_UNKNOWN_SOURCE
	}
	return u
}

func releaseProto(r *modupdates.Release) *v1.ModRelease {
	if r == nil {
		return nil
	}
	out := &v1.ModRelease{
		VersionId:    r.VersionID,
		Version:      r.Version,
		FileName:     r.FileName,
		ChangelogUrl: webLink(r.ChangelogURL),
	}
	switch r.Channel {
	case "release":
		out.ReleaseType = v1.ReleaseType_RELEASE_TYPE_RELEASE
	case "beta":
		out.ReleaseType = v1.ReleaseType_RELEASE_TYPE_BETA
	case "alpha":
		out.ReleaseType = v1.ReleaseType_RELEASE_TYPE_ALPHA
	}
	if !r.Published.IsZero() {
		out.PublishedAt = timestamppb.New(r.Published)
	}
	return out
}

// Upstream links reach the browser only as plain web urls
func webLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return raw
}
