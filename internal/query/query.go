// Package query implements the small, deterministic predicate language shared
// by live inventory, search, and offline assessment commands.
package query

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type valueKind uint8

const (
	stringValue valueKind = iota + 1
	numberValue
	boolValue
)

type Predicate struct {
	Field     string
	Qualifier string
	Operator  string
	Value     string
	kind      valueKind
	numeric   float64
	boolean   bool
}

type Filter struct{ predicates []Predicate }

type Subject struct {
	Kind             vsphere.Kind
	Fields           map[string]any
	Tags             []vsphere.Tag
	CustomAttributes []vsphere.CustomAttribute
	TagsAvailable    bool
	CustomAvailable  bool
	// TagsStatus and CustomStatus are the recorded source states, normalized
	// so a payload without one reads as not recorded.
	TagsStatus   string
	CustomStatus string
}

var schemas = map[vsphere.Kind]map[string]valueKind{}

func init() {
	for kind, typ := range map[vsphere.Kind]reflect.Type{
		vsphere.KindVM: vsphereType[vsphere.VM](), vsphere.KindTemplate: vsphereType[vsphere.VM](),
		vsphere.KindHost: vsphereType[vsphere.Host](), vsphere.KindCluster: vsphereType[vsphere.Cluster](),
		vsphere.KindVApp: vsphereType[vsphere.VApp](), vsphere.KindDatastore: vsphereType[vsphere.Datastore](),
		vsphere.KindNetwork: vsphereType[vsphere.Network](), vsphere.KindResourcePool: vsphereType[vsphere.ResourcePool](),
		vsphere.KindDVSwitch: vsphereType[vsphere.DVSwitch](),
	} {
		schemas[kind] = typeSchema(typ)
	}
	schemas[vsphere.KindDatastore]["free_percent"] = numberValue
	schemas[vsphere.KindDatastore]["used_percent"] = numberValue
	for _, fields := range schemas {
		fields["kind"], fields["context"] = stringValue, stringValue
	}
}

func vsphereType[T any]() reflect.Type { return reflect.TypeOf((*T)(nil)).Elem() }

func typeSchema(t reflect.Type) map[string]valueKind {
	out := map[string]valueKind{}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" || f.Name == "Metadata" {
			continue
		}
		jsonName := strings.Split(f.Tag.Get("json"), ",")[0]
		if jsonName == "-" {
			continue
		}
		if f.Anonymous && jsonName == "" {
			for name, kind := range typeSchema(f.Type) {
				out[name] = kind
			}
			continue
		}
		if jsonName == "" {
			jsonName = strings.ToLower(f.Name)
		}
		if kind, ok := scalarKind(f.Type); ok {
			out[jsonName] = kind
		}
	}
	return out
}

func scalarKind(t reflect.Type) (valueKind, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return stringValue, true
	case reflect.Bool:
		return boolValue, true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return numberValue, true
	default:
		return 0, false
	}
}

// Parse parses one predicate. The caller can repeat --where; the resulting
// filter is an AND of all predicates.
func Parse(expressions []string, kinds []vsphere.Kind) (Filter, error) {
	if len(expressions) == 0 {
		return Filter{}, nil
	}
	allowed := kinds
	if len(allowed) == 0 {
		for kind := range schemas {
			allowed = append(allowed, kind)
		}
	}
	filter := Filter{predicates: make([]Predicate, 0, len(expressions))}
	for _, expression := range expressions {
		p, err := parseOne(expression)
		if err != nil {
			return Filter{}, err
		}
		if p.Field != "tag" && p.Field != "custom" {
			var expected valueKind
			found := false
			for _, kind := range allowed {
				if k, ok := schemas[kind][p.Field]; ok {
					expected, found = k, true
					break
				}
			}
			if !found {
				return Filter{}, fmt.Errorf("unknown query field %q", p.Field)
			}
			p.kind = expected
		} else {
			p.kind = stringValue
		}
		if p.Operator != "=" && p.Operator != "!=" {
			if p.kind != numberValue {
				return Filter{}, fmt.Errorf("operator %s requires a numeric field", p.Operator)
			}
		}
		switch p.kind {
		case numberValue:
			n, err := strconv.ParseFloat(p.Value, 64)
			if err != nil {
				return Filter{}, fmt.Errorf("query value %q is not numeric", p.Value)
			}
			p.numeric = n
		case boolValue:
			b, err := strconv.ParseBool(strings.ToLower(p.Value))
			if err != nil {
				return Filter{}, fmt.Errorf("query value %q is not true or false", p.Value)
			}
			p.boolean = b
		}
		filter.predicates = append(filter.predicates, p)
	}
	return filter, nil
}

func parseOne(expression string) (Predicate, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return Predicate{}, fmt.Errorf("query expression is empty")
	}
	operator := ""
	position := -1
	for _, candidate := range []string{"<=", ">=", "!=", "=", "<", ">"} {
		if i := strings.Index(expression, candidate); i >= 0 && (position < 0 || i < position) {
			position, operator = i, candidate
		}
	}
	if position <= 0 {
		return Predicate{}, fmt.Errorf("invalid query expression %q (expected field=value)", expression)
	}
	field := strings.TrimSpace(expression[:position])
	value, err := parseValue(expression[position+len(operator):])
	if err != nil {
		return Predicate{}, fmt.Errorf("%s: %w", expression, err)
	}
	p := Predicate{Operator: operator, Value: value}
	lower := strings.ToLower(field)
	switch {
	case lower == "tag":
		p.Field = "tag"
	case strings.HasPrefix(lower, "tag."):
		p.Field, p.Qualifier = "tag", field[len("tag."):]
	case lower == "custom":
		return Predicate{}, fmt.Errorf("custom query requires a field name")
	case strings.HasPrefix(lower, "custom."):
		p.Field, p.Qualifier = "custom", field[len("custom."):]
	default:
		p.Field = lower
	}
	if p.Field == "tag" && p.Qualifier == "" && p.Operator != "=" && p.Operator != "!=" {
		return Predicate{}, fmt.Errorf("tag queries support only = and !=")
	}
	return p, nil
}

func parseValue(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("query value is empty")
	}
	if raw[0] != '\'' && raw[0] != '"' {
		if strings.ContainsAny(raw, "'\"") {
			return "", fmt.Errorf("unterminated or misplaced quote")
		}
		return raw, nil
	}
	quote := rune(raw[0])
	if len(raw) < 2 || rune(raw[len(raw)-1]) != quote {
		return "", fmt.Errorf("unterminated quoted value")
	}
	var out strings.Builder
	escaped := false
	for _, r := range raw[1 : len(raw)-1] {
		if escaped {
			if r != quote && r != '\\' {
				return "", fmt.Errorf("invalid escape")
			}
			out.WriteRune(r)
			escaped = false
		} else if r == '\\' {
			escaped = true
		} else {
			out.WriteRune(r)
		}
	}
	if escaped {
		return "", fmt.Errorf("trailing escape")
	}
	return out.String(), nil
}

func (f Filter) Empty() bool { return len(f.predicates) == 0 }

func (f Filter) Match(s Subject) bool {
	for _, p := range f.predicates {
		if !matchPredicate(p, s) {
			return false
		}
	}
	return true
}

// Outcome is the three-valued result of evaluating a filter. Undetermined
// means no predicate was false but at least one names a metadata source the
// subject could not read, so membership is unknown rather than negative.
type Outcome uint8

const (
	NoMatch Outcome = iota
	Matched
	Undetermined
)

// Evaluate is Match with unknown metadata kept distinct from a false
// predicate. Sources lists the unreadable sources ("tags", "custom") that
// left an Undetermined outcome. A definitely false predicate wins: a subject
// that fails cpu>=8 is excluded no matter what its tags would have said.
func (f Filter) Evaluate(s Subject) (Outcome, []string) {
	var unknown []string
	for _, p := range f.predicates {
		if source, ok := unreadableSource(p, s); ok {
			if !contains(unknown, source) {
				unknown = append(unknown, source)
			}
			continue
		}
		if !matchPredicate(p, s) {
			return NoMatch, nil
		}
	}
	if len(unknown) > 0 {
		return Undetermined, unknown
	}
	return Matched, nil
}

func unreadableSource(p Predicate, s Subject) (string, bool) {
	switch {
	case p.Field == "tag" && !s.TagsAvailable:
		return "tags", true
	case p.Field == "custom" && !s.CustomAvailable:
		return "custom", true
	}
	return "", false
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// String returns the predicate in the form it was written.
func (p Predicate) String() string {
	field := p.Field
	if p.Qualifier != "" {
		field += "." + p.Qualifier
	}
	return field + p.Operator + p.Value
}

// Predicates returns the parsed predicates in order.
func (f Filter) Predicates() []Predicate { return append([]Predicate(nil), f.predicates...) }

func matchPredicate(p Predicate, s Subject) bool {
	if p.Field == "tag" {
		if !s.TagsAvailable {
			return false
		}
		found := false
		for _, tag := range s.Tags {
			if tag.Name == p.Value && (p.Qualifier == "" || tag.Category == p.Qualifier) {
				found = true
				break
			}
		}
		return found == (p.Operator == "=")
	}
	if p.Field == "custom" {
		if !s.CustomAvailable {
			return false
		}
		for _, attr := range s.CustomAttributes {
			keyMatch := p.Qualifier == "#"+strconv.Itoa(int(attr.Key))
			if keyMatch || (p.Qualifier != "" && attr.Name == p.Qualifier) {
				equal := attr.Value == p.Value
				if p.Operator == "!=" {
					return !equal
				}
				return equal
			}
		}
		return false
	}
	v, ok := s.Fields[p.Field]
	if !ok || v == nil {
		return false
	}
	var equal bool
	switch p.kind {
	case numberValue:
		n, ok := number(v)
		if !ok {
			return false
		}
		switch p.Operator {
		case "=":
			equal = n == p.numeric
		case "!=":
			equal = n != p.numeric
		case "<":
			equal = n < p.numeric
		case "<=":
			equal = n <= p.numeric
		case ">":
			equal = n > p.numeric
		case ">=":
			equal = n >= p.numeric
		}
		return equal
	case boolValue:
		b, ok := v.(bool)
		if !ok {
			return false
		}
		equal = b == p.boolean
	default:
		equal = fmt.Sprint(v) == p.Value
	}
	if p.Operator == "!=" {
		return !equal
	}
	return equal
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		x, err := n.Float64()
		return x, err == nil
	default:
		return 0, false
	}
}

// SubjectFromObject converts a typed vSphere object into a query subject.
func SubjectFromObject(kind vsphere.Kind, object any) (Subject, error) {
	raw, err := json.Marshal(object)
	if err != nil {
		return Subject{}, err
	}
	return SubjectFromJSON(kind, raw)
}

// SubjectFromJSON converts a stored object payload into a query subject.
func SubjectFromJSON(kind vsphere.Kind, raw []byte) (Subject, error) {
	var fields map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&fields); err != nil {
		return Subject{}, err
	}
	s := Subject{Kind: kind, Fields: fields}
	if kind == vsphere.KindDatastore {
		capacity, cok := number(fields["capacity_bytes"])
		free, fok := number(fields["free_bytes"])
		if cok && fok && capacity > 0 {
			s.Fields["free_percent"] = free / capacity * 100
			s.Fields["used_percent"] = (capacity - free) / capacity * 100
		}
	}
	if kindValue, ok := fields["kind"].(string); ok {
		s.Fields["kind"] = kindValue
	}
	if _, ok := s.Fields["kind"]; !ok {
		s.Fields["kind"] = string(kind)
	}
	meta, _ := fields["metadata"].(map[string]any)
	s.TagsStatus = vsphere.MetadataStatus(metaString(meta, "tags_status"))
	s.CustomStatus = vsphere.MetadataStatus(metaString(meta, "custom_attributes_status"))
	s.TagsAvailable = s.TagsStatus == vsphere.MetadataAvailable
	s.CustomAvailable = s.CustomStatus == vsphere.MetadataAvailable
	if values, ok := meta["tags"].([]any); ok {
		for _, value := range values {
			if b, ok := value.(map[string]any); ok {
				s.Tags = append(s.Tags, vsphere.Tag{ID: metaString(b, "id"), Name: metaString(b, "name"), CategoryID: metaString(b, "category_id"), Category: metaString(b, "category")})
			}
		}
	}
	if values, ok := meta["custom_attributes"].([]any); ok {
		for _, value := range values {
			if b, ok := value.(map[string]any); ok {
				key, _ := number(b["key"])
				s.CustomAttributes = append(s.CustomAttributes, vsphere.CustomAttribute{Key: int32(key), Name: metaString(b, "name"), Value: metaString(b, "value")})
			}
		}
	}
	return s, nil
}

func metaString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// KnownField reports whether field is a scalar field of any of kinds (or of
// any kind when kinds is empty), using the same schema Parse checks against.
func KnownField(field string, kinds []vsphere.Kind) bool {
	field = strings.ToLower(field)
	if len(kinds) == 0 {
		for _, fields := range schemas {
			if _, ok := fields[field]; ok {
				return true
			}
		}
		return false
	}
	for _, kind := range kinds {
		if _, ok := schemas[kind][field]; ok {
			return true
		}
	}
	return false
}
