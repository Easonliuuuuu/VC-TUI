package metareport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/query"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Definition is a saved report: which objects to select and which columns to
// show. It names no assessment; it is resolved against one at run time, so
// the same definition can be run against every capture.
type Definition struct {
	Name        string   `toml:"name" json:"name"`
	Description string   `toml:"description,omitempty" json:"description,omitempty"`
	Contexts    []string `toml:"contexts,omitempty" json:"contexts,omitempty"`
	Kinds       []string `toml:"kinds,omitempty" json:"kinds,omitempty"`
	Where       []string `toml:"where,omitempty" json:"where,omitempty"`
	Columns     []string `toml:"columns,omitempty" json:"columns,omitempty"`
}

// DefaultColumns are used when a definition names none.
var DefaultColumns = []string{"context", "kind", "name", "id"}

// ReportsDir is where named definitions live: a reports directory beside the
// configuration file, so an isolated VSFLEET_CONFIG isolates them too.
func ReportsDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "reports")
}

// Resolve finds a definition by file path or by name. An argument that names
// an existing file, or ends in .toml, is a path; anything else is looked up
// as <dir>/<name>.toml.
func Resolve(arg, dir string) (string, error) {
	if arg == "" {
		return "", fmt.Errorf("report definition is empty")
	}
	if info, err := os.Stat(arg); err == nil && !info.IsDir() {
		return arg, nil
	}
	if strings.HasSuffix(arg, ".toml") || strings.ContainsRune(arg, os.PathSeparator) || strings.Contains(arg, "/") {
		return "", fmt.Errorf("report definition %s not found", arg)
	}
	path := filepath.Join(dir, arg+".toml")
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no saved report %q (looked for %s)", arg, path)
	}
	return path, nil
}

// Load reads and validates a definition. Unknown keys are an error so a
// misspelt "wehre" cannot silently widen a report.
func Load(path string) (Definition, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	var d Definition
	dec := toml.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return Definition{}, fmt.Errorf("parse report definition %s: %w", path, err)
	}
	if d.Name == "" {
		d.Name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if _, err := d.Compile(); err != nil {
		return Definition{}, fmt.Errorf("report definition %s: %w", path, err)
	}
	return d, nil
}

// Compiled is a validated definition ready to evaluate.
type Compiled struct {
	Definition Definition
	kinds      []vsphere.Kind
	filter     query.Filter
	columns    []column
	sources    []string
}

type column struct {
	name      string
	source    string // "" for an inventory field
	field     string
	qualifier string
}

// Compile validates kinds, predicates and columns.
func (d Definition) Compile() (Compiled, error) {
	c := Compiled{Definition: d}
	for _, raw := range d.Kinds {
		kind, err := ParseKind(raw)
		if err != nil {
			return Compiled{}, err
		}
		if !containsKind(c.kinds, kind) {
			c.kinds = append(c.kinds, kind)
		}
	}
	filter, err := query.Parse(d.Where, c.kinds)
	if err != nil {
		return Compiled{}, fmt.Errorf("where: %w", err)
	}
	c.filter = filter
	for _, p := range filter.Predicates() {
		source := ""
		switch p.Field {
		case "tag":
			source = SourceTag
		case "custom":
			source = SourceCustom
		}
		if source != "" && !containsString(c.sources, source) {
			c.sources = append(c.sources, source)
		}
	}
	names := d.Columns
	if len(names) == 0 {
		names = DefaultColumns
	}
	for _, name := range names {
		col, err := parseColumn(name, c.kinds)
		if err != nil {
			return Compiled{}, err
		}
		c.columns = append(c.columns, col)
	}
	return c, nil
}

// ParseKind accepts every kind an assessment stores metadata for.
func ParseKind(raw string) (vsphere.Kind, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "resourcepool", "resourcepools":
		return vsphere.KindResourcePool, nil
	case "dvswitch", "dvswitches":
		return vsphere.KindDVSwitch, nil
	}
	kind, err := vsphere.ParseKind(value)
	if err != nil {
		return "", err
	}
	if !isMetadataKind(kind) {
		return "", fmt.Errorf("kind %q is not stored in assessments", raw)
	}
	return kind, nil
}

func parseColumn(name string, kinds []vsphere.Kind) (column, error) {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	switch {
	case lower == "context" || lower == "vcenter_id" || lower == "kind" || lower == "id" || lower == "name" || lower == "path":
		return column{name: lower, field: lower}, nil
	case lower == "tags":
		return column{name: lower, source: SourceTag}, nil
	case lower == "custom_attributes":
		return column{name: lower, source: SourceCustom}, nil
	case lower == "tags_status":
		return column{name: lower, source: SourceTag, field: "status"}, nil
	case lower == "custom_attributes_status":
		return column{name: lower, source: SourceCustom, field: "status"}, nil
	case strings.HasPrefix(lower, "tag.") && len(trimmed) > len("tag."):
		return column{name: trimmed, source: SourceTag, qualifier: trimmed[len("tag."):]}, nil
	case strings.HasPrefix(lower, "custom.") && len(trimmed) > len("custom."):
		return column{name: trimmed, source: SourceCustom, qualifier: trimmed[len("custom."):]}, nil
	case query.KnownField(lower, kinds):
		return column{name: lower, field: lower}, nil
	}
	return column{}, fmt.Errorf("unknown report column %q", name)
}

// Member is one object in a report, or one whose membership is unknown.
type Member struct {
	Context      string   `json:"context"`
	VCenterID    string   `json:"vcenter_id"`
	Kind         string   `json:"kind"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	TagsStatus   string   `json:"tags_status"`
	CustomStatus string   `json:"custom_attributes_status"`
	Values       []string `json:"values"`
	// Unreadable names the metadata sources a predicate needed but the
	// capture could not read. It is set only on undetermined members.
	Unreadable []string `json:"unreadable,omitempty"`
	identity   string
}

// CollectionState records whether a context's collection succeeded, which
// is what lets a later comparison tell a removal from a gap.
type CollectionState struct {
	Context   string `json:"context"`
	VCenterID string `json:"vcenter_id"`
	Kind      string `json:"kind"`
	Observed  bool   `json:"observed"`
}

// ContextInfo is one context the report was resolved against.
type ContextInfo struct {
	Name       string    `json:"context"`
	VCenterID  string    `json:"vcenter_id"`
	CapturedAt time.Time `json:"captured_at"`
	Status     string    `json:"status"`
}

// Result is a definition resolved against one assessment.
type Result struct {
	Report       Definition        `json:"report"`
	RunID        int64             `json:"run_id"`
	RunLabel     string            `json:"run_label,omitempty"`
	RunStatus    string            `json:"run_status"`
	CapturedAt   time.Time         `json:"captured_at"`
	Contexts     []ContextInfo     `json:"contexts"`
	Columns      []string          `json:"columns"`
	Members      []Member          `json:"members"`
	Undetermined []Member          `json:"undetermined"`
	Coverage     []Coverage        `json:"coverage"`
	Collections  []CollectionState `json:"collections"`
	Warnings     []string          `json:"warnings"`
	// Complete is true only when membership is known for every in-scope
	// object: no undetermined members, no failed or missing collection and
	// no requested context absent from the capture.
	Complete bool `json:"complete"`
}

// Evaluate resolves the compiled definition against data.
func (c Compiled) Evaluate(data assessment.ExportData) Result {
	r := Result{Report: c.Definition, RunID: data.Run.ID, RunLabel: data.Run.Label, RunStatus: string(data.Run.Status), CapturedAt: data.Run.StartedAt.UTC(), Members: []Member{}, Undetermined: []Member{}, Warnings: []string{}, Coverage: []Coverage{}, Collections: []CollectionState{}}
	for _, col := range c.columns {
		r.Columns = append(r.Columns, col.name)
	}
	data = c.scope(data, &r)
	kinds := c.kinds
	if len(kinds) == 0 {
		kinds = MetadataKinds
	}
	for _, ctx := range data.Contexts {
		r.Contexts = append(r.Contexts, ContextInfo{Name: ctx.Name, VCenterID: ctx.VCenterID, CapturedAt: ctx.StartedAt.UTC(), Status: ctx.VMStatus})
		seen := map[string]bool{}
		for _, kind := range kinds {
			ck := collectionKind(kind)
			if seen[ck] {
				continue
			}
			seen[ck] = true
			r.Collections = append(r.Collections, CollectionState{Context: ctx.Name, VCenterID: ctx.VCenterID, Kind: ck, Observed: observed(data, ctx.Name, kind)})
		}
	}
	objects := Objects(data)
	var inScope []Object
	for _, o := range objects {
		if containsKind(kinds, o.Kind) {
			inScope = append(inScope, o)
		}
	}
	columnSources := append([]string(nil), c.sources...)
	for _, col := range c.columns {
		if col.source != "" && !containsString(columnSources, col.source) {
			columnSources = append(columnSources, col.source)
		}
	}
	for _, o := range inScope {
		outcome, unreadable := c.filter.Evaluate(o.Subject)
		if outcome == query.NoMatch {
			continue
		}
		m := Member{Context: o.Context, VCenterID: o.VCenterID, Kind: string(o.Kind), ID: o.ID, Name: o.Name, TagsStatus: o.Metadata.TagsStatus, CustomStatus: o.Metadata.CustomAttributesStatus, identity: o.Identity()}
		for _, col := range c.columns {
			m.Values = append(m.Values, col.value(o))
		}
		if outcome == query.Undetermined {
			for _, source := range unreadable {
				if source == "custom" {
					source = SourceCustom
				} else {
					source = SourceTag
				}
				m.Unreadable = append(m.Unreadable, source)
			}
			r.Undetermined = append(r.Undetermined, m)
			continue
		}
		r.Members = append(r.Members, m)
	}
	// Coverage covers every source the report reads, whether in a predicate
	// or only in a column, plus failed and missing collections.
	r.Coverage = CoverageOf(data, inScope, kinds, columnSources)
	if len(columnSources) == 0 {
		r.Coverage = collectionOnly(r.Coverage)
	}
	predicateSources := c.sources
	complete := len(r.Undetermined) == 0
	for _, cov := range r.Coverage {
		if cov.Complete() {
			continue
		}
		r.Warnings = append(r.Warnings, cov.Warning())
		if cov.Source == "" || containsString(predicateSources, cov.Source) {
			complete = false
		}
	}
	if n := len(r.Undetermined); n > 0 {
		r.Warnings = append(r.Warnings, fmt.Sprintf("%d object(s) have undetermined membership because a filtered metadata source could not be read", n))
	}
	if r.RunStatus != "" && r.RunStatus != string(assessment.RunComplete) {
		r.Warnings = append(r.Warnings, fmt.Sprintf("assessment %d finished with status %s", r.RunID, r.RunStatus))
	}
	r.Complete = complete && !hasMissingContext(r.Warnings)
	return r
}

const missingContextPrefix = "requested context not in assessment: "

func hasMissingContext(warnings []string) bool {
	for _, w := range warnings {
		if strings.HasPrefix(w, missingContextPrefix) {
			return true
		}
	}
	return false
}

func collectionOnly(in []Coverage) []Coverage {
	out := []Coverage{}
	for _, c := range in {
		if c.Source == "" {
			out = append(out, c)
		}
	}
	return out
}

// scope narrows data to the definition's contexts, matched by name or
// vCenter ID, and warns about each requested context the capture lacks.
func (c Compiled) scope(data assessment.ExportData, r *Result) assessment.ExportData {
	if len(c.Definition.Contexts) == 0 {
		return data
	}
	keep := map[string]bool{}
	for _, want := range c.Definition.Contexts {
		found := false
		for _, ctx := range data.Contexts {
			if strings.EqualFold(ctx.Name, want) || (ctx.VCenterID != "" && ctx.VCenterID == want) {
				keep[ctx.Name] = true
				found = true
			}
		}
		if !found {
			r.Warnings = append(r.Warnings, missingContextPrefix+want)
		}
	}
	out := data
	out.Contexts = nil
	for _, ctx := range data.Contexts {
		if keep[ctx.Name] {
			out.Contexts = append(out.Contexts, ctx)
		}
	}
	out.VMs = nil
	for _, vm := range data.VMs {
		if keep[vm.Observation.Context] {
			out.VMs = append(out.VMs, vm)
		}
	}
	out.Resources = nil
	for _, res := range data.Resources {
		if keep[res.Context] {
			out.Resources = append(out.Resources, res)
		}
	}
	return out
}

func (col column) value(o Object) string {
	if col.source == "" {
		switch col.field {
		case "context":
			return o.Context
		case "vcenter_id":
			return o.VCenterID
		case "kind":
			return string(o.Kind)
		case "id":
			return o.ID
		case "name":
			return o.Name
		case "path":
			return o.Path
		}
		v, ok := o.Subject.Fields[col.field]
		if !ok || v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
	status, _ := o.status(col.source)
	if col.field == "status" {
		return status
	}
	// An unreadable source is shown as its status, never as an empty cell
	// that would read as "no value".
	if status != vsphere.MetadataAvailable {
		return "(" + statusLabel(status) + ")"
	}
	var parts []string
	if col.source == SourceTag {
		for _, tag := range o.Metadata.Tags {
			switch {
			case col.qualifier == "":
				parts = append(parts, tag.Category+"/"+tag.Name)
			case tag.Category == col.qualifier:
				parts = append(parts, tag.Name)
			}
		}
	} else {
		for _, attr := range o.Metadata.CustomAttributes {
			switch {
			case col.qualifier == "":
				parts = append(parts, attr.Name+"="+attr.Value)
			case col.qualifier == "#"+strconv.Itoa(int(attr.Key)) || attr.Name == col.qualifier:
				parts = append(parts, attr.Value)
			}
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// ResultColumns is the CSV header for a result: the definition's columns,
// then fixed membership and source-status columns.
func (r Result) ResultColumns() []string {
	return append(append([]string(nil), r.Columns...), "membership", "tags_status", "custom_attributes_status")
}

// CSV renders members and undetermined objects. Membership is "member" or
// "undetermined"; a consumer that ignores it still sees every unknown row.
func (r Result) CSV() ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(r.ResultColumns()); err != nil {
		return nil, err
	}
	for _, group := range []struct {
		label   string
		members []Member
	}{{"member", r.Members}, {"undetermined", r.Undetermined}} {
		for _, m := range group.members {
			record := append(append([]string(nil), m.Values...), group.label, m.TagsStatus, m.CustomStatus)
			if err := w.Write(record); err != nil {
				return nil, err
			}
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}
