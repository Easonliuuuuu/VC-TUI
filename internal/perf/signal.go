package perf

import "fmt"

// Signal is the conservative per-VM sizing reading derived from a window of
// summaries. It is evidence about the window, not a resize instruction.
type Signal string

const (
	// SignalInsufficient: samples were returned but do not support a reading.
	SignalInsufficient Signal = "insufficient-data"
	// SignalUnavailable: a required counter could not be read at all.
	SignalUnavailable Signal = "unavailable"
	// SignalSustainedLow: peak usage stayed low for the whole window.
	SignalSustainedLow Signal = "sustained-low"
	// SignalPeaksObserved: typical usage is low but the window contains
	// intervals of high usage, so a current sample would understate demand.
	SignalPeaksObserved Signal = "peaks-observed"
	// SignalInUse: typical usage is not low.
	SignalInUse Signal = "in-use"
	// SignalContention: CPU ready, ballooning or swapping was observed.
	// Contention outranks utilisation: a VM that is starved looks idle.
	SignalContention Signal = "contention-observed"
	// SignalCPUOnly: CPU usage is supported by enough samples but active
	// memory could not be read, most often because the vCenter keeps
	// statistics level 1, which does not include mem.active. The reason
	// gives the CPU reading; memory is unknown, not low.
	SignalCPUOnly Signal = "cpu-only"
)

// Documented sizing thresholds. They apply to interval averages, so a
// reported peak is a floor on real instantaneous demand.
const (
	// LowPeakPercent: CPU and active-memory peaks below this are "low".
	LowPeakPercent = 30.0
	// HighPeakPercent: a peak at or above this counts as a peak of demand.
	HighPeakPercent = 60.0
	// ReadyContentionPercent: average per-vCPU CPU ready at or above this in
	// the peak interval indicates scheduling contention.
	ReadyContentionPercent = 5.0
	// MemPressureMiB: ballooned or swapped memory at or above this in the peak
	// interval indicates host memory pressure. Smaller amounts are treated as
	// noise rather than contention.
	MemPressureMiB = 1.0
)

// ClassifyInput is the configured size the summaries are judged against.
type ClassifyInput struct {
	Summaries []Summary
	MemoryMB  int64
}

// Classify derives a Signal and a one-line reason. Anything missing,
// denied or too sparse yields SignalUnavailable or SignalInsufficient; a low
// reading is only ever returned when every required counter is supported by
// enough samples. When CPU usage is supported but active memory cannot be
// read at all, the result is SignalCPUOnly, which never claims memory is low.
func Classify(in ClassifyInput) (Signal, string) {
	by := map[Metric]Summary{}
	for _, s := range in.Summaries {
		by[s.Metric] = s
	}

	// Contention is reported from any counter that has supporting samples,
	// even when the utilisation counters are incomplete.
	if s, ok := by[CPUReady]; ok && s.Status == StatusOK && s.Peak != nil && *s.Peak >= ReadyContentionPercent {
		return SignalContention, fmt.Sprintf("CPU ready peaked at %.1f%% per vCPU", *s.Peak)
	}
	for _, m := range []Metric{MemBalloon, MemSwapped} {
		if s, ok := by[m]; ok && s.Status == StatusOK && s.Peak != nil && *s.Peak >= MemPressureMiB {
			return SignalContention, fmt.Sprintf("%s reached %.0f MiB", m, *s.Peak)
		}
	}
	if s, ok := by[MemSwapinRate]; ok && s.Status == StatusOK && s.Peak != nil && *s.Peak >= SwapinRateKBps {
		return SignalContention, fmt.Sprintf("%s peaked at %.0f KBps", MemSwapinRate, *s.Peak)
	}

	cpu, ok := by[CPUUsage]
	switch {
	case !ok:
		return SignalUnavailable, fmt.Sprintf("%s was not collected", CPUUsage)
	case cpu.Status == StatusUnavailable:
		return SignalUnavailable, fmt.Sprintf("%s unavailable: %s", CPUUsage, cpu.Reason)
	case cpu.Status != StatusOK:
		return SignalInsufficient, fmt.Sprintf("%s: %s", CPUUsage, cpu.Reason)
	}
	cpuAvg, cpuPeak := *cpu.Average, *cpu.Peak

	mem, ok := by[MemActive]
	switch {
	case !ok:
		return SignalCPUOnly, fmt.Sprintf("CPU avg %.0f%% peak %.0f%%; active memory unknown: %s was not collected", cpuAvg, cpuPeak, MemActive)
	case mem.Status == StatusUnavailable:
		return SignalCPUOnly, fmt.Sprintf("CPU avg %.0f%% peak %.0f%%; active memory unknown: %s", cpuAvg, cpuPeak, mem.Reason)
	case mem.Status != StatusOK:
		return SignalInsufficient, fmt.Sprintf("%s: %s", MemActive, mem.Reason)
	}
	if in.MemoryMB <= 0 {
		return SignalInsufficient, "configured memory is unknown, so active memory cannot be judged"
	}

	memAvg := *mem.Average / float64(in.MemoryMB) * 100
	memPeak := *mem.Peak / float64(in.MemoryMB) * 100

	switch {
	case cpuPeak < LowPeakPercent && memPeak < LowPeakPercent:
		return SignalSustainedLow, fmt.Sprintf("peaks stayed low: CPU %.0f%%, active memory %.0f%%", cpuPeak, memPeak)
	case (cpuPeak >= HighPeakPercent && cpuAvg < LowPeakPercent) || (memPeak >= HighPeakPercent && memAvg < LowPeakPercent):
		return SignalPeaksObserved, fmt.Sprintf("low typical usage but peaks of CPU %.0f%%, active memory %.0f%%", cpuPeak, memPeak)
	default:
		return SignalInUse, fmt.Sprintf("CPU avg %.0f%% peak %.0f%%, active memory avg %.0f%% peak %.0f%%", cpuAvg, cpuPeak, memAvg, memPeak)
	}
}
