package sizing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const g = int64(1 << 30)

func ip(v int) *int          { return &v }
func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func ctxRun(name string, vm, ds string) assessment.ContextRun {
	return assessment.ContextRun{Name: name, VMStatus: vm, Collections: []assessment.CollectionRun{{Kind: "vm", Status: vm}, {Kind: "datastore", Status: ds}}}
}

func dsResource(t *testing.T, d vsphere.Datastore) assessment.ResourceObservation {
	t.Helper()
	p, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: d.Context, Kind: "datastore", ID: d.ID, Name: d.Name, Payload: p}
}

func vm(ctx, name, cluster string, cpu int32, memGiB int64, disks ...vsphere.VMDisk) assessment.ExportVM {
	return assessment.ExportVM{Observation: assessment.Observation{Context: ctx, VCenterID: "vc-" + ctx, VM: vsphere.VM{
		ID: name, Name: name, Cluster: cluster, PowerState: "poweredOn", CPU: cpu, MemoryMB: memGiB * 1024,
		ConfigurationAvailable: true, Disks: disks, StorageGB: 10,
	}}}
}

func disk(key int32, gb int64, path string) vsphere.VMDisk {
	return vsphere.VMDisk{Key: key, Label: "Hard disk", CapacityBytes: gb * g, BackingPath: path}
}

// fixture: two source clusters (prod/cluster-a, dr/cluster-b), one datastore
// seen under two names by two vCenters, an RDM, and a disk shared across VMs.
//
//	vCPU 40, memory 160 GiB, provisioned datastore disks 600 GiB, RDM 500 GiB.
func fixture(t *testing.T) assessment.ExportData {
	shared := disk(2, 100, "[shared-ds] app/shared.vmdk")
	sharedB := disk(2, 100, "[lun-7] app/shared.vmdk")
	rdm := vsphere.VMDisk{Key: 3, Label: "RDM", CapacityBytes: 500 * g, Raw: true, BackingType: "rdm", RawLUNID: "naa.600A"}
	a1 := vm("prod", "a1", "cluster-a", 8, 32, disk(1, 100, "[shared-ds] a1/a1.vmdk"), shared)
	a2 := vm("prod", "a2", "cluster-a", 16, 64, disk(1, 200, "[shared-ds] a2/a2.vmdk"))
	b1 := vm("dr", "b1", "cluster-b", 8, 32, disk(1, 100, "[lun-7] b1/b1.vmdk"), sharedB, rdm)
	b1.Observation.VM.MemoryAllocation = &vsphere.VMResourceAllocation{Reservation: i64(32 * 1024)}
	b2 := vm("dr", "b2", "cluster-b", 8, 32, disk(1, 100, "[lun-7] b2/b2.vmdk"))
	b1.Observation.VM.Partitions = []vsphere.VMPartition{{Path: "/", DiskKeys: []int32{1}, CapacityBytes: 100 * g, FreeBytes: 60 * g}, {Path: "/data", DiskKeys: []int32{3}, CapacityBytes: 500 * g, FreeBytes: 100 * g}}
	backing := vsphere.DatastoreBacking{VMFSUUID: "uuid-1", Extents: []string{"naa.shared"}}
	return assessment.ExportData{
		Run:      assessment.Run{ID: 42, Status: assessment.RunComplete, FinishedAt: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), InventorySchemaVersion: "19"},
		Contexts: []assessment.ContextRun{ctxRun("prod", "success", "success"), ctxRun("dr", "success", "success")},
		VMs:      []assessment.ExportVM{a1, a2, b1, b2},
		Resources: []assessment.ResourceObservation{
			dsResource(t, vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "d1", Name: "shared-ds", CapacityBytes: 2048 * g, FreeBytes: 1000 * g, Backing: backing}),
			dsResource(t, vsphere.Datastore{Location: vsphere.Location{Context: "dr"}, ID: "d2", Name: "lun-7", CapacityBytes: 2048 * g, FreeBytes: 1000 * g, Backing: backing}),
		},
	}
}

func full(hosts int) Scenario {
	return Scenario{Hosts: ip(hosts), HostCores: ip(16), HostRAMGiB: f64(128), DatastoreCapacityBytes: f64(float64(700 * g)), RDMCapacityBytes: f64(float64(500 * g)), CPURatio: f64(2)}
}

func dim(t *testing.T, r Result, name string) Dimension {
	t.Helper()
	for _, d := range r.Dimensions {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no dimension %s", name)
	return Dimension{}
}

var now = time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

func TestFitsUnderNMinusOne(t *testing.T) {
	r := Evaluate(fixture(t), full(3), now)
	if r.Verdict != VerdictFit {
		t.Fatalf("verdict=%s dims=%+v", r.Verdict, r.Dimensions)
	}
	if r.Basis != "allocation-based" || r.RunID != 42 || !r.GeneratedAt.Equal(now) {
		t.Fatalf("provenance: %+v", r)
	}
	if c := dim(t, r, "cpu"); *c.Required != 40 || *c.Supply != 64 {
		t.Fatalf("cpu=%+v", c)
	}
	// 600 GiB: the disk shared between clusters and reached through two
	// names is counted once, and the 500 GiB RDM is not added.
	if d := dim(t, r, "datastore-capacity"); *d.Required != 600 {
		t.Fatalf("datastore required=%v", *d.Required)
	}
	if d := dim(t, r, "rdm-capacity"); *d.Required != 500 || d.Verdict != VerdictFit {
		t.Fatalf("rdm=%+v", d)
	}
	if len(r.SourceStorage) != 1 || len(r.SourceStorage[0].Aliases) != 2 || r.SourceStorage[0].CapacityBytes != 2048*g {
		t.Fatalf("shared datastore should be one group counted once: %+v", r.SourceStorage)
	}
	if r.Readiness.Blockers == 0 || !r.Readiness.RDMsRequireExplicit {
		t.Fatalf("readiness link should surface the RDM blocker: %+v", r.Readiness)
	}
	if !strings.Contains(dim(t, r, "cpu").Formula, "(1 + growth") {
		t.Fatal("formula missing")
	}
}

func TestInsufficientUnderNMinusOne(t *testing.T) {
	r := Evaluate(fixture(t), full(2), now)
	if r.Verdict != VerdictInsufficient {
		t.Fatalf("verdict=%s", r.Verdict)
	}
	// 2 hosts minus one failure leaves 1 host: 32 vCPU capacity, 128 GiB.
	if d := dim(t, r, "cpu"); d.Verdict != VerdictInsufficient || *d.Supply != 32 {
		t.Fatalf("cpu=%+v", d)
	}
	if d := dim(t, r, "memory"); d.Verdict != VerdictInsufficient || *d.Supply != 128 {
		t.Fatalf("mem=%+v", d)
	}
	sc := full(2)
	sc.HostFailures = ip(0)
	if r := Evaluate(fixture(t), sc, now); dim(t, r, "cpu").Verdict != VerdictFit {
		t.Fatalf("without the failure allowance 2 hosts should fit cpu: %+v", dim(t, r, "cpu"))
	}
}

func TestGrowthAndDatastoreInsufficient(t *testing.T) {
	sc := full(3)
	sc.GrowthPct = f64(25) // 600 -> 750 GiB > 700
	r := Evaluate(fixture(t), sc, now)
	if d := dim(t, r, "datastore-capacity"); d.Verdict != VerdictInsufficient {
		t.Fatalf("datastore=%+v", d)
	}
}

func TestMemoryReservationCheck(t *testing.T) {
	sc := full(3)
	sc.HostRAMGiB = f64(8) // usable 16 GiB < 32 GiB reserved
	r := Evaluate(fixture(t), sc, now)
	if d := dim(t, r, "memory-reservation"); d.Verdict != VerdictInsufficient {
		t.Fatalf("reservation=%+v", d)
	}
}

func TestMissingAssumptionsAreUnknown(t *testing.T) {
	r := Evaluate(fixture(t), Scenario{}, now)
	if r.Verdict != VerdictUnknown {
		t.Fatalf("verdict=%s", r.Verdict)
	}
	for _, d := range r.Dimensions {
		if d.Verdict != VerdictUnknown {
			t.Fatalf("%s=%s, want unknown", d.Name, d.Verdict)
		}
	}
	missing := 0
	for _, a := range r.Assumptions {
		if a.Source == "missing" {
			missing++
		}
	}
	if missing < 4 {
		t.Fatalf("assumptions=%+v", r.Assumptions)
	}
	sc := full(3)
	sc.RDMCapacityBytes = nil
	r = Evaluate(fixture(t), sc, now)
	if d := dim(t, r, "rdm-capacity"); d.Verdict != VerdictUnknown {
		t.Fatalf("RDM without target capacity=%+v", d)
	}
	if r.Verdict != VerdictUnknown {
		t.Fatalf("verdict=%s", r.Verdict)
	}
}

func TestPartialCoverageIsNeverFit(t *testing.T) {
	data := fixture(t)
	data.Run.Status = assessment.RunPartial
	data.Contexts[1] = ctxRun("dr", "failed", "success")
	r := Evaluate(data, full(3), now)
	if r.Verdict != VerdictUnknown {
		t.Fatalf("verdict=%s", r.Verdict)
	}
	if len(r.Coverage) == 0 {
		t.Fatal("coverage gaps not reported")
	}
	// Even incomplete, demand that already exceeds supply stays insufficient.
	r = Evaluate(data, full(2), now)
	if r.Verdict != VerdictInsufficient {
		t.Fatalf("verdict=%s", r.Verdict)
	}
}

func TestMissingDatastoreCollectionIsUnknownForStorageOnly(t *testing.T) {
	data := fixture(t)
	data.Contexts[0] = ctxRun("prod", "success", "failed")
	r := Evaluate(data, full(3), now)
	if dim(t, r, "datastore-capacity").Verdict != VerdictUnknown || dim(t, r, "cpu").Verdict != VerdictFit {
		t.Fatalf("dims=%+v", r.Dimensions)
	}
}

func TestAmbiguousStorageIdentityIsUnknown(t *testing.T) {
	data := fixture(t)
	for i := range data.Resources {
		var d vsphere.Datastore
		_ = json.Unmarshal(data.Resources[i].Payload, &d)
		d.Backing = vsphere.DatastoreBacking{}
		d.Name = "same-name"
		data.Resources[i] = dsResource(t, d)
	}
	for i := range data.VMs {
		v := &data.VMs[i].Observation.VM
		for j := range v.Disks {
			if !v.Disks[j].Raw {
				v.Disks[j].BackingPath = strings.NewReplacer("[shared-ds]", "[same-name]", "[lun-7]", "[same-name]").Replace(v.Disks[j].BackingPath)
			}
		}
	}
	r := Evaluate(data, full(3), now)
	if d := dim(t, r, "datastore-capacity"); d.Verdict != VerdictUnknown {
		t.Fatalf("ambiguous identity=%+v", d)
	}
}

func TestSharedBusRDMWithoutLUNIdentityIsUnknown(t *testing.T) {
	data := fixture(t)
	v := &data.VMs[2].Observation.VM
	v.Disks[2].RawLUNID = ""
	v.Disks[2].SharedBus = "physicalSharing"
	r := Evaluate(data, full(3), now)
	if d := dim(t, r, "rdm-capacity"); d.Verdict != VerdictUnknown {
		t.Fatalf("rdm=%+v", d)
	}
}

func TestSharedRDMCountedOnce(t *testing.T) {
	data := fixture(t)
	rdm := vsphere.VMDisk{Key: 3, Label: "RDM", CapacityBytes: 500 * g, Raw: true, RawLUNID: "naa.600A"}
	data.VMs[3].Observation.VM.Disks = append(data.VMs[3].Observation.VM.Disks, rdm)
	r := Evaluate(data, full(3), now)
	if d := dim(t, r, "rdm-capacity"); *d.Required != 500 {
		t.Fatalf("shared RDM double counted: %+v", d)
	}
}

func TestExclusionsAreExplainedAndScopeApplies(t *testing.T) {
	data := fixture(t)
	tpl := vm("prod", "tpl", "cluster-a", 4, 4)
	tpl.Observation.VM.IsTemplate = true
	off := vm("prod", "off", "cluster-a", 64, 512)
	off.Observation.VM.PowerState = "poweredOff"
	data.VMs = append(data.VMs, tpl, off)
	sc := full(3)
	sc.Clusters = []string{"cluster-a"}
	r := Evaluate(data, sc, now)
	reasons := map[string]string{}
	for _, x := range r.Exclusions {
		reasons[x.Name] = x.Reason
	}
	for _, n := range []string{"tpl", "off", "b1", "b2"} {
		if reasons[n] == "" {
			t.Fatalf("missing exclusion for %s: %+v", n, r.Exclusions)
		}
	}
	if c := dim(t, r, "cpu"); *c.Required != 24 {
		t.Fatalf("cpu required=%v", *c.Required)
	}
	sc.IncludePoweredOff = true
	if c := dim(t, Evaluate(data, sc, now), "cpu"); *c.Required != 88 {
		t.Fatalf("with powered-off cpu required=%v", *c.Required)
	}
}

func TestGuestUseIsSeparateFromDatastoreAndRDM(t *testing.T) {
	r := Evaluate(fixture(t), full(3), now)
	byName := map[string]Measure{}
	for _, m := range r.Measures {
		byName[m.Name] = m
	}
	if byName["guest-fs-used-datastore-backed"].Value != 40 || byName["guest-fs-used-rdm-backed"].Value != 400 {
		t.Fatalf("guest split: %+v", byName)
	}
	if byName["guest-fs-used-datastore-backed"].InVerdict || byName["datastore-provisioned"].Value != 600 {
		t.Fatalf("measures: %+v", byName)
	}
}

func TestEvaluateDoesNotMutateInput(t *testing.T) {
	data := fixture(t)
	before, _ := json.Marshal(data)
	Evaluate(data, full(3), now)
	after, _ := json.Marshal(data)
	if string(before) != string(after) {
		t.Fatal("input was mutated")
	}
}
