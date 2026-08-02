package files

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/discohaus/discopanel/pkg/minecraft"
	"github.com/mholt/archives"
)

// Joins rel under base and rejects escapes
func ResolveUnder(base, rel string) (string, error) {
	full := filepath.Join(base, rel)
	r, err := filepath.Rel(base, full)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes base directory: %s", rel)
	}
	return full, nil
}

// Reports whether child sits at or under parent
func Within(parent, child string) bool {
	r, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return r == "." || (r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)))
}

// Reads level-name from server.properties, vanilla default otherwise
func worldName(dataDir string) string {
	props, err := minecraft.LoadPropertiesFile(dataDir)
	if err != nil {
		return "world"
	}
	if name := props["level-name"]; name != "" {
		return name
	}
	return "world"
}

func FindWorldDir(dataDir string) (string, error) {
	worldDir, err := ResolveUnder(dataDir, worldName(dataDir))
	if err != nil {
		return "", fmt.Errorf("invalid level-name in %s: %w", dataDir, err)
	}
	if _, err := os.Stat(filepath.Join(worldDir, "level.dat")); err != nil {
		return "", fmt.Errorf("no valid world directory found in %s", dataDir)
	}
	return worldDir, nil
}

// Returns the world directory plus any sibling world dirs
func FindWorldDirs(dataDir string) ([]string, error) {
	worldDir, err := FindWorldDir(dataDir)
	if err != nil {
		return nil, err
	}

	dirs := []string{worldDir}
	for _, suffix := range []string{"_nether", "_the_end"} {
		dimDir := worldDir + suffix
		if info, err := os.Stat(dimDir); err == nil && info.IsDir() {
			dirs = append(dirs, dimDir)
		}
	}
	return dirs, nil
}

// Calculates total directory size in bytes, including nested files
func CalculateDirSize(dirPath string) (int64, error) {
	var totalSize int64

	err := filepath.WalkDir(dirPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Skip problematic paths
			return nil // Continue walking
		}

		// Skip if it's a directory or symbolic link
		if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			log.Printf("Error getting info for %s: %v", path, err)
			return nil // Continue walking
		}

		totalSize += info.Size()
		return nil
	})

	if err != nil {
		return 0, fmt.Errorf("failed to walk directory %s: %w", dirPath, err)
	}

	return totalSize, nil
}

func SanitizePathName(name string) string {
	// Alphanum plus underscore and dash
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	safe := re.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")

	// Empty
	if safe == "" {
		safe = "DISCO_GENERIC"
	}
	return safe
}

// Extract archive to destPath
func ExtractArchive(ctx context.Context, archivePath string, destPath string, counter *atomic.Int32) (int, error) {
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open archive file: %w", err)
	}
	defer archiveFile.Close()

	// Identify archive format
	format, stream, err := archives.Identify(ctx, archivePath, archiveFile)
	if err != nil {
		return 0, fmt.Errorf("failed to identify archive format: %w", err)
	}

	// Checks if format supports extraction
	extractor, ok := format.(archives.Extractor)
	if !ok {
		return 0, fmt.Errorf("format does not support extraction")
	}

	// Make dest
	if err := os.MkdirAll(destPath, 0755); err != nil {
		return 0, fmt.Errorf("failed to create destination directory: %w", err)
	}

	// Extract and walk while recursively extracting
	filesExtracted := 0
	err = extractor.Extract(ctx, stream, func(ctx context.Context, f archives.FileInfo) error {
		// No sneaky traversals
		targetPath, err := ResolveUnder(destPath, f.NameInArchive)
		if err != nil {
			return fmt.Errorf("illegal file path in archive: %s", f.NameInArchive)
		}

		if f.IsDir() {
			return os.MkdirAll(targetPath, 0755)
		}

		// Make parent(s)
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			return fmt.Errorf("failed to create parent directory: %w", err)
		}

		// Create file
		outFile, err := os.Create(targetPath)
		if err != nil {
			return fmt.Errorf("failed to create file %s: %w", targetPath, err)
		}
		defer outFile.Close()

		// Open the file from the archive
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("failed to open file in archive: %w", err)
		}
		defer rc.Close()

		// Copy contents
		if _, err := io.Copy(outFile, rc); err != nil {
			return fmt.Errorf("failed to extract file %s: %w", targetPath, err)
		}

		// Set file perms
		if f.Mode() != 0 {
			os.Chmod(targetPath, f.Mode())
		}

		filesExtracted++
		if counter != nil {
			counter.Add(1)
		}
		return nil
	})

	if err != nil {
		return filesExtracted, fmt.Errorf("failed to extract archive: %w", err)
	}

	return filesExtracted, nil
}

func IsTextFile(path string) bool {
	// Read first 512 bytes to detect content type
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	// Read up to 512 bytes
	buffer := make([]byte, 512)
	n, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false
	}

	if n == 0 {
		// Empty files are considered text
		return true
	}

	// Check for null bytes (binary indicator)
	if bytes.Contains(buffer[:n], []byte{0}) {
		return false
	}

	// Check if it's valid UTF-8 with printable characters
	for i := range n {
		b := buffer[i]
		// Allow printable ASCII, tabs, newlines, carriage returns
		if b < 32 && b != 9 && b != 10 && b != 13 {
			return false
		}
		// Reject high control characters
		if b == 127 {
			return false
		}
	}

	return true
}

// These get zip.Store (no compression) to avoid wasting CPU
var compressedExts = map[string]bool{
	// Archives
	".zip": true, ".gz": true, ".bz2": true, ".xz": true, ".7z": true,
	".rar": true, ".lz": true, ".zst": true, ".lz4": true, ".br": true,
	".tgz": true, ".tbz2": true, ".txz": true,
	// Images
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".avif": true,
	// Audio
	".mp3": true, ".aac": true, ".ogg": true, ".opus": true, ".flac": true, ".m4a": true,
	// Video
	".mp4": true, ".mkv": true, ".avi": true, ".webm": true, ".mov": true, ".flv": true, ".m4v": true,
	// Java / packages (already zip-based)
	".jar": true, ".war": true, ".ear": true, ".deb": true, ".rpm": true, ".whl": true,
	// Game-specific
	".mcworld": true, ".mcpack": true,
	// Documents (zip-based internally)
	".docx": true, ".xlsx": true, ".pptx": true,
}

// Get zip.Store for compressed, else zip.Deflate
func zipMethod(name string) uint16 {
	ext := strings.ToLower(filepath.Ext(name))
	if compressedExts[ext] {
		return zip.Store
	}
	return zip.Deflate
}

// Writes a zip archive of paths, returns file count
func CreateZipToWriter(paths []string, basePath string, w io.Writer, compress bool) (int, error) {
	zw := zip.NewWriter(w)
	defer zw.Close()

	method := func(name string) uint16 {
		if !compress {
			return zip.Store
		}
		return zipMethod(name)
	}

	count := 0
	for _, p := range paths {
		fullPath := filepath.Join(basePath, p)
		info, err := os.Stat(fullPath)
		if err != nil {
			return count, fmt.Errorf("failed to stat %s: %w", p, err)
		}

		if info.IsDir() {
			err = filepath.WalkDir(fullPath, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				rel, _ := filepath.Rel(basePath, path)
				if d.IsDir() {
					// Add directory entry with trailing slash
					_, err := zw.Create(rel + "/")
					return err
				}
				fi, err := d.Info()
				if err != nil {
					return err
				}
				header, err := zip.FileInfoHeader(fi)
				if err != nil {
					return err
				}
				header.Name = rel
				header.Method = method(rel)
				writer, err := zw.CreateHeader(header)
				if err != nil {
					return err
				}
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				_, err = io.Copy(writer, f)
				count++
				return err
			})
			if err != nil {
				return count, fmt.Errorf("failed to add directory %s to zip: %w", p, err)
			}
		} else {
			header, err := zip.FileInfoHeader(info)
			if err != nil {
				return count, fmt.Errorf("failed to create header for %s: %w", p, err)
			}
			header.Name = p
			header.Method = method(p)
			writer, err := zw.CreateHeader(header)
			if err != nil {
				return count, fmt.Errorf("failed to create zip entry for %s: %w", p, err)
			}
			f, err := os.Open(fullPath)
			if err != nil {
				return count, fmt.Errorf("failed to open %s: %w", p, err)
			}
			_, err = io.Copy(writer, f)
			f.Close()
			if err != nil {
				return count, fmt.Errorf("failed to write %s to zip: %w", p, err)
			}
			count++
		}
	}
	return count, nil
}

// Creates a zip archive file on disk from paths
func CreateZipArchive(paths []string, basePath string, destPath string, compress bool) (int, error) {
	f, err := os.Create(destPath)
	if err != nil {
		return 0, fmt.Errorf("failed to create archive file: %w", err)
	}
	defer f.Close()

	count, err := CreateZipToWriter(paths, basePath, f, compress)
	if err != nil {
		os.Remove(destPath)
		return 0, err
	}
	return count, nil
}

// Recursively copies a directory tree from src to dst
func CopyDir(src, dst string) error {
	srcInfo, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("failed to stat source: %w", err)
	}
	if err := os.MkdirAll(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("failed to create destination: %w", err)
	}

	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			mode := fs.FileMode(0755)
			if info, ierr := d.Info(); ierr == nil {
				mode = info.Mode().Perm()
			}
			return os.MkdirAll(target, mode)
		}
		// Symlinks recreate instead of following
		if d.Type()&fs.ModeSymlink != 0 {
			link, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			return os.Symlink(link, target)
		}
		return CopyFile(path, target)
	})
}

func CopyFile(src, dst string) error {
	// Prevents copying a file onto itself before truncation
	if filepath.Clean(src) == filepath.Clean(dst) {
		return fmt.Errorf("source and destination are the same file: %s", src)
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	info, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err = io.Copy(dstFile, srcFile); err != nil {
		return err
	}
	if err := dstFile.Sync(); err != nil {
		return err
	}
	// Exec bits and friends survive the copy
	return os.Chmod(dst, info.Mode().Perm())
}
