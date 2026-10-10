package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/report"
)

func newCompatibilityCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:     "compatibility",
		Aliases: []string{"compat"},
		Short:   "Describe what the export profile writes",
		Example: `  # Every worksheet and column the RVTools export writes
  vsfleet compatibility report

  # One worksheet, as JSON, for a pipeline to validate against
  vsfleet compatibility report --sheet vInfo -o json`,
	})
	cmd.AddCommand(newCompatibilityReportCommand(a))
	return cmd
}

func newCompatibilityReportCommand(a *App) *cobra.Command {
	var sheet string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Report every worksheet and column the rvtools export writes",
		Example: `  # Every worksheet and column the export writes
  vsfleet compatibility report

  # One worksheet
  vsfleet compatibility report --sheet vInfo

  # As JSON, for a pipeline to validate its own requirements against
  vsfleet compatibility report -o json`,
		Long: strings.TrimSpace(`
Report every worksheet the rvtools export profile writes, with each column's
type, unit, and when it is left empty.

It describes what vsfleet emits and what those values mean, including observed
differences from RVTools in shared columns. It does not describe the
complete RVTools schema. Reads no configuration, opens no keyring, and contacts
no vCenter.`),
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			profile, err := report.Profile()
			if err != nil {
				return err
			}
			if sheet != "" {
				profile, err = filterProfile(profile, sheet)
				if err != nil {
					return err
				}
			}
			if a.json() {
				return writeJSON(a.out(), profile)
			}
			printCompatibilityProfile(a, profile)
			return nil
		},
	}
	cmd.Flags().StringVar(&sheet, "sheet", "", "describe one worksheet instead of all of them")
	return cmd
}

// filterProfile narrows the profile to one worksheet, matched without regard
// to case so "vinfo" finds vInfo.
func filterProfile(profile []report.SheetSpec, sheet string) ([]report.SheetSpec, error) {
	for _, spec := range profile {
		if strings.EqualFold(spec.Name, sheet) {
			return []report.SheetSpec{spec}, nil
		}
	}
	names := make([]string, 0, len(profile))
	for _, spec := range profile {
		names = append(names, spec.Name)
	}
	return nil, fmt.Errorf("unknown worksheet %q (this profile writes: %s)", sheet, strings.Join(names, ", "))
}

func printCompatibilityProfile(a *App, profile []report.SheetSpec) {
	out := a.out()
	for i, spec := range profile {
		if i > 0 {
			fmt.Fprintln(out)
		}
		origin := "vsfleet extension"
		if spec.Compatible {
			origin = "RVTools-named layout"
		}
		fmt.Fprintf(out, "%s  (%d columns, %s, from %s)\n", spec.Name, len(spec.Columns), origin, spec.DerivesFrom)
		if spec.Note != "" {
			for _, line := range wrapText(spec.Note, 76) {
				fmt.Fprintf(out, "  %s\n", line)
			}
		}
		fmt.Fprintln(out)
		// A column's explanation is a sentence, not a cell: in a table it
		// either wraps into illegibility or pushes every other column off an
		// 80-column terminal. Each column gets its own block instead, so the
		// output stays readable at any width and diffs cleanly between
		// releases.
		for _, col := range spec.Columns {
			kind := col.Kind
			if col.Unit != "" {
				kind += ", " + col.Unit
			}
			fmt.Fprintf(out, "  %s  [%s]\n", col.Name, kind)
			if col.Note != "" {
				for _, line := range wrapText(col.Note, 72) {
					fmt.Fprintf(out, "      %s\n", line)
				}
			}
			if col.Empty != "" {
				for _, line := range wrapText("Empty "+col.Empty+".", 72) {
					fmt.Fprintf(out, "      %s\n", line)
				}
			}
		}
	}
}

// wrapText breaks s into lines of at most width characters, so a worksheet's
// explanation stays readable in a terminal without the table below it being
// pushed sideways.
func wrapText(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}
