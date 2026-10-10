package vsphere

import (
	"testing"

	"github.com/vmware/govmomi/vim25/types"
)

// TestReconfiguredValueReadsWhatDetailWrites keeps the parser and the
// formatter of a reconfiguration's one-line detail in step.
func TestReconfiguredValueReadsWhatDetailWrites(t *testing.T) {
	fields, detail := reconfiguredFields(types.VirtualMachineConfigSpec{NumCPUs: 4, MemoryMB: 8192, Annotation: "x", Name: "web"})
	e := VMEvent{Fields: fields, Detail: detail}
	for field, want := range map[string]string{"cpu": "4", "memory": "8192"} {
		if got, ok := e.ReconfiguredValue(field); !ok || got != want {
			t.Errorf("ReconfiguredValue(%q) = %q, %v from %q; want %q", field, got, ok, detail, want)
		}
	}
	for _, field := range []string{"annotation", "name", "devices", "guest_os"} {
		if got, ok := e.ReconfiguredValue(field); ok {
			t.Errorf("ReconfiguredValue(%q) = %q; only cpu and memory carry a value", field, got)
		}
	}
	memoryOnly := VMEvent{Detail: "memory 512 MB"}
	if _, ok := memoryOnly.ReconfiguredValue("cpu"); ok {
		t.Error("an event that did not set cpu reported a cpu value")
	}
	if _, ok := (VMEvent{}).ReconfiguredValue("memory"); ok {
		t.Error("an event without a detail reported a memory value")
	}
}
