package cli

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/metareport"
	"github.com/easonliuuuuu/vsfleet/internal/query"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// incompleteExitError marks a metadata export or report whose membership or
// values are not fully known. It is raised only under --fail-on-incomplete.
type incompleteExitError struct{ what string }

func (e *incompleteExitError) Error() string {
	return e.what + " is incomplete; see the warnings above"
}
func (e *incompleteExitError) ExitCode() int { return 3 }

// metadataExport is the JSON shape of "assessment metadata".
type metadataExport struct {
	RunID      int64                 `json:"run_id"`
	RunLabel   string                `json:"run_label,omitempty"`
	CapturedAt time.Time             `json:"captured_at"`
	Columns    []string              `json:"columns"`
	Rows       []metareport.Row      `json:"rows"`
	Coverage   []metareport.Coverage `json:"coverage"`
	Warnings   []string              `json:"warnings"`
	Complete   bool                  `json:"complete"`
}

func newAssessmentMetadataCommand(a *App) *cobra.Command {
	var kinds, where, sources []string
	var format, file string
	var force, failIncomplete bool
	cmd := &cobra.Command{Use: "metadata [RUN]", Short: "Export stored tags and custom attributes in a fixed schema", Long: strings.TrimSpace(`
Export every tag and custom attribute stored in an assessment, one row per
value, with the object's context, vCenter identity and provenance.

The columns never change with an estate's tag categories or attributes, so
the output can be loaded into a database or compared between vCenters. An
object whose source was read but holds no values gets one row with an empty
field and status "available"; an object whose source could not be read gets
one row naming the status (unavailable, denied, unsupported or not_recorded).
Coverage gaps are also reported as warnings. Reads stored evidence only.`),
		Example: `  # Every tag and custom attribute in the latest capture, as CSV
  vsfleet assessment metadata --format csv --file metadata.csv

  # Only VM tags, as JSON
  vsfleet assessment metadata --kind vm --source tag -o json

  # Metadata of production VMs in a labelled capture
  vsfleet assessment metadata nightly --where 'tag.Environment=Production'`,
		Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			parsedKinds, err := parseMetadataKinds(kinds)
			if err != nil {
				return err
			}
			var parsedSources []string
			for _, raw := range sources {
				for _, part := range strings.Split(raw, ",") {
					source, ok := metareport.ParseSource(part)
					if !ok {
						return fmt.Errorf("--source: unknown metadata source %q (supported: tag, custom)", part)
					}
					parsedSources = append(parsedSources, source)
				}
			}
			filter, err := query.Parse(where, parsedKinds)
			if err != nil {
				return fmt.Errorf("--where: %w", err)
			}
			if err := checkMetadataFormat(a, format, file); err != nil {
				return err
			}
			data, err := loadStoredRun(a, cmd, args)
			if err != nil {
				return err
			}
			var objects []metareport.Object
			undetermined := 0
			for _, o := range metareport.Objects(data) {
				if len(parsedKinds) > 0 && !kindIn(parsedKinds, o.Kind) {
					continue
				}
				switch outcome, _ := filter.Evaluate(o.Subject); outcome {
				case query.Matched:
					objects = append(objects, o)
				case query.Undetermined:
					undetermined++
				}
			}
			out := metadataExport{RunID: data.Run.ID, RunLabel: data.Run.Label, CapturedAt: data.Run.StartedAt.UTC(), Columns: metareport.RowColumns, Rows: metareport.Rows(objects, parsedSources), Warnings: []string{}}
			if out.Rows == nil {
				out.Rows = []metareport.Row{}
			}
			out.Coverage = metareport.CoverageOf(data, objects, parsedKinds, parsedSources)
			out.Complete = undetermined == 0
			for _, c := range out.Coverage {
				if !c.Complete() {
					out.Warnings = append(out.Warnings, c.Warning())
					out.Complete = false
				}
			}
			if undetermined > 0 {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%d object(s) excluded as undetermined: --where names a metadata source they could not read", undetermined))
			}
			if out.Coverage == nil {
				out.Coverage = []metareport.Coverage{}
			}
			for _, w := range out.Warnings {
				fmt.Fprintf(a.errOut(), "warning: %s\n", w)
			}
			switch {
			case format == "csv":
				b, err := metareport.RowsCSV(out.Rows)
				if err != nil {
					return err
				}
				if err := writeOutput(a.out(), file, b, force); err != nil {
					return err
				}
				if file != "" {
					fmt.Fprintf(a.errOut(), "assessment %d metadata written to %s (%d rows)\n", out.RunID, file, len(out.Rows))
				}
			case a.json():
				if err := writeJSON(a.out(), out); err != nil {
					return err
				}
			default:
				fmt.Fprintf(a.out(), "assessment %d%s captured %s\n", out.RunID, labelSuffix(out.RunLabel), out.CapturedAt.Format(time.RFC3339))
				t := newTable(a.out(), "CONTEXT", "KIND", "NAME", "ID", "SOURCE", "FIELD", "VALUE", "STATUS")
				for _, r := range out.Rows {
					t.row(r.Context, r.Kind, r.ObjectName, r.ObjectID, r.Source, dash(r.Field), dash(r.Value), r.Status)
				}
				t.flush()
			}
			if failIncomplete && !out.Complete {
				return &incompleteExitError{what: fmt.Sprintf("assessment %d metadata", out.RunID)}
			}
			return nil
		}}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "restrict to kinds (repeatable or comma-separated)")
	cmd.Flags().StringArrayVar(&where, "where", nil, "structured predicate (repeatable; predicates are ANDed)")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "restrict to metadata sources: tag, custom")
	addMetadataOutputFlags(cmd, &format, &file, &force, &failIncomplete)
	return cmd
}

func newAssessmentMetadataReportCommand(a *App) *cobra.Command {
	var format, file, base string
	var force, failIncomplete bool
	cmd := &cobra.Command{Use: "metadata-report DEFINITION [RUN]", Short: "Run a saved tag and custom attribute report", Long: strings.TrimSpace(`
Resolve a saved report definition against a stored assessment.

DEFINITION is a TOML file, or the name of one saved as
<config dir>/reports/<name>.toml. It selects contexts, kinds and --where
style predicates, and names the output columns. The same definition run
against the same assessment always produces the same output.

An object whose membership depends on a tag or custom attribute source that
could not be read is listed as undetermined, never silently excluded, and
the report is marked incomplete. With --base, the report is also run against
an earlier assessment and membership changes are listed; an object whose
membership is unknown on either side is reported as an unknown change, not
as added or removed. Reads stored evidence only.`),
		Example: `  # Run a saved report against the latest capture
  vsfleet assessment metadata-report pci-production

  # Same report, as CSV, against a labelled capture
  vsfleet assessment metadata-report ./pci.toml nightly --format csv --file pci.csv

  # What joined or left the report since last week's baseline
  vsfleet assessment metadata-report pci-production --base baseline`,
		Args: cobra.RangeArgs(1, 2), RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkMetadataFormat(a, format, file); err != nil {
				return err
			}
			configPath := a.ConfigPath
			if configPath == "" {
				p, err := config.DefaultPath()
				if err != nil {
					return err
				}
				configPath = p
			}
			path, err := metareport.Resolve(args[0], metareport.ReportsDir(configPath))
			if err != nil {
				return err
			}
			def, err := metareport.Load(path)
			if err != nil {
				return err
			}
			compiled, err := def.Compile()
			if err != nil {
				return err
			}
			data, err := loadStoredRun(a, cmd, args[1:])
			if err != nil {
				return err
			}
			result := compiled.Evaluate(data)
			if base != "" {
				baseData, err := loadStoredRun(a, cmd, []string{base})
				if err != nil {
					return err
				}
				comparison := metareport.Compare(compiled.Evaluate(baseData), result)
				return printComparison(a, comparison, format, file, force, failIncomplete)
			}
			for _, w := range result.Warnings {
				fmt.Fprintf(a.errOut(), "warning: %s\n", w)
			}
			switch {
			case format == "csv":
				b, err := result.CSV()
				if err != nil {
					return err
				}
				if err := writeOutput(a.out(), file, b, force); err != nil {
					return err
				}
				if file != "" {
					fmt.Fprintf(a.errOut(), "report %s on assessment %d written to %s (%d members, %d undetermined)\n", def.Name, result.RunID, file, len(result.Members), len(result.Undetermined))
				}
			case a.json():
				if err := writeJSON(a.out(), result); err != nil {
					return err
				}
			default:
				printReportResult(a.out(), result)
			}
			if failIncomplete && !result.Complete {
				return &incompleteExitError{what: fmt.Sprintf("report %s on assessment %d", def.Name, result.RunID)}
			}
			return nil
		}}
	cmd.Flags().StringVar(&base, "base", "", "also run the report against this earlier assessment and list membership changes")
	addMetadataOutputFlags(cmd, &format, &file, &force, &failIncomplete)
	return cmd
}

func addMetadataOutputFlags(cmd *cobra.Command, format, file *string, force, failIncomplete *bool) {
	cmd.Flags().StringVar(format, "format", "", "write csv instead of the table or -o json output")
	cmd.Flags().StringVar(file, "file", "", "write the output to this file instead of stdout")
	cmd.Flags().BoolVar(force, "force", false, "replace an existing --file")
	cmd.Flags().BoolVar(failIncomplete, "fail-on-incomplete", false, "exit 3 when the output is incomplete: unreadable metadata or a failed collection in scope, or undetermined report membership")
}

func checkMetadataFormat(a *App, format, file string) error {
	switch strings.ToLower(format) {
	case "", "table":
	case "json":
		a.Format = FormatJSON
	case "csv":
		if a.json() {
			return fmt.Errorf("--format csv and -o json are mutually exclusive")
		}
	default:
		return fmt.Errorf("unsupported --format %q (supported: csv, json)", format)
	}
	if file != "" && strings.ToLower(format) != "csv" && !a.json() {
		return fmt.Errorf("--file needs --format csv or -o json")
	}
	return nil
}

func parseMetadataKinds(values []string) ([]vsphere.Kind, error) {
	var out []vsphere.Kind
	for _, raw := range values {
		for _, part := range strings.Split(raw, ",") {
			kind, err := metareport.ParseKind(part)
			if err != nil {
				return nil, err
			}
			out = append(out, kind)
		}
	}
	return out, nil
}

func kindIn(kinds []vsphere.Kind, kind vsphere.Kind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func loadStoredRun(a *App, cmd *cobra.Command, args []string) (assessment.ExportData, error) {
	selector := "latest"
	if len(args) == 1 {
		selector = args[0]
	}
	s, err := a.History()
	if err != nil {
		return assessment.ExportData{}, err
	}
	runID, err := s.ResolveRun(cmd.Context(), selector)
	if err != nil {
		return assessment.ExportData{}, err
	}
	return s.LoadExportDataForContexts(cmd.Context(), runID, a.StoredContextNames())
}

// writeOutput writes b to stdout, or atomically to file: a sibling temp
// file is published only once complete, and an existing file is replaced
// only with force.
func writeOutput(stdout io.Writer, file string, b []byte, force bool) error {
	if file == "" {
		_, err := stdout.Write(b)
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return publishExportFile(tmpName, file, force)
}

func labelSuffix(label string) string {
	if label == "" {
		return ""
	}
	return " (" + label + ")"
}

func printReportResult(out io.Writer, r metareport.Result) {
	fmt.Fprintf(out, "report %s on assessment %d%s captured %s\n", r.Report.Name, r.RunID, labelSuffix(r.RunLabel), r.CapturedAt.Format(time.RFC3339))
	headers := make([]string, 0, len(r.Columns)+1)
	for _, c := range r.Columns {
		headers = append(headers, strings.ToUpper(c))
	}
	t := newTable(out, append(headers, "MEMBERSHIP")...)
	for _, m := range r.Members {
		t.row(append(dashAll(m.Values), "member")...)
	}
	for _, m := range r.Undetermined {
		t.row(append(dashAll(m.Values), "undetermined")...)
	}
	t.flush()
	state := "complete"
	if !r.Complete {
		state = "INCOMPLETE"
	}
	fmt.Fprintf(out, "\n%d member(s), %d undetermined; membership %s\n", len(r.Members), len(r.Undetermined), state)
}

func dashAll(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = dash(v)
	}
	return out
}

var comparisonColumns = []string{"change", "before", "after", "context", "vcenter_id", "kind", "id", "name", "reason"}

func printComparison(a *App, c metareport.Comparison, format, file string, force, failIncomplete bool) error {
	for _, w := range c.Warnings {
		fmt.Fprintf(a.errOut(), "warning: %s\n", w)
	}
	switch {
	case format == "csv":
		rows := make([][]string, 0, len(c.Changes))
		for _, ch := range c.Changes {
			rows = append(rows, []string{ch.Change, ch.Before, ch.After, ch.Context, ch.VCenterID, ch.Kind, ch.ID, ch.Name, ch.Reason})
		}
		b, err := csvBytes(comparisonColumns, rows)
		if err != nil {
			return err
		}
		if err := writeOutput(a.out(), file, b, force); err != nil {
			return err
		}
	case a.json():
		if err := writeJSON(a.out(), c); err != nil {
			return err
		}
	default:
		out := a.out()
		fmt.Fprintf(out, "report %s: assessment %d (%s) -> %d (%s)\n", c.Report, c.BaseRunID, c.BaseCapturedAt.Format(time.RFC3339), c.TargetRunID, c.TargetCapturedAt.Format(time.RFC3339))
		t := newTable(out, "CHANGE", "CONTEXT", "KIND", "NAME", "ID", "BEFORE", "AFTER", "REASON")
		for _, ch := range c.Changes {
			t.row(ch.Change, ch.Context, ch.Kind, ch.Name, ch.ID, ch.Before, ch.After, dash(ch.Reason))
		}
		t.flush()
		state := "complete"
		if !c.Complete {
			state = "INCOMPLETE"
		}
		counts := map[string]int{}
		for _, ch := range c.Changes {
			counts[ch.Change]++
		}
		fmt.Fprintf(out, "\n%d added, %d removed, %d unknown, %d unchanged member(s); comparison %s\n", counts[metareport.ChangeAdded], counts[metareport.ChangeRemoved], counts[metareport.ChangeUnknown], c.Unchanged, state)
	}
	if failIncomplete && !c.Complete {
		return &incompleteExitError{what: "report comparison " + c.Report}
	}
	return nil
}

func csvBytes(header []string, rows [][]string) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(header); err != nil {
		return nil, err
	}
	if err := w.WriteAll(rows); err != nil {
		return nil, err
	}
	return buf.Bytes(), w.Error()
}
