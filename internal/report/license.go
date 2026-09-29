package report

import (
	"encoding/json"
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// The license worksheets exist only in an export of a run that asked for
// license collection (assessment run --include-licenses). A run that did not
// has no vLicense sheet and no license coverage row: the absence is the "not
// collected" statement, and an empty vLicense sheet is never written for a run
// that did not look. When a run did look, vsfleetCoverage says whether each
// context answered, was denied, or does not support license queries, so a
// missing answer cannot be mistaken for zero licenses.
//
// License keys are never rendered. The vLicense worksheet keeps the RVTools
// "Key" column so column positions match, and fills every cell with the fixed
// marker vsphere.LicenseRedacted. A separate, deliberately designed export
// gate would be required to ever change that; none exists.
const (
	licenseSheetName           = "vLicense"
	licenseAssignmentSheetName = "vsfleetLicenseAssignment"
)

var (
	// licenseHeaders follows the RVTools 4.8.2 vLicense layout with vsfleet's
	// standard trailing context column.
	licenseHeaders = []string{"Name", "Key", "Labels", "Cost Unit", "Total", "Used", "Expiration Date", "Features", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	// licenseDateCols are the zero-based date columns of vLicense.
	licenseDateCols = []int{6}

	licenseAssignmentHeaders = []string{"License", "Edition key", "Entity", "Entity type", "Entity ID", "Scope", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
)

// licensesRecorded reports whether any context in the run has a license
// collection, however it ended.
func licensesRecorded(data assessment.ExportData) bool {
	for _, c := range data.Contexts {
		for _, collection := range c.Collections {
			if collection.Kind == assessment.LicenseKind {
				return true
			}
		}
	}
	for _, r := range data.Resources {
		if r.Kind == assessment.LicenseKind {
			return true
		}
	}
	return false
}

func licenseSheets(data assessment.ExportData, always bool) (vLicense, assignments *sheet) {
	if !always && !licensesRecorded(data) {
		return nil, nil
	}
	return &sheet{name: licenseSheetName, headers: licenseHeaders, rows: licenseRows(data), dateCols: licenseDateCols},
		&sheet{name: licenseAssignmentSheetName, headers: licenseAssignmentHeaders, rows: licenseAssignmentRows(data)}
}

func decodeLicense(r assessment.ResourceObservation) (vsphere.License, bool) {
	if r.Kind != assessment.LicenseKind {
		return vsphere.License{}, false
	}
	var l vsphere.License
	if err := json.Unmarshal(r.Payload, &l); err != nil {
		return vsphere.License{}, false
	}
	return l, true
}

func licenseRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, r := range data.Resources {
		l, ok := decodeLicense(r)
		if !ok {
			continue
		}
		var expiration any
		if l.Expiration != nil {
			expiration = l.Expiration.UTC()
		}
		rows = append(rows, []any{
			nonempty(l.Name, r.Name), vsphere.LicenseRedacted, nil, l.CostUnit, l.Total, l.Used, expiration,
			strings.Join(l.Features, "; "), contextEndpoint(data, r.Context), r.VCenterID, r.Context,
		})
	}
	return rows
}

func licenseAssignmentRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, r := range data.Resources {
		l, ok := decodeLicense(r)
		if !ok {
			continue
		}
		for _, a := range l.Assignments {
			rows = append(rows, []any{
				nonempty(l.Name, r.Name), l.EditionKey, a.EntityName, a.EntityType, a.EntityID, optionalString(a.Scope),
				contextEndpoint(data, r.Context), r.VCenterID, r.Context,
			})
		}
	}
	return rows
}

// licenseCoverageCounts returns, per context, the license rows and assignment
// rows the exporter will write.
func licenseCoverageCounts(data assessment.ExportData) (licenses, assignments map[string]int) {
	licenses, assignments = make(map[string]int), make(map[string]int)
	for _, r := range data.Resources {
		l, ok := decodeLicense(r)
		if !ok {
			continue
		}
		licenses[r.Context]++
		assignments[r.Context] += len(l.Assignments)
	}
	return licenses, assignments
}

// licenseCoverageRow maps a license collection onto the two sheets it feeds.
// A partial collection answered the license list but not the assignments, so
// vLicense is complete and the assignment sheet is reported unavailable.
func licenseCoverageRow(collection assessment.CollectionRun, found, assignmentSheet bool) (status, message string) {
	if !found {
		return "not recorded", "license collection was not part of this context's capture"
	}
	switch {
	case collection.Status == vsphere.LicenseStatusPartial && assignmentSheet:
		return vsphere.LicenseStatusUnavailable, collection.Error
	case collection.Status == vsphere.LicenseStatusPartial:
		return "success", "license list answered; entity assignments were not readable (see " + licenseAssignmentSheetName + ")"
	}
	return collection.Status, collection.Error
}
