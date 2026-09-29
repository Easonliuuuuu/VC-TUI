package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type orphanFlags struct {
	confidence    []string
	minSize       int64
	failOnUnknown bool
}

// orphanCoverageExitError reports that the orphan scan could not cover the whole
// datastore scope, so "no candidates" is not proof of a clean estate. It is
// raised only under --fail-on-unknown.
type orphanCoverageExitError struct{ gaps int }

func (e *orphanCoverageExitError) Error() string {
	if e.gaps == 0 {
		return "orphan scan coverage is incomplete: no datastore browse evidence"
	}
	return fmt.Sprintf("orphan scan coverage is incomplete (%d datastore(s) not fully browsed)", e.gaps)
}
func (e *orphanCoverageExitError) ExitCode() int { return 2 }

func newAssessmentOrphansCommand(a *App) *cobra.Command {
	var flags orphanFlags
	cmd := &cobra.Command{
		Use:   "orphans [RUN]",
		Short: "Explain browsed datastore VMDK orphan evidence",
		Long: strings.TrimSpace(`
Explain the VMDK files found on datastores that no registered VM claims, with
the confidence behind each candidate.

The evidence comes from datastore browsing, which only "assessment run
--browse-datastores" records. Without it there is nothing to explain, and
coverage gaps are reported separately: "no candidates" is not proof of a clean
estate unless the scan reached every datastore.`),
		Example: `  # Capture the evidence, then explain it
  vsfleet assessment run --all-contexts --browse-datastores
  vsfleet assessment orphans

  # Only high-confidence candidates over 10 GiB
  vsfleet assessment orphans --confidence verified --min-size 10737418240

  # Gate a cleanup pipeline on complete coverage
  vsfleet assessment orphans --fail-on-unknown -o json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for i := range flags.confidence {
				flags.confidence[i] = strings.ToLower(strings.TrimSpace(flags.confidence[i]))
				switch health.Confidence(flags.confidence[i]) {
				case health.ConfidenceVerified, health.ConfidenceSuspected, health.ConfidenceOtherContext, health.ConfidenceUnknown:
				default:
					return fmt.Errorf("unknown --confidence %q", flags.confidence[i])
				}
			}
			if flags.minSize < 0 {
				return fmt.Errorf("--min-size must be zero or greater")
			}
			data, err := loadRunExportData(cmd, a, args)
			if err != nil {
				return err
			}
			report := health.Orphans(data)
			report.Entries = filterOrphans(report.Entries, flags)
			printOrphanCoverage(a.errOut(), report.Coverage)
			if a.json() {
				if err := writeJSON(a.out(), report); err != nil {
					return err
				}
			} else {
				printOrphans(a, report)
			}
			if flags.failOnUnknown && !report.Coverage.Complete() {
				return &orphanCoverageExitError{gaps: len(report.Coverage.Gaps)}
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&flags.confidence, "confidence", nil, "only show confidence tier (repeat or comma-separate)")
	cmd.Flags().Int64Var(&flags.minSize, "min-size", 0, "only show files at least this many bytes")
	cmd.Flags().BoolVar(&flags.failOnUnknown, "fail-on-unknown", false, "exit 2 when any datastore was not fully browsed")
	return cmd
}

// printOrphanCoverage names every datastore that could not be fully browsed, so
// an empty candidate list is never read as a clean estate.
func printOrphanCoverage(out io.Writer, coverage health.OrphanCoverage) {
	for _, gap := range coverage.Gaps {
		message := fmt.Sprintf("%s / %s not evaluated (%s)", gap.Object.Context, gap.Object.Name, gap.Status)
		if gap.Reason != "" {
			message += ": " + gap.Reason
		}
		fmt.Fprintf(out, "%s %s\n", glyphFail, message)
	}
}

func filterOrphans(entries []health.OrphanEvidence, flags orphanFlags) []health.OrphanEvidence {
	allowed := make(map[health.Confidence]bool, len(flags.confidence))
	for _, value := range flags.confidence {
		allowed[health.Confidence(strings.ToLower(strings.TrimSpace(value)))] = true
	}
	out := make([]health.OrphanEvidence, 0, len(entries))
	for _, entry := range entries {
		if flags.minSize > 0 && entry.SizeBytes < flags.minSize {
			continue
		}
		if len(allowed) > 0 && !allowed[entry.Confidence] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// orphanPath is the file as the operator would type it. The stored path already
// carries its "[datastore]" prefix; only a bare relative path needs one added.
func orphanPath(entry health.OrphanEvidence) string {
	if _, _, ok := vsphere.SplitDatastorePath(entry.Path); ok {
		return entry.Path
	}
	return "[" + entry.Object.Name + "] " + entry.Path
}

func printOrphans(a *App, report health.OrphanReport) {
	if len(report.Entries) == 0 {
		fmt.Fprintln(a.out(), orphanEmptyMessage(report.Coverage))
		return
	}
	for i, entry := range report.Entries {
		if i > 0 {
			fmt.Fprintln(a.out())
		}
		fmt.Fprintf(a.out(), "[%s] %s  %s  %s\n", orphanLabel(entry.Confidence), orphanPath(entry), humanBytes(entry.SizeBytes), orphanDate(entry.Modified))
		fields := newFields(a.out())
		if len(entry.ReferencedBy) == 0 {
			fields.add("not referenced by", strings.Join(entry.CheckedContexts, ", "))
		} else {
			for _, ref := range entry.ReferencedBy {
				label := "referenced by"
				if ref.Template {
					label = "referenced by template"
				}
				fields.add(label, fmt.Sprintf("%s @ %s", ref.VM, ref.Context))
			}
		}
		for _, blind := range entry.Blind {
			fields.add("could not verify", blind.Context+" — "+blind.Reason)
		}
		if len(entry.Identity) > 0 {
			fields.add("shared datastore", entry.Object.Name+" / "+strings.Join(entry.Identity, ", "))
		}
		if len(entry.Reasons) > 0 {
			fields.add("reason", strings.Join(entry.Reasons, "; "))
		}
		fields.flush()
	}
}

// orphanEmptyMessage renders the no-candidates line so that an incomplete scan
// is never reported as a clean estate.
func orphanEmptyMessage(coverage health.OrphanCoverage) string {
	switch {
	case coverage.Datastores == 0:
		return "No datastore inventory in this assessment; orphan scan NOT EVALUATED."
	case coverage.Browsed == 0:
		return "No datastore browse evidence; orphan scan NOT EVALUATED (capture with --browse-datastores)."
	case !coverage.Complete():
		return fmt.Sprintf("No orphan candidates in %d of %d browsed datastore(s); %d NOT EVALUATED (see warnings above).", coverage.Browsed, coverage.Datastores, len(coverage.Gaps))
	default:
		return "No browsed VMDK orphan candidates."
	}
}

func orphanLabel(confidence health.Confidence) string {
	switch confidence {
	case health.ConfidenceVerified:
		return "VERIFIED"
	case health.ConfidenceSuspected:
		return "SUSPECTED"
	case health.ConfidenceOtherContext:
		return "IN USE"
	default:
		return "UNKNOWN"
	}
}

func orphanDate(value time.Time) string {
	if value.IsZero() {
		return "unknown date"
	}
	return value.Local().Format("2006-01-02")
}
