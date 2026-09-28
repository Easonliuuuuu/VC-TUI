package assessment

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PruneCandidate struct {
	Run   Run   `json:"run"`
	Bytes int64 `json:"bytes,omitempty"`
}

type PruneResult struct {
	Cutoff     time.Time        `json:"cutoff"`
	KeepLast   int              `json:"keep_last"`
	Execute    bool             `json:"execute"`
	Candidates []PruneCandidate `json:"candidates"`
	Deleted    int              `json:"deleted"`
	// PerfWindows counts performance collections older than the cutoff.
	// The newest collection of each context is never counted.
	PerfWindows int `json:"perf_windows"`
	PerfDeleted int `json:"perf_deleted"`
}

func (s *Store) Prune(ctx context.Context, olderThan time.Duration, keepLast int, execute bool) (PruneResult, error) {
	if olderThan <= 0 {
		return PruneResult{}, fmt.Errorf("prune duration must be greater than zero")
	}
	if keepLast < 0 {
		return PruneResult{}, fmt.Errorf("keep-last cannot be negative")
	}
	runs, err := s.Runs(ctx)
	if err != nil {
		return PruneResult{}, err
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	// Keep the newest completed runs regardless of age. Runs are returned
	// newest first, making this stable even when timestamps collide.
	kept := make(map[int64]bool)
	count := 0
	for _, run := range runs {
		if run.Status == RunComplete && count < keepLast {
			kept[run.ID] = true
			count++
		}
	}
	result := PruneResult{Cutoff: cutoff, KeepLast: keepLast, Execute: execute}
	for _, run := range runs {
		if run.Status == RunRunning || run.Pinned || run.FinishedAt.IsZero() || !run.FinishedAt.Before(cutoff) || kept[run.ID] {
			continue
		}
		result.Candidates = append(result.Candidates, PruneCandidate{Run: run, Bytes: s.runBytes(ctx, run.ID)})
	}
	perfIDs, err := s.prunablePerfWindows(ctx, cutoff)
	if err != nil {
		return result, err
	}
	result.PerfWindows = len(perfIDs)
	if !execute || (len(result.Candidates) == 0 && len(perfIDs) == 0) {
		return result, nil
	}
	lease, err := s.AcquireOperationLease(ctx, time.Now().UTC(), "prune")
	if err != nil {
		return result, err
	}
	defer s.ReleaseCaptureLease(context.Background(), lease)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	for _, candidate := range result.Candidates {
		res, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE id=? AND pinned=0 AND status<>'running'`, candidate.Run.ID)
		if err != nil {
			_ = tx.Rollback()
			return result, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.Deleted++
		}
	}
	for _, id := range perfIDs {
		res, err := tx.ExecContext(ctx, `DELETE FROM perf_windows WHERE id=?`, id)
		if err != nil {
			_ = tx.Rollback()
			return result, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result.PerfDeleted++
		}
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) runBytes(ctx context.Context, runID int64) int64 {
	var bytes sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT coalesce((SELECT sum(length(payload)) FROM vm_observations v JOIN context_runs c ON c.id=v.context_run_id WHERE c.run_id=?),0) + coalesce((SELECT sum(length(payload)) FROM resource_observations ro JOIN context_collections cc ON cc.id=ro.collection_id JOIN context_runs c ON c.id=cc.context_run_id WHERE c.run_id=?),0)`, runID, runID).Scan(&bytes)
	if bytes.Valid {
		return bytes.Int64
	}
	return 0
}

func (s *Store) Backup(ctx context.Context, destination string, force bool) error {
	if strings.TrimSpace(destination) == "" {
		return fmt.Errorf("backup destination is required")
	}
	if !force {
		if _, err := os.Stat(destination); err == nil {
			return fmt.Errorf("backup destination already exists; use --force")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	lease, err := s.AcquireOperationLease(ctx, time.Now().UTC(), "backup")
	if err != nil {
		return err
	}
	defer s.ReleaseCaptureLease(context.Background(), lease)
	return s.backupFile(ctx, destination, force)
}

func (s *Store) backupFile(ctx context.Context, destination string, force bool) error {
	if s.path == "" {
		return fmt.Errorf("history database path is unavailable")
	}
	if filepath.Clean(destination) == filepath.Clean(s.path) {
		return fmt.Errorf("backup destination must differ from the history database")
	}
	if !force {
		if _, err := os.Stat(destination); err == nil {
			return fmt.Errorf("backup destination already exists; use --force")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	tmp := destination + ".tmp-" + fmt.Sprintf("%d", time.Now().UnixNano())
	_ = os.Remove(tmp)
	defer os.Remove(tmp)
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("create sqlite backup: %w", err)
	}
	// The writer lease is process-local coordination state, not assessment
	// evidence. Do not make a backup appear busy to the process that restores
	// or inspects it later.
	if db, err := openDB(tmp); err == nil {
		_, _ = db.Exec(`DELETE FROM capture_lease`)
		_ = db.Close()
	}
	defer os.Remove(tmp + "-wal")
	defer os.Remove(tmp + "-shm")
	if err := verifySQLiteFile(tmp); err != nil {
		return fmt.Errorf("verify sqlite backup: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if force {
		if err := os.Remove(destination); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(tmp, destination); err != nil {
		return fmt.Errorf("publish sqlite backup: %w", err)
	}
	return nil
}

func verifySQLiteFile(path string) error {
	db, err := openDB(path)
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if !strings.EqualFold(integrity, "ok") {
		return fmt.Errorf("integrity check: %s", integrity)
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version < 1 || version > currentSchemaVersion {
		return fmt.Errorf("unsupported history schema version %d", version)
	}
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='runs'`).Scan(&tables); err != nil {
		return err
	}
	if tables != 1 {
		return fmt.Errorf("history runs table is missing")
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("foreign key violations present")
	}
	return rows.Err()
}

func (s *Store) Restore(ctx context.Context, source string, force bool) (string, error) {
	if !force {
		return "", fmt.Errorf("restoring assessment history requires --force")
	}
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("restore source is required")
	}
	if err := verifySQLiteFile(source); err != nil {
		return "", fmt.Errorf("validate restore source: %w", err)
	}
	lease, err := s.AcquireOperationLease(ctx, time.Now().UTC(), "restore")
	if err != nil {
		return "", err
	}
	defer s.ReleaseCaptureLease(context.Background(), lease)
	if s.path == "" {
		return "", fmt.Errorf("history database path is unavailable")
	}
	safety := fmt.Sprintf("%s.pre-restore-%s.db", s.path, time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := s.backupFile(ctx, safety, false); err != nil {
		return "", fmt.Errorf("create pre-restore safety copy: %w", err)
	}
	tmp := s.path + ".restore-" + lease.Token
	_ = os.Remove(tmp)
	if err := copyFile(source, tmp); err != nil {
		return safety, err
	}
	if err := verifySQLiteFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return safety, err
	}
	if err := s.db.Close(); err != nil {
		_ = os.Remove(tmp)
		return safety, err
	}
	reopenSafety := func() {
		_ = os.Remove(s.path)
		_ = copyFile(safety, s.path)
		if reopened, openErr := Open(s.path); openErr == nil {
			s.db = reopened.db
		}
	}
	for _, sidecar := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			reopenSafety()
			return safety, fmt.Errorf("remove current history database: %w", err)
		}
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		reopenSafety()
		return safety, fmt.Errorf("replace history database: %w", err)
	}
	reopened, err := Open(s.path)
	if err != nil {
		// Restore the safety copy so a failed migration never leaves the store
		// unusable. The caller can still inspect the returned safety path.
		_ = os.Remove(s.path)
		_ = copyFile(safety, s.path)
		reopened, _ = Open(s.path)
		if reopened != nil {
			s.db = reopened.db
		}
		return safety, fmt.Errorf("reopen restored history database: %w", err)
	}
	s.db = reopened.db
	s.path = reopened.path
	return safety, nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

type DoctorReport struct {
	SchemaVersion        int       `json:"schema_version"`
	Integrity            string    `json:"integrity"`
	ForeignKeys          string    `json:"foreign_keys"`
	Runs                 int       `json:"runs"`
	VMObservations       int       `json:"vm_observations"`
	ResourceObservations int       `json:"resource_observations"`
	OrphanedRuns         int       `json:"orphaned_runs"`
	LeaseOperation       string    `json:"lease_operation,omitempty"`
	LeaseExpiresAt       time.Time `json:"lease_expires_at,omitempty"`
	DatabaseBytes        int64     `json:"database_bytes"`
	WALBytes             int64     `json:"wal_bytes"`
	Warnings             []string  `json:"warnings,omitempty"`
}

func (s *Store) Doctor(ctx context.Context) (DoctorReport, error) {
	report := DoctorReport{}
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&report.SchemaVersion); err != nil {
		return report, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&report.Integrity); err != nil {
		return report, err
	}
	rows, err := s.db.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return report, err
	}
	if rows.Next() {
		report.ForeignKeys = "violations"
	} else {
		report.ForeignKeys = "ok"
	}
	rows.Close()
	for query, dest := range map[string]*int{"SELECT count(*) FROM runs": &report.Runs, "SELECT count(*) FROM vm_observations": &report.VMObservations, "SELECT count(*) FROM resource_observations": &report.ResourceObservations} {
		if err := s.db.QueryRowContext(ctx, query).Scan(dest); err != nil {
			return report, err
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM runs WHERE status='running' AND finished_at IS NULL AND NOT EXISTS (SELECT 1 FROM capture_lease WHERE capture_lease.run_id=runs.id AND capture_lease.expires_at>?)`, time.Now().UTC().UnixMilli()).Scan(&report.OrphanedRuns); err != nil {
		return report, err
	}
	var op string
	var expiry sql.NullInt64
	if err := s.db.QueryRowContext(ctx, `SELECT operation,expires_at FROM capture_lease WHERE id=1`).Scan(&op, &expiry); err == nil {
		report.LeaseOperation, report.LeaseExpiresAt = op, fromMillis(expiry)
	} else if err != sql.ErrNoRows {
		return report, err
	}
	if report.Integrity != "ok" {
		report.Warnings = append(report.Warnings, "sqlite integrity check failed")
	}
	if report.ForeignKeys != "ok" {
		report.Warnings = append(report.Warnings, "foreign key violations detected")
	}
	if report.OrphanedRuns > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d running run(s) have not finished", report.OrphanedRuns))
	}
	if s.path != "" {
		if info, err := os.Stat(s.path); err == nil {
			report.DatabaseBytes = info.Size()
		}
		if info, err := os.Stat(s.path + "-wal"); err == nil {
			report.WALBytes = info.Size()
		}
	}
	return report, nil
}
