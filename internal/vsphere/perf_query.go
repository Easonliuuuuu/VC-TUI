package vsphere

import (
	"context"
	"errors"
	"fmt"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// queryPerfBody is the one hand-rolled operation used for performance
// history. The govmomi wrapper lives in the performance package, which also
// imports the object and methods packages that expose mutations, so — as with
// the datastore browser — the body is defined here and TestOnlyPerfAndBrowserShimsDefineFault
// keeps every hand-rolled Fault in the two reviewed files.
//
// QueryPerf returns statistics for the entities named in its specs and
// changes nothing on the server.
type queryPerfBody struct {
	Req    *types.QueryPerf         `xml:"urn:vim25 QueryPerf,omitempty"`
	Res    *types.QueryPerfResponse `xml:"QueryPerfResponse,omitempty"`
	Fault_ *soap.Fault              `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *queryPerfBody) Fault() *soap.Fault { return b.Fault_ }

// perfAPI is the seam between the collection logic and vSphere. The real
// implementation is perfClient; tests substitute a deterministic fake.
type perfAPI interface {
	// counters lists every counter the server knows.
	counters(ctx context.Context) ([]types.PerfCounterInfo, error)
	// intervals lists the historical roll-up intervals and how long each is
	// retained.
	intervals(ctx context.Context) ([]types.PerfInterval, error)
	// query runs one QueryPerf call for the given specs.
	query(ctx context.Context, specs []types.PerfQuerySpec) ([]types.BasePerfEntityMetricBase, error)
}

type perfClient struct{ c *Client }

func (p perfClient) manager() (types.ManagedObjectReference, error) {
	ref := p.c.VIM().ServiceContent.PerfManager
	if ref == nil {
		return types.ManagedObjectReference{}, errors.New("this server does not expose a PerformanceManager")
	}
	return *ref, nil
}

func (p perfClient) counters(ctx context.Context) ([]types.PerfCounterInfo, error) {
	ref, err := p.manager()
	if err != nil {
		return nil, err
	}
	var pm mo.PerformanceManager
	if err := property.DefaultCollector(p.c.VIM()).RetrieveOne(ctx, ref, []string{"perfCounter"}, &pm); err != nil {
		return nil, fmt.Errorf("read performance counters: %w", err)
	}
	return pm.PerfCounter, nil
}

func (p perfClient) intervals(ctx context.Context) ([]types.PerfInterval, error) {
	ref, err := p.manager()
	if err != nil {
		return nil, err
	}
	var pm mo.PerformanceManager
	if err := property.DefaultCollector(p.c.VIM()).RetrieveOne(ctx, ref, []string{"historicalInterval"}, &pm); err != nil {
		return nil, fmt.Errorf("read performance intervals: %w", err)
	}
	return pm.HistoricalInterval, nil
}

func (p perfClient) query(ctx context.Context, specs []types.PerfQuerySpec) ([]types.BasePerfEntityMetricBase, error) {
	ref, err := p.manager()
	if err != nil {
		return nil, err
	}
	reqBody := queryPerfBody{Req: &types.QueryPerf{This: ref, QuerySpec: specs}}
	var resBody queryPerfBody
	if err := p.c.VIM().RoundTrip(ctx, &reqBody, &resBody); err != nil {
		return nil, err
	}
	if resBody.Res == nil {
		return nil, errors.New("QueryPerf returned no response")
	}
	return resBody.Res.Returnval, nil
}
