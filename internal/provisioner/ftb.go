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
	var pending []ftbFile
	total := 0
	for _, file := range manifest.Files {
		if !p.ftbFileWanted(server, &file, excludes, forceIncludes) {
			continue
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
			dest := ftbDest(server.DataPath, &file)
			var sum *checksum
			if file.Sha1 != "" {
				sum = &checksum{algo: "sha1", value: file.Sha1}
			}
			err := fmt.Errorf("file %q has no download url", file.Name)
			for _, u := range append([]string{file.URL}, file.Mirrors...) {
				if u == "" {
					continue
				}
				if err = p.download(gctx, u, dest, sum, nil, nil); err == nil {
					break
				}
			}
			if err != nil {
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

	// Adopted pack version keeps the server row coherent
	ev := ftbEvidence(&manifest)
	p.adoptMCVersion(ctx, server, ev.mcVersion)
	return p.installPackRuntime(ctx, server, cfg, ev)
}

// Loader facts an FTB manifest declares
func ftbEvidence(manifest *ftbVersionManifest) packEvidence {
	ev := packEvidence{}
	for _, t := range manifest.Targets {
		switch t.Type {
		case "game":
			ev.mcVersion = t.Version
		case "modloader":
			ev.loaderID = t.Name + "-" + t.Version
		}
	}
	if loader, version, ok := minecraft.CutPackLoaderID(ev.loaderID); ok {
		ev.loader = loader
		ev.loaderVersion = version
	}
	return ev
}

// Applies FTB side flags plus user include exclude rules
func (p *Provisioner) ftbFileWanted(server *v1.Server, file *ftbFile, excludes, forceIncludes []string) bool {
	if minecraft.MatchesPatterns(file.Name, forceIncludes) {
		return true
	}
	if minecraft.MatchesPatterns(file.Name, excludes) {
		p.progress(server, "skipping excluded file %s", file.Name)
		return false
	}
	// FTB side flags are authoritative, no slug guessing
	if file.ClientOnly {
		p.progress(server, "skipping client-only file %s", file.Name)
		return false
	}
	return true
}

// Joins an FTB file entry onto the data dir
func ftbDest(dataPath string, file *ftbFile) string {
	rel := path.Join(strings.TrimPrefix(file.Path, "./"), file.Name)
	return joinData(dataPath, rel)
}
