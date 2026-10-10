package update

import (
	"path"
	"strings"
)

// Method is how this copy of vsfleet was installed, and therefore what
// upgrades it.
type Method struct {
	// Name is how the operator knows the installer: "Homebrew", "Scoop".
	Name string
	// Command upgrades vsfleet, or is nil where there is no single command
	// (a .deb or .rpm, a release archive). It is always one of the fixed
	// commands below, chosen from where the binary lives — never anything
	// derived from the release check's answer.
	Command []string
	// Runs reports whether vsfleet may run Command itself. When false, Why
	// says why, and the operator is offered the command to copy instead.
	Runs bool
	Why  string
}

// CommandLine is Command as the operator would type it.
func (m Method) CommandLine() string { return strings.Join(m.Command, " ") }

// Env is what Detect needs to know about the machine, as a struct so tests
// can describe any platform from any other.
type Env struct {
	GOOS     string
	Home     string
	Getenv   func(string) string
	LookPath func(string) (string, error)
}

const (
	whyWindows = "Windows can't replace a vsfleet.exe that is running."
	whySudo    = "Installing it needs sudo, which vsfleet never runs."
	whyArchive = "There is no package manager to hand this to."
)

// Detect works out the install method from exe, the resolved path of the
// running binary. The packaging layouts it recognises are the ones
// .goreleaser.yaml produces: the Homebrew tap, the Scoop bucket, winget, the
// nfpm packages (which install /usr/bin/vsfleet), and "go install".
func Detect(exe string, env Env) Method {
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	windows := env.GOOS == "windows"
	has := func(name string) bool {
		if env.LookPath == nil {
			return false
		}
		_, err := env.LookPath(name)
		return err == nil
	}
	runnable := func(m Method, tool string) Method {
		switch {
		case windows:
			m.Why = whyWindows
		case !has(tool):
			m.Why = tool + " is not on your PATH."
		default:
			m.Runs = true
		}
		return m
	}

	switch {
	case strings.Contains(p, "/cellar/vsfleet/") || strings.Contains(p, "/homebrew/") || strings.Contains(p, "/linuxbrew/"):
		return runnable(Method{Name: "Homebrew", Command: []string{"brew", "upgrade", "vsfleet"}}, "brew")
	case strings.Contains(p, "/scoop/apps/vsfleet/") || strings.Contains(p, "/scoop/shims/"):
		return Method{Name: "Scoop", Command: []string{"scoop", "update", "vsfleet"}, Why: whyWindows}
	case strings.Contains(p, "/winget/packages/") || strings.Contains(p, "/winget/links/"):
		return Method{Name: "winget", Command: []string{"winget", "upgrade", "vsfleet"}, Why: whyWindows}
	case inGoBin(p, env):
		return runnable(Method{Name: "go install", Command: []string{"go", "install", "github.com/easonliuuuuu/vsfleet/cmd/vsfleet@latest"}}, "go")
	case p == "/usr/bin/vsfleet":
		return Method{Name: "package", Why: whySudo}
	default:
		return Method{Name: "release archive", Why: whyArchive}
	}
}

// inGoBin reports whether p sits directly in a directory "go install" writes
// to: $GOBIN, each $GOPATH entry's bin, or ~/go/bin when GOPATH is unset.
func inGoBin(p string, env Env) bool {
	getenv := env.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	norm := func(s string) string { return strings.ToLower(strings.TrimRight(strings.ReplaceAll(s, `\`, "/"), "/")) }
	dir := path.Dir(p)
	var dirs []string
	if b := getenv("GOBIN"); b != "" {
		dirs = append(dirs, b)
	}
	sep := ":"
	if env.GOOS == "windows" {
		sep = ";"
	}
	if gp := getenv("GOPATH"); gp != "" {
		for _, e := range strings.Split(gp, sep) {
			if e != "" {
				dirs = append(dirs, e+"/bin")
			}
		}
	} else if env.Home != "" {
		dirs = append(dirs, env.Home+"/go/bin")
	}
	for _, d := range dirs {
		if norm(d) == dir {
			return true
		}
	}
	return false
}
