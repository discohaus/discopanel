package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/schema"
)

const revisionTable = "atlas_schema_revisions"

// Same columns as atlas cli so its status reads ours
const revisionTableDDL = `CREATE TABLE IF NOT EXISTS "atlas_schema_revisions" (
  "version" text NOT NULL,
  "description" text NOT NULL,
  "type" integer NOT NULL DEFAULT 2,
  "applied" integer NOT NULL DEFAULT 0,
  "total" integer NOT NULL DEFAULT 0,
  "executed_at" datetime NOT NULL,
  "execution_time" integer NOT NULL,
  "error" text NULL,
  "error_stmt" text NULL,
  "hash" text NOT NULL,
  "partial_hashes" json NULL,
  "operator_version" text NOT NULL,
  PRIMARY KEY ("version")
)`

const revisionColumns = `"version", "description", "type", "applied", "total", "executed_at", "execution_time", "error", "error_stmt", "hash", "partial_hashes", "operator_version"`

// Stores atlas revisions through whichever connection is migrating
type revisionStore struct {
	conn schema.ExecQuerier
}

func (r revisionStore) Ident() *migrate.TableIdent {
	return &migrate.TableIdent{Name: revisionTable, Schema: "main"}
}

func (r revisionStore) ReadRevisions(ctx context.Context) ([]*migrate.Revision, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT `+revisionColumns+` FROM "`+revisionTable+`" ORDER BY "version" ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var revs []*migrate.Revision
	for rows.Next() {
		rev, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		revs = append(revs, rev)
	}
	return revs, rows.Err()
}

func (r revisionStore) ReadRevision(ctx context.Context, version string) (*migrate.Revision, error) {
	rows, err := r.conn.QueryContext(ctx, `SELECT `+revisionColumns+` FROM "`+revisionTable+`" WHERE "version" = ?`, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, migrate.ErrRevisionNotExist
	}
	return scanRevision(rows)
}

func (r revisionStore) WriteRevision(ctx context.Context, rev *migrate.Revision) error {
	hashes, err := json.Marshal(rev.PartialHashes)
	if err != nil {
		return err
	}
	_, err = r.conn.ExecContext(ctx, `INSERT INTO "`+revisionTable+`" (`+revisionColumns+`)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT("version") DO UPDATE SET
  "description" = excluded."description",
  "type" = excluded."type",
  "applied" = excluded."applied",
  "total" = excluded."total",
  "executed_at" = excluded."executed_at",
  "execution_time" = excluded."execution_time",
  "error" = excluded."error",
  "error_stmt" = excluded."error_stmt",
  "hash" = excluded."hash",
  "partial_hashes" = excluded."partial_hashes",
  "operator_version" = excluded."operator_version"`,
		rev.Version, rev.Description, uint(rev.Type), rev.Applied, rev.Total, rev.ExecutedAt.UTC(),
		int64(rev.ExecutionTime), nullString(rev.Error), nullString(rev.ErrorStmt), rev.Hash,
		string(hashes), rev.OperatorVersion)
	return err
}

func (r revisionStore) DeleteRevision(ctx context.Context, version string) error {
	_, err := r.conn.ExecContext(ctx, `DELETE FROM "`+revisionTable+`" WHERE "version" = ?`, version)
	return err
}

// Reads one revision row in revisionColumns order
func scanRevision(rows *sql.Rows) (*migrate.Revision, error) {
	var (
		rev           migrate.Revision
		kind          uint
		executedAt    time.Time
		executionTime int64
		errText       sql.NullString
		errStmt       sql.NullString
		hashes        sql.NullString
	)
	if err := rows.Scan(&rev.Version, &rev.Description, &kind, &rev.Applied, &rev.Total, &executedAt,
		&executionTime, &errText, &errStmt, &rev.Hash, &hashes, &rev.OperatorVersion); err != nil {
		return nil, fmt.Errorf("scan revision: %w", err)
	}
	rev.Type = migrate.RevisionType(kind)
	rev.ExecutedAt = executedAt
	rev.ExecutionTime = time.Duration(executionTime)
	rev.Error = errText.String
	rev.ErrorStmt = errStmt.String
	if hashes.Valid && hashes.String != "" {
		if err := json.Unmarshal([]byte(hashes.String), &rev.PartialHashes); err != nil {
			return nil, fmt.Errorf("decode partial hashes: %w", err)
		}
	}
	return &rev, nil
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func ensureRevisionTable(ctx context.Context, conn schema.ExecQuerier) error {
	if _, err := conn.ExecContext(ctx, revisionTableDDL); err != nil {
		return fmt.Errorf("create revision table: %w", err)
	}
	return nil
}

// Names of every user table in the database
func tableNames(ctx context.Context, conn schema.ExecQuerier) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names[name] = true
	}
	return names, rows.Err()
}
