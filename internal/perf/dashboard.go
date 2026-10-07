package perf

// Metrics read only for the VM and vApp dashboards. They are kept out of
// Counters on purpose: Counters drives estate-wide `assessment perf`
// collection, whose batches are sized against vCenter's default limit of 64
// metrics per query and whose stored summaries feed the sizing signal.
// DashboardCounters is read for one VM, or a vApp's members in batches small
// enough for that limit.
const (
	CPUUsageMHz    Metric = "cpu.usagemhz"
	CPUCostop      Metric = "cpu.costop"
	CPUMaxLimited  Metric = "cpu.maxlimited"
	DiskRead       Metric = "disk.read"
	DiskWrite      Metric = "disk.write"
	DiskMaxLatency Metric = "disk.maxTotalLatency"
	DiskReadIOPS   Metric = "disk.numberReadAveraged"
	DiskWriteIOPS  Metric = "disk.numberWriteAveraged"
	NetReceived    Metric = "net.received"
	NetTransmitted Metric = "net.transmitted"
	NetDroppedRx   Metric = "net.droppedRx"
	NetDroppedTx   Metric = "net.droppedTx"
	SysUptime      Metric = "sys.uptime"
)

const (
	UnitMHz     Unit = "MHz"
	UnitKBps    Unit = "KBps"
	UnitMs      Unit = "ms"
	UnitPerSec  Unit = "per-second"
	UnitPackets Unit = "packets"
	UnitSeconds Unit = "seconds"
)

// DashboardCounters is Counters plus the extra counters the VM detail
// dashboard draws, in a stable order.
var DashboardCounters = append(append([]Counter(nil), Counters...),
	Counter{CPUUsageMHz, "cpu", "usagemhz", "average", UnitMHz, "interval average of VM CPU usage in MHz, which adds across VMs and compares with a resource pool's or vApp's CPU limit"},
	Counter{CPUCostop, "cpu", "costop", "summation", UnitPercent, "interval sum of co-stop milliseconds, converted to the average percentage of the interval a single vCPU waited for its siblings to be co-scheduled"},
	Counter{CPUMaxLimited, "cpu", "maxlimited", "summation", UnitPercent, "interval sum of milliseconds the VM was ready but held back by its CPU limit, converted to a per-vCPU percentage"},
	Counter{DiskRead, "disk", "read", "average", UnitKBps, "interval average rate of data read from the VM's disks"},
	Counter{DiskWrite, "disk", "write", "average", UnitKBps, "interval average rate of data written to the VM's disks"},
	Counter{DiskMaxLatency, "disk", "maxTotalLatency", "latest", UnitMs, "highest latency of any of the VM's disks at the end of the interval"},
	Counter{DiskReadIOPS, "disk", "numberReadAveraged", "average", UnitPerSec, "interval average of read commands per second"},
	Counter{DiskWriteIOPS, "disk", "numberWriteAveraged", "average", UnitPerSec, "interval average of write commands per second"},
	Counter{NetReceived, "net", "received", "average", UnitKBps, "interval average rate of data received"},
	Counter{NetTransmitted, "net", "transmitted", "average", UnitKBps, "interval average rate of data transmitted"},
	Counter{NetDroppedRx, "net", "droppedRx", "summation", UnitPackets, "received packets dropped during the interval"},
	Counter{NetDroppedTx, "net", "droppedTx", "summation", UnitPackets, "transmitted packets dropped during the interval"},
	Counter{SysUptime, "sys", "uptime", "latest", UnitSeconds, "seconds since the VM was last powered on, at the end of the interval"},
)

// summedInstances are the counters whose per-device instances (each disk,
// each vNIC) add up to the VM's total. vSphere does not offer every one of
// them as a VM-level aggregate on every version, so they are requested for
// every instance and summed when the aggregate is missing. Latency is not
// here: a maximum does not add.
var summedInstances = map[Metric]bool{
	DiskRead: true, DiskWrite: true, DiskReadIOPS: true, DiskWriteIOPS: true,
	NetReceived: true, NetTransmitted: true, NetDroppedRx: true, NetDroppedTx: true,
}

// SumsInstances reports whether a counter's instances add up to the VM total.
func SumsInstances(m Metric) bool { return summedInstances[m] }

// Dashboard thresholds. Unlike the sizing thresholds these judge nothing
// that is stored or exported; they decide only when the detail dashboard
// draws a warning beside a reading. They are the common VMware rules of
// thumb for each counter.
const (
	// CostopContentionPercent: per-vCPU co-stop at or above this suggests
	// the VM has more vCPUs than the host can schedule together.
	CostopContentionPercent = 3.0
	// LimitedPercent: any measurable time held back by a CPU limit.
	LimitedPercent = 1.0
	// DiskLatencyHighMs: disk latency at or above this is slow storage for
	// most workloads.
	DiskLatencyHighMs = 20.0
	// SwapinRateKBps: any measurable swap-in means the guest is waiting on
	// host swap right now, unlike mem.swapped, which can be old pages. The
	// sizing signal uses it too, as contention.
	SwapinRateKBps = 1.0
	// DroppedPackets: any dropped packet in an interval.
	DroppedPackets = 1.0
)
