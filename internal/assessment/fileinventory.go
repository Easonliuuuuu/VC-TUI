package assessment

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// InventoryFileInfoSchema is the first inventory schema that can carry the
// opt-in datastore file inventory.
const InventoryFileInfoSchema = 19

// File inventory coverage states, as reported per datastore and per context.
// They are deliberately more specific than a collection status: "denied" and
// "skipped" call for different follow-up than "failed".
const (
	FileInventoryComplete    = "complete"
	FileInventoryTruncated   = "truncated"
	FileInventoryDenied      = vsphere.FileInventoryDenied
	FileInventoryFailed      = vsphere.FileInventoryFailed
	FileInventorySkipped     = vsphere.FileInventorySkipped
	FileInventoryUnavailable = vsphere.FileInventoryUnavailable
	FileInventoryNotRecorded = "not recorded"

	// FileInventoryNotRequestedMessage explains a context with no inventory.
	FileInventoryNotRequestedMessage = "datastore file inventory was not requested for this capture (assessment run --datastore-file-inventory); vFileInfo says nothing about the files on this context's datastores"
)

// FileInventoryDatastore is one datastore's inventory coverage in a stored run.
type FileInventoryDatastore struct {
	Context   string `json:"context"`
	Datastore string `json:"datastore"`
	ID        string `json:"datastore_id,omitempty"`
	// Status is one of the FileInventory* constants.
	Status string `json:"status"`
	// Files is the number of file rows captured, which for anything other than
	// "complete" is a lower bound rather than a count of the datastore.
	Files   int    `json:"files"`
	Message string `json:"message,omitempty"`
}

// Incomplete reports whether the datastore's file list must not be read as
// everything the datastore holds.
func (d FileInventoryDatastore) Incomplete() bool { return d.Status != FileInventoryComplete }

// FileInventoryContext is one context's inventory coverage.
type FileInventoryContext struct {
	Context string `json:"context"`
	// Requested is true when at least one datastore of the context carries an
	// inventory record. False means the capture never asked.
	Requested  bool                     `json:"requested"`
	Datastores []FileInventoryDatastore `json:"datastores,omitempty"`
}

// Files is the number of file rows captured for the context.
func (c FileInventoryContext) Files() int {
	n := 0
	for _, d := range c.Datastores {
		n += d.Files
	}
	return n
}

// Gaps lists the datastores whose file list is not complete.
func (c FileInventoryContext) Gaps() []FileInventoryDatastore {
	var out []FileInventoryDatastore
	for _, d := range c.Datastores {
		if d.Incomplete() {
			out = append(out, d)
		}
	}
	return out
}

// Summary reduces a context's coverage to a status usable in vsfleetCoverage
// (success, empty, partial, failed, not recorded) and a human message. Only a
// context whose every datastore was listed to the end is success or empty.
func (c FileInventoryContext) Summary() (status string, message string) {
	if !c.Requested {
		return FileInventoryNotRecorded, FileInventoryNotRequestedMessage
	}
	gaps := c.Gaps()
	switch {
	case len(gaps) == 0 && c.Files() == 0:
		return "empty", "every datastore was listed to the end and holds no files"
	case len(gaps) == 0:
		return "success", ""
	}
	parts := make([]string, 0, len(gaps))
	for _, g := range gaps {
		parts = append(parts, fmt.Sprintf("%s %s", g.Datastore, g.Status))
	}
	message = fmt.Sprintf("%d of %d datastores incomplete (%s); an absent file is NOT evidence that it does not exist", len(gaps), len(c.Datastores), strings.Join(parts, ", "))
	if len(gaps) == len(c.Datastores) {
		return "failed", message
	}
	return "partial", message
}

// InventoryDatastoreStatus classifies one datastore's stored inventory.
func InventoryDatastoreStatus(inv *vsphere.DatastoreFileInventory) (status, message string) {
	switch {
	case inv == nil:
		return FileInventoryNotRecorded, "no file inventory was recorded for this datastore"
	case inv.Status == vsphere.FileInventorySuccess && inv.Truncated:
		reason := inv.TruncatedReason
		if reason == "" {
			reason = "row limit"
		}
		return FileInventoryTruncated, fmt.Sprintf("listing stopped at %d files (%s); files beyond it are not in this inventory", len(inv.Files), reason)
	case inv.Status == vsphere.FileInventorySuccess:
		return FileInventoryComplete, ""
	case inv.Status == "":
		return FileInventoryFailed, "file inventory status was not recorded"
	default:
		msg := strings.TrimSpace(inv.Error)
		if msg == "" {
			msg = "file inventory " + inv.Status
		}
		return inv.Status, msg
	}
}

// FileInventoryCoverage derives file inventory coverage for every context in
// a stored run purely from persisted evidence. It never contacts a vCenter.
// Contexts appear in the run's context order; datastores are ordered by name
// then ID so the result is deterministic.
func FileInventoryCoverage(data ExportData) []FileInventoryContext {
	out := make([]FileInventoryContext, 0, len(data.Contexts))
	byContext := make(map[string]int, len(data.Contexts))
	for _, c := range data.Contexts {
		byContext[c.Name] = len(out)
		out = append(out, FileInventoryContext{Context: c.Name})
	}
	for _, r := range data.Resources {
		if r.Kind != "datastore" {
			continue
		}
		i, ok := byContext[r.Context]
		if !ok {
			continue
		}
		var ds vsphere.Datastore
		if err := json.Unmarshal(r.Payload, &ds); err != nil {
			continue
		}
		entry := FileInventoryDatastore{Context: r.Context, Datastore: nonempty(ds.Name, r.Name), ID: nonempty(ds.ID, r.ID)}
		entry.Status, entry.Message = InventoryDatastoreStatus(ds.FileInventory)
		if ds.FileInventory != nil {
			out[i].Requested = true
			entry.Files = len(ds.FileInventory.Files)
		}
		out[i].Datastores = append(out[i].Datastores, entry)
	}
	for i := range out {
		ds := out[i].Datastores
		sort.SliceStable(ds, func(a, b int) bool {
			if ds[a].Datastore != ds[b].Datastore {
				return ds[a].Datastore < ds[b].Datastore
			}
			return ds[a].ID < ds[b].ID
		})
	}
	return out
}

// HasFileInventory reports whether any datastore of the run carries an
// inventory record, that is, whether the capture opted in.
func HasFileInventory(data ExportData) bool {
	for _, c := range FileInventoryCoverage(data) {
		if c.Requested {
			return true
		}
	}
	return false
}
