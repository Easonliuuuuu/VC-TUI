package vsphere

import (
	"context"
	"errors"
	"time"

	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// currentTimeBody is the one hand-rolled operation used to read the vCenter's
// clock. The govmomi wrapper lives in the methods package that vsfleet must
// not import. As with the event, license and performance queries, the body is
// defined here and TestOnlyPerfAndBrowserShimsDefineFault keeps every
// hand-rolled Fault in the reviewed files.
//
// ServiceInstance.CurrentTime returns the server's time and changes nothing.
type currentTimeBody struct {
	Req    *types.CurrentTime         `xml:"urn:vim25 CurrentTime,omitempty"`
	Res    *types.CurrentTimeResponse `xml:"CurrentTimeResponse,omitempty"`
	Fault_ *soap.Fault                `xml:"http://schemas.xmlsoap.org/soap/envelope/ Fault,omitempty"`
}

func (b *currentTimeBody) Fault() *soap.Fault { return b.Fault_ }

// currentTime runs one CurrentTime call against the service instance.
func currentTime(ctx context.Context, client *vim25.Client) (time.Time, error) {
	reqBody := currentTimeBody{Req: &types.CurrentTime{This: vim25.ServiceInstance}}
	var resBody currentTimeBody
	if err := client.RoundTrip(ctx, &reqBody, &resBody); err != nil {
		return time.Time{}, err
	}
	if resBody.Res == nil {
		return time.Time{}, errors.New("CurrentTime returned no response")
	}
	return resBody.Res.Returnval, nil
}

// ClockOffset measures how far the vCenter's clock is ahead of the local one:
// negative when the vCenter is behind. It makes one read-only CurrentTime call
// and compares the answer with the midpoint of the local time before and
// after it, which cancels a symmetric network delay; the remaining error is at
// most half the round trip.
//
// vCenter event times come from this clock, while run times come from the
// local one; the offset is what lets the two be compared.
func (c *Client) ClockOffset(ctx context.Context) (time.Duration, error) {
	if c == nil || c.vim == nil || c.vim.Client == nil {
		return 0, errors.New("not connected")
	}
	before := time.Now()
	remote, err := currentTime(ctx, c.vim.Client)
	after := time.Now()
	if err != nil {
		return 0, err
	}
	return remote.Sub(before.Add(after.Sub(before) / 2)), nil
}
