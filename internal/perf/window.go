package perf

import "time"

// Window statuses describe how much of the requested collection completed.
const (
	WindowSuccess = "success"
	WindowPartial = "partial"
	WindowFailed  = "failed"
)

// Budget is the bound a collection ran under. It is stored with the window so
// a report can say what was, and was not, asked of the vCenter.
type Budget struct {
	MaxVMs      int `json:"max_vms"`
	MaxRequests int `json:"max_requests"`
	MaxSamples  int `json:"max_samples"`
	BatchVMs    int `json:"batch_vms"`
	// TimeoutSeconds is the runtime bound of the whole collection.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// VMResult is one VM's outcome. VM identity is captured at collection time so
// the result is meaningful without the inventory run it was collected beside.
type VMResult struct {
	MoRef        string `json:"moref"`
	InstanceUUID string `json:"instance_uuid,omitempty"`
	Name         string `json:"name"`
	PowerState   string `json:"power_state,omitempty"`
	VCPU         int32  `json:"vcpu"`
	MemoryMB     int64  `json:"memory_mb"`
	// Sampled is false when the VM was not queried, for instance because the
	// request or VM budget ran out. Its Signal is then insufficient-data.
	Sampled      bool      `json:"sampled"`
	Signal       Signal    `json:"signal"`
	SignalReason string    `json:"signal_reason,omitempty"`
	Summaries    []Summary `json:"summaries,omitempty"`
}

// Window is one bounded collection: what was asked, what it cost, and what
// came back. It is kept apart from immutable inventory observations.
type Window struct {
	ID        int64  `json:"id"`
	Context   string `json:"context"`
	VCenterID string `json:"vcenter_id,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	// Source records the API and server the numbers came from, for example
	// "PerformanceManager.QueryPerf historical interval 300s; VMware vCenter
	// Server 8.0.3 build-24022515".
	Source          string    `json:"source"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	WindowStart     time.Time `json:"window_start"`
	WindowEnd       time.Time `json:"window_end"`
	IntervalSeconds int       `json:"interval_seconds"`
	// ExpectedSamples is the number of samples one counter should return
	// over the window at this interval.
	ExpectedSamples int        `json:"expected_samples"`
	RequestsUsed    int        `json:"requests_used"`
	VMsRequested    int        `json:"vms_requested"`
	VMsSampled      int        `json:"vms_sampled"`
	Status          string     `json:"status"`
	Error           string     `json:"error,omitempty"`
	Budget          Budget     `json:"budget"`
	VMs             []VMResult `json:"vms,omitempty"`
}
