// Package humanize renders sizes, counts and latencies the way an operator
// says them out loud. It sits below both the command line and the terminal UI
// so that a memory figure reads the same in a table, in a detail pane and in
// JSON-adjacent output, rather than drifting between them.
package humanize

import (
	"strconv"
	"strings"
	"time"
)

// MB renders a memory size given in mebibytes: 32G, not 32768.
func MB(mb int64) string {
	switch {
	case mb <= 0:
		return "-"
	case mb >= 1<<20 && mb%(1<<20) == 0:
		return strconv.FormatInt(mb/(1<<20), 10) + "T"
	case mb >= 1024:
		g := float64(mb) / 1024
		if g == float64(int64(g)) {
			return strconv.FormatInt(int64(g), 10) + "G"
		}
		return strconv.FormatFloat(g, 'f', 1, 64) + "G"
	default:
		return strconv.FormatInt(mb, 10) + "M"
	}
}

// Bytes renders a storage size with binary units.
func Bytes(b int64) string {
	if b <= 0 {
		return "-"
	}
	const unit = 1024
	units := []string{"K", "M", "G", "T", "P"}
	if b < unit {
		return strconv.FormatInt(b, 10) + "B"
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	if v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64) + units[exp]
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + units[exp]
}

// FileBytes renders a size the source actually measured, such as a file size
// the datastore browser was asked to report. Zero is a real empty file there,
// so it reads 0B; only a negative value, which no source reports, reads as
// unknown. Bytes stays the renderer for figures where zero means not reported.
func FileBytes(b int64) string {
	if b == 0 {
		return "0B"
	}
	return Bytes(b)
}

// GB renders a size already expressed in gibibytes.
func GB(gb float64) string {
	if gb <= 0 {
		return "-"
	}
	return Bytes(int64(gb * (1 << 30)))
}

// MHz renders a CPU frequency or capacity: 2.4GHz for a core, 76GHz for a
// host's total, which is how vCenter itself reports aggregate compute.
func MHz(mhz int64) string {
	switch {
	case mhz <= 0:
		return "-"
	case mhz >= 1000:
		g := float64(mhz) / 1000
		if g >= 100 {
			return strconv.FormatFloat(g, 'f', 0, 64) + "GHz"
		}
		return strconv.FormatFloat(g, 'f', 1, 64) + "GHz"
	default:
		return strconv.FormatInt(mhz, 10) + "MHz"
	}
}

// Duration renders a latency in the unit that keeps it readable.
func Duration(d time.Duration) string {
	switch {
	case d <= 0:
		return "-"
	case d < time.Millisecond:
		return strconv.FormatInt(d.Microseconds(), 10) + " us"
	case d < time.Second:
		return strconv.FormatInt(d.Milliseconds(), 10) + " ms"
	default:
		return strconv.FormatFloat(d.Seconds(), 'f', 2, 64) + " s"
	}
}

// Dash replaces an empty value with a dash, so that a column of mostly-present
// values does not develop holes.
func Dash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
