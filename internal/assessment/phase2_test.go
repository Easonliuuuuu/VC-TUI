package assessment

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestRunMetadataSelectorsAndPinning(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, err := s.StartRunWithMetadata(context.Background(), "test", []*config.Context{testContext("prod")}, now, RunMetadata{Label: "nightly-1", Note: "baseline", Pinned: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRun(context.Background(), r.ID)
	if err != nil || got.Label != "nightly-1" || !got.Pinned {
		t.Fatalf("run=%+v err=%v", got, err)
	}
	if id, err := s.ResolveRun(context.Background(), "NIGHTLY-1"); err != nil || id != r.ID {
		t.Fatalf("resolve id=%d err=%v", id, err)
	}
	if err := s.DeleteRunSelector(context.Background(), "nightly-1"); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("delete err=%v", err)
	}
	falseValue := false
	if _, err := s.UpdateRunFields(context.Background(), "nightly-1", nil, nil, &falseValue); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteRunSelector(context.Background(), "nightly-1"); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureLeaseFencesConcurrentWriters(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	lease, err := s.AcquireCaptureLease(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireCaptureLease(context.Background(), now); err == nil {
		t.Fatal("second capture acquired the lease")
	}
	if err := s.ReleaseCaptureLease(context.Background(), lease); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcquireCaptureLease(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

func TestTimelineReportsRenameAndSnapshotEvents(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	v1 := testVM("billing", "vm-1", "instance-1", "bios-1", "esx-1")
	saveTestRun(t, s, base, v1)
	v2 := testVM("billing-renamed", "vm-1", "instance-1", "bios-1", "esx-1")
	v2.Snapshots = []vsphere.VMSnapshot{{ID: "snap-1", Name: "old", CreateTime: base.Add(-time.Hour)}}
	saveTestRun(t, s, base.Add(time.Hour), v2)
	events, err := s.Timeline(context.Background(), "instance-1", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	var renamed, created bool
	for _, e := range events {
		renamed = renamed || e.Kind == "renamed"
		created = created || e.Kind == "snapshot-created"
	}
	if !renamed || !created {
		t.Fatalf("events=%+v", events)
	}
}

func TestTimelineFollowsCrossVCenterMoveByInstanceUUID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cc := []*config.Context{testContext("prod")}
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i, vc := range []string{"vc-a", "vc-b"} {
		r, err := s.StartRun(context.Background(), "test", cc, when.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		v := testVM("billing", fmt.Sprintf("vm-%d", i+1), "instance-move", "bios-move", "esx")
		if err := s.SaveContext(context.Background(), r.ID, ContextResult{Name: "prod", VCenterID: vc, Status: "success", VMs: []Observation{{VCenterID: vc, Context: "prod", VM: v}}}, when.Add(time.Duration(i)*time.Hour+time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FinishRun(context.Background(), r.ID, when.Add(time.Duration(i)*time.Hour+time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := s.Timeline(context.Background(), "instance-move", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "moved" {
			return
		}
	}
	t.Fatalf("events=%+v", events)
}

func TestTimelineRejectsDuplicateUUIDInOneRun(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, err := s.StartRun(context.Background(), "test", []*config.Context{testContext("prod")}, when)
	if err != nil {
		t.Fatal(err)
	}
	v1 := testVM("one", "vm-1", "duplicate", "bios-1", "esx")
	v2 := testVM("two", "vm-2", "duplicate", "bios-2", "esx")
	if err := s.SaveContext(context.Background(), r.ID, ContextResult{Name: "prod", VCenterID: "vc-a", Status: "success", VMs: []Observation{{VCenterID: "vc-a", Context: "prod", VM: v1}, {VCenterID: "vc-a", Context: "prod", VM: v2}}}, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(context.Background(), r.ID, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, err = s.Timeline(context.Background(), "duplicate", "", false, false)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("err=%v", err)
	}
}

func TestTimelineDoesNotDecodeUnrelatedVMs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	when := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r, err := s.StartRun(context.Background(), "test", []*config.Context{testContext("prod")}, when)
	if err != nil {
		t.Fatal(err)
	}
	target := testVM("target", "vm-1", "instance-1", "bios-1", "esx")
	noise := testVM("noise", "vm-2", "instance-2", "bios-2", "esx")
	if err := s.SaveContext(context.Background(), r.ID, ContextResult{
		Name: "prod", VCenterID: "vc-a", Status: "success",
		VMs: []Observation{
			{VCenterID: "vc-a", Context: "prod", VM: target},
			{VCenterID: "vc-a", Context: "prod", VM: noise},
		},
	}, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(context.Background(), r.ID, when.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE vm_observations SET payload='not-json' WHERE name='noise'`); err != nil {
		t.Fatal(err)
	}

	events, err := s.Timeline(context.Background(), "target", "", false, false)
	if err != nil {
		t.Fatalf("Timeline decoded an unrelated VM: %v", err)
	}
	if len(events) != 1 || events[0].Kind != "first_seen" || events[0].Name != "target" {
		t.Fatalf("events=%+v", events)
	}
}

func TestEvaluatePolicy(t *testing.T) {
	d := Diff{Base: Run{Status: RunComplete}, Target: Run{Status: RunComplete}, Counts: DiffCounts{Appeared: 1}}
	p := EvaluatePolicy(d, PolicyOptions{FailOn: []string{"appeared"}})
	if p.Passed || len(p.Violations) != 1 {
		t.Fatalf("policy=%+v", p)
	}
}
