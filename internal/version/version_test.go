package version

import "testing"

func TestReleasePrefersTheStampedVersion(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "0.10.0"
	if got := Release(); got != "0.10.0" {
		t.Errorf("Release() = %q, want the stamped 0.10.0", got)
	}
	Version = "v0.10.0"
	if got := Release(); got != "0.10.0" {
		t.Errorf("Release() = %q, want 0.10.0 without the v", got)
	}
}

func TestModuleReleaseOnlyTrustsACleanTag(t *testing.T) {
	cases := map[string]string{
		"v0.10.0":                            "0.10.0",
		"(devel)":                            "dev",
		"":                                   "dev",
		"v0.10.1-0.20261010120000-abcdef123": "dev",
		"v0.10.0+dirty":                      "dev",
	}
	for in, want := range cases {
		if got := moduleRelease(in); got != want {
			t.Errorf("moduleRelease(%q) = %q, want %q", in, got, want)
		}
	}
}
