package cli

import (
	"bytes"
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

func newTopologyTestHistoryDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	contexts := []*config.Context{{Name: "prod", Endpoint: "https://prod.example"}, {Name: "edge", Endpoint: "https://edge.example"}}
	run, err := store.StartRunWithMetadata(context.Background(), "test", contexts, when, assessment.RunMetadata{InventorySchemaVersion: "12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, vcenter, id, extent string
	}{
		{name: "prod", vcenter: "vc-prod", id: "ds-prod", extent: "naa.prod"},
		{name: "edge", vcenter: "vc-edge", id: "ds-edge", extent: "naa.edge"},
	} {
		ds := vsphere.Datastore{Location: vsphere.Location{Context: item.name, Datacenter: "dc"}, ID: item.id, Name: "datastore1", Backing: vsphere.DatastoreBacking{Extents: []string{item.extent}}}
		payload, _ := json.Marshal(ds)
		collections := make([]assessment.CollectionResult, 0, 8)
		for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
			collection := assessment.CollectionResult{Kind: kind, Status: "empty"}
			if kind == "datastore" {
				collection.Status = "success"
				collection.ItemCount = 1
				collection.Resources = []assessment.ResourceObservation{{Context: item.name, VCenterID: item.vcenter, Kind: "datastore", ID: item.id, Name: "datastore1", Payload: payload}}
			}
			collections = append(collections, collection)
		}
		if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: item.name, VCenterID: item.vcenter, Status: "success", Collections: collections}, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinishRun(context.Background(), run.ID, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func runTopology(t *testing.T, dbPath string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"--history-db", dbPath}, args...))
	defer func() { _ = a.Close(context.Background()) }()
	err := root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestTopologyCommandAmbiguityAndContextNarrowing(t *testing.T) {
	dbPath := newTopologyTestHistoryDB(t)
	stdout, _, err := runTopology(t, dbPath, "topology", "datastore", "datastore1")
	if err != nil || !strings.Contains(stdout, "[AMBIGUOUS] 2 distinct datastores") || !strings.Contains(stdout, "prod") || !strings.Contains(stdout, "edge") {
		t.Fatalf("ambiguity err=%v output=%s", err, stdout)
	}
	stdout, _, err = runTopology(t, dbPath, "--context", "prod", "topology", "datastore", "datastore1", "-o", "json")
	if err != nil || strings.Contains(stdout, `"ambiguous": true`) {
		t.Fatalf("narrowed err=%v output=%s", err, stdout)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil || len(result["subjects"].([]any)) != 1 {
		t.Fatalf("narrowed JSON err=%v output=%s", err, stdout)
	}
}

func TestTopologyCommandRejectsBadKind(t *testing.T) {
	dbPath := newTopologyTestHistoryDB(t)
	_, _, err := runTopology(t, dbPath, "topology", "widget", "anything")
	if err == nil || !strings.Contains(err.Error(), `unknown kind "widget"`) {
		t.Fatalf("bad kind error=%v", err)
	}
}

func TestTopologyCommandsRejectUnknownScope(t *testing.T) {
	dbPath := newTopologyTestHistoryDB(t)
	for _, direction := range []string{"topology", "dependencies", "blast-radius"} {
		_, _, err := runTopology(t, dbPath, "--context", "does-not-exist", direction, "datastore", "datastore1", "-o", "json")
		if err == nil || !strings.Contains(err.Error(), "unknown assessment context") {
			t.Fatalf("%s: err=%v, want unknown-context failure", direction, err)
		}
	}
}
