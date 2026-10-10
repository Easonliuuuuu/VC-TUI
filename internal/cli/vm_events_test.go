package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestStoredEventTargetsReadEachVCenterOnce(t *testing.T) {
	seen := func(contextName, vcenter, id string) assessment.VMHistoryEvent {
		return assessment.VMHistoryEvent{Observation: &assessment.Observation{Context: contextName, VCenterID: vcenter, VM: vsphere.VM{ID: id}}}
	}
	got := storedEventTargets([]assessment.VMHistoryEvent{
		seen("b-alias", "vc-1", "vm-7"),
		seen("a-lab", "vc-1", "vm-7"),
		seen("a-lab", "vc-1", "vm-7"),
		// Re-registered: an older ID on the same vCenter keeps older events.
		seen("a-lab", "vc-1", "vm-3"),
		// The same ID on another vCenter is a different VM.
		seen("other", "vc-2", "vm-7"),
		// No vCenter identity: only the same context is known to be the same.
		seen("legacy", "", "vm-9"),
		seen("legacy-2", "", "vm-9"),
		{Kind: "vanished"},
		seen("a-lab", "vc-1", ""),
	})
	want := []eventTarget{
		{vmID: "vm-3", contexts: []string{"a-lab"}},
		{vmID: "vm-7", contexts: []string{"a-lab", "b-alias"}},
		{vmID: "vm-9", contexts: []string{"legacy"}},
		{vmID: "vm-9", contexts: []string{"legacy-2"}},
		{vmID: "vm-7", contexts: []string{"other"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %+v\nwant     %+v", got, want)
	}
}

func TestTaskHistoryNoteNamesTheContextAndWhatIsLeftOut(t *testing.T) {
	note := taskHistoryNote(vsphere.VMEventListing{Context: "lab", TaskHistoryError: "NoPermission: System.View"})
	for _, want := range []string{"task history on lab", "task results are unknown", "failed tasks may be missing", "NoPermission: System.View"} {
		if !strings.Contains(note, want) {
			t.Fatalf("note %q is missing %q", note, want)
		}
	}
	// A merged listing names each context in its reason instead.
	if note := taskHistoryNote(vsphere.VMEventListing{TaskHistoryError: "a: denied; b: denied"}); strings.Contains(note, " on ") || !strings.HasSuffix(note, "a: denied; b: denied") {
		t.Fatalf("merged note = %q", note)
	}
}
