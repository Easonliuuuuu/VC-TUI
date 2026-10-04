package demo

import (
	"context"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The demo's ledger holds five runs, oldest first. The newest run's date is
// load-bearing: health's orphan analysis treats a file modified within a day
// of the run as possibly in flight, and finance01.vmdk must read as settled.
var (
	runDates = []time.Time{
		time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 14, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC),
		demoNow,
	}
	runLabels = []string{"q2-baseline", "june-audit", "july-audit", "pre-migration", "september-audit"}
)

// AssessmentService returns a seeded, in-memory history service for the
// presentation demo: five dated runs whose older states are derived backwards
// from today's estate (see viewAt), so drift, churn, snapshot and capacity
// history all have something to show. It lets the History health pane show the
// orphaned VM and zombie-VMDK evidence without touching disk. dr-site fails in
// every run.
func (b *Backend) AssessmentService() (*assessment.Service, func(), error) {
	store, err := assessment.OpenMemory()
	if err != nil {
		return nil, nil, err
	}
	closeStore := func() { _ = store.Close() }
	ctx := context.Background()
	fail := func(err error) (*assessment.Service, func(), error) {
		closeStore()
		return nil, nil, err
	}
	for run, at := range runDates {
		r, err := store.StartRunWithMetadata(ctx, "demo", b.contexts, at, assessment.RunMetadata{
			InventorySchemaVersion: assessment.CurrentInventorySchemaVersion, Label: runLabels[run],
		})
		if err != nil {
			return fail(err)
		}
		for i, id := range []string{"demo-prod-vc", "demo-edge-vc"} {
			cc := b.contexts[i]
			if err := saveSite(ctx, store, r.ID, cc, id, viewAt(b.estates[cc.Name], run), at.Add(time.Minute)); err != nil {
				return fail(err)
			}
		}
		dr := b.contexts[2]
		failed := make([]assessment.CollectionResult, 0, 8)
		for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
			c := assessment.CollectionResult{Kind: kind, Status: "failed"}
			if kind == "vm" {
				c.Error = "proxy connection refused"
			}
			failed = append(failed, c)
		}
		if err := store.SaveContext(ctx, r.ID, assessment.ContextResult{Name: dr.Name, VCenterID: "demo-dr-site", Status: "failed", Error: "proxy 10.24.0.8:3128: connection refused", Collections: failed}, at.Add(time.Minute)); err != nil {
			return fail(err)
		}
		if _, err := store.FinishRun(ctx, r.ID, at.Add(2*time.Minute)); err != nil {
			return fail(err)
		}
	}
	// No Collector: the presentation dials nothing, so it cannot run a live
	// capture. Service.CanCapture reports false and the TUI hides the "n"
	// action rather than offering a capture that always fails (issue #119).
	return &assessment.Service{Store: store}, closeStore, nil
}

func saveSite(ctx context.Context, store *assessment.Store, runID int64, cc *config.Context, vcenterID string, inv *vsphere.Inventory, at time.Time) error {
	vms := append(append([]vsphere.VM(nil), inv.VMs...), inv.Templates...)
	observations := make([]assessment.Observation, 0, len(vms))
	for _, vm := range vms {
		observations = append(observations, assessment.Observation{Context: cc.Name, VCenterID: vcenterID, VM: vm})
	}
	res := func(kind string, values any, n int) assessment.CollectionResult {
		return assessment.CollectionResult{Kind: kind, Status: "success", ItemCount: n, Resources: demoResources(cc.Name, vcenterID, kind, values)}
	}
	return store.SaveContext(ctx, runID, assessment.ContextResult{Name: cc.Name, VCenterID: vcenterID, Status: "success", VMs: observations, Collections: []assessment.CollectionResult{
		{Kind: "vm", Status: "success", ItemCount: len(observations)},
		res("host", inv.Hosts, len(inv.Hosts)),
		res("cluster", inv.Clusters, len(inv.Clusters)),
		res("resourcepool", inv.ResourcePools, len(inv.ResourcePools)),
		res("vapp", inv.VApps, len(inv.VApps)),
		res("datastore", inv.Datastores, len(inv.Datastores)),
		res("dvswitch", inv.DVSwitches, len(inv.DVSwitches)),
		res("network", inv.Networks, len(inv.Networks)),
		{Kind: "snapshot", Status: snapshotStatus(observations), ItemCount: snapshotTotal(observations)},
	}}, at)
}

// pinnedVM names the VMs that exist in every run and are never resized: the
// evidence anchors and the vCenter's own agents.
func pinnedVM(name string) bool {
	switch name {
	case "api-01", "postgres-01", "build-runner-03", "finance01":
		return true
	}
	return strings.HasPrefix(name, "vCLS-")
}

// bornRun is the first run a VM appears in. About one VM in seven was deployed
// after the oldest run, spread over the four later ones.
func bornRun(ctx, name string) int {
	if pinnedVM(name) || hv(ctx, name, "born")%1000 >= 140 {
		return 0
	}
	return 1 + int(hv(ctx, name, "born-run")%4)
}

// viewAt derives what the estate looked like in run (0 oldest, 4 newest) from
// its current state: later deployments are absent, some decommissioned VMs
// still exist, a few VMs were smaller, snapshots created since did not yet
// exist, datastores had more free space, and one host had not joined.
func viewAt(e *estate, run int) *vsphere.Inventory {
	cur := e.inv
	if run == len(runDates)-1 {
		return cur
	}
	out := *cur
	out.VMs = make([]vsphere.VM, 0, len(cur.VMs)+len(cur.VMs)/20)
	at := runDates[run]
	for i, vm := range cur.VMs {
		if bornRun(cur.Context, vm.Name) > run {
			continue
		}
		vm = agedVM(vm, at, run)
		out.VMs = append(out.VMs, vm)
		// Every twentieth VM has a since-decommissioned predecessor.
		if i%20 == 7 && !pinnedVM(vm.Name) {
			if die := 1 + int(hv(cur.Context, vm.Name, "die")%4); run < die {
				old := vm
				old.Name, old.ID = "old-"+vm.Name, vm.ID+"-retired"
				old.Path = strings.Replace(vm.Path, "/"+vm.Name, "/old-"+vm.Name, 1)
				old.InstanceUUID, old.BIOSUUID = guid(cur.Context, "inst", old.Name), guid(cur.Context, "bios", old.Name)
				old.Snapshots, old.IPAddress, old.GuestHostName = nil, "", ""
				old.Disks = append([]vsphere.VMDisk(nil), vm.Disks...)
				for d := range old.Disks {
					old.Disks[d].BackingPath = strings.Replace(old.Disks[d].BackingPath, "] "+vm.Name+"/"+vm.Name, "] "+old.Name+"/"+old.Name, 1)
				}
				out.VMs = append(out.VMs, old)
			}
		}
	}

	out.Hosts = make([]vsphere.Host, 0, len(cur.Hosts))
	joined := ""
	for _, h := range cur.Hosts {
		if h.Name == e.lateHost {
			joined = h.Cluster
			continue
		}
		out.Hosts = append(out.Hosts, h)
	}
	out.Clusters = append([]vsphere.Cluster(nil), cur.Clusters...)
	for i := range out.Clusters {
		if out.Clusters[i].Name == joined {
			out.Clusters[i].Hosts--
			out.Clusters[i].EffectiveHost--
		}
	}

	out.Datastores = append([]vsphere.Datastore(nil), cur.Datastores...)
	behind := int64(len(runDates) - 1 - run)
	for i := range out.Datastores {
		ds := &out.Datastores[i]
		rate := int64(12) // 1.2% of capacity per interval
		if ds.Name == e.fastDS {
			rate = 45
		}
		ds.FreeBytes = min(ds.CapacityBytes, ds.FreeBytes+ds.CapacityBytes*rate*behind/1000)
	}
	return &out
}

func agedVM(vm vsphere.VM, at time.Time, run int) vsphere.VM {
	if len(vm.Snapshots) > 0 {
		var kept []vsphere.VMSnapshot
		for _, s := range vm.Snapshots {
			if !s.CreateTime.After(at) {
				kept = append(kept, s)
			}
		}
		vm.Snapshots = kept
	}
	if !pinnedVM(vm.Name) && hv(vm.Context, vm.Name, "resize")%40 == 0 && vm.CPU > 1 && run < 1+int(hv(vm.Context, vm.Name, "resize-run")%4) {
		vm.CPU, vm.MemoryMB = vm.CPU/2, max(vm.MemoryMB/2, 1024)
		vm.CPUSockets = vm.CPU
	}
	return vm
}
