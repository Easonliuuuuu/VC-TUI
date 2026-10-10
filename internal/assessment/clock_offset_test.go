package assessment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

// The offset is stored per context of a run and read back on the run. A
// context that did not measure one has none, which is not an offset of zero.
func TestRunCarriesTheClockOffsetItsContextsMeasured(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	run, err := s.StartRunWithMetadata(ctx, "test", []*config.Context{
		{Name: "behind", Endpoint: "https://behind.example"},
		{Name: "exact", Endpoint: "https://exact.example"},
		{Name: "silent", Endpoint: "https://silent.example"},
	}, when, RunMetadata{InventorySchemaVersion: CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	behind, exact := -779*time.Millisecond-300*time.Microsecond, time.Duration(0)
	for _, result := range []ContextResult{
		{Name: "behind", Status: "success", ClockOffset: &behind},
		{Name: "exact", Status: "success", ClockOffset: &exact},
		{Name: "silent", Status: "success"},
	} {
		if err := s.SaveContext(ctx, run.ID, result, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if offset, ok := got.ClockOffset("behind"); !ok || offset != -779*time.Millisecond {
		t.Fatalf("behind offset = %v, %v; want -779ms to the millisecond", offset, ok)
	}
	if offset, ok := got.ClockOffset("exact"); !ok || offset != 0 {
		t.Fatalf("a measured zero must be kept as measured: %v, %v", offset, ok)
	}
	if _, ok := got.ClockOffset("silent"); ok {
		t.Fatalf("a context that measured nothing has an offset: %+v", got.ClockOffsetsMS)
	}
	runs, err := s.Runs(ctx)
	if err != nil || len(runs) != 1 || len(runs[0].ClockOffsetsMS) != 2 {
		t.Fatalf("Runs must carry the same offsets: %+v err=%v", runs, err)
	}
}

// A database written by the previous build (schema 7, no clock_offset_ms
// column) must open, gain the column, and report its old runs as unmeasured.
func TestOpenMigratesSchemaSevenWithoutInventingClockOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	run, err := s.StartRunWithMetadata(ctx, "test", []*config.Context{{Name: "prod", Endpoint: "https://vc.example"}}, when, RunMetadata{InventorySchemaVersion: CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContext(ctx, run.ID, ContextResult{Name: "prod", Status: "success"}, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`ALTER TABLE context_runs DROP COLUMN clock_offset_ms`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 7`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a schema 7 database: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("user_version=%d err=%v, want %d", version, err, currentSchemaVersion)
	}
	got, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ClockOffsetsMS) != 0 {
		t.Fatalf("an old run has clock offsets: %+v", got.ClockOffsetsMS)
	}
}
