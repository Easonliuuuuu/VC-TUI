package vsphere

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

func browseSimulator(t *testing.T) (*Client, *simulator.Model) {
	t.Helper()
	model := simulator.VPX()
	model.Datacenter = 1
	model.Cluster = 1
	model.ClusterHost = 1
	model.App = 1
	model.Machine = 2
	model.Datastore = 1
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
	return NewClientForTest(cc, gc), model
}

func seedSimulatorVMDK(t *testing.T, c *Client) {
	t.Helper()
	finder := find.NewFinder(c.VIM(), false)
	datacenter, err := finder.DefaultDatacenter(context.Background())
	if err != nil {
		t.Fatalf("find simulator datacenter: %v", err)
	}
	finder.SetDatacenter(datacenter)
	datastore, err := finder.DefaultDatastore(context.Background())
	if err != nil {
		t.Fatalf("find simulator datastore: %v", err)
	}
	var props mo.Datastore
	if err := datastore.Properties(context.Background(), datastore.Reference(), []string{"summary"}, &props); err != nil {
		t.Fatalf("read simulator datastore path: %v", err)
	}
	dir := filepath.Join(props.Summary.Url, "fixture")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create simulator datastore fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orphan.vmdk"), []byte("synthetic vmdk"), 0o600); err != nil {
		t.Fatalf("write simulator datastore fixture: %v", err)
	}
}

func TestBrowseDatastoresAgainstSimulator(t *testing.T) {
	c, _ := browseSimulator(t)
	seedSimulatorVMDK(t, c)
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inv := c.FetchGroupWith(context.Background(), idx, GroupDatastores, FetchOptions{PageSize: -1, BrowseDatastoreFiles: true})
	if len(inv.Errors) != 0 {
		t.Fatalf("browse errors=%v", inv.Errors)
	}
	if len(inv.Datastores) == 0 {
		t.Fatal("simulator returned no datastores")
	}
	for _, datastore := range inv.Datastores {
		if datastore.Backing.URL == "" {
			t.Fatalf("datastore %q has no backing URL", datastore.Name)
		}
		if datastore.BrowseStatus != "success" {
			t.Fatalf("datastore browse status=%q error=%q", datastore.BrowseStatus, datastore.BrowseError)
		}
		if len(datastore.Files) == 0 {
			t.Fatalf("datastore %q browse returned no files", datastore.Name)
		}
	}
}

func TestBrowseDatastorePermissionFailureIsProvenance(t *testing.T) {
	c, model := browseSimulator(t)
	model.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{
		MethodName:  "SearchDatastoreSubFolders_Task",
		ObjectType:  "*",
		ObjectName:  "*",
		Probability: 1,
		FaultType:   simulator.FaultTypeNoPermission,
		Message:     "Datastore.Browse denied",
		Enabled:     true,
	})
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inv := c.FetchGroupWith(context.Background(), idx, GroupDatastores, FetchOptions{PageSize: -1, BrowseDatastoreFiles: true})
	if len(inv.Datastores) == 0 {
		t.Fatal("simulator returned no datastores")
	}
	for _, datastore := range inv.Datastores {
		if datastore.BrowseStatus != "denied" || datastore.BrowseError == "" || len(datastore.Files) != 0 {
			t.Fatalf("denied browse lost provenance: %+v", datastore)
		}
	}
}

func TestBrowseDatastoreOtherFailureIsNotDenied(t *testing.T) {
	c, model := browseSimulator(t)
	model.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{
		MethodName:  "SearchDatastoreSubFolders_Task",
		ObjectType:  "*",
		ObjectName:  "*",
		Probability: 1,
		FaultType:   simulator.FaultTypeInvalidState,
		Message:     "host unreachable",
		Enabled:     true,
	})
	idx, err := c.NewIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inv := c.FetchGroupWith(context.Background(), idx, GroupDatastores, FetchOptions{PageSize: -1, BrowseDatastoreFiles: true})
	if len(inv.Datastores) == 0 {
		t.Fatal("simulator returned no datastores")
	}
	for _, datastore := range inv.Datastores {
		if datastore.BrowseStatus != "failed" || datastore.BrowseError == "" {
			t.Fatalf("non-permission failure must stay failed: %+v", datastore)
		}
	}
}

func TestDatastoreFilesCapIsHonored(t *testing.T) {
	files := make([]types.BaseFileInfo, 0, datastoreBrowseFileCap+1)
	for i := 0; i < datastoreBrowseFileCap+1; i++ {
		files = append(files, &types.VmDiskFileInfo{FileInfo: types.FileInfo{Path: "disk.vmdk"}})
	}
	got, truncated := datastoreFiles("ds", types.ArrayOfHostDatastoreBrowserSearchResults{HostDatastoreBrowserSearchResults: []types.HostDatastoreBrowserSearchResults{{FolderPath: "[ds]", File: files}}})
	if len(got) != datastoreBrowseFileCap {
		t.Fatalf("datastore file count=%d, want cap %d", len(got), datastoreBrowseFileCap)
	}
	if !truncated {
		t.Fatal("datastore file cap was reached without truncation provenance")
	}
}

// Real ESXi returns fileSize 0 on VmDiskFileInfo results; the plain listing
// carries the extent sizes. The descriptor row must absorb its extents.
func TestFoldDiskExtentSizesSumsDescriptorAndExtents(t *testing.T) {
	const mib = 1 << 20
	files := []DatastoreFile{
		{Path: "[nfs] orphans/orphan-disk-flat.vmdk", SizeBytes: 16 * mib},
		{Path: "[nfs] orphans/orphan-disk.vmdk", SizeBytes: 471},
		{Path: "[nfs] vm/vm-000001.vmdk", SizeBytes: 300},
		{Path: "[nfs] vm/vm-000001-delta.vmdk", SizeBytes: 4 * mib},
		{Path: "[nfs] vm/vm.vmdk", SizeBytes: 400},
		{Path: "[nfs] vm/vm-flat.vmdk", SizeBytes: 8 * mib},
		{Path: "[nfs] vm/vm-rdmp.vmdk", SizeBytes: 1 << 40},
		{Path: "[nfs] other/other.vmdk", SizeBytes: 0},
		{Path: "[nfs] hosted/hosted.vmdk", SizeBytes: 500},
		{Path: "[nfs] hosted/hosted-s001.vmdk", SizeBytes: 2 * mib},
		{Path: "[nfs] hosted/hosted-s002.vmdk", SizeBytes: 1 * mib},
	}
	got := foldDiskExtentSizes(files)
	want := map[string]int64{
		"[nfs] orphans/orphan-disk.vmdk": 16*mib + 471,
		"[nfs] vm/vm-000001.vmdk":        4*mib + 300,
		"[nfs] vm/vm.vmdk":               8*mib + 400,
		"[nfs] other/other.vmdk":         0,
		"[nfs] hosted/hosted.vmdk":       3*mib + 500,
	}
	if len(got) != len(want) {
		t.Fatalf("rows=%+v, want %d descriptor rows", got, len(want))
	}
	for _, f := range got {
		if size, ok := want[f.Path]; !ok || f.SizeBytes != size {
			t.Fatalf("row %s size=%d, want %d (present=%v)", f.Path, f.SizeBytes, size, ok)
		}
	}
}

func TestDatastoreFilesFromRealShapedResultSizeFromExtent(t *testing.T) {
	result := types.ArrayOfHostDatastoreBrowserSearchResults{HostDatastoreBrowserSearchResults: []types.HostDatastoreBrowserSearchResults{{
		FolderPath: "[nfs-shared] orphans/",
		File: []types.BaseFileInfo{
			&types.FileInfo{Path: "orphan-disk-flat.vmdk", FileSize: 16 << 20},
			&types.FileInfo{Path: "orphan-disk.vmdk", FileSize: 471},
		},
	}}}
	files, _ := datastoreFiles("nfs-shared", result)
	files = foldDiskExtentSizes(files)
	if len(files) != 1 || files[0].SizeBytes != 16<<20+471 {
		t.Fatalf("files=%+v", files)
	}
}
