package assessment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

// Performance windows are stored apart from runs and inventory observations.
// A window is one bounded collection of summaries with its provenance; it
// never rewrites or references an inventory run, so the immutable inventory
// ledger is unaffected by collecting, pruning or discarding performance data.

// migrateV6 adds the performance tables. It is additive and idempotent.
func (s *Store) migrateV6(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin history v6 migration: %w", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS perf_windows (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			context TEXT NOT NULL,
			vcenter_id TEXT NOT NULL DEFAULT '',
			endpoint TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT '',
			started_at INTEGER NOT NULL,
			finished_at INTEGER,
			window_start INTEGER NOT NULL,
			window_end INTEGER NOT NULL,
			interval_seconds INTEGER NOT NULL,
			expected_samples INTEGER NOT NULL,
			requests_used INTEGER NOT NULL,
			vms_requested INTEGER NOT NULL,
			vms_sampled INTEGER NOT NULL,
			status TEXT NOT NULL,
			error TEXT NOT NULL DEFAULT '',
			budget TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS perf_windows_context ON perf_windows(context, id)`,
		`CREATE TABLE IF NOT EXISTS perf_vms (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			window_id INTEGER NOT NULL REFERENCES perf_windows(id) ON DELETE CASCADE,
			moref TEXT NOT NULL,
			instance_uuid TEXT NOT NULL DEFAULT '',
			name TEXT NOT NULL,
			power_state TEXT NOT NULL DEFAULT '',
			vcpu INTEGER NOT NULL,
			memory_mb INTEGER NOT NULL,
			sampled INTEGER NOT NULL,
			signal TEXT NOT NULL,
			signal_reason TEXT NOT NULL DEFAULT '',
			UNIQUE(window_id, moref)
		)`,
		`CREATE TABLE IF NOT EXISTS perf_summaries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			perf_vm_id INTEGER NOT NULL REFERENCES perf_vms(id) ON DELETE CASCADE,
			metric TEXT NOT NULL,
			unit TEXT NOT NULL,
			aggregation TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL,
			expected_samples INTEGER NOT NULL,
			successful_samples INTEGER NOT NULL,
			missing_samples INTEGER NOT NULL,
			average REAL,
			peak REAL,
			p95 REAL,
			status TEXT NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			UNIQUE(perf_vm_id, metric)
		)`,
		`PRAGMA user_version = 6`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migrate history database to v6: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit history v6 migration: %w", err)
	}
	return nil
}

func nullFloat(v *float64) sql.NullFloat64 {
	if v == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *v, Valid: true}
}

// SavePerfWindow stores one collection, all-or-nothing, and returns it with
// its assigned ID. Unknown statistics are stored as NULL, never as zero.
func (s *Store) SavePerfWindow(ctx context.Context, w perf.Window) (perf.Window, error) {
	budget, err := json.Marshal(w.Budget)
	if err != nil {
		return w, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return w, fmt.Errorf("begin performance save: %w", err)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO perf_windows(context,vcenter_id,endpoint,source,started_at,finished_at,window_start,window_end,interval_seconds,expected_samples,requests_used,vms_requested,vms_sampled,status,error,budget) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		w.Context, w.VCenterID, w.Endpoint, w.Source, w.StartedAt.UnixMilli(), w.FinishedAt.UnixMilli(), w.WindowStart.UnixMilli(), w.WindowEnd.UnixMilli(),
		w.IntervalSeconds, w.ExpectedSamples, w.RequestsUsed, w.VMsRequested, w.VMsSampled, w.Status, w.Error, string(budget))
	if err != nil {
		_ = tx.Rollback()
		return w, fmt.Errorf("save performance window: %w", err)
	}
	w.ID, _ = res.LastInsertId()
	for _, vm := range w.VMs {
		vres, err := tx.ExecContext(ctx, `INSERT INTO perf_vms(window_id,moref,instance_uuid,name,power_state,vcpu,memory_mb,sampled,signal,signal_reason) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			w.ID, vm.MoRef, vm.InstanceUUID, vm.Name, vm.PowerState, vm.VCPU, vm.MemoryMB, boolInt(vm.Sampled), string(vm.Signal), vm.SignalReason)
		if err != nil {
			_ = tx.Rollback()
			return w, fmt.Errorf("save performance VM %s: %w", vm.MoRef, err)
		}
		vmID, _ := vres.LastInsertId()
		for _, sum := range vm.Summaries {
			if _, err := tx.ExecContext(ctx, `INSERT INTO perf_summaries(perf_vm_id,metric,unit,aggregation,interval_seconds,expected_samples,successful_samples,missing_samples,average,peak,p95,status,reason) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				vmID, string(sum.Metric), string(sum.Unit), sum.Aggregation, sum.Interval, sum.Expected, sum.Successful, sum.Missing,
				nullFloat(sum.Average), nullFloat(sum.Peak), nullFloat(sum.P95), string(sum.Status), sum.Reason); err != nil {
				_ = tx.Rollback()
				return w, fmt.Errorf("save performance summary %s/%s: %w", vm.MoRef, sum.Metric, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return w, fmt.Errorf("commit performance window: %w", err)
	}
	return w, nil
}

const perfWindowColumns = `id,context,vcenter_id,endpoint,source,started_at,finished_at,window_start,window_end,interval_seconds,expected_samples,requests_used,vms_requested,vms_sampled,status,error,budget`

type rowScanner interface{ Scan(dest ...any) error }

func scanPerfWindow(row rowScanner) (perf.Window, error) {
	var (
		w                   perf.Window
		started, start, end int64
		finished            sql.NullInt64
		budget              string
	)
	if err := row.Scan(&w.ID, &w.Context, &w.VCenterID, &w.Endpoint, &w.Source, &started, &finished, &start, &end,
		&w.IntervalSeconds, &w.ExpectedSamples, &w.RequestsUsed, &w.VMsRequested, &w.VMsSampled, &w.Status, &w.Error, &budget); err != nil {
		return w, err
	}
	w.StartedAt = time.UnixMilli(started).UTC()
	w.FinishedAt = fromMillis(finished)
	w.WindowStart = time.UnixMilli(start).UTC()
	w.WindowEnd = time.UnixMilli(end).UTC()
	_ = json.Unmarshal([]byte(budget), &w.Budget)
	return w, nil
}

// PerfWindows lists collections newest first, without their per-VM rows. A
// non-empty contextName restricts the list to that context.
func (s *Store) PerfWindows(ctx context.Context, contextName string, limit int) ([]perf.Window, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT ` + perfWindowColumns + ` FROM perf_windows`
	args := []any{}
	if contextName != "" {
		query += ` WHERE context=?`
		args = append(args, contextName)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []perf.Window
	for rows.Next() {
		w, err := scanPerfWindow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// LoadPerfWindow returns one collection with every VM and summary.
func (s *Store) LoadPerfWindow(ctx context.Context, id int64) (perf.Window, error) {
	w, err := scanPerfWindow(s.db.QueryRowContext(ctx, `SELECT `+perfWindowColumns+` FROM perf_windows WHERE id=?`, id))
	if err == sql.ErrNoRows {
		return w, fmt.Errorf("no performance window %d", id)
	}
	if err != nil {
		return w, err
	}
	return w, s.loadPerfVMs(ctx, &w)
}

func (s *Store) loadPerfVMs(ctx context.Context, w *perf.Window) error {
	rows, err := s.db.QueryContext(ctx, `SELECT v.moref,v.instance_uuid,v.name,v.power_state,v.vcpu,v.memory_mb,v.sampled,v.signal,v.signal_reason,
		m.metric,m.unit,m.aggregation,m.interval_seconds,m.expected_samples,m.successful_samples,m.missing_samples,m.average,m.peak,m.p95,m.status,m.reason
		FROM perf_vms v LEFT JOIN perf_summaries m ON m.perf_vm_id=v.id WHERE v.window_id=? ORDER BY v.moref, m.id`, w.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	index := map[string]int{}
	for rows.Next() {
		var (
			vm                      perf.VMResult
			sampled                 int
			signal                  string
			metric, unit, agg       sql.NullString
			status, reason          sql.NullString
			interval, exp, ok, miss sql.NullInt64
			avg, peak, p95          sql.NullFloat64
		)
		if err := rows.Scan(&vm.MoRef, &vm.InstanceUUID, &vm.Name, &vm.PowerState, &vm.VCPU, &vm.MemoryMB, &sampled, &signal, &vm.SignalReason,
			&metric, &unit, &agg, &interval, &exp, &ok, &miss, &avg, &peak, &p95, &status, &reason); err != nil {
			return err
		}
		i, seen := index[vm.MoRef]
		if !seen {
			vm.Sampled, vm.Signal = sampled != 0, perf.Signal(signal)
			w.VMs = append(w.VMs, vm)
			i = len(w.VMs) - 1
			index[vm.MoRef] = i
		}
		if metric.Valid {
			w.VMs[i].Summaries = append(w.VMs[i].Summaries, perf.Summary{
				Metric: perf.Metric(metric.String), Unit: perf.Unit(unit.String), Aggregation: agg.String,
				Interval: int(interval.Int64), Expected: int(exp.Int64), Successful: int(ok.Int64), Missing: int(miss.Int64),
				Average: nullFloatPtr(avg), Peak: nullFloatPtr(peak), P95: nullFloatPtr(p95), Status: perf.Status(status.String), Reason: reason.String,
			})
		}
	}
	return rows.Err()
}

// LatestPerfWindow returns the newest usable collection for a context, by
// vCenter identity when both sides know it and by context name otherwise.
// Failed collections carry no data and are skipped. found is false when none
// exists, which callers must present as "not collected", never as zero.
func (s *Store) LatestPerfWindow(ctx context.Context, contextName, vcenterID string) (w perf.Window, found bool, err error) {
	query := `SELECT ` + perfWindowColumns + ` FROM perf_windows WHERE status IN ('success','partial') AND `
	args := []any{}
	if vcenterID != "" {
		query += `vcenter_id=?`
		args = append(args, vcenterID)
	} else {
		query += `context=? AND vcenter_id=''`
		args = append(args, contextName)
	}
	query += ` ORDER BY window_end DESC, id DESC LIMIT 1`
	w, err = scanPerfWindow(s.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return w, false, nil
	}
	if err != nil {
		return w, false, err
	}
	return w, true, s.loadPerfVMs(ctx, &w)
}

// prunablePerfWindows returns the windows finished before cutoff, always
// sparing the newest usable (success or partial) window of each context, so
// that pruning never leaves a report with no performance evidence at all and
// a later failed attempt cannot cost a context its last good window.
func (s *Store) prunablePerfWindows(ctx context.Context, cutoff time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM perf_windows
		WHERE finished_at IS NOT NULL AND finished_at<?
		AND id NOT IN (SELECT max(id) FROM perf_windows WHERE status IN ('success','partial') GROUP BY context, vcenter_id)
		ORDER BY id`, cutoff.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// PerfSummary is the short per-context performance line shown in an
// assessment report. Status is "not collected" for a context with no usable
// window, so an absent collection is never mistaken for a quiet estate.
type PerfSummary struct {
	Context         string         `json:"context"`
	Status          string         `json:"status"`
	WindowID        int64          `json:"window_id,omitempty"`
	WindowStart     time.Time      `json:"window_start,omitempty"`
	WindowEnd       time.Time      `json:"window_end,omitempty"`
	IntervalSeconds int            `json:"interval_seconds,omitempty"`
	VMsSampled      int            `json:"vms_sampled,omitempty"`
	VMsRequested    int            `json:"vms_requested,omitempty"`
	Signals         map[string]int `json:"signals,omitempty"`
}

// PerformanceSummary reports, for each selected context of a run, the newest
// usable performance window. It reads only stored evidence.
func (s *Store) PerformanceSummary(ctx context.Context, runID int64, selectors []string) ([]PerfSummary, error) {
	contexts, err := s.ContextRuns(ctx, runID)
	if err != nil {
		return nil, err
	}
	selected, err := ValidateStoredContexts(selectors, contexts)
	if err != nil {
		return nil, err
	}
	var out []PerfSummary
	for _, c := range contexts {
		if len(selected) > 0 && !contextSelected(c.Name, selected) {
			continue
		}
		w, found, err := s.LatestPerfWindow(ctx, c.Name, c.VCenterID)
		if err != nil {
			return nil, err
		}
		if !found {
			out = append(out, PerfSummary{Context: c.Name, Status: "not collected"})
			continue
		}
		row := PerfSummary{Context: c.Name, Status: w.Status, WindowID: w.ID, WindowStart: w.WindowStart, WindowEnd: w.WindowEnd,
			IntervalSeconds: w.IntervalSeconds, VMsSampled: w.VMsSampled, VMsRequested: w.VMsRequested, Signals: map[string]int{}}
		for _, vm := range w.VMs {
			row.Signals[string(vm.Signal)]++
		}
		out = append(out, row)
	}
	return out, nil
}
