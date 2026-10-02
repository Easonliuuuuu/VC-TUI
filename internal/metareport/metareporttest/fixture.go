// Package metareporttest seeds a deterministic, visibly synthetic pair of
// assessments for metadata export and saved-report tests.
//
// The estate has two vCenters that use different tag categories, the same
// VM name and managed object ID in both, a custom attribute named "owner"
// under a different numeric key in each, and values that change between the
// captures. The second capture also loses the tagging service on one vCenter
// and the host collection on the other, so every coverage path is exercised.
package metareporttest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Context names and vCenter IDs used by the fixture.
const (
	East       = "synthetic-east"
	West       = "synthetic-west"
	EastVC     = "00000000-0000-0000-0000-00000000ea57"
	WestVC     = "00000000-0000-0000-0000-00000000we57"
	BaseLabel  = "synthetic-base"
	LaterLabel = "synthetic-later"
)

// BaseTime is when the first synthetic capture starts.
var BaseTime = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// PCIReport is a report definition over the fixture: VMs tagged PCI.
const PCIReport = `name = "pci"
description = "Synthetic: VMs in PCI scope"
kinds = ["vm"]
where = ["tag.Compliance=PCI"]
columns = ["context", "vcenter_id", "name", "id", "tag.Compliance", "custom.owner"]
`

func tag(category, name string) vsphere.Tag {
	return vsphere.Tag{ID: "urn:synthetic:tag:" + category + ":" + name, Name: name, CategoryID: "urn:synthetic:cat:" + category, Category: category}
}

func attr(key int32, name, value string) vsphere.CustomAttribute {
	return vsphere.CustomAttribute{Key: key, Name: name, Value: value}
}

func meta(tags []vsphere.Tag, attrs []vsphere.CustomAttribute) vsphere.Metadata {
	if tags == nil {
		tags = []vsphere.Tag{}
	}
	if attrs == nil {
		attrs = []vsphere.CustomAttribute{}
	}
	return vsphere.Metadata{Tags: tags, CustomAttributes: attrs, TagsStatus: vsphere.MetadataAvailable, CustomAttributesStatus: vsphere.MetadataAvailable}
}

func vm(context, id, name string, m vsphere.Metadata) assessment.Observation {
	vc := EastVC
	if context == West {
		vc = WestVC
	}
	return assessment.Observation{Context: context, VCenterID: vc, VM: vsphere.VM{Location: vsphere.Location{Context: context, Datacenter: "synthetic-dc", Path: "/synthetic-dc/vm/" + name}, Metadata: m, ID: id, Name: name}}
}

// Seed stores the two captures and returns their run IDs.
func Seed(t testing.TB, store *assessment.Store) (baseID, laterID int64) {
	t.Helper()
	ctx := context.Background()
	contexts := []*config.Context{{Name: East, Endpoint: "https://east.synthetic.invalid"}, {Name: West, Endpoint: "https://west.synthetic.invalid"}}

	denied := meta(nil, []vsphere.CustomAttribute{attr(7, "owner", "synthetic-dave")})
	denied.TagsStatus, denied.TagsError = vsphere.MetadataDenied, "403 Forbidden: synthetic tagging denial"
	host := vsphere.Host{Location: vsphere.Location{Context: East, Datacenter: "synthetic-dc", Path: "/synthetic-dc/host/esx-01"}, Metadata: meta([]vsphere.Tag{tag("Environment", "Production")}, nil), ID: "host-1", Name: "esx-01"}

	base := map[string]assessment.ContextResult{
		East: {Name: East, VCenterID: EastVC, Status: "success", VMs: []assessment.Observation{
			vm(East, "vm-1", "web", meta([]vsphere.Tag{tag("Environment", "Production"), tag("Compliance", "PCI")}, []vsphere.CustomAttribute{attr(1, "owner", "synthetic-alice")})),
			vm(East, "vm-2", "db", meta([]vsphere.Tag{tag("Environment", "Production")}, []vsphere.CustomAttribute{attr(1, "owner", "synthetic-bob"), attr(2, "tier", "gold")})),
		}},
		West: {Name: West, VCenterID: WestVC, Status: "success", VMs: []assessment.Observation{
			vm(West, "vm-1", "web", meta([]vsphere.Tag{tag("Region", "us-west")}, []vsphere.CustomAttribute{attr(7, "owner", "synthetic-carol")})),
			vm(West, "vm-9", "batch", denied),
		}},
	}
	baseResults := withCollections(base, map[string]assessment.CollectionResult{East: hostCollection(host)})
	baseID = seedRun(t, ctx, store, BaseTime, BaseLabel, contexts, baseResults)

	lostTags := func(o assessment.Observation) assessment.Observation {
		o.VM.Metadata.Tags = []vsphere.Tag{}
		o.VM.Metadata.TagsStatus, o.VM.Metadata.TagsError = vsphere.MetadataUnavailable, "503 Service Unavailable: synthetic tagging outage"
		return o
	}
	later := map[string]assessment.ContextResult{
		East: {Name: East, VCenterID: EastVC, Status: "success", VMs: []assessment.Observation{
			vm(East, "vm-1", "web", meta([]vsphere.Tag{tag("Environment", "Production")}, []vsphere.CustomAttribute{attr(1, "owner", "synthetic-erin")})),
			vm(East, "vm-2", "db", meta([]vsphere.Tag{tag("Environment", "Production"), tag("Compliance", "PCI")}, []vsphere.CustomAttribute{attr(1, "owner", "synthetic-bob"), attr(2, "tier", "gold"), attr(3, "cost-center", "synthetic-42")})),
		}},
		West: {Name: West, VCenterID: WestVC, Status: "success", VMs: []assessment.Observation{
			lostTags(vm(West, "vm-1", "web", meta(nil, []vsphere.CustomAttribute{attr(7, "owner", "synthetic-carol")}))),
			lostTags(vm(West, "vm-9", "batch", meta(nil, []vsphere.CustomAttribute{attr(7, "owner", "synthetic-dave")}))),
		}},
	}
	failedHosts := assessment.CollectionResult{Kind: "host", Status: "failed", Error: "synthetic host collection failure"}
	laterResults := withCollections(later, map[string]assessment.CollectionResult{East: failedHosts})
	laterID = seedRun(t, ctx, store, BaseTime.Add(24*time.Hour), LaterLabel, contexts, laterResults)
	return baseID, laterID
}

func hostCollection(host vsphere.Host) assessment.CollectionResult {
	payload, _ := json.Marshal(host)
	return assessment.CollectionResult{Kind: "host", Status: "success", ItemCount: 1, TagsStatus: host.Metadata.TagsStatus, CustomAttributesStatus: host.Metadata.CustomAttributesStatus,
		Resources: []assessment.ResourceObservation{{VCenterID: EastVC, Context: East, Kind: "host", ID: host.ID, Name: host.Name, Payload: payload}}}
}

// withCollections records every persisted collection for each context, using
// the given host collection where one is supplied.
func withCollections(in map[string]assessment.ContextResult, extra map[string]assessment.CollectionResult) map[string]assessment.ContextResult {
	out := map[string]assessment.ContextResult{}
	for name, r := range in {
		r.Collections = append(r.Collections, assessment.CollectionResult{Kind: "vm", Status: "success", ItemCount: len(r.VMs)}, assessment.CollectionResult{Kind: "snapshot", Status: "empty"})
		if c, ok := extra[name]; ok {
			r.Collections = append(r.Collections, c)
		} else {
			r.Collections = append(r.Collections, assessment.CollectionResult{Kind: "host", Status: "empty"})
		}
		for _, kind := range []string{"cluster", "datastore", "resourcepool", "dvswitch", "network"} {
			r.Collections = append(r.Collections, assessment.CollectionResult{Kind: kind, Status: "empty"})
		}
		out[name] = r
	}
	return out
}

func seedRun(t testing.TB, ctx context.Context, store *assessment.Store, at time.Time, label string, contexts []*config.Context, results map[string]assessment.ContextResult) int64 {
	t.Helper()
	run, err := store.StartRunWithMetadata(ctx, "synthetic", contexts, at, assessment.RunMetadata{Label: label, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	for _, cc := range contexts {
		if err := store.SaveContext(ctx, run.ID, results[cc.Name], at.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinishRun(ctx, run.ID, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return run.ID
}
