package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func runCapacity(t *testing.T, dbPath string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"--history-db", dbPath, "assessment", "capacity"}, args...))
	defer func() { _ = a.Close(context.Background()) }()
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestCapacityCommandJSONAndFilters(t *testing.T) {
	dbPath := newTopologyTestHistoryDB(t)
	stdout, _, err := runCapacity(t, dbPath, "latest", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var report assessment.CapacityReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("JSON=%s err=%v", stdout, err)
	}
	if len(report.Datastores) != 2 || report.SchemaVersion != 1 {
		t.Fatalf("report=%+v", report)
	}
	stdout, _, err = runCapacity(t, dbPath, "latest", "--datastore", "missing", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(stdout), []byte(`"datastores": []`)) {
		t.Fatalf("filtered report=%s", stdout)
	}
	if _, _, err := runCapacity(t, dbPath, "latest", "--min-free-bytes", "not-a-size"); err == nil {
		t.Fatal("invalid --min-free-bytes unexpectedly succeeded")
	}
	if _, _, err := runCapacity(t, dbPath, "latest", "--to", "latest"); err == nil {
		t.Fatal("positional RUN and --to unexpectedly succeeded")
	}
}

func newProjectionCapacityHistoryDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	contexts := []*config.Context{{Name: "prod", Endpoint: "https://prod.example"}}
	for i := 0; i < 8; i++ {
		when := time.Date(2026, 6, 1+i, 0, 0, 0, 0, time.UTC)
		run, err := store.StartRunWithMetadata(context.Background(), "test", contexts, when, assessment.RunMetadata{InventorySchemaVersion: assessment.CurrentInventorySchemaVersion})
		if err != nil {
			t.Fatal(err)
		}
		ds := vsphere.Datastore{Location: vsphere.Location{Context: "prod", Datacenter: "dc-a"}, ID: "ds-1", Name: "prod", CapacityBytes: 2000, FreeBytes: int64(1000 - i*100)}
		payload, _ := json.Marshal(ds)
		collections := make([]assessment.CollectionResult, 0, 8)
		for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
			collection := assessment.CollectionResult{Kind: kind, Status: "empty"}
			if kind == "vm" || kind == "datastore" {
				collection.Status = "success"
			}
			if kind == "datastore" {
				collection.ItemCount = 1
				collection.Resources = []assessment.ResourceObservation{{Context: "prod", VCenterID: "vc-1", Kind: "datastore", ID: "ds-1", Name: "prod", Payload: payload}}
			}
			collections = append(collections, collection)
		}
		if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success", Collections: collections}, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := store.FinishRun(context.Background(), run.ID, when.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCapacityCommandFailOnProjection(t *testing.T) {
	dbPath := newProjectionCapacityHistoryDB(t)
	_, _, err := runCapacity(t, dbPath, "latest", "--since", "10d", "--fail-on-projection", "30d")
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 2 {
		t.Fatalf("err=%v, want exit code 2", err)
	}
}
