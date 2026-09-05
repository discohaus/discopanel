package provisioner

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/discohaus/discopanel/pkg/minecraft"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"golang.org/x/sync/errgroup"
)

// Public FTB modpack api base url
const ftbAPIBase = "https://api.feed-the-beast.com/v1/modpacks/public/modpack"

// Version manifest the FTB modpack api serves
type ftbVersionManifest struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Name    string `json:"name"`
	Targets []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"targets"`
	Files []ftbFile `json:"files"`
}

// One pack file with authoritative side flags
type ftbFile struct {
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	URL        string   `json:"url"`
	Mirrors    []string `json:"mirrors"`
	Sha1       string   `json:"sha1"`
	ClientOnly bool     `json:"clientonly"`
}

// Matches id assignments inside FTB installer scripts
var (
	ftbPackIDRe    = regexp.MustCompile(`pack_id\W{0,3}(\d+)`)
	ftbVersionIDRe = regexp.MustCompile(`version_id\W{0,3}(\d+)`)
)

// Reads FTB installer stub ids out of a pack zip
func ftbInstallerRef(reader *zip.Reader) (int, int, bool) {
	for _, f := range reader.File {
		base := strings.ToLower(path.Base(f.Name))
		if base != "install.sh" && base != "install.bat" {
			continue
		}
		if f.UncompressedSize64 > 1<<20 {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			continue
		}
		if packID, versionID, ok := ftbStubIDs(string(data)); ok {
			return packID, versionID, true
		}
	}
	return 0, 0, false
}

// Reads FTB installer stub ids from the data dir
func ftbInstallerRefDir(dataPath string) (int, int, bool) {
	for _, name := range []string{"install.sh", "install.bat"} {
		info, err := os.Stat(filepath.Join(dataPath, name))
		if err != nil || info.Size() > 1<<20 {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dataPath, name))
		if err != nil {
			continue
		}
		if packID, versionID, ok := ftbStubIDs(string(data)); ok {
			return packID, versionID, true
		}
	}
	return 0, 0, false
}

// Matches FTB ids inside installer script content
func ftbStubIDs(content string) (int, int, bool) {
	content = strings.ToLower(content)
	if !strings.Contains(content, "ftb") {
		return 0, 0, false
	}
	packMatch := ftbPackIDRe.FindStringSubmatch(content)
	versionMatch := ftbVersionIDRe.FindStringSubmatch(content)
	if packMatch == nil || versionMatch == nil {
		return 0, 0, false
	}
	packID, _ := strconv.Atoi(packMatch[1])
	versionID, _ := strconv.Atoi(versionMatch[1])
	if packID <= 0 || versionID <= 0 {
		return 0, 0, false
	}
	return packID, versionID, true
}

// Installs server files straight from the FTB api
func (p *Provisioner) installFTBPack(ctx context.Context, server *v1.Server, cfg *v1.ServerProperties, packID, versionID int, force bool) (*Result, error) {
	manifestURL := fmt.Sprintf("%s/%d/%d", ftbAPIBase, packID, versionID)
	var manifest ftbVersionManifest
	if err := p.getJSON(ctx, manifestURL, &manifest); err != nil {
		return nil, fmt.Errorf("failed to fetch FTB manifest for pack %d version %d: %w", packID, versionID, err)
	}
	if manifest.Status != "" && manifest.Status != "success" {
		return nil, fmt.Errorf("FTB api rejected pack %d version %d: %s", packID, versionID, manifest.Message)
	}

	excludes := p.packExcludes(server, cfg)
	forceIncludes := packForceIncludes(cfg)

	// Resolves wanted files, then downloads concurrently, bounded
	// Client flagged mods still download, the sweep decides
	var pending []ftbFile
	var packFlagged []string
	total := 0
	for _, file := range manifest.Files {
		wanted, clientFlag := ftbFileWanted(&file, excludes, forceIncludes)
		if !wanted {
			p.progress(server, "skipping excluded file %s", file.Name)
			continue
		}
		if clientFlag {
			// Non-mod client files cannot be loader deps
			if filepath.Dir(ftbDest(server.DataPath, &file)) != joinData(server.DataPath, "mods") {
				p.progress(server, "skipping client-only file %s", file.Name)
				continue
			}
			packFlagged = append(packFlagged, strings.ToLower(file.Name))
		}
		total++
		if !force && fileExists(ftbDest(server.DataPath, &file)) {
			continue
		}
		pending = append(pending, file)
	}
	p.progress(server, "installing FTB server files %s: downloading %d files (%d already present)...",
		manifest.Name, len(pending), total-len(pending))

	var done atomic.Int64
	done.Store(int64(total - len(pending)))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(packDownloadConcurrency)
	for _, file := range pending {
		g.Go(func() error {
			if err := p.downloadFTBFile(gctx, &file, ftbDest(server.DataPath, &file)); err != nil {
				return fmt.Errorf("failed to download %q: %w", file.Name, err)
			}
			if n := done.Add(1); n%25 == 0 {
				p.progress(server, "downloaded %d/%d files...", n, total)
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		p.progress(server, "pack downloads complete (%d/%d)", done.Load(), total)
	}

	p.disableClientOnlyMods(ctx, server, forceIncludes, packFlagged)

	return p.installPackRuntime(ctx, server, cfg, ftbEvidence(&manifest))
}

// Tries the primary url then mirrors until one lands
func (p *Provisioner) downloadFTBFile(ctx context.Context, file *ftbFile, dest string) error {
	var sum *checksum
	if file.Sha1 != "" {
		sum = &checksum{algo: "sha1", value: file.Sha1}
	}
	err := fmt.Errorf("file %q has no download url", file.Name)
	for _, u := range append([]string{file.URL}, file.Mirrors...) {
		if u == "" {
			continue
		}
		if err = p.download(ctx, u, dest, sum, nil, nil); err == nil {
			return nil
		}
	}
	return err
}

// Loader facts an FTB manifest declares
func ftbEvidence(manifest *ftbVersionManifest) packEvidence {
	mc, name, version := "", "", ""
	for _, t := range manifest.Targets {
		switch t.Type {
		case "game":
			mc = t.Version
		case "modloader":
			name, version = t.Name, t.Version
		}
	}
	if name == "" {
		return packEvidence{mcVersion: mc}
	}
	return loaderEvidence(name, version, mc)
}

// Applies FTB side flags plus user include exclude rules
// Client side flags mark for the sweep instead of skipping
func ftbFileWanted(file *ftbFile, excludes, forceIncludes []string) (bool, bool) {
	if minecraft.MatchesPatterns(file.Name, forceIncludes) {
		return true, false
	}
	if minecraft.MatchesPatterns(file.Name, excludes) {
		return false, false
	}
	return true, file.ClientOnly
}

// Joins an FTB file entry onto the data dir
func ftbDest(dataPath string, file *ftbFile) string {
	rel := path.Join(strings.TrimPrefix(file.Path, "./"), file.Name)
	return joinData(dataPath, rel)
}
