package query

import (
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestPredicatesAndMetadata(t *testing.T) {
	filter, err := Parse([]string{"cpu>=8", "power_state=poweredOn", `custom.environment="prod west"`, "tag.Compute=Production"}, []vsphere.Kind{vsphere.KindVM})
	if err != nil {
		t.Fatal(err)
	}
	subject := Subject{Fields: map[string]any{"cpu": float64(16), "power_state": "poweredOn"}, TagsAvailable: true, CustomAvailable: true, Tags: []vsphere.Tag{{Category: "Compute", Name: "Production"}}, CustomAttributes: []vsphere.CustomAttribute{{Name: "environment", Value: "prod west"}}}
	if !filter.Match(subject) {
		t.Fatal("expected predicate set to match")
	}
	subject.TagsAvailable = false
	if filter.Match(subject) {
		t.Fatal("unavailable tags must not match")
	}
}

func TestNumericComparisonIsNotLexical(t *testing.T) {
	filter, err := Parse([]string{"cpu>=8"}, []vsphere.Kind{vsphere.KindVM})
	if err != nil {
		t.Fatal(err)
	}
	if !filter.Match(Subject{Fields: map[string]any{"cpu": float64(10)}}) {
		t.Fatal("10 should be >= 8")
	}
}

func TestTagAndCustomRequireANameAfterTheDot(t *testing.T) {
	for _, expression := range []string{`custom.=""`, "custom.=x", "tag.=x", "custom. =x", "tag.  !=x"} {
		if _, err := Parse([]string{expression}, []vsphere.Kind{vsphere.KindVM}); err == nil {
			t.Errorf("%q was accepted with an empty name", expression)
		}
	}
	if _, err := Parse([]string{"tag=Production", "custom.env=prod"}, []vsphere.Kind{vsphere.KindVM}); err != nil {
		t.Fatalf("named and unqualified forms must still parse: %v", err)
	}
}

func TestInvalidTypesFailEarly(t *testing.T) {
	for _, expression := range []string{"cpu>=many", "power_state>on", "missing=x", "cpu='unterminated"} {
		if _, err := Parse([]string{expression}, []vsphere.Kind{vsphere.KindVM}); err == nil {
			t.Errorf("Parse(%q) succeeded", expression)
		}
	}
}
