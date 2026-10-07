package assessment

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// AmbiguousVMError is returned when a name identifies more than one stable VM
// lineage. Callers should ask for a MoRef, instance UUID, or BIOS UUID.
type AmbiguousVMError struct {
	Query      string
	Candidates []string
}

func (e *AmbiguousVMError) Error() string {
	return fmt.Sprintf("VM selector %q is ambiguous; use a UUID or MoRef (%s)", e.Query, strings.Join(e.Candidates, ", "))
}

// Timeline returns lifecycle and configuration events for one VM lineage.
// The default is change-only; includeUnchanged adds one observation event per
// run, which is useful when exporting a complete audit trail.
func (s *Store) Timeline(ctx context.Context, query, contextName string, includeUnchanged, includeRuntime bool) ([]VMHistoryEvent, error) {
	selectors := []string(nil)
	if strings.TrimSpace(contextName) != "" {
		selectors = []string{contextName}
	}
	return s.TimelineForContexts(ctx, query, selectors, includeUnchanged, includeRuntime)
}

// TimelineForContexts is the multi-context form of Timeline.
func (s *Store) TimelineForContexts(ctx context.Context, query string, selectors []string, includeUnchanged, includeRuntime bool) ([]VMHistoryEvent, error) {
	seed, err := s.HistoryForContexts(ctx, query, selectors)
	if err != nil {
		return nil, err
	}
	if len(seed) == 0 {
		return nil, nil
	}
	lineages := make(map[string]VMHistoryEntry)
	variants := make(map[string]map[string]map[int64]map[string]struct{})
	for _, e := range seed {
		k := lineageKey(e.Observation)
		lineages[k] = e
		if variants[k] == nil {
			variants[k] = make(map[string]map[int64]map[string]struct{})
		}
		if variants[k][e.Observation.VCenterID] == nil {
			variants[k][e.Observation.VCenterID] = make(map[int64]map[string]struct{})
		}
		if variants[k][e.Observation.VCenterID][e.Run.ID] == nil {
			variants[k][e.Observation.VCenterID][e.Run.ID] = make(map[string]struct{})
		}
		variants[k][e.Observation.VCenterID][e.Run.ID][e.Observation.VM.ID] = struct{}{}
	}
	for k, byVC := range variants {
		if strings.HasPrefix(k, "i:") || strings.HasPrefix(k, "b:") {
			ambiguous := false
			for _, byRun := range byVC {
				for _, values := range byRun {
					if len(values) > 1 {
						ambiguous = true
					}
				}
			}
			if ambiguous {
				candidates := make([]string, 0, len(seed))
				for _, e := range seed {
					if lineageKey(e.Observation) == k {
						candidates = append(candidates, fmt.Sprintf("%s (%s)", e.Observation.VM.Name, e.Observation.Context))
					}
				}
				sort.Strings(candidates)
				return nil, &AmbiguousVMError{Query: query, Candidates: candidates}
			}
		}
	}
	if len(lineages) > 1 {
		candidates := make([]string, 0, len(lineages))
		for _, e := range lineages {
			candidates = append(candidates, fmt.Sprintf("%s (%s)", e.Observation.VM.Name, e.Observation.Context))
		}
		sort.Strings(candidates)
		return nil, &AmbiguousVMError{Query: query, Candidates: candidates}
	}
	wanted := lineages[func() string {
		for k := range lineages {
			return k
		}
		return ""
	}()]
	all, err := s.historyForLineage(ctx, wanted.Observation, selectors)
	if err != nil {
		return nil, err
	}
	entries := make([]VMHistoryEntry, 0)
	for _, e := range all {
		if sameLineage(wanted.Observation, e.Observation) {
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Run.StartedAt.Before(entries[j].Run.StartedAt) })
	if len(entries) == 0 {
		return nil, nil
	}
	runs, err := s.Runs(ctx)
	if err != nil {
		return nil, err
	}
	runIndex := make(map[int64]int, len(runs))
	for i, r := range runs {
		runIndex[r.ID] = i
	}
	var events []VMHistoryEvent
	first := entries[0]
	events = append(events, VMHistoryEvent{Kind: "first_seen", Run: first.Run, Context: first.Observation.Context, Name: first.Observation.VM.Name, Observation: &first.Observation, Snapshots: first.Snapshots})
	for i := 1; i < len(entries); i++ {
		before, after := entries[i-1], entries[i]
		if bi, bok := runIndex[before.Run.ID]; bok {
			if ai, aok := runIndex[after.Run.ID]; aok && ai < bi-1 {
				events = append(events, VMHistoryEvent{Kind: "appeared", Run: after.Run, Context: after.Observation.Context, Name: after.Observation.VM.Name, Observation: &after.Observation})
			}
		}
		fields := changedFields(before.Observation.VM, after.Observation.VM, includeRuntime)
		if before.Observation.VM.Name != after.Observation.VM.Name {
			events = append(events, VMHistoryEvent{Kind: "renamed", Run: after.Run, Context: after.Observation.Context, Name: after.Observation.VM.Name, Changes: []FieldChange{{Field: "name", Before: before.Observation.VM.Name, After: after.Observation.VM.Name}}, Observation: &after.Observation})
			fields = removeField(fields, "name")
		}
		if moved(before.Observation.VM, after.Observation.VM, before.Observation.VCenterID, after.Observation.VCenterID) {
			events = append(events, VMHistoryEvent{Kind: "moved", Run: after.Run, Context: after.Observation.Context, Name: after.Observation.VM.Name, Observation: &after.Observation})
		}
		if len(fields) > 0 {
			events = append(events, VMHistoryEvent{Kind: "modified", Run: after.Run, Context: after.Observation.Context, Name: after.Observation.VM.Name, Changes: fields, Observation: &after.Observation})
		}
		for _, sc := range compareSnapshots(storedVM{observation: before.Observation, snapshots: before.Snapshots}, storedVM{observation: after.Observation, snapshots: after.Snapshots}) {
			events = append(events, VMHistoryEvent{Kind: "snapshot-" + sc.Kind, Run: after.Run, Context: after.Observation.Context, Name: after.Observation.VM.Name, Changes: []FieldChange{{Field: "snapshot", Before: sc.Before, After: sc.After}}, Observation: &after.Observation})
		}
		if includeUnchanged && len(fields) == 0 && before.Observation.VM.Name == after.Observation.VM.Name {
			events = append(events, VMHistoryEvent{Kind: "observed", Run: after.Run, Context: after.Observation.Context, Name: after.Observation.VM.Name, Observation: &after.Observation, Snapshots: after.Snapshots})
		}
	}
	// A gap between two successful runs is an unknown boundary. We only emit a
	// vanished event when the immediately following run is comparable and has no
	// observation for this lineage.
	byRun := make(map[int64]VMHistoryEntry, len(entries))
	for _, e := range entries {
		byRun[e.Run.ID] = e
	}
	for i := len(runs) - 1; i >= 0; i-- { // oldest to newest
		if _, ok := byRun[runs[i].ID]; !ok || i == 0 {
			continue
		}
		prevRun := runs[i]
		nextRun := runs[i-1]
		prev, hadPrev := byRun[prevRun.ID]
		_, hadNext := byRun[nextRun.ID]
		if hadPrev && !hadNext && nextRun.Status == RunComplete {
			events = append(events, VMHistoryEvent{Kind: "vanished", Run: nextRun, Context: prev.Observation.Context, Name: prev.Observation.VM.Name, Observation: &prev.Observation})
		}
	}
	sort.SliceStable(events, func(i, j int) bool { return events[i].Run.StartedAt.Before(events[j].Run.StartedAt) })
	return events, nil
}

func removeField(in []FieldChange, name string) []FieldChange {
	out := in[:0]
	for _, f := range in {
		if f.Field != name {
			out = append(out, f)
		}
	}
	return out
}

func lineageKey(o Observation) string {
	if o.VM.InstanceUUID != "" {
		return "i:" + o.VM.InstanceUUID
	}
	if o.VM.BIOSUUID != "" {
		return "b:" + o.VM.BIOSUUID
	}
	if o.VCenterID != "" && o.VM.ID != "" {
		return "m:" + o.VCenterID + "\x00" + o.VM.ID
	}
	return "n:" + strings.ToLower(o.Context) + "\x00" + strings.ToLower(o.VM.Name)
}

func sameLineage(a, b Observation) bool {
	if a.VCenterID != "" && b.VCenterID != "" && a.VCenterID == b.VCenterID && a.VM.ID != "" && a.VM.ID == b.VM.ID {
		return true
	}
	if a.VM.InstanceUUID != "" && a.VM.InstanceUUID == b.VM.InstanceUUID {
		return true
	}
	if a.VM.BIOSUUID != "" && a.VM.BIOSUUID == b.VM.BIOSUUID {
		return true
	}
	return lineageKey(a) == lineageKey(b)
}
