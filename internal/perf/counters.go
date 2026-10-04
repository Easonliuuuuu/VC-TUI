// Package perf holds the offline semantics of VM performance history: which
// vSphere counters are read, what their units mean, how raw samples become a
// summary, and how summaries become a conservative sizing signal. It imports
// nothing from vSphere so every rule is testable without a vCenter.
package perf

// Metric names one normalised measurement vsfleet reports.
type Metric string

const (
	CPUUsage    Metric = "cpu.usage"
	CPUReady    Metric = "cpu.ready"
	MemActive   Metric = "mem.active"
	MemConsumed Metric = "mem.consumed"
	MemBalloon  Metric = "mem.balloon"
	MemSwapped  Metric = "mem.swapped"
)

// Unit is the unit of a normalised value, not of the raw vSphere counter.
type Unit string

const (
	UnitPercent Unit = "percent"
	UnitMiB     Unit = "MiB"
)

// Counter ties a normalised metric to the vSphere counter it is read from.
// Group/Name/Rollup are the PerformanceManager counter identity, so the
// collector can resolve the numeric counter id by name.
type Counter struct {
	Metric Metric
	Group  string
	Name   string
	Rollup string
	Unit   Unit
	// Aggregation states what one sample already is, because every historical
	// vSphere sample is a rollup over an interval rather than an instant.
	Aggregation string
}

// VSphereName is the counter's "group.name.rollup" identity.
func (c Counter) VSphereName() string { return c.Group + "." + c.Name + "." + c.Rollup }

// Counters is the complete, ordered set vsfleet requests. Storage I/O is
// deliberately absent until its counters and cost are validated.
var Counters = []Counter{
	{CPUUsage, "cpu", "usage", "average", UnitPercent, "interval average of VM CPU usage, as a percentage of the VM's configured vCPU capacity"},
	{CPUReady, "cpu", "ready", "summation", UnitPercent, "interval sum of CPU ready milliseconds, converted to the average percentage of the interval a single vCPU waited to be scheduled"},
	{MemActive, "mem", "active", "average", UnitMiB, "interval average of guest memory recently touched, as estimated by the hypervisor"},
	{MemConsumed, "mem", "consumed", "average", UnitMiB, "interval average of host memory backing the VM"},
	{MemBalloon, "mem", "vmmemctl", "average", UnitMiB, "interval average of memory reclaimed by the balloon driver"},
	{MemSwapped, "mem", "swapped", "average", UnitMiB, "interval average of VM memory swapped to the host swap file"},
}

// CounterFor returns the counter definition for a metric.
func CounterFor(m Metric) (Counter, bool) {
	for _, c := range Counters {
		if c.Metric == m {
			return c, true
		}
	}
	return Counter{}, false
}

// NoData is the value vSphere uses for a sample it has no reading for: a VM
// that was powered off, a counter that was not collected at that level, or
// history that was rolled off. It is never a measurement of zero.
const NoData int64 = -1

// Normalize converts one raw vSphere sample to the counter's normalised unit.
// ok is false for NoData or any negative value, so callers cannot fold a
// missing sample into a mean as zero. vcpu is the VM's configured vCPU count
// and intervalSeconds the sample interval; both are only needed for CPU ready.
func Normalize(c Counter, raw int64, intervalSeconds int, vcpu int32) (value float64, ok bool) {
	if raw < 0 {
		return 0, false
	}
	switch c.Metric {
	case CPUUsage:
		// vSphere reports usage in hundredths of a percent.
		return float64(raw) / 100, true
	case CPUReady, CPUCostop, CPUMaxLimited:
		if intervalSeconds <= 0 || vcpu <= 0 {
			return 0, false
		}
		// The VM-level (aggregate) instance sums ready (or co-stop, or
		// limited) time over all vCPUs, so divide by vCPU count to get a
		// per-vCPU figure.
		return float64(raw) / (float64(intervalSeconds) * 1000 * float64(vcpu)) * 100, true
	case MemActive, MemConsumed, MemBalloon, MemSwapped:
		// vSphere reports these memory counters in KB.
		return float64(raw) / 1024, true
	case CPUUsageMHz, MemSwapinRate, DiskRead, DiskWrite, NetReceived, NetTransmitted,
		DiskMaxLatency, DiskReadIOPS, DiskWriteIOPS, NetDroppedRx, NetDroppedTx, SysUptime:
		// Already in the normalised unit: MHz, KBps, milliseconds, commands
		// per second, packets per interval, or seconds.
		return float64(raw), true
	}
	return 0, false
}
