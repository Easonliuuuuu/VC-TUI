package assessment

import (
	"context"
	"encoding/json"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// LicenseKind is the persisted collection kind for opt-in license metadata.
// A run only has a collection of this kind when the capture asked for it, so
// its absence means "not collected", never "no licenses".
const LicenseKind = "license"

// licenseCollection reads license metadata for one context and maps it into a
// collection. The persisted payload is vsphere.License, which has no key field:
// keys never leave the vsphere package, so nothing here can store or print one.
func licenseCollection(ctx context.Context, client *vsphere.Client, vcenterID, contextName string) CollectionResult {
	return licenseCollectionFrom(client.FetchLicenses(ctx), vcenterID, contextName)
}

func licenseCollectionFrom(inv vsphere.LicenseInventory, vcenterID, contextName string) CollectionResult {
	c := CollectionResult{Kind: LicenseKind, Status: inv.Status, Error: inv.Message, ItemCount: len(inv.Licenses)}
	for _, l := range inv.Licenses {
		payload, err := json.Marshal(l)
		if err != nil {
			c.Status, c.Error = "failed", err.Error()
			continue
		}
		c.Resources = append(c.Resources, ResourceObservation{VCenterID: vcenterID, Context: contextName, Kind: LicenseKind, ID: l.ID, Name: l.Name, Payload: payload})
	}
	// Tags and custom attributes do not apply to license records.
	c.TagsStatus, c.CustomAttributesStatus = "unavailable", "unavailable"
	return c
}
