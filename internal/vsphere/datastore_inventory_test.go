package vsphere

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

// inventorySimulator starts a simulator with two datastores and seeds each with
// synthetic files. It returns the client and the seeded root of every
// datastore, keyed by datastore name.
func inventorySimulator(t *testing.T) (*Client, *simulator.Model, map[string]string) {
	t.Helper()
	model := simulator.VPX()
	model.Datacenter = 1
	model.Cluster = 1
	model.ClusterHost = 1
	model.Machine = 1
	model.Datastore = 2
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)
	gc, err := govmomi.NewClient(context.Background(), server.URL, true)
	if err != nil {
		t.Fatalf("connect to simulator: %v", err)
	}
	t.Cleanup(func() { _ = gc.Logout(context.Background()) })
	cc := &config.Context{Name: "sim", Endpoint: server.URL.String(), Username: "user", TLS: config.TLSConfig{Mode: config.TLSInsecure}}
	cc.Normalize()
	c := NewClientForTest(cc, gc)

	finder := find.NewFinder(c.VIM(), false)
	dc, err := finder.DefaultDatacenter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	finder.SetDatacenter(dc)
	stores, err := finder.DatastoreList(context.Background(), "*")
	if err != nil {
		t.Fatal(err)
	}
	roots := make(map[string]string)
	for _, ds := range stores {
		var props mo.Datastore
		if err := ds.Properties(context.Background(), ds.Reference(), []string{"summary"}, &props); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(props.Summary.Url, "synthetic-vm")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, size := range map[string]int{"synthetic-vm.vmx": 10, "synthetic-vm.vmdk": 20, "vmware.log": 5} {
			if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(props.Summary.Url, "notes.txt"), make([]byte, 7), 0o600); err != nil {
			t.Fatal(err)
		}
		roots[props.Summary.Name] = props.Summary.Url
	}
	return c, model, roots
}

func fetchDatastores(t *testing.T, c *Client, opts FetchOptions) *Inventory {
	t.Helper()
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opts.PageSize = -1
	inv := c.FetchGroupWith(context.Background(), idx, GroupDatastores, opts)
	if len(inv.Errors) != 0 {
		t.Fatalf("datastore fetch errors: %v", inv.Errors)
	}
	if len(inv.Datastores) != 2 {
		t.Fatalf("datastores = %d, want 2", len(inv.Datastores))
	}
	return inv
}

func TestFileInventoryIsOffByDefaultAndNotImpliedByBrowse(t *testing.T) {
	c, model, _ := inventorySimulator(t)
	// If anything issued a recursive search it would be refused here.
	model.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{MethodName: "SearchDatastoreSubFolders_Task", ObjectType: "*", ObjectName: "*", Probability: 1, FaultType: simulator.FaultTypeNoPermission, Message: "must not be called", Enabled: true})
	for name, opts := range map[string]FetchOptions{"default": {}, "browse only": {}} {
		inv := fetchDatastores(t, c, opts)
		for _, ds := range inv.Datastores {
			if ds.FileInventory != nil || len(ds.Files) != 0 || ds.BrowseStatus != "" {
				t.Fatalf("%s: datastore %s browsed without opt-in: %+v", name, ds.Name, ds)
			}
		}
	}
	// --browse-datastores (VMDK evidence) must not switch the inventory on.
	c2, _, _ := inventorySimulator(t)
	inv := fetchDatastores(t, c2, FetchOptions{BrowseDatastoreFiles: true})
	for _, ds := range inv.Datastores {
		if ds.FileInventory != nil {
			t.Fatalf("VMDK browse enabled the file inventory on %s", ds.Name)
		}
		if ds.BrowseStatus != "success" {
			t.Fatalf("VMDK browse status = %q", ds.BrowseStatus)
		}
	}
}

func TestFileInventoryListsEveryFileWithTypeAndByteSize(t *testing.T) {
	c, _, roots := inventorySimulator(t)
	inv := fetchDatastores(t, c, FetchOptions{FileInventory: &FileInventoryOptions{}})
	for _, ds := range inv.Datastores {
		fi := ds.FileInventory
		if fi == nil || !fi.Complete() {
			t.Fatalf("%s inventory = %+v, want complete", ds.Name, fi)
		}
		if _, ok := roots[ds.Name]; !ok {
			t.Fatalf("unexpected datastore %q", ds.Name)
		}
		got := map[string]DatastoreInventoryFile{}
		for _, f := range fi.Files {
			got[f.Path] = f
		}
		for path, want := range map[string]struct {
			typ  string
			size int64
		}{
			"[" + ds.Name + "] synthetic-vm/synthetic-vm.vmx":  {"VmConfigFileInfo", 10},
			"[" + ds.Name + "] synthetic-vm/synthetic-vm.vmdk": {"VmDiskFileInfo", 20},
			"[" + ds.Name + "] synthetic-vm/vmware.log":        {"VmLogFileInfo", 5},
			"[" + ds.Name + "] notes.txt":                      {"FileInfo", 7},
		} {
			f, ok := got[path]
			if !ok {
				t.Fatalf("%s: %q missing from inventory %v", ds.Name, path, fi.Files)
			}
			if f.Type != want.typ || f.SizeBytes != want.size {
				t.Errorf("%s: %q = type %q size %d, want %q %d", ds.Name, path, f.Type, f.SizeBytes, want.typ, want.size)
			}
		}
		for _, f := range fi.Files {
			if f.Type == "FolderFileInfo" {
				t.Errorf("%s: folder %q listed as an inventory row", ds.Name, f.Path)
			}
		}
		for i := 1; i < len(fi.Files); i++ {
			if strings.ToLower(fi.Files[i-1].Path) > strings.ToLower(fi.Files[i].Path) {
				t.Errorf("%s: files not ordered by path: %q before %q", ds.Name, fi.Files[i-1].Path, fi.Files[i].Path)
			}
		}
	}
}

func TestFileInventoryDeniedBrowseIsRecordedNotEmpty(t *testing.T) {
	c, model, _ := inventorySimulator(t)
	model.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{MethodName: "SearchDatastoreSubFolders_Task", ObjectType: "*", ObjectName: "*", Probability: 1, FaultType: simulator.FaultTypeNoPermission, Message: "Datastore.Browse denied", Enabled: true})
	for _, ds := range fetchDatastores(t, c, FetchOptions{FileInventory: &FileInventoryOptions{}}).Datastores {
		fi := ds.FileInventory
		if fi == nil || fi.Status != FileInventoryDenied || fi.Error == "" || len(fi.Files) != 0 || fi.Complete() {
			t.Fatalf("%s denied browse = %+v", ds.Name, fi)
		}
	}
}

func TestFileInventoryTruncationIsNeverComplete(t *testing.T) {
	c, _, _ := inventorySimulator(t)
	inv := fetchDatastores(t, c, FetchOptions{FileInventory: &FileInventoryOptions{MaxFilesPerDatastore: 2}})
	for _, ds := range inv.Datastores {
		fi := ds.FileInventory
		if fi == nil || fi.Status != FileInventorySuccess || !fi.Truncated || len(fi.Files) != 2 || fi.Complete() || fi.Limit != 2 || fi.TruncatedReason == "" {
			t.Fatalf("%s truncated inventory = %+v", ds.Name, fi)
		}
	}
}

func TestFileInventoryContextBudgetSkipsLaterDatastoresDeterministically(t *testing.T) {
	c, _, _ := inventorySimulator(t)
	inv := fetchDatastores(t, c, FetchOptions{FileInventory: &FileInventoryOptions{MaxFilesPerContext: 3}})
	first, second := inv.Datastores[0], inv.Datastores[1] // ordered by name
	if first.Name > second.Name {
		first, second = second, first
	}
	if fi := first.FileInventory; fi == nil || !fi.Truncated || len(fi.Files) != 3 || fi.TruncatedReason != fileInventoryTruncatedByContextBudget {
		t.Fatalf("first datastore = %+v, want truncated by the context budget at 3 files", first.FileInventory)
	}
	if fi := second.FileInventory; fi == nil || fi.Status != FileInventorySkipped || len(fi.Files) != 0 || fi.Error == "" {
		t.Fatalf("second datastore = %+v, want skipped", second.FileInventory)
	}
}

func TestFileInventoryTimeoutIsAFailureNotAnEmptyListing(t *testing.T) {
	c, _, _ := inventorySimulator(t)
	inv := fetchDatastores(t, c, FetchOptions{FileInventory: &FileInventoryOptions{DatastoreTimeout: 1}})
	for _, ds := range inv.Datastores {
		fi := ds.FileInventory
		if fi == nil || fi.Status != FileInventoryFailed || fi.Complete() || len(fi.Files) != 0 || !strings.Contains(fi.Error, "timed out") {
			t.Fatalf("%s timed-out inventory = %+v", ds.Name, fi)
		}
	}
}

func TestFileInventoryUnavailableDatastoresAreRecorded(t *testing.T) {
	var c Client
	datastores := []Datastore{
		{ID: "ds-1", Name: "offline", Accessible: false},
		{ID: "ds-2", Name: "no-browser", Accessible: true},
	}
	c.inventoryDatastores(context.Background(), datastores, map[string]types.ManagedObjectReference{}, FileInventoryOptions{})
	for _, ds := range datastores {
		if fi := ds.FileInventory; fi == nil || fi.Status != FileInventoryUnavailable || fi.Error == "" || fi.Complete() {
			t.Fatalf("%s = %+v, want unavailable", ds.Name, ds.FileInventory)
		}
	}
}

func TestFileInventoryOptionsDefaultsAndClamps(t *testing.T) {
	d := FileInventoryOptions{}.WithDefaults()
	if d.MaxFilesPerDatastore != DefaultFileInventoryMaxFiles || d.MaxFilesPerContext != DefaultFileInventoryMaxTotalFiles || d.DatastoreTimeout != DefaultFileInventoryDatastoreTimeout || d.ContextTimeout != DefaultFileInventoryContextTimeout {
		t.Fatalf("defaults = %+v", d)
	}
	c := FileInventoryOptions{MaxFilesPerDatastore: 1 << 40, MaxFilesPerContext: 1 << 40}.WithDefaults()
	if c.MaxFilesPerDatastore != MaxFileInventoryMaxFiles || c.MaxFilesPerContext != MaxFileInventoryMaxTotalFiles {
		t.Fatalf("clamps = %+v", c)
	}
}

func TestDatastoreInventoryFilesSkipsFoldersSortsAndCaps(t *testing.T) {
	info := func(path string, size int64) types.FileInfo { return types.FileInfo{Path: path, FileSize: size} }
	result := types.ArrayOfHostDatastoreBrowserSearchResults{HostDatastoreBrowserSearchResults: []types.HostDatastoreBrowserSearchResults{
		{FolderPath: "[ds] vm", File: []types.BaseFileInfo{
			&types.VmDiskFileInfo{FileInfo: info("b.vmdk", 5<<30)},
			&types.FolderFileInfo{FileInfo: info("snapshots", 0)},
			&types.VmConfigFileInfo{FileInfo: info("A.vmx", 9)},
		}},
		{FolderPath: "[ds]", File: []types.BaseFileInfo{&types.FileInfo{Path: "z.txt", FileSize: 1}}},
	}}
	files, truncated := datastoreInventoryFiles("ds", result, 10)
	if truncated || len(files) != 3 {
		t.Fatalf("files=%+v truncated=%v", files, truncated)
	}
	want := []DatastoreInventoryFile{
		{Path: "[ds] vm/A.vmx", Type: "VmConfigFileInfo", SizeBytes: 9},
		{Path: "[ds] vm/b.vmdk", Type: "VmDiskFileInfo", SizeBytes: 5 << 30},
		{Path: "[ds] z.txt", Type: "FileInfo", SizeBytes: 1},
	}
	for i := range want {
		if files[i].Path != want[i].Path || files[i].Type != want[i].Type || files[i].SizeBytes != want[i].SizeBytes {
			t.Errorf("file %d = %+v, want %+v", i, files[i], want[i])
		}
	}
	capped, truncated := datastoreInventoryFiles("ds", result, 2)
	if !truncated || len(capped) != 2 || capped[0].Path != want[0].Path {
		t.Fatalf("capped=%+v truncated=%v; want the first two paths and truncation provenance", capped, truncated)
	}
}

// A real vCenter 8.0.3 reports a subfolder in its parent's listing as a plain
// FileInfo (4096 bytes on NFS) rather than a FolderFileInfo. The folder is also
// a search result of its own, which is what identifies the entry as a folder.
func TestDatastoreInventoryFilesDropsSubfoldersReportedAsPlainFiles(t *testing.T) {
	result := types.ArrayOfHostDatastoreBrowserSearchResults{HostDatastoreBrowserSearchResults: []types.HostDatastoreBrowserSearchResults{
		{FolderPath: "[ds]", File: []types.BaseFileInfo{
			&types.FileInfo{Path: "nested", FileSize: 4096},
			&types.FileInfo{Path: "depot.zip", FileSize: 640882207},
		}},
		{FolderPath: "[ds] nested/", File: []types.BaseFileInfo{&types.FileInfo{Path: "target-folder", FileSize: 4096}}},
		{FolderPath: "[ds] nested/target-folder/", File: []types.BaseFileInfo{&types.VmConfigFileInfo{FileInfo: types.FileInfo{Path: "vm.vmx", FileSize: 9}}}},
	}}
	files, truncated := datastoreInventoryFiles("ds", result, 10)
	if truncated || len(files) != 2 || files[0].Path != "[ds] depot.zip" || files[1].Path != "[ds] nested/target-folder/vm.vmx" {
		t.Fatalf("files=%+v truncated=%v; want only the two real files", files, truncated)
	}
}
