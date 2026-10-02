package demo

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Now pins the interface's clock to the instant the synthetic estate was
// built around, so snapshot ages and chart timestamps read the same on every
// run and in every screenshot.
func (b *Backend) Now() time.Time { return demoNow }

// demoProfile is the shape a synthetic VM's counters take, chosen from its
// name so the same VM always draws the same charts.
type demoProfile int

const (
	profileSteady demoProfile = iota
	profileIdle
	profilePeaky
	profileContended
)

// VMPerfSeries implements the TUI's chart extension with synthetic counters.
// Samples are generated in vSphere's raw units and go through the same
// normalisation and summary gating as a live read, so the demo cannot show a
// statistic the real path would withhold. A powered-off VM has no realtime
// samples at all and only the older half of any historical window.
func (b *Backend) VMPerfSeries(_ context.Context, cc *config.Context, vm vsphere.VM, window time.Duration, interval int, now time.Time) (perf.SeriesSet, error) {
	if _, ok := b.estates[cc.Name]; !ok {
		if err := b.failures[cc.Name]; err != nil {
			return perf.SeriesSet{}, err
		}
		return perf.SeriesSet{}, fmt.Errorf("context %q has no sample inventory", cc.Name)
	}
	end := now.UTC()
	set := perf.SeriesSet{IntervalSeconds: interval, WindowStart: end.Add(-window), WindowEnd: end}
	n := int(window.Seconds()) / interval
	if n < 1 || n > vsphere.DefaultPerfMaxSamples {
		return set, fmt.Errorf("a %s window at %ds intervals is %d samples per counter, outside 1 to %d", window, interval, n, vsphere.DefaultPerfMaxSamples)
	}
	profile := profileSteady
	if v := pickN(10, cc.Name, vm.ID, "perf"); v < 4 {
		profile = demoProfile(v)
	}
	seed := frac(cc.Name, vm.ID, fmt.Sprint(interval)) * 2 * math.Pi
	off := 0
	if vm.PowerState != "poweredOn" {
		off = n
		if interval != vsphere.RealtimePerfInterval {
			off = n / 2
		}
	}
	for _, c := range perf.DashboardCounters {
		raw := make([]int64, n)
		for i := range raw {
			if i >= n-off {
				raw[i] = perf.NoData
				continue
			}
			raw[i] = demoSample(c.Metric, profile, vm, interval, seed, i, n)
		}
		set.Series = append(set.Series, perf.NewSeries(c, raw, n, interval, vm.CPU))
	}
	set.Signal, set.SignalReason = perf.Classify(perf.ClassifyInput{Summaries: set.Summaries(), MemoryMB: vm.MemoryMB})
	return set, nil
}

// demoSample is one raw sample in the counter's vSphere unit: CPU usage in
// hundredths of a percent; ready, co-stop and limited in summed
// milliseconds; memory in KB; throughput in KBps; latency in ms; commands
// per second; dropped packets per interval; uptime in seconds.
func demoSample(m perf.Metric, p demoProfile, vm vsphere.VM, interval int, seed float64, i, n int) int64 {
	x := float64(i) / float64(max(1, n-1))
	wave := math.Sin(x*9+seed) + 0.5*math.Sin(x*31+seed*2)
	var cpuPct, readyPct, activePct, balloonMiB, costopPct, swapinKBps float64
	// Disk and network follow their own rhythm rather than CPU's.
	io := math.Sin(x*5+seed*1.7) + 0.4*math.Sin(x*23+seed)
	nw := math.Sin(x*13+seed*0.6) + 0.3*math.Sin(x*41+seed*2.3)
	diskKBps, latencyMs, drops := 900+500*io, 3+1.5*io, 0.0
	switch p {
	case profileIdle:
		cpuPct, readyPct, activePct = 6+2*wave, 0.3, 9+wave
	case profilePeaky:
		cpuPct, readyPct, activePct = 18+6*wave, 0.8+0.3*wave, 20+3*wave
		if i%max(1, n/3) < max(1, n/40) {
			cpuPct += 55
		}
	case profileContended:
		cpuPct, readyPct, activePct = 55+12*wave, 7+3*wave, 48+6*wave
		balloonMiB, costopPct, swapinKBps = 256, 3.5+1.5*wave, 40+30*wave
		diskKBps, latencyMs = 6000+3000*io, 24+10*io
		if i%max(1, n/5) == 0 {
			drops = 40
		}
	default:
		cpuPct, readyPct, activePct = 38+10*wave, 1.2+0.6*wave, 35+5*wave
	}
	if p == profileIdle {
		diskKBps, latencyMs = 40+20*io, 1+0.5*io
	}
	perVCPU := func(pct float64) int64 {
		return int64(math.Max(0, pct) / 100 * float64(interval) * 1000 * float64(max(vm.CPU, 1)))
	}
	memMiB := float64(max(vm.MemoryMB, 1024))
	switch m {
	case perf.CPUCostop:
		return perVCPU(costopPct + 0.2)
	case perf.CPUMaxLimited:
		return 0
	case perf.MemSwapinRate:
		return int64(math.Max(0, swapinKBps))
	case perf.DiskRead:
		return int64(math.Max(0, diskKBps*0.35))
	case perf.DiskWrite:
		return int64(math.Max(0, diskKBps*0.65))
	case perf.DiskMaxLatency:
		return int64(math.Max(0, latencyMs))
	case perf.DiskReadIOPS:
		return int64(math.Max(0, diskKBps*0.35/16))
	case perf.DiskWriteIOPS:
		return int64(math.Max(0, diskKBps*0.65/16))
	case perf.NetReceived:
		return int64(math.Max(0, 1500+900*nw))
	case perf.NetTransmitted:
		return int64(math.Max(0, 400+250*math.Sin(x*11+seed*1.3)))
	case perf.NetDroppedRx, perf.NetDroppedTx:
		return int64(drops / 2)
	case perf.SysUptime:
		// Powered on 41 days before the end of the window, counting forward.
		return int64(41*86400 - (n-1-i)*interval)
	case perf.CPUUsage:
		return int64(math.Max(0, math.Min(100, cpuPct)) * 100)
	case perf.CPUReady:
		return perVCPU(readyPct)
	case perf.MemActive:
		return int64(math.Max(0, math.Min(100, activePct)) / 100 * memMiB * 1024)
	case perf.MemConsumed:
		return int64(memMiB * 0.9 * 1024)
	case perf.MemBalloon:
		return int64(balloonMiB * 1024)
	}
	return 0
}
