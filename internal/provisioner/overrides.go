package provisioner

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/discohaus/discopanel/pkg/configedit"
	"github.com/discohaus/discopanel/pkg/files"
	v1 "github.com/discohaus/discopanel/pkg/proto/discopanel/v1"
	"github.com/discohaus/discopanel/pkg/runtimespec"
)

// Bounds keeping one server's overrides reasonable
const (
	maxOverrideFiles   = 200
	maxOverrideEntries = 500
	maxOverrideKey     = 512
	maxOverrideValue   = 8 << 10
)

// Format an override file is edited with, unknown when the extension
// does not support one or disagrees with an explicit choice
func OverrideFormat(f *v1.ConfigOverrideFile) configedit.Format {
	detected := configedit.DetectFormat(f.GetPath())
	explicit := f.GetFormat()
	if explicit != v1.ConfigFormat_CONFIG_FORMAT_UNSPECIFIED && protoFormat(detected) != explicit {
		return configedit.FormatUnknown
	}
	return detected
}

// Format an explicit enum value names
func explicitFormat(f v1.ConfigFormat) configedit.Format {
	switch f {
	case v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES:
		return configedit.FormatProperties
	case v1.ConfigFormat_CONFIG_FORMAT_TOML:
		return configedit.FormatTOML
	case v1.ConfigFormat_CONFIG_FORMAT_JSON:
		return configedit.FormatJSON
	case v1.ConfigFormat_CONFIG_FORMAT_YAML:
		return configedit.FormatYAML
	case v1.ConfigFormat_CONFIG_FORMAT_CFG:
		return configedit.FormatCFG
	case v1.ConfigFormat_CONFIG_FORMAT_SNBT:
		return configedit.FormatSNBT
	}
	return configedit.FormatUnknown
}

func protoFormat(f configedit.Format) v1.ConfigFormat {
	switch f {
	case configedit.FormatProperties:
		return v1.ConfigFormat_CONFIG_FORMAT_PROPERTIES
	case configedit.FormatTOML:
		return v1.ConfigFormat_CONFIG_FORMAT_TOML
	case configedit.FormatJSON:
		return v1.ConfigFormat_CONFIG_FORMAT_JSON
	case configedit.FormatYAML:
		return v1.ConfigFormat_CONFIG_FORMAT_YAML
	case configedit.FormatCFG:
		return v1.ConfigFormat_CONFIG_FORMAT_CFG
	case configedit.FormatSNBT:
		return v1.ConfigFormat_CONFIG_FORMAT_SNBT
	}
	return v1.ConfigFormat_CONFIG_FORMAT_UNSPECIFIED
}

// Cleans a user path to data dir relative slash form
func normalizeOverridePath(raw string) (string, error) {
	p := strings.TrimSpace(strings.ReplaceAll(raw, `\`, "/"))
	p = strings.TrimPrefix(p, "/data/")
	p = strings.TrimLeft(p, "/")
	p = path.Clean(p)
	if p == "." || p == "" {
		return "", errors.New("file path is empty")
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("%s is outside the server directory", raw)
	}
	first, _, _ := strings.Cut(p, "/")
	if strings.EqualFold(first, runtimespec.StateDir) {
		return "", fmt.Errorf("%s is panel state and cannot be overridden", raw)
	}
	if panelOwnedFiles[strings.ToLower(p)] {
		return "", fmt.Errorf("%s is managed by the panel, use the Properties tab instead", p)
	}
	if launcherFile(p) {
		return "", fmt.Errorf("%s launches the server and cannot be overridden", p)
	}
	return p, nil
}

// Scripts, launch args and archives, never edited whatever the format
var launcherNames = map[string]bool{
	"user_jvm_args.txt": true, "unix_args.txt": true, "win_args.txt": true, "eula.txt": true,
}

var launcherExts = map[string]bool{
	".sh": true, ".bash": true, ".bat": true, ".cmd": true, ".ps1": true, ".command": true,
	".jar": true, ".zip": true, ".exe": true,
}

func launcherFile(rel string) bool {
	base := strings.ToLower(path.Base(rel))
	return launcherNames[base] || launcherExts[path.Ext(base)]
}

// Largest file an override will parse and rewrite
const maxOverrideFileBytes = 8 << 20

// Reads a target file, refusing anything that is not a text config
func readOverrideTarget(full string) ([]byte, os.FileInfo, error) {
	info, err := os.Stat(full)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, info, errors.New("not a regular file")
	}
	if info.Size() > maxOverrideFileBytes {
		return nil, info, fmt.Errorf("file is larger than %d MB", maxOverrideFileBytes>>20)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, info, err
	}
	if err := configedit.CheckText(data); err != nil {
		return nil, info, err
	}
	return data, info, nil
}

// Validates and cleans overrides before they are stored
func NormalizeConfigOverrides(dataDir string, in []*v1.ConfigOverrideFile) ([]*v1.ConfigOverrideFile, error) {
	if len(in) > maxOverrideFiles {
		return nil, fmt.Errorf("at most %d override files are allowed", maxOverrideFiles)
	}
	seen := map[string]bool{}
	out := make([]*v1.ConfigOverrideFile, 0, len(in))
	for _, f := range in {
		rel, err := normalizeOverridePath(f.GetPath())
		if err != nil {
			return nil, err
		}
		full, err := files.ResolveUnder(dataDir, rel)
		if err != nil {
			return nil, fmt.Errorf("%s is outside the server directory", rel)
		}
		if seen[rel] {
			return nil, fmt.Errorf("%s is listed twice", rel)
		}
		seen[rel] = true
		clean := &v1.ConfigOverrideFile{Path: rel, Format: f.GetFormat()}
		format := OverrideFormat(clean)
		if format == configedit.FormatUnknown {
			if explicit := explicitFormat(f.GetFormat()); explicit != configedit.FormatUnknown {
				return nil, fmt.Errorf("%s is not a %s file", rel, explicit)
			}
			return nil, fmt.Errorf("%s: unsupported file type, supported are .properties, .toml, .json, .json5, .yml, .cfg and .snbt", rel)
		}
		// Files that exist must already be text configs
		if _, _, err := readOverrideTarget(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %v", rel, err)
		}
		if len(f.GetEntries()) > maxOverrideEntries {
			return nil, fmt.Errorf("%s: at most %d keys per file", rel, maxOverrideEntries)
		}
		keys := map[string]bool{}
		for _, e := range f.GetEntries() {
			key := strings.TrimSpace(e.GetKey())
			if len(key) > maxOverrideKey || len(e.GetValue()) > maxOverrideValue {
				return nil, fmt.Errorf("%s: key %q or its value is too long", rel, key)
			}
			if err := configedit.ValidateKey(format, key); err != nil {
				return nil, fmt.Errorf("%s: %v", rel, err)
			}
			if strings.ContainsAny(e.GetValue(), "\r\n") {
				return nil, fmt.Errorf("%s: value of %s must be a single line", rel, key)
			}
			if keys[key] {
				return nil, fmt.Errorf("%s: key %s is listed twice", rel, key)
			}
			keys[key] = true
			clean.Entries = append(clean.Entries, &v1.ConfigOverrideEntry{Key: key, Value: e.GetValue()})
		}
		out = append(out, clean)
	}
	return out, nil
}

// Existence and resolved format of each override file
func ConfigOverrideStatus(dataDir string, list []*v1.ConfigOverrideFile) []*v1.ConfigOverrideFileStatus {
	out := make([]*v1.ConfigOverrideFileStatus, 0, len(list))
	for _, f := range list {
		st := &v1.ConfigOverrideFileStatus{Path: f.GetPath(), Format: protoFormat(OverrideFormat(f))}
		if full, err := files.ResolveUnder(dataDir, f.GetPath()); err == nil {
			st.Exists = fileExists(full)
		}
		out = append(out, st)
	}
	return out
}

// Re-applies stored overrides, problems are reported and never fatal
func (p *Provisioner) applyConfigOverrides(server *v1.Server) {
	overrides, err := runtimespec.ReadConfigOverrides(server.DataPath)
	if err != nil {
		p.progress(server, "warning: config overrides skipped: %v", err)
		return
	}
	for _, f := range overrides.GetFiles() {
		p.applyOverrideFile(server, f)
	}
}

func (p *Provisioner) applyOverrideFile(server *v1.Server, f *v1.ConfigOverrideFile) {
	if len(f.GetEntries()) == 0 {
		return
	}
	// The stored file is panel state but still revalidated
	rel, err := normalizeOverridePath(f.GetPath())
	if err != nil {
		p.progress(server, "warning: config override %s skipped: %v", f.GetPath(), err)
		return
	}
	full, err := files.ResolveUnder(server.DataPath, rel)
	if err != nil {
		p.progress(server, "warning: config override %s skipped: %v", rel, err)
		return
	}
	format := OverrideFormat(f)
	if format == configedit.FormatUnknown {
		p.progress(server, "warning: config override %s skipped: unsupported file type", rel)
		return
	}
	data, info, err := readOverrideTarget(full)
	if errors.Is(err, os.ErrNotExist) {
		p.progress(server, "config override %s: file not found yet, will apply once it exists", rel)
		return
	}
	if err != nil {
		p.progress(server, "warning: config override %s skipped: %v", rel, err)
		return
	}

	entries := make([]configedit.Entry, len(f.GetEntries()))
	for i, e := range f.GetEntries() {
		entries[i] = configedit.Entry{Key: e.GetKey(), Value: e.GetValue()}
	}
	res, err := configedit.Apply(format, data, entries)
	if err != nil {
		p.progress(server, "warning: config override %s skipped, %s file did not parse: %v", rel, format, err)
		return
	}
	for _, e := range res.Errors {
		p.progress(server, "warning: config override %s: %v", rel, e)
	}
	if !res.Changed {
		return
	}
	if err := writeOverrideTarget(full, res.Data, info); err != nil {
		p.progress(server, "warning: config override %s not written: %v", rel, err)
		return
	}
	p.progress(server, "config override %s: applied %d of %d keys", rel, res.Applied, len(entries))
}

// Replaces the file atomically, keeping mode and owner the container needs
func writeOverrideTarget(full string, data []byte, info os.FileInfo) error {
	// Symlinks inside the data dir keep pointing at the edited file
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(real), "."+filepath.Base(real)+".override-*")
	if err != nil {
		return err
	}
	cleanup := func(err error) error {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return cleanup(err)
	}
	if uid, gid, ok := files.Owner(real); ok {
		if err := tmp.Chown(uid, gid); err != nil && os.Geteuid() == 0 {
			return cleanup(err)
		}
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), real); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
