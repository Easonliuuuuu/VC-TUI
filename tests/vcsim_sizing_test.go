//go:build integration

package tests

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/sizing"
)

func sizingJSON(t *testing.T, r *runner, historyDB string, args ...string) sizing.Result {
	t.Helper()
	full := append([]string{"--history-db", historyDB}, args...)
	stdout := vcsimJSON(t, r, full...)
	var result sizing.Result
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("decode sizing result: %v\n%s", err, stdout)
	}
	return result
}

func sizingDimension(t *testing.T, result sizing.Result, name string) sizing.Dimension {
	t.Helper()
	for _, d := range result.Dimensions {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("sizing result has no %q dimension: %+v", name, result.Dimensions)
	return sizing.Dimension{}
}

func sizingMeasure(t *testing.T, result sizing.Result, name string) float64 {
	t.Helper()
	for _, m := range result.Measures {
		if m.Name == name {
			return m.Value
		}
	}
	t.Fatalf("sizing result has no %q measure: %+v", name, result.Measures)
	return 0
}

// TestVCSIMSizingCompleteAndPartialRuns proves that offline destination sizing
// counts every context of a complete run, and that a partial run whose context
// went dark can never look better than the estate really is: the blind context
// is named as a coverage gap, every compute and storage dimension is unknown
// instead of fit, and a scenario that the partial demand alone would satisfy
// is still not reported as fit.
func TestVCSIMSizingCompleteAndPartialRuns(t *testing.T) {
	fixture := fixtureBasicMultivcenter(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	historyDB := filepath.Join(t.TempDir(), "history.db")
	complete, _ := captureVCSIM(t, r, historyDB, false)
	if complete.Status != assessment.RunComplete || complete.SuccessfulContexts != 2 {
		t.Fatalf("baseline run=%+v, want complete two-context run", complete)
	}
	completeID := strconv.FormatInt(complete.ID, 10)

	// Every generated VM has one vCPU, so vCPU demand equals VM count: 12 in
	// vc-prod (two datacenters x three pool VMs and three vApp VMs) plus 2 in
	// vc-edge. The target is generous enough to fit either subset.
	roomy := []string{"-o", "json", "assessment", "sizing", completeID, "--hosts", "4", "--host-cores", "32", "--host-ram-gib", "512", "--datastore-capacity", "40TiB"}
	all := sizingJSON(t, r, historyDB, roomy...)
	if all.Verdict != sizing.VerdictFit || all.RunID != complete.ID || all.RunStatus != string(assessment.RunComplete) || len(all.Coverage) != 0 {
		t.Fatalf("complete run verdict=%s status=%s coverage=%v, want fit on a complete run with no gaps: %+v", all.Verdict, all.RunStatus, all.Coverage, all)
	}
	if strings.Join(all.Scope.Contexts, ",") != "vc-edge,vc-prod" || all.Scope.VMsInScope != 14 {
		t.Fatalf("complete run scope=%+v, want vc-edge and vc-prod with 14 VMs", all.Scope)
	}
	if got := sizingMeasure(t, all, "vcpu-allocated"); got != 14 {
		t.Fatalf("complete run vCPU demand=%v, want 14", got)
	}
	aliases := map[string]bool{}
	for _, group := range all.SourceStorage {
		for _, alias := range group.Aliases {
			aliases[alias] = true
		}
	}
	if !aliases["vc-prod/LocalDS_0"] || !aliases["vc-edge/LocalDS_0"] {
		t.Fatalf("source datastores %v, want storage from both contexts counted separately", aliases)
	}
	// Scoping to one context must give that context's share, so the two
	// contributions add up to the complete-run demand.
	var byContext float64
	for name, wantVMs := range map[string]int{"vc-prod": 12, "vc-edge": 2} {
		scoped := sizingJSON(t, r, historyDB, append([]string{"--context", name}, roomy...)...)
		if scoped.Scope.VMsInScope != wantVMs || strings.Join(scoped.Scope.Contexts, ",") != name {
			t.Fatalf("--context %s scope=%+v, want %d VMs from that context only", name, scoped.Scope, wantVMs)
		}
		byContext += sizingMeasure(t, scoped, "vcpu-allocated")
	}
	if byContext != 14 {
		t.Fatalf("per-context vCPU demand sums to %v, want 14", byContext)
	}

	// One usable host of 13 cores: the real 14 vCPU demand does not fit, but
	// the 12 vCPU that vc-prod alone contributes would.
	tight := []string{"--hosts", "2", "--host-cores", "13", "--host-ram-gib", "512", "--datastore-capacity", "40TiB"}
	full := sizingJSON(t, r, historyDB, append([]string{"-o", "json", "assessment", "sizing", completeID}, tight...)...)
	if cpu := sizingDimension(t, full, "cpu"); full.Verdict != sizing.VerdictInsufficient || cpu.Verdict != sizing.VerdictInsufficient {
		t.Fatalf("complete run on the tight target verdict=%s cpu=%s, want insufficient: %+v", full.Verdict, cpu.Verdict, full)
	}

	fixture.Endpoints["vc-edge"].Kill()
	partial, _ := captureVCSIM(t, r, historyDB, false)
	if partial.Status != assessment.RunPartial || partial.SuccessfulContexts != 1 {
		t.Fatalf("post-kill run=%+v, want partial with one successful context", partial)
	}
	partialID := strconv.FormatInt(partial.ID, 10)

	blindRoomy := sizingJSON(t, r, historyDB, "-o", "json", "assessment", "sizing", partialID, "--hosts", "4", "--host-cores", "32", "--host-ram-gib", "512", "--datastore-capacity", "40TiB")
	if blindRoomy.RunStatus != string(assessment.RunPartial) || blindRoomy.Scope.VMsInScope != 12 || sizingMeasure(t, blindRoomy, "vcpu-allocated") != 12 {
		t.Fatalf("partial run status=%s scope=%+v, want only the 12 VMs of vc-prod: %+v", blindRoomy.RunStatus, blindRoomy.Scope, blindRoomy)
	}
	if blindRoomy.Verdict != sizing.VerdictUnknown {
		t.Fatalf("partial run verdict=%s, want unknown even though the visible demand fits: %+v", blindRoomy.Verdict, blindRoomy)
	}
	for _, name := range []string{"cpu", "memory", "memory-reservation", "datastore-capacity"} {
		if d := sizingDimension(t, blindRoomy, name); d.Verdict != sizing.VerdictUnknown {
			t.Fatalf("partial run %s verdict=%s, want unknown: %+v", name, d.Verdict, d)
		}
	}
	gaps := strings.Join(blindRoomy.Coverage, "\n")
	if !strings.Contains(gaps, "vc-edge") || strings.Contains(gaps, "vc-prod") || !strings.Contains(gaps, "partial") {
		t.Fatalf("partial run coverage gaps=%q, want the partial status and vc-edge (and not the healthy vc-prod) named", gaps)
	}

	blindTight := sizingJSON(t, r, historyDB, append([]string{"-o", "json", "assessment", "sizing", partialID}, tight...)...)
	if cpu := sizingDimension(t, blindTight, "cpu"); blindTight.Verdict == sizing.VerdictFit || cpu.Verdict == sizing.VerdictFit {
		t.Fatalf("partial run on the tight target verdict=%s cpu=%s: the 12 visible vCPU fit 13 cores, but a blind context must never make the result look fit: %+v", blindTight.Verdict, cpu.Verdict, blindTight)
	}

	_, _, gateErr := r.run("", "--history-db", historyDB, "-o", "json", "assessment", "sizing", partialID, "--hosts", "4", "--host-cores", "32", "--host-ram-gib", "512", "--datastore-capacity", "40TiB", "--fail-unless-fit")
	if exitCode(gateErr) != 2 {
		t.Fatalf("--fail-unless-fit on a partial run exit=%d err=%v, want 2", exitCode(gateErr), gateErr)
	}
	if _, _, err := r.run("", "--history-db", historyDB, "-o", "json", "assessment", "sizing", completeID, "--hosts", "4", "--host-cores", "32", "--host-ram-gib", "512", "--datastore-capacity", "40TiB", "--fail-unless-fit"); err != nil {
		t.Fatalf("--fail-unless-fit on the complete run: %v", err)
	}
}
