package sshalias

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzDiscover parses arbitrary ~/.ssh/config text, plus one includable
// file, under deliberately small limits. Discovery must stay inside the
// limits, return only literal, unique aliases, and fail with nothing but
// ErrTruncated.
func FuzzDiscover(f *testing.F) {
	f.Add("Host db\n  HostName 10.0.0.1\n", "")
	f.Add("Host a b c d e\n  HostName 10.0.0.1\nHost f g\n  HostName=10.0.0.1\n", "")
	f.Add("Include extra.conf\nHost web\n  HostName 10.0.0.2\n", "Host db\n  HostName 10.0.0.1\n")
	f.Add("Include extra.conf\n", "Include extra.conf\nHost loop\n HostName 10.0.0.1\n")
	f.Add("Match host x\n  Include extra.conf\nHost *.lab ?x !y z\n  HostName 10.0.0.1 # comment\n", "")
	f.Add("Host \"quoted alias\"\n\tHostName ::ffff:10.0.0.1\n", "")
	target := netip.MustParseAddr("10.0.0.1")
	lim := Limits{MaxDepth: 3, MaxFiles: 4, MaxBytes: 4096, MaxCandidates: 3}
	f.Fuzz(func(t *testing.T, config, extra string) {
		if !hermeticIncludes(config) || !hermeticIncludes(extra) {
			t.Skip("include escapes the temporary home")
		}
		home := t.TempDir()
		sshDir := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(sshDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{"config": config, "extra.conf": extra} {
			if err := os.WriteFile(filepath.Join(sshDir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		aliases, err := Discover(home, target, lim)
		if err != nil && !errors.Is(err, ErrTruncated) {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(aliases) > lim.MaxCandidates {
			t.Fatalf("%d aliases exceed MaxCandidates %d", len(aliases), lim.MaxCandidates)
		}
		seen := map[string]bool{}
		for _, alias := range aliases {
			if alias == "" || strings.ContainsAny(alias, "*?!") {
				t.Fatalf("non-literal alias %q", alias)
			}
			if seen[alias] {
				t.Fatalf("alias %q returned twice", alias)
			}
			seen[alias] = true
		}
	})
}

// hermeticIncludes rejects Include patterns that could reach files outside
// the fuzz case's temporary home: absolute, home-relative, parent-relative,
// or token-expanded paths. Relative includes stay under ~/.ssh.
func hermeticIncludes(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		key, val, ok := splitDirective(line)
		if !ok || !strings.EqualFold(key, "include") {
			continue
		}
		for _, pattern := range splitFields(val) {
			pattern = strings.Trim(strings.TrimSpace(pattern), `"`)
			if filepath.IsAbs(pattern) || filepath.VolumeName(pattern) != "" ||
				strings.HasPrefix(pattern, "~") || strings.Contains(pattern, "..") ||
				strings.ContainsAny(pattern, `%$\`) || strings.HasPrefix(pattern, "/") {
				return false
			}
		}
	}
	return true
}
