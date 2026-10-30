// Sources single mod jars through the indexers
package provisioner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/discohaus/discopanel/pkg/indexers"
	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/protometa"
)

// Indexer registered without the mod sourcing capability
var errNotModSourcer = errors.New("indexer does not source single mods")

// Downloads the first indexer jar declaring modID into stageDir
func (p *Provisioner) SourceMod(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, stageDir, modID, versionRange, dialect string) (string, error) {
	modsDir := minecraft.GetModsPath(server.DataPath, server.ModLoader)
	if modsDir == "" {
		return "", fmt.Errorf("%s does not load mods", server.Name)
	}
	facets := minecraft.DialectFacets(minecraft.ResolveDialects(server.ModLoader, server.DataPath, modsDir))
	if len(facets) == 0 {
		return "", fmt.Errorf("no loader facet resolvable for %s", server.Name)
	}
	q := indexers.ModQuery{ModID: modID, McVersion: server.McVersion, Loaders: facets}

	var errs []error
	for _, info := range indexers.OrderByPackSource(indexers.Indexers(), minecraft.PackSourceFor(server.ModLoader)) {
		path, err := p.sourceFromIndexer(ctx, cfg, info, stageDir, q, versionRange, dialect)
		if err == nil {
			return path, nil
		}
		if errors.Is(err, errNotModSourcer) {
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", info.Name, err))
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("no registered indexer can source mods")
	}
	return "", errors.Join(errs...)
}

// Tries one indexer's candidates, the first gated jar wins
func (p *Provisioner) sourceFromIndexer(ctx context.Context, cfg *v1.ServerProperties, info indexers.IndexerInfo, stageDir string, q indexers.ModQuery, versionRange, dialect string) (string, error) {
	apiKey, err := p.indexerCredential(ctx, cfg, info)
	if err != nil {
		return "", err
	}
	if apiKey == "" && indexers.CredentialRequired(info) {
		return "", fmt.Errorf("no %s configured", info.CredentialProperty)
	}
	ix, err := indexers.NewIndexer(info.Name, apiKey, p.cfg.Server.UserAgent)
	if err != nil {
		return "", err
	}
	src, ok := ix.(indexers.ModSourcer)
	if !ok {
		return "", errNotModSourcer
	}
	candidates, err := src.SourceMod(ctx, q)
	if err != nil {
		return "", err
	}
	var errs []error
	for i := range candidates {
		c := &candidates[i]
		dest := filepath.Join(stageDir, c.FileName)
		var sum *checksum
		if c.HashAlgo != "" && c.HashSum != "" {
			sum = &checksum{algo: c.HashAlgo, value: c.HashSum}
		}
		if err := p.download(ctx, c.URL, dest, sum, nil, nil); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := gateModJar(dest, c.Origin, q.ModID, versionRange, dialect); err != nil {
			errs = append(errs, err)
			continue
		}
		return dest, nil
	}
	if len(errs) == 0 {
		return "", fmt.Errorf("no candidate files for %q", q.ModID)
	}
	return "", errors.Join(errs...)
}

// Indexer key from the server row first, then global settings
func (p *Provisioner) indexerCredential(ctx context.Context, cfg *v1.ServerProperties, info indexers.IndexerInfo) (string, error) {
	if info.CredentialProperty == "" {
		return "", nil
	}
	if cfg != nil {
		m := cfg.ProtoReflect()
		if fd := m.Descriptor().Fields().ByJSONName(info.CredentialProperty); fd != nil {
			if value, set := protometa.ScalarString(m, fd); set && value != "" {
				return value, nil
			}
		}
	}
	return p.store.IndexerCredential(ctx, info.CredentialProperty)
}

// Removes the jar unless it declares modID inside versionRange
func gateModJar(dest, origin, modID, versionRange, dialect string) error {
	meta, err := minecraft.ReadModJar(dest)
	if err != nil || !meta.HasModID(modID) {
		os.Remove(dest)
		return fmt.Errorf("%s does not provide the mod id %q", origin, modID)
	}
	if versionRange != "" && !minecraft.VersionSatisfies(meta.VersionOf(modID), versionRange, dialect) {
		os.Remove(dest)
		return fmt.Errorf("%s ships %s %s, outside required range %s", origin, modID, meta.VersionOf(modID), versionRange)
	}
	return nil
}
