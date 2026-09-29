package vsphere

import (
	"context"
	"errors"

	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// queryAssignedLicensesBody is the one hand-rolled operation used for license
// collection. The govmomi wrapper lives in the license package, which also
// exposes AddLicense, RemoveLicense, UpdateAssignedLicense and label updates
// through the object and methods packages that vsfleet must not import. As with
// the datastore browser and performance query, the body is defined here and
// TestOnlyPerfAndBrowserShimsDefineFault keeps every hand-rolled Fault in the
// reviewed files.
//
// QueryAssignedLicenses lists which entities hold which licenses and changes
// nothing on the server. Its response carries license keys; the caller
// (buildLicenseInventory) drops them.
type queryAssignedLicensesBody struct {
	Req    *types.QueryAssignedLicenses         `xml:"urn:vim25 QueryAssignedLicenses,omitempty"`
	Res    *types.QueryAssignedLicensesResponse `xml:"QueryAssignedLicensesResponse,omitempty"`
	Fault_ *soap.Fault                          `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *queryAssignedLicensesBody) Fault() *soap.Fault { return b.Fault_ }

// queryAssignedLicenses returns every entity's assigned license: an empty
// entity ID asks for all of them.
func queryAssignedLicenses(ctx context.Context, client *vim25.Client, manager types.ManagedObjectReference) ([]types.LicenseAssignmentManagerLicenseAssignment, error) {
	reqBody := queryAssignedLicensesBody{Req: &types.QueryAssignedLicenses{This: manager}}
	var resBody queryAssignedLicensesBody
	if err := client.RoundTrip(ctx, &reqBody, &resBody); err != nil {
		return nil, err
	}
	if resBody.Res == nil {
		return nil, errors.New("QueryAssignedLicenses returned no response")
	}
	return resBody.Res.Returnval, nil
}
