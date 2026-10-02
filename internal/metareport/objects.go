// Package metareport turns the tags and custom attributes stored in an
// assessment into a fixed-schema long-format export and into saved,
// repeatable reports.
//
// Two rules shape everything here. First, the column schema never depends on
// which tag categories or custom attributes an estate happens to use: each
// value is a row, not a column. Second, a metadata source that could not be
// read is never reported as an empty one. A predicate on an unreadable source
// leaves membership undetermined, and every gap is reported as coverage.
package metareport

import (
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/query"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Metadata source names, as they appear in rows and coverage.
const (
	SourceTag    = "tag"
	SourceCustom = "custom_attribute"
)

// Sources lists every metadata source in output order.
var Sources = []string{SourceTag, SourceCustom}

// collectionKind maps an object kind to the assessment collection that holds
// it. Templates are collected with VMs.
func collectionKind(kind vsphere.Kind) string {
	if kind == vsphere.KindTemplate {
		return string(vsphere.KindVM)
	}
	return string(kind)
}

// MetadataKinds are the object kinds an assessment stores metadata for.
var MetadataKinds = []vsphere.Kind{vsphere.KindVM, vsphere.KindTemplate, vsphere.KindHost, vsphere.KindCluster, vsphere.KindResourcePool, vsphere.KindDatastore, vsphere.KindNetwork, vsphere.KindDVSwitch}

// Object is one stored inventory object with its metadata and provenance.
type Object struct {
	RunID      int64
	CapturedAt time.Time
	Context    string
	VCenterID  string
	Kind       vsphere.Kind
	ID         string
	Name       string
	Path       string
	Metadata   vsphere.Metadata
	Subject    query.Subject
}

// Identity is the key that follows an object across captures: the vCenter
// instance (or, when a capture did not record it, the context name), the
// kind and the managed object ID. Names are not identity: two vCenters
// routinely hold objects with the same name.
func (o Object) Identity() string {
	return identity(o.Context, o.VCenterID, string(o.Kind), o.ID)
}

func identity(context, vcenter, kind, id string) string {
	scope := "vc:" + vcenter
	if vcenter == "" {
		scope = "ctx:" + context
	}
	return scope + "\x00" + kind + "\x00" + id
}

// Objects returns every metadata-bearing object stored in data, in a
// deterministic order: context, kind, object ID.
func Objects(data assessment.ExportData) []Object {
	captured := map[string]time.Time{}
	for _, c := range data.Contexts {
		captured[c.Name] = c.StartedAt
	}
	at := func(context string) time.Time {
		if t, ok := captured[context]; ok && !t.IsZero() {
			return t.UTC()
		}
		return data.Run.StartedAt.UTC()
	}
	out := make([]Object, 0, len(data.VMs)+len(data.Resources))
	for _, vm := range data.VMs {
		kind := vsphere.KindVM
		if vm.Observation.VM.IsTemplate {
			kind = vsphere.KindTemplate
		}
		raw, err := json.Marshal(vm.Observation.VM)
		if err != nil {
			continue
		}
		if o, ok := newObject(data.Run.ID, at(vm.Observation.Context), vm.Observation.Context, vm.Observation.VCenterID, kind, vm.Observation.VM.ID, vm.Observation.VM.Name, raw); ok {
			out = append(out, o)
		}
	}
	for _, r := range data.Resources {
		kind := vsphere.Kind(r.Kind)
		if !isMetadataKind(kind) {
			continue
		}
		if o, ok := newObject(data.Run.ID, at(r.Context), r.Context, r.VCenterID, kind, r.ID, r.Name, r.Payload); ok {
			out = append(out, o)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Context != b.Context {
			return a.Context < b.Context
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	return out
}

func isMetadataKind(kind vsphere.Kind) bool {
	for _, k := range MetadataKinds {
		if k == kind {
			return true
		}
	}
	return false
}

func newObject(runID int64, captured time.Time, context, vcenter string, kind vsphere.Kind, id, name string, raw []byte) (Object, bool) {
	subject, err := query.SubjectFromJSON(kind, raw)
	if err != nil {
		return Object{}, false
	}
	var payload struct {
		Path     string            `json:"path"`
		Metadata *vsphere.Metadata `json:"metadata"`
	}
	_ = json.Unmarshal(raw, &payload)
	var m vsphere.Metadata
	if payload.Metadata != nil {
		m = *payload.Metadata
	}
	m.TagsStatus = vsphere.MetadataStatus(m.TagsStatus)
	m.CustomAttributesStatus = vsphere.MetadataStatus(m.CustomAttributesStatus)
	return Object{RunID: runID, CapturedAt: captured, Context: context, VCenterID: vcenter, Kind: kind, ID: id, Name: name, Path: payload.Path, Metadata: m, Subject: subject}, true
}

// status returns the object's status and error for one source.
func (o Object) status(source string) (string, string) {
	if source == SourceTag {
		return o.Metadata.TagsStatus, o.Metadata.TagsError
	}
	return o.Metadata.CustomAttributesStatus, o.Metadata.CustomAttributesError
}

// ParseSource maps a user-supplied source name onto a Source constant.
func ParseSource(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tag", "tags":
		return SourceTag, true
	case "custom", "custom_attribute", "custom_attributes", "custom-attribute", "custom-attributes":
		return SourceCustom, true
	}
	return "", false
}
