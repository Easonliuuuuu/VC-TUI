package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newDecommissionTestHistoryDB(t *testing.T, vm vsphere.VM) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	contexts := []*config.Context{{Name: "prod", Endpoint: "https://prod.example", Datacenter: "dc"}}
	run, err := store.StartRunWithMetadata(context.Background(), "test", contexts, when, assessment.RunMetadata{InventorySchemaVersion: assessment.CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	collections := make([]assessment.CollectionResult, 0, 8)
	for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
		status := "empty"
		itemCount := 0
		if kind == "vm" {
			status = "success"
			itemCount = 1
		}
		collections = append(collections, assessment.CollectionResult{Kind: kind, Status: status, ItemCount: itemCount})
	}
	if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-prod", Status: "success", VMs: []assessment.Observation{{Context: "prod", VCenterID: "vc-prod", VM: vm}}, Collections: collections}, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(context.Background(), run.ID, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestVMDecommissionCheckUsesStoredEvidence(t *testing.T) {
	dbPath := newDecommissionTestHistoryDB(t, vsphere.VM{ID: "vm-1", Name: "app", PowerState: "poweredOn", ConnectionState: "connected", ConfigurationAvailable: true})
	stdout, _, err := runTopology(t, dbPath, "vm", "decommission-check", "app", "--fail-on-blockers", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "decommission check blocked") {
		t.Fatalf("err = %v, output = %s", err, stdout)
	}
	var result struct {
		Verdict string `json:"verdict"`
		RunID   int64  `json:"run_id"`
	}
	if decodeErr := json.Unmarshal([]byte(stdout), &result); decodeErr != nil {
		t.Fatalf("decode output: %v (%s)", decodeErr, stdout)
	}
	if result.Verdict != "blocked" || result.RunID == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestVMDecommissionCheckNoMatchIsUnknown(t *testing.T) {
	dbPath := newDecommissionTestHistoryDB(t, vsphere.VM{ID: "vm-1", Name: "app", PowerState: "poweredOff", ConnectionState: "connected", ConfigurationAvailable: true})
	stdout, _, err := runTopology(t, dbPath, "vm", "decommission-check", "missing", "-o", "json")
	if err != nil {
		t.Fatalf("err = %v, output = %s", err, stdout)
	}
	if !strings.Contains(stdout, `"verdict": "unknown"`) || !strings.Contains(stdout, `"id": "identity"`) {
		t.Fatalf("output = %s", stdout)
	}
}
