//go:build integration

package tests

import (
	"context"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
)

func mutateVCSIMVM(t *testing.T, endpoint *simEndpoint, vmName string, powerOff bool, rename string) {
	t.Helper()
	ctx := context.Background()
	u, err := url.Parse(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/sdk"
	u.User = url.UserPassword(vcsimUsername, testPassword)
	client, err := govmomi.NewClient(ctx, u, true)
	if err != nil {
		t.Fatalf("connect to vcsim for mutation: %v", err)
	}
	defer client.Logout(ctx)
	finder := find.NewFinder(client.Client, true)
	dc, err := finder.Datacenter(ctx, "DC0")
	if err != nil {
		t.Fatalf("find datacenter: %v", err)
	}
	finder.SetDatacenter(dc)
	vm, err := finder.VirtualMachine(ctx, vmName)
	if err != nil {
		t.Fatalf("find VM %q: %v", vmName, err)
	}
	if powerOff {
		task, err := vm.PowerOff(ctx)
		if err != nil {
			t.Fatalf("power off %q: %v", vmName, err)
		}
		if err := task.Wait(ctx); err != nil {
			t.Fatalf("wait for power off %q: %v", vmName, err)
		}
	}
	if rename != "" {
		task, err := vm.Rename(ctx, rename)
		if err != nil {
			t.Fatalf("rename %q: %v", vmName, err)
		}
		if err := task.Wait(ctx); err != nil {
			t.Fatalf("wait for rename %q: %v", vmName, err)
		}
	}
}

func assertCapacityRunIDs(t *testing.T, raw string, want int) {
	t.Helper()
	var trend assessment.CapacityTrend
	if err := json.Unmarshal([]byte(raw), &trend); err != nil {
		t.Fatal(err)
	}
	if len(trend.Series) == 0 {
		t.Fatalf("capacity trend has no series: %s", raw)
	}
	for _, series := range trend.Series {
		if len(series.Points) != want {
			t.Fatalf("%s/%s/%s points=%d, want %d", series.Kind, series.Scope, series.Name, len(series.Points), want)
		}
		for i, point := range series.Points {
			if point.Run.ID == 0 {
				t.Fatalf("%s/%s/%s point %d lost run ID", series.Kind, series.Scope, series.Name, i)
			}
			if i > 0 && point.Run.ID <= series.Points[i-1].Run.ID {
				t.Fatalf("%s/%s/%s run IDs are not ordered: %+v", series.Kind, series.Scope, series.Name, series.Points)
			}
		}
	}
}

// TestVCSIMHistoryDiffVMHistoryAndCapacity proves that independent vcsim
// captures retain identity, mutations, context-scoped history and real run
// IDs in all capacity trend scopes (#117).
func TestVCSIMHistoryDiffVMHistoryAndCapacity(t *testing.T) {
	fixture := fixtureHistory(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	historyDB := filepath.Join(t.TempDir(), "history.db")
	first, _ := captureVCSIM(t, r, historyDB, false)
	mutateVCSIMVM(t, fixture.Endpoints["history"], "DC0_C0_RP0_VM0", true, "")
	second, _ := captureVCSIM(t, r, historyDB, false)
	mutateVCSIMVM(t, fixture.Endpoints["history"], "DC0_C0_RP0_VM1", false, "renamed-vm")
	third, _ := captureVCSIM(t, r, historyDB, false)
	if first.ID == second.ID || second.ID == third.ID {
		t.Fatalf("captures did not create distinct runs: %d, %d, %d", first.ID, second.ID, third.ID)
	}

	diffJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "diff", strconv.FormatInt(first.ID, 10), strconv.FormatInt(second.ID, 10), "--include-runtime")
	if !strings.Contains(diffJSON, "power_state") && !strings.Contains(diffJSON, "modified") {
		t.Fatalf("diff omitted power mutation: %s", diffJSON)
	}
	historyJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "vm", "history", "DC0_C0_RP0_VM0", "--all-observations", "--include-runtime")
	if !strings.Contains(historyJSON, strconv.FormatInt(first.ID, 10)) || !strings.Contains(historyJSON, strconv.FormatInt(second.ID, 10)) {
		t.Fatalf("VM history omitted captures: %s", historyJSON)
	}
	trendJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "trends", "capacity", "--kind", "all", "--limit", "0")
	assertCapacityRunIDs(t, trendJSON, 3)
	contextTrendJSON := vcsimJSON(t, r, "--history-db", historyDB, "--context", "history", "-o", "json", "assessment", "trends", "capacity", "--kind", "host", "--limit", "0")
	assertCapacityRunIDs(t, contextTrendJSON, 3)
	if !strings.Contains(contextTrendJSON, `"scope": "context"`) || !strings.Contains(contextTrendJSON, `"name": "history"`) {
		t.Fatalf("context-scoped capacity trend omitted context series: %s", contextTrendJSON)
	}
}

// TestVCSIMDiffTreatsLostContextAsUnknownNotRemoved proves that a context that
// answered in the baseline and disappeared before the target capture is
// reported as not covered rather than as deleted VMs: the diff carries a
// target-side coverage issue for it and no lifecycle change, the VM's history
// has no vanished event, the strict policy gate refuses the comparison, and
// the healthy context's VMs show no spurious change.
func TestVCSIMDiffTreatsLostContextAsUnknownNotRemoved(t *testing.T) {
	fixture := fixturePartialFailure(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	historyDB := filepath.Join(t.TempDir(), "history.db")
	first, _ := captureVCSIM(t, r, historyDB, false)
	if first.Status != assessment.RunComplete || first.SuccessfulContexts != 2 {
		t.Fatalf("baseline run=%+v, want complete two-context run", first)
	}
	fixture.Endpoints["killable"].Kill()
	second, _ := captureVCSIM(t, r, historyDB, false)
	if second.Status != assessment.RunPartial || second.SuccessfulContexts != 1 {
		t.Fatalf("target run=%+v, want partial with one successful context", second)
	}

	diffJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "diff", strconv.FormatInt(first.ID, 10), strconv.FormatInt(second.ID, 10), "--include-runtime")
	var diff assessment.Diff
	if err := json.Unmarshal([]byte(diffJSON), &diff); err != nil {
		t.Fatalf("decode diff: %v\n%s", err, diffJSON)
	}
	if diff.Base.ID != first.ID || diff.Target.ID != second.ID {
		t.Fatalf("diff compared runs %d..%d, want %d..%d: %s", diff.Base.ID, diff.Target.ID, first.ID, second.ID, diffJSON)
	}
	if diff.Counts != (assessment.DiffCounts{}) || len(diff.VMs) != 0 || len(diff.Resources) != 0 || len(diff.Snapshots) != 0 {
		t.Fatalf("diff reported changes although only coverage was lost (lost VMs must not read as removed, healthy VMs must not change): %s", diffJSON)
	}
	lost := 0
	for _, issue := range diff.Coverage {
		if issue.Context != "killable" || issue.Scope != "target" {
			t.Fatalf("coverage issue %+v, want only target-side issues for killable: %s", issue, diffJSON)
		}
		lost++
	}
	if lost == 0 {
		t.Fatalf("diff has no coverage issue for the lost context: %s", diffJSON)
	}
	notCollected := false
	for _, warning := range diff.Warnings {
		if strings.Contains(warning, "killable") && strings.Contains(warning, "not") && strings.Contains(warning, "target") {
			notCollected = true
		}
		if strings.Contains(warning, "healthy") {
			t.Fatalf("diff warned about the healthy context: %s", diffJSON)
		}
	}
	if !notCollected {
		t.Fatalf("diff warnings do not say the killable context was not collected in the target: %s", diffJSON)
	}

	_, _, policyErr := r.run("", "--history-db", historyDB, "-o", "json", "assessment", "diff", strconv.FormatInt(first.ID, 10), strconv.FormatInt(second.ID, 10), "--require-complete")
	if exitCode(policyErr) != 2 {
		t.Fatalf("--require-complete exit=%d err=%v, want 2 for an incomplete target", exitCode(policyErr), policyErr)
	}

	// Both contexts hold the same generated VM name, so every history query is
	// scoped to one context.
	historyEvents := func(contextName string) []assessment.VMHistoryEvent {
		raw := vcsimJSON(t, r, "--history-db", historyDB, "--context", contextName, "-o", "json", "vm", "history", "DC0_C0_RP0_VM0", "--all-observations", "--include-runtime")
		var events []assessment.VMHistoryEvent
		if err := json.Unmarshal([]byte(raw), &events); err != nil {
			t.Fatalf("decode %s VM history: %v\n%s", contextName, err, raw)
		}
		for _, event := range events {
			if event.Context != contextName {
				t.Fatalf("%s VM history leaked event %+v: %s", contextName, event, raw)
			}
			switch event.Kind {
			case "vanished", "removed", "deleted":
				t.Fatalf("%s VM history reports %q: a lost context is unknown, not removed: %s", contextName, event.Kind, raw)
			}
		}
		return events
	}
	lostEvents := historyEvents("killable")
	if len(lostEvents) != 1 || lostEvents[0].Kind != "first_seen" || lostEvents[0].Run.ID != first.ID {
		t.Fatalf("lost-context VM history=%+v, want only first_seen in run %d (no observation exists for the partial run)", lostEvents, first.ID)
	}
	healthyEvents := historyEvents("healthy")
	if len(healthyEvents) != 2 || healthyEvents[0].Kind != "first_seen" || healthyEvents[0].Run.ID != first.ID || healthyEvents[1].Kind != "observed" || healthyEvents[1].Run.ID != second.ID {
		t.Fatalf("healthy VM history=%+v, want first_seen then unchanged observed in the partial run", healthyEvents)
	}
}
