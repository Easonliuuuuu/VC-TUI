package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// unloadedReason is why a vCenter has no inventory to search or list. It is
// the one wording shared by the estate search ("edge-vc not searched: …") and
// the all-vCenters table, so the two screens cannot describe the same vCenter
// differently.
func (s *contextState) unloadedReason() string {
	switch {
	case s.credentialsRequired():
		return "credentials required"
	case s.showsLoading():
		return "still loading"
	case s.err != nil:
		return firstLine(s.err.Error())
	default:
		return "not connected"
	}
}

// unloadedGroup is the vCenters in the all-vCenters table that have no
// inventory yet and are not failures, grouped by what is true of them.
// Failures are not here: they already get a line each, with the key that
// diagnoses them, from failuresInScope.
type unloadedGroup struct {
	reason string
	glyph  string
	warn   bool // warn-coloured: the operator has something to do
	names  []string
	// hints are the key suffixes to append, longest first.
	hints []string
}

// unloadedInScope lists the vCenters the all-vCenters table is not showing
// because nothing has loaded from them. It is empty in a single-vCenter scope,
// where the table's own empty message and the header say the same thing about
// the one vCenter on screen.
func (m *Model) unloadedInScope() []unloadedGroup {
	if !m.allScope {
		return nil
	}
	notConnected := unloadedGroup{reason: "not connected", glyph: glyphOffline, warn: true,
		hints: []string{"R connects all · c then enter connects one, d diagnoses", "R connects all · c picks one", "R connects all"}}
	connecting := unloadedGroup{reason: "connecting", glyph: glyphPending}
	credentials := unloadedGroup{reason: "credentials required", glyph: glyphPending, warn: true,
		hints: []string{"R asks for them · c then enter picks one", "R asks for them"}}
	for _, st := range m.states {
		if st.inv != nil {
			continue
		}
		switch {
		case st.credentialsRequired():
			credentials.names = append(credentials.names, st.cc.Name)
		case st.err != nil:
			// A failure: reported, with its diagnosis key, by failuresInScope.
		case st.showsLoading():
			connecting.names = append(connecting.names, st.cc.Name)
		default:
			notConnected.names = append(notConnected.names, st.cc.Name)
		}
	}
	var out []unloadedGroup
	for _, g := range []unloadedGroup{notConnected, connecting, credentials} {
		if len(g.names) > 0 {
			out = append(out, g)
		}
	}
	return out
}

// render fits the group on one line of w columns: "◐ reason: a, b, +N more ·
// keys". Names give way before the keys do, because the keys are the part the
// operator cannot work out for themselves.
func (g unloadedGroup) render(w int) string {
	lead := fmt.Sprintf("%s %s: ", g.glyph, g.reason)
	hints := g.hints
	if len(hints) == 0 {
		hints = []string{""}
	}
	line := func(hint string, shown int) string {
		names := strings.Join(g.names[:shown], ", ")
		if shown < len(g.names) {
			if shown > 0 {
				names += ", "
			}
			names += fmt.Sprintf("+%d more", len(g.names)-shown)
		}
		if hint == "" {
			return lead + names
		}
		return lead + names + " · " + hint
	}
	for _, hint := range hints {
		if s := line(hint, len(g.names)); ansi.StringWidth(s) <= w {
			return s
		}
	}
	// Not every name fits with any hint. Keep the second-longest hint, which
	// still says how to connect one vCenter, and as many names as fit beside it.
	hint := hints[min(1, len(hints)-1)]
	for shown := len(g.names) - 1; shown >= 0; shown-- {
		if s := line(hint, shown); ansi.StringWidth(s) <= w {
			return s
		}
	}
	return truncate(line(hints[len(hints)-1], 0), w)
}
