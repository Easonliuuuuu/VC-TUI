package vsphere

import (
	"errors"
	"strings"
	"testing"

	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

const unitKey = "SYNTH-UNITT-ESTKE-YMUST-NOTLEAK"

func TestLicenseListDeniedIsUnavailable(t *testing.T) {
	err := soap.WrapVimFault(&types.NoPermission{PrivilegeId: "Global.Licenses"})
	inv := buildLicenseInventory(licenseWire{ListErr: err})
	if inv.Status != LicenseStatusUnavailable || len(inv.Licenses) != 0 {
		t.Fatalf("status=%q licenses=%d", inv.Status, len(inv.Licenses))
	}
	if !strings.Contains(inv.Message, "permission denied") || !strings.Contains(inv.Message, "Global.Licenses") {
		t.Fatalf("message=%q", inv.Message)
	}
}

func TestLicenseListUnsupportedIsDistinctFromDenied(t *testing.T) {
	inv := buildLicenseInventory(licenseWire{ListErr: soap.WrapVimFault(&types.MethodNotFound{Method: "x"})})
	if inv.Status != LicenseStatusUnavailable || !strings.HasPrefix(inv.Message, "unsupported:") {
		t.Fatalf("status=%q message=%q", inv.Status, inv.Message)
	}
	denied := buildLicenseInventory(licenseWire{ListErr: soap.WrapVimFault(&types.NoPermission{})})
	if denied.Message == inv.Message || strings.HasPrefix(denied.Message, "unsupported:") {
		t.Fatalf("denied and unsupported are not distinguishable: %q vs %q", denied.Message, inv.Message)
	}
}

func TestLicenseZeroRecordsIsNeverEmpty(t *testing.T) {
	inv := buildLicenseInventory(licenseWire{})
	if inv.Status != LicenseStatusUnavailable || inv.Status == "empty" || inv.Message == "" {
		t.Fatalf("status=%q message=%q", inv.Status, inv.Message)
	}
}

func TestLicenseAssignmentsNotSupportedIsPartial(t *testing.T) {
	inv := buildLicenseInventory(licenseWire{
		Infos:     []types.LicenseManagerLicenseInfo{{LicenseKey: unitKey, Name: "Synthetic", Total: 8, Used: 1}},
		AssignErr: errLicenseAssignmentsUnsupported,
	})
	if inv.Status != LicenseStatusPartial || len(inv.Licenses) != 1 || !strings.Contains(inv.Message, "unsupported") {
		t.Fatalf("%+v", inv)
	}
}

func TestLicenseErrorTextNeverCarriesAKey(t *testing.T) {
	inv := buildLicenseInventory(licenseWire{
		Infos:     []types.LicenseManagerLicenseInfo{{LicenseKey: unitKey, Name: "Synthetic"}},
		AssignErr: errors.New("query failed for " + unitKey),
	})
	if inv.Status != LicenseStatusPartial {
		t.Fatalf("status=%q", inv.Status)
	}
	if strings.Contains(inv.Message, unitKey) || !strings.Contains(inv.Message, LicenseRedacted) {
		t.Fatalf("message=%q", inv.Message)
	}
}

func TestLicenseAssignmentToUnlistedLicenseIsKept(t *testing.T) {
	inv := buildLicenseInventory(licenseWire{
		Infos: []types.LicenseManagerLicenseInfo{{LicenseKey: unitKey, Name: "Listed"}},
		Assigned: []types.LicenseAssignmentManagerLicenseAssignment{
			{EntityId: "host-1", EntityDisplayName: "esx-1", AssignedLicense: types.LicenseManagerLicenseInfo{LicenseKey: "OTHER", Name: "Unlisted", Used: 1}},
		},
	})
	if inv.Status != "success" || len(inv.Licenses) != 2 {
		t.Fatalf("%+v", inv)
	}
}

func TestLicenseListEmptyButAssignmentsPresentIsPartial(t *testing.T) {
	inv := buildLicenseInventory(licenseWire{
		Assigned: []types.LicenseAssignmentManagerLicenseAssignment{
			{EntityId: "host-1", EntityDisplayName: "esx-1", AssignedLicense: types.LicenseManagerLicenseInfo{LicenseKey: unitKey, Name: "Only via assignment"}},
		},
	})
	if inv.Status != LicenseStatusPartial || len(inv.Licenses) != 1 || len(inv.Licenses[0].Assignments) != 1 {
		t.Fatalf("%+v", inv)
	}
}

func TestLicenseOrdinalsAreDeterministicRegardlessOfWireOrder(t *testing.T) {
	a := types.LicenseManagerLicenseInfo{LicenseKey: "KEY-A", Name: "Same", Total: 4}
	b := types.LicenseManagerLicenseInfo{LicenseKey: "KEY-B", Name: "Same", Total: 4}
	x := buildLicenseInventory(licenseWire{Infos: []types.LicenseManagerLicenseInfo{a, b}})
	y := buildLicenseInventory(licenseWire{Infos: []types.LicenseManagerLicenseInfo{b, a}})
	if len(x.Licenses) != 2 || len(y.Licenses) != 2 {
		t.Fatal("expected two licenses")
	}
	for i := range x.Licenses {
		if x.Licenses[i].ID != y.Licenses[i].ID || x.Licenses[i].Name != y.Licenses[i].Name {
			t.Fatalf("order differs: %+v vs %+v", x.Licenses, y.Licenses)
		}
	}
}

func TestLicenseEntityTypes(t *testing.T) {
	for id, want := range map[string]string{
		"host-12":                              "host",
		"domain-c8":                            "cluster",
		"11111111-2222-3333-4444-555555555555": "vcenter",
		"something-else":                       "other",
	} {
		if got := licenseEntityType(id); got != want {
			t.Errorf("licenseEntityType(%q)=%q, want %q", id, got, want)
		}
	}
}

func TestFetchLicensesWithoutManagerIsUnsupported(t *testing.T) {
	inv := (&Client{}).FetchLicenses(t.Context())
	if inv.Status != LicenseStatusUnavailable || !strings.HasPrefix(inv.Message, "unsupported:") {
		t.Fatalf("%+v", inv)
	}
}
