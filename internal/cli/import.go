package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/rvimport"
)

// newImportCommand groups every offline import path into assessment history.
// Import is deliberately its own top-level verb rather than an "assessment"
// subcommand: it never contacts a vCenter, and grouping it with capture would
// suggest it does.
func newImportCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:   "import",
		Short: "Import estate snapshots from other tools into assessment history",
		Long: strings.TrimSpace(`
Bring evidence vsfleet did not collect itself into the same local history used
by "vsfleet assessment", so stored-evidence commands — history, diff, trends —
work on it exactly as they would on a live capture.

Every import runs entirely offline: no configuration, credentials, or vCenter
connection is used or required.`),
		Example: `  # Import an RVTools export
  vsfleet import rvtools estate_2026-01-01.xlsx`,
	})
	cmd.AddCommand(newImportRVToolsCommand(a))
	return cmd
}

func newImportRVToolsCommand(a *App) *cobra.Command {
	var (
		label, note, capturedAt, timezoneName string
		contextMap                            []string
		dryRun, allowDuplicate                bool
	)
	cmd := &cobra.Command{
		Use:   "rvtools FILE",
		Short: "Import an RVTools-compatible XLSX export as a new assessment run",
		Long: strings.TrimSpace(`
Adapt an RVTools-compatible XLSX export into a new stored assessment run.

The workbook's own layout never reaches vsfleet's domain model directly: this
reads supported VM, host, and switch worksheets by column name, accepts known
RVTools and vsfleet header spellings, and normalizes what they carry. A
worksheet or column this importer does not recognize is reported, not silently
dropped.

Imported evidence may reduce confidence, but a field the workbook does not
carry never improves a verdict. A worksheet that is absent, or present without
the capacity columns that make its rows trustworthy, is recorded as an
unavailable collection — the same explicit gap a live capture records for a
denied query — never as an empty one. A workbook with no vHost worksheet is not
an estate with no hosts. Resource pools and networks are always recorded
unavailable: vRP carries no pool membership and RVTools has no network
identity to import. vCD/vUSB attach to VMs; vHBA, vNIC, vSwitch, vPort and
vSC+VMK attach to a host only when a same-workbook host object ID or a unique
name within the same vCenter resolves the row. vMultiPath always resolves a
unique Host name through vHost within the same vCenter: RVTools' Object ID
on that sheet identifies the datastore.

vCenter identity is reconstructed from the workbook's own "vsfleet Context" or
"VI SDK Server" column: two contexts with an identically named VM stay two
VMs, and an identity two rows share is reported as an ambiguity rather than
resolved by guessing. --context-map renames a reconstructed context without
touching today's configuration — an imported run's contexts are independent
of config.toml.

Importing the same file again creates another run, and warns that it did;
--allow-duplicate silences the warning.

The import is all-or-nothing: nothing is written until the whole workbook has
been read, and a failure partway through deletes the run rather than leaving
it half-written.`),
		Example: `  # Import a workbook as a new run
  vsfleet import rvtools estate.xlsx

  # Preview what would be imported without writing anything
  vsfleet import rvtools estate.xlsx --dry-run

  # Label it, pin the capture time, and rename a reconstructed context
  vsfleet import rvtools estate.xlsx --label pre-migration --captured-at 2026-01-01T00:00:00Z --timezone America/Los_Angeles --context-map vc01.example.com=prod`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sha, err := rvimport.FileSHA256(args[0])
			if err != nil {
				return err
			}
			opts := rvimport.Options{SourceLabel: filepath.Base(args[0]), SourceSHA256: sha, Label: label, Note: note}
			if strings.TrimSpace(capturedAt) != "" {
				t, err := time.Parse(time.RFC3339, capturedAt)
				if err != nil {
					return fmt.Errorf("--captured-at must be RFC3339 (e.g. 2026-01-02T15:04:05Z): %w", err)
				}
				opts.CapturedAt = t
			}
			if strings.TrimSpace(timezoneName) != "" {
				location, err := time.LoadLocation(strings.TrimSpace(timezoneName))
				if err != nil {
					return fmt.Errorf("--timezone must be an IANA time zone (e.g. America/Los_Angeles): %w", err)
				}
				opts.Timezone = location
			}
			if len(contextMap) > 0 {
				m := make(map[string]string, len(contextMap))
				for _, kv := range contextMap {
					key, value, ok := strings.Cut(kv, "=")
					key, value = strings.TrimSpace(key), strings.TrimSpace(value)
					if !ok || key == "" || value == "" {
						return fmt.Errorf("--context-map must be KEY=NAME, got %q", kv)
					}
					m[key] = value
				}
				opts.ContextMap = m
			}

			f, err := rvimport.OpenFile(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			result, err := rvimport.Parse(f, opts)
			if err != nil {
				return err
			}
			if dryRun {
				return printImportReport(a, result.Report, nil)
			}
			store, err := a.History()
			if err != nil {
				return err
			}
			if !allowDuplicate {
				prior, found, err := rvimport.FindDuplicate(cmd.Context(), store, sha)
				if err != nil {
					return err
				}
				if found {
					fmt.Fprintf(a.errOut(), "%s this workbook was already imported as assessment %d; importing it again creates another run (--allow-duplicate silences this)\n", glyphFail, prior.ID)
				}
			}
			run, err := result.Write(cmd.Context(), store, time.Now())
			if err != nil {
				return err
			}
			return printImportReport(a, result.Report, &run)
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "label for the stored run")
	cmd.Flags().StringVar(&note, "note", "", "free-text note stored with the run, alongside this import's own provenance")
	cmd.Flags().StringVar(&capturedAt, "captured-at", "", "RFC3339 capture time override (default: vMetaData, workbook properties, then import time)")
	cmd.Flags().StringVar(&timezoneName, "timezone", "", "IANA time zone for timezone-free RVTools timestamps (e.g. America/Los_Angeles)")
	cmd.Flags().StringSliceVar(&contextMap, "context-map", nil, "rename a reconstructed context, KEY=NAME (repeatable); KEY is the vsfleet Context or VI SDK Server value the report names")
	cmd.Flags().BoolVar(&allowDuplicate, "allow-duplicate", false, "do not warn when this exact workbook was already imported")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report what would be imported without writing to history")
	return cmd
}

func printImportReport(a *App, report rvimport.Report, run *assessment.Run) error {
	if a.json() {
		payload := map[string]any{"report": report}
		if run != nil {
			payload["run"] = *run
		}
		return writeJSON(a.out(), payload)
	}
	out := a.out()
	if run != nil {
		fmt.Fprintf(out, "Imported assessment %d (%s) from %s\n", run.ID, strings.ToUpper(string(run.Status)), report.SourceLabel)
	} else {
		fmt.Fprintln(out, "Dry run — nothing was written")
	}
	fmt.Fprintf(out, "Captured at: %s (%s)\n", report.CapturedAt.Local().Format("2006-01-02 15:04:05"), report.CapturedAtSource)
	fmt.Fprintf(out, "Profile: %s, inventory schema %s\n", report.ProfileVersion, report.SchemaVersion)
	fmt.Fprintf(out, "Recognized worksheets: %s\n", strings.Join(report.RecognizedSheets, ", "))
	if len(report.IgnoredSheets) > 0 {
		fmt.Fprintf(out, "Ignored worksheets: %s\n", strings.Join(report.IgnoredSheets, ", "))
	}
	if run == nil {
		// The per-column view is what a dry run is for: it is how a user learns,
		// before writing anything, which columns were read and which were not.
		fmt.Fprintln(out)
		st := newTable(out, "WORKSHEET", "ROWS", "READ", "IGNORED", "MISSING")
		for _, sh := range report.Sheets {
			st.row(sh.Name, itoa(sh.Rows), itoa(len(sh.RecognizedColumns)), joinOrDash(sh.IgnoredColumns), joinOrDash(sh.MissingColumns))
		}
		st.flush()
	}
	fmt.Fprintln(out)
	t := newTable(out, "CONTEXT", "ENDPOINT", "VMS", "HOSTS", "CLUSTERS", "DATASTORES", "SNAPSHOTS", "DVSWITCHES")
	for _, c := range report.Contexts {
		t.row(c.Name, dash(c.Endpoint), itoa(c.VMCount), itoa(c.HostCount), itoa(c.ClusterCount), itoa(c.DatastoreCount), itoa(c.SnapshotCount), itoa(c.DVSwitchCount))
	}
	t.flush()
	if run != nil && run.Status == assessment.RunPartial {
		fmt.Fprintln(out, "\nThis run is partial by design (see the gaps below); trend commands need --include-partial to plot it.")
	}
	if len(report.Gaps) > 0 {
		fmt.Fprintln(out, "\nCoverage gaps (recorded as unavailable, never as empty):")
		for _, g := range report.Gaps {
			scope := ""
			if g.Context != "" {
				scope = " [" + g.Context + "]"
			}
			fmt.Fprintf(out, "  %s%s: %s\n", g.Evidence, scope, g.Reason)
		}
	}
	if len(report.Ambiguities) > 0 {
		fmt.Fprintln(out, "\nIdentity ambiguities (kept separate, never merged):")
		for _, amb := range report.Ambiguities {
			fmt.Fprintf(out, "  %s [%s] %s: %s\n", amb.Sheet, amb.Context, amb.Identity, amb.Detail)
		}
	}
	if len(report.Warnings) > 0 {
		fmt.Fprintln(a.errOut())
		for _, w := range report.Warnings {
			fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, w)
		}
	}
	return nil
}

func joinOrDash(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}
