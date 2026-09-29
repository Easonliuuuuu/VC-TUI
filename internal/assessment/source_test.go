package assessment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestLoadExportDataAttributesSourceToEachContext(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	run, err := s.StartRunWithMetadata(ctx, "test", []*config.Context{
		{Name: "alpha", Endpoint: "https://alpha.example"},
		{Name: "beta", Endpoint: "https://beta.example"},
		{Name: "down", Endpoint: "https://down.example"},
	}, when, RunMetadata{InventorySchemaVersion: CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	alpha := &SourceInfo{Name: "VMware vCenter Server", Version: "8.0.3", Build: "24022515", APIType: "VirtualCenter", ProductLineID: "vpx", InstanceUUID: "uuid-alpha"}
	beta := &SourceInfo{Name: "VMware ESXi", Version: "7.0.3", PatchLevel: "ESXi70U3", APIType: "HostAgent", ProductLineID: "embeddedEsx", InstanceUUID: "uuid-beta"}
	for _, result := range []ContextResult{
		{Name: "alpha", VCenterID: "uuid-alpha", Status: "success", Source: alpha},
		{Name: "beta", VCenterID: "uuid-beta", Status: "success", Source: beta},
		{Name: "down", Status: "failed", Error: "connection refused"},
	} {
		if err := s.SaveContext(ctx, run.ID, result, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.FinishRun(ctx, run.ID, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	data, err := s.LoadExportData(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*SourceInfo{}
	for _, c := range data.Contexts {
		got[c.Name] = c.Source
	}
	if got["alpha"] == nil || *got["alpha"] != *alpha || got["beta"] == nil || *got["beta"] != *beta {
		t.Fatalf("sources were not attributed to their contexts: %+v", got)
	}
	if got["down"] != nil {
		t.Fatalf("a context that never connected has a source: %+v", got["down"])
	}
	scoped, err := s.LoadExportDataForContexts(ctx, run.ID, []string{"beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped.Contexts) != 1 || scoped.Contexts[0].Source == nil || scoped.Contexts[0].Source.Version != "7.0.3" {
		t.Fatalf("scoped export lost or mixed the source: %+v", scoped.Contexts)
	}
	if err := s.DeleteRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM context_sources`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("source rows survived run deletion: n=%d err=%v", n, err)
	}
}

// A database written by the previous build (schema 6, no context_sources
// table) must open, gain the table, and export its old runs with no invented
// source.
func TestOpenMigratesSchemaSixWithoutInventingSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	run, err := s.StartRunWithMetadata(ctx, "test", []*config.Context{{Name: "prod", Endpoint: "https://vc.example"}}, when, RunMetadata{InventorySchemaVersion: "16"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveContext(ctx, run.ID, ContextResult{Name: "prod", VCenterID: "vc-uuid", Status: "success", VMs: []Observation{{Context: "prod", VM: vsphere.VM{ID: "vm-1", Name: "app"}}}}, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(ctx, run.ID, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP TABLE context_sources`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 6`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopening a schema 6 database: %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("user_version=%d err=%v, want %d", version, err, currentSchemaVersion)
	}
	data, err := s.LoadExportData(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(data.Contexts) != 1 || data.Contexts[0].Source != nil || len(data.VMs) != 1 {
		t.Fatalf("old run export=%+v", data)
	}
}
