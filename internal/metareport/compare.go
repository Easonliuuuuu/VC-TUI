package metareport

import (
	"fmt"
	"sort"
	"time"
)

// Membership change kinds.
const (
	ChangeAdded   = "added"
	ChangeRemoved = "removed"
	// ChangeUnknown means at least one side cannot say whether the object
	// was a member: its metadata was unreadable, or its collection failed.
	ChangeUnknown = "unknown"
)

// Change is one object whose membership differs, or cannot be compared,
// between two results.
type Change struct {
	Change    string `json:"change"`
	Before    string `json:"before"`
	After     string `json:"after"`
	Context   string `json:"context"`
	VCenterID string `json:"vcenter_id"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Reason    string `json:"reason,omitempty"`
}

// Comparison is the membership difference between two runs of one report.
type Comparison struct {
	Report           string    `json:"report"`
	BaseRunID        int64     `json:"base_run_id"`
	BaseCapturedAt   time.Time `json:"base_captured_at"`
	TargetRunID      int64     `json:"target_run_id"`
	TargetCapturedAt time.Time `json:"target_captured_at"`
	Unchanged        int       `json:"unchanged"`
	Changes          []Change  `json:"changes"`
	Warnings         []string  `json:"warnings"`
	Complete         bool      `json:"complete"`
}

// Membership states as reported in Change.Before and Change.After.
const (
	stateMember       = "member"
	stateNotMember    = "not_member"
	stateUndetermined = "undetermined"
	stateNotObserved  = "not_observed"
)

// Compare reports how membership changed from base to target. An object is
// removed only when the target observed its collection and positively
// excluded it; one that vanished with a failed collection, or whose
// metadata was unreadable on either side, is an unknown change instead.
func Compare(base, target Result) Comparison {
	out := Comparison{Report: target.Report.Name, BaseRunID: base.RunID, BaseCapturedAt: base.CapturedAt, TargetRunID: target.RunID, TargetCapturedAt: target.CapturedAt, Changes: []Change{}, Warnings: []string{}}
	baseStates, baseMembers := states(base)
	targetStates, targetMembers := states(target)
	keys := map[string]bool{}
	for k := range baseStates {
		keys[k] = true
	}
	for k := range targetStates {
		keys[k] = true
	}
	for k := range keys {
		before := stateOf(base, baseStates, baseMembers, targetMembers, k)
		after := stateOf(target, targetStates, targetMembers, baseMembers, k)
		if before == after {
			if before == stateMember {
				out.Unchanged++
			} else if before == stateUndetermined || before == stateNotObserved {
				out.Changes = append(out.Changes, change(ChangeUnknown, before, after, pick(targetMembers, baseMembers, k)))
			}
			continue
		}
		kind := ChangeUnknown
		switch {
		case before == stateNotMember && after == stateMember:
			kind = ChangeAdded
		case before == stateMember && after == stateNotMember:
			kind = ChangeRemoved
		}
		out.Changes = append(out.Changes, change(kind, before, after, pick(targetMembers, baseMembers, k)))
	}
	sort.Slice(out.Changes, func(i, j int) bool {
		a, b := out.Changes[i], out.Changes[j]
		if a.Change != b.Change {
			return a.Change < b.Change
		}
		if a.Context != b.Context {
			return a.Context < b.Context
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	unknown := 0
	for _, c := range out.Changes {
		if c.Change == ChangeUnknown {
			unknown++
		}
	}
	if unknown > 0 {
		out.Warnings = append(out.Warnings, fmt.Sprintf("%d object(s) could not be compared because membership is unknown in one or both assessments", unknown))
	}
	for _, w := range base.Warnings {
		out.Warnings = append(out.Warnings, fmt.Sprintf("base assessment %d: %s", base.RunID, w))
	}
	for _, w := range target.Warnings {
		out.Warnings = append(out.Warnings, fmt.Sprintf("target assessment %d: %s", target.RunID, w))
	}
	out.Complete = unknown == 0 && base.Complete && target.Complete
	return out
}

func states(r Result) (map[string]string, map[string]Member) {
	st := map[string]string{}
	members := map[string]Member{}
	for _, m := range r.Members {
		st[m.identity] = stateMember
		members[m.identity] = m
	}
	for _, m := range r.Undetermined {
		st[m.identity] = stateUndetermined
		members[m.identity] = m
	}
	return st, members
}

// stateOf is an object's membership in r. An object r does not list was
// either excluded (its collection was observed) or not seen at all.
func stateOf(r Result, st map[string]string, own, other map[string]Member, key string) string {
	if s, ok := st[key]; ok {
		return s
	}
	m, ok := other[key]
	if !ok {
		m = own[key]
	}
	ck := m.Kind
	if ck == "template" {
		ck = "vm"
	}
	for _, c := range r.Collections {
		if c.Kind == ck && identity(c.Context, c.VCenterID, "", "") == identity(m.Context, m.VCenterID, "", "") {
			if c.Observed {
				return stateNotMember
			}
			return stateNotObserved
		}
	}
	return stateNotObserved
}

func pick(first, second map[string]Member, key string) Member {
	if m, ok := first[key]; ok {
		return m
	}
	return second[key]
}

func change(kind, before, after string, m Member) Change {
	c := Change{Change: kind, Before: before, After: after, Context: m.Context, VCenterID: m.VCenterID, Kind: m.Kind, ID: m.ID, Name: m.Name}
	if kind == ChangeUnknown {
		switch {
		case before == stateNotObserved || after == stateNotObserved:
			c.Reason = "collection not observed in one assessment"
		default:
			c.Reason = "a filtered metadata source could not be read"
		}
	}
	return c
}
