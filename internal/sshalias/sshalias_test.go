package sshalias

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, home, rel, content string) {
	t.Helper()
	path := filepath.Join(home, ".ssh", rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse addr %q: %v", s, err)
	}
	return a
}

func TestDiscoverLiteralAlias(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
Host app-prod
    HostName 172.31.7.122
    User devops
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want [app-prod]", got)
	}
}

func TestDiscoverIncludeOfDirectoryIsSkipped(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", "Include conf.d\nHost app-prod\n  HostName 172.31.7.122\n")
	if err := os.MkdirAll(filepath.Join(home, ".ssh", "conf.d"), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want [app-prod]", got)
	}
}

func TestDiscoverBangOnlyNegatesAsPrefix(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", "Host 0! !neg\n  HostName 172.31.7.122\n")
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "0!" {
		t.Fatalf("got %v, want [0!]", got)
	}
}

func TestDiscoverNoMatch(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
Host app-prod
    HostName 172.31.7.122
`)
	got, err := Discover(home, mustAddr(t, "10.0.0.1"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want none", got)
	}
}

func TestDiscoverNoConfig(t *testing.T) {
	home := t.TempDir()
	got, err := Discover(home, mustAddr(t, "10.0.0.1"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestDiscoverFollowsInclude(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
Include conf.d/*.conf
`)
	writeConfig(t, home, "conf.d/app.conf", `
Host app-prod
    HostName 172.31.7.122
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want [app-prod]", got)
	}
}

func TestDiscoverNestedIncludeIsBoundedAndDeterministic(t *testing.T) {
	// A relative Include always resolves against ~/.ssh, per ssh_config(5) —
	// not against the file containing the directive — so a nested Include
	// one level down still reaches level2/*.conf directly.
	home := t.TempDir()
	writeConfig(t, home, "config", `
Include level1/*.conf
`)
	writeConfig(t, home, "level1/a.conf", `
Include level2/*.conf
`)
	writeConfig(t, home, "level2/b.conf", `
Host app-prod
    HostName 172.31.7.122
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want [app-prod]", got)
	}

	// Running it again is byte-for-byte the same: deterministic.
	got2, err2 := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err2 != nil {
		t.Fatalf("Discover (again): %v", err2)
	}
	if len(got) != len(got2) || got[0] != got2[0] {
		t.Fatalf("nondeterministic: %v vs %v", got, got2)
	}
}

func TestDiscoverDeduplicatesAliases(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
Host app-prod
    HostName 172.31.7.122

Include extra.conf
`)
	writeConfig(t, home, "extra.conf", `
Host app-prod
    HostName 172.31.7.122
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want deduplicated [app-prod]", got)
	}
}

func TestDiscoverConservativeCandidates(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
# wildcard-only pattern is never a candidate
Host *
    HostName 172.31.7.122

# negated pattern is skipped, its literal sibling is not
Host app-prod !app-prod-staging
    HostName 172.31.7.122

# dynamically constructed / tokenized HostName is never trusted
Host dynamic-host
    HostName 10.0.0.%d

Host tokenized
    HostName %h

# Match blocks are not simple Host aliases and their HostName is ignored
Match host app-match
    HostName 172.31.7.122
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want only [app-prod]", got)
	}
}

func TestDiscoverMultipleAliasesForOneIP(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
Host app-prod
    HostName 172.31.7.122

Host app-prod-via-bastion
    HostName 172.31.7.122
    ProxyJump bastion-prod
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	want := []string{"app-prod", "app-prod-via-bastion"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestDiscoverStaleAliasHasDifferentIP(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", `
Host app-prod
    HostName 172.31.7.99
`)
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want none (different IP)", got)
	}
}

func TestDiscoverBlockPersistsAcrossInclude(t *testing.T) {
	// An Include is spliced in place, so a Host block opened before it still
	// governs lines after it returns — exactly what ssh(1) itself does.
	home := t.TempDir()
	writeConfig(t, home, "config", `
Host app-prod
Include empty.conf
    HostName 172.31.7.122
`)
	writeConfig(t, home, "empty.conf", "# nothing here\n")
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), DefaultLimits())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(got) != 1 || got[0] != "app-prod" {
		t.Fatalf("got %v, want [app-prod]", got)
	}
}

func TestDiscoverMaxCandidatesIsBounded(t *testing.T) {
	home := t.TempDir()
	var b strings.Builder
	for i := 0; i < 20; i++ {
		b.WriteString("Host host")
		b.WriteString(strings.Repeat("x", i+1))
		b.WriteString("\n    HostName 172.31.7.122\n")
	}
	writeConfig(t, home, "config", b.String())
	lim := DefaultLimits()
	lim.MaxCandidates = 3
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), lim)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d candidates, want 3", len(got))
	}
}

func TestDiscoverMaxFilesIsBounded(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", "Include conf.d/*.conf\n")
	for i := 0; i < 10; i++ {
		writeConfig(t, home, filepath.Join("conf.d", strings.Repeat("f", i+1)+".conf"), `
Host nope
    HostName 10.0.0.1
`)
	}
	lim := DefaultLimits()
	lim.MaxFiles = 3
	_, err := Discover(home, mustAddr(t, "10.0.0.1"), lim)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
}

func TestDiscoverMaxDepthIsBounded(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", "Include d1/next.conf\n")
	writeConfig(t, home, "d1/next.conf", "Include d2/next.conf\n")
	writeConfig(t, home, "d2/next.conf", `
Host too-deep
    HostName 172.31.7.122
`)
	lim := DefaultLimits()
	lim.MaxDepth = 1
	got, err := Discover(home, mustAddr(t, "172.31.7.122"), lim)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("err = %v, want ErrTruncated", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want none (too deep)", got)
	}
}

func TestDiscoverIncludeCycleIsBounded(t *testing.T) {
	home := t.TempDir()
	writeConfig(t, home, "config", "Include a.conf\n")
	writeConfig(t, home, "a.conf", "Include b.conf\n")
	writeConfig(t, home, "b.conf", "Include a.conf\n")
	lim := DefaultLimits()
	lim.MaxFiles = 10
	done := make(chan struct{})
	go func() {
		Discover(home, mustAddr(t, "172.31.7.122"), lim)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Discover did not return: unbounded Include cycle")
	}
}
