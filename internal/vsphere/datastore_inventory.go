package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// The opt-in datastore file inventory is a separate capture profile from the
// VMDK browse evidence behind orphan analysis (Datastore.Files). It exists
// because that evidence cannot answer "what files are on this datastore":
// it queries VMDKs only, carries no file type, and stops at a much smaller
// cap. Keeping the two apart also means neither can change the other's
// conclusions — orphan analysis never reads FileInventory, so a datastore
// whose inventory was skipped, denied or truncated cannot make an orphan
// finding look better or worse.

// Datastore file inventory statuses.
const (
	// FileInventorySuccess means the whole datastore was listed. It is
	// only complete when Truncated is false.
	FileInventorySuccess = "success"
	// FileInventoryDenied means the account lacked the Datastore.Browse
	// privilege (or the browse was otherwise refused).
	FileInventoryDenied = "denied"
	// FileInventoryFailed means the browse was attempted and errored or ran
	// out of time.
	FileInventoryFailed = "failed"
	// FileInventoryUnavailable means the datastore could not be browsed at
	// all: it was inaccessible or exposed no browser.
	FileInventoryUnavailable = "unavailable"
	// FileInventorySkipped means the capture's context-wide row or time
	// budget was spent before this datastore was reached.
	FileInventorySkipped = "skipped"
)

// Default and maximum bounds for a file inventory capture.
const (
	DefaultFileInventoryMaxFiles          = 100000
	DefaultFileInventoryMaxTotalFiles     = 500000
	DefaultFileInventoryDatastoreTimeout  = 10 * time.Minute
	DefaultFileInventoryContextTimeout    = 30 * time.Minute
	MaxFileInventoryMaxFiles              = 1000000
	MaxFileInventoryMaxTotalFiles         = 5000000
	fileInventoryTruncatedByDatastoreCap  = "datastore file limit"
	fileInventoryTruncatedByContextBudget = "context file limit"
)

// FileInventoryOptions opts a capture into the datastore file inventory and
// bounds it. The zero value is NOT "enabled with defaults": a nil
// *FileInventoryOptions is what disables it, so a caller must construct one
// on purpose.
type FileInventoryOptions struct {
	// MaxFilesPerDatastore caps the rows kept for one datastore.
	MaxFilesPerDatastore int
	// MaxFilesPerContext caps the rows kept across every datastore of one
	// vCenter context.
	MaxFilesPerContext int
	// DatastoreTimeout bounds one datastore's recursive listing.
	DatastoreTimeout time.Duration
	// ContextTimeout bounds the whole inventory of one vCenter context.
	ContextTimeout time.Duration
}

// WithDefaults fills every unset bound and clamps the rest to their maxima.
func (o FileInventoryOptions) WithDefaults() FileInventoryOptions {
	if o.MaxFilesPerDatastore <= 0 {
		o.MaxFilesPerDatastore = DefaultFileInventoryMaxFiles
	}
	if o.MaxFilesPerDatastore > MaxFileInventoryMaxFiles {
		o.MaxFilesPerDatastore = MaxFileInventoryMaxFiles
	}
	if o.MaxFilesPerContext <= 0 {
		o.MaxFilesPerContext = DefaultFileInventoryMaxTotalFiles
	}
	if o.MaxFilesPerContext > MaxFileInventoryMaxTotalFiles {
		o.MaxFilesPerContext = MaxFileInventoryMaxTotalFiles
	}
	if o.DatastoreTimeout <= 0 {
		o.DatastoreTimeout = DefaultFileInventoryDatastoreTimeout
	}
	if o.ContextTimeout <= 0 {
		o.ContextTimeout = DefaultFileInventoryContextTimeout
	}
	return o
}

// DatastoreInventoryFile is one file of the opt-in datastore file inventory.
// Path is the canonical "[datastore] relative/path" form; Type is the vSphere
// datastore-browser file class (for example VmDiskFileInfo). Content is never
// read. Folders are not listed.
type DatastoreInventoryFile struct {
	Path      string    `json:"path"`
	Type      string    `json:"type"`
	SizeBytes int64     `json:"size_bytes"`
	Modified  time.Time `json:"modified,omitempty"`
}

// DatastoreFileInventory records what one opt-in file inventory pass saw on
// one datastore, and how far it got. A nil *DatastoreFileInventory on a
// Datastore means inventory was never requested for that capture, which is
// not the same thing as an empty one.
type DatastoreFileInventory struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Truncated reports that a bound cut the listing short: Files is then a
	// prefix of the datastore, never the whole of it.
	Truncated       bool   `json:"truncated,omitempty"`
	TruncatedReason string `json:"truncated_reason,omitempty"`
	// Limit is the row cap that applied to this datastore.
	Limit int                      `json:"limit,omitempty"`
	Files []DatastoreInventoryFile `json:"files,omitempty"`
}

// Complete reports whether the inventory listed every file the datastore
// holds. Only a successful, untruncated pass qualifies.
func (i *DatastoreFileInventory) Complete() bool {
	return i != nil && i.Status == FileInventorySuccess && !i.Truncated
}

// inventoryDatastores fills FileInventory on every datastore, in a
// deterministic (name, ID) order so that when a shared budget runs out the
// same datastores are skipped on every run.
func (c *Client) inventoryDatastores(parent context.Context, datastores []Datastore, browsers map[string]types.ManagedObjectReference, opts FileInventoryOptions) {
	opts = opts.WithDefaults()
	order := make([]int, len(datastores))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		x, y := datastores[order[a]], datastores[order[b]]
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		return x.ID < y.ID
	})
	ctx, cancel := context.WithTimeout(parent, opts.ContextTimeout)
	defer cancel()
	remaining := opts.MaxFilesPerContext
	for _, i := range order {
		ds := &datastores[i]
		inv := &DatastoreFileInventory{}
		ds.FileInventory = inv
		switch {
		case !ds.Accessible:
			inv.Status, inv.Error = FileInventoryUnavailable, "datastore is inaccessible"
			continue
		case ctx.Err() != nil:
			inv.Status = FileInventorySkipped
			inv.Error = "context file inventory time budget was spent before this datastore was reached"
			continue
		case remaining <= 0:
			inv.Status = FileInventorySkipped
			inv.Error = "context file inventory row budget was spent before this datastore was reached"
			continue
		}
		browser := browsers[ds.ID]
		if browser.Type == "" || browser.Value == "" {
			inv.Status, inv.Error = FileInventoryUnavailable, "datastore browser reference is unavailable"
			continue
		}
		limit := min(opts.MaxFilesPerDatastore, remaining)
		byContext := remaining < opts.MaxFilesPerDatastore
		files, truncated, err := c.inventoryDatastore(ctx, ds.Name, browser, limit, opts.DatastoreTimeout)
		inv.Limit = limit
		if err != nil {
			inv.Status, inv.Error = classifyInventoryError(err), err.Error()
			continue
		}
		inv.Status, inv.Files = FileInventorySuccess, files
		remaining -= len(files)
		if truncated {
			inv.Truncated = true
			inv.TruncatedReason = fileInventoryTruncatedByDatastoreCap
			if byContext {
				inv.TruncatedReason = fileInventoryTruncatedByContextBudget
			}
		}
	}
}

// classifyInventoryError separates "the account may not browse" from every
// other failure, because the remedy differs: a privilege grant versus a retry.
func classifyInventoryError(err error) string {
	if soap.IsSoapFault(err) {
		if _, ok := soap.ToSoapFault(err).VimFault().(types.NoPermission); ok {
			return FileInventoryDenied
		}
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"permission", "privilege", "not authorized", "unauthorized", "denied"} {
		if strings.Contains(text, marker) {
			return FileInventoryDenied
		}
	}
	return FileInventoryFailed
}

// inventoryDatastore lists every file of one datastore through the recursive
// browser search, keeping at most limit rows.
func (c *Client) inventoryDatastore(parent context.Context, datastore string, browser types.ManagedObjectReference, limit int, timeout time.Duration) ([]DatastoreInventoryFile, bool, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	spec := &types.HostDatastoreBrowserSearchSpec{
		MatchPattern: []string{"*"},
		// Every typed file query plus the generic one: the typed queries are
		// what make vSphere classify a file (and fold a disk's -flat.vmdk
		// extent into its descriptor), the generic FileQuery catches
		// everything else as plain FileInfo. FolderFileQuery is included so
		// that folders come back typed and can be dropped: without it a real
		// vCenter reports each subfolder as a plain FileInfo, and folders are
		// not inventory rows.
		Query: []types.BaseFileQuery{
			&types.FolderFileQuery{},
			&types.VmConfigFileQuery{},
			&types.TemplateConfigFileQuery{},
			&types.VmDiskFileQuery{},
			&types.VmLogFileQuery{},
			&types.VmNvramFileQuery{},
			&types.VmSnapshotFileQuery{},
			&types.FloppyImageFileQuery{},
			&types.IsoImageFileQuery{},
			&types.FileQuery{},
		},
		Details: &types.FileQueryFlags{FileSize: true, Modification: true, FileType: true},
	}
	result, err := c.searchDatastoreSubFolders(ctx, browser, fmt.Sprintf("[%s]", datastore), spec)
	if err != nil {
		switch {
		case errors.Is(parent.Err(), context.Canceled):
			return nil, false, fmt.Errorf("file listing was cancelled: %w", context.Canceled)
		case parent.Err() != nil:
			return nil, false, fmt.Errorf("context file inventory time budget was spent while listing this datastore: %w", parent.Err())
		case ctx.Err() != nil:
			return nil, false, fmt.Errorf("file listing timed out after %s: %w", timeout, context.DeadlineExceeded)
		}
		return nil, false, err
	}
	files, truncated := datastoreInventoryFiles(datastore, result, limit)
	return files, truncated, nil
}

// datastoreInventoryFiles maps a browser result to inventory rows, sorted and
// capped at limit. Truncation is decided after sorting so which rows survive
// does not depend on the order the server happened to return folders.
func datastoreInventoryFiles(datastore string, result types.AnyType, limit int) ([]DatastoreInventoryFile, bool) {
	var files []DatastoreInventoryFile
	searches := browserSearchResults(result)
	// Every searched folder is itself a result, so its path also identifies
	// the entry its parent listed for it, however that entry was typed.
	folders := make(map[string]bool, len(searches))
	for _, search := range searches {
		folders[datastoreFilePath(datastore, search.FolderPath, "")] = true
	}
	for _, search := range searches {
		for _, file := range search.File {
			if _, folder := file.(*types.FolderFileInfo); folder {
				continue
			}
			info := file.GetFileInfo()
			if info == nil {
				continue
			}
			var modified time.Time
			if info.Modification != nil {
				modified = info.Modification.UTC()
			}
			path := datastoreFilePath(datastore, search.FolderPath, info.Path)
			if folders[path] {
				continue
			}
			files = append(files, DatastoreInventoryFile{
				Path:      path,
				Type:      strings.TrimPrefix(fmt.Sprintf("%T", file), "*types."),
				SizeBytes: info.FileSize,
				Modified:  modified,
			})
		}
	}
	SortInventoryFiles(files)
	if limit > 0 && len(files) > limit {
		return files[:limit], true
	}
	return files, false
}

// SortInventoryFiles orders files by path (case-insensitively, then exactly),
// then type, then size: a total order, so identical evidence always renders
// identically.
func SortInventoryFiles(files []DatastoreInventoryFile) {
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i], files[j]
		if la, lb := strings.ToLower(a.Path), strings.ToLower(b.Path); la != lb {
			return la < lb
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Type != b.Type {
			return a.Type < b.Type
		}
		return a.SizeBytes < b.SizeBytes
	})
}

// datastoreBrowsers maps datastore IDs to their browser references for a
// property retrieval already performed by the caller.
func datastoreBrowsers(raw []mo.Datastore) map[string]types.ManagedObjectReference {
	out := make(map[string]types.ManagedObjectReference, len(raw))
	for i := range raw {
		out[raw[i].Self.Value] = raw[i].Browser
	}
	return out
}
