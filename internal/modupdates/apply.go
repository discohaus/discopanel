package modupdates

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/discohaus/discopanel/pkg/files"
)

// Fetches url into a new file at dest, failing unless size and checksum match
type Fetcher func(ctx context.Context, url, dest, algo, sum string, size int64) error

// Panel private store of replaced jars for one server dir.
// It lives outside the server data dir: that dir is mounted into the game
// container, so anything in it may be planted by plugin code.
type Backups struct {
	Root string
}

// Backup store for one server's mods or plugins dir
func BackupsFor(panelDataDir, serverID, modsDir string) (Backups, error) {
	base, err := BackupBase(panelDataDir, serverID)
	if err != nil {
		return Backups{}, err
	}
	return Backups{Root: filepath.Join(base, filepath.Base(modsDir))}, nil
}

// Every update backup a server owns, removed with the server
func BackupBase(panelDataDir, serverID string) (string, error) {
	if !files.IsPlainFileName(serverID) || panelDataDir == "" {
		return "", fmt.Errorf("invalid server id %q", serverID)
	}
	return filepath.Join(panelDataDir, "mod-updates", serverID), nil
}

// What an update replaced, keyed by the file it installed
type BackupRecord struct {
	Previous    string    `json:"previous"`
	Backup      string    `json:"backup"`
	FromVersion string    `json:"from_version,omitempty"`
	ToVersion   string    `json:"to_version,omitempty"`
	At          time.Time `json:"at"`
}

type backupIndex struct {
	Version int                     `json:"version"`
	Files   map[string]BackupRecord `json:"files"`
}

func (b Backups) indexPath() string { return filepath.Join(b.Root, "index.json") }

func (b Backups) read() *backupIndex {
	idx := &backupIndex{Version: 1, Files: map[string]BackupRecord{}}
	data, err := os.ReadFile(b.indexPath())
	if err != nil {
		return idx
	}
	if json.Unmarshal(data, idx) != nil || idx.Files == nil {
		return &backupIndex{Version: 1, Files: map[string]BackupRecord{}}
	}
	return idx
}

func (b Backups) write(idx *backupIndex) error {
	if err := os.MkdirAll(b.Root, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(b.Root, ".index-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), b.indexPath())
}

// Record for a current file whose backup is still on disk
func (b Backups) Lookup(name string) (BackupRecord, bool) {
	rec, ok := b.read().Files[name]
	if !ok || !files.IsPlainFileName(rec.Backup) || !validJarName(rec.Previous) {
		return BackupRecord{}, false
	}
	if info, err := os.Lstat(filepath.Join(b.Root, rec.Backup)); err != nil || !info.Mode().IsRegular() {
		return BackupRecord{}, false
	}
	return rec, true
}

// Jar names the panel would accept as a new file
func validJarName(name string) bool {
	return files.IsPlainFileName(name) && strings.EqualFold(filepath.Ext(name), ".jar")
}

// A name taken in any of dirs by an entry other than skip
func nameTaken(name, skip string, dirs []*os.Root) bool {
	if name == skip {
		return false
	}
	for _, d := range dirs {
		if _, err := d.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// Opens a regular file inside dir, refusing links and specials
func openRegular(dir *os.Root, name string) (*os.File, os.FileInfo, error) {
	if info, err := dir.Lstat(name); err != nil {
		return nil, nil, err
	} else if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", name)
	}
	f, err := dir.Open(name)
	if err != nil {
		return nil, nil, err
	}
	// The entry may have been swapped since the lstat, trust the fd
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%s is not a regular file", name)
	}
	return f, info, nil
}

// Copies r into a fresh part file in dir through the creating handle.
// The part has a hidden non jar name so no loader picks it up half written.
func writePart(dir *os.Root, r io.Reader, prefix string) (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	name := fmt.Sprintf(".%s-%s.part", prefix, hex.EncodeToString(buf[:]))
	f, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Chmod(0644)
	}
	if err == nil {
		err = matchOwner(dir, f)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		dir.Remove(name)
		return "", err
	}
	return name, nil
}

// A root panel hands new jars to the dir's owner, the game user
func matchOwner(dir *os.Root, f *os.File) error {
	if os.Geteuid() != 0 {
		return nil
	}
	info, err := dir.Stat(".")
	if err != nil {
		return err
	}
	if uid, gid, ok := files.InfoOwner(info); ok {
		return f.Chown(uid, gid)
	}
	return nil
}

// Copies an open file into the private backup store
func storeBackup(b Backups, src *os.File, name string) (string, error) {
	if err := os.MkdirAll(b.Root, 0700); err != nil {
		return "", err
	}
	backupName := fmt.Sprintf("%d-%s", time.Now().UnixNano(), name)
	if !files.IsPlainFileName(backupName) {
		return "", errors.New("invalid backup name")
	}
	dst, err := os.OpenFile(filepath.Join(b.Root, backupName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		dst.Close()
		return "", err
	}
	_, err = io.Copy(dst, src)
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(filepath.Join(b.Root, backupName))
		return "", err
	}
	return backupName, nil
}

// Swaps jar for rel and keeps the old file for a rollback.
// dir is the jar's folder opened as a root, so no path, link or rename
// can leave it; dirs holds every folder the loader reads names from.
// The old jar is copied from an fd into the private store first, then
// the new one is renamed into place, so the folder never lacks a copy.
func Apply(ctx context.Context, dir *os.Root, jar Jar, from string, rel *Release, b Backups, stageDir string, dirs []*os.Root, fetch Fetcher) (string, error) {
	if rel == nil || rel.URL == "" {
		return "", errors.New("no newer file to install")
	}
	if rel.HashAlgo == "" || rel.HashSum == "" || newHash(rel.HashAlgo) == nil {
		return "", errors.New("upstream published no usable checksum for the new file")
	}
	if !validJarName(jar.Name) {
		return "", fmt.Errorf("installed file name %q is not a plain jar name", jar.Name)
	}
	newName := rel.FileName
	if !validJarName(newName) {
		return "", fmt.Errorf("upstream file name %q is not a plain jar name", newName)
	}
	if nameTaken(newName, jar.Name, dirs) {
		return "", fmt.Errorf("%s already exists, remove it first", newName)
	}
	old, _, err := openRegular(dir, jar.Name)
	if err != nil {
		return "", fmt.Errorf("%s cannot be read: %w", jar.Name, err)
	}
	defer old.Close()

	staged := filepath.Join(stageDir, "download.jar")
	if err := fetch(ctx, rel.URL, staged, rel.HashAlgo, rel.HashSum, rel.Size); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	src, err := os.Open(staged)
	if err != nil {
		return "", err
	}
	defer src.Close()
	// Belt and braces, the fetcher is not trusted to have checked
	if err := verifyReader(src, rel.HashAlgo, rel.HashSum); err != nil {
		return "", err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	part, err := writePart(dir, src, "update")
	if err != nil {
		return "", err
	}
	defer dir.Remove(part)

	backupName, err := storeBackup(b, old, jar.Name)
	if err != nil {
		return "", fmt.Errorf("backing up %s failed: %w", jar.Name, err)
	}
	backupPath := filepath.Join(b.Root, backupName)
	if err := dir.Rename(part, newName); err != nil {
		os.Remove(backupPath)
		return "", err
	}
	if newName != jar.Name {
		if err := dir.Remove(jar.Name); err != nil {
			// Two builds of one plugin must never load together
			dir.Remove(newName)
			os.Remove(backupPath)
			return "", fmt.Errorf("removing %s failed: %w", jar.Name, err)
		}
	}

	idx := b.read()
	// Chained updates keep only the newest generation
	for _, key := range []string{jar.Name, newName} {
		if prev, ok := idx.Files[key]; ok {
			if files.IsPlainFileName(prev.Backup) {
				os.Remove(filepath.Join(b.Root, prev.Backup))
			}
			delete(idx.Files, key)
		}
	}
	idx.Files[newName] = BackupRecord{
		Previous:    jar.Name,
		Backup:      backupName,
		FromVersion: from,
		ToVersion:   rel.Version,
		At:          time.Now().UTC(),
	}
	if err := b.write(idx); err != nil {
		return newName, fmt.Errorf("updated, but the rollback record could not be saved: %w", err)
	}
	return newName, nil
}

// Puts back the jar an update replaced, reading only the private store
func Rollback(dir *os.Root, current Jar, b Backups, dirs []*os.Root) (string, error) {
	rec, ok := b.Lookup(current.Name)
	if !ok {
		return "", errors.New("no backup is kept for this file")
	}
	if nameTaken(rec.Previous, current.Name, dirs) {
		return "", fmt.Errorf("%s already exists, remove it first", rec.Previous)
	}
	backupPath := filepath.Join(b.Root, rec.Backup)
	src, err := os.Open(backupPath)
	if err != nil {
		return "", err
	}
	defer src.Close()
	part, err := writePart(dir, src, "rollback")
	if err != nil {
		return "", err
	}
	defer dir.Remove(part)
	if err := dir.Rename(part, rec.Previous); err != nil {
		return "", err
	}
	if rec.Previous != current.Name {
		if err := dir.Remove(current.Name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			dir.Remove(rec.Previous)
			return "", fmt.Errorf("removing %s failed: %w", current.Name, err)
		}
	}
	idx := b.read()
	delete(idx.Files, current.Name)
	os.Remove(backupPath)
	if err := b.write(idx); err != nil {
		return rec.Previous, fmt.Errorf("restored, but the backup record could not be updated: %w", err)
	}
	return rec.Previous, nil
}

func newHash(algo string) hash.Hash {
	switch strings.ToLower(algo) {
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "sha512":
		return sha512.New()
	case "md5":
		return md5.New()
	}
	return nil
}

func digestEqual(got []byte, want string) bool {
	return strings.EqualFold(hex.EncodeToString(got), want)
}

// Checks a reader against a hex digest, never echoing what it got
func verifyReader(r io.Reader, algo, sum string) error {
	h := newHash(algo)
	if h == nil {
		return fmt.Errorf("unsupported checksum %q", algo)
	}
	if _, err := io.Copy(h, r); err != nil {
		return err
	}
	if !digestEqual(h.Sum(nil), sum) {
		return errors.New("downloaded file does not match the published checksum")
	}
	return nil
}
