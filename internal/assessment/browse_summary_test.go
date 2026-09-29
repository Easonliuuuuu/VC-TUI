package assessment

import (
	"encoding/json"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func browseResource(t *testing.T, name, status, browseError string) ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(vsphere.Datastore{ID: "id-" + name, Name: name, BrowseStatus: status, BrowseError: browseError})
	if err != nil {
		t.Fatal(err)
	}
	return ResourceObservation{Context: "prod", Kind: "datastore", ID: "id-" + name, Name: name, Payload: payload}
}

func TestSummarizeBrowse(t *testing.T) {
	const denied = "ServerFaultCode: Permission to perform this operation was denied."
	cases := []struct {
		name      string
		resources []ResourceObservation
		want      BrowseSummary
		note      string
	}{
		{"not requested", []ResourceObservation{browseResource(t, "a", "", "")}, BrowseSummary{Datastores: 1}, ""},
		{"all denied", []ResourceObservation{browseResource(t, "a", "denied", denied), browseResource(t, "b", "denied", denied), browseResource(t, "c", "failed", denied)}, BrowseSummary{Requested: true, Datastores: 3, Denied: 3}, "datastore browse denied on 3/3 datastores"},
		{"other failure", []ResourceObservation{browseResource(t, "a", "failed", "host unreachable"), browseResource(t, "b", "success", "")}, BrowseSummary{Requested: true, Datastores: 2, Browsed: 1, Failed: 1}, "datastore browse failed on 1/2 datastores"},
		{"typed denial without permission text", []ResourceObservation{browseResource(t, "a", "denied", "NoPermission")}, BrowseSummary{Requested: true, Datastores: 1, Denied: 1}, "datastore browse denied on 1/1 datastores"},
		{"inaccessible is not a privilege problem", []ResourceObservation{browseResource(t, "a", "denied", vsphere.DatastoreInaccessibleMessage)}, BrowseSummary{Requested: true, Datastores: 1, Failed: 1}, "datastore browse failed on 1/1 datastores"},
		{"clean", []ResourceObservation{browseResource(t, "a", "success", "")}, BrowseSummary{Requested: true, Datastores: 1, Browsed: 1}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeBrowse(ExportData{Resources: tc.resources})
			if got != tc.want || got.Note() != tc.note {
				t.Fatalf("got %+v note %q, want %+v note %q", got, got.Note(), tc.want, tc.note)
			}
		})
	}
}
