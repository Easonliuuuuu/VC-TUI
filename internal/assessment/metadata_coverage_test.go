package assessment

import (
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// A collection's metadata status is tallied over every object. Reading it
// from the first object alone would let one failed tag lookup further down
// the page disappear from coverage.
func TestMetadataCoverageCountsEveryObject(t *testing.T) {
	ok := vsphere.Metadata{TagsStatus: vsphere.MetadataAvailable, CustomAttributesStatus: vsphere.MetadataAvailable}
	failed := ok
	failed.TagsStatus, failed.TagsError = vsphere.MetadataUnavailable, "get tag urn:x: 500"
	var c CollectionResult
	applyMetadataCoverage(&c, []vsphere.VM{{Metadata: ok}, {Metadata: ok}, {Metadata: failed}})
	if c.TagsStatus != vsphere.MetadataPartial || c.TagsError != "1 of 3 objects: get tag urn:x: 500" {
		t.Fatalf("tags=%s %q", c.TagsStatus, c.TagsError)
	}
	if c.CustomAttributesStatus != vsphere.MetadataAvailable || c.CustomAttributesError != "" {
		t.Fatalf("custom=%s %q", c.CustomAttributesStatus, c.CustomAttributesError)
	}

	var empty CollectionResult
	applyMetadataCoverage(&empty, []vsphere.Host{})
	if empty.TagsStatus != vsphere.MetadataNotApplicable {
		t.Fatalf("empty collection tags=%s", empty.TagsStatus)
	}

	denied := ok
	denied.TagsStatus, denied.TagsError = vsphere.MetadataDenied, "403 Forbidden"
	var all CollectionResult
	applyMetadataCoverage(&all, []vsphere.Datastore{{Metadata: denied}, {Metadata: denied}}, []vsphere.Datastore{})
	if all.TagsStatus != vsphere.MetadataDenied || all.TagsError != "403 Forbidden" {
		t.Fatalf("denied=%s %q", all.TagsStatus, all.TagsError)
	}
}
