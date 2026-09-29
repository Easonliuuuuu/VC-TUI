package assessment

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// BrowseSummary counts the outcome of a --browse-datastores capture from its
// persisted evidence. Requested is false when no datastore carries a browse
// status, which is what a capture without the flag looks like.
type BrowseSummary struct {
	Requested  bool `json:"requested"`
	Datastores int  `json:"datastores"`
	Browsed    int  `json:"browsed"`
	// Denied counts browses the account was not privileged to make.
	Denied int `json:"denied"`
	// Failed counts browses that did not complete for any other reason,
	// including a datastore that reported itself inaccessible.
	Failed int `json:"failed"`
}

// SummarizeBrowse derives the browse outcome of a stored run. It never contacts
// a vCenter.
func SummarizeBrowse(data ExportData) BrowseSummary {
	var out BrowseSummary
	for _, r := range data.Resources {
		if r.Kind != "datastore" {
			continue
		}
		var ds vsphere.Datastore
		if err := json.Unmarshal(r.Payload, &ds); err != nil {
			continue
		}
		out.Datastores++
		switch ds.BrowseStatus {
		case "":
		case "success":
			out.Requested = true
			out.Browsed++
		case "denied":
			// "denied" also records a datastore that reported itself
			// inaccessible; any other denial is a missing privilege, whatever
			// text the typed fault carried.
			out.Requested = true
			if ds.BrowseError == "" || ds.BrowseError == vsphere.DatastoreInaccessibleMessage {
				out.Failed++
			} else {
				out.Denied++
			}
		case "failed":
			// Runs captured before permission faults were classified recorded
			// them as failures; the server's text still says which it was.
			out.Requested = true
			if vsphere.BrowsePermissionDenied(ds.BrowseError) {
				out.Denied++
			} else {
				out.Failed++
			}
		default:
			out.Requested = true
			out.Failed++
		}
	}
	return out
}

// Note renders the shortfall for a run summary line, for example "datastore
// browse denied on 3/3 datastores". It is empty when the browse was not
// requested or every requested browse succeeded.
func (s BrowseSummary) Note() string {
	if !s.Requested {
		return ""
	}
	var parts []string
	if s.Denied > 0 {
		parts = append(parts, fmt.Sprintf("datastore browse denied on %d/%d datastores", s.Denied, s.Datastores))
	}
	if s.Failed > 0 {
		parts = append(parts, fmt.Sprintf("datastore browse failed on %d/%d datastores", s.Failed, s.Datastores))
	}
	return strings.Join(parts, ", ")
}
