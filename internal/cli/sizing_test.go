package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/sizing"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newSizingHistoryDB(t *testing.T, datastoreStatus string) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "history.db")
	store, err := assessment.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	run, err := store.StartRunWithMetadata(context.Background(), "test", []*config.Context{{Name: "prod", Endpoint: "https://prod.example"}}, when, assessment.RunMetadata{InventorySchemaVersion: assessment.CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	const gib = int64(1 << 30)
	ds := vsphere.Datastore{Location: vsphere.Location{Context: "prod", Datacenter: "dc"}, ID: "ds-1", Name: "ds1", CapacityBytes: 1000 * gib, FreeBytes: 500 * gib, Accessible: true, Backing: vsphere.DatastoreBacking{VMFSUUID: "u1"}}
	payload, _ := json.Marshal(ds)
	mk := func(name string, cpu int32) assessment.Observation {
		return assessment.Observation{Context: "prod", VCenterID: "vc-1", VM: vsphere.VM{Location: vsphere.Location{Context: "prod"}, ID: name, Name: name, Cluster: "c1", PowerState: "poweredOn", CPU: cpu, MemoryMB: 16 * 1024, ConfigurationAvailable: true,
			Disks: []vsphere.VMDisk{{Key: 1, Label: "d", CapacityBytes: 100 * gib, BackingPath: "[ds1] " + name + "/" + name + ".vmdk"}}}}
	}
	var collections []assessment.CollectionResult
	for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
		c := assessment.CollectionResult{Kind: kind, Status: "empty"}
		switch kind {
		case "vm":
			c.Status, c.ItemCount = "success", 2
		case "datastore":
			c.Status, c.ItemCount = datastoreStatus, 1
			if datastoreStatus == "success" {
				c.Resources = []assessment.ResourceObservation{{Context: "prod", VCenterID: "vc-1", Kind: "datastore", ID: "ds-1", Name: "ds1", Payload: payload}}
			}
		}
		collections = append(collections, c)
	}
	if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success", VMs: []assessment.Observation{mk("a", 8), mk("b", 8)}, Collections: collections}, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun(context.Background(), run.ID, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func runSizing(t *testing.T, dbPath string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"--history-db", dbPath, "assessment", "sizing"}, args...))
	defer func() { _ = a.Close(context.Background()) }()
	err := root.ExecuteContext(context.Background())
	return out.String(), err
}

func TestSizingCommandFitAndInsufficient(t *testing.T) {
	dbPath := newSizingHistoryDB(t, "success")
	base := []string{"latest", "--host-cores", "16", "--host-ram-gib", "64", "--datastore-capacity", "1TiB"}
	stdout, err := runSizing(t, dbPath, append(base, "--hosts", "3", "-o", "json")...)
	if err != nil {
		t.Fatal(err)
	}
	var result sizing.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON=%s err=%v", stdout, err)
	}
	if result.Verdict != sizing.VerdictFit || result.Basis != "allocation-based" || result.RunID == 0 {
		t.Fatalf("result=%+v", result)
	}
	// A single host leaves nothing after the default N-1 allowance.
	stdout, err = runSizing(t, dbPath, append(base, "--hosts", "1", "--fail-unless-fit")...)
	var coded interface{ ExitCode() int }
	if !errors.As(err, &coded) || coded.ExitCode() != 2 {
		t.Fatalf("err=%v, want exit code 2", err)
	}
	if !strings.Contains(stdout, "INSUFFICIENT") || !strings.Contains(stdout, "allocation-based") || !strings.Contains(stdout, "Target assumptions") {
		t.Fatalf("text output=%s", stdout)
	}
}

func TestSizingCommandMissingInputsAndCoverageAreUnknown(t *testing.T) {
	stdout, err := runSizing(t, newSizingHistoryDB(t, "success"))
	if err != nil || !strings.Contains(stdout, "UNKNOWN") {
		t.Fatalf("err=%v output=%s", err, stdout)
	}
	stdout, err = runSizing(t, newSizingHistoryDB(t, "failed"), "--hosts", "9", "--host-cores", "64", "--host-ram-gib", "1024", "--datastore-capacity", "100TiB")
	if err != nil || !strings.Contains(stdout, "UNKNOWN") || !strings.Contains(stdout, "Coverage gaps") {
		t.Fatalf("err=%v output=%s", err, stdout)
	}
	if _, err := runSizing(t, newSizingHistoryDB(t, "success"), "--datastore-capacity", "lots"); err == nil {
		t.Fatal("invalid --datastore-capacity unexpectedly succeeded")
	}
}
