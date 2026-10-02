package metareport_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/metareport"
	"github.com/easonliuuuuu/vsfleet/internal/metareport/metareporttest"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func seeded(t *testing.T) (assessment.ExportData, assessment.ExportData) {
	t.Helper()
	store, err := assessment.Open(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	baseID, laterID := metareporttest.Seed(t, store)
	base, err := store.LoadExportData(context.Background(), baseID)
	if err != nil {
		t.Fatal(err)
	}
	later, err := store.LoadExportData(context.Background(), laterID)
	if err != nil {
		t.Fatal(err)
	}
	return base, later
}

func pciReport(t *testing.T) metareport.Compiled {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pci.toml")
	if err := os.WriteFile(path, []byte(metareporttest.PCIReport), 0o600); err != nil {
		t.Fatal(err)
	}
	def, err := metareport.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := def.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

// The schema is the same for both vCenters and both captures even though
// they use different tag categories and attributes, and same-named objects
// with the same managed object ID stay distinct by vCenter identity.
func TestRowsKeepAStableSchemaAndObjectIdentity(t *testing.T) {
	base, later := seeded(t)
	var headers []string
	for _, data := range []assessment.ExportData{base, later} {
		b, err := metareport.RowsCSV(metareport.Rows(metareport.Objects(data), nil))
		if err != nil {
			t.Fatal(err)
		}
		headers = append(headers, strings.SplitN(string(b), "\n", 2)[0])
	}
	if headers[0] != headers[1] || headers[0] != strings.Join(metareport.RowColumns, ",") {
		t.Fatalf("headers differ: %q", headers)
	}

	rows := metareport.Rows(metareport.Objects(base), nil)
	webs := map[string]bool{}
	for _, r := range rows {
		if r.ObjectName == "web" {
			webs[r.Context+"|"+r.VCenterID+"|"+r.ObjectID] = true
		}
	}
	if len(webs) != 2 {
		t.Fatalf("duplicate-named VMs collapsed: %v", webs)
	}
	var owners []string
	for _, r := range rows {
		if r.Source == metareport.SourceCustom && r.Field == "owner" {
			owners = append(owners, r.Context+":"+r.FieldID+"="+r.Value)
		}
	}
	want := "synthetic-east:1=synthetic-alice,synthetic-east:1=synthetic-bob,synthetic-west:7=synthetic-carol,synthetic-west:7=synthetic-dave"
	if strings.Join(owners, ",") != want {
		t.Fatalf("owners=%v", owners)
	}
}

// An object whose source was read but holds nothing still has a row, and
// one whose source was denied has a row naming that, never no row at all.
func TestRowsDistinguishVerifiedNoneFromUnreadable(t *testing.T) {
	base, _ := seeded(t)
	var hostTags, deniedTags []metareport.Row
	for _, r := range metareport.Rows(metareport.Objects(base), nil) {
		if r.Kind == "host" && r.Source == metareport.SourceCustom {
			hostTags = append(hostTags, r)
		}
		if r.ObjectName == "batch" && r.Source == metareport.SourceTag {
			deniedTags = append(deniedTags, r)
		}
	}
	if len(hostTags) != 1 || hostTags[0].Status != vsphere.MetadataAvailable || hostTags[0].Field != "" {
		t.Fatalf("verified-none row=%+v", hostTags)
	}
	if len(deniedTags) != 1 || deniedTags[0].Status != vsphere.MetadataDenied || deniedTags[0].Error == "" {
		t.Fatalf("denied row=%+v", deniedTags)
	}
}

// Old captures carry no status; they must read as not recorded.
func TestObjectsReportMissingStatusAsNotRecorded(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{"id": "host-1", "name": "old"})
	data := assessment.ExportData{Resources: []assessment.ResourceObservation{{Context: "c", Kind: "host", ID: "host-1", Name: "old", Payload: payload}}}
	objects := metareport.Objects(data)
	if len(objects) != 1 || objects[0].Metadata.TagsStatus != vsphere.MetadataNotRecorded {
		t.Fatalf("objects=%+v", objects)
	}
}

func TestSavedReportIsDeterministicAndReportsUndetermined(t *testing.T) {
	base, _ := seeded(t)
	report := pciReport(t)
	first, err := json.Marshal(report.Evaluate(base))
	if err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(report.Evaluate(base))
	if !bytes.Equal(first, second) {
		t.Fatal("same report on the same assessment produced different output")
	}
	r := report.Evaluate(base)
	if r.RunID != base.Run.ID || r.CapturedAt.IsZero() {
		t.Fatalf("run identity missing: %+v", r)
	}
	if len(r.Members) != 1 || r.Members[0].Context != metareporttest.East || r.Members[0].ID != "vm-1" {
		t.Fatalf("members=%+v", r.Members)
	}
	if got := strings.Join(r.Members[0].Values, "|"); got != "synthetic-east|"+metareporttest.EastVC+"|web|vm-1|PCI|synthetic-alice" {
		t.Fatalf("values=%s", got)
	}
	// vm-9's tags were denied: it may be PCI, so it is neither a member nor
	// silently excluded, and the report is incomplete.
	if len(r.Undetermined) != 1 || r.Undetermined[0].ID != "vm-9" || r.Undetermined[0].Unreadable[0] != metareport.SourceTag {
		t.Fatalf("undetermined=%+v", r.Undetermined)
	}
	if r.Complete {
		t.Fatal("report with denied tags claimed completeness")
	}
	if !hasWarning(r.Warnings, "denied") {
		t.Fatalf("no denial warning: %v", r.Warnings)
	}
}

// A negative predicate on a missing source is the case most likely to look
// complete when it is not.
func TestNegativePredicateOnUnreadableSourceIsUndetermined(t *testing.T) {
	_, later := seeded(t)
	def := metareport.Definition{Name: "not-pci", Kinds: []string{"vm"}, Where: []string{"tag.Compliance!=PCI"}}
	compiled, err := def.Compile()
	if err != nil {
		t.Fatal(err)
	}
	r := compiled.Evaluate(later)
	if len(r.Members) != 1 || r.Members[0].ID != "vm-1" || r.Members[0].Context != metareporttest.East {
		t.Fatalf("members=%+v", r.Members)
	}
	if len(r.Undetermined) != 2 || r.Complete {
		t.Fatalf("west VMs with unavailable tags must be undetermined: %+v complete=%v", r.Undetermined, r.Complete)
	}
}

func TestReportComparisonShowsMembershipChangesExplicitly(t *testing.T) {
	base, later := seeded(t)
	report := pciReport(t)
	c := metareport.Compare(report.Evaluate(base), report.Evaluate(later))
	var got []string
	for _, ch := range c.Changes {
		got = append(got, ch.Change+":"+ch.Context+"/"+ch.ID+":"+ch.Before+"->"+ch.After)
	}
	want := []string{
		"added:synthetic-east/vm-2:not_member->member",
		"removed:synthetic-east/vm-1:member->not_member",
		"unknown:synthetic-west/vm-1:not_member->undetermined",
		"unknown:synthetic-west/vm-9:undetermined->undetermined",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("changes:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if c.Complete || c.BaseRunID != base.Run.ID || c.TargetRunID != later.Run.ID {
		t.Fatalf("comparison=%+v", c)
	}
}

// An object that vanished with a failed collection was not removed.
func TestComparisonDoesNotReportAFailedCollectionAsRemoval(t *testing.T) {
	base, later := seeded(t)
	def := metareport.Definition{Name: "prod-hosts", Kinds: []string{"host"}, Where: []string{"tag.Environment=Production"}}
	compiled, err := def.Compile()
	if err != nil {
		t.Fatal(err)
	}
	target := compiled.Evaluate(later)
	if target.Complete || !hasWarning(target.Warnings, "collection failed") {
		t.Fatalf("failed host collection not reported: %+v", target.Warnings)
	}
	c := metareport.Compare(compiled.Evaluate(base), target)
	if len(c.Changes) != 1 || c.Changes[0].Change != metareport.ChangeUnknown || c.Changes[0].After != "not_observed" {
		t.Fatalf("changes=%+v", c.Changes)
	}
}

func TestReportContextScopeWarnsAboutMissingContext(t *testing.T) {
	base, _ := seeded(t)
	def := metareport.Definition{Name: "east", Contexts: []string{metareporttest.East, "synthetic-gone"}, Kinds: []string{"vm"}}
	compiled, err := def.Compile()
	if err != nil {
		t.Fatal(err)
	}
	r := compiled.Evaluate(base)
	if len(r.Members) != 2 || r.Complete || !hasWarning(r.Warnings, "synthetic-gone") {
		t.Fatalf("result=%+v", r)
	}
	// Selecting by vCenter ID is equivalent to selecting by name.
	def.Contexts = []string{metareporttest.EastVC}
	compiled, _ = def.Compile()
	if r := compiled.Evaluate(base); len(r.Members) != 2 || !r.Complete {
		t.Fatalf("by vCenter ID: %+v", r)
	}
}

func TestDefinitionValidation(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"typo.toml":   "name = \"x\"\nwehre = [\"tag=PCI\"]\n",
		"column.toml": "name = \"x\"\ncolumns = [\"nonsense\"]\n",
		"kind.toml":   "name = \"x\"\nkinds = [\"vapp\"]\n",
		"where.toml":  "name = \"x\"\nwhere = [\"cpu>abc\"]\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := metareport.Load(path); err == nil {
			t.Errorf("%s: accepted an invalid definition", name)
		}
	}
	reports := filepath.Join(dir, "reports")
	if err := os.MkdirAll(reports, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reports, "pci.toml"), []byte(metareporttest.PCIReport), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, err := metareport.Resolve("pci", reports); err != nil || filepath.Base(path) != "pci.toml" {
		t.Fatalf("resolve by name: %s %v", path, err)
	}
	if _, err := metareport.Resolve("missing", reports); err == nil {
		t.Fatal("resolved a missing report")
	}
}

func hasWarning(warnings []string, fragment string) bool {
	for _, w := range warnings {
		if strings.Contains(w, fragment) {
			return true
		}
	}
	return false
}
