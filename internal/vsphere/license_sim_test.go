package vsphere_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// plantedKeys are distinctive, visibly synthetic strings standing in for real
// license keys. No output of any kind may contain them.
const (
	plantedKeyStandard = "SYNTH-LICEN-SEKEY-MUST-NEVERLEAK"
	plantedKeyTrial    = "SYNTH-TRIAL-KEYSH-OULDN-OTLEAK"
)

// seedLicenses replaces the simulator's default evaluation license with two
// synthetic records and assigns the first to every host, through the
// simulator's own state (no vsfleet mutation is involved).
func seedLicenses(t *testing.T, model *simulator.Model, expires time.Time) {
	t.Helper()
	m := model.Map()
	lm, ok := m.Get(types.ManagedObjectReference{Type: "LicenseManager", Value: "LicenseManager"}).(*simulator.LicenseManager)
	if !ok {
		t.Fatal("simulator has no LicenseManager")
	}
	standard := types.LicenseManagerLicenseInfo{
		LicenseKey: plantedKeyStandard, EditionKey: "esx.standard.cpuPackage", Name: "Synthetic vSphere Standard (example)",
		Total: 16, Used: 2, CostUnit: "cpuPackage",
		Properties: []types.KeyAnyValue{
			{Key: "expirationDate", Value: expires},
			{Key: "feature", Value: types.KeyValue{Key: "vmotion", Value: "vMotion"}},
			{Key: "feature", Value: types.KeyValue{Key: "dvs", Value: "Distributed Switch"}},
		},
		Labels: []types.KeyValue{{Key: "owner", Value: "synthetic-team"}},
	}
	trial := types.LicenseManagerLicenseInfo{LicenseKey: plantedKeyTrial, EditionKey: "eval", Name: "Synthetic Evaluation Mode (example)"}
	lm.Licenses = []types.LicenseManagerLicenseInfo{standard, trial}

	lam, ok := m.Get(*lm.LicenseAssignmentManager).(*simulator.LicenseAssignmentManager)
	if !ok {
		t.Fatal("simulator has no LicenseAssignmentManager")
	}
	var assigned []types.LicenseAssignmentManagerLicenseAssignment
	for _, ref := range m.AllReference("HostSystem") {
		host := ref.Reference()
		assigned = append(assigned, types.LicenseAssignmentManagerLicenseAssignment{
			EntityId: host.Value, EntityDisplayName: "synthetic-" + host.Value, Scope: "synthetic-vcenter-uuid", AssignedLicense: standard,
		})
	}
	lam.QueryAssignedLicensesResponse.Returnval = assigned
}

func licenseModel(m *simulator.Model) {
	m.Datacenter = 1
	m.Cluster = 1
	m.ClusterHost = 2
	m.Machine = 1
}

func TestFetchLicensesReportsProductUsageAndExpirationWithoutKeys(t *testing.T) {
	model := simulator.VPX()
	licenseModel(model)
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	expires := time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)
	seedLicenses(t, model, expires)
	gc, endpoint := dialSimulator(t, model)
	client := clientFor(gc, endpoint, "")

	inv := client.FetchLicenses(context.Background())
	if inv.Status != "success" || inv.Message != "" {
		t.Fatalf("status=%q message=%q", inv.Status, inv.Message)
	}
	if len(inv.Licenses) != 2 {
		t.Fatalf("licenses=%d, want 2: %+v", len(inv.Licenses), inv.Licenses)
	}
	// Deterministic ordinal IDs after sorting by name: Evaluation, then Standard.
	eval, std := inv.Licenses[0], inv.Licenses[1]
	if eval.ID != "license-001" || std.ID != "license-002" {
		t.Fatalf("ids=%q,%q", eval.ID, std.ID)
	}
	if std.Name != "Synthetic vSphere Standard (example)" || std.EditionKey != "esx.standard.cpuPackage" || std.CostUnit != "cpuPackage" || std.Total != 16 || std.Used != 2 {
		t.Fatalf("standard=%+v", std)
	}
	if std.Expiration == nil || !std.Expiration.Equal(expires) {
		t.Fatalf("expiration=%v, want %v", std.Expiration, expires)
	}
	if strings.Join(std.Features, ",") != "Distributed Switch,vMotion" {
		t.Fatalf("features=%v", std.Features)
	}
	if eval.Expiration != nil {
		t.Fatalf("license with no expiration property has expiration %v", eval.Expiration)
	}
	if want := len(model.Map().AllReference("HostSystem")); len(std.Assignments) != want || want == 0 {
		t.Fatalf("assignments=%+v, want one per host", std.Assignments)
	}
	for _, a := range std.Assignments {
		if a.EntityType != "host" || !strings.HasPrefix(a.EntityName, "synthetic-host-") || a.Scope != "synthetic-vcenter-uuid" {
			t.Fatalf("assignment=%+v", a)
		}
	}

	// No representation of the result may carry a key.
	raw, err := json.Marshal(inv.Licenses)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{string(raw), fmt.Sprintf("%v", inv), fmt.Sprintf("%+v", inv), fmt.Sprintf("%#v", inv)} {
		for _, key := range []string{plantedKeyStandard, plantedKeyTrial} {
			if strings.Contains(view, key) {
				t.Fatalf("license key leaked into %q", view[:min(len(view), 80)])
			}
		}
	}
	// Labels are free text and are not collected either.
	if strings.Contains(string(raw), "synthetic-team") {
		t.Fatalf("labels were persisted: %s", raw)
	}
}

func TestFetchLicensesWithDeniedAssignmentsIsPartialAndKeyFree(t *testing.T) {
	model := simulator.VPX()
	licenseModel(model)
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	seedLicenses(t, model, time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC))
	model.Service.FaultInjector().AddRule(&simulator.FaultInjectionRule{
		MethodName: "QueryAssignedLicenses", ObjectType: "*", ObjectName: "*", Probability: 1, Enabled: true,
		FaultType: simulator.FaultTypeCustom,
		Fault:     &types.NoPermission{PrivilegeId: "Global.Licenses"},
		// A hostile server could echo a key in its fault text.
		Message: "denied for " + plantedKeyStandard,
	})
	gc, endpoint := dialSimulator(t, model)
	inv := clientFor(gc, endpoint, "").FetchLicenses(context.Background())

	if inv.Status != vsphere.LicenseStatusPartial {
		t.Fatalf("status=%q message=%q", inv.Status, inv.Message)
	}
	if !strings.Contains(inv.Message, "permission denied") || !strings.Contains(inv.Message, "Global.Licenses") {
		t.Fatalf("message does not explain the denial: %q", inv.Message)
	}
	if len(inv.Licenses) != 2 {
		t.Fatalf("licenses=%d: the list answered and must be kept", len(inv.Licenses))
	}
	for _, l := range inv.Licenses {
		if len(l.Assignments) != 0 {
			t.Fatalf("assignments recorded despite denial: %+v", l)
		}
	}
	raw, _ := json.Marshal(inv)
	for _, key := range []string{plantedKeyStandard, plantedKeyTrial} {
		if strings.Contains(inv.Message, key) || strings.Contains(string(raw), key) {
			t.Fatalf("key leaked: %q / %s", inv.Message, raw)
		}
	}
}

// An account without the license privilege can be handed an empty list rather
// than an error. That must be unavailable, never a zero-license inventory.
func TestFetchLicensesEmptyListIsUnavailableNotEmpty(t *testing.T) {
	model := simulator.VPX()
	licenseModel(model)
	if err := model.Create(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(model.Remove)
	seedLicenses(t, model, time.Now())
	lm := model.Map().Get(types.ManagedObjectReference{Type: "LicenseManager", Value: "LicenseManager"}).(*simulator.LicenseManager)
	lm.Licenses = nil
	lam := model.Map().Get(*lm.LicenseAssignmentManager).(*simulator.LicenseAssignmentManager)
	lam.QueryAssignedLicensesResponse.Returnval = nil
	gc, endpoint := dialSimulator(t, model)

	inv := clientFor(gc, endpoint, "").FetchLicenses(context.Background())
	if inv.Status != vsphere.LicenseStatusUnavailable || len(inv.Licenses) != 0 {
		t.Fatalf("status=%q licenses=%d", inv.Status, len(inv.Licenses))
	}
	if !strings.Contains(inv.Message, "Global.Licenses") || !strings.Contains(inv.Message, "rather than as zero licenses") {
		t.Fatalf("message=%q", inv.Message)
	}
}
