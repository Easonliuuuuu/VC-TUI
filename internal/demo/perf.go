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
	for _, c := range perf.Counters {
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

// demoSample is one raw sample: CPU usage in hundredths of a percent, CPU
// ready in summed milliseconds, memory in KB.
func demoSample(m perf.Metric, p demoProfile, vm vsphere.VM, interval int, seed float64, i, n int) int64 {
	x := float64(i) / float64(max(1, n-1))
	wave := math.Sin(x*9+seed) + 0.5*math.Sin(x*31+seed*2)
	var cpuPct, readyPct, activePct, balloonMiB float64
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
		balloonMiB = 256
	default:
		cpuPct, readyPct, activePct = 38+10*wave, 1.2+0.6*wave, 35+5*wave
	}
	memMiB := float64(max(vm.MemoryMB, 1024))
	switch m {
	case perf.CPUUsage:
		return int64(math.Max(0, math.Min(100, cpuPct)) * 100)
	case perf.CPUReady:
		return int64(math.Max(0, readyPct) / 100 * float64(interval) * 1000 * float64(max(vm.CPU, 1)))
	case perf.MemActive:
		return int64(math.Max(0, math.Min(100, activePct)) / 100 * memMiB * 1024)
	case perf.MemConsumed:
		return int64(memMiB * 0.9 * 1024)
	case perf.MemBalloon:
		return int64(balloonMiB * 1024)
	}
	return 0
}
