package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/query"
	"github.com/easonliuuuuu/vsfleet/internal/search"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newSearchCommand(a *App) *cobra.Command {
	var (
		kinds []string
		limit int
		where []string
		tags  []string
		wide  bool
	)
	cmd := &cobra.Command{
		Use:   "search [text]",
		Short: "Search every vCenter at once",
		Example: `  # Find a name anywhere in the estate
  vsfleet search ubuntu-golden --all-contexts

  # Restrict to one kind and cap the result count
  vsfleet search nvme --kind datastore --limit 20

  # Several kinds at once, as JSON
  vsfleet search web --kind vm,template -o json`,
		Long: `Search every configured vCenter for objects whose name contains the optional text, or narrow the search with structured metadata predicates.

This is the question that is genuinely hard to answer today: which of the
estate's vCenters holds that template, and where. Each vCenter is queried
concurrently and independently, so one that is unreachable costs one line of
output rather than the whole answer.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parsed := make([]vsphere.Kind, 0, len(kinds))
			for _, k := range kinds {
				kind, err := vsphere.ParseKind(strings.ToLower(strings.TrimSpace(k)))
				if err != nil {
					return err
				}
				parsed = append(parsed, kind)
			}
			expressions := append([]string(nil), where...)
			for _, tag := range tags {
				tag = strings.TrimSpace(tag)
				if tag == "" {
					return fmt.Errorf("--tag cannot be empty")
				}
				if i := strings.Index(tag, "/"); i > 0 {
					expressions = append(expressions, "tag."+tag[:i]+"="+tag[i+1:])
				} else {
					expressions = append(expressions, "tag="+tag)
				}
			}
			filter, err := query.Parse(expressions, parsed)
			if err != nil {
				return fmt.Errorf("query: %w", err)
			}
			text := ""
			if len(args) == 1 {
				text = args[0]
			}
			if strings.TrimSpace(text) == "" && len(expressions) == 0 {
				return fmt.Errorf("search requires text, --tag, or --where")
			}
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			// Search spans the whole estate unless the operator narrows it.
			contexts, err := cfg.Resolve(a.ContextNames, a.AllContexts || len(a.ContextNames) == 0)
			if err != nil {
				return err
			}
			res := search.Search(cmd.Context(), a.Sessions(), contexts, text, search.Options{
				Kinds:   parsed,
				Limit:   limit,
				Timeout: a.Timeout,
				Filter:  filter,
			})
			if a.json() {
				return writeJSON(a.out(), searchJSON(res))
			}
			printSearch(a, res, wide)
			if len(res.Matches) == 0 && len(res.Failures) > 0 {
				return fmt.Errorf("no vCenter could be searched")
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "restrict to these kinds: vm, template, host, cluster, vapp, datastore, network")
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of matches to show")
	cmd.Flags().StringArrayVar(&where, "where", nil, "structured predicate (repeatable; predicates are ANDed)")
	cmd.Flags().StringArrayVar(&tags, "tag", nil, "exact tag name or CATEGORY/NAME (repeatable)")
	cmd.Flags().BoolVar(&wide, "wide", false, "include tags and custom attributes")
	return cmd
}

func printSearch(a *App, res *search.Results, wide bool) {
	out := a.out()
	if len(res.Matches) == 0 {
		if strings.TrimSpace(res.Query) == "" {
			fmt.Fprintf(out, "No matches in %d vCenter(s).\n", res.Searched)
		} else {
			fmt.Fprintf(out, "No matches for %q in %d vCenter(s).\n", res.Query, res.Searched)
		}
	} else {
		headers := []string{"VCENTER", "TYPE", "NAME", "DATACENTER", "PATH"}
		if wide {
			headers = append(headers, "TAGS", "CUSTOM ATTRIBUTES")
		}
		t := newTable(out, headers...)
		for _, m := range res.Matches {
			row := []string{m.Context, string(m.Kind), m.Name, dash(m.Datacenter), dash(m.Path)}
			if wide {
				row = append(row, dash(metadataTags(m.Metadata)), dash(metadataAttributes(m.Metadata)))
			}
			t.row(row...)
		}
		t.flush()
		fmt.Fprintf(out, "\n%d match(es) across %d vCenter(s) in %s\n", len(res.Matches), res.Searched, humanDuration(res.Elapsed))
	}
	for _, f := range res.Failures {
		fmt.Fprintf(a.errOut(), "%s %s: %v\n", glyphFail, f.Context, f.Err)
	}
	seen := map[string]bool{}
	for _, m := range res.Matches {
		for _, item := range [][3]string{{"tags", m.Metadata.TagsStatus, m.Metadata.TagsError}, {"custom attributes", m.Metadata.CustomAttributesStatus, m.Metadata.CustomAttributesError}} {
			source, status, message := item[0], item[1], item[2]
			if line, ok := metadataWarning(source, status, message); ok && !seen[line] {
				fmt.Fprintf(a.errOut(), "%s %s\n", glyphFail, line)
				seen[line] = true
			}
		}
	}
}

func searchJSON(res *search.Results) map[string]any {
	matches := make([]map[string]any, 0, len(res.Matches))
	for _, m := range res.Matches {
		matches = append(matches, map[string]any{
			"context":     m.Context,
			"kind":        string(m.Kind),
			"id":          m.ID,
			"name":        m.Name,
			"datacenter":  m.Datacenter,
			"path":        m.Path,
			"description": m.Description,
			"metadata":    m.Metadata,
		})
	}
	failures := make([]map[string]any, 0, len(res.Failures))
	for _, f := range res.Failures {
		failures = append(failures, map[string]any{"context": f.Context, "error": f.Err.Error()})
	}
	return map[string]any{
		"query":      res.Query,
		"matches":    matches,
		"failures":   failures,
		"searched":   res.Searched,
		"elapsed_ms": res.Elapsed.Milliseconds(),
	}
}
