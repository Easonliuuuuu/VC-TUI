package vsphere

import "strings"

// ReconfiguredValue is the value a reconfiguration event set a numeric field
// to, in the form assessment diffs store it: cpu as a count, memory in MB. It
// reports false for any other field, for an event that does not say, and for
// events that are not reconfigurations. The value comes from the one-line
// Detail that reconfiguredFields writes, so the two must change together;
// TestReconfiguredValueReadsWhatDetailWrites holds that line.
func (e VMEvent) ReconfiguredValue(field string) (string, bool) {
	var prefix, suffix string
	switch field {
	case "cpu":
		prefix = "cpu "
	case "memory":
		prefix, suffix = "memory ", " MB"
	default:
		return "", false
	}
	for _, part := range strings.Split(e.Detail, " · ") {
		if v, ok := strings.CutPrefix(part, prefix); ok {
			if v, ok = strings.CutSuffix(v, suffix); ok && v != "" {
				return v, true
			}
		}
	}
	return "", false
}
