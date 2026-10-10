// Command vsfleet-demo runs the TUI against deterministic sample data. It is
// the launcher for the presentation profile of scripts/testbed. The released
// vsfleet binary reaches the same fixtures through "vsfleet demo".
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/easonliuuuuu/vsfleet/internal/demo"
	"github.com/easonliuuuuu/vsfleet/internal/tui"
)

func main() {
	// Ctrl-C belongs to Bubble Tea or an interactive handoff child; only
	// SIGTERM should cancel the outer application context.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()

	backend, opts, cleanup, err := setupDemo()
	if err != nil {
		fmt.Fprintf(os.Stderr, "vsfleet-demo: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()
	if scenario := os.Getenv("VSFLEET_TESTBED_SCENARIO"); scenario != "" {
		fmt.Fprintf(os.Stderr, "Scenario: %s\n", scenario)
	}

	_, err = tui.Run(ctx, backend, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vsfleet-demo: %v\n", err)
		os.Exit(1)
	}
}

// setupDemo builds the synthetic backend, the seeded in-memory assessment
// service, and the TUI options for the presentation binary. It mirrors the
// "vsfleet demo" wiring in internal/cli/demo.go so both launch paths expose
// the same History capability. The returned cleanup closes the store and
// must run even when tui.Run fails.
func setupDemo() (*demo.Backend, tui.Options, func(), error) {
	backend := demo.NewBackend()
	service, closeHistory, err := backend.AssessmentService()
	if err != nil {
		return nil, tui.Options{}, nil, err
	}
	opts := tui.Options{Current: "prod-vc", Demo: true, Assessment: service}
	if strings.TrimSpace(os.Getenv("VSFLEET_NO_WELCOME")) == "" && strings.TrimSpace(os.Getenv("CI")) == "" {
		opts.Welcome = tui.Welcome{Kind: tui.WelcomeDemo}
	}
	return backend, opts, closeHistory, nil
}
