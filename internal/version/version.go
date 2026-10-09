// Package version carries the build identity, stamped by the release build.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the release version, overridden at link time.
var Version = "dev"

// Commit is the source revision, overridden at link time.
var Commit = ""

// Date is the build date, overridden at link time.
var Date = ""

// String renders the version for --version and the vCenter user agent.
func String() string {
	v := Version
	if c := commit(); c != "" {
		v += " (" + c + ")"
	}
	return v
}

// UserAgent identifies vsfleet to vCenter. Operators reading vCenter's session
// list should be able to tell what connected.
func UserAgent() string { return "vsfleet/" + Version }

// Release names this build for deciding whether the operator has seen it
// before: the version a release build stamped, else the tag "go install
// ...@v1.2.3" recorded in the module, else "dev". A pseudo-version or a dirty
// local build is "dev" too, so rebuilding from a checkout is never mistaken
// for an upgrade.
func Release() string {
	if Version != "dev" {
		return strings.TrimPrefix(Version, "v")
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	return moduleRelease(info.Main.Version)
}

// moduleRelease accepts only a clean tag such as "v1.2.3": "(devel)", a
// pseudo-version (which carries a "-") and a "+dirty" build all mean the
// binary came from a checkout rather than a release.
func moduleRelease(v string) string {
	if !strings.HasPrefix(v, "v") || strings.ContainsAny(v, "-+") {
		return "dev"
	}
	return strings.TrimPrefix(v, "v")
}

func commit() string {
	if Commit != "" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 7 {
			return s.Value[:7]
		}
	}
	return ""
}
