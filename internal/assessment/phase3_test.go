package assessment

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestInfrastructureRoundTripDiffAndTrends(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
	save := func(when time.Time, host vsphere.Host) Run {
		r, err := s.StartRun(context.Background(), "test", []*config.Context{testContext("prod")}, when)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(host)
		err = s.SaveContext(context.Background(), r.ID, ContextResult{Name: "prod", VCenterID: "vc-1", Status: "success", VMs: nil, Collections: []CollectionResult{{Kind: "vm", Status: "empty"}, {Kind: "host", Status: "success", ItemCount: 1, Resources: []ResourceObservation{{VCenterID: "vc-1", Context: "prod", Kind: "host", ID: host.ID, Name: host.Name, Payload: payload}}}, {Kind: "cluster", Status: "empty"}, {Kind: "resourcepool", Status: "empty"}, {Kind: "vapp", Status: "empty"}, {Kind: "dvswitch", Status: "empty"}, {Kind: "datastore", Status: "empty"}, {Kind: "network", Status: "empty"}, {Kind: "snapshot", Status: "empty"}}}, when.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.FinishRun(context.Background(), r.ID, when.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	host := vsphere.Host{ID: "host-1", Name: "esx-1", CPUCores: 16, MemoryMB: 32768}
	r1 := save(base, host)
	host.MemoryMB = 65536
	r2 := save(base.Add(time.Hour), host)
	if r2.Status != RunComplete || r2.SuccessfulCollections != 9 {
		t.Fatalf("run=%+v", r2)
	}
	d, err := s.Diff(context.Background(), r1.ID, r2.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Resources) != 1 || d.Resources[0].Kind != "host" {
		t.Fatalf("resources=%+v", d.Resources)
	}
	trend, err := s.ChurnTrend(context.Background(), TrendOptions{Limit: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(trend.Points) != 2 {
		t.Fatalf("trend=%+v", trend)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if err := s.Backup(context.Background(), backup, false); err != nil {
		t.Fatal(err)
	}
	backupStore, err := Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	if lease, err := backupStore.AcquireCaptureLease(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	} else {
		_ = backupStore.ReleaseCaptureLease(context.Background(), lease)
	}
	_ = backupStore.Close()
	if _, err := s.Restore(context.Background(), backup, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(context.Background(), r1.ID); err != nil {
		t.Fatalf("restored run: %v", err)
	}
}

func TestInfrastructureRuntimeAndPolicy(t *testing.T) {
	d := Diff{Resources: []ResourceChange{{Kind: "host", Changes: []string{"appeared"}}}}
	p := EvaluatePolicy(d, PolicyOptions{FailOn: []string{"host-appeared"}})
	if p.Passed || len(p.Violations) != 1 {
		t.Fatalf("policy=%+v", p)
	}
	if got := changedResourceFields("host", json.RawMessage(`{"power_state":"poweredOn"}`), json.RawMessage(`{"power_state":"poweredOff"}`), false); len(got) != 0 {
		t.Fatalf("default runtime diff=%+v", got)
	}
	if got := changedResourceFields("host", json.RawMessage(`{"power_state":"poweredOn"}`), json.RawMessage(`{"power_state":"poweredOff"}`), true); len(got) != 1 {
		t.Fatalf("runtime diff=%+v", got)
	}
}

func TestOperationLeaseExpiryReclaimsSidecar(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	first, err := s.AcquireOperationLease(context.Background(), now, "test")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcquireOperationLease(context.Background(), now.Add(3*time.Minute), "test2")
	if err != nil {
		t.Fatal(err)
	}
	if second.Operation != "test2" {
		t.Fatalf("lease=%+v", second)
	}
	_ = s.ReleaseCaptureLease(context.Background(), first)
	_ = s.ReleaseCaptureLease(context.Background(), second)
}
