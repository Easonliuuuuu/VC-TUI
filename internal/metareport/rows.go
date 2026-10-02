package metareport

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Row is one metadata value, or one statement about a source, for one
// object. An object whose source was read and holds no values still gets a
// row for that source, with empty Field and Value and status available, so
// "verified none" is visible and distinct from "could not read". An object
// whose source could not be read gets a single row carrying that status.
//
// For a tag, FieldID and Field are the tag category and ValueID and Value
// the tag. For a custom attribute, FieldID is the numeric field key, Field
// its name, and ValueID is empty. The fields never vary with the estate.
type Row struct {
	RunID      int64     `json:"run_id"`
	CapturedAt time.Time `json:"captured_at"`
	Context    string    `json:"context"`
	VCenterID  string    `json:"vcenter_id"`
	Kind       string    `json:"kind"`
	ObjectID   string    `json:"object_id"`
	ObjectName string    `json:"object_name"`
	Path       string    `json:"path"`
	Source     string    `json:"source"`
	FieldID    string    `json:"field_id"`
	Field      string    `json:"field"`
	ValueID    string    `json:"value_id"`
	Value      string    `json:"value"`
	Status     string    `json:"status"`
	Error      string    `json:"error"`
}

// RowColumns is the fixed CSV header, matching Row's JSON names.
var RowColumns = []string{"run_id", "captured_at", "context", "vcenter_id", "kind", "object_id", "object_name", "path", "source", "field_id", "field", "value_id", "value", "status", "error"}

// Record renders the row in RowColumns order.
func (r Row) Record() []string {
	return []string{strconv.FormatInt(r.RunID, 10), formatTime(r.CapturedAt), r.Context, r.VCenterID, r.Kind, r.ObjectID, r.ObjectName, r.Path, r.Source, r.FieldID, r.Field, r.ValueID, r.Value, r.Status, r.Error}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Rows expands objects into metadata rows for the requested sources (every
// source when none are given), in object order and then source, field and
// value order.
func Rows(objects []Object, sources []string) []Row {
	if len(sources) == 0 {
		sources = Sources
	}
	var out []Row
	for _, o := range objects {
		base := Row{RunID: o.RunID, CapturedAt: o.CapturedAt, Context: o.Context, VCenterID: o.VCenterID, Kind: string(o.Kind), ObjectID: o.ID, ObjectName: o.Name, Path: o.Path}
		for _, source := range Sources {
			if !containsString(sources, source) {
				continue
			}
			status, message := o.status(source)
			base.Source, base.Status = source, status
			if status != vsphere.MetadataAvailable {
				row := base
				row.Error = message
				out = append(out, row)
				continue
			}
			values := sourceRows(base, o, source)
			if len(values) == 0 {
				out = append(out, base)
			}
			out = append(out, values...)
		}
	}
	return out
}

func sourceRows(base Row, o Object, source string) []Row {
	var out []Row
	if source == SourceTag {
		tags := append([]vsphere.Tag(nil), o.Metadata.Tags...)
		sort.SliceStable(tags, func(i, j int) bool {
			if tags[i].Category != tags[j].Category {
				return tags[i].Category < tags[j].Category
			}
			if tags[i].Name != tags[j].Name {
				return tags[i].Name < tags[j].Name
			}
			return tags[i].ID < tags[j].ID
		})
		for _, tag := range tags {
			row := base
			row.FieldID, row.Field, row.ValueID, row.Value = tag.CategoryID, tag.Category, tag.ID, tag.Name
			out = append(out, row)
		}
		return out
	}
	attrs := append([]vsphere.CustomAttribute(nil), o.Metadata.CustomAttributes...)
	sort.SliceStable(attrs, func(i, j int) bool { return attrs[i].Key < attrs[j].Key })
	for _, attr := range attrs {
		row := base
		row.FieldID, row.Field, row.Value = strconv.Itoa(int(attr.Key)), attr.Name, attr.Value
		out = append(out, row)
	}
	return out
}

// RowsCSV renders rows as CSV with the RowColumns header.
func RowsCSV(rows []Row) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write(RowColumns); err != nil {
		return nil, err
	}
	for _, r := range rows {
		if err := w.Write(r.Record()); err != nil {
			return nil, err
		}
	}
	w.Flush()
	return buf.Bytes(), w.Error()
}

// Collection-level coverage states beyond the metadata source states.
const (
	// CollectionFailed means the objects themselves were not collected, so
	// neither their membership nor their metadata is known.
	CollectionFailed = "collection_failed"
	// CollectionNotRecorded means the capture holds no record of the
	// collection at all, typically because it predates that kind.
	CollectionNotRecorded = "collection_not_recorded"
)

// Coverage is how completely one metadata source was read for one kind in
// one context. Objects counts the stored objects and Readable those whose
// source was available.
type Coverage struct {
	Context   string `json:"context"`
	VCenterID string `json:"vcenter_id"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	Objects   int    `json:"objects"`
	Readable  int    `json:"readable"`
	// Breakdown counts unreadable objects by state, e.g. "1 denied".
	Breakdown string `json:"breakdown,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Complete reports whether nothing is missing from this entry.
func (c Coverage) Complete() bool {
	return c.Status == vsphere.MetadataAvailable || c.Status == vsphere.MetadataNotApplicable
}

// Warning is a one-line description of an incomplete entry.
func (c Coverage) Warning() string {
	where := c.Context + " " + c.Kind
	switch c.Status {
	case CollectionFailed:
		return fmt.Sprintf("%s: collection failed, membership and metadata unknown: %s", where, nonempty(c.Error, "no error recorded"))
	case CollectionNotRecorded:
		return fmt.Sprintf("%s: capture holds no %s collection", where, c.Kind)
	}
	msg := fmt.Sprintf("%s: %s %s (%d of %d objects readable", where, sourceLabel(c.Source), statusLabel(c.Status), c.Readable, c.Objects)
	if c.Breakdown != "" {
		msg += "; " + c.Breakdown
	}
	msg += ")"
	if c.Error != "" {
		msg += ": " + c.Error
	}
	return msg
}

func sourceLabel(source string) string {
	if source == SourceTag {
		return "tags"
	}
	return "custom attributes"
}

func statusLabel(status string) string {
	b := []byte(status)
	for i, c := range b {
		if c == '_' {
			b[i] = ' '
		}
	}
	return string(b)
}

func nonempty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// CoverageOf reports source coverage per context, kind and source for the
// objects in scope, plus an entry for every in-scope collection that failed
// or was never recorded. kinds and sources narrow the scope; empty means all.
func CoverageOf(data assessment.ExportData, objects []Object, kinds []vsphere.Kind, sources []string) []Coverage {
	if len(sources) == 0 {
		sources = Sources
	}
	if len(kinds) == 0 {
		kinds = MetadataKinds
	}
	type key struct{ context, kind, source string }
	tallies := map[key]*vsphere.MetadataTally{}
	vcenters := map[string]string{}
	for _, c := range data.Contexts {
		vcenters[c.Name] = c.VCenterID
	}
	for _, o := range objects {
		if !containsKind(kinds, o.Kind) {
			continue
		}
		for _, source := range sources {
			k := key{o.Context, string(o.Kind), source}
			if tallies[k] == nil {
				tallies[k] = &vsphere.MetadataTally{}
			}
			status, message := o.status(source)
			tallies[k].Add(status, message)
		}
	}
	var out []Coverage
	for k, t := range tallies {
		status, _ := t.Result()
		out = append(out, Coverage{Context: k.context, VCenterID: vcenters[k.context], Kind: k.kind, Source: k.source, Status: status, Objects: t.Total, Readable: t.Available, Breakdown: t.Breakdown(), Error: t.FirstError()})
	}
	for _, c := range data.Contexts {
		seen := map[string]bool{}
		for _, kind := range kinds {
			collection := collectionKind(kind)
			if seen[collection] {
				continue
			}
			seen[collection] = true
			run, ok := findCollection(c, collection)
			switch {
			case !ok && (c.Error != "" || c.VMStatus == "failed"):
				out = append(out, Coverage{Context: c.Name, VCenterID: c.VCenterID, Kind: collection, Status: CollectionFailed, Error: c.Error})
			case !ok:
				out = append(out, Coverage{Context: c.Name, VCenterID: c.VCenterID, Kind: collection, Status: CollectionNotRecorded})
			case run.Status == "failed":
				out = append(out, Coverage{Context: c.Name, VCenterID: c.VCenterID, Kind: collection, Status: CollectionFailed, Error: run.Error})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Context != b.Context {
			return a.Context < b.Context
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Source < b.Source
	})
	return out
}

func findCollection(c assessment.ContextRun, kind string) (assessment.CollectionRun, bool) {
	for _, run := range c.Collections {
		if run.Kind == kind {
			return run, true
		}
	}
	return assessment.CollectionRun{}, false
}

// observed reports whether a context's collection for kind succeeded, which
// is what makes an object's absence meaningful.
func observed(data assessment.ExportData, context string, kind vsphere.Kind) bool {
	for _, c := range data.Contexts {
		if c.Name != context {
			continue
		}
		run, ok := findCollection(c, collectionKind(kind))
		return ok && run.Status != "failed"
	}
	return false
}

func containsString(values []string, v string) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}

func containsKind(values []vsphere.Kind, v vsphere.Kind) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
