package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/report"
)

// shareFlags are the opt-in flags of `assessment export` that select a named
// scoped sharing profile. Without --profile the ordinary export is unchanged.
type shareFlags struct {
	profile      string
	pseudonymize bool
	keyFile      string
	linkExports  bool
	preview      bool
}

func (f *shareFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.profile, "profile", "", "scoped sharing profile: "+strings.Join(report.ShareProfileNames(), " or ")+" (default: the ordinary RVTools-compatible export)")
	fl.BoolVar(&f.pseudonymize, "pseudonymize", false, "replace names, addresses, paths and identifiers with keyed deterministic tokens (requires --profile and --pseudonymize-key-file)")
	fl.StringVar(&f.keyFile, "pseudonymize-key-file", "", "file holding the pseudonymization secret (at least 16 bytes, mode 0600); use - to read it from standard input")
	fl.BoolVar(&f.linkExports, "link-exports", false, "make pseudonyms stable across exports of the same estate made with the same key (default: bound to this run)")
	fl.BoolVar(&f.preview, "preview", false, "print the contexts, worksheets, row counts and sensitive field classes a --profile export would contain, and write nothing")
}

func (f *shareFlags) enabled() bool { return f.profile != "" }

func (f *shareFlags) validate(format string) error {
	if !f.enabled() {
		if f.pseudonymize || f.keyFile != "" || f.linkExports || f.preview {
			return fmt.Errorf("--pseudonymize, --pseudonymize-key-file, --link-exports and --preview require --profile; the ordinary export is never transformed")
		}
		return nil
	}
	if format != "rvtools" {
		return fmt.Errorf("--profile writes an XLSX workbook; --format %s is not supported with a profile", format)
	}
	switch {
	case f.pseudonymize && !f.preview && f.keyFile == "":
		return fmt.Errorf("--pseudonymize needs --pseudonymize-key-file: pseudonyms are keyed, and the export refuses to run without one")
	case f.keyFile != "" && !f.pseudonymize:
		return fmt.Errorf("--pseudonymize-key-file has no effect without --pseudonymize")
	case f.linkExports && !f.pseudonymize:
		return fmt.Errorf("--link-exports only applies with --pseudonymize")
	}
	return nil
}

func (f *shareFlags) options(key []byte) report.ShareOptions {
	return report.ShareOptions{Profile: f.profile, Pseudonymize: f.pseudonymize, LinkExports: f.linkExports, Key: key}
}

// readShareKey reads the pseudonymization secret from a file, or standard
// input for "-". The key is never accepted as a flag value, which would put it
// in shell history and the process list. A key file readable by other users is
// refused.
func readShareKey(path string, stdin io.Reader) ([]byte, error) {
	var raw []byte
	if path == "-" {
		b, err := io.ReadAll(io.LimitReader(stdin, 1<<16))
		if err != nil {
			return nil, fmt.Errorf("read pseudonymization key from standard input: %w", err)
		}
		raw = b
	} else {
		path = filepath.Clean(path)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("pseudonymization key file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("pseudonymization key file %q is not a regular file", path)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("pseudonymization key file %q is accessible to other users (mode %04o); run chmod 600 on it", path, info.Mode().Perm())
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read pseudonymization key file: %w", err)
		}
		raw = b
	}
	key := bytes.TrimSpace(raw)
	if len(key) < report.MinShareKeyBytes {
		return nil, fmt.Errorf("pseudonymization key must be at least %d bytes (for example the output of `openssl rand -base64 32`)", report.MinShareKeyBytes)
	}
	return key, nil
}

// printSharePlan renders the preview. It shows the same plan the export
// writes, and never a key.
func printSharePlan(a *App, plan report.SharePlan) error {
	if a.json() {
		return writeJSON(a.out(), plan)
	}
	out := a.out()
	mode := "not pseudonymized (values exported as written)"
	if plan.Pseudonymize {
		mode = "pseudonymized, not anonymous; linkage: " + plan.Linkage
	}
	partial := ""
	if plan.Partial {
		partial = ", PARTIAL"
	}
	fmt.Fprintf(out, "Profile %s: %s\n", plan.Profile, plan.Summary)
	fmt.Fprintf(out, "Assessment %d (%s%s); %s\n", plan.RunID, plan.RunStatus, partial, mode)
	fmt.Fprintf(out, "Contexts: %s\n", strings.Join(plan.Contexts, ", "))
	for _, g := range plan.Gaps {
		fmt.Fprintf(out, "  coverage gap: %s %s %s\n", g.Context, g.Kind, g.Status)
	}
	fmt.Fprintln(out, "\nWorksheets:")
	for _, s := range plan.Sheets {
		extra := ""
		if len(s.Omitted) > 0 {
			extra = fmt.Sprintf(", %d columns omitted", len(s.Omitted))
		}
		fmt.Fprintf(out, "  %-24s %7d rows, %2d columns%s\n", s.Name, s.Rows, len(s.Columns), extra)
		for _, c := range s.Columns {
			if c.Sensitivity != report.ClassNone && c.Action != "generated" {
				fmt.Fprintf(out, "      %-40s %-10s %s\n", c.Name, c.Sensitivity, c.Action)
			}
		}
	}
	if len(plan.OmittedSheet) > 0 {
		fmt.Fprintf(out, "Omitted worksheets: %s\n", strings.Join(plan.OmittedSheet, ", "))
	}
	classes := make([]string, 0, len(plan.SensitiveClasses))
	for c, n := range plan.SensitiveClasses {
		if c != report.ClassNone {
			classes = append(classes, fmt.Sprintf("%s=%d", c, n))
		}
	}
	sort.Strings(classes)
	fmt.Fprintf(out, "Sensitive columns by class: %s\n", strings.Join(classes, " "))
	fmt.Fprintln(out, "\nWarnings:")
	for _, w := range plan.Warnings {
		fmt.Fprintf(out, "  - %s\n", w)
	}
	fmt.Fprintln(out, "\nNothing was written. Re-run without --preview and with --file to export.")
	return nil
}
