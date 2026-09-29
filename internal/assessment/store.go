package assessment

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const (
	EnvDBPath = "VSFLEET_HISTORY_DB"
	Driver    = "sqlite"
)

type Store struct {
	db   *sql.DB
	path string
}

const (
	leaseDuration  = 2 * time.Minute
	leaseHeartbeat = 20 * time.Second
)

var persistedKinds = []string{"vm", "host", "cluster", "datastore", "resourcepool", "dvswitch", "network", "snapshot"}

var runLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func DefaultPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvDBPath)); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "vsfleet", "history.db"), nil
}

func Open(path string) (*Store, error) {
	return open(path, false)
}

// OpenMemory opens an isolated history database backed only by process memory.
// It is used by the synthetic demo so the History panes can show deterministic
// evidence without creating or modifying a file on the operator's machine.
func OpenMemory() (*Store, error) {
	return open(":memory:", true)
}

func open(path string, memory bool) (*Store, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if memory {
		// SQLite in-memory databases are connection-local. A single pooled
		// connection keeps migrations and later history queries on the same
		// database while preserving the Store API used by file-backed history.
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
	}
	// journal_mode is recorded in the database header, so unlike the
	// connection-scoped pragmas it is set once rather than per connection.
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("configure history database: %w", err)
	}
	s := &Store{db: db, path: path}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	// A running row is recovered only when it has no live lease. Active
	// captures (including captures in another process) are left untouched.
	_ = s.recoverOrphanedRuns(context.Background())
	if !memory {
		_ = os.Chmod(path, 0o600)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// connPragmas are scoped to a single connection, so database/sql cannot be
// trusted to carry them: it opens up to MaxOpenConns physical connections
// lazily, and a pragma executed through the pool reaches only whichever
// connection happened to serve that call. Applying them in Connect is what
// makes them true of every connection -- without it the later ones open with
// busy_timeout=0, turning a concurrent writer into an immediate SQLITE_BUSY,
// and with foreign_keys=OFF, which silently skips the ON DELETE CASCADE that
// pruning a run relies on to remove its observations.
var connPragmas = []string{
	"PRAGMA busy_timeout = 5000",
	"PRAGMA foreign_keys = ON",
}

// pragmaConnector applies connPragmas to each physical connection as it is
// opened. Interposing on the connector keeps the path an opaque filename:
// passing the pragmas as dsn query parameters instead would make every path
// containing "?" open the wrong file.
type pragmaConnector struct{ driver.Connector }

func (c pragmaConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.Connector.Connect(ctx)
	if err != nil {
		return nil, err
	}
	execer, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("sqlite driver connection does not support ExecContext")
	}
	for _, stmt := range connPragmas {
		if _, err := execer.ExecContext(ctx, stmt, nil); err != nil {
			conn.Close()
			return nil, fmt.Errorf("configure history connection: %w", err)
		}
	}
	return conn, nil
}

// openDB opens path with a connection pool whose every connection carries
// connPragmas.
func openDB(path string) (*sql.DB, error) {
	base, err := sqlite.NewConnector(path)
	if err != nil {
		return nil, fmt.Errorf("open history database: %w", err)
	}
	db := sql.OpenDB(pragmaConnector{base})
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	return db, nil
}

// currentSchemaVersion is the ledger schema this build writes.
const currentSchemaVersion = 7

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read history schema version: %w", err)
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("history schema version %d is newer than this build understands", version)
	}
	if version >= currentSchemaVersion {
		return nil
	}
	if version < 6 {
		if err := s.migrateLegacy(ctx); err != nil {
			return err
		}
		if err := s.migrateV6(ctx); err != nil {
			return err
		}
	}
	return s.migrateV7(ctx)
}

// migrateV7 adds per-context source identity (the ServiceInstance About
// record). It is additive: runs written before it simply have no row, which
// readers report as not recorded rather than guessing a version.
func (s *Store) migrateV7(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history v7 migration: %w", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS context_sources (
			context_run_id INTEGER PRIMARY KEY REFERENCES context_runs(id) ON DELETE CASCADE,
			payload TEXT NOT NULL
		)`,
		`PRAGMA user_version = 7`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate history database to v7: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history v7 migration: %w", err)
	}
	return nil
}

// migrateLegacy applies the v1-v5 steps. Note that v5 never recorded its own
// version, so databases written before v6 report user_version 4.
func (s *Store) migrateLegacy(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read history schema version: %w", err)
	}
	if version == 5 {
		return s.migrateV3(ctx)
	}
	if version == 4 {
		if err := s.migrateV3(ctx); err != nil {
			return err
		}
		return s.migrateV5(ctx)
	}
	if version == 3 {
		if err := s.migrateV4(ctx); err != nil {
			return err
		}
		return s.migrateV5(ctx)
	}
	if version == 2 {
		if err := s.migrateV3(ctx); err != nil {
			return err
		}
		if err := s.migrateV4(ctx); err != nil {
			return err
		}
		return s.migrateV5(ctx)
	}
	if version == 1 {
		stmts := []string{
			`ALTER TABLE runs ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN note TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE runs ADD COLUMN tool_version TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE runs ADD COLUMN inventory_schema_version TEXT NOT NULL DEFAULT ''`,
			`CREATE UNIQUE INDEX IF NOT EXISTS runs_label_unique ON runs(label COLLATE NOCASE) WHERE label <> ''`,
			`CREATE INDEX IF NOT EXISTS vm_name ON vm_observations(name COLLATE NOCASE)`,
			`CREATE TABLE IF NOT EXISTS capture_lease (id INTEGER PRIMARY KEY CHECK(id=1), token TEXT NOT NULL, run_id INTEGER NOT NULL DEFAULT 0, acquired_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
			`PRAGMA user_version = 2`,
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin history migration: %w", err)
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("migrate history database: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit history migration: %w", err)
		}
		if err := s.migrateV3(ctx); err != nil {
			return err
		}
		if err := s.migrateV4(ctx); err != nil {
			return err
		}
		return s.migrateV5(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history migration: %w", err)
	}
	stmts := []string{
		`CREATE TABLE runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			source TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			status TEXT NOT NULL,
			requested_contexts INTEGER NOT NULL DEFAULT 0,
			successful_contexts INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE context_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			endpoint TEXT NOT NULL,
			datacenter TEXT NOT NULL DEFAULT '',
			vcenter_id TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			vm_status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			UNIQUE(run_id, name)
		)`,
		`CREATE TABLE vm_observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			context_run_id INTEGER NOT NULL REFERENCES context_runs(id) ON DELETE CASCADE,
			moref TEXT NOT NULL,
			instance_uuid TEXT NOT NULL DEFAULT '',
			bios_uuid TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			payload BLOB NOT NULL,
			UNIQUE(context_run_id, moref)
		)`,
		`CREATE TABLE snapshot_observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			vm_observation_id INTEGER NOT NULL REFERENCES vm_observations(id) ON DELETE CASCADE,
			moref TEXT NOT NULL,
			numeric_id INTEGER NOT NULL,
			parent_moref TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			create_time INTEGER NOT NULL,
			power_state TEXT NOT NULL,
			quiesced INTEGER NOT NULL,
			current_snapshot INTEGER NOT NULL
		)`,
		`CREATE INDEX vm_identity_instance ON vm_observations(instance_uuid)`,
		`CREATE INDEX vm_identity_bios ON vm_observations(bios_uuid)`,
		`CREATE INDEX vm_name ON vm_observations(name COLLATE NOCASE)`,
		`CREATE INDEX snapshots_time ON snapshot_observations(create_time)`,
		`ALTER TABLE runs ADD COLUMN label TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN note TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN pinned INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN tool_version TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN inventory_schema_version TEXT NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX runs_label_unique ON runs(label COLLATE NOCASE) WHERE label <> ''`,
		`CREATE TABLE capture_lease (id INTEGER PRIMARY KEY CHECK(id=1), token TEXT NOT NULL, run_id INTEGER NOT NULL DEFAULT 0, acquired_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`PRAGMA user_version = 2`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate history database: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history migration: %w", err)
	}
	if err := s.migrateV3(ctx); err != nil {
		return err
	}
	if err := s.migrateV4(ctx); err != nil {
		return err
	}
	return s.migrateV5(ctx)
}

// migrateV3 adds per-resource collection status and durable infrastructure
// observations. The migration is additive and leaves all v1/v2 VM history
// untouched; old runs simply have no infrastructure coverage.
func (s *Store) migrateV3(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read history schema version: %w", err)
	}
	if version >= 3 {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin history v3 compatibility migration: %w", err)
		}
		stmts := []string{
			`ALTER TABLE runs ADD COLUMN requested_collections INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE runs ADD COLUMN successful_collections INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE capture_lease ADD COLUMN operation TEXT NOT NULL DEFAULT 'capture'`,
			`CREATE TABLE IF NOT EXISTS context_collections (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				context_run_id INTEGER NOT NULL REFERENCES context_runs(id) ON DELETE CASCADE,
				kind TEXT NOT NULL,
				started_at INTEGER NOT NULL,
				finished_at INTEGER,
				status TEXT NOT NULL,
				error TEXT NOT NULL DEFAULT '',
				item_count INTEGER NOT NULL DEFAULT 0,
				UNIQUE(context_run_id, kind)
			)`,
			`CREATE TABLE IF NOT EXISTS resource_observations (
				id INTEGER PRIMARY KEY AUTOINCREMENT,
				collection_id INTEGER NOT NULL REFERENCES context_collections(id) ON DELETE CASCADE,
				kind TEXT NOT NULL,
				moref TEXT NOT NULL,
				name TEXT NOT NULL,
				payload BLOB NOT NULL,
				cpu_capacity REAL,
				cpu_used REAL,
				memory_capacity REAL,
				memory_used REAL,
				storage_capacity REAL,
				storage_free REAL,
				UNIQUE(collection_id, moref)
			)`,
			`ALTER TABLE resource_observations ADD COLUMN cpu_capacity REAL`,
			`ALTER TABLE resource_observations ADD COLUMN cpu_used REAL`,
			`ALTER TABLE resource_observations ADD COLUMN memory_capacity REAL`,
			`ALTER TABLE resource_observations ADD COLUMN memory_used REAL`,
			`ALTER TABLE resource_observations ADD COLUMN storage_capacity REAL`,
			`ALTER TABLE resource_observations ADD COLUMN storage_free REAL`,
			`CREATE INDEX IF NOT EXISTS collection_kind ON context_collections(kind, status)`,
			`CREATE INDEX IF NOT EXISTS resource_kind_identity ON resource_observations(kind, moref)`,
			`CREATE INDEX IF NOT EXISTS resource_name ON resource_observations(name COLLATE NOCASE)`,
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				_ = tx.Rollback()
				return fmt.Errorf("migrate history v3 compatibility schema: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit history v3 compatibility migration: %w", err)
		}
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history v3 migration: %w", err)
	}
	stmts := []string{
		`ALTER TABLE runs ADD COLUMN requested_collections INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN successful_collections INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE capture_lease ADD COLUMN operation TEXT NOT NULL DEFAULT 'capture'`,
		`CREATE TABLE IF NOT EXISTS context_collections (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			context_run_id INTEGER NOT NULL REFERENCES context_runs(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			item_count INTEGER NOT NULL DEFAULT 0,
			UNIQUE(context_run_id, kind)
		)`,
		`CREATE TABLE IF NOT EXISTS resource_observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collection_id INTEGER NOT NULL REFERENCES context_collections(id) ON DELETE CASCADE,
			kind TEXT NOT NULL,
			moref TEXT NOT NULL,
			name TEXT NOT NULL,
			payload BLOB NOT NULL,
			cpu_capacity REAL,
			cpu_used REAL,
			memory_capacity REAL,
			memory_used REAL,
			storage_capacity REAL,
			storage_free REAL,
			UNIQUE(collection_id, moref)
		)`,
		`ALTER TABLE resource_observations ADD COLUMN cpu_capacity REAL`,
		`ALTER TABLE resource_observations ADD COLUMN cpu_used REAL`,
		`ALTER TABLE resource_observations ADD COLUMN memory_capacity REAL`,
		`ALTER TABLE resource_observations ADD COLUMN memory_used REAL`,
		`ALTER TABLE resource_observations ADD COLUMN storage_capacity REAL`,
		`ALTER TABLE resource_observations ADD COLUMN storage_free REAL`,
		`CREATE INDEX IF NOT EXISTS collection_kind ON context_collections(kind, status)`,
		`CREATE INDEX IF NOT EXISTS resource_kind_identity ON resource_observations(kind, moref)`,
		`CREATE INDEX IF NOT EXISTS resource_name ON resource_observations(name COLLATE NOCASE)`,
		`PRAGMA user_version = 3`,
	}
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			// ALTER TABLE is intentionally idempotent for partially applied
			// migrations. A duplicate-column error means the remaining tables
			// still need to be completed.
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				continue
			}
			_ = tx.Rollback()
			return fmt.Errorf("migrate history database to v3: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history v3 migration: %w", err)
	}
	return nil
}

// migrateV4 recomputes host cpu_capacity projections from stored lossless
// payloads (Issue #111: host CPU capacity was previously projected as per-core
// MHz instead of total host CPU capacity).
func (s *Store) migrateV4(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read history schema version: %w", err)
	}
	if version >= 4 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history v4 migration: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, payload FROM resource_observations WHERE kind = 'host'`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("query host observations for v4 migration: %w", err)
	}
	type hostCapUpdate struct {
		id  int64
		cap float64
	}
	var updates []hostCapUpdate
	for rows.Next() {
		var id int64
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			_ = tx.Rollback()
			return fmt.Errorf("scan host observation for v4 migration: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err == nil {
			if c, ok := hostCPUCapacity(m); ok {
				updates = append(updates, hostCapUpdate{id: id, cap: c})
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("iterate host observations for v4 migration: %w", err)
	}
	for _, u := range updates {
		if _, err := tx.ExecContext(ctx, `UPDATE resource_observations SET cpu_capacity = ? WHERE id = ?`, u.cap, u.id); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("update host cpu_capacity for v4 migration: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set history schema version to 4: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history v4 migration: %w", err)
	}
	return nil
}

// migrateV5 records metadata-source coverage per collection. The additive
// columns are safe to apply repeatedly while a database is being developed.
func (s *Store) migrateV5(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read history schema version: %w", err)
	}
	if version > 4 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history v5 migration: %w", err)
	}
	for _, stmt := range []string{
		`ALTER TABLE context_collections ADD COLUMN tags_status TEXT NOT NULL DEFAULT 'unavailable'`,
		`ALTER TABLE context_collections ADD COLUMN tags_error TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE context_collections ADD COLUMN custom_attributes_status TEXT NOT NULL DEFAULT 'unavailable'`,
		`ALTER TABLE context_collections ADD COLUMN custom_attributes_error TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			_ = tx.Rollback()
			return fmt.Errorf("migrate history database to v5: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history v5 migration: %w", err)
	}
	return nil
}

func fromMillis(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return time.UnixMilli(v.Int64).UTC()
}

func (s *Store) recoverOrphanedRuns(ctx context.Context) error {
	// Older databases have no lease rows. Treat those rows as interrupted on
	// open for compatibility, while never touching a run protected by a live
	// lease in the current schema.
	now := time.Now().UTC().UnixMilli()
	_, err := s.db.ExecContext(ctx, `UPDATE context_runs SET vm_status='failed', error=CASE WHEN error='' THEN 'assessment interrupted' ELSE error END, finished_at=? WHERE vm_status='running' AND NOT EXISTS (SELECT 1 FROM capture_lease WHERE capture_lease.expires_at>?)`, now, now)
	if err != nil {
		return err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE context_collections SET status='failed', error=CASE WHEN error='' THEN 'assessment interrupted' ELSE error END, finished_at=? WHERE status='running' AND NOT EXISTS (SELECT 1 FROM capture_lease WHERE capture_lease.expires_at>?)`, now, now)
	_, err = s.db.ExecContext(ctx, `UPDATE runs SET status='failed', finished_at=? WHERE status='running' AND NOT EXISTS (SELECT 1 FROM capture_lease WHERE capture_lease.expires_at>?)`, now, now)
	return err
}

func newLeaseToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", b), nil
}

// AcquireCaptureLease reserves the database for one writer. Expired leases
// are reclaimed atomically, so two scheduled captures cannot both proceed.
func (s *Store) AcquireCaptureLease(ctx context.Context, now time.Time) (CaptureLease, error) {
	return s.AcquireOperationLease(ctx, now, "capture")
}

// AcquireOperationLease fences every mutating ledger operation, not only live
// captures. The operation is informational in the table but is returned to
// callers so diagnostics can explain who owns the writer slot.
func (s *Store) AcquireOperationLease(ctx context.Context, now time.Time, operation string) (CaptureLease, error) {
	if operation == "" {
		operation = "operation"
	}
	token, err := newLeaseToken()
	if err != nil {
		return CaptureLease{}, err
	}
	if err := s.acquireSidecarLease(token, now, now.Add(leaseDuration)); err != nil {
		return CaptureLease{}, err
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = s.releaseSidecarLease(token)
		}
	}()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CaptureLease{}, err
	}
	defer tx.Rollback()
	nowMS := now.UTC().UnixMilli()
	if _, err := tx.ExecContext(ctx, `DELETE FROM capture_lease WHERE id=1 AND expires_at<=?`, nowMS); err != nil {
		return CaptureLease{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO capture_lease(id,token,run_id,operation,acquired_at,expires_at) VALUES(1,?,0,?,?,?)`, token, operation, nowMS, now.Add(leaseDuration).UTC().UnixMilli()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return CaptureLease{}, fmt.Errorf("another assessment capture is already running")
		}
		return CaptureLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return CaptureLease{}, err
	}
	lockHeld = false
	return CaptureLease{Token: token, Operation: operation, ExpiresAt: now.Add(leaseDuration).UTC()}, nil
}

func (s *Store) SetCaptureLeaseRun(ctx context.Context, lease CaptureLease, runID int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE capture_lease SET run_id=? WHERE id=1 AND token=? AND expires_at>?`, runID, lease.Token, time.Now().UTC().UnixMilli())
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("assessment capture lease is no longer valid")
	}
	return nil
}

func (s *Store) RenewCaptureLease(ctx context.Context, lease CaptureLease, now time.Time) (CaptureLease, error) {
	expires := now.Add(leaseDuration).UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE capture_lease SET expires_at=? WHERE id=1 AND token=? AND expires_at>?`, expires.UnixMilli(), lease.Token, now.UTC().UnixMilli())
	if err != nil {
		return CaptureLease{}, err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return CaptureLease{}, fmt.Errorf("assessment capture lease is no longer valid")
	}
	lease.ExpiresAt = expires
	if err := s.renewSidecarLease(lease.Token, expires); err != nil {
		return CaptureLease{}, err
	}
	return lease, nil
}

func (s *Store) ReleaseCaptureLease(ctx context.Context, lease CaptureLease) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM capture_lease WHERE id=1 AND token=?`, lease.Token)
	_ = s.releaseSidecarLease(lease.Token)
	return err
}

func (s *Store) sidecarPath() string {
	if s.path == "" || s.path == ":memory:" {
		return ""
	}
	return s.path + ".lock"
}

func (s *Store) acquireSidecarLease(token string, now, expires time.Time) error {
	path := s.sidecarPath()
	if path == "" {
		return nil
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err == nil {
		_, _ = fmt.Fprintf(f, "%d %s\n", expires.UnixMilli(), token)
		_ = f.Close()
		return nil
	} else if !os.IsExist(err) {
		return err
	}
	b, err := os.ReadFile(path)
	if err == nil {
		var expiry int64
		var owner string
		if _, scanErr := fmt.Sscanf(string(b), "%d %s", &expiry, &owner); scanErr == nil && expiry <= now.UTC().UnixMilli() {
			_ = os.Remove(path)
			if f, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); createErr == nil {
				_, _ = fmt.Fprintf(f, "%d %s\n", expires.UnixMilli(), token)
				_ = f.Close()
				return nil
			}
		}
	}
	return fmt.Errorf("another assessment operation is already running")
}

func (s *Store) renewSidecarLease(token string, expires time.Time) error {
	path := s.sidecarPath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var owner string
	var expiry int64
	if _, err := fmt.Sscanf(string(b), "%d %s", &expiry, &owner); err != nil || owner != token {
		return fmt.Errorf("assessment operation sidecar lease is no longer valid")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "%d %s\n", expires.UnixMilli(), token)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func (s *Store) releaseSidecarLease(token string) error {
	path := s.sidecarPath()
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var owner string
	var expiry int64
	if _, err := fmt.Sscanf(string(b), "%d %s", &expiry, &owner); err != nil || owner != token {
		return nil
	}
	return os.Remove(path)
}

func (s *Store) ValidateCaptureLease(ctx context.Context, lease CaptureLease) error {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM capture_lease WHERE id=1 AND token=? AND expires_at>?`, lease.Token, time.Now().UTC().UnixMilli()).Scan(&n)
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("assessment capture lease is no longer valid")
	}
	return nil
}

func validateRunLabel(label string) error {
	if label == "" {
		return nil
	}
	if !runLabelPattern.MatchString(label) {
		return fmt.Errorf("run label must match [A-Za-z0-9][A-Za-z0-9._-]{0,63}")
	}
	if strings.EqualFold(label, "latest") || strings.EqualFold(label, "previous") {
		return fmt.Errorf("run label %q is reserved", label)
	}
	return nil
}

func (s *Store) StartRun(ctx context.Context, source string, contexts []*config.Context, now time.Time) (Run, error) {
	return s.StartRunWithMetadata(ctx, source, contexts, now, RunMetadata{})
}

func (s *Store) StartRunWithMetadata(ctx context.Context, source string, contexts []*config.Context, now time.Time, meta RunMetadata) (Run, error) {
	if source == "" {
		source = "cli"
	}
	if err := validateRunLabel(meta.Label); err != nil {
		return Run{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO runs(source,started_at,status,requested_contexts,requested_collections,label,note,pinned,tool_version,inventory_schema_version) VALUES(?,?,?,?,?,?,?,?,?,?)`, source, now.UnixMilli(), RunRunning, len(contexts), len(contexts)*len(persistedKinds), meta.Label, meta.Note, boolInt(meta.Pinned), meta.ToolVersion, meta.InventorySchemaVersion)
	if err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return Run{}, err
	}
	for _, cc := range contexts {
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_runs(run_id,name,endpoint,datacenter,started_at,vm_status) VALUES(?,?,?,?,?,?)`, id, cc.Name, cc.Endpoint, cc.Datacenter, now.UnixMilli(), "running"); err != nil {
			_ = tx.Rollback()
			return Run{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Run{}, err
	}
	return Run{ID: id, Source: source, Label: meta.Label, Note: meta.Note, Pinned: meta.Pinned, ToolVersion: meta.ToolVersion, InventorySchemaVersion: meta.InventorySchemaVersion, StartedAt: now.UTC(), Status: RunRunning, RequestedContexts: len(contexts)}, nil
}

func (s *Store) SaveContext(ctx context.Context, runID int64, result ContextResult, now time.Time) error {
	return s.saveContext(ctx, runID, result, now, nil)
}

func (s *Store) SaveContextWithLease(ctx context.Context, runID int64, result ContextResult, now time.Time, lease CaptureLease) error {
	return s.saveContext(ctx, runID, result, now, &lease)
}

func (s *Store) saveContext(ctx context.Context, runID int64, result ContextResult, now time.Time, lease *CaptureLease) error {
	if lease != nil {
		if err := s.ValidateCaptureLease(ctx, *lease); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	status := result.Status
	if status == "" {
		status = "success"
	}
	_, err = tx.ExecContext(ctx, `UPDATE context_runs SET vcenter_id=?,finished_at=?,vm_status=?,error=? WHERE run_id=? AND name=?`, result.VCenterID, now.UnixMilli(), status, result.Error, runID, result.Name)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	var contextRunID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM context_runs WHERE run_id=? AND name=?`, runID, result.Name).Scan(&contextRunID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if result.Source != nil {
		payload, err := json.Marshal(result.Source)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO context_sources(context_run_id,payload) VALUES(?,?) ON CONFLICT(context_run_id) DO UPDATE SET payload=excluded.payload`, contextRunID, string(payload)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	for _, vm := range result.VMs {
		payload, err := json.Marshal(vm.VM)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO vm_observations(context_run_id,moref,instance_uuid,bios_uuid,name,payload) VALUES(?,?,?,?,?,?)`, contextRunID, vm.VM.ID, vm.VM.InstanceUUID, vm.VM.BIOSUUID, vm.VM.Name, payload)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		vmID, err := res.LastInsertId()
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, snap := range vm.VM.Snapshots {
			_, err = tx.ExecContext(ctx, `INSERT INTO snapshot_observations(vm_observation_id,moref,numeric_id,parent_moref,name,description,create_time,power_state,quiesced,current_snapshot) VALUES(?,?,?,?,?,?,?,?,?,?)`, vmID, snap.ID, snap.NumericID, snap.ParentID, snap.Name, snap.Description, snap.CreateTime.UnixMilli(), snap.PowerState, boolInt(snap.Quiesced), boolInt(snap.Current))
			if err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	for _, collection := range result.Collections {
		kind := strings.ToLower(strings.TrimSpace(collection.Kind))
		if kind == "" {
			continue
		}
		status := collection.Status
		if status == "" {
			status = "success"
		}
		itemCount := collection.ItemCount
		if itemCount == 0 && len(collection.Resources) > 0 {
			itemCount = len(collection.Resources)
		}
		tagsStatus, customStatus := collection.TagsStatus, collection.CustomAttributesStatus
		if tagsStatus == "" {
			tagsStatus = "unavailable"
		}
		if customStatus == "" {
			customStatus = "unavailable"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO context_collections(context_run_id,kind,started_at,finished_at,status,error,item_count,tags_status,tags_error,custom_attributes_status,custom_attributes_error) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(context_run_id,kind) DO UPDATE SET finished_at=excluded.finished_at,status=excluded.status,error=excluded.error,item_count=excluded.item_count,tags_status=excluded.tags_status,tags_error=excluded.tags_error,custom_attributes_status=excluded.custom_attributes_status,custom_attributes_error=excluded.custom_attributes_error`, contextRunID, kind, now.UnixMilli(), now.UnixMilli(), status, collection.Error, itemCount, tagsStatus, collection.TagsError, customStatus, collection.CustomAttributesError)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		var collectionID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM context_collections WHERE context_run_id=? AND kind=?`, contextRunID, kind).Scan(&collectionID); err != nil {
			_ = tx.Rollback()
			return err
		}
		for _, resource := range collection.Resources {
			moref := resource.ID
			payload := resource.Payload
			if len(payload) == 0 {
				payload, _ = json.Marshal(resource)
			}
			if moref == "" || resource.Name == "" {
				var identity struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				_ = json.Unmarshal(payload, &identity)
				if moref == "" {
					moref = identity.ID
				}
				if resource.Name == "" {
					resource.Name = identity.Name
				}
			}
			if moref == "" {
				moref = resource.Name
			}
			if moref == "" {
				continue
			}
			if len(payload) == 0 {
				payload, err = json.Marshal(resource)
				if err != nil {
					_ = tx.Rollback()
					return err
				}
			}
			metrics := resourceMetrics(kind, payload)
			_, err = tx.ExecContext(ctx, `INSERT INTO resource_observations(collection_id,kind,moref,name,payload,cpu_capacity,cpu_used,memory_capacity,memory_used,storage_capacity,storage_free) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(collection_id,moref) DO UPDATE SET kind=excluded.kind,name=excluded.name,payload=excluded.payload,cpu_capacity=excluded.cpu_capacity,cpu_used=excluded.cpu_used,memory_capacity=excluded.memory_capacity,memory_used=excluded.memory_used,storage_capacity=excluded.storage_capacity,storage_free=excluded.storage_free`, collectionID, kind, moref, resource.Name, payload, metrics[0], metrics[1], metrics[2], metrics[3], metrics[4], metrics[5])
			if err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}
	if lease != nil {
		var token string
		if err := tx.QueryRowContext(ctx, `SELECT token FROM capture_lease WHERE id=1 AND expires_at>?`, time.Now().UTC().UnixMilli()).Scan(&token); err != nil || token != lease.Token {
			_ = tx.Rollback()
			return fmt.Errorf("assessment capture lease is no longer valid")
		}
	}
	return tx.Commit()
}

type ContextResult struct {
	Name      string
	VCenterID string
	Status    string
	Error     string
	// Source is the ServiceInstance About record observed on connect. Leave
	// it nil when the context never connected.
	Source      *SourceInfo
	VMs         []Observation
	Collections []CollectionResult
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *Store) FinishRun(ctx context.Context, runID int64, now time.Time) (Run, error) {
	return s.finishRun(ctx, runID, now, nil)
}

func (s *Store) FinishRunWithLease(ctx context.Context, runID int64, now time.Time, lease CaptureLease) (Run, error) {
	return s.finishRun(ctx, runID, now, &lease)
}

func (s *Store) finishRun(ctx context.Context, runID int64, now time.Time, lease *CaptureLease) (Run, error) {
	if lease != nil {
		if err := s.ValidateCaptureLease(ctx, *lease); err != nil {
			return Run{}, err
		}
	}
	var requested, success, failed int
	var requestedCollections int
	if err := s.db.QueryRowContext(ctx, `SELECT requested_contexts, successful_contexts, requested_collections FROM runs WHERE id=?`, runID).Scan(&requested, &success, &requestedCollections); err != nil {
		return Run{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM context_runs WHERE run_id=? AND vm_status IN ('success','empty')`, runID).Scan(&success); err != nil {
		return Run{}, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM context_runs WHERE run_id=? AND vm_status='running'`, runID).Scan(&failed); err != nil {
		return Run{}, err
	}
	status := RunPartial
	successfulCollections := 0
	var collectedCollections int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM context_collections WHERE context_run_id IN (SELECT id FROM context_runs WHERE run_id=?)`, runID).Scan(&collectedCollections); err != nil {
		return Run{}, err
	}
	if collectedCollections > 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM context_collections WHERE context_run_id IN (SELECT id FROM context_runs WHERE run_id=?) AND status IN ('success','empty')`, runID).Scan(&successfulCollections); err != nil {
			return Run{}, err
		}
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM context_collections WHERE context_run_id IN (SELECT id FROM context_runs WHERE run_id=?) AND status='running'`, runID).Scan(&failed); err != nil {
			return Run{}, err
		}
		if requestedCollections == 0 {
			requestedCollections = collectedCollections
		}
		if successfulCollections == 0 {
			status = RunFailed
		} else if successfulCollections == requestedCollections && failed == 0 && collectedCollections == requestedCollections {
			status = RunComplete
		}
	} else if success == 0 {
		status = RunFailed
	} else if success == requested && failed == 0 {
		status = RunComplete
	}
	query := `UPDATE runs SET finished_at=?,status=?,successful_contexts=?,successful_collections=? WHERE id=?`
	args := []any{now.UnixMilli(), status, success, successfulCollections, runID}
	if lease != nil {
		query += ` AND EXISTS (SELECT 1 FROM capture_lease WHERE id=1 AND token=? AND expires_at>?)`
		args = append(args, lease.Token, now.UTC().UnixMilli())
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return Run{}, err
	}
	if lease != nil {
		n, _ := res.RowsAffected()
		if n != 1 {
			return Run{}, fmt.Errorf("assessment capture lease is no longer valid")
		}
	}
	return s.GetRun(ctx, runID)
}

func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	var r Run
	var start, finish sql.NullInt64
	var pinned int
	err := s.db.QueryRowContext(ctx, `SELECT id,source,label,note,pinned,tool_version,inventory_schema_version,started_at,finished_at,status,requested_contexts,successful_contexts,requested_collections,successful_collections FROM runs WHERE id=?`, id).Scan(&r.ID, &r.Source, &r.Label, &r.Note, &pinned, &r.ToolVersion, &r.InventorySchemaVersion, &start, &finish, &r.Status, &r.RequestedContexts, &r.SuccessfulContexts, &r.RequestedCollections, &r.SuccessfulCollections)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("assessment %d not found", id)
	}
	if err != nil {
		return Run{}, err
	}
	r.StartedAt, r.FinishedAt = fromMillis(start), fromMillis(finish)
	r.Pinned = pinned != 0
	return r, nil
}

func (s *Store) Runs(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,source,label,note,pinned,tool_version,inventory_schema_version,started_at,finished_at,status,requested_contexts,successful_contexts,requested_collections,successful_collections FROM runs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var start, finish sql.NullInt64
		var pinned int
		if err := rows.Scan(&r.ID, &r.Source, &r.Label, &r.Note, &pinned, &r.ToolVersion, &r.InventorySchemaVersion, &start, &finish, &r.Status, &r.RequestedContexts, &r.SuccessfulContexts, &r.RequestedCollections, &r.SuccessfulCollections); err != nil {
			return nil, err
		}
		r.StartedAt, r.FinishedAt = fromMillis(start), fromMillis(finish)
		r.Pinned = pinned != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateRun(ctx context.Context, selector string, meta RunMetadata) (Run, error) {
	id, err := s.ResolveRun(ctx, selector)
	if err != nil {
		return Run{}, err
	}
	if meta.Label != "" {
		if err := validateRunLabel(meta.Label); err != nil {
			return Run{}, err
		}
	}
	// Empty label means “leave it unchanged” for this API; callers that need
	// to clear a label can use ClearLabel.
	sets := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if meta.Label != "" {
		sets = append(sets, "label=?")
		args = append(args, meta.Label)
	}
	if meta.Note != "" {
		sets = append(sets, "note=?")
		args = append(args, meta.Note)
	}
	if len(sets) == 0 && !meta.Pinned {
		return Run{}, fmt.Errorf("at least one run metadata change is required")
	}
	if meta.Pinned {
		sets = append(sets, "pinned=1")
	}
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, id)
}

// UpdateRunFields is the lossless metadata update used by the CLI. Nil means
// “leave unchanged”, allowing an explicit empty note or cleared label.
func (s *Store) UpdateRunFields(ctx context.Context, selector string, label, note *string, pinned *bool) (Run, error) {
	id, err := s.ResolveRun(ctx, selector)
	if err != nil {
		return Run{}, err
	}
	sets := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if label != nil {
		if err := validateRunLabel(*label); err != nil {
			return Run{}, err
		}
		sets = append(sets, "label=?")
		args = append(args, *label)
	}
	if note != nil {
		sets = append(sets, "note=?")
		args = append(args, *note)
	}
	if pinned != nil {
		sets = append(sets, "pinned=?")
		args = append(args, boolInt(*pinned))
	}
	if len(sets) == 0 {
		return Run{}, fmt.Errorf("at least one run metadata change is required")
	}
	args = append(args, id)
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET `+strings.Join(sets, ",")+` WHERE id=?`, args...); err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, id)
}

func (s *Store) SetRunPinned(ctx context.Context, selector string, pinned bool) (Run, error) {
	id, err := s.ResolveRun(ctx, selector)
	if err != nil {
		return Run{}, err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE runs SET pinned=? WHERE id=?`, boolInt(pinned), id); err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, id)
}

func (s *Store) ResolveRun(ctx context.Context, selector string) (int64, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return 0, fmt.Errorf("assessment selector is empty")
	}
	if selector == "latest" || selector == "previous" {
		runs, err := s.Runs(ctx)
		if err != nil {
			return 0, err
		}
		if selector == "latest" {
			if len(runs) < 1 {
				return 0, fmt.Errorf("no assessments stored")
			}
			return runs[0].ID, nil
		}
		if len(runs) < 2 {
			return 0, fmt.Errorf("no previous assessment")
		}
		return runs[1].ID, nil
	}
	if id, err := strconv.ParseInt(selector, 10, 64); err == nil {
		if _, err := s.GetRun(ctx, id); err != nil {
			return 0, err
		}
		return id, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM runs WHERE label=? COLLATE NOCASE`, selector).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("assessment selector %q not found", selector)
	}
	return id, err
}

func (s *Store) DeleteRun(ctx context.Context, id int64) error {
	var pinned int
	if err := s.db.QueryRowContext(ctx, `SELECT pinned FROM runs WHERE id=?`, id).Scan(&pinned); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("assessment %d not found", id)
	} else if err != nil {
		return err
	} else if pinned != 0 {
		return fmt.Errorf("assessment %d is pinned; unpin it before deletion", id)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE id=?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("assessment %d not found", id)
	}
	return nil
}

func (s *Store) DeleteRunSelector(ctx context.Context, selector string) error {
	id, err := s.ResolveRun(ctx, selector)
	if err != nil {
		return err
	}
	return s.DeleteRun(ctx, id)
}

func (s *Store) SnapshotAges(ctx context.Context, runID int64, olderThan time.Duration) ([]SnapshotAge, error) {
	return s.snapshotAges(ctx, runID, olderThan)
}

// SnapshotAgesForContexts limits snapshot evidence to context rows recorded
// in the selected run.
func (s *Store) SnapshotAgesForContexts(ctx context.Context, runID int64, olderThan time.Duration, selectors []string) ([]SnapshotAge, error) {
	ages, err := s.snapshotAges(ctx, runID, olderThan)
	if err != nil {
		return nil, err
	}
	contexts, err := s.ContextRuns(ctx, runID)
	if err != nil {
		return nil, err
	}
	selected, err := ValidateStoredContexts(selectors, contexts)
	if err != nil {
		return nil, err
	}
	if len(selected) == 0 {
		return ages, nil
	}
	out := ages[:0]
	for _, age := range ages {
		if contextSelected(age.Context, selected) {
			out = append(out, age)
		}
	}
	return out, nil
}

func (s *Store) History(ctx context.Context, query, contextName string) ([]VMHistoryEntry, error) {
	selectors := []string(nil)
	if strings.TrimSpace(contextName) != "" {
		selectors = []string{contextName}
	}
	return s.HistoryForContexts(ctx, query, selectors)
}

// HistoryForContexts searches stored VM history across the selected ledger
// contexts. Selectors are validated against all recorded runs.
func (s *Store) HistoryForContexts(ctx context.Context, query string, selectors []string) ([]VMHistoryEntry, error) {
	runs, err := s.Runs(ctx)
	if err != nil {
		return nil, err
	}
	var allContexts []ContextRun
	for _, run := range runs {
		contexts, err := s.ContextRuns(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		allContexts = append(allContexts, contexts...)
	}
	selected, err := ValidateStoredContexts(selectors, allContexts)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	var out []VMHistoryEntry
	for _, run := range runs {
		byContext, contexts, err := s.loadVMs(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		for id, c := range contexts {
			if !contextSelected(c.Name, selected) {
				continue
			}
			for _, v := range byContext[id] {
				vm := v.observation.VM
				matches := needle == "" || strings.EqualFold(vm.Name, needle) || strings.EqualFold(vm.ID, needle) || strings.EqualFold(vm.InstanceUUID, needle) || strings.EqualFold(vm.BIOSUUID, needle)
				if matches {
					out = append(out, VMHistoryEntry{Run: run, Observation: v.observation, Snapshots: v.snapshots})
				}
			}
		}
	}
	return out, nil
}

type storedVM struct {
	observation Observation
	snapshots   []vsphere.VMSnapshot
	seen        time.Time
}

func (s *Store) loadVMs(ctx context.Context, runID int64) (map[int64][]storedVM, map[int64]ContextRun, error) {
	contexts := make(map[int64]ContextRun)
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,endpoint,datacenter,vcenter_id,started_at,finished_at,vm_status,error FROM context_runs WHERE run_id=?`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c ContextRun
		var start, finish sql.NullInt64
		if err := rows.Scan(&id, &c.Name, &c.Endpoint, &c.Datacenter, &c.VCenterID, &start, &finish, &c.VMStatus, &c.Error); err != nil {
			return nil, nil, err
		}
		c.RunID = runID
		c.StartedAt, c.FinishedAt = fromMillis(start), fromMillis(finish)
		contexts[id] = c
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	collectionRows, err := s.db.QueryContext(ctx, `SELECT cc.context_run_id,cc.kind,cc.started_at,cc.finished_at,cc.status,cc.error,cc.item_count,cc.tags_status,cc.tags_error,cc.custom_attributes_status,cc.custom_attributes_error FROM context_collections cc JOIN context_runs cr ON cr.id=cc.context_run_id WHERE cr.run_id=?`, runID)
	if err == nil {
		for collectionRows.Next() {
			var contextID int64
			var c CollectionRun
			var start, finish sql.NullInt64
			if scanErr := collectionRows.Scan(&contextID, &c.Kind, &start, &finish, &c.Status, &c.Error, &c.ItemCount, &c.TagsStatus, &c.TagsError, &c.CustomAttributesStatus, &c.CustomAttributesError); scanErr != nil {
				collectionRows.Close()
				return nil, nil, scanErr
			}
			c.StartedAt, c.FinishedAt = fromMillis(start), fromMillis(finish)
			if context, ok := contexts[contextID]; ok {
				context.Collections = append(context.Collections, c)
				contexts[contextID] = context
			}
		}
		if scanErr := collectionRows.Err(); scanErr != nil {
			collectionRows.Close()
			return nil, nil, scanErr
		}
		collectionRows.Close()
	} else {
		return nil, nil, err
	}
	byContext := make(map[int64][]storedVM)
	for id := range contexts {
		// vm_observations is fully drained into memory before loadSnapshots
		// issues its own query. A store opened with OpenMemory pins the pool
		// to exactly one physical connection — see open()'s memory branch —
		// so a nested query issued while this one is still open would have
		// nowhere to go: the single connection is held by this very read and
		// can only be released by finishing it, which the nested query is
		// blocking on. That is a deadlock, not contention that clears on its
		// own, and file-backed stores' larger pool only ever hid it.
		rows, err := s.db.QueryContext(ctx, `SELECT id,moref,instance_uuid,bios_uuid,payload FROM vm_observations WHERE context_run_id=?`, id)
		if err != nil {
			return nil, nil, err
		}
		type rawVM struct {
			id            int64
			moref, iu, bu string
			payload       []byte
		}
		var raw []rawVM
		for rows.Next() {
			var v rawVM
			if err := rows.Scan(&v.id, &v.moref, &v.iu, &v.bu, &v.payload); err != nil {
				rows.Close()
				return nil, nil, err
			}
			raw = append(raw, v)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rows.Close()
		for _, v := range raw {
			var vm vsphere.VM
			if err := json.Unmarshal(v.payload, &vm); err != nil {
				return nil, nil, err
			}
			vm.ID = v.moref
			vm.InstanceUUID = v.iu
			vm.BIOSUUID = v.bu
			snaps, err := s.loadSnapshots(ctx, v.id)
			if err != nil {
				return nil, nil, err
			}
			byContext[id] = append(byContext[id], storedVM{observation: Observation{VCenterID: contexts[id].VCenterID, Context: contexts[id].Name, VM: vm}, snapshots: snaps, seen: contexts[id].FinishedAt})
		}
	}
	return byContext, contexts, nil
}

// ContextRuns returns context and per-kind collection status for one run.
func (s *Store) ContextRuns(ctx context.Context, runID int64) ([]ContextRun, error) {
	_, contexts, err := s.loadVMs(ctx, runID)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(contexts))
	for id := range contexts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]ContextRun, 0, len(ids))
	for _, id := range ids {
		out = append(out, contexts[id])
	}
	return out, nil
}

func (s *Store) Resources(ctx context.Context, runID int64, kind string) ([]ResourceObservation, error) {
	data, err := s.loadResources(ctx, runID)
	if err != nil {
		return nil, err
	}
	values := data.ByKind[strings.ToLower(strings.TrimSpace(kind))]
	out := make([]ResourceObservation, 0, len(values))
	for _, value := range values {
		out = append(out, value.observation)
	}
	return out, nil
}

func (s *Store) loadSnapshots(ctx context.Context, vmID int64) ([]vsphere.VMSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT moref,numeric_id,parent_moref,name,description,create_time,power_state,quiesced,current_snapshot FROM snapshot_observations WHERE vm_observation_id=?`, vmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vsphere.VMSnapshot
	for rows.Next() {
		var n int32
		var moref, parent, name, desc, power string
		var ct int64
		var q, cur int
		if err := rows.Scan(&moref, &n, &parent, &name, &desc, &ct, &power, &q, &cur); err != nil {
			return nil, err
		}
		out = append(out, vsphere.VMSnapshot{ID: moref, NumericID: n, ParentID: parent, Name: name, Description: desc, CreateTime: time.UnixMilli(ct).UTC(), PowerState: power, Quiesced: q != 0, Current: cur != 0})
	}
	return out, rows.Err()
}
