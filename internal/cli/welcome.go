package cli

import (
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/tui"
)

// EnvNoWelcome turns the once-per-version welcome animation off when set to
// anything but an empty string.
const EnvNoWelcome = "VSFLEET_NO_WELCOME"

// welcomeFor decides whether this run opens with the welcome animation:
// once on a machine that has never shown it, and once more for each new
// release after that. welcomed is the release it last played for (empty if
// never), current the running one, and configured whether any vCenter is set
// up — which tells a brand-new operator apart from one upgrading from a
// release that predates the welcome. It is skipped when asked and under CI,
// where nobody is watching.
func welcomeFor(welcomed, current string, configured bool, getenv func(string) string) tui.Welcome {
	if strings.TrimSpace(getenv(EnvNoWelcome)) != "" || strings.TrimSpace(getenv("CI")) != "" {
		return tui.Welcome{}
	}
	switch {
	case welcomed == current:
		return tui.Welcome{}
	case welcomed == "" && !configured:
		return tui.Welcome{Kind: tui.WelcomeFirstRun, Version: current}
	default:
		return tui.Welcome{Kind: tui.WelcomeUpdated, Version: current, Previous: welcomed}
	}
}
