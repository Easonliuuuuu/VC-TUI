package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
)

// Output formats.
const (
	FormatTable = "table"
	FormatJSON  = "json"
)

// Status glyphs used across the commands.
const (
	glyphOK      = "✓" // check mark
	glyphFail    = "✕" // ballot X
	glyphSkip    = "–" // en dash
	glyphOnline  = "●" // filled circle
	glyphPending = "◐" // half circle
	glyphOffline = "○" // empty circle
)

// table writes aligned columns. Padding is deliberately generous so that
// output stays readable when a vCenter name and a VM name sit side by side.
type table struct {
	w    *tabwriter.Writer
	cols int
}

func newTable(out io.Writer, headers ...string) *table {
	t := &table{w: tabwriter.NewWriter(out, 0, 4, 3, ' ', 0), cols: len(headers)}
	if len(headers) > 0 {
		fmt.Fprintln(t.w, strings.Join(headers, "\t"))
	}
	return t
}

func (t *table) row(cells ...string) {
	fmt.Fprintln(t.w, strings.Join(cells, "\t"))
}

func (t *table) flush() { _ = t.w.Flush() }

// writeJSON renders v as indented JSON, which is what makes vsfleet scriptable.
func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// fields writes a two-column "label value" block, the shape used by the
// detail and diagnostic commands.
type fields struct {
	w *tabwriter.Writer
}

func newFields(out io.Writer) *fields {
	return &fields{w: tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)}
}

func (f *fields) add(label, value string) {
	if value == "" {
		return
	}
	fmt.Fprintf(f.w, "  %s\t%s\n", label, value)
}

func (f *fields) flush() { _ = f.w.Flush() }

// The rendering rules live in internal/humanize because the terminal UI has
// to produce identical figures; these are the names the commands read with.
func humanMB(mb int64) string { return humanize.MB(mb) }

func humanBytes(b int64) string { return humanize.Bytes(b) }

func humanFileBytes(b int64) string { return humanize.FileBytes(b) }

func humanDuration(d time.Duration) string { return humanize.Duration(d) }

func dash(s string) string { return humanize.Dash(s) }

func itoa(n int) string { return strconv.Itoa(n) }

func i32toa(n int32) string { return strconv.FormatInt(int64(n), 10) }
