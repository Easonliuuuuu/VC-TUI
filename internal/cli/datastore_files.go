package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// datastoreBrowseResult bundles one directory listing (or search result) with
// the extra context a VMDK entry's relationship column needs: the live VM
// disk references on the datastore, and the most recently stored assessment,
// if any. Both are loaded once per command, not once per file, and only when
// the result actually contains a VMDK — most directories do not.
type datastoreBrowseResult struct {
	listing        vsphere.DatastoreListing
	refs           vsphere.DatastoreReferenceListing
	data           assessment.ExportData
	haveAssessment bool
	datastore      vsphere.Datastore
}

// newDatastoreFilesCommand exposes the read-only datastore browser and
// recursive search the terminal interface already offers, so datastore
// investigation is scriptable outside the TUI. It calls the same
// vsphere.Client methods the TUI does rather than a parallel traversal, so a
// permission change or a new search flag only has to be taught once.
func newDatastoreFilesCommand(a *App) *cobra.Command {
	cmd := requireSubcommand(&cobra.Command{
		Use:   "files",
		Short: "Browse and search datastore files (read-only)",
		Long: strings.TrimSpace(`
Browse one datastore directory at a time, or search a whole datastore
recursively for a name or pattern.

Both operations are strictly read-only and never mutate datastore contents.
Listing is lazy and non-recursive; the recursive search is bounded and reports
when it stops short of a complete answer.`),
		Example: `  # List the root of a datastore
  vsfleet datastore files list nvme-01

  # List one directory
  vsfleet datastore files list nvme-01 vm/web-01/

  # Search a datastore recursively for a pattern
  vsfleet datastore files find nvme-01 '*.vmdk'`,
	})
	cmd.AddCommand(newDatastoreFilesListCommand(a), newDatastoreFilesFindCommand(a))
	return cmd
}

func newDatastoreFilesListCommand(a *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list DATASTORE [PATH]",
		Aliases: []string{"ls"},
		Short:   "List one datastore directory",
		Long: strings.TrimSpace(`
List exactly one directory of a datastore: files and folders, size, and
modified time where available. PATH is relative to the datastore root, empty
for the root itself.

This never descends into subdirectories on its own — each call costs one
query. For a recursive search across the whole datastore, use
"vsfleet datastore files find".`),
		Example: `  # List a datastore's root
  vsfleet datastore files list nvme-01

  # List one directory
  vsfleet datastore files list nvme-01 vm/web-01/

  # As JSON, for a script
  vsfleet datastore files list nvme-01 -o json`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 2 {
				path = args[1]
			}
			cc, ds, err := resolveDatastoreTarget(cmd, a, args[0])
			if err != nil {
				return err
			}
			result, err := browseDatastoreDirectory(cmd.Context(), a, cc, ds, path)
			if err != nil {
				return err
			}
			if a.json() {
				return writeJSON(a.out(), datastoreListingJSON(cc.Name, path, result))
			}
			printDatastoreListing(a, cc.Name, path, result)
			return nil
		},
	}
	return cmd
}

func newDatastoreFilesFindCommand(a *App) *cobra.Command {
	var (
		limit    int
		typeFlag string
	)
	cmd := &cobra.Command{
		Use:   "find DATASTORE PATTERN",
		Short: "Recursively search a datastore for a name or pattern",
		Long: strings.TrimSpace(`
Search a whole datastore for files and folders matching PATTERN. PATTERN is a
match pattern sent to the datastore browser, not a substring: "*.vmdk" finds
what "vmdk" would not.

The search is bounded. A result that hit the bound comes back marked
truncated rather than presented as a complete answer — "not found" and "not
found in the first N matches" are different answers.`),
		Example: `  # Every VMDK on a datastore
  vsfleet datastore files find nvme-01 '*.vmdk'

  # Only folders matching a name
  vsfleet datastore files find nvme-01 'web-*' --type directory

  # Cap the client-side result count, as JSON
  vsfleet datastore files find nvme-01 orphan.vmdk --limit 20 -o json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit < 0 {
				return fmt.Errorf("--limit must be zero or greater")
			}
			entryType, err := parseDatastoreEntryTypeFlag(typeFlag)
			if err != nil {
				return err
			}
			cc, ds, err := resolveDatastoreTarget(cmd, a, args[0])
			if err != nil {
				return err
			}
			result, err := findInDatastore(cmd.Context(), a, cc, ds, args[1])
			if err != nil {
				return err
			}
			entries, limited := filterAndLimitDatastoreEntries(result.listing.Entries, entryType, limit)
			if a.json() {
				return writeJSON(a.out(), datastoreFindJSON(cc.Name, args[1], typeFlag, limit, limited, entries, result))
			}
			printDatastoreFind(a, cc.Name, args[1], limited, entries, result)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum number of matches to show (0 = no client-side limit)")
	cmd.Flags().StringVar(&typeFlag, "type", "all", "restrict results to: file, directory, or all")
	return cmd
}

func parseDatastoreEntryTypeFlag(value string) (vsphere.DatastoreEntryType, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "all":
		return "", nil
	case "file":
		return vsphere.DatastoreEntryFile, nil
	case "directory", "folder":
		return vsphere.DatastoreEntryFolder, nil
	default:
		return "", fmt.Errorf("unknown --type %q (supported: file, directory, all)", value)
	}
}

func filterAndLimitDatastoreEntries(entries []vsphere.DatastoreEntry, entryType vsphere.DatastoreEntryType, limit int) ([]vsphere.DatastoreEntry, bool) {
	if entryType != "" {
		filtered := make([]vsphere.DatastoreEntry, 0, len(entries))
		for _, e := range entries {
			if e.Type == entryType {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if limit > 0 && len(entries) > limit {
		return entries[:limit], true
	}
	return entries, false
}

// resolveDatastoreTarget finds the one datastore a name identifies across the
// contexts the command is scoped to. A name that matches more than one
// datastore — whether across contexts or, more rarely, within one context
// spanning several datacenters — is refused rather than guessed at: browsing
// silently picks a datastore an operator did not name.
func resolveDatastoreTarget(cmd *cobra.Command, a *App, name string) (*config.Context, vsphere.Datastore, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, vsphere.Datastore{}, fmt.Errorf("datastore name is required")
	}
	contexts, err := a.Contexts()
	if err != nil {
		return nil, vsphere.Datastore{}, err
	}
	stores, failures, err := gather(cmd.Context(), a, func(ctx context.Context, c *vsphere.Client) ([]vsphere.Datastore, error) {
		return c.ListDatastores(ctx)
	})
	if err != nil {
		reportFailures(a, failures)
		return nil, vsphere.Datastore{}, err
	}
	var matches []vsphere.Datastore
	for _, d := range stores {
		if strings.EqualFold(d.Name, name) {
			matches = append(matches, d)
		}
	}
	reportFailures(a, failures)
	switch len(matches) {
	case 0:
		if len(failures) > 0 {
			return nil, vsphere.Datastore{}, fmt.Errorf("datastore %q not found (%d context(s) searched, %d failed)", name, len(contexts), len(failures))
		}
		return nil, vsphere.Datastore{}, fmt.Errorf("datastore %q not found in %d context(s) searched", name, len(contexts))
	case 1:
		cc, err := findContextByName(contexts, matches[0].Context)
		if err != nil {
			return nil, vsphere.Datastore{}, err
		}
		return cc, matches[0], nil
	default:
		printDatastoreCandidates(a, matches)
		return nil, vsphere.Datastore{}, fmt.Errorf("datastore %q is ambiguous (%d matches); use --context to narrow", name, len(matches))
	}
}

func findContextByName(contexts []*config.Context, name string) (*config.Context, error) {
	for _, cc := range contexts {
		if cc.Name == name {
			return cc, nil
		}
	}
	return nil, fmt.Errorf("context %q is no longer in scope", name)
}

func printDatastoreCandidates(a *App, candidates []vsphere.Datastore) {
	out := a.errOut()
	fmt.Fprintln(out, "Candidates:")
	t := newTable(out, "CONTEXT", "DATACENTER", "NAME", "TYPE")
	for _, d := range candidates {
		t.row(d.Context, dash(d.Datacenter), d.Name, dash(d.Type))
	}
	t.flush()
}

// browseDatastoreDirectory lists one directory and, when it contains a VMDK,
// attaches the relationship evidence a VMDK result needs: who currently
// references it, and what the most recent stored assessment recorded.
func browseDatastoreDirectory(ctx context.Context, a *App, cc *config.Context, ds vsphere.Datastore, path string) (datastoreBrowseResult, error) {
	mgr := a.Sessions()
	opCtx, cancel, tracker := mgr.Operation(ctx)
	defer cancel()
	s, err := mgr.Connect(opCtx, cc)
	if err != nil {
		return datastoreBrowseResult{}, mgr.TimeoutError(err, tracker)
	}
	client := s.Client()
	listing, err := client.ListDatastoreDirectory(opCtx, ds.ID, ds.Name, path)
	if err != nil {
		return datastoreBrowseResult{}, mgr.TimeoutError(err, tracker)
	}
	result := datastoreBrowseResult{listing: listing, datastore: ds}
	if !anyVMDK(listing.Entries) {
		return result, nil
	}
	result.refs, _ = client.ListDatastoreVMReferences(opCtx, ds.ID)
	result.data, result.haveAssessment = loadLatestDatastoreAssessment(ctx, a, cc)
	return result, nil
}

func findInDatastore(ctx context.Context, a *App, cc *config.Context, ds vsphere.Datastore, pattern string) (datastoreBrowseResult, error) {
	mgr := a.Sessions()
	opCtx, cancel, tracker := mgr.Operation(ctx)
	defer cancel()
	s, err := mgr.Connect(opCtx, cc)
	if err != nil {
		return datastoreBrowseResult{}, mgr.TimeoutError(err, tracker)
	}
	client := s.Client()
	listing, err := client.FindInDatastore(opCtx, ds.ID, ds.Name, pattern)
	if err != nil {
		return datastoreBrowseResult{}, mgr.TimeoutError(err, tracker)
	}
	result := datastoreBrowseResult{listing: listing, datastore: ds}
	if !anyVMDK(listing.Entries) {
		return result, nil
	}
	result.refs, _ = client.ListDatastoreVMReferences(opCtx, ds.ID)
	result.data, result.haveAssessment = loadLatestDatastoreAssessment(ctx, a, cc)
	return result, nil
}

func anyVMDK(entries []vsphere.DatastoreEntry) bool {
	for _, e := range entries {
		if isVMDKEntry(e) {
			return true
		}
	}
	return false
}

func isVMDKEntry(e vsphere.DatastoreEntry) bool {
	return e.Type == vsphere.DatastoreEntryFile && strings.HasSuffix(strings.ToLower(e.Name), ".vmdk")
}

// loadLatestDatastoreAssessment loads the most recent stored assessment for
// one context, best-effort. A datastore file listing is a live-query
// operation; the absence of stored history is not an error here, only a gap
// in the relationship evidence a VMDK result can show.
func loadLatestDatastoreAssessment(ctx context.Context, a *App, cc *config.Context) (assessment.ExportData, bool) {
	store, err := a.History()
	if err != nil {
		return assessment.ExportData{}, false
	}
	runID, err := store.ResolveRun(ctx, "latest")
	if err != nil {
		return assessment.ExportData{}, false
	}
	data, err := store.LoadExportDataForContexts(ctx, runID, []string{cc.Name})
	if err != nil {
		return assessment.ExportData{}, false
	}
	return data, true
}

// datastoreVMDKFamily groups sibling VMDK descriptors and extents — -flat,
// -delta, -ctk, snapshot generations — under their base disk, the way the
// TUI's own detail view already does, so a search result names the disk an
// operator would recognize rather than one physical extent of it.
func datastoreVMDKFamily(value string) string {
	value = vsphere.NormalizeRelativePath(value)
	for _, suffix := range []string{"-flat.vmdk", "-delta.vmdk", "-sesparse.vmdk", "-ctk.vmdk", "-rdm.vmdk", "-rdmp.vmdk", "-digest.vmdk"} {
		if strings.HasSuffix(value, suffix) {
			value = strings.TrimSuffix(value, suffix) + ".vmdk"
			break
		}
	}
	base := strings.TrimSuffix(value, ".vmdk")
	if len(base) > 7 && base[len(base)-7] == '-' && datastoreAllDigits(base[len(base)-6:]) {
		value = base[:len(base)-7] + ".vmdk"
	}
	return value
}

func datastoreAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func liveReferencesFor(refs vsphere.DatastoreReferenceListing, path string) []vsphere.DatastoreVMReference {
	_, selectedPath, ok := vsphere.SplitDatastorePath(path)
	if !ok {
		return nil
	}
	out := make([]vsphere.DatastoreVMReference, 0, len(refs.References))
	for _, ref := range refs.References {
		_, refPath, ok := vsphere.SplitDatastorePath(ref.BackingPath)
		if ok && datastoreVMDKFamily(refPath) == datastoreVMDKFamily(selectedPath) {
			out = append(out, ref)
		}
	}
	return out
}

func relationshipSummary(entry vsphere.DatastoreEntry, result datastoreBrowseResult) string {
	if !isVMDKEntry(entry) {
		return ""
	}
	live := liveReferencesFor(result.refs, entry.Path)
	if len(live) > 0 {
		label := live[0].VMName
		if live[0].Template {
			label += " (template)"
		}
		if len(live) > 1 {
			label += fmt.Sprintf(" +%d more", len(live)-1)
		}
		return "referenced by " + label
	}
	if !result.haveAssessment {
		return "unknown — no stored assessment"
	}
	evaluated := health.AssessDatastoreFile(result.data, result.datastore, entry.Path)
	return orphanLabel(evaluated.Confidence)
}

func datastoreModified(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04")
}

func datastoreModifiedRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func printDatastoreListing(a *App, contextName, path string, result datastoreBrowseResult) {
	out := a.out()
	fmt.Fprintf(out, "%s [%s]  /%s\n", result.datastore.Name, contextName, path)
	if len(result.listing.Entries) == 0 {
		fmt.Fprintln(out, "empty directory")
		return
	}
	t := newTable(out, "TYPE", "NAME", "SIZE", "MODIFIED", "RELATIONSHIP")
	for _, e := range result.listing.Entries {
		kind, name := "file", e.Name
		if e.Type == vsphere.DatastoreEntryFolder {
			kind, name = "dir", e.Name+"/"
		}
		size := "-"
		if e.Type != vsphere.DatastoreEntryFolder {
			size = humanFileBytes(e.SizeBytes)
		}
		t.row(kind, name, size, datastoreModified(e.Modified), dash(relationshipSummary(e, result)))
	}
	t.flush()
	if result.listing.Truncated {
		fmt.Fprintf(out, "%s showing the first %d entries — this directory is larger than that\n", glyphFail, len(result.listing.Entries))
	}
}

func printDatastoreFind(a *App, contextName, pattern string, limited bool, entries []vsphere.DatastoreEntry, result datastoreBrowseResult) {
	out := a.out()
	fmt.Fprintf(out, "Find %q in %s [%s]\n", pattern, result.datastore.Name, contextName)
	if len(entries) == 0 {
		fmt.Fprintln(out, "no matches")
	} else {
		t := newTable(out, "TYPE", "PATH", "SIZE", "MODIFIED", "RELATIONSHIP")
		for _, e := range entries {
			kind := "file"
			if e.Type == vsphere.DatastoreEntryFolder {
				kind = "dir"
			}
			size := "-"
			if e.Type != vsphere.DatastoreEntryFolder {
				size = humanFileBytes(e.SizeBytes)
			}
			t.row(kind, e.Path, size, datastoreModified(e.Modified), dash(relationshipSummary(e, result)))
		}
		t.flush()
	}
	if result.listing.Truncated {
		fmt.Fprintf(out, "%s partial result — the datastore search stopped short; narrow the pattern\n", glyphFail)
	}
	if limited {
		fmt.Fprintf(out, "%s showing the first %d match(es) — raise --limit to see more\n", glyphFail, len(entries))
	}
}

func datastoreEntryJSON(e vsphere.DatastoreEntry, result datastoreBrowseResult) map[string]any {
	m := map[string]any{
		"name":       e.Name,
		"path":       e.Path,
		"type":       string(e.Type),
		"size_bytes": e.SizeBytes,
	}
	if modified := datastoreModifiedRFC3339(e.Modified); modified != "" {
		m["modified"] = modified
	}
	if !isVMDKEntry(e) {
		return m
	}
	live := liveReferencesFor(result.refs, e.Path)
	if len(live) > 0 {
		refList := make([]map[string]any, 0, len(live))
		for _, ref := range live {
			refList = append(refList, map[string]any{
				"context":      ref.Context,
				"vm":           ref.VMName,
				"template":     ref.Template,
				"disk_label":   ref.DiskLabel,
				"backing_path": ref.BackingPath,
			})
		}
		m["references"] = refList
	}
	if result.haveAssessment {
		evaluated := health.AssessDatastoreFile(result.data, result.datastore, e.Path)
		m["assessment"] = map[string]any{
			"confidence": string(evaluated.Confidence),
			"reasons":    evaluated.Reasons,
		}
	}
	return m
}

func datastoreListingJSON(contextName, path string, result datastoreBrowseResult) map[string]any {
	entries := make([]map[string]any, 0, len(result.listing.Entries))
	for _, e := range result.listing.Entries {
		entries = append(entries, datastoreEntryJSON(e, result))
	}
	return map[string]any{
		"context":   contextName,
		"datastore": result.datastore.Name,
		"path":      path,
		"truncated": result.listing.Truncated,
		"entries":   entries,
	}
}

func datastoreFindJSON(contextName, pattern, typeFilter string, limit int, limited bool, entries []vsphere.DatastoreEntry, result datastoreBrowseResult) map[string]any {
	matches := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		matches = append(matches, datastoreEntryJSON(e, result))
	}
	out := map[string]any{
		"context":   contextName,
		"datastore": result.datastore.Name,
		"pattern":   pattern,
		"truncated": result.listing.Truncated,
		"limited":   limited,
		"matches":   matches,
	}
	if typeFilter != "" && typeFilter != "all" {
		out["type"] = typeFilter
	}
	if limit > 0 {
		out["limit"] = limit
	}
	return out
}
