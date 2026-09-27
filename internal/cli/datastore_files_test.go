package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// An empty file is a measured size, not a missing one: it must read 0B, and
// only a folder keeps the "-" placeholder.
func TestDatastoreFilesShowsEmptyFilesAsZeroBytes(t *testing.T) {
	var out bytes.Buffer
	a := &App{Out: &out}
	entries := []vsphere.DatastoreEntry{
		{Name: "logs", Path: "[ds-01] vm/logs", Type: vsphere.DatastoreEntryFolder},
		{Name: "vm.vmsd", Path: "[ds-01] vm/vm.vmsd", Type: vsphere.DatastoreEntryFile},
		{Name: "vm.vmx", Path: "[ds-01] vm/vm.vmx", Type: vsphere.DatastoreEntryFile, SizeBytes: 2048},
	}
	result := datastoreBrowseResult{
		listing:   vsphere.DatastoreListing{Entries: entries},
		datastore: vsphere.Datastore{Name: "ds-01"},
	}

	printDatastoreListing(a, "lab", "vm", result)
	printDatastoreFind(a, "lab", "vm*", false, entries, result)

	want := map[string]string{"logs/": "-", "vm.vmsd": "0B", "vm.vmx": "2.0K", "[ds-01] vm/logs": "-", "[ds-01] vm/vm.vmsd": "0B"}
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[1]
		if strings.HasPrefix(name, "[") && len(fields) > 3 {
			name += " " + fields[2]
			fields = append(fields[:2], fields[3:]...)
		}
		if size, ok := want[name]; ok {
			if fields[2] != size {
				t.Errorf("%s size = %q, want %q\n%s", name, fields[2], size, out.String())
			}
			delete(want, name)
		}
	}
	if len(want) != 0 {
		t.Errorf("rows not rendered: %v\n%s", want, out.String())
	}
}
