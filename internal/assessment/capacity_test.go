package assessment

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type capacityTestContext struct {
	name, vcenter string
	endpoint      string
}

func newCapacityTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func saveCapacityRun(t *testing.T, store *Store, when time.Time, contexts []capacityTestContext, datastores map[string][]vsphere.Datastore, vms map[string][]vsphere.VM, failedVM map[string]bool) Run {
	t.Helper()
	cfg := make([]*config.Context, 0, len(contexts))
	for _, item := range contexts {
		cfg = append(cfg, &config.Context{Name: item.name, Endpoint: item.endpoint})
	}
	run, err := store.StartRunWithMetadata(context.Background(), "test", cfg, when, RunMetadata{InventorySchemaVersion: CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range contexts {
		collections := make([]CollectionResult, 0, len(persistedKinds))
		for _, kind := range persistedKinds {
			status := "empty"
			if kind == "vm" {
				status = "success"
				if failedVM[item.name] {
					status = "failed"
				}
			}
			if kind == "datastore" {
				status = "success"
			}
			collection := CollectionResult{Kind: kind, Status: status}
			if kind == "datastore" {
				for _, ds := range datastores[item.name] {
					payload, marshalErr := json.Marshal(ds)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					collection.ItemCount++
					collection.Resources = append(collection.Resources, ResourceObservation{Context: item.name, VCenterID: item.vcenter, Kind: "datastore", ID: ds.ID, Name: ds.Name, Payload: payload})
				}
			}
			collections = append(collections, collection)
		}
		observations := make([]Observation, 0, len(vms[item.name]))
		for _, vm := range vms[item.name] {
			observations = append(observations, Observation{Context: item.name, VCenterID: item.vcenter, VM: vm})
		}
		result := ContextResult{Name: item.name, VCenterID: item.vcenter, Status: "success", VMs: observations, Collections: collections}
		if err := store.SaveContext(context.Background(), run.ID, result, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinishRun(context.Background(), run.ID, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return run
}

func capacityDatastore(name, id, vcenter string, capacity, free int64) vsphere.Datastore {
	return vsphere.Datastore{Location: vsphere.Location{Context: name, Datacenter: "dc-a"}, ID: id, Name: name, CapacityBytes: capacity, FreeBytes: free, Backing: vsphere.DatastoreBacking{Extents: []string{"naa-shared"}}}
}

func TestCapacityReportUsesUsedGrowthAndAttributionPrecedence(t *testing.T) {
	store := newCapacityTestStore(t)
	contexts := []capacityTestContext{{name: "prod", vcenter: "vc-1", endpoint: "https://prod"}}
	vm := func(storage float64, fileSize int64) vsphere.VM {
		return vsphere.VM{Location: vsphere.Location{Context: "prod"}, ID: "vm-1", Name: "app", StorageGB: storage, Disks: []vsphere.VMDisk{{BackingPath: "[prod] app/disk.vmdk", CapacityBytes: fileSize}}}
	}
	for i, item := range []struct {
		capacity, free, file int64
	}{
		{1000, 500, 400},
		{2000, 1500, 400},
		{2000, 1400, 500},
	} {
		ds := capacityDatastore("prod", "ds-1", "vc-1", item.capacity, item.free)
		ds.BrowseStatus = "success"
		ds.Files = []vsphere.DatastoreFile{{Path: "[ds-prod] app/disk.vmdk", SizeBytes: item.file}}
		saveCapacityRun(t, store, time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC), contexts, map[string][]vsphere.Datastore{"prod": {ds}}, map[string][]vsphere.VM{"prod": {vm(float64(i), item.file)}}, nil)
	}
	report, err := store.CapacityReport(context.Background(), TrendOptions{Limit: 0}, CapacityThresholds{FreePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Datastores) != 1 {
		t.Fatalf("datastores=%d, want one", len(report.Datastores))
	}
	ds := report.Datastores[0]
	if ds.UsedGrowthBytes == nil || *ds.UsedGrowthBytes != 100 {
		t.Fatalf("used growth=%v, want 100", ds.UsedGrowthBytes)
	}
	if ds.CapacityChangedBytes != 1000 {
		t.Fatalf("capacity change=%v, want 1000", ds.CapacityChangedBytes)
	}
	var app GrowthContributor
	for _, contributor := range ds.Contributors {
		if contributor.Name == "app" {
			app = contributor
		}
	}
	if app.Basis != BasisExact || app.DeltaBytes != 100 {
		t.Fatalf("app contributor=%+v, want exact +100", app)
	}
	if ds.Confidence != CapacityAttributed {
		t.Fatalf("confidence=%q, want attributed", ds.Confidence)
	}
}

func TestCapacityReportProjectionAndFlatSeries(t *testing.T) {
	store := newCapacityTestStore(t)
	contexts := []capacityTestContext{{name: "prod", vcenter: "vc-1", endpoint: "https://prod"}}
	for i := 0; i < 8; i++ {
		ds := capacityDatastore("prod", "ds-1", "vc-1", 2000, int64(1000-100*i))
		saveCapacityRun(t, store, time.Date(2026, 2, 1+i, 0, 0, 0, 0, time.UTC), contexts, map[string][]vsphere.Datastore{"prod": {ds}}, nil, nil)
	}
	report, err := store.CapacityReport(context.Background(), TrendOptions{}, CapacityThresholds{FreePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	projection := report.Datastores[0].Projection
	if projection == nil || projection.Confidence != ProjectionProjected || projection.DaysRemaining == nil || math.Abs(*projection.DaysRemaining-1) > 0.01 {
		t.Fatalf("projection=%+v, want one projected day", projection)
	}
	for i := 0; i < 3; i++ {
		ds := capacityDatastore("prod", "ds-flat", "vc-1", 2000, 1000)
		saveCapacityRun(t, store, time.Date(2026, 3, 1+i, 0, 0, 0, 0, time.UTC), contexts, map[string][]vsphere.Datastore{"prod": {ds}}, nil, nil)
	}
	report, err = store.CapacityReport(context.Background(), TrendOptions{FromID: 9}, CapacityThresholds{FreePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, datastore := range report.Datastores {
		if datastore.Object.ID == "ds-flat" && (datastore.Projection == nil || datastore.Projection.Confidence != ProjectionUnknown || !strings.Contains(strings.Join(datastore.Projection.Reasons, ";"), "free space is not shrinking")) {
			t.Fatalf("flat projection=%+v", datastore.Projection)
		}
	}
}

func TestCapacityReportSharedIdentityAndLocalIsolation(t *testing.T) {
	store := newCapacityTestStore(t)
	contexts := []capacityTestContext{{name: "prod", vcenter: "vc-1", endpoint: "https://prod"}, {name: "dr", vcenter: "vc-2", endpoint: "https://dr"}}
	for i := 0; i < 2; i++ {
		sharedProd := capacityDatastore("shared-prod", "ds-prod", "vc-1", 1000, int64(500-i*10))
		sharedProd.Backing = vsphere.DatastoreBacking{Extents: []string{"naa-shared"}}
		sharedDR := capacityDatastore("shared-dr", "ds-dr", "vc-2", 1000, int64(500-i*10))
		sharedDR.Backing = vsphere.DatastoreBacking{Extents: []string{"naa-shared"}}
		local := capacityDatastore("local", "ds-local", "vc-2", 1000, int64(500-i*10))
		local.Backing = vsphere.DatastoreBacking{Local: true}
		saveCapacityRun(t, store, time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC), contexts, map[string][]vsphere.Datastore{"prod": {sharedProd}, "dr": {sharedDR, local}}, nil, nil)
	}
	report, err := store.CapacityReport(context.Background(), TrendOptions{IncludePartial: true}, CapacityThresholds{FreePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	shared, localCount := 0, 0
	for _, datastore := range report.Datastores {
		if len(datastore.Identity) > 0 {
			shared++
		}
		if datastore.Object.ID == "ds-local" {
			localCount++
		}
	}
	if shared != 1 || localCount != 1 {
		t.Fatalf("shared=%d local=%d report=%+v", shared, localCount, report.Datastores)
	}
}

func TestCapacityReportBlindnessAndRename(t *testing.T) {
	store := newCapacityTestStore(t)
	contexts := []capacityTestContext{{name: "prod", vcenter: "vc-1", endpoint: "https://prod"}}
	first := capacityDatastore("old-name", "ds-1", "vc-1", 1000, 500)
	last := capacityDatastore("new-name", "ds-1", "vc-1", 1000, 400)
	saveCapacityRun(t, store, time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC), contexts, map[string][]vsphere.Datastore{"prod": {first}}, nil, nil)
	saveCapacityRun(t, store, time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), contexts, map[string][]vsphere.Datastore{"prod": {last}}, nil, map[string]bool{"prod": true})
	report, err := store.CapacityReport(context.Background(), TrendOptions{IncludePartial: true}, CapacityThresholds{FreePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Datastores) != 1 || report.Datastores[0].Confidence != CapacityUnknown || len(report.Datastores[0].Blind) == 0 || !strings.Contains(strings.Join(report.Datastores[0].Reasons, ";"), "renamed") {
		t.Fatalf("rename/blind report=%+v", report.Datastores)
	}
}

func TestCapacityReportContextIdentityNeverLeaksNULKey(t *testing.T) {
	store := newCapacityTestStore(t)
	// The same shared datastore is observed under a legitimate vCenter-ID
	// transition: prod/vc-1 for the first two runs, prod/vc-2 for the last two.
	for i := 0; i < 4; i++ {
		vcenter := "vc-1"
		if i >= 2 {
			vcenter = "vc-2"
		}
		ctxs := []capacityTestContext{{name: "prod", vcenter: vcenter, endpoint: "https://prod"}}
		ds := capacityDatastore("shared", "ds-1", vcenter, 2000, int64(1500-100*i))
		saveCapacityRun(t, store, time.Date(2026, 8, 1+i, 0, 0, 0, 0, time.UTC), ctxs, map[string][]vsphere.Datastore{"prod": {ds}}, nil, nil)
	}
	report, err := store.CapacityReport(context.Background(), TrendOptions{}, CapacityThresholds{FreePercent: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Datastores) != 1 {
		t.Fatalf("datastores=%d, want the transition folded into one group: %+v", len(report.Datastores), report.Datastores)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(raw), '\x00') {
		t.Fatalf("report JSON contains a NUL delimiter: %s", raw)
	}
	ds := report.Datastores[0]
	for _, blind := range ds.Blind {
		if strings.ContainsRune(blind.Context, '\x00') {
			t.Fatalf("blindness context leaked internal key: %q", blind.Context)
		}
		if strings.Contains(blind.Reason, "context was not recorded") {
			t.Fatalf("vCenter transition reported as missing context: %+v", blind)
		}
	}
	for _, issue := range report.Coverage {
		if strings.ContainsRune(issue.Context, '\x00') {
			t.Fatalf("coverage context leaked internal key: %q", issue.Context)
		}
	}
	if ds.Confidence == CapacityUnknown {
		t.Fatalf("confidence downgraded despite complete coverage: %+v", ds)
	}
}

func TestCapacityAnomaliesFlagOnlySpikes(t *testing.T) {
	points := make([]capacityPoint, 0, 5)
	for i, used := range []int64{100, 101, 102, 103, 113} {
		run := Run{ID: int64(i + 1), StartedAt: time.Date(2026, 7, 1+i, 0, 0, 0, 0, time.UTC)}
		ds := vsphere.Datastore{ID: "ds-1", CapacityBytes: 1000, FreeBytes: 1000 - used}
		points = append(points, capacityPoint{run: run, ds: capacityDS{run: run, ds: ds}})
	}
	if anomalies := capacityAnomalies(points); len(anomalies) != 1 || anomalies[0].ToRunID != 5 {
		t.Fatalf("anomalies=%+v, want only final spike", anomalies)
	}
	for i := range points {
		points[i].ds.ds.FreeBytes = 1000 - int64(100+i)
	}
	if anomalies := capacityAnomalies(points); len(anomalies) != 0 {
		t.Fatalf("steady anomalies=%+v", anomalies)
	}
}

// A VM spanning several datastores is a split contributor only to the
// datastores it actually lives on, never to every datastore in the context.
func TestSplitVMContributesOnlyToItsOwnDatastores(t *testing.T) {
	vm := vsphere.VM{
		Name:       "vm-iso",
		Datastores: []string{"local-esxi02", "nfs-shared"},
		Disks:      []vsphere.VMDisk{{BackingPath: "[local-esxi02] vm-iso/disk.vmdk", CapacityBytes: 100}},
	}
	group := func(name string) capacityGroup {
		return capacityGroup{items: []capacityDS{{ds: vsphere.Datastore{Name: name}}}}
	}
	item := capacityVM{vm: vm}

	if _, _, ok := appearedVMDelta(item, group("local-esxi01")); ok {
		t.Fatal("appeared VM listed against a datastore it has no files on")
	}
	if _, _, ok := inferredVMDelta(item, item, group("local-esxi01")); ok {
		t.Fatal("inferred VM listed against a datastore it has no files on")
	}
	delta, basis, ok := appearedVMDelta(item, group("local-esxi02"))
	if !ok || basis != BasisSplit || delta != 100 {
		t.Fatalf("own datastore = %v %v %v, want split +100", delta, basis, ok)
	}
	if _, _, ok := appearedVMDelta(item, group("nfs-shared")); !ok {
		t.Fatal("VM dropped from a datastore it is on, even without a disk there")
	}
}
