package query

import (
	"strings"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// FuzzParseAndEvaluate feeds arbitrary --where expressions through the
// parser. An accepted unquoted expression must print back to one that parses
// the same way (String drops quotes, so quoted values are display-only), and
// the three-valued Evaluate must never contradict Match.
func FuzzParseAndEvaluate(f *testing.F) {
	for _, seed := range []string{
		"cpu>=8", "power_state=poweredOn", `custom.environment="prod west"`,
		"tag.Compute=Production", "name~web", "memory_mb<4096", "template=false",
		"free_percent<10", "cpu>=", "=x", "tag.=", `custom."a b"=c`, "cpu>=1e309",
	} {
		f.Add(seed, true, true)
		f.Add(seed, false, true)
	}
	vm := vsphere.VM{Name: "fuzz-vm", CPU: 4, MemoryMB: 8192, PowerState: "poweredOn", GuestOS: "otherLinux64Guest"}
	f.Fuzz(func(t *testing.T, expression string, tagsAvailable, customAvailable bool) {
		filter, err := Parse([]string{expression}, nil)
		if err != nil {
			return
		}
		for _, p := range filter.Predicates() {
			if (p.Field == "tag" || p.Field == "custom") && strings.Contains(expression, p.Field+".") && strings.TrimSpace(p.Qualifier) == "" {
				t.Fatalf("%q was accepted with an empty %s name", expression, p.Field)
			}
			if strings.ContainsAny(expression, `'"`) {
				continue
			}
			again, err := Parse([]string{p.String()}, nil)
			if err != nil {
				t.Fatalf("%q parsed, but its printed form %q does not: %v", expression, p.String(), err)
			}
			if got := again.Predicates(); len(got) != 1 || got[0].String() != p.String() || got[0].Field != p.Field || got[0].Operator != p.Operator || got[0].Value != p.Value {
				t.Fatalf("%q printed as %q, which re-parses as %+v", expression, p.String(), got)
			}
		}

		subject, err := SubjectFromObject(vsphere.KindVM, vm)
		if err != nil {
			t.Fatal(err)
		}
		subject.TagsAvailable, subject.CustomAvailable = tagsAvailable, customAvailable
		subject.Tags = []vsphere.Tag{{Category: "Compute", Name: "Production"}}
		subject.CustomAttributes = []vsphere.CustomAttribute{{Name: "environment", Value: "prod west"}}
		matched := filter.Match(subject)
		outcome, sources := filter.Evaluate(subject)
		if matched && outcome != Matched {
			t.Fatalf("%q: Match is true but Evaluate is %v", expression, outcome)
		}
		if outcome == NoMatch && matched {
			t.Fatalf("%q: Evaluate is NoMatch but Match is true", expression)
		}
		if outcome == Undetermined && tagsAvailable && customAvailable {
			t.Fatalf("%q: Undetermined with every metadata source readable (%v)", expression, sources)
		}
	})
}
