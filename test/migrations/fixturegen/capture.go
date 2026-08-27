// Snapshots sqlite databases and counts what they hold
package main

import (
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Opens one database file quietly
func openDB(path string) (*gorm.DB, func(), error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, nil, err
	}
	closeFn := func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	}
	return db, closeFn, nil
}

// Snapshots one database into a gzip file
// Vacuum folds any wal into a single clean copy
func captureDB(dbPath, outPath string) error {
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("no database captured: %w", err)
	}
	db, closeDB, err := openDB(dbPath)
	if err != nil {
		return err
	}
	snap := dbPath + ".snap"
	os.Remove(snap)
	err = db.Exec("VACUUM INTO ?", snap).Error
	closeDB()
	if err != nil {
		return err
	}
	defer os.Remove(snap)

	in, err := os.Open(snap)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := outPath + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := gz.Close(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, outPath)
}

// Row counts for every user table
func tableCounts(dbPath string) (map[string]int64, error) {
	db, closeDB, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	defer closeDB()
	var names []string
	if err := db.Raw("SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name").Scan(&names).Error; err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, name := range names {
		var n int64
		if err := db.Raw("SELECT COUNT(*) FROM " + quote(name)).Scan(&n).Error; err != nil {
			return nil, err
		}
		counts[name] = n
	}
	return counts, nil
}

// Row counts for a gzipped fixture
func tableCountsGz(path string) (map[string]int64, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	tmp, err := os.CreateTemp("", "fixture-count-*.db")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, gz); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()
	return tableCounts(tmp.Name())
}

// Double quotes one identifier
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
