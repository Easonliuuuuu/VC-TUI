package main

import (
	"context"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/tui"
)

func TestSetupDemoWelcomeHonorsOptOut(t *testing.T) {
	for _, disabledBy := range []string{"", "CI", "VSFLEET_NO_WELCOME"} {
		t.Run(disabledBy, func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("VSFLEET_NO_WELCOME", "")
			want := tui.WelcomeDemo
			if disabledBy != "" {
				t.Setenv(disabledBy, "1")
				want = tui.WelcomeNone
			}
			_, opts, cleanup, err := setupDemo()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			if opts.Welcome.Kind != want {
				t.Errorf("welcome=%v, want %v", opts.Welcome.Kind, want)
			}
		})
	}
}

// setupDemo must wire the same seeded in-memory assessment service as
// "vsfleet demo" so History works in the standalone presentation binary.
func TestSetupDemoWiresSeededAssessmentHistory(t *testing.T) {
	backend, opts, cleanup, err := setupDemo()
	if err != nil {
		t.Fatalf("setupDemo: %v", err)
	}
	if cleanup == nil {
		t.Fatal("setupDemo returned nil cleanup")
	}
	defer cleanup()
	if backend == nil {
		t.Fatal("setupDemo returned nil backend")
	}
	if !opts.Demo {
		t.Error("standalone demo options must set Demo")
	}
	if opts.Current != "prod-vc" {
		t.Errorf("standalone demo Current=%q, want %q", opts.Current, "prod-vc")
	}
	if opts.Assessment == nil || opts.Assessment.Store == nil {
		t.Fatal("standalone demo options must include the seeded assessment service")
	}

	ctx := context.Background()
	runID, err := opts.Assessment.Store.ResolveRun(ctx, "latest")
	if err != nil {
		t.Fatalf("resolving seeded demo run: %v", err)
	}
	data, err := opts.Assessment.Store.LoadExportData(ctx, runID)
	if err != nil {
		t.Fatalf("loading seeded demo run: %v", err)
	}
	foundProd := false
	for _, cr := range data.Contexts {
		if cr.Name == "prod-vc" {
			foundProd = true
		}
	}
	if !foundProd {
		t.Fatalf("seeded demo run is missing prod-vc, contexts=%+v", data.Contexts)
	}
}
