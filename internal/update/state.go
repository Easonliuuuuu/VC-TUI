package update

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/uistate"
)

// State is what the release check remembers between runs. It lives in
// update.json beside the interface's state.json, rather than inside it,
// because it is written from places state.json is not — a background check
// finishing mid-session, a command-line run — and two writers of one file
// would lose each other's changes.
type State struct {
	// CheckedAt is when GitHub was last asked, successfully or not.
	CheckedAt time.Time `json:"checked_at,omitzero"`
	// Latest is the newest release the last successful check found.
	Latest string `json:"latest,omitempty"`
	// Skipped is a release the operator chose never to be asked about again.
	Skipped string `json:"skipped,omitempty"`
	// SnoozedUntil keeps the launch prompt away after "not now".
	SnoozedUntil time.Time `json:"snoozed_until,omitzero"`
	// NotifiedAt is when the command-line notice was last printed, so it
	// appears at most once a day however many commands a script runs.
	NotifiedAt time.Time `json:"notified_at,omitzero"`
}

// DefaultPath is update.json in the same directory as the interface state,
// so VSFLEET_STATE moves both.
func DefaultPath() (string, error) {
	p, err := uistate.DefaultPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), "update.json"), nil
}

// Load reads the remembered state. A missing or unreadable file is the zero
// value, which simply means "never checked".
func Load(path string) State {
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}
	}
	if _, ok := parseRelease(s.Latest); !ok {
		s.Latest = ""
	}
	return s
}

// Modify re-reads the file, applies change and writes it back, so a writer
// only ever replaces the fields it means to.
func Modify(path string, change func(*State)) error {
	s := Load(path)
	change(&s)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update state: %w", err)
	}
	return uistate.WriteFileAtomic(path, b)
}

// Stale reports whether it is time to ask GitHub again.
func (s State) Stale(now time.Time) bool {
	return now.Sub(s.CheckedAt) >= CheckInterval
}

// Available is the release worth mentioning to someone running current: the
// newest one known, if it is newer and they have not skipped it. Empty
// otherwise.
func (s State) Available(current string) string {
	if s.Latest == "" || s.Latest == s.Skipped || !Newer(s.Latest, current) {
		return ""
	}
	return s.Latest
}

// ShouldPrompt reports whether the launch prompt is due.
func (s State) ShouldPrompt(current string, now time.Time) bool {
	return s.Available(current) != "" && !now.Before(s.SnoozedUntil)
}
