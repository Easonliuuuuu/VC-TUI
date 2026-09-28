package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

const (
	datastoreBrowseTimeout = 2 * time.Minute
	datastoreBrowseFileCap = 10000
)

// The interactive browser's bounds are deliberately its own constants rather
// than the assessment ones above. The two workflows want different numbers —
// an operator waiting on one directory will not sit through two minutes, and a
// capture recording evidence would rather wait than come back short — and
// keeping them separate means tuning either one can never quietly change the
// other's behaviour.
const (
	datastoreListTimeout   = 30 * time.Second
	datastoreListEntryCap  = 5000
	datastoreFindTimeout   = 2 * time.Minute
	datastoreFindResultCap = 1000
)

// The two SOAP bodies below are the only deliberately hand-rolled operations
// in the read-only client. The govmomi wrappers for these methods live in
// packages that also expose mutations, so keeping both bodies in this one file
// makes the operations and their review surface explicit — see
// TestOnlyPerfAndBrowserShimsDefineFault, which holds that line (the other reviewed file is perf_query.go).

// searchDatastoreSubFoldersTaskBody is the recursive search: it descends the
// whole datastore. Both the assessment's VMDK sweep and the operator-triggered
// "find in datastore" use it.
type searchDatastoreSubFoldersTaskBody struct {
	Req    *types.SearchDatastoreSubFolders_Task         `xml:"urn:vim25 SearchDatastoreSubFolders_Task,omitempty"`
	Res    *types.SearchDatastoreSubFolders_TaskResponse `xml:"SearchDatastoreSubFolders_TaskResponse,omitempty"`
	Fault_ *soap.Fault                                   `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *searchDatastoreSubFoldersTaskBody) Fault() *soap.Fault { return b.Fault_ }

// searchDatastoreTaskBody is the single-directory listing. It is
// non-recursive by construction — the operation descends nowhere — which is
// what makes the interactive browser lazy rather than an enumeration of the
// whole datastore dressed up as navigation.
type searchDatastoreTaskBody struct {
	Req    *types.SearchDatastore_Task         `xml:"urn:vim25 SearchDatastore_Task,omitempty"`
	Res    *types.SearchDatastore_TaskResponse `xml:"SearchDatastore_TaskResponse,omitempty"`
	Fault_ *soap.Fault                         `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *searchDatastoreTaskBody) Fault() *soap.Fault { return b.Fault_ }

func (c *Client) browseDatastoreFiles(parent context.Context, datastore string, browser types.ManagedObjectReference) ([]DatastoreFile, string, string, bool) {
	ctx, cancel := context.WithTimeout(parent, datastoreBrowseTimeout)
	defer cancel()
	if browser.Type == "" || browser.Value == "" {
		return nil, "failed", "datastore browser reference is unavailable", false
	}

	spec := &types.HostDatastoreBrowserSearchSpec{
		MatchPattern: []string{"*"},
		Query:        []types.BaseFileQuery{&types.VmDiskFileQuery{}},
		Details: &types.FileQueryFlags{
			FileSize:     true,
			Modification: true,
			FileType:     true,
		},
	}
	result, err := c.searchDatastoreSubFolders(ctx, browser, fmt.Sprintf("[%s]", datastore), spec)
	if err != nil {
		return nil, "failed", err.Error(), false
	}

	files, truncated := datastoreFiles(datastore, result)
	sort.SliceStable(files, func(i, j int) bool {
		if !strings.EqualFold(files[i].Path, files[j].Path) {
			return strings.ToLower(files[i].Path) < strings.ToLower(files[j].Path)
		}
		if files[i].Path != files[j].Path {
			return files[i].Path < files[j].Path
		}
		return files[i].SizeBytes < files[j].SizeBytes
	})
	return files, "success", "", truncated
}

// searchDatastoreSubFolders issues the recursive search and waits for it.
func (c *Client) searchDatastoreSubFolders(ctx context.Context, browser types.ManagedObjectReference, datastorePath string, spec *types.HostDatastoreBrowserSearchSpec) (types.AnyType, error) {
	reqBody := searchDatastoreSubFoldersTaskBody{Req: &types.SearchDatastoreSubFolders_Task{
		This:          browser,
		DatastorePath: datastorePath,
		SearchSpec:    spec,
	}}
	var resBody searchDatastoreSubFoldersTaskBody
	if err := c.VIM().RoundTrip(ctx, &reqBody, &resBody); err != nil {
		return nil, err
	}
	if resBody.Res == nil || resBody.Res.Returnval.Value == "" {
		return nil, errors.New("datastore browser returned no task")
	}
	return c.waitForBrowserTask(ctx, resBody.Res.Returnval)
}

// searchDatastore issues the single-directory listing and waits for it.
func (c *Client) searchDatastore(ctx context.Context, browser types.ManagedObjectReference, datastorePath string, spec *types.HostDatastoreBrowserSearchSpec) (types.AnyType, error) {
	reqBody := searchDatastoreTaskBody{Req: &types.SearchDatastore_Task{
		This:          browser,
		DatastorePath: datastorePath,
		SearchSpec:    spec,
	}}
	var resBody searchDatastoreTaskBody
	if err := c.VIM().RoundTrip(ctx, &reqBody, &resBody); err != nil {
		return nil, err
	}
	if resBody.Res == nil || resBody.Res.Returnval.Value == "" {
		return nil, errors.New("datastore browser returned no task")
	}
	return c.waitForBrowserTask(ctx, resBody.Res.Returnval)
}

// waitForBrowserTask watches one datastore browser task to completion and
// returns its result. The task is only ever a handle: the browser returns file
// metadata through it and has no way to change anything in inventory.
func (c *Client) waitForBrowserTask(ctx context.Context, taskRef types.ManagedObjectReference) (types.AnyType, error) {
	collector, err := property.DefaultCollector(c.VIM()).Create(ctx)
	if err != nil {
		return nil, fmt.Errorf("create datastore browser property collector: %w", err)
	}
	defer func() { _ = collector.Destroy(context.WithoutCancel(ctx)) }()
	filterSpec := types.PropertyFilterSpec{
		ObjectSet: []types.ObjectSpec{{Obj: taskRef}},
		// The Task managed object exposes the complete info value atomically;
		// watching it also avoids losing the result when a server coalesces the
		// state and result changes into one update.
		PropSet: []types.PropertySpec{{Type: "Task", PathSet: []string{"info"}}},
	}
	if _, err := collector.CreateFilter(ctx, types.CreateFilter{Spec: filterSpec}); err != nil {
		return nil, fmt.Errorf("create datastore browser task filter: %w", err)
	}

	var state types.TaskInfoState
	var result types.AnyType
	var taskErr *types.LocalizedMethodFault
	waitOptions := &property.WaitOptions{Options: &types.WaitOptions{}}
	err = collector.WaitForUpdatesEx(ctx, waitOptions, func(updates []types.ObjectUpdate) bool {
		for _, update := range updates {
			if update.Obj != taskRef {
				continue
			}
			for _, change := range update.ChangeSet {
				switch change.Name {
				case "info":
					switch value := change.Val.(type) {
					case types.TaskInfo:
						state, result, taskErr = value.State, value.Result, value.Error
					case *types.TaskInfo:
						if value != nil {
							state, result, taskErr = value.State, value.Result, value.Error
						}
					}
				case "info.state":
					state = taskInfoState(change.Val)
				case "info.result":
					result = change.Val
				case "info.error":
					switch value := change.Val.(type) {
					case *types.LocalizedMethodFault:
						taskErr = value
					case types.LocalizedMethodFault:
						taskErr = &value
					}
				}
			}
		}
		return state == types.TaskInfoStateSuccess || state == types.TaskInfoStateError
	})
	if err != nil {
		return nil, fmt.Errorf("wait for datastore browser task: %w", err)
	}
	if state == types.TaskInfoStateError {
		// The server's own localized text is the whole value here: a denied
		// browse and an unreachable host are different problems, and only
		// vCenter can tell them apart.
		if taskErr != nil && taskErr.LocalizedMessage != "" {
			return nil, errors.New(taskErr.LocalizedMessage)
		}
		return nil, errors.New("datastore browser task failed")
	}
	return result, nil
}

// ListDatastoreDirectory lists exactly one directory of one datastore.
//
// It is the interactive counterpart to browseDatastoreFiles and shares none of
// its semantics: it descends nowhere, returns folders as well as files, and
// reports a failure as an error rather than as recorded provenance, because
// the caller is an operator waiting for an answer rather than a capture
// filing evidence. relative is the path inside the datastore, "" for the root.
func (c *Client) ListDatastoreDirectory(parent context.Context, datastoreID, datastore, relative string) (DatastoreListing, error) {
	ctx, cancel := context.WithTimeout(parent, datastoreListTimeout)
	defer cancel()

	relative, err := normalizeBrowsePath(relative)
	if err != nil {
		return DatastoreListing{}, err
	}
	browser, err := c.datastoreBrowserRef(ctx, datastoreID)
	if err != nil {
		return DatastoreListing{}, err
	}
	result, err := c.searchDatastore(ctx, browser, datastoreDirPath(datastore, relative), browseSearchSpec("*"))
	if err != nil {
		return DatastoreListing{}, err
	}
	return datastoreListing(datastore, result, datastoreListEntryCap), nil
}

// ListDatastoreVMReferences returns the virtual disks configured on the VMs
// attached to one datastore. It is intentionally separate from inventory
// retrieval: the TUI calls it only after an operator opens a VMDK's detail.
// A missing VM configuration is retained as partial coverage rather than
// being interpreted as an unreferenced disk.
func (c *Client) ListDatastoreVMReferences(parent context.Context, datastoreID string) (DatastoreReferenceListing, error) {
	ctx, cancel := context.WithTimeout(parent, datastoreListTimeout)
	defer cancel()
	datastoreID = strings.TrimSpace(datastoreID)
	if datastoreID == "" {
		return DatastoreReferenceListing{}, errors.New("datastore reference is unavailable")
	}
	dsref := types.ManagedObjectReference{Type: "Datastore", Value: datastoreID}
	var ds mo.Datastore
	if err := property.DefaultCollector(c.VIM()).RetrieveOne(ctx, dsref, []string{"vm"}, &ds); err != nil {
		return DatastoreReferenceListing{}, fmt.Errorf("read datastore VM references: %w", err)
	}
	out := DatastoreReferenceListing{TotalVMs: len(ds.Vm)}
	if len(ds.Vm) == 0 {
		return out, nil
	}
	var raw []mo.VirtualMachine
	if err := property.DefaultCollector(c.VIM()).Retrieve(ctx, ds.Vm, []string{"name", "config.template", "config.hardware.device"}, &raw); err != nil {
		out.Problems = append(out.Problems, err.Error())
		return out, nil
	}
	out.CheckedVMs = len(raw)
	if out.CheckedVMs < out.TotalVMs {
		out.Problems = append(out.Problems, fmt.Sprintf("%d datastore VM configuration(s) were unavailable", out.TotalVMs-out.CheckedVMs))
	}
	for i := range raw {
		vm := &raw[i]
		if vm.Config == nil {
			out.Problems = append(out.Problems, fmt.Sprintf("VM %s configuration was unavailable", vm.Name))
			continue
		}
		for _, device := range vm.Config.Hardware.Device {
			disk, ok := device.(*types.VirtualDisk)
			if !ok || disk == nil {
				continue
			}
			backing := normalizeBacking(disk.Backing)
			if strings.TrimSpace(backing.path) == "" {
				continue
			}
			label := ""
			if disk.DeviceInfo != nil {
				label = disk.DeviceInfo.GetDescription().Label
			}
			out.References = append(out.References, DatastoreVMReference{
				Context: c.Context.Name,
				VMID:    vm.Self.Value, VMName: vm.Name, Template: vm.Config.Template,
				DiskLabel: label, BackingPath: backing.path,
			})
		}
	}
	return out, nil
}

// FindInDatastore searches a whole datastore for names matching pattern.
//
// This is the one recursive operation the browser offers, and it is never
// implicit: it runs because an operator asked for it by name. It is bounded by
// datastoreFindTimeout and datastoreFindResultCap, and a result that hit the
// cap comes back with Truncated set so the caller can say so rather than
// presenting a partial answer as a complete one.
func (c *Client) FindInDatastore(parent context.Context, datastoreID, datastore, pattern string) (DatastoreListing, error) {
	ctx, cancel := context.WithTimeout(parent, datastoreFindTimeout)
	defer cancel()

	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return DatastoreListing{}, errors.New("search pattern is empty")
	}
	browser, err := c.datastoreBrowserRef(ctx, datastoreID)
	if err != nil {
		return DatastoreListing{}, err
	}
	result, err := c.searchDatastoreSubFolders(ctx, browser, datastoreDirPath(datastore, ""), browseSearchSpec(pattern))
	if err != nil {
		return DatastoreListing{}, err
	}
	return datastoreListing(datastore, result, datastoreFindResultCap), nil
}

// browseSearchSpec is the spec both interactive operations use. Unlike the
// assessment sweep it asks for every kind of file rather than only VMDKs, and
// asks for folders as well, because an operator looking for a path needs to
// see the directories on the way to it.
func browseSearchSpec(pattern string) *types.HostDatastoreBrowserSearchSpec {
	return &types.HostDatastoreBrowserSearchSpec{
		MatchPattern: []string{pattern},
		Query:        []types.BaseFileQuery{&types.FolderFileQuery{}, &types.FileQuery{}},
		Details: &types.FileQueryFlags{
			FileSize:     true,
			Modification: true,
			FileType:     true,
		},
	}
}

// datastoreBrowserRef resolves one datastore's browser.
//
// The reference is fetched on demand rather than carried on the Datastore
// type: that type is persisted into assessment captures and exports, and a
// moref that only an interactive session can use has no business in a record
// meant to be read back months later. Datastore.ID holds the moref value
// alone, so the type is supplied here.
func (c *Client) datastoreBrowserRef(ctx context.Context, datastoreID string) (types.ManagedObjectReference, error) {
	datastoreID = strings.TrimSpace(datastoreID)
	if datastoreID == "" {
		return types.ManagedObjectReference{}, errors.New("datastore reference is unavailable")
	}
	ref := types.ManagedObjectReference{Type: "Datastore", Value: datastoreID}
	var props mo.Datastore
	if err := property.DefaultCollector(c.VIM()).RetrieveOne(ctx, ref, []string{"browser"}, &props); err != nil {
		return types.ManagedObjectReference{}, fmt.Errorf("read datastore browser: %w", err)
	}
	if props.Browser.Type == "" || props.Browser.Value == "" {
		return types.ManagedObjectReference{}, errors.New("datastore browser reference is unavailable")
	}
	return props.Browser, nil
}

func taskInfoState(value any) types.TaskInfoState {
	switch value := value.(type) {
	case types.TaskInfoState:
		return value
	case *types.TaskInfoState:
		if value != nil {
			return *value
		}
	}
	return ""
}

// browserSearchResults unwraps the several shapes a browser task result
// arrives in, depending on server version and how the property update was
// coalesced.
func browserSearchResults(result types.AnyType) []types.HostDatastoreBrowserSearchResults {
	switch value := result.(type) {
	case types.ArrayOfHostDatastoreBrowserSearchResults:
		return value.HostDatastoreBrowserSearchResults
	case *types.ArrayOfHostDatastoreBrowserSearchResults:
		if value != nil {
			return value.HostDatastoreBrowserSearchResults
		}
	case types.HostDatastoreBrowserSearchResults:
		return []types.HostDatastoreBrowserSearchResults{value}
	case *types.HostDatastoreBrowserSearchResults:
		if value != nil {
			return []types.HostDatastoreBrowserSearchResults{*value}
		}
	}
	return nil
}

func datastoreFiles(datastore string, result types.AnyType) ([]DatastoreFile, bool) {
	results := browserSearchResults(result)
	files := make([]DatastoreFile, 0, min(datastoreBrowseFileCap, len(results)))
	for _, search := range results {
		for _, file := range search.File {
			if len(files) >= datastoreBrowseFileCap {
				return files, true
			}
			info := file.GetFileInfo()
			if info == nil {
				continue
			}
			var modified time.Time
			if info.Modification != nil {
				modified = info.Modification.UTC()
			}
			files = append(files, DatastoreFile{
				Path:      datastoreFilePath(datastore, search.FolderPath, info.Path),
				SizeBytes: info.FileSize,
				Modified:  modified,
			})
		}
	}
	return files, false
}

// datastoreListing maps a browser result into entries, capped at limit and
// ordered folders first so a directory reads the way an operator navigates it.
func datastoreListing(datastore string, result types.AnyType, limit int) DatastoreListing {
	var out DatastoreListing
	for _, search := range browserSearchResults(result) {
		for _, file := range search.File {
			if len(out.Entries) >= limit {
				out.Truncated = true
				sortDatastoreEntries(out.Entries)
				return out
			}
			entry, ok := datastoreEntry(datastore, search.FolderPath, file)
			if !ok {
				continue
			}
			out.Entries = append(out.Entries, entry)
		}
	}
	sortDatastoreEntries(out.Entries)
	return out
}

func sortDatastoreEntries(entries []DatastoreEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if (entries[i].Type == DatastoreEntryFolder) != (entries[j].Type == DatastoreEntryFolder) {
			return entries[i].Type == DatastoreEntryFolder
		}
		if !strings.EqualFold(entries[i].Path, entries[j].Path) {
			return strings.ToLower(entries[i].Path) < strings.ToLower(entries[j].Path)
		}
		return entries[i].Path < entries[j].Path
	})
}

func datastoreEntry(datastore, folder string, file types.BaseFileInfo) (DatastoreEntry, bool) {
	info := file.GetFileInfo()
	if info == nil {
		return DatastoreEntry{}, false
	}
	name := strings.Trim(strings.ReplaceAll(strings.TrimSpace(info.Path), "\\", "/"), "/")
	if name == "" || name == "." || name == ".." {
		return DatastoreEntry{}, false
	}
	kind := DatastoreEntryFile
	if _, ok := file.(*types.FolderFileInfo); ok {
		kind = DatastoreEntryFolder
	}
	var modified time.Time
	if info.Modification != nil {
		modified = info.Modification.UTC()
	}
	return DatastoreEntry{
		Name:      name,
		Path:      datastoreFilePath(datastore, folder, info.Path),
		Type:      kind,
		SizeBytes: info.FileSize,
		Modified:  modified,
	}, true
}

func datastoreFilePath(datastore, folder, file string) string {
	display := strings.TrimSpace(datastore)
	folder = strings.TrimSpace(strings.ReplaceAll(folder, "\\", "/"))
	if strings.HasPrefix(folder, "[") {
		if close := strings.IndexByte(folder, ']'); close >= 0 {
			folder = folder[close+1:]
		}
	}
	folder = strings.Trim(folder, " /")
	file = strings.Trim(strings.ReplaceAll(strings.TrimSpace(file), "\\", "/"), " /")
	if strings.HasPrefix(file, "[") {
		return file
	}
	relative := file
	if folder != "" && file != "" {
		relative = folder + "/" + file
	} else if folder != "" {
		relative = folder
	}
	if relative == "" {
		return fmt.Sprintf("[%s]", display)
	}
	return fmt.Sprintf("[%s] %s", display, relative)
}

// datastoreDirPath composes the "[datastore] relative" address the browser
// takes. It keeps the case of the relative path, unlike NormalizeRelativePath,
// because this address is sent to a server that may be case-sensitive rather
// than used as a join key.
func datastoreDirPath(datastore, relative string) string {
	display := strings.TrimSpace(datastore)
	relative = trimBrowsePath(relative)
	if relative == "" {
		return fmt.Sprintf("[%s]", display)
	}
	return fmt.Sprintf("[%s] %s", display, relative)
}

// SplitBrowsePath splits a canonical "[datastore] relative" path, keeping the
// case of both halves.
//
// It is the browsing counterpart to SplitDatastorePath, which lowercases
// everything because it builds join keys — exactly the wrong thing for a path
// that is about to be shown to an operator or sent back to a server that may
// be case-sensitive.
func SplitBrowsePath(value string) (datastore, relative string, ok bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if !strings.HasPrefix(value, "[") {
		return "", "", false
	}
	close := strings.IndexByte(value, ']')
	if close <= 1 {
		return "", "", false
	}
	return strings.TrimSpace(value[1:close]), trimBrowsePath(value[close+1:]), true
}

// ParentBrowsePath is the directory containing relative, or "" at the root.
func ParentBrowsePath(relative string) string {
	relative = trimBrowsePath(relative)
	if i := strings.LastIndexByte(relative, '/'); i >= 0 {
		return relative[:i]
	}
	return ""
}

// ChildBrowsePath descends one level.
func ChildBrowsePath(relative, name string) string {
	relative, name = trimBrowsePath(relative), trimBrowsePath(name)
	switch {
	case name == "":
		return relative
	case relative == "":
		return name
	default:
		return relative + "/" + name
	}
}

// normalizeBrowsePath cleans a path and refuses to walk out of the datastore.
// A browser that can be talked into "../.." is a browser that reads somewhere
// its operator did not ask about, which is not what read-only means.
func normalizeBrowsePath(relative string) (string, error) {
	relative = trimBrowsePath(relative)
	for _, segment := range strings.Split(relative, "/") {
		if segment == ".." {
			return "", fmt.Errorf("invalid datastore path %q", relative)
		}
	}
	return relative, nil
}

func trimBrowsePath(value string) string {
	value = strings.Trim(strings.TrimSpace(strings.ReplaceAll(value, "\\", "/")), "/")
	for strings.Contains(value, "//") {
		value = strings.ReplaceAll(value, "//", "/")
	}
	return value
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
