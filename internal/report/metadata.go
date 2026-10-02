package report

import (
	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/metareport"
)

// metadataSheetName is vsfleet's long-format tag and custom attribute sheet.
// It is a vsfleet extension, written only on request, so the RVTools sheets
// never gain columns that depend on an estate's tag categories.
const metadataSheetName = "vsfleetMetadata"

var metadataHeaders = []string{"Run ID", "Captured at", "Context", "vCenter ID", "Kind", "Object ID", "Object name", "Path", "Metadata source", "Field ID", "Field", "Value ID", "Value", "Source status", "Source error"}

func metadataSheet(data assessment.ExportData) sheet {
	rows := metareport.Rows(metareport.Objects(data), nil)
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		var captured any = ""
		if !r.CapturedAt.IsZero() {
			captured = r.CapturedAt
		}
		out = append(out, []any{r.RunID, captured, r.Context, r.VCenterID, r.Kind, r.ObjectID, r.ObjectName, r.Path, r.Source, r.FieldID, r.Field, r.ValueID, r.Value, r.Status, r.Error})
	}
	return sheet{name: metadataSheetName, headers: metadataHeaders, rows: out, dateCols: []int{1}}
}
