package vsphere

import (
	"context"
	"errors"

	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// queryEventsBody is the one hand-rolled operation used to read a VM's
// vCenter event log. The govmomi wrapper lives in the event package, which
// reaches the server through the methods package that vsfleet must not
// import. As with the datastore browser, performance and license queries, the
// body is defined here and TestOnlyPerfAndBrowserShimsDefineFault keeps every
// hand-rolled Fault in the reviewed files.
//
// QueryEvents returns the events matching a filter and changes nothing on the
// server. Unlike CreateCollectorForEvents it leaves no collector behind.
type queryEventsBody struct {
	Req    *types.QueryEvents         `xml:"urn:vim25 QueryEvents,omitempty"`
	Res    *types.QueryEventsResponse `xml:"QueryEventsResponse,omitempty"`
	Fault_ *soap.Fault                `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *queryEventsBody) Fault() *soap.Fault { return b.Fault_ }

// queryEvents runs one QueryEvents call against the event manager.
func queryEvents(ctx context.Context, client *vim25.Client, manager types.ManagedObjectReference, filter types.EventFilterSpec) ([]types.BaseEvent, error) {
	reqBody := queryEventsBody{Req: &types.QueryEvents{This: manager, Filter: filter}}
	var resBody queryEventsBody
	if err := client.RoundTrip(ctx, &reqBody, &resBody); err != nil {
		return nil, err
	}
	if resBody.Res == nil {
		return nil, errors.New("QueryEvents returned no response")
	}
	return resBody.Res.Returnval, nil
}
