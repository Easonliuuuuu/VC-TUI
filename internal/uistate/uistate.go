// Package uistate remembers what the terminal interface was showing between
// runs — the context, the resource tab and the sort order — so that opening
// vsfleet a second time picks up where the last session left off.
//
// This is deliberately not part of internal/config. The configuration file
// describes what a vCenter is and how to reach it, and is meant to be
// written once and occasionally hand-edited; this file is scratch state a
// person never needs to look at, and losing it costs nothing more than
// landing on the default view once.
package uistate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

// EnvStatePath overrides the state file location, the same way
// config.EnvConfigPath overrides the configuration file.
const EnvStatePath = "VSFLEET_STATE"

// State is everything remembered between runs. Every field is optional: a
// zero value just means "nothing to restore", never an error.
type State struct {
	Context string `json:"context,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Sort    string `json:"sort,omitempty"`
	// SSHUsers holds the login typed into the TUI's SSH prompt for each
	// machine, keyed "<context>/<moref>". It lives here rather than in
	// config.toml because it is scratch state written by the program, not
	// something a person edits, and losing it costs one retyped user name.
	SSHUsers map[string]string `json:"ssh_users,omitempty"`
	// SSHIdentityFiles holds the private-key path selected for each machine,
	// keyed "<context>/<moref>". The file itself is never copied into state.
	SSHIdentityFiles map[string]string `json:"ssh_identity_files,omitempty"`
	// SSHDestinations holds the operator's chosen SSH destination for each
	// machine, keyed "<context>/<moref>" the same way. See SSHDestination.
	SSHDestinations map[string]SSHDestination `json:"ssh_destinations,omitempty"`
	// WelcomedVersion is the release the welcome animation last played for
	// (see version.Release). A different running release plays it once more,
	// as an update; an empty value means it has never played.
	WelcomedVersion string `json:"welcomed_version,omitempty"`
}

// SSHDestination is one machine's remembered SSH destination: either an
// OpenSSH alias that already describes a complete route on its own, or an
// override of the fallback route vsfleet would otherwise choose for the
// machine's own guest DNS name or IP address (see internal/config.SSHRoute
// for the estate-wide, deterministic form of that same choice). Exactly one
// of the two is meaningful per value, selected by Kind.
//
// This deliberately carries no credential, key material, proxy password, or
// command fragment — see Load, which drops any value it cannot prove is one
// of the shapes below, so a hand-edited or corrupted state.json can never
// smuggle an executable argument into a generated ssh(1) command line.
type SSHDestination struct {
	// Kind is "openssh_alias" or "route". Any other value — including one
	// left over from a future version of this program — is dropped by Load
	// rather than trusted.
	Kind string `json:"kind"`
	// Alias is the OpenSSH Host alias to connect through. Meaningful only
	// when Kind is "openssh_alias"; ignored otherwise. It is re-validated
	// against the machine's current guest IP before every use (see
	// DiscoverSSHDestinations) rather than trusted from state alone — a
	// stale alias is dropped and rediscovered, never used to silently
	// connect somewhere else.
	Alias string `json:"alias,omitempty"`
	// Route is the per-VM fallback route override: "openssh" (add none of
	// vsfleet's own ssh(1) arguments and let ~/.ssh/config decide),
	// "direct", "http" or "socks5". Meaningful only when Kind is "route".
	Route string `json:"route,omitempty"`
	// ProxyAddress is the proxy's own host:port. Meaningful only when Route
	// is "http" or "socks5"; it carries no credential, matching
	// config.SSHRoute's own unauthenticated-proxy-only rule.
	ProxyAddress string `json:"proxy_address,omitempty"`
}

// valid reports whether d is one of the shapes SSHDestination documents.
// validAlias and validProxyAddress are the exact predicates
// internal/tui/handoff.go and internal/config already apply to the same
// strings before they reach an ssh(1) argument list; Load reuses them so a
// state file can never carry something those callers would have refused.
func (d SSHDestination) valid(validAlias, validProxyAddress func(string) bool) bool {
	switch d.Kind {
	case "openssh_alias":
		return d.Alias != "" && validAlias(d.Alias) && d.Route == "" && d.ProxyAddress == ""
	case "route":
		switch d.Route {
		case "openssh", "direct":
			return d.Alias == "" && d.ProxyAddress == ""
		case "http", "socks5":
			return d.Alias == "" && validProxyAddress(d.ProxyAddress)
		default:
			return false
		}
	default:
		return false
	}
}

// DefaultPath returns the state file path, honouring VSFLEET_STATE and then the
// platform user configuration directory.
func DefaultPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvStatePath)); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "vsfleet", "state.json"), nil
}

// Load reads the remembered state. A missing file, or one that fails to
// parse, is not an error: both just mean starting from the defaults, which
// is exactly what a first run looks like anyway.
func Load(path string) State {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return State{}
		}
		path = p
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return State{}
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}
	}
	s.SSHDestinations = sanitizeSSHDestinations(s.SSHDestinations)
	return s
}

// sanitizeSSHDestinations drops any entry a corrupt or hand-edited state
// file might carry that Load must never hand back as trustworthy: an
// unknown Kind, an alias or proxy address that would not itself pass the
// same safety check applied just before it reaches an ssh(1) argument list.
// A destination this program never wrote is treated exactly like one it
// did not write at all — falling back only costs choosing it again.
func sanitizeSSHDestinations(in map[string]SSHDestination) map[string]SSHDestination {
	if len(in) == 0 {
		return nil
	}
	clean := make(map[string]SSHDestination, len(in))
	for k, d := range in {
		if d.valid(validSSHAlias, config.ValidSSHProxyAddress) {
			clean[k] = d
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// validSSHAlias mirrors the safety rule internal/tui/handoff.go's
// validSSHHost applies to every string that reaches an ssh(1) argument
// list: never empty, never a leading "-" (which ssh(1) reads as an option),
// never "@" (which would be read as a user), and no whitespace or control
// character.
func validSSHAlias(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsRune(s, '@') {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// Save writes the state atomically, mirroring how the configuration file is
// written: a temp file in the same directory, renamed into place, so a
// process killed mid-write never leaves a truncated file behind.
func Save(path string, s State) error {
	if path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		path = p
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode interface state: %w", err)
	}
	return WriteFileAtomic(path, b)
}

// WriteFileAtomic writes b to path through a temp file in the same
// directory renamed into place, creating the directory if needed. Other
// scratch state kept beside state.json (see internal/update) writes the same
// way, for the same reason.
func WriteFileAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename into place: %w", err)
	}
	tmpPath = ""
	return nil
}
