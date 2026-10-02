package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/query"
	"github.com/easonliuuuuu/vsfleet/internal/report"
	"github.com/easonliuuuuu/vsfleet/internal/version"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type policyExitError struct{ violations []assessment.PolicyViolation }

func (e *policyExitError) Error() string {
	return fmt.Sprintf("assessment diff policy failed (%d violation(s))", len(e.violations))
}
func (e *policyExitError) ExitCode() int { return 2 }

type doctorExitError struct{ message string }

func (e *doctorExitError) Error() string { return e.message }
func (e *doctorExitError) ExitCode() int { return 1 }

// partialExitError reports a capture that stored real evidence from some
// vCenters but not all of them. It is raised only under --fail-on-partial:
// a scheduled job that has always treated one site being down as routine must
// keep behaving the way it did before the flag existed.
type partialExitError struct {
	runID      int64
	successful int
	total      int
}

func (e *partialExitError) Error() string {
	return fmt.Sprintf("assessment %d is partial: %d of %d contexts returned inventory", e.runID, e.successful, e.total)
}
func (e *partialExitError) ExitCode() int { return 3 }

func newAssessmentCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:     "assessment",
		Aliases: []string{"assess", "history"},
		Short:   "Capture and compare historical assessments",
		Long: strings.TrimSpace(`
Capture a point-in-time snapshot of the estate into the local history
database, then read it back: compare two captures, render a report, export an
RVTools workbook, or ask what changed.

Every subcommand below "run" reads stored evidence only, except "perf
collect", which reads a bounded window of performance history. They never
contact a vCenter otherwise, so they stay usable when a site is offline and
always describe the estate as it was when the capture was taken.`),
		Example: `  # Capture every context and label the capture
  vsfleet assessment run --all-contexts --label nightly

  # See what has been captured, then compare the two most recent
  vsfleet assessment list
  vsfleet assessment diff

  # Compare a labelled baseline against the latest capture
  vsfleet assessment diff nightly latest

  # Fail a pipeline when VMs were removed
  vsfleet assessment diff --fail-on removed

  # Read a capture without touching a vCenter
  vsfleet assessment findings
  vsfleet assessment export --file estate.xlsx`,
	})
	cmd.AddCommand(newAssessmentRunCommand(a), newAssessmentListCommand(a), newAssessmentDiffCommand(a), newAssessmentSnapshotsCommand(a), newAssessmentDeleteCommand(a), newAssessmentUpdateCommand(a), newAssessmentTrendsCommand(a), newAssessmentCapacityCommand(a), newAssessmentReportCommand(a), newAssessmentExportCommand(a), newAssessmentFindingsCommand(a), newAssessmentInventoryCommand(a), newAssessmentMetadataCommand(a), newAssessmentMetadataReportCommand(a), newAssessmentOrphansCommand(a), newAssessmentReadinessCommand(a), newAssessmentNetworkReadinessCommand(a), newAssessmentSizingCommand(a), newAssessmentPruneCommand(a), newAssessmentBackupCommand(a), newAssessmentRestoreCommand(a), newAssessmentDoctorCommand(a), newAssessmentPerfCommand(a))
	return cmd
}

type storedInventoryItem struct {
	RunID   int64           `json:"run_id"`
	Context string          `json:"context"`
	Kind    string          `json:"kind"`
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Object  json.RawMessage `json:"object"`
}

func newAssessmentInventoryCommand(a *App) *cobra.Command {
	var kinds, where []string
	var wide bool
	cmd := &cobra.Command{Use: "inventory [RUN]", Short: "Query inventory stored in an assessment", Example: `  # Query tagged objects from the latest capture
  vsfleet assessment inventory --where 'tag=Production' --wide

  # Show hosts and datastores with at least 8 CPUs or low free space
  vsfleet assessment inventory --kind host,datastore --where 'cpu_cores>=8'`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		parsed := make([]vsphere.Kind, 0, len(kinds))
		for _, value := range kinds {
			value = strings.ToLower(strings.TrimSpace(value))
			kind, err := vsphere.ParseKind(value)
			if err != nil {
				switch value {
				case "resourcepool", "resourcepools":
					kind = vsphere.KindResourcePool
				case "dvswitch", "dvswitches":
					kind = vsphere.KindDVSwitch
				default:
					return err
				}
			}
			parsed = append(parsed, kind)
		}
		filter, err := query.Parse(where, parsed)
		if err != nil {
			return fmt.Errorf("--where: %w", err)
		}
		selector := "latest"
		if len(args) == 1 {
			selector = args[0]
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		runID, err := s.ResolveRun(cmd.Context(), selector)
		if err != nil {
			return err
		}
		data, err := s.LoadExportDataForContexts(cmd.Context(), runID, a.StoredContextNames())
		if err != nil {
			return err
		}
		items := storedInventoryItems(data, parsed, filter)
		if a.json() {
			return writeJSON(a.out(), items)
		}
		headers := []string{"CONTEXT", "KIND", "NAME", "ID", "PATH"}
		if wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		t := newTable(a.out(), headers...)
		for _, item := range items {
			subject, _ := query.SubjectFromJSON(vsphere.Kind(item.Kind), item.Object)
			path, _ := subject.Fields["path"].(string)
			row := []string{item.Context, item.Kind, item.Name, item.ID, dash(path)}
			if wide {
				metadata := metadataFromSubject(subject)
				row = append(row, dash(metadataTags(metadata)), dash(metadataAttributes(metadata)))
			}
			t.row(row...)
		}
		t.flush()
		return nil
	}}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "restrict to kinds (repeatable or comma-separated)")
	cmd.Flags().StringArrayVar(&where, "where", nil, "structured predicate (repeatable; predicates are ANDed)")
	cmd.Flags().BoolVar(&wide, "wide", false, "include tags and custom attributes")
	return cmd
}

func storedInventoryItems(data assessment.ExportData, kinds []vsphere.Kind, filter query.Filter) []storedInventoryItem {
	wants := func(kind vsphere.Kind) bool {
		if len(kinds) == 0 {
			return true
		}
		for _, want := range kinds {
			if want == kind {
				return true
			}
		}
		return false
	}
	items := make([]storedInventoryItem, 0, len(data.VMs)+len(data.Resources))
	for _, vm := range data.VMs {
		kind := vsphere.KindVM
		if vm.Observation.VM.IsTemplate {
			kind = vsphere.KindTemplate
		}
		if !wants(kind) {
			continue
		}
		raw, _ := json.Marshal(vm.Observation.VM)
		subject, err := query.SubjectFromJSON(kind, raw)
		if err != nil || !filter.Match(subject) {
			continue
		}
		items = append(items, storedInventoryItem{RunID: data.Run.ID, Context: vm.Observation.Context, Kind: string(kind), ID: vm.Observation.VM.ID, Name: vm.Observation.VM.Name, Object: raw})
	}
	for _, resource := range data.Resources {
		kind := vsphere.Kind(resource.Kind)
		if !wants(kind) {
			continue
		}
		subject, err := query.SubjectFromJSON(kind, resource.Payload)
		if err != nil || !filter.Match(subject) {
			continue
		}
		items = append(items, storedInventoryItem{RunID: data.Run.ID, Context: resource.Context, Kind: resource.Kind, ID: resource.ID, Name: resource.Name, Object: resource.Payload})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Context != items[j].Context {
			return items[i].Context < items[j].Context
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Name < items[j].Name
	})
	return items
}

func newAssessmentFindingsCommand(a *App) *cobra.Command {
	cmd := newFindingsCommand(a, "findings [RUN]", "Show health findings for a stored assessment")
	cmd.Long = strings.TrimSpace(`
List the individual health findings behind a stored assessment.

This is the itemised form of "vsfleet health", which reports the same rules as
a single verdict. Reads stored evidence only and never contacts a vCenter.`)
	cmd.Example = `  # Findings for the most recent capture
  vsfleet assessment findings

  # Findings for a labelled capture, as JSON
  vsfleet assessment findings nightly -o json

  # Capacity findings of warning severity or worse
  vsfleet assessment findings --category capacity --severity warning`
	return cmd
}

func newAssessmentReadinessCommand(a *App) *cobra.Command {
	var flags healthFlags
	var failOnBlockers bool
	cmd := &cobra.Command{Use: "readiness [RUN]", Short: "Assess migration readiness from a stored assessment", Example: `  # Readiness verdict for the most recent capture
  vsfleet assessment readiness

  # Verdict for a labelled capture, as JSON
  vsfleet assessment readiness pre-migration -o json

  # Gate a migration pipeline: exit 2 when blockers remain
  vsfleet assessment readiness --fail-on-blockers`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		// Readiness always considers every category and severity. The tuning and
		// disable flags still apply because they change the evaluated report.
		flags.minimumSeverity = "info"
		flags.category = ""
		report, err := evaluateHealthCommand(cmd, a, args, flags)
		if err != nil {
			return err
		}
		readiness := health.Readiness(report)
		if a.json() {
			if err := writeJSON(a.out(), readiness); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(a.out(), "Migration readiness: %s\n", strings.ToUpper(readiness.Verdict))
			if len(readiness.Blockers) > 0 {
				fmt.Fprintln(a.out(), "Blockers:")
				for _, finding := range readiness.Blockers {
					fmt.Fprintf(a.out(), "  %s [%s] %s — %s\n", finding.Rule, finding.Object.Kind+"/"+finding.Object.Name, finding.Message, finding.Recommendation)
				}
			}
			if len(readiness.Advisories) > 0 {
				fmt.Fprintln(a.out(), "Advisories:")
				for _, finding := range readiness.Advisories {
					fmt.Fprintf(a.out(), "  %s [%s] %s — %s\n", finding.Rule, finding.Object.Kind+"/"+finding.Object.Name, finding.Message, finding.Recommendation)
				}
			}
			if len(readiness.Unresolved) > 0 {
				fmt.Fprintln(a.out(), "Not evaluated:")
				for _, rule := range readiness.Unresolved {
					reason := rule.Reason
					if len(rule.Blind) > 0 {
						reason = strings.TrimSpace(strings.TrimSuffix(reason, ".") + "; blind vCenters: " + strings.Join(rule.Blind, ", "))
					}
					fmt.Fprintf(a.out(), "  %s — %s\n", rule.Rule, reason)
				}
			}
		}
		if failOnBlockers && len(readiness.Blockers) > 0 {
			return &readinessExitError{blockers: len(readiness.Blockers)}
		}
		return nil
	}}
	flags.add(cmd, false)
	cmd.Flags().BoolVar(&failOnBlockers, "fail-on-blockers", false, "exit 2 when migration blockers are present")
	return cmd
}

type exportFile struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type exportReceipt struct {
	RunID  int64        `json:"run_id"`
	Path   string       `json:"path"`
	Bytes  int64        `json:"bytes,omitempty"`
	SHA256 string       `json:"sha256,omitempty"`
	Files  []exportFile `json:"files,omitempty"`
	// Profile, Pseudonymized, Partial and Warnings describe a scoped sharing
	// export; they are absent from the ordinary export receipt.
	Profile       string   `json:"profile,omitempty"`
	Pseudonymized bool     `json:"pseudonymized,omitempty"`
	Partial       bool     `json:"partial,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
	// FileInventory reports, per context and datastore, how complete the
	// vFileInfo rows are. It is present only when the run opted into the
	// datastore file inventory; a status other than "complete" means the
	// datastore's rows are a partial list.
	FileInventory []assessment.FileInventoryContext `json:"file_inventory,omitempty"`
}

func newAssessmentExportCommand(a *App) *cobra.Command {
	var format, file string
	var force, includeMetadata bool
	var share shareFlags
	cmd := &cobra.Command{Use: "export [RUN]", Short: "Export a stored assessment as RVTools XLSX or CSV", Example: `  # 25-sheet RVTools workbook from the most recent capture
  vsfleet assessment export --file estate.xlsx

  # One CSV per RVTools tab, into a directory
  vsfleet assessment export --format csv --file ./estate-csv

  # Export a labelled capture, replacing an existing file
  vsfleet assessment export nightly --file estate.xlsx --force

  # See what the workbook will contain before exporting
  vsfleet compatibility report

  # Preview a scoped sizing summary, then write it with pseudonymized names
  vsfleet assessment export --profile sizing-summary --pseudonymize --preview
  vsfleet assessment export --profile sizing-summary --pseudonymize \
    --pseudonymize-key-file ~/.config/vsfleet/share.key --file sizing.xlsx`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		format = strings.ToLower(strings.TrimSpace(format))
		if err := share.validate(format); err != nil {
			return err
		}
		var shareKey []byte
		if share.enabled() && !share.preview && share.pseudonymize {
			key, err := readShareKey(share.keyFile, a.in())
			if err != nil {
				return err
			}
			shareKey = key
		}
		if file == "" && !share.preview {
			return fmt.Errorf("--file is required")
		}
		switch format {
		case "rvtools":
			if file != "" && filepath.Ext(file) != ".xlsx" {
				return fmt.Errorf("--file must have a .xlsx extension")
			}
		case "csv":
			if filepath.Ext(file) == ".xlsx" {
				return fmt.Errorf("--file must be a directory for --format csv, not a .xlsx path")
			}
		default:
			return fmt.Errorf("unsupported export format %q (supported: rvtools, csv)", format)
		}
		selector := "latest"
		if len(args) == 1 {
			selector = args[0]
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		runID, err := s.ResolveRun(cmd.Context(), selector)
		if err != nil {
			return err
		}
		data, err := s.LoadExportDataForContexts(cmd.Context(), runID, a.StoredContextNames())
		if err != nil {
			return err
		}
		healthReport := health.Evaluate(data, health.Options{Thresholds: health.DefaultThresholds()})
		for _, warning := range exportWarnings(data) {
			fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, warning)
		}
		var inventory []assessment.FileInventoryContext
		if assessment.HasFileInventory(data) {
			inventory = assessment.FileInventoryCoverage(data)
			for _, warning := range fileInventoryWarnings(inventory) {
				fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, warning)
			}
			files := 0
			for _, c := range inventory {
				files += c.Files()
			}
			fmt.Fprintf(a.errOut(), "note: vFileInfo lists %d datastore file names and paths; the export can reveal VM names and internal layout, so handle it as sensitive\n", files)
		}
		var receipt exportReceipt
		if share.enabled() {
			opts := share.options(shareKey)
			opts.Metadata = includeMetadata
			plan, err := report.PlanShare(data, healthReport, opts)
			if err != nil {
				return err
			}
			if share.preview {
				return printSharePlan(a, plan)
			}
			for _, w := range plan.Warnings {
				fmt.Fprintf(a.errOut(), "note: %s\n", w)
			}
			receipt, err = publishXLSX(data.Run.ID, file, force, func(w io.Writer) error {
				if err := report.WriteShared(w, data, healthReport, opts); err != nil {
					return fmt.Errorf("write %s export: %w", opts.Profile, err)
				}
				return nil
			})
			if err != nil {
				return err
			}
			receipt.Profile, receipt.Pseudonymized, receipt.Partial, receipt.Warnings = plan.Profile, plan.Pseudonymize, plan.Partial, plan.Warnings
			if a.json() {
				return writeJSON(a.out(), receipt)
			}
			fmt.Fprintf(a.out(), "assessment %d exported to %s with profile %s (%d bytes, sha256 %s)\n", receipt.RunID, receipt.Path, plan.Profile, receipt.Bytes, receipt.SHA256)
			return nil
		}
		exportOpts := report.ExportOptions{Metadata: includeMetadata}
		if format == "csv" {
			receipt, err = publishRVToolsCSV(data, healthReport, exportOpts, file, force)
		} else {
			receipt, err = publishRVToolsXLSX(data, healthReport, exportOpts, file, force)
		}
		if err != nil {
			return err
		}
		receipt.FileInventory = inventory
		if a.json() {
			return writeJSON(a.out(), receipt)
		}
		if format == "csv" {
			fmt.Fprintf(a.out(), "assessment %d exported to %s (%d files)\n", receipt.RunID, receipt.Path, len(receipt.Files))
			for _, f := range receipt.Files {
				fmt.Fprintf(a.out(), "  %s (%d bytes, sha256 %s)\n", f.Name, f.Bytes, f.SHA256)
			}
		} else {
			fmt.Fprintf(a.out(), "assessment %d exported to %s (%d bytes, sha256 %s)\n", receipt.RunID, receipt.Path, receipt.Bytes, receipt.SHA256)
		}
		return nil
	}}
	cmd.Flags().StringVar(&format, "format", "rvtools", "export format: rvtools (XLSX) or csv (one file per RVTools tab)")
	cmd.Flags().StringVar(&file, "file", "", "destination: a .xlsx file for --format rvtools, a directory for --format csv")
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing export")
	cmd.Flags().BoolVar(&includeMetadata, "include-metadata", false, "add the vsfleetMetadata sheet: one row per tag or custom attribute value (omitted by --profile sizing-summary)")
	share.add(cmd)
	return cmd
}

// publishRVToolsXLSX renders data as one RVTools workbook at file: written to
// a sibling temp file first, then published atomically (a rename with
// --force, otherwise a no-clobber hard link) so a failed export never leaves
// a half-written workbook.
func publishRVToolsXLSX(data assessment.ExportData, healthReport health.Report, opts report.ExportOptions, file string, force bool) (exportReceipt, error) {
	return publishXLSX(data.Run.ID, file, force, func(w io.Writer) error {
		if err := report.WriteRVToolsWith(w, data, healthReport, opts); err != nil {
			return fmt.Errorf("write RVTools export: %w", err)
		}
		return nil
	})
}

// publishXLSX publishes the workbook write produces at file with the atomic,
// no-clobber-without-force behavior shared by every XLSX export.
func publishXLSX(runID int64, file string, force bool, write func(io.Writer) error) (exportReceipt, error) {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return exportReceipt{}, fmt.Errorf("create export directory: %w", err)
	}
	if _, err := os.Stat(file); err == nil && !force {
		return exportReceipt{}, fmt.Errorf("export file %q already exists; pass --force to replace it", file)
	} else if err != nil && !os.IsNotExist(err) {
		return exportReceipt{}, fmt.Errorf("check export file: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".vsfleet-export-*.xlsx")
	if err != nil {
		return exportReceipt{}, fmt.Errorf("create temporary export: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return exportReceipt{}, err
	}
	if err := write(tmp); err != nil {
		_ = tmp.Close()
		return exportReceipt{}, err
	}
	if err := tmp.Close(); err != nil {
		return exportReceipt{}, fmt.Errorf("close temporary export: %w", err)
	}
	info, err := os.Stat(tmpName)
	if err != nil {
		return exportReceipt{}, err
	}
	hash, err := fileSHA256(tmpName)
	if err != nil {
		return exportReceipt{}, err
	}
	if err := publishExportFile(tmpName, file, force); err != nil {
		return exportReceipt{}, err
	}
	return exportReceipt{RunID: runID, Path: file, Bytes: info.Size(), SHA256: hash}, nil
}

// publishRVToolsCSV renders data as one CSV file per RVTools tab under dir.
// Rendering happens entirely in memory before any file is touched, so a data
// error leaves the destination untouched; each tab is then published with
// the same atomic-publish, no-clobber-without-force behavior as the XLSX
// writer.
func publishRVToolsCSV(data assessment.ExportData, healthReport health.Report, opts report.ExportOptions, dir string, force bool) (exportReceipt, error) {
	files, err := report.RVToolsCSVWith(data, healthReport, opts)
	if err != nil {
		return exportReceipt{}, fmt.Errorf("render CSV export: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return exportReceipt{}, fmt.Errorf("create export directory: %w", err)
	}
	if !force {
		for _, file := range files {
			dest := filepath.Join(dir, file.Name)
			if _, err := os.Stat(dest); err == nil {
				return exportReceipt{}, fmt.Errorf("export file %q already exists; pass --force to replace it", dest)
			} else if !os.IsNotExist(err) {
				return exportReceipt{}, fmt.Errorf("check export file: %w", err)
			}
		}
	}
	receipt := exportReceipt{RunID: data.Run.ID, Path: dir}
	for _, file := range files {
		dest := filepath.Join(dir, file.Name)
		tmp, err := os.CreateTemp(dir, ".vsfleet-export-*.csv")
		if err != nil {
			return exportReceipt{}, fmt.Errorf("create temporary export: %w", err)
		}
		tmpName := tmp.Name()
		if err := tmp.Chmod(0o600); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return exportReceipt{}, err
		}
		if _, err := tmp.Write(file.Data); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return exportReceipt{}, fmt.Errorf("write %s: %w", file.Name, err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return exportReceipt{}, fmt.Errorf("close temporary export: %w", err)
		}
		if err := publishExportFile(tmpName, dest, force); err != nil {
			return exportReceipt{}, err
		}
		receipt.Files = append(receipt.Files, exportFile{Name: file.Name, Bytes: int64(len(file.Data)), SHA256: fmt.Sprintf("%x", sha256.Sum256(file.Data))})
	}
	return receipt, nil
}

// publishExportFile atomically moves tmpName to dest: a rename when force
// replaces an existing export, or a no-clobber hard link otherwise.
func publishExportFile(tmpName, dest string, force bool) error {
	if force {
		if err := os.Rename(tmpName, dest); err != nil {
			return fmt.Errorf("publish export: %w", err)
		}
		return nil
	}
	if err := os.Link(tmpName, dest); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("export file %q already exists; pass --force to replace it", dest)
		}
		return fmt.Errorf("publish export: %w", err)
	}
	return os.Remove(tmpName)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func exportWarnings(data assessment.ExportData) []string {
	var warnings []string
	if data.Run.Status != assessment.RunComplete {
		warnings = append(warnings, fmt.Sprintf("assessment %d is %s; export coverage may be incomplete", data.Run.ID, data.Run.Status))
	}
	for _, c := range data.Contexts {
		if c.VMStatus != "success" && c.VMStatus != "empty" {
			warnings = append(warnings, fmt.Sprintf("%s VM collection: %s", c.Name, nonemptyExport(c.Error, c.VMStatus)))
		}
		seen := make(map[string]bool)
		for _, collection := range c.Collections {
			seen[collection.Kind] = true
			if collection.Status != "success" && collection.Status != "empty" {
				warnings = append(warnings, fmt.Sprintf("%s %s collection: %s", c.Name, collection.Kind, nonemptyExport(collection.Error, collection.Status)))
			}
		}
		for _, kind := range []string{"host", "cluster", "datastore"} {
			if !seen[kind] {
				warnings = append(warnings, fmt.Sprintf("%s %s collection was not recorded", c.Name, kind))
			}
		}
	}
	return warnings
}

func nonemptyExport(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func newAssessmentTrendsCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:   "trends",
		Short: "Show historical estate trends",
		Long: strings.TrimSpace(`
Plot one dimension of the estate across every stored capture, rather than
comparing two captures against each other.`),
		Example: `  # VM population and churn over the stored history
  vsfleet assessment trends churn

  # Snapshot age, narrowed to a window
  vsfleet assessment trends snapshots --from 2026-01-01 --to 2026-03-31

  # Compute and storage capacity, most recent 20 captures
  vsfleet assessment trends capacity --limit 20`,
	})
	cmd.AddCommand(newAssessmentChurnTrendCommand(a), newAssessmentSnapshotTrendCommand(a), newAssessmentCapacityTrendCommand(a))
	return cmd
}

type trendFlags struct {
	from, to       string
	limit          int
	includePartial bool
	contexts       []string
}

func (f *trendFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.from, "from", "", "first assessment ID or label (inclusive)")
	cmd.Flags().StringVar(&f.to, "to", "", "last assessment ID or label (inclusive)")
	cmd.Flags().IntVar(&f.limit, "limit", 30, "number of complete assessments (0 means unlimited)")
	cmd.Flags().BoolVar(&f.includePartial, "include-partial", false, "include partial assessments")
	cmd.Flags().StringSliceVar(&f.contexts, "context", nil, "limit trend data to context(s)")
}

// options resolves trend scope with the same --all-contexts precedence as
// every other stored-evidence command: --all-contexts wins outright, even
// over an explicit trend-local --context, since a raw --context value must
// not keep narrowing or validating the stored scope once --all-contexts is
// present.
func (f trendFlags) options(ctx context.Context, s *assessment.Store, a *App) (assessment.TrendOptions, error) {
	var contexts []string
	switch {
	case a.AllContexts:
		contexts = nil
	case len(f.contexts) > 0:
		contexts = f.contexts
	default:
		contexts = a.ContextNames
	}
	opts := assessment.TrendOptions{Limit: f.limit, IncludePartial: f.includePartial, Contexts: contexts}
	var err error
	if f.from != "" {
		opts.FromID, err = s.ResolveRun(ctx, f.from)
		if err != nil {
			return opts, fmt.Errorf("--from: %w", err)
		}
	}
	if f.to != "" {
		opts.ToID, err = s.ResolveRun(ctx, f.to)
		if err != nil {
			return opts, fmt.Errorf("--to: %w", err)
		}
	}
	return opts, nil
}

func newAssessmentChurnTrendCommand(a *App) *cobra.Command {
	var flags trendFlags
	cmd := &cobra.Command{Use: "churn", Short: "Show VM population and churn over time", Example: `  # VM population across the stored history
  vsfleet assessment trends churn

  # A window, by label or ID
  vsfleet assessment trends churn --from pre-migration --to nightly

  # One context, most recent 10 captures, as JSON
  vsfleet assessment trends churn --context prod --limit 10 -o json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		opts, err := flags.options(cmd.Context(), s, a)
		if err != nil {
			return err
		}
		trend, err := s.ChurnTrend(cmd.Context(), opts)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), trend)
		}
		t := newTable(a.out(), "RUN", "DATE", "VMS", "+APPEARED", "-VANISHED", "→MOVED", "~MODIFIED")
		for _, p := range trend.Points {
			t.row(strconv.FormatInt(p.Run.ID, 10), p.Run.StartedAt.Local().Format("2006-01-02"), itoa(p.VMCount), itoa(p.Appeared), itoa(p.Vanished), itoa(p.Moved), itoa(p.Modified))
		}
		t.flush()
		return nil
	}}
	flags.add(cmd)
	return cmd
}

func newAssessmentSnapshotTrendCommand(a *App) *cobra.Command {
	var flags trendFlags
	var older string
	cmd := &cobra.Command{Use: "snapshots", Short: "Show snapshot-age trends", Example: `  # Snapshot-age trend across the stored history
  vsfleet assessment trends snapshots

  # Count snapshots older than 90 days instead of the 30-day default
  vsfleet assessment trends snapshots --older-than 90d

  # A window, including partial captures
  vsfleet assessment trends snapshots --from 2026-01-01 --include-partial`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		opts, err := flags.options(cmd.Context(), s, a)
		if err != nil {
			return err
		}
		age, err := parseHumanDuration(older)
		if err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
		trend, err := s.SnapshotTrend(cmd.Context(), opts, age)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), trend)
		}
		t := newTable(a.out(), "RUN", "DATE", "TOTAL", "STALE", "OLDEST", "TREND")
		values := make([]float64, len(trend.Points))
		for i, p := range trend.Points {
			values[i] = float64(p.Total)
			t.row(strconv.FormatInt(p.Run.ID, 10), p.Run.StartedAt.Local().Format("2006-01-02"), itoa(p.Total), itoa(p.Stale), humanize.Age(p.OldestAge), "")
		}
		t.flush()
		if len(values) > 0 {
			fmt.Fprintf(a.out(), "snapshot totals: %s\n", sparkline(values))
		}
		return nil
	}}
	flags.add(cmd)
	cmd.Flags().StringVar(&older, "older-than", "30d", "stale snapshot threshold (e.g. 30d, 2w)")
	return cmd
}

func newAssessmentCapacityTrendCommand(a *App) *cobra.Command {
	var flags trendFlags
	var kind string
	cmd := &cobra.Command{Use: "capacity", Short: "Show compute and storage capacity trends", Example: `  # Compute and storage capacity across the stored history
  vsfleet assessment trends capacity

  # Datastores only, most recent 20 captures
  vsfleet assessment trends capacity --kind datastore --limit 20

  # Hosts in one context, as JSON
  vsfleet assessment trends capacity --kind host --context prod -o json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		opts, err := flags.options(cmd.Context(), s, a)
		if err != nil {
			return err
		}
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind != "all" && kind != "host" && kind != "cluster" && kind != "datastore" {
			return fmt.Errorf("--kind must be host, cluster, datastore, or all")
		}
		kinds := []string{kind}
		if kind == "all" {
			kinds = []string{"host", "cluster", "datastore"}
		}
		trend, err := s.CapacityTrend(cmd.Context(), opts, kinds)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), trend)
		}
		t := newTable(a.out(), "KIND", "SCOPE", "NAME", "RUN", "CPU CAP", "CPU USED", "CPU%", "MEM CAP", "MEM USED", "MEM%", "STORAGE", "USED", "FREE", "DS%")
		for _, series := range trend.Series {
			for _, p := range series.Points {
				t.row(series.Kind, series.Scope, dash(series.Name), strconv.FormatInt(p.Run.ID, 10), capacityCell(p.CPUCapacity), capacityCell(p.CPUUsed), percentCell(p.CPUUtilization), capacityCell(p.MemoryCapacity), capacityCell(p.MemoryUsed), percentCell(p.MemoryUtilization), capacityCell(p.StorageCapacity), capacityCell(p.StorageUsed), capacityCell(p.StorageFree), percentCell(p.StorageUtilization))
			}
		}
		t.flush()
		return nil
	}}
	flags.add(cmd)
	cmd.Flags().StringVar(&kind, "kind", "all", "resource kind: host, cluster, datastore, or all")
	return cmd
}

func newAssessmentReportCommand(a *App) *cobra.Command {
	var older string
	cmd := &cobra.Command{Use: "report [RUN]", Short: "Render a point-in-time assessment report", Example: `  # Report on the most recent capture
  vsfleet assessment report

  # Report on a labelled capture
  vsfleet assessment report pre-migration

  # Count snapshots older than 90 days as stale, as JSON
  vsfleet assessment report --older-than 90d -o json`, Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		selector := "latest"
		if len(args) == 1 {
			selector = args[0]
		}
		runID, err := s.ResolveRun(cmd.Context(), selector)
		if err != nil {
			return err
		}
		age, err := parseHumanDuration(older)
		if err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
		report, err := s.ReportForContexts(cmd.Context(), runID, age, a.StoredContextNames())
		if err != nil {
			return err
		}
		performance, err := s.PerformanceSummary(cmd.Context(), runID, a.StoredContextNames())
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), struct {
				assessment.AssessmentReport
				Performance []assessment.PerfSummary `json:"performance"`
			}{report, performance})
		}
		fmt.Fprintf(a.out(), "Assessment %d  %s  %s\n", report.Run.ID, report.Run.Status, report.Run.StartedAt.Local().Format("2006-01-02 15:04:05"))
		fmt.Fprintf(a.out(), "VMs: %d  Hosts: %d  Clusters: %d  Datastores: %d  Snapshots: %d (%d stale)\n", report.VMCount, report.HostCount, report.ClusterCount, report.DatastoreCount, report.SnapshotTotal, report.SnapshotStale)
		t := newTable(a.out(), "CONTEXT", "KIND", "STATUS", "ITEMS", "ERROR")
		for _, c := range report.Coverage {
			t.row(c.Context, c.Kind, c.Status, itoa(c.ItemCount), c.Error)
		}
		t.flush()
		printPerformanceSummary(a, performance)
		for _, w := range report.Warnings {
			fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, w)
		}
		return nil
	}}
	cmd.Flags().StringVar(&older, "older-than", "30d", "stale snapshot threshold (e.g. 30d, 2w)")
	return cmd
}

func newAssessmentPruneCommand(a *App) *cobra.Command {
	var older string
	var keepLast int
	var execute bool
	cmd := &cobra.Command{Use: "prune", Short: "Prune old assessment history", Long: strings.TrimSpace(`
Delete old captures from the local history database.

Pruning is a dry run unless --execute is passed, so the default output is a
preview of what would go. Pinned captures are never pruned.`), Example: `  # Preview what a prune would remove (nothing is deleted)
  vsfleet assessment prune --older-than 90d

  # Actually remove them, keeping the 5 newest complete captures
  vsfleet assessment prune --older-than 90d --keep-last 5 --execute`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := rejectStoredContextSelection(cmd, "prune"); err != nil {
			return err
		}
		d, err := parseHumanDuration(older)
		if err != nil || d <= 0 {
			if err == nil {
				err = fmt.Errorf("duration must be greater than zero")
			}
			return fmt.Errorf("--older-than: %w", err)
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		result, err := s.Prune(cmd.Context(), d, keepLast, execute)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), result)
		}
		mode := "dry-run"
		if execute {
			mode = "deleted"
		}
		fmt.Fprintf(a.out(), "assessment prune %s: %d candidate(s)\n", mode, len(result.Candidates))
		for _, c := range result.Candidates {
			fmt.Fprintf(a.out(), "  #%d %s %s (%s)\n", c.Run.ID, c.Run.Status, c.Run.StartedAt.Local().Format("2006-01-02 15:04:05"), humanBytes(c.Bytes))
		}
		if result.PerfWindows > 0 {
			fmt.Fprintf(a.out(), "performance windows older than the cutoff: %d (the newest per context is kept)\n", result.PerfWindows)
		}
		if execute {
			fmt.Fprintf(a.out(), "deleted: %d\n", result.Deleted)
			if result.PerfWindows > 0 {
				fmt.Fprintf(a.out(), "performance windows deleted: %d\n", result.PerfDeleted)
			}
		}
		return nil
	}}
	cmd.Flags().StringVar(&older, "older-than", "", "required age of runs to consider (e.g. 90d)")
	cmd.Flags().IntVar(&keepLast, "keep-last", 2, "preserve this many newest completed runs")
	cmd.Flags().BoolVar(&execute, "execute", false, "perform deletion (default is a dry run)")
	return cmd
}

func newAssessmentBackupCommand(a *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "backup FILE", Short: "Create a consistent SQLite history backup", Example: `  # Back the history database up to a file
  vsfleet assessment backup ./vsfleet-history.db

  # Overwrite an existing backup, e.g. from a nightly job
  vsfleet assessment backup ./vsfleet-history.db --force`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectStoredContextSelection(cmd, "backup"); err != nil {
			return err
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		if err := s.Backup(cmd.Context(), args[0], force); err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), map[string]string{"backup": args[0]})
		}
		fmt.Fprintf(a.out(), "history backup written to %s\n", args[0])
		return nil
	}}
	cmd.Flags().BoolVar(&force, "force", false, "replace an existing backup")
	return cmd
}

func newAssessmentRestoreCommand(a *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "restore FILE", Short: "Restore SQLite history from a backup", Example: `  # Restore into a fresh history database
  vsfleet assessment restore ./vsfleet-history.db

  # Replace the history database in place (destructive, so it must be confirmed)
  vsfleet assessment restore ./vsfleet-history.db --force`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectStoredContextSelection(cmd, "restore"); err != nil {
			return err
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		safety, err := s.Restore(cmd.Context(), args[0], force)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), map[string]string{"restored": args[0], "safety_backup": safety})
		}
		fmt.Fprintf(a.out(), "history restored from %s\npre-restore safety backup: %s\n", args[0], safety)
		return nil
	}}
	cmd.Flags().BoolVar(&force, "force", false, "confirm in-place restore")
	return cmd
}

func newAssessmentDoctorCommand(a *App) *cobra.Command {
	cmd := &cobra.Command{Use: "doctor", Short: "Check assessment database integrity", Example: `  # Check the local history database
  vsfleet assessment doctor

  # Check a specific database, as JSON
  vsfleet assessment doctor --history-db ./vsfleet-history.db -o json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := rejectStoredContextSelection(cmd, "doctor"); err != nil {
			return err
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		report, err := s.Doctor(cmd.Context())
		if err != nil {
			return err
		}
		if a.json() {
			if err := writeJSON(a.out(), report); err != nil {
				return err
			}
		} else {
			fmt.Fprintf(a.out(), "schema: %d  integrity: %s  foreign keys: %s\n", report.SchemaVersion, report.Integrity, report.ForeignKeys)
			fmt.Fprintf(a.out(), "runs: %d  VMs: %d  resources: %d  database: %s\n", report.Runs, report.VMObservations, report.ResourceObservations, humanBytes(report.DatabaseBytes))
			for _, warning := range report.Warnings {
				fmt.Fprintf(a.out(), "%s %s\n", glyphFail, warning)
			}
		}
		if len(report.Warnings) > 0 {
			return &doctorExitError{message: "assessment database checks reported warnings"}
		}
		return nil
	}}
	return cmd
}

func capacityCell(value *float64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatFloat(*value, 'f', 1, 64)
}

func percentCell(value *float64) string {
	if value == nil {
		return "—"
	}
	return strconv.FormatFloat(*value, 'f', 1, 64)
}

func sparkline(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	glyphs := []rune("▁▂▃▄▅▆▇█")
	min, max := values[0], values[0]
	for _, v := range values[1:] {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	if max == min {
		return strings.Repeat(string(glyphs[0]), len(values))
	}
	var b strings.Builder
	for _, v := range values {
		i := int((v - min) / (max - min) * float64(len(glyphs)-1))
		b.WriteRune(glyphs[i])
	}
	return b.String()
}

func newAssessmentRunCommand(a *App) *cobra.Command {
	var label, note string
	var pin bool
	var browseDatastores, includeLicenses bool
	var fileInventory bool
	var inventoryMaxFiles, inventoryMaxTotal int
	var inventoryTimeout time.Duration
	var failOnPartial bool
	cmd := &cobra.Command{Use: "run", Short: "Capture a point-in-time inventory assessment", Long: strings.TrimSpace(`
Read inventory from the selected contexts and store it as one immutable
capture in the local history database.

Apart from "perf collect", this is the only assessment subcommand that contacts
a vCenter; everything else reads back what it stored. A capture that reaches some contexts but not
all is stored as partial and says so.`), Example: `  # Capture the current context
  vsfleet assessment run

  # Capture the whole estate under a stable label
  vsfleet assessment run --all-contexts --label nightly

  # Also record datastore VMDK files, so "assessment orphans" has evidence
  vsfleet assessment run --all-contexts --browse-datastores

  # Also record license products, usage, expiration and host assignments for a
  # licensing review (keys are never stored or exported)
  vsfleet assessment run --all-contexts --include-licenses

  # Also list every datastore file, for the optional vFileInfo export tab.
  # Opt-in and bounded; exports filenames and paths (see docs/assessments.md)
  vsfleet assessment run --all-contexts --datastore-file-inventory

  # Pin a baseline so pruning never removes it
  vsfleet assessment run --all-contexts --label pre-migration --pin

  # Gate a scheduled job: exit 3 when a site was unreachable
  vsfleet assessment run --all-contexts --fail-on-partial`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		inventoryOptions, err := fileInventoryOptions(cmd, fileInventory, inventoryMaxFiles, inventoryMaxTotal, inventoryTimeout)
		if err != nil {
			return err
		}
		contexts, err := a.Contexts()
		if err != nil {
			return err
		}
		service, err := a.Assessment()
		if err != nil {
			return err
		}
		run, err := service.Capture(cmd.Context(), assessment.CaptureOptions{Contexts: contexts, Source: "cli", Label: label, Note: note, Pinned: pin, BrowseDatastores: browseDatastores, IncludeLicenses: includeLicenses, FileInventory: inventoryOptions, ToolVersion: version.String(), InventorySchemaVersion: assessment.CurrentInventorySchemaVersion, Progress: func(p assessment.ContextProgress) {
			if p.Error != nil {
				fmt.Fprintf(a.errOut(), "%s %s: %v\n", glyphFail, p.Context, p.Error)
			}
			for _, collection := range p.Collections {
				if collection.Kind == assessment.LicenseKind && collection.Status != "success" && collection.Status != "empty" {
					fmt.Fprintf(a.errOut(), "%s %s license collection %s: %v\n", glyphFail, p.Context, collection.Status, collection.Error)
				}
			}
		}})
		if err != nil {
			return err
		}
		var coverage []assessment.FileInventoryContext
		var browse assessment.BrowseSummary
		if inventoryOptions != nil || browseDatastores {
			// Read the stored evidence back rather than trusting in-memory
			// state, so what is reported is exactly what an export will say.
			if store, storeErr := a.History(); storeErr == nil {
				if data, loadErr := store.LoadExportData(cmd.Context(), run.ID); loadErr == nil {
					if inventoryOptions != nil {
						coverage = assessment.FileInventoryCoverage(data)
					}
					if browseDatastores {
						browse = assessment.SummarizeBrowse(data)
					}
				} else {
					fmt.Fprintf(a.errOut(), "%s capture coverage could not be read back: %v\n", glyphFail, loadErr)
				}
			}
		}
		if a.json() {
			payload := struct {
				assessment.Run
				DatastoreBrowse *assessment.BrowseSummary         `json:"datastore_browse,omitempty"`
				FileInventory   []assessment.FileInventoryContext `json:"file_inventory,omitempty"`
			}{Run: run, FileInventory: coverage}
			if browse.Requested {
				payload.DatastoreBrowse = &browse
			}
			if writeErr := writeJSON(a.out(), payload); writeErr != nil {
				return writeErr
			}
		} else {
			printRun(a.out(), run, browse.Note())
			printFileInventoryCoverage(a.out(), coverage)
		}
		for _, warning := range fileInventoryWarnings(coverage) {
			fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, warning)
		}
		if run.Status == assessment.RunFailed {
			return fmt.Errorf("assessment %d failed: no context returned VM inventory", run.ID)
		}
		if failOnPartial && run.Status == assessment.RunPartial {
			return &partialExitError{runID: run.ID, successful: run.SuccessfulContexts, total: run.RequestedContexts}
		}
		return nil
	}}
	cmd.Flags().StringVar(&label, "label", "", "stable label for this assessment (used as a selector)")
	cmd.Flags().StringVar(&note, "note", "", "operator note stored with this assessment")
	cmd.Flags().BoolVar(&pin, "pin", false, "pin this assessment against deletion")
	cmd.Flags().BoolVar(&browseDatastores, "browse-datastores", false, "record VM disk files from each accessible datastore (requires Datastore.Browse; adds time on large estates)")
	cmd.Flags().BoolVar(&includeLicenses, "include-licenses", false, "record license products, usage, expiration and host assignments (read-only; needs the Global.Licenses privilege; license keys are never stored)")
	cmd.Flags().BoolVar(&fileInventory, "datastore-file-inventory", false, "also list every file on each accessible datastore for the optional vFileInfo export tab (opt-in; needs Datastore.Browse; exports filenames and paths; bounded by the --file-inventory-* limits; independent of --browse-datastores)")
	cmd.Flags().IntVar(&inventoryMaxFiles, "file-inventory-max-files", vsphere.DefaultFileInventoryMaxFiles, "with --datastore-file-inventory: most file rows kept per datastore; a datastore that hits it is recorded as truncated, never complete")
	cmd.Flags().IntVar(&inventoryMaxTotal, "file-inventory-max-total-files", vsphere.DefaultFileInventoryMaxTotalFiles, "with --datastore-file-inventory: most file rows kept per context; datastores not reached are recorded as skipped")
	cmd.Flags().DurationVar(&inventoryTimeout, "file-inventory-timeout", vsphere.DefaultFileInventoryDatastoreTimeout, "with --datastore-file-inventory: time allowed to list one datastore before it is recorded as failed (each context also gets three times this, at most)")
	cmd.Flags().BoolVar(&failOnPartial, "fail-on-partial", false, "exit 3 when the capture stored evidence from some contexts but not all")
	return cmd
}

func newAssessmentListCommand(a *App) *cobra.Command {
	return &cobra.Command{Use: "list", Aliases: []string{"ls"}, Short: "List stored assessments", Long: strings.TrimSpace(`
List the captures in the local history database, newest first.

The ID and label columns are what every other subcommand accepts as its RUN
argument, alongside the selector "latest".`), Example: `  # Every stored capture, newest first
  vsfleet assessment list

  # As JSON, to pick a run ID in a script
  vsfleet assessment list -o json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := rejectStoredContextSelection(cmd, "list"); err != nil {
			return err
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		runs, err := s.Runs(cmd.Context())
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), runs)
		}
		t := newTable(a.out(), "ID", "LABEL", "SOURCE", "STARTED", "STATUS", "CONTEXTS", "SUCCESSFUL")
		for _, r := range runs {
			label := r.Label
			if r.Pinned {
				label = "📌 " + label
			}
			t.row(strconv.FormatInt(r.ID, 10), label, dash(r.Source), r.StartedAt.Local().Format("2006-01-02 15:04:05"), string(r.Status), itoa(r.RequestedContexts), itoa(r.SuccessfulContexts))
		}
		t.flush()
		return nil
	}}
}

func newAssessmentDiffCommand(a *App) *cobra.Command {
	var runtime bool
	var failOn []string
	var maxAge string
	var requireComplete bool
	cmd := &cobra.Command{Use: "diff [BASE] [TARGET]", Short: "Compare two assessments", Long: strings.TrimSpace(`
Compare two stored captures and report what changed between them.

BASE and TARGET are assessment IDs or labels from "vsfleet assessment list",
or the selector "latest". With no arguments the two most recent captures are
compared; with one, it is compared against the most recent.

Volatile runtime fields — power state, guest, IP, tools, storage — are left
out unless --include-runtime is passed, so ordinary churn does not drown the
structural changes.`), Example: `  # The two most recent captures
  vsfleet assessment diff

  # A labelled baseline against the most recent capture
  vsfleet assessment diff pre-migration latest

  # Two captures by ID, including volatile runtime fields
  vsfleet assessment diff 41 42 --include-runtime

  # Gate a pipeline: exit 2 when VMs were removed or networks changed
  vsfleet assessment diff --fail-on removed,network-changed

  # Refuse to report on captures that are not fully comparable
  vsfleet assessment diff --require-complete`, Args: cobra.MaximumNArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		base, target, err := resolveRunPair(cmd.Context(), s, args)
		if err != nil {
			return err
		}
		d, err := s.DiffForContexts(cmd.Context(), base, target, runtime, a.StoredContextNames())
		if err != nil {
			return err
		}
		age, err := parseHumanDuration(maxAge)
		if err != nil {
			return fmt.Errorf("--max-snapshot-age: %w", err)
		}
		for _, rule := range failOn {
			if !validPolicyRule(rule) {
				return fmt.Errorf("unknown --fail-on rule %q", rule)
			}
		}
		if len(failOn) > 0 || age > 0 || requireComplete {
			policy := assessment.EvaluatePolicy(d, assessment.PolicyOptions{FailOn: failOn, MaxSnapshotAge: age, RequireComplete: requireComplete})
			d.Policy = &policy
		}
		if a.json() {
			if err := writeJSON(a.out(), d); err != nil {
				return err
			}
			if d.Policy != nil && !d.Policy.Passed {
				return &policyExitError{violations: d.Policy.Violations}
			}
			return nil
		}
		fmt.Fprintf(a.out(), "Assessment %d → %d  (%s → %s)\n", d.Base.ID, d.Target.ID, d.Base.Status, d.Target.Status)
		fmt.Fprintf(a.out(), "Appeared: %d  Vanished: %d  Moved: %d  Modified: %d  Resources: %d  Snapshots: %d\n", d.Counts.Appeared, d.Counts.Vanished, d.Counts.Moved, d.Counts.Modified, d.Counts.Resources, d.Counts.Snapshots)
		t := newTable(a.out(), "CHANGE", "RESOURCE", "CONTEXT", "DETAIL")
		for _, v := range d.VMs {
			t.row(strings.Join(v.Changes, ","), v.Name, v.Context, changeDetail(v))
		}
		for _, v := range d.Snapshots {
			t.row("snapshot "+v.Kind, v.VMName, v.Context, v.Name)
		}
		for _, r := range d.Resources {
			t.row(strings.Join(r.Changes, ","), r.Kind+"/"+r.Name, r.Context, resourceChangeDetail(r))
		}
		t.flush()
		for _, w := range d.Warnings {
			fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, w)
		}
		if d.Policy != nil {
			if d.Policy.Passed {
				fmt.Fprintln(a.out(), "policy: PASS")
			} else {
				for _, v := range d.Policy.Violations {
					fmt.Fprintf(a.out(), "policy: FAIL [%s] %s\n", v.Rule, v.Message)
				}
				return &policyExitError{violations: d.Policy.Violations}
			}
		}
		return nil
	}}
	cmd.Flags().BoolVar(&runtime, "include-runtime", false, "include volatile power, guest, IP, tools, and storage changes")
	cmd.Flags().StringSliceVar(&failOn, "fail-on", nil, "fail with exit code 2 when a change category appears (repeat or comma-separate)")
	cmd.Flags().StringVar(&maxAge, "max-snapshot-age", "", "fail when a target snapshot is at least this old (e.g. 30d, 2w)")
	cmd.Flags().BoolVar(&requireComplete, "require-complete", false, "fail unless both runs are complete and fully comparable")
	return cmd
}

func newAssessmentSnapshotsCommand(a *App) *cobra.Command {
	var at int64
	var older string
	cmd := &cobra.Command{Use: "snapshots", Short: "Show snapshot ages in an assessment", Example: `  # Snapshots in the most recent capture
  vsfleet assessment snapshots

  # Only snapshots at least 90 days old
  vsfleet assessment snapshots --older-than 90d

  # Snapshots in a specific capture, as JSON
  vsfleet assessment snapshots --at 42 -o json`, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		s, err := a.History()
		if err != nil {
			return err
		}
		if at == 0 {
			at, err = resolveRunPairTarget(cmd.Context(), s)
			if err != nil {
				return err
			}
		} else if _, err := s.GetRun(cmd.Context(), at); err != nil {
			return err
		}
		olderDuration, err := parseHumanDuration(older)
		if err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
		ages, err := s.SnapshotAgesForContexts(cmd.Context(), at, olderDuration, a.StoredContextNames())
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), ages)
		}
		t := newTable(a.out(), "VM", "CONTEXT", "SNAPSHOT", "CREATED", "AGE", "FIRST SEEN", "LAST SEEN")
		for _, v := range ages {
			t.row(v.VMName, v.Context, v.Name, v.CreateTime.Local().Format("2006-01-02"), humanize.Age(v.Age), v.FirstSeen.Local().Format("2006-01-02"), v.LastSeen.Local().Format("2006-01-02"))
		}
		t.flush()
		return nil
	}}
	cmd.Flags().Int64Var(&at, "at", 0, "assessment ID (default: latest)")
	cmd.Flags().StringVar(&older, "older-than", "", "only snapshots at least this old (e.g. 30d, 2w)")
	return cmd
}

func newAssessmentDeleteCommand(a *App) *cobra.Command {
	var force bool
	cmd := &cobra.Command{Use: "delete RUN", Short: "Delete one stored assessment", Example: `  # Delete one capture by ID (deletion must be confirmed)
  vsfleet assessment delete 42 --force

  # Delete a labelled capture
  vsfleet assessment delete nightly --force`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectStoredContextSelection(cmd, "delete"); err != nil {
			return err
		}
		if !force {
			return fmt.Errorf("deleting assessment history requires --force")
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		if err := s.DeleteRunSelector(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Fprintf(a.out(), "deleted assessment %s\n", args[0])
		return nil
	}}
	cmd.Flags().BoolVar(&force, "force", false, "confirm deletion")
	return cmd
}

func newAssessmentUpdateCommand(a *App) *cobra.Command {
	var label, note string
	var pin, unpin bool
	cmd := &cobra.Command{Use: "update RUN", Short: "Update assessment label, note, or pin", Example: `  # Label a capture so other commands can select it by name
  vsfleet assessment update 42 --label pre-migration

  # Pin it against pruning, with a note
  vsfleet assessment update 42 --pin --note "baseline for the Q3 migration"

  # Clear the note and unpin
  vsfleet assessment update 42 --note "" --unpin`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := rejectStoredContextSelection(cmd, "update"); err != nil {
			return err
		}
		if pin && unpin {
			return fmt.Errorf("--pin and --unpin cannot be combined")
		}
		if !cmd.Flags().Changed("label") && !cmd.Flags().Changed("note") && !pin && !unpin {
			return fmt.Errorf("at least one metadata change is required")
		}
		s, err := a.History()
		if err != nil {
			return err
		}
		var lp, np *string
		var pp *bool
		if cmd.Flags().Changed("label") {
			lp = &label
		}
		if cmd.Flags().Changed("note") {
			np = &note
		}
		if pin || unpin {
			v := pin
			pp = &v
		}
		run, err := s.UpdateRunFields(cmd.Context(), args[0], lp, np, pp)
		if err != nil {
			return err
		}
		if a.json() {
			return writeJSON(a.out(), run)
		}
		printRun(a.out(), run)
		return nil
	}}
	cmd.Flags().StringVar(&label, "label", "", "set the run label (empty clears it)")
	cmd.Flags().StringVar(&note, "note", "", "set the run note (empty clears it)")
	cmd.Flags().BoolVar(&pin, "pin", false, "pin the run")
	cmd.Flags().BoolVar(&unpin, "unpin", false, "unpin the run")
	return cmd
}

func resolveRunPair(ctx context.Context, s *assessment.Store, args []string) (int64, int64, error) {
	runs, err := s.Runs(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(runs) < 2 {
		return 0, 0, fmt.Errorf("need at least two assessments to compare")
	}
	if len(args) == 0 {
		return runs[1].ID, runs[0].ID, nil
	}
	if len(args) == 1 {
		target, err := runSelector(args[0], runs)
		if err != nil {
			return 0, 0, err
		}
		for _, r := range runs {
			if r.ID != target && r.StartedAt.Before(targetTime(runs, target)) {
				return r.ID, target, nil
			}
		}
		return 0, 0, fmt.Errorf("no baseline assessment before %s", args[0])
	}
	base, err := runSelector(args[0], runs)
	if err != nil {
		return 0, 0, err
	}
	target, err := runSelector(args[1], runs)
	if err != nil {
		return 0, 0, err
	}
	return base, target, nil
}
func resolveRunPairTarget(ctx context.Context, s *assessment.Store) (int64, error) {
	runs, err := s.Runs(ctx)
	if err != nil {
		return 0, err
	}
	if len(runs) == 0 {
		return 0, fmt.Errorf("no assessments stored")
	}
	return runs[0].ID, nil
}
func runSelector(v string, runs []assessment.Run) (int64, error) {
	if v == "latest" {
		return runs[0].ID, nil
	}
	if v == "previous" {
		if len(runs) < 2 {
			return 0, fmt.Errorf("no previous assessment")
		}
		return runs[1].ID, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		for _, r := range runs {
			if r.Label != "" && strings.EqualFold(r.Label, v) {
				return r.ID, nil
			}
		}
		return 0, fmt.Errorf("assessment selector %q is not a number, label, latest, or previous", v)
	}
	for _, r := range runs {
		if r.ID == id {
			return id, nil
		}
	}
	return 0, fmt.Errorf("assessment %d not found", id)
}

var durationToken = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)([smhdw])`)

var byteToken = regexp.MustCompile(`(?i)^([0-9]+(?:\.[0-9]+)?)\s*(b|k|kb|ki|kib|m|mb|mi|mib|g|gb|gi|gib|t|tb|ti|tib|p|pb|pi|pib)?$`)

func parseHumanDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(value); err == nil {
		return d, nil
	}
	matches := durationToken.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	var total time.Duration
	consumed := ""
	for _, m := range matches {
		consumed += m[0]
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, err
		}
		unit := map[string]float64{"s": float64(time.Second), "m": float64(time.Minute), "h": float64(time.Hour), "d": float64(24 * time.Hour), "w": float64(7 * 24 * time.Hour)}[strings.ToLower(m[2])]
		total += time.Duration(n * unit)
	}
	if consumed != value {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return total, nil
}

// validatePercent rejects a flag value that is not a finite percentage in
// [0, 100], including NaN and +/-Inf, which ordinary range comparisons let
// through silently. flag is the flag's own name (e.g. "--min-free") for the
// error message.
func validatePercent(flag string, value float64) error {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 100 {
		return fmt.Errorf("%s must be between 0 and 100", flag)
	}
	return nil
}

func parseHumanBytes(value string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	matches := byteToken.FindStringSubmatch(value)
	if len(matches) == 0 {
		return 0, fmt.Errorf("invalid byte size %q", value)
	}
	n, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return 0, err
	}
	power := 0
	switch strings.ToLower(matches[2]) {
	case "", "b":
		power = 0
	case "k", "kb", "ki", "kib":
		power = 1
	case "m", "mb", "mi", "mib":
		power = 2
	case "g", "gb", "gi", "gib":
		power = 3
	case "t", "tb", "ti", "tib":
		power = 4
	case "p", "pb", "pi", "pib":
		power = 5
	}
	for i := 0; i < power; i++ {
		n *= 1024
	}
	return n, nil
}

func validPolicyRule(rule string) bool {
	switch rule {
	case "appeared", "vanished", "moved", "modified", "snapshot-created", "snapshot-removed", "snapshot-changed":
		return true
	}
	if strings.HasPrefix(rule, "host-") || strings.HasPrefix(rule, "cluster-") || strings.HasPrefix(rule, "datastore-") {
		kind, change := strings.SplitN(rule, "-", 2)[0], strings.SplitN(rule, "-", 2)[1]
		return (kind == "host" || kind == "cluster" || kind == "datastore") && (change == "appeared" || change == "vanished" || change == "modified")
	}
	return false
}

func targetTime(runs []assessment.Run, id int64) time.Time {
	for _, r := range runs {
		if r.ID == id {
			return r.StartedAt
		}
	}
	return time.Time{}
}
func printRun(out interface{ Write([]byte) (int, error) }, r assessment.Run, notes ...string) {
	label := ""
	if r.Label != "" {
		label = " label=" + r.Label
	}
	if r.Pinned {
		label += " pinned"
	}
	detail := fmt.Sprintf("%d/%d contexts successful", r.SuccessfulContexts, r.RequestedContexts)
	for _, note := range notes {
		if note != "" {
			detail += ", " + note
		}
	}
	fmt.Fprintf(out, "assessment %d: %s%s (%s)\n", r.ID, r.Status, label, detail)
}
func changeDetail(v assessment.VMChange) string {
	if len(v.Fields) == 0 {
		return ""
	}
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.Field + ":" + f.Before + "→" + f.After
	}
	return strings.Join(parts, " ")
}

func resourceChangeDetail(v assessment.ResourceChange) string {
	if len(v.Fields) == 0 {
		return ""
	}
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.Field + ":" + f.Before + "→" + f.After
	}
	return strings.Join(parts, " ")
}

// fileInventoryOptions turns the opt-in flag and its limits into capture
// options. It returns nil, which disables the inventory entirely, unless
// --datastore-file-inventory was given: a limit flag on its own is an error,
// never a quiet way to switch datastore browsing on.
func fileInventoryOptions(cmd *cobra.Command, enabled bool, maxFiles, maxTotal int, timeout time.Duration) (*vsphere.FileInventoryOptions, error) {
	limitFlags := []string{"file-inventory-max-files", "file-inventory-max-total-files", "file-inventory-timeout"}
	if !enabled {
		for _, name := range limitFlags {
			if cmd.Flags().Changed(name) {
				return nil, fmt.Errorf("--%s has no effect without --datastore-file-inventory; datastore file inventory is opt-in", name)
			}
		}
		return nil, nil
	}
	switch {
	case maxFiles < 1 || maxFiles > vsphere.MaxFileInventoryMaxFiles:
		return nil, fmt.Errorf("--file-inventory-max-files must be between 1 and %d", vsphere.MaxFileInventoryMaxFiles)
	case maxTotal < 1 || maxTotal > vsphere.MaxFileInventoryMaxTotalFiles:
		return nil, fmt.Errorf("--file-inventory-max-total-files must be between 1 and %d", vsphere.MaxFileInventoryMaxTotalFiles)
	case timeout < time.Second:
		return nil, fmt.Errorf("--file-inventory-timeout must be at least 1s")
	}
	return &vsphere.FileInventoryOptions{
		MaxFilesPerDatastore: maxFiles,
		MaxFilesPerContext:   maxTotal,
		DatastoreTimeout:     timeout,
		ContextTimeout:       3 * timeout,
	}, nil
}

// printFileInventoryCoverage prints one line per context and one per
// datastore that is not complete.
func printFileInventoryCoverage(out io.Writer, coverage []assessment.FileInventoryContext) {
	for _, c := range coverage {
		if !c.Requested {
			continue
		}
		status, message := c.Summary()
		fmt.Fprintf(out, "  file inventory %s: %s, %d files", c.Context, status, c.Files())
		if message != "" {
			fmt.Fprintf(out, " (%s)", message)
		}
		fmt.Fprintln(out)
		for _, d := range c.Gaps() {
			fmt.Fprintf(out, "    %s: %s, %d files captured: %s\n", d.Datastore, strings.ToUpper(d.Status), d.Files, d.Message)
		}
	}
}

// fileInventoryWarnings names every datastore whose file list is incomplete.
// A datastore with no rows must never read as "no files".
func fileInventoryWarnings(coverage []assessment.FileInventoryContext) []string {
	var warnings []string
	for _, c := range coverage {
		if !c.Requested {
			continue
		}
		for _, d := range c.Gaps() {
			warnings = append(warnings, fmt.Sprintf("%s vFileInfo: datastore %s is %s (%d files captured): %s; absence of a file here is not evidence it does not exist", c.Context, d.Datastore, d.Status, d.Files, d.Message))
		}
	}
	return warnings
}
