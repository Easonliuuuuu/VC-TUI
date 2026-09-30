//go:build integration

package tests

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
)

func decodePerfWindows(t *testing.T, raw string) map[string]perf.Window {
	t.Helper()
	var windows []perf.Window
	if err := json.Unmarshal([]byte(raw), &windows); err != nil {
		t.Fatalf("decode performance windows: %v\n%s", err, raw)
	}
	byContext := make(map[string]perf.Window, len(windows))
	for _, w := range windows {
		if _, dup := byContext[w.Context]; dup {
			t.Fatalf("context %s has two windows in one listing: %s", w.Context, raw)
		}
		byContext[w.Context] = w
	}
	return byContext
}

// TestVCSIMPerfCollectRecordsProvenanceAndFailures collects historical
// performance from both external endpoints, reads it back through perf list
// and perf show, then takes one endpoint down and collects again. vcsim
// returns randomised sample values, so only deterministic facts are asserted:
// which contexts were attempted, how many VMs were requested and sampled, the
// interval and sample count, and the provenance and status of every window. A
// dead endpoint must be stored and shown as a failed window with its error and
// must not be reported as a clean collection.
func TestVCSIMPerfCollectRecordsProvenanceAndFailures(t *testing.T) {
	fixture := fixtureBasicMultivcenter(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	historyDB := filepath.Join(t.TempDir(), "history.db")
	perfArgs := func(args ...string) []string {
		return append([]string{"--history-db", historyDB, "--all-contexts", "-o", "json", "assessment", "perf"}, args...)
	}

	// Requested VMs follow the inventory shape asserted in
	// TestVCSIMMultiContextInventoryAndProvenance: 12 in vc-prod, 2 in vc-edge.
	wantVMs := map[string]int{"vc-prod": 12, "vc-edge": 2}
	collected := decodePerfWindows(t, vcsimJSON(t, r, perfArgs("collect", "--window", "1h")...))
	if len(collected) != 2 {
		t.Fatalf("collected windows for %d contexts, want vc-prod and vc-edge: %+v", len(collected), collected)
	}
	byID := map[int64]perf.Window{}
	for name, want := range wantVMs {
		w, ok := collected[name]
		if !ok {
			t.Fatalf("collect did not attempt context %s: %+v", name, collected)
		}
		if w.Status != perf.WindowSuccess || w.Error != "" || w.ID == 0 {
			t.Fatalf("%s window status=%q error=%q id=%d, want a stored successful window", name, w.Status, w.Error, w.ID)
		}
		if w.VMsRequested != want || w.VMsSampled != want || len(w.VMs) != want {
			t.Fatalf("%s window requested=%d sampled=%d vms=%d, want %d of each", name, w.VMsRequested, w.VMsSampled, len(w.VMs), want)
		}
		if w.IntervalSeconds != 300 || w.ExpectedSamples != 12 || w.RequestsUsed == 0 {
			t.Fatalf("%s window interval=%ds expected=%d requests=%d, want a 1h window at 300s intervals (12 samples)", name, w.IntervalSeconds, w.ExpectedSamples, w.RequestsUsed)
		}
		endpoint := fixture.Endpoints[name]
		if w.Endpoint != endpoint.URL || w.VCenterID == "" || !strings.Contains(w.Source, "QueryPerf") {
			t.Fatalf("%s provenance endpoint=%q (want %q) vcenter=%q source=%q", name, w.Endpoint, endpoint.URL, w.VCenterID, w.Source)
		}
		byID[w.ID] = w
	}
	if collected["vc-prod"].VCenterID == collected["vc-edge"].VCenterID || collected["vc-prod"].ID == collected["vc-edge"].ID {
		t.Fatalf("contexts share identity: %+v", collected)
	}

	listed := decodePerfWindows(t, vcsimJSON(t, r, perfArgs("list")...))
	for name, w := range collected {
		got, ok := listed[name]
		if !ok || got.ID != w.ID || got.Status != perf.WindowSuccess || got.Endpoint != w.Endpoint || got.VCenterID != w.VCenterID || got.Source != w.Source || got.VMsSampled != w.VMsSampled {
			t.Fatalf("perf list entry for %s = %+v (present=%v), want the stored window %+v", name, got, ok, w)
		}
	}
	for name, w := range collected {
		var shown perf.Window
		raw := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "perf", "show", strconv.FormatInt(w.ID, 10))
		if err := json.Unmarshal([]byte(raw), &shown); err != nil {
			t.Fatalf("decode perf show %d: %v\n%s", w.ID, err, raw)
		}
		if shown.Context != name || shown.Status != perf.WindowSuccess || len(shown.VMs) != wantVMs[name] || shown.Endpoint != w.Endpoint {
			t.Fatalf("perf show %d context=%s status=%s vms=%d, want %s with %d VMs: %s", w.ID, shown.Context, shown.Status, len(shown.VMs), name, wantVMs[name], raw)
		}
		for _, vm := range shown.VMs {
			if !vm.Sampled || vm.Name == "" || vm.Signal == "" {
				t.Fatalf("%s VM %+v lacks a sampled result and signal", name, vm)
			}
		}
	}
	var latest perf.Window
	if err := json.Unmarshal([]byte(vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "perf", "show", "latest")), &latest); err != nil {
		t.Fatal(err)
	}
	if _, ok := byID[latest.ID]; !ok || latest.Status != perf.WindowSuccess {
		t.Fatalf("perf show latest=%d %s, want one of the stored successful windows %v", latest.ID, latest.Status, byID)
	}

	// Take vc-edge down. The other context still collects, so the command
	// succeeds overall, and the loss must be visible in JSON and text output.
	fixture.Endpoints["vc-edge"].Kill()
	after := decodePerfWindows(t, vcsimJSON(t, r, perfArgs("collect", "--window", "1h")...))
	prod, edge := after["vc-prod"], after["vc-edge"]
	if prod.Status != perf.WindowSuccess || prod.VMsSampled != wantVMs["vc-prod"] {
		t.Fatalf("healthy vc-prod window after the loss=%+v, want an unaffected success", prod)
	}
	if edge.Status != perf.WindowFailed || edge.Error == "" || edge.VMsSampled != 0 || len(edge.VMs) != 0 || edge.ID == 0 || edge.Endpoint != fixture.Endpoints["vc-edge"].URL {
		t.Fatalf("lost vc-edge window=%+v, want a stored failed window with an error, no sampled VMs and its endpoint", edge)
	}
	if edge.ID == collected["vc-edge"].ID {
		t.Fatalf("failed attempt overwrote the earlier successful vc-edge window %d", edge.ID)
	}
	listedRaw := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "perf", "list")
	var history []perf.Window
	if err := json.Unmarshal([]byte(listedRaw), &history); err != nil {
		t.Fatal(err)
	}
	statuses := map[string]map[string]int{"vc-prod": {}, "vc-edge": {}}
	for _, w := range history {
		statuses[w.Context][w.Status]++
	}
	if statuses["vc-prod"][perf.WindowSuccess] != 2 || statuses["vc-edge"][perf.WindowSuccess] != 1 || statuses["vc-edge"][perf.WindowFailed] != 1 {
		t.Fatalf("perf list statuses=%v, want vc-prod 2 success and vc-edge 1 success plus 1 failed: %s", statuses, listedRaw)
	}

	stdout, stderr, err := r.run("", "--history-db", historyDB, "--all-contexts", "assessment", "perf", "collect", "--window", "1h")
	if err != nil {
		t.Fatalf("text collect with one healthy context: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var edgeLine, prodLine string
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.Contains(line, "vc-edge: window"):
			edgeLine = line
		case strings.Contains(line, "vc-prod: window"):
			prodLine = line
		}
	}
	if !strings.Contains(edgeLine, "failed") || strings.HasPrefix(strings.TrimSpace(edgeLine), "ok") || !strings.Contains(stdout, "error:") {
		t.Fatalf("text output does not show the vc-edge failure (line %q):\n%s", edgeLine, stdout)
	}
	if !strings.Contains(prodLine, "success") || strings.Contains(prodLine, "failed") {
		t.Fatalf("text output line for vc-prod=%q:\n%s", prodLine, stdout)
	}

	// With every endpoint down the command must fail in both output modes, and
	// JSON callers still receive the failed windows on stdout.
	fixture.Endpoints["vc-prod"].Kill()
	stdout, stderr, err = r.run("", perfArgs("collect", "--window", "1h")...)
	if err == nil || !strings.Contains(err.Error()+stderr, "failed for every context") {
		t.Fatalf("JSON collect with no healthy context err=%v, want a failure\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	for name, w := range decodePerfWindows(t, stdout) {
		if w.Status != perf.WindowFailed || w.Error == "" {
			t.Fatalf("%s window with no healthy context=%+v, want failed with an error", name, w)
		}
	}
	if _, stderr, err = r.run("", "--history-db", historyDB, "--all-contexts", "assessment", "perf", "collect", "--window", "1h"); err == nil {
		t.Fatalf("text collect with no healthy context succeeded\nstderr:\n%s", stderr)
	}
}
