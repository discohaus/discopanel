// Scans pack servers for newer upstream modpack versions
package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/discohaus/discopanel/pkg/indexers"
	"github.com/discohaus/discopanel/pkg/minecraft"
	optionsv1 "github.com/discohaus/discopanel/pkg/proto/discopanel/options/v1"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Delay before the first upstream version check after boot
const modpackVersionWarmup = 2 * time.Minute

// Checks pack servers slowly, first pass after warmup
func (c *Collector) collectModpackVersionsLoop() {
	defer c.wg.Done()
	warmup := time.NewTimer(modpackVersionWarmup)
	defer warmup.Stop()
	select {
	case <-warmup.C:
	case <-c.stopChan:
		return
	}
	ticker := time.NewTicker(c.collectorConfig.ModpackVersionInterval)
	defer ticker.Stop()
	c.collectModpackVersions()
	for {
		select {
		case <-ticker.C:
			c.collectModpackVersions()
		case <-c.stopChan:
			return
		}
	}
}

// Checks every server once, a failing server is skipped
func (c *Collector) collectModpackVersions() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	servers, err := c.store.ListServers(ctx)
	if err != nil {
		c.log.Debug("modpack version scan skipped: %v", err)
		return
	}
	cache := make(map[string][]*v1.IndexedModpackFile)
	for _, server := range servers {
		if ctx.Err() != nil {
			return
		}
		c.checkModpackVersion(ctx, server, cache)
	}
}

// Records the newest upstream version beyond the installed one
func (c *Collector) checkModpackVersion(ctx context.Context, server *v1.Server, cache map[string][]*v1.IndexedModpackFile) {
	modpack, installed, ok := c.installedModpack(ctx, server)
	if !ok {
		return
	}
	files, err := c.modpackFiles(ctx, modpack, cache)
	if err != nil {
		c.log.Debug("modpack version check skipped for %s: %v", server.Name, err)
		return
	}
	c.recordModpackUpdate(ctx, server, NewerModpackFile(files, installed.id, installed.at))
}

// Installed pack version and when the provisioner wrote it
type installedPack struct {
	id string
	at time.Time
}

// Resolves indexed modpack and installed version, manifest backfills rows
func (c *Collector) installedModpack(ctx context.Context, server *v1.Server) (*v1.IndexedModpack, installedPack, bool) {
	if minecraft.PackSourceFor(server.ModLoader) == optionsv1.PackSource_PACK_SOURCE_UNSPECIFIED {
		return nil, installedPack{}, false
	}
	manifest, err := runtimespec.ReadManifest(server.DataPath)
	if err != nil || manifest == nil || manifest.Modpack == nil || manifest.Modpack.VersionId == "" {
		return nil, installedPack{}, false
	}
	ref := manifest.Modpack
	if ref.Source == optionsv1.PackSource_PACK_SOURCE_ZIP {
		return nil, installedPack{}, false
	}
	installed := installedPack{id: ref.VersionId}
	if manifest.ProvisionedAt != nil {
		installed.at = manifest.ProvisionedAt.AsTime()
	}
	backfill := map[string]any{}
	if ref.VersionId != server.ModpackVersionId {
		server.ModpackVersionId = ref.VersionId
		backfill["modpack_version_id"] = ref.VersionId
	}
	var modpack *v1.IndexedModpack
	if server.ModpackId != "" {
		modpack, _ = c.store.GetIndexedModpack(ctx, server.ModpackId)
	}
	if modpack == nil {
		modpack, _ = c.store.FindIndexedModpack(ctx, indexerNamesFor(ref.Source), ref.Id)
		if modpack == nil {
			return nil, installedPack{}, false
		}
		server.ModpackId = modpack.Id
		backfill["modpack_id"] = modpack.Id
	}
	if len(backfill) > 0 {
		if err := c.store.UpdateServerFields(ctx, server.Id, backfill); err != nil {
			c.log.Debug("modpack link for %s not recorded: %v", server.Name, err)
		}
	}
	return modpack, installed, true
}

// Registered indexers serving one pack platform
func indexerNamesFor(source optionsv1.PackSource) []string {
	var names []string
	for _, info := range indexers.Indexers() {
		if info.PackSource == source {
			names = append(names, info.Name)
		}
	}
	return names
}

// Upstream file list for a modpack, cached per scan
func (c *Collector) modpackFiles(ctx context.Context, modpack *v1.IndexedModpack, cache map[string][]*v1.IndexedModpackFile) ([]*v1.IndexedModpackFile, error) {
	if files, ok := cache[modpack.Id]; ok {
		return files, nil
	}
	idx, err := c.openIndexer(ctx, modpack.Indexer)
	if err != nil {
		return nil, err
	}
	files, err := idx.GetModpackFiles(ctx, modpack.IndexerId, "", "")
	if err != nil {
		return nil, err
	}
	if cache != nil {
		cache[modpack.Id] = files
	}
	return files, nil
}

// Opens a registered indexer with the operator credential
func (c *Collector) openIndexer(ctx context.Context, name string) (indexers.ModpackIndexer, error) {
	info, ok := indexers.LookupIndexer(name)
	if !ok {
		return nil, fmt.Errorf("indexer %q is not registered", name)
	}
	apiKey := ""
	if info.CredentialProperty != "" {
		key, err := c.store.IndexerCredential(ctx, info.CredentialProperty)
		if err != nil {
			return nil, err
		}
		apiKey = key
		if apiKey == "" && indexers.CredentialRequired(info) {
			return nil, fmt.Errorf("indexer %q has no %s credential", name, info.CredentialProperty)
		}
	}
	return indexers.NewIndexer(name, apiKey, c.config.Server.UserAgent)
}

// Newest later file of the same kind in allowed channels
// Deleted upstream installs fall back to the provision time
func NewerModpackFile(files []*v1.IndexedModpackFile, installedID string, installedAt time.Time) *v1.IndexedModpackFile {
	var installed *v1.IndexedModpackFile
	for _, f := range files {
		if f.Id == installedID {
			installed = f
			break
		}
	}
	baseline := installedAt
	serverPack := false
	allowed := map[v1.ReleaseType]bool{v1.ReleaseType_RELEASE_TYPE_RELEASE: true}
	if installed != nil {
		if installed.FileDate == nil {
			return nil
		}
		baseline = installed.FileDate.AsTime()
		serverPack = installed.IsServerPack
		allowed[installed.ReleaseType] = true
	}
	if baseline.IsZero() {
		return nil
	}
	var newest *v1.IndexedModpackFile
	for _, f := range files {
		if f.Id == installedID || f.IsServerPack != serverPack || !allowed[f.ReleaseType] || f.FileDate == nil {
			continue
		}
		if !f.FileDate.AsTime().After(baseline) {
			continue
		}
		if newest == nil || f.FileDate.AsTime().After(newest.FileDate.AsTime()) {
			newest = f
		}
	}
	return newest
}

// Persists a changed advertised update, new ones hit the ledger
func (c *Collector) recordModpackUpdate(ctx context.Context, server *v1.Server, newest *v1.IndexedModpackFile) {
	id, name := "", ""
	var date any
	if newest != nil {
		id, name = newest.Id, newest.DisplayName
		if newest.FileDate != nil {
			date = newest.FileDate.AsTime()
		}
	}
	if id == server.ModpackUpdateVersionId {
		return
	}
	fields := map[string]any{
		"modpack_update_version_id":   id,
		"modpack_update_version_name": name,
		"modpack_update_file_date":    date,
	}
	if err := c.store.UpdateServerFields(ctx, server.Id, fields); err != nil {
		c.log.Debug("modpack update for %s not recorded: %v", server.Name, err)
		return
	}
	server.ModpackUpdateVersionId, server.ModpackUpdateVersionName = id, name
	if id != "" && c.rec != nil {
		c.rec.Record(WithSource(ctx, "version scan"), server.Id, v1.ServerActionKind_SERVER_ACTION_KIND_MODPACK_UPDATE_AVAILABLE,
			Attrs{"version": name, "version_id": id}, "modpack update available: %s", name)
	}
}
