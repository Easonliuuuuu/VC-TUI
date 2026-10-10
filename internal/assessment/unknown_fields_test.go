package assessment

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// saveContextRun stores one run captured by the named context of the shared
// vCenter vc-1, like an operator switching accounts between runs.
func saveContextRun(t *testing.T, s *Store, contextName string, when time.Time, vm vsphere.VM) {
	t.Helper()
	r, err := s.StartRun(context.Background(), "test", []*config.Context{testContext(contextName)}, when)
	if err != nil {
		t.Fatal(err)
	}
	result := ContextResult{Name: contextName, VCenterID: "vc-1", Status: "success", VMs: []Observation{{VCenterID: "vc-1", Context: contextName, VM: vm}}, Collections: []CollectionResult{{Kind: "snapshot", Status: "success"}}}
	if err := s.SaveContext(context.Background(), r.ID, result, when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishRun(context.Background(), r.ID, when.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

// withoutHostAccess is vm as an account that cannot read hosts stores it.
func withoutHostAccess(vm vsphere.VM) vsphere.VM {
	vm.Host, vm.Cluster, vm.Datastores = "", "", nil
	return vm
}

// withoutConfigAccess is vm as an account that cannot read its configuration
// stores it.
func withoutConfigAccess(vm vsphere.VM) vsphere.VM {
	vm.CPU, vm.MemoryMB, vm.GuestOS, vm.Annotation = 0, 0, "", ""
	return vm
}

func eventKinds(events []VMHistoryEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Kind
	}
	return out
}

func TestTimelineTreatsUnreadablePlacementAsUnknown(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	full := testVM("billing", "vm-1", "instance-1", "bios-1", "esx-1")
	saveContextRun(t, s, "full", base, full)
	saveContextRun(t, s, "nohost", base.Add(time.Hour), withoutHostAccess(full))
	saveContextRun(t, s, "full", base.Add(2*time.Hour), full)
	events, err := s.Timeline(context.Background(), "instance-1", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(events); len(got) != 1 || got[0] != "first_seen" {
		t.Fatalf("unreadable placement produced events %v: %+v", got, events)
	}
}

func TestTimelineComparesAcrossARunThatCouldNotSeeThePlacement(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	before := testVM("billing", "vm-1", "instance-1", "bios-1", "esx-1")
	after := testVM("billing", "vm-1", "instance-1", "bios-1", "esx-2")
	saveContextRun(t, s, "full", base, before)
	saveContextRun(t, s, "nohost", base.Add(time.Hour), withoutHostAccess(before))
	saveContextRun(t, s, "full", base.Add(2*time.Hour), after)
	events, err := s.Timeline(context.Background(), "instance-1", "", false, true)
	if err != nil {
		t.Fatal(err)
	}
	var moves []VMHistoryEvent
	for _, e := range events {
		if e.Kind == "moved" {
			moves = append(moves, e)
		}
	}
	if len(moves) != 1 || moves[0].Context != "full" || len(moves[0].Changes) != 1 || moves[0].Changes[0] != (FieldChange{Field: "host", Before: "esx-1", After: "esx-2"}) {
		t.Fatalf("a real host change across the blind run must be reported once, with the host it left: %+v", events)
	}
}

func TestTimelineTreatsUnreadableConfigurationAsUnknown(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	full := testVM("billing", "vm-1", "instance-1", "bios-1", "esx-1")
	grown := full
	grown.CPU = 4
	saveContextRun(t, s, "full", base, full)
	saveContextRun(t, s, "noconfig", base.Add(time.Hour), withoutConfigAccess(full))
	saveContextRun(t, s, "full", base.Add(2*time.Hour), grown)
	events, err := s.Timeline(context.Background(), "instance-1", "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventKinds(events); len(got) != 2 || got[1] != "modified" || len(events[1].Changes) != 1 || events[1].Changes[0] != (FieldChange{Field: "cpu", Before: "2", After: "4"}) {
		t.Fatalf("only the real cpu change may be reported, once: %v %+v", got, events)
	}
}

func TestMovedTreatsEmptyPlacementAsUnknown(t *testing.T) {
	full := testVM("billing", "vm-1", "instance-1", "bios-1", "esx-1")
	full.Folder = "/prod"
	if moved(full, withoutHostAccess(full), "vc-1", "vc-1") || moved(withoutHostAccess(full), full, "vc-1", "vc-1") {
		t.Fatal("an empty host, cluster or datastore list is unknown, not a move")
	}
	elsewhere := full
	elsewhere.Host = "esx-2"
	if !moved(full, elsewhere, "vc-1", "vc-1") {
		t.Fatal("a different host must still be a move")
	}
	elsewhere = full
	elsewhere.Datastores = []string{"ds-b"}
	if !moved(full, elsewhere, "vc-1", "vc-1") {
		t.Fatal("a different datastore must still be a move")
	}
	if !moved(full, withoutHostAccess(full), "vc-1", "vc-2") {
		t.Fatal("a different vCenter must still be a move")
	}
}

func TestCompareVMsIgnoresFieldsTheTargetAccountCouldNotSee(t *testing.T) {
	full := stored("billing", "vc-1", "vm-1", "instance-1")
	full.observation.VM = testVM("billing", "vm-1", "instance-1", "bios-1", "esx-1")
	blind := full
	blind.observation.Context = "nohost"
	blind.observation.VM = withoutConfigAccess(withoutHostAccess(full.observation.VM))
	if changes, _ := compareVMs([]storedVM{full}, []storedVM{blind}, false); len(changes) != 0 {
		t.Fatalf("a restricted account's empty fields reported as changes: %+v", changes)
	}
	if changes, _ := compareVMs([]storedVM{blind}, []storedVM{full}, false); len(changes) != 0 {
		t.Fatalf("a restricted account's empty fields reported as changes: %+v", changes)
	}
	moves := full
	moves.observation.VM.Host = "esx-2"
	changes, _ := compareVMs([]storedVM{full}, []storedVM{moves}, false)
	if len(changes) != 1 || changes[0].Changes[0] != "moved" {
		t.Fatalf("a real host change must still be reported: %+v", changes)
	}
}
