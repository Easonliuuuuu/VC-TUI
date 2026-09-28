package assessment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// DefaultPerfMaxRuntime bounds the QueryPerf phase of one context. Connecting
// and listing VMs are bounded separately by the session timeout, exactly as in
// an inventory capture.
const DefaultPerfMaxRuntime = 10 * time.Minute

// PerfCaptureOptions describe one bounded performance collection.
type PerfCaptureOptions struct {
	Contexts   []*config.Context
	Perf       vsphere.PerfOptions
	MaxRuntime time.Duration
	Now        func() time.Time
	Progress   func(perf.Window)
}

// CapturePerf collects performance history from every context and stores each
// window. It contacts a vCenter read-only and never touches inventory runs. A
// context that cannot be reached is still recorded, as a failed window, so the
// attempt and its error are part of the history. Windows are returned in the
// order of opts.Contexts.
func (c *Collector) CapturePerf(ctx context.Context, opts PerfCaptureOptions) ([]perf.Window, error) {
	if c.Store == nil || c.Manager == nil {
		return nil, fmt.Errorf("assessment collector is not configured")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxRuntime <= 0 {
		opts.MaxRuntime = DefaultPerfMaxRuntime
	}
	opts.Perf.TimeoutSeconds = int(opts.MaxRuntime.Seconds())
	windows := make([]perf.Window, len(opts.Contexts))
	errs := make([]error, len(opts.Contexts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, session.DefaultConcurrency)
	for i, cc := range opts.Contexts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			w := c.capturePerfContext(ctx, cc, opts)
			saved, err := c.Store.SavePerfWindow(context.WithoutCancel(ctx), w)
			if err != nil {
				errs[i] = fmt.Errorf("%s: %w", cc.Name, err)
				saved = w
				saved.Status, saved.Error = perf.WindowFailed, fmt.Sprintf("save performance window: %v", err)
			}
			windows[i] = saved
			if opts.Progress != nil {
				opts.Progress(saved)
			}
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return windows, err
		}
	}
	return windows, nil
}

func (c *Collector) capturePerfContext(parent context.Context, cc *config.Context, opts PerfCaptureOptions) perf.Window {
	now := opts.Now().UTC()
	w := perf.Window{
		Context: cc.Name, Endpoint: cc.Endpoint, Status: perf.WindowFailed,
		StartedAt: now, FinishedAt: now, WindowStart: now.Add(-opts.Perf.Window), WindowEnd: now,
	}
	fail := func(msg string) perf.Window {
		w.Error, w.FinishedAt = msg, opts.Now().UTC()
		return w
	}

	opCtx, cancel, tracker := c.Manager.Operation(parent)
	s, err := c.Manager.Connect(opCtx, cc)
	if err != nil {
		cancel()
		return fail(c.Manager.TimeoutError(err, tracker).Error())
	}
	client := s.Client()
	if client == nil {
		cancel()
		return fail("context is not connected")
	}
	w.VCenterID = client.About.InstanceID
	if w.VCenterID == "" {
		w.VCenterID = cc.Endpoint
	}
	idx, err := client.NewIndex(opCtx)
	if err != nil {
		cancel()
		return fail(c.Manager.TimeoutError(err, tracker).Error())
	}
	inv := client.FetchGroup(opCtx, idx, vsphere.GroupVMs)
	cancel()
	if msg, failed := inv.ErrorFor(vsphere.KindVM); failed {
		return fail("list VMs: " + msg)
	}

	perfCtx, cancelPerf := context.WithTimeout(parent, opts.MaxRuntime)
	defer cancelPerf()
	pw := client.CollectPerf(perfCtx, inv.VMs, opts.Perf)
	pw.Context, pw.VCenterID, pw.Endpoint = cc.Name, w.VCenterID, cc.Endpoint
	pw.StartedAt = now
	return pw
}
