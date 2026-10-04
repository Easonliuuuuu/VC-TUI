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

func newSnapshotAgeTestHistoryDB(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	started := time.Now().UTC().Truncate(time.Second)
	run, err := store.StartRun(context.Background(), "test", []*config.Context{{Name: "prod", Endpoint: "https://prod.example"}}, started)
	if err != nil {
		t.Fatal(err)
	}
	finished := started.Add(2 * time.Minute)
	vm := vsphere.VM{
		Location:  vsphere.Location{Context: "prod", Datacenter: "dc1", Path: "/dc1/vm/vm-snapshot"},
		ID:        "vm-1",
		Name:      "vm-snapshot",
		Snapshots: []vsphere.VMSnapshot{{ID: "snap-1", Name: "snap-1", CreateTime: finished.Add(-90 * 24 * time.Hour)}},
	}
	collections := make([]assessment.CollectionResult, 0, 8)
	for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
		collection := assessment.CollectionResult{Kind: kind, Status: "empty"}
		if kind == "vm" {
			collection.Status = "success"
			collection.ItemCount = 1
		} else if kind == "snapshot" {
			collection.Status = "success"
			collection.ItemCount = 1
		}
		collections = append(collections, collection)
	}
	if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{
		Name: "prod", VCenterID: "vc-1", Status: "success",
		VMs:         []assessment.Observation{{VCenterID: "vc-1", Context: "prod", VM: vm}},
		Collections: collections,
	}, finished); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(context.Background(), run.ID, finished); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestAssessmentSnapshotAgeUsesCompactAgeAndKeepsJSON(t *testing.T) {
	dbPath := newSnapshotAgeTestHistoryDB(t)
	stdout, _, err := runAssessment(t, dbPath, "snapshots")
	if err != nil {
		t.Fatalf("assessment snapshots: %v", err)
	}
	if !strings.Contains(stdout, "12w") || strings.Contains(stdout, "7776000") {
		t.Fatalf("snapshot age should be compactly rendered as 12w, got:\n%s", stdout)
	}

	stdout, _, err = runAssessment(t, dbPath, "snapshots", "-o", "json")
	if err != nil {
		t.Fatalf("assessment snapshots JSON: %v", err)
	}
	var ages []struct {
		Age int64 `json:"age"`
	}
	if err := json.Unmarshal([]byte(stdout), &ages); err != nil {
		t.Fatalf("decode snapshots JSON: %v\n%s", err, stdout)
	}
	want := int64(90 * 24 * time.Hour)
	if len(ages) != 1 || ages[0].Age != want {
		t.Fatalf("snapshot JSON age = %+v, want duration nanoseconds %d", ages, want)
	}
}

func TestAssessmentSnapshotTrendOldestUsesCompactAge(t *testing.T) {
	dbPath := newSnapshotAgeTestHistoryDB(t)
	stdout, _, err := runAssessment(t, dbPath, "trends", "snapshots")
	if err != nil {
		t.Fatalf("assessment snapshot trend: %v", err)
	}
	if !strings.Contains(stdout, "12w") || strings.Contains(stdout, "7776000") {
		t.Fatalf("snapshot trend oldest age should be compactly rendered as 12w, got:\n%s", stdout)
	}
}
