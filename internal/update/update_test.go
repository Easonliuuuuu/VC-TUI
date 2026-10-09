package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		candidate, current string
		want               bool
	}{
		{"0.11.0", "0.10.0", true},
		{"0.10.1", "0.10.0", true},
		{"1.0.0", "0.99.99", true},
		{"0.10.0", "0.10.0", false},
		{"0.9.9", "0.10.0", false},
		{"v0.11.0", "0.10.0", true},
		{"0.11.0-rc.1", "0.10.0", false},
		{"0.11.0", "dev", false},
		{"; rm -rf /", "0.10.0", false},
	}
	for _, tc := range cases {
		if got := Newer(tc.candidate, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.candidate, tc.current, got, tc.want)
		}
	}
}

func TestEnabled(t *testing.T) {
	none := func(string) string { return "" }
	if !Enabled("0.10.0", none) {
		t.Error("a release build should check")
	}
	if Enabled("dev", none) {
		t.Error("a checkout build has nothing to upgrade to")
	}
	for _, k := range []string{EnvNoUpdateNotifier, "CI"} {
		if Enabled("0.10.0", func(name string) string {
			if name == k {
				return "1"
			}
			return ""
		}) {
			t.Errorf("%s should turn the check off", k)
		}
	}
}

func TestLatest(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
		ok     bool
	}{
		{"release", 200, `{"tag_name":"v0.11.0"}`, "0.11.0", true},
		{"prerelease", 200, `{"tag_name":"v0.11.0","prerelease":true}`, "", false},
		{"odd tag", 200, `{"tag_name":"nightly"}`, "", false},
		{"rate limited", 403, `{}`, "", false},
		{"garbage", 200, `not json`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ua string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ua = r.Header.Get("User-Agent")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			got, err := Latest(context.Background(), srv.Client(), srv.URL, "vsfleet/0.10.0")
			if (err == nil) != tc.ok || got != tc.want {
				t.Errorf("Latest = %q, %v; want %q, ok=%v", got, err, tc.want, tc.ok)
			}
			if ua != "vsfleet/0.10.0" {
				t.Errorf("User-Agent = %q, want vsfleet/0.10.0 and nothing else identifying", ua)
			}
		})
	}
}

func TestStateDecisions(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	s := State{CheckedAt: now.Add(-time.Hour), Latest: "0.11.0"}
	if s.Stale(now) {
		t.Error("an hour-old check is not stale")
	}
	if !s.Stale(now.Add(CheckInterval)) {
		t.Error("a day-old check is stale")
	}
	if got := s.Available("0.10.0"); got != "0.11.0" {
		t.Errorf("Available = %q, want 0.11.0", got)
	}
	if got := s.Available("0.11.0"); got != "" {
		t.Errorf("Available on the latest release = %q, want none", got)
	}
	if !s.ShouldPrompt("0.10.0", now) {
		t.Error("a known newer release should prompt")
	}
	s.SnoozedUntil = now.Add(SnoozeInterval)
	if s.ShouldPrompt("0.10.0", now) || !s.ShouldPrompt("0.10.0", now.Add(SnoozeInterval)) {
		t.Error("not now should hold the prompt for exactly the snooze interval")
	}
	s.Skipped = "0.11.0"
	if s.Available("0.10.0") != "" {
		t.Error("a skipped release should not be mentioned at all")
	}
}

func TestModifyKeepsOtherFieldsAndLoadDropsBadVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	if err := Modify(path, func(s *State) { s.Latest = "0.11.0"; s.Skipped = "0.10.5" }); err != nil {
		t.Fatal(err)
	}
	if err := Modify(path, func(s *State) { s.SnoozedUntil = time.Unix(1, 0) }); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got.Latest != "0.11.0" || got.Skipped != "0.10.5" || got.SnoozedUntil.IsZero() {
		t.Errorf("Modify lost a field: %+v", got)
	}
	if err := os.WriteFile(path, []byte(`{"latest":"$(curl evil)"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(path).Latest; got != "" {
		t.Errorf("Load kept a version that is not X.Y.Z: %q", got)
	}
}

func TestDetect(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/x", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	env := func(goos, home string, vars map[string]string, look func(string) (string, error)) Env {
		return Env{GOOS: goos, Home: home, Getenv: func(k string) string { return vars[k] }, LookPath: look}
	}
	cases := []struct {
		name    string
		exe     string
		env     Env
		method  string
		command string
		runs    bool
	}{
		{"homebrew on macOS", "/opt/homebrew/Cellar/vsfleet/0.10.0/bin/vsfleet", env("darwin", "/Users/op", nil, found), "Homebrew", "brew upgrade vsfleet", true},
		{"linuxbrew", "/home/linuxbrew/.linuxbrew/Cellar/vsfleet/0.10.0/bin/vsfleet", env("linux", "/home/op", nil, found), "Homebrew", "brew upgrade vsfleet", true},
		{"homebrew without brew on PATH", "/usr/local/Cellar/vsfleet/0.10.0/bin/vsfleet", env("darwin", "/Users/op", nil, missing), "Homebrew", "brew upgrade vsfleet", false},
		{"go install default GOPATH", "/home/op/go/bin/vsfleet", env("linux", "/home/op", nil, found), "go install", "go install github.com/easonliuuuuu/vsfleet/cmd/vsfleet@latest", true},
		{"go install GOBIN", "/opt/tools/vsfleet", env("linux", "/home/op", map[string]string{"GOBIN": "/opt/tools"}, found), "go install", "go install github.com/easonliuuuuu/vsfleet/cmd/vsfleet@latest", true},
		{"go install on Windows", `C:\Users\op\go\bin\vsfleet.exe`, env("windows", `C:\Users\op`, nil, found), "go install", "go install github.com/easonliuuuuu/vsfleet/cmd/vsfleet@latest", false},
		{"scoop", `C:\Users\op\scoop\apps\vsfleet\current\vsfleet.exe`, env("windows", `C:\Users\op`, nil, found), "Scoop", "scoop update vsfleet", false},
		{"winget", `C:\Users\op\AppData\Local\Microsoft\WinGet\Packages\vsfleet_x\vsfleet.exe`, env("windows", `C:\Users\op`, nil, found), "winget", "winget upgrade vsfleet", false},
		{"deb or rpm", "/usr/bin/vsfleet", env("linux", "/home/op", nil, found), "package", "", false},
		{"archive", "/home/op/bin/vsfleet", env("linux", "/home/op", nil, found), "release archive", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Detect(tc.exe, tc.env)
			if m.Name != tc.method || m.CommandLine() != tc.command || m.Runs != tc.runs {
				t.Errorf("Detect = %q %q runs=%v, want %q %q runs=%v", m.Name, m.CommandLine(), m.Runs, tc.method, tc.command, tc.runs)
			}
			if !m.Runs && m.Why == "" {
				t.Error("a method vsfleet will not run must say why")
			}
		})
	}
}
