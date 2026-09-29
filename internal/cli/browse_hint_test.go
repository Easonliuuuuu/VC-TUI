package cli

import (
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestAssessmentOrphansNamesDeniedPrivilegeNotFlag(t *testing.T) {
	strong := vsphere.DatastoreBacking{VMFSUUID: "uuid-1"}
	const denied = "ServerFaultCode: Permission to perform this operation was denied."

	// Requested but denied: name the privilege, never suggest the flag again.
	db := newOrphanCoverageHistoryDB(t, vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-1", Name: "datastore1", Accessible: true, Backing: strong, BrowseStatus: "denied", BrowseError: denied})
	stdout, stderr, err := runAssessmentOrphans(t, db, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "NOT EVALUATED (browse denied on 1/1 datastores: grant Datastore.Browse)") || strings.Contains(stdout, "capture with --browse-datastores") {
		t.Fatalf("denied stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "not evaluated (denied)") {
		t.Fatalf("denied stderr=%q", stderr)
	}

	// Requested but failed for another reason: show the failure, no flag hint.
	db = newOrphanCoverageHistoryDB(t, vsphere.Datastore{Location: vsphere.Location{Context: "prod"}, ID: "ds-2", Name: "datastore2", Accessible: true, Backing: strong, BrowseStatus: "failed", BrowseError: "host unreachable"})
	stdout, _, err = runAssessmentOrphans(t, db, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "browse failed on 1/1 datastores: host unreachable") || strings.Contains(stdout, "--browse-datastores") || strings.Contains(stdout, "Datastore.Browse") {
		t.Fatalf("failed stdout=%q", stdout)
	}
}

func TestPrintRunAppendsNotes(t *testing.T) {
	run := assessment.Run{ID: 2, Status: assessment.RunComplete, RequestedContexts: 1, SuccessfulContexts: 1}
	var out strings.Builder
	printRun(&out, run, "datastore browse denied on 3/3 datastores")
	if got, want := out.String(), "assessment 2: complete (1/1 contexts successful, datastore browse denied on 3/3 datastores)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	out.Reset()
	printRun(&out, run, "")
	if got, want := out.String(), "assessment 2: complete (1/1 contexts successful)\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
