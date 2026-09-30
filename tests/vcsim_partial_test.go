//go:build integration

package tests

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
)

// TestVCSIMPartialCoverageSurvivesProcessLoss verifies healthy evidence,
// partial status, coverage diagnostics and exit-code policy after one
// external endpoint is terminated between captures.
func TestVCSIMPartialCoverageSurvivesProcessLoss(t *testing.T) {
	fixture := fixturePartialFailure(t)
	r := newRunner(t)
	addFixtureContexts(t, r, fixture)
	addUnreachableContext(r, "dead-port")
	historyDB := filepath.Join(t.TempDir(), "history.db")

	first, err := captureVCSIM(t, r, historyDB, false)
	if err != nil || first.Status != assessment.RunPartial {
		t.Fatalf("first partial capture run=%+v err=%v", first, err)
	}
	fixture.Endpoints["killable"].Kill()

	second, err := captureVCSIM(t, r, historyDB, false)
	if err != nil || second.Status != assessment.RunPartial || second.SuccessfulContexts != 1 {
		t.Fatalf("post-kill capture run=%+v err=%v", second, err)
	}

	reportJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "report", "latest")
	// The report carries one coverage row per context and collection kind, so a
	// context is judged by all of its rows rather than by a single status.
	var report struct {
		Run      assessment.Run `json:"run"`
		Coverage []struct {
			Context   string `json:"context"`
			Kind      string `json:"kind"`
			Status    string `json:"status"`
			ItemCount int    `json:"item_count"`
			Error     string `json:"error"`
		} `json:"coverage"`
	}
	if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
		t.Fatal(err)
	}
	if report.Run.Status != assessment.RunPartial {
		t.Fatalf("report status=%s, want partial", report.Run.Status)
	}
	rows := map[string]int{}
	for _, row := range report.Coverage {
		rows[row.Context]++
		switch row.Context {
		case "healthy":
			if row.Status != "success" && row.Status != "empty" || row.Error != "" {
				t.Fatalf("healthy %s coverage status=%q error=%q, want success or empty: %s", row.Kind, row.Status, row.Error, reportJSON)
			}
			if row.Kind == "vm" && (row.Status != "success" || row.ItemCount == 0) {
				t.Fatalf("healthy vm coverage=%+v, want inventory: %s", row, reportJSON)
			}
		case "killable", "dead-port":
			if row.Kind != "vm" || row.Status != "failed" || row.Error == "" || row.ItemCount != 0 {
				t.Fatalf("%s coverage=%+v, want a single failed vm row with an error and no items: %s", row.Context, row, reportJSON)
			}
		default:
			t.Fatalf("coverage names unexpected context %q: %s", row.Context, reportJSON)
		}
	}
	if rows["healthy"] == 0 || rows["killable"] != 1 || rows["dead-port"] != 1 {
		t.Fatalf("coverage rows per context=%v, want healthy evidence and one failed row each for killable and dead-port: %s", rows, reportJSON)
	}

	findingsJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "findings", "latest")
	var findings health.Report
	if err := json.Unmarshal([]byte(findingsJSON), &findings); err != nil {
		t.Fatal(err)
	}
	// killable and dead-port recorded no evidence and must be blind for every
	// rule that needs collected inventory. healthy is expected to be blind for
	// exactly one rule: vcsim serves no host multipath data, so
	// host-path-redundancy cannot be resolved even for a fully collected context.
	if want := []string{"dead-port", "healthy", "killable"}; !reflect.DeepEqual(findings.Coverage.BlindContexts, want) {
		t.Fatalf("findings blind contexts=%v, want %v: %s", findings.Coverage.BlindContexts, want, findingsJSON)
	}
	for _, rule := range findings.Rules {
		if len(rule.Blind) == 0 {
			continue
		}
		wantBlind := []string{"dead-port", "killable"}
		if rule.Rule == "host-path-redundancy" {
			wantBlind = []string{"dead-port", "healthy", "killable"}
		}
		if !reflect.DeepEqual(rule.Blind, wantBlind) {
			t.Fatalf("rule %s blind contexts=%v, want %v: %s", rule.Rule, rule.Blind, wantBlind, findingsJSON)
		}
	}
	if findings.Coverage.Contexts != 3 || findings.Coverage.CompleteContexts != 1 {
		t.Fatalf("findings coverage contexts=%d complete=%d, want 3 and 1: %s", findings.Coverage.Contexts, findings.Coverage.CompleteContexts, findingsJSON)
	}
	readinessJSON := vcsimJSON(t, r, "--history-db", historyDB, "-o", "json", "assessment", "readiness", "latest")
	var readiness health.ReadinessReport
	if err := json.Unmarshal([]byte(readinessJSON), &readiness); err != nil {
		t.Fatal(err)
	}
	if readiness.Verdict == "ready" || len(readiness.Unresolved) == 0 {
		t.Fatalf("partial readiness looked clean: %s", readinessJSON)
	}

	_, partialErr := captureVCSIM(t, r, historyDB, true)
	if exitCode(partialErr) != 3 {
		t.Fatalf("--fail-on-partial exit=%d err=%v", exitCode(partialErr), partialErr)
	}
}
