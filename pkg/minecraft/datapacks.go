// Datapack discovery and reference scanning, loader agnostic
package minecraft

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Dirs that never hold datapack directories
var datapackSkipDirs = map[string]bool{
	"mods": true, "mods_disabled": true, "plugins": true, "libraries": true,
	"versions": true, "logs": true, "crash-reports": true, "backups": true,
	"region": true, "entities": true, "poi": true, "playerdata": true,
	"stats": true, "advancements": true,
}

// Caps how much of one zip entry a scan reads
const maxDatapackEntryBytes = 4 << 20

// Finds datapack zips in any datapacks dir three levels deep
func FindDatapackZips(dataPath string) []string {
	var zips []string
	dirs := []string{""}
	for depth := 0; depth < 3 && len(dirs) > 0; depth++ {
		var next []string
		for _, dir := range dirs {
			entries, err := os.ReadDir(filepath.Join(dataPath, dir))
			if err != nil {
				continue
			}
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				name := e.Name()
				if datapackSkipDirs[name] || strings.HasPrefix(name, ".") {
					continue
				}
				rel := filepath.Join(dir, name)
				if name == "datapacks" {
					zips = append(zips, datapackZipsIn(dataPath, rel)...)
					continue
				}
				next = append(next, rel)
			}
		}
		dirs = next
	}
	return zips
}

func datapackZipsIn(dataPath, relDir string) []string {
	entries, err := os.ReadDir(filepath.Join(dataPath, relDir))
	if err != nil {
		return nil
	}
	var zips []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".zip") {
			continue
		}
		zips = append(zips, filepath.Join(relDir, e.Name()))
	}
	return zips
}

// Reports whether any json entry quotes one of the ids
func ZipRefsAny(absPath string, ids []string) bool {
	r, err := zip.OpenReader(absPath)
	if err != nil {
		return false
	}
	defer r.Close()
	quoted := make([][]byte, len(ids))
	for i, id := range ids {
		quoted[i] = []byte(`"` + id + `"`)
	}
	for _, f := range r.File {
		if !strings.HasSuffix(f.Name, ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxDatapackEntryBytes))
		rc.Close()
		if err != nil {
			continue
		}
		for _, q := range quoted {
			if bytes.Contains(data, q) {
				return true
			}
		}
	}
	return false
}

// Finds datapack zips referencing any of the missing entries
func FindDatapackRefs(dataPath string, ids []string) []string {
	var hits []string
	for _, rel := range FindDatapackZips(dataPath) {
		if ZipRefsAny(filepath.Join(dataPath, rel), ids) {
			hits = append(hits, rel)
		}
	}
	return hits
}

// Moves one datapack zip into a sibling disabled directory
func DisableDatapack(dataPath, relPath string) error {
	abs := filepath.Join(dataPath, relPath)
	disabledDir := filepath.Dir(abs) + "_disabled"
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		return err
	}
	return os.Rename(abs, filepath.Join(disabledDir, filepath.Base(abs)))
}

// Moves a disabled datapack zip back where it was
func EnableDatapack(dataPath, relPath string) error {
	abs := filepath.Join(dataPath, relPath)
	return os.Rename(filepath.Join(filepath.Dir(abs)+"_disabled", filepath.Base(abs)), abs)
}
