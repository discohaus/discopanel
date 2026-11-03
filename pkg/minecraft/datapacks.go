// Datapack discovery and reference scanning, loader agnostic
package minecraft

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

// Caps how much of pack.mcmeta a listing reads
const maxPackMetaBytes = 64 << 10

// One datapack in a world datapacks dir
type Datapack struct {
	Name        string
	IsDir       bool
	Enabled     bool
	Size        int64
	ModTime     time.Time
	Description string
	PackFormat  int32
}

// Errors the datapack helpers report for bad input
var (
	ErrDatapackName     = errors.New("invalid datapack name")
	ErrDatapackNoMeta   = errors.New("zip has no pack.mcmeta at its root or one folder deep")
	ErrDatapackNotZip   = errors.New("file is not a valid zip archive")
	ErrDatapackNotFound = errors.New("datapack not found")
	ErrDatapackExists   = errors.New("a datapack with that name already exists there")
)

// Rejects names that are not one plain entry in the dir
func ValidDatapackName(name string) error {
	if name == "" || name == "." || name == ".." || strings.HasPrefix(name, ".") ||
		strings.ContainsAny(name, `/\`) || filepath.Base(name) != name || len(name) > 255 {
		return ErrDatapackName
	}
	return nil
}

// Lists zips and pack folders, an enabled twin hides a disabled one
func ListDatapacks(enabledDir, disabledDir string) []Datapack {
	packs := scanDatapackDir(enabledDir, true)
	seen := make(map[string]bool, len(packs))
	for _, p := range packs {
		seen[p.Name] = true
	}
	for _, p := range scanDatapackDir(disabledDir, false) {
		if !seen[p.Name] {
			packs = append(packs, p)
		}
	}
	sort.Slice(packs, func(i, j int) bool {
		return strings.ToLower(packs[i].Name) < strings.ToLower(packs[j].Name)
	})
	return packs
}

func scanDatapackDir(dir string, enabled bool) []Datapack {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var packs []Datapack
	for _, e := range entries {
		if ValidDatapackName(e.Name()) != nil {
			continue
		}
		if p, ok := readDatapack(dir, e.Name(), enabled); ok {
			packs = append(packs, p)
		}
	}
	return packs
}

// Reads one entry, false when it is not a datapack
func readDatapack(dir, name string, enabled bool) (Datapack, bool) {
	path := filepath.Join(dir, name)
	// Lstat keeps symlinks out of the listing
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return Datapack{}, false
	}
	p := Datapack{Name: name, IsDir: info.IsDir(), Enabled: enabled, Size: info.Size(), ModTime: info.ModTime()}
	var meta []byte
	if info.IsDir() {
		meta, err = readLimited(filepath.Join(path, "pack.mcmeta"))
		if err != nil {
			return Datapack{}, false
		}
		p.Size = dirSize(path)
	} else {
		if !strings.EqualFold(filepath.Ext(name), ".zip") {
			return Datapack{}, false
		}
		meta, _ = zipPackMeta(path)
	}
	p.Description, p.PackFormat = parsePackMeta(meta)
	return p, true
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxPackMetaBytes))
}

func dirSize(root string) int64 {
	var total int64
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Root pack.mcmeta of a zip, nil when missing
func zipPackMeta(path string) ([]byte, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, f := range r.File {
		if normalizeZipName(f.Name) == "pack.mcmeta" {
			return readZipEntry(f)
		}
	}
	return nil, ErrDatapackNoMeta
}

func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, maxPackMetaBytes))
}

// Pulls description text and pack_format, zero values when absent
func parsePackMeta(data []byte) (string, int32) {
	var meta struct {
		Pack struct {
			PackFormat  json.RawMessage `json:"pack_format"`
			MinFormat   json.RawMessage `json:"min_format"`
			Description json.RawMessage `json:"description"`
		} `json:"pack"`
	}
	if len(data) == 0 || json.Unmarshal(data, &meta) != nil {
		return "", 0
	}
	// Newer packs may drop pack_format for min_format
	format := packFormatNumber(meta.Pack.PackFormat)
	if format == 0 {
		format = packFormatNumber(meta.Pack.MinFormat)
	}
	var desc any
	if len(meta.Pack.Description) > 0 {
		_ = json.Unmarshal(meta.Pack.Description, &desc)
	}
	return strings.TrimSpace(legacyCodes.ReplaceAllString(componentText(desc), "")), format
}

// Reads a format written as a number or a [major, minor] pair
func packFormatNumber(raw json.RawMessage) int32 {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return int32(n)
	}
	var pair []float64
	if json.Unmarshal(raw, &pair) == nil && len(pair) > 0 {
		return int32(pair[0])
	}
	return 0
}

// Section sign formatting codes
var legacyCodes = regexp.MustCompile(`§.`)

// Flattens a chat component to plain text
func componentText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case float64, bool:
		return fmt.Sprint(c)
	case []any:
		var b strings.Builder
		for _, part := range c {
			b.WriteString(componentText(part))
		}
		return b.String()
	case map[string]any:
		var b strings.Builder
		if text, ok := c["text"]; ok {
			b.WriteString(componentText(text))
		} else if fb, ok := c["fallback"].(string); ok {
			b.WriteString(fb)
		} else if key, ok := c["translate"].(string); ok {
			b.WriteString(key)
		}
		if extra, ok := c["extra"]; ok {
			b.WriteString(componentText(extra))
		}
		return b.String()
	}
	return ""
}

// Strips leading ./ segments some archivers write
func normalizeZipName(name string) string {
	for strings.HasPrefix(name, "./") {
		name = name[2:]
	}
	return name
}

// Finds pack.mcmeta at the zip root or one folder deep, returns that folder
// prefix and whether entry names need rewriting before the game can load it
func DatapackZipRoot(path string) (prefix string, rewrite bool, err error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", false, ErrDatapackNotZip
	}
	defer r.Close()
	dotted, root, found := false, false, false
	for _, f := range r.File {
		name := normalizeZipName(f.Name)
		dotted = dotted || name != f.Name
		if name == "pack.mcmeta" {
			root = true
			continue
		}
		dir, base, ok := strings.Cut(name, "/")
		// macOS archive junk never counts as the pack folder
		if ok && base == "pack.mcmeta" && dir != "__MACOSX" && !found {
			prefix, found = dir+"/", true
		}
	}
	switch {
	case root:
		return "", dotted, nil
	case found:
		return prefix, true, nil
	}
	return "", false, ErrDatapackNoMeta
}

// Copies a zip to dst with ./ and prefix stripped so pack.mcmeta lands at the root
func RebaseDatapackZip(src, dst, prefix string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return ErrDatapackNotZip
	}
	defer r.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	w := zip.NewWriter(out)
	for _, f := range r.File {
		rest, ok := strings.CutPrefix(normalizeZipName(f.Name), prefix)
		if !ok || rest == "" {
			continue
		}
		hdr := f.FileHeader
		hdr.Name = rest
		// Raw copy keeps the compressed bytes and checksums as they were
		raw, err := f.OpenRaw()
		if err != nil {
			w.Close()
			out.Close()
			return err
		}
		fw, err := w.CreateRaw(&hdr)
		if err == nil {
			_, err = io.Copy(fw, raw)
		}
		if err != nil {
			w.Close()
			out.Close()
			return err
		}
	}
	if err := w.Close(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Moves a datapack between the enabled and disabled dirs
func SetDatapackEnabled(enabledDir, name string, enabled bool) error {
	if err := ValidDatapackName(name); err != nil {
		return err
	}
	from, to := filepath.Join(enabledDir+"_disabled", name), filepath.Join(enabledDir, name)
	if !enabled {
		from, to = to, from
	}
	if _, err := os.Lstat(from); err != nil {
		return ErrDatapackNotFound
	}
	// Rename would silently replace a twin file
	if _, err := os.Lstat(to); err == nil {
		return ErrDatapackExists
	}
	if enabled {
		return EnableDatapack(enabledDir, name)
	}
	return DisableDatapack(enabledDir, name)
}

// Finds a listed datapack by name, enabled copy first
func FindDatapack(enabledDir, disabledDir, name string) (Datapack, string, bool) {
	if ValidDatapackName(name) != nil {
		return Datapack{}, "", false
	}
	for _, scan := range []struct {
		dir     string
		enabled bool
	}{{enabledDir, true}, {disabledDir, false}} {
		if p, ok := readDatapack(scan.dir, name, scan.enabled); ok {
			return p, scan.dir, true
		}
	}
	return Datapack{}, "", false
}
