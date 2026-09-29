// Package assessment stores point-in-time inventory observations and explains
// the changes between two observations. It is deliberately independent of the
// live TUI cache: a historical record is immutable evidence, not stale UI.
package assessment

import (
	"encoding/json"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type RunStatus string

// CurrentInventorySchemaVersion identifies the fields captured in inventory
// payloads. Version 2 adds per-VM disks and network adapters; version 3 adds the
// VMware Tools version and version status; version 4 adds guest filesystem
// partitions; version 5 adds the virtual disks backing each of them; version 6
// adds normalized CD-ROM and USB devices (including connection and backing
// identity); version 7 adds VM connection state; version 8 adds resource-pool
// observations; version 9 adds host storage adapter, multipath, physical NIC,
// virtual switch, port group and VMkernel adapter inventory; version 10 adds
// distributed virtual switch and port group inventory; version 11 adds
// datastore backing identity and browse-truncation provenance; version 12 adds
// persisted network inventory; version 13 adds target-neutral VM migration
// configuration and special-device evidence; version 14 adds host storage
// locality evidence; version 15 renames the VM NIC direct-path field to UPT
// compatibility, which is what vSphere reports. The old serialized direct-path
// value in schema 12–14 rows was UPT compatibility mislabeled and is no longer
// read; version 17 records each context's ServiceInstance About identity for
// the `vSource` export tab.
// All keep the payload backward-compatible with older ledger rows:
// a reader of an older run sees the field absent, which is what it is. The
// metadata fields introduced in schema 16 are optional evidence, so consumers
// must continue to treat older captures as metadata-unavailable. Schema 18
// adds the opt-in "license" collection (key-free license metadata and entity
// assignments); a run that did not ask for it simply has no such collection,
// which readers must treat as "not collected", never as "no licenses". Schema
// 19 adds the opt-in datastore file inventory (Datastore.FileInventory) behind
// the vFileInfo export; a run captured without it simply has no such field.
const CurrentInventorySchemaVersion = "19"

const (
	RunRunning  RunStatus = "running"
	RunComplete RunStatus = "complete"
	RunPartial  RunStatus = "partial"
	RunFailed   RunStatus = "failed"
)

type Run struct {
	ID                     int64     `json:"id"`
	Source                 string    `json:"source"`
	Label                  string    `json:"label,omitempty"`
	Note                   string    `json:"note,omitempty"`
	Pinned                 bool      `json:"pinned,omitempty"`
	ToolVersion            string    `json:"tool_version,omitempty"`
	InventorySchemaVersion string    `json:"inventory_schema_version,omitempty"`
	StartedAt              time.Time `json:"started_at"`
	FinishedAt             time.Time `json:"finished_at,omitempty"`
	Status                 RunStatus `json:"status"`
	RequestedContexts      int       `json:"requested_contexts"`
	SuccessfulContexts     int       `json:"successful_contexts"`
	RequestedCollections   int       `json:"requested_collections,omitempty"`
	SuccessfulCollections  int       `json:"successful_collections,omitempty"`
}

type ContextRun struct {
	RunID       int64           `json:"run_id"`
	Name        string          `json:"context"`
	Endpoint    string          `json:"endpoint"`
	Datacenter  string          `json:"datacenter,omitempty"`
	VCenterID   string          `json:"vcenter_id,omitempty"`
	StartedAt   time.Time       `json:"started_at"`
	FinishedAt  time.Time       `json:"finished_at,omitempty"`
	VMStatus    string          `json:"vm_status"`
	Error       string          `json:"error,omitempty"`
	Collections []CollectionRun `json:"collections,omitempty"`
	// Source is the ServiceInstance About record the server reported when
	// this context was captured. It is nil for runs captured before schema 17
	// and for contexts that never connected; consumers must not substitute
	// current configuration or a live query for it.
	Source *SourceInfo `json:"source,omitempty"`
}

// SourceInfo is the persisted ServiceInstance About evidence for one context
// of one run. Every field is stored exactly as the server reported it; an
// empty value means the server did not report it.
type SourceInfo struct {
	Name                  string `json:"name,omitempty"`
	FullName              string `json:"full_name,omitempty"`
	Vendor                string `json:"vendor,omitempty"`
	Version               string `json:"version,omitempty"`
	PatchLevel            string `json:"patch_level,omitempty"`
	Build                 string `json:"build,omitempty"`
	OSType                string `json:"os_type,omitempty"`
	ProductLineID         string `json:"product_line_id,omitempty"`
	APIType               string `json:"api_type,omitempty"`
	APIVersion            string `json:"api_version,omitempty"`
	InstanceUUID          string `json:"instance_uuid,omitempty"`
	LicenseProductName    string `json:"license_product_name,omitempty"`
	LicenseProductVersion string `json:"license_product_version,omitempty"`
}

// SourceInfoFromAbout converts a live client's About record into the value
// persisted with a run.
func SourceInfoFromAbout(a vsphere.About) *SourceInfo {
	return &SourceInfo{
		Name: a.Name, FullName: a.FullName, Vendor: a.Vendor, Version: a.Version, PatchLevel: a.PatchLevel,
		Build: a.Build, OSType: a.OSType, ProductLineID: a.ProductLineID, APIType: a.APIType, APIVersion: a.APIVersion,
		InstanceUUID: a.InstanceID, LicenseProductName: a.LicenseProductName, LicenseProductVersion: a.LicenseProductVersion,
	}
}

// CollectionRun records the result of one resource-group collection inside a
// context. Keeping this separate from ContextRun lets a run remain useful when
// an account can read VMs but not hosts, clusters, or datastores.
type CollectionRun struct {
	Kind                   string    `json:"kind"`
	StartedAt              time.Time `json:"started_at"`
	FinishedAt             time.Time `json:"finished_at,omitempty"`
	Status                 string    `json:"status"`
	Error                  string    `json:"error,omitempty"`
	ItemCount              int       `json:"item_count"`
	TagsStatus             string    `json:"tags_status,omitempty"`
	TagsError              string    `json:"tags_error,omitempty"`
	CustomAttributesStatus string    `json:"custom_attributes_status,omitempty"`
	CustomAttributesError  string    `json:"custom_attributes_error,omitempty"`
}

// ResourceObservation is the durable, versioned representation used for
// hosts, clusters, datastores, resource pools, and distributed switches. Payload contains the original typed object
// so new fields can be added without another ledger migration; the indexed
// identity columns keep diffs and trend queries inexpensive.
type ResourceObservation struct {
	VCenterID string          `json:"vcenter_id"`
	Context   string          `json:"context"`
	Kind      string          `json:"kind"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Payload   json.RawMessage `json:"payload"`
	// Indexed metric projections are kept out of the JSON envelope; Payload
	// remains the lossless source for fields that are not yet projected.
	CPUCapacity     *float64 `json:"-"`
	CPUUsed         *float64 `json:"-"`
	MemoryCapacity  *float64 `json:"-"`
	MemoryUsed      *float64 `json:"-"`
	StorageCapacity *float64 `json:"-"`
	StorageFree     *float64 `json:"-"`
}

type Observation struct {
	VCenterID string     `json:"vcenter_id"`
	Context   string     `json:"context"`
	VM        vsphere.VM `json:"vm"`
}

type FieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type VMChange struct {
	Kind       string        `json:"kind"`
	Changes    []string      `json:"changes,omitempty"`
	Name       string        `json:"name"`
	Context    string        `json:"context"`
	MatchBasis string        `json:"match_basis,omitempty"`
	Fields     []FieldChange `json:"fields,omitempty"`
	Before     *Observation  `json:"before,omitempty"`
	After      *Observation  `json:"after,omitempty"`
}

type SnapshotChange struct {
	Kind       string `json:"kind"`
	VMName     string `json:"vm_name"`
	Context    string `json:"context"`
	Name       string `json:"snapshot"`
	Before     string `json:"before,omitempty"`
	After      string `json:"after,omitempty"`
	CreateTime string `json:"create_time,omitempty"`
}

type SnapshotAge struct {
	VMName     string        `json:"vm_name"`
	Context    string        `json:"context"`
	Name       string        `json:"snapshot"`
	CreateTime time.Time     `json:"create_time"`
	Age        time.Duration `json:"age"`
	FirstSeen  time.Time     `json:"first_seen"`
	LastSeen   time.Time     `json:"last_seen"`
}

type Diff struct {
	SchemaVersion int              `json:"schema_version"`
	Base          Run              `json:"base"`
	Target        Run              `json:"target"`
	VMs           []VMChange       `json:"vms,omitempty"`
	Resources     []ResourceChange `json:"resources,omitempty"`
	Snapshots     []SnapshotChange `json:"snapshots,omitempty"`
	SnapshotAges  []SnapshotAge    `json:"snapshot_ages,omitempty"`
	Warnings      []string         `json:"warnings,omitempty"`
	Coverage      []CoverageIssue  `json:"coverage,omitempty"`
	Policy        *PolicyResult    `json:"policy,omitempty"`
	Counts        DiffCounts       `json:"counts"`
}

type ResourceChange struct {
	Kind       string               `json:"kind"`
	Changes    []string             `json:"changes,omitempty"`
	ID         string               `json:"id"`
	Name       string               `json:"name"`
	Context    string               `json:"context"`
	MatchBasis string               `json:"match_basis,omitempty"`
	Fields     []FieldChange        `json:"fields,omitempty"`
	Before     *ResourceObservation `json:"before,omitempty"`
	After      *ResourceObservation `json:"after,omitempty"`
}

type DiffCounts struct {
	Appeared  int `json:"appeared"`
	Vanished  int `json:"vanished"`
	Moved     int `json:"moved"`
	Modified  int `json:"modified"`
	Snapshots int `json:"snapshots"`
	Resources int `json:"resources"`
}

type ContextProgress struct {
	Context     string
	Status      string
	VMs         int
	Collections []CollectionProgress
	Error       error
}

type CollectionProgress struct {
	Kind      string
	Status    string
	ItemCount int
	Error     error
}

type CollectionResult struct {
	Kind                   string
	Status                 string
	Error                  string
	ItemCount              int
	Resources              []ResourceObservation
	TagsStatus             string
	TagsError              string
	CustomAttributesStatus string
	CustomAttributesError  string
}

type VMHistoryEntry struct {
	Run         Run                  `json:"run"`
	Observation Observation          `json:"observation"`
	Snapshots   []vsphere.VMSnapshot `json:"snapshots,omitempty"`
}

// CaptureLease identifies the one process allowed to write a live capture.
// Token is a fencing value: a stale process cannot save after its lease has
// expired and another process has taken over.
type CaptureLease struct {
	Token     string
	RunID     int64
	Operation string
	ExpiresAt time.Time
}

type RunMetadata struct {
	Label                  string
	Note                   string
	Pinned                 bool
	ToolVersion            string
	InventorySchemaVersion string
	// Licenses records that the capture asked for the opt-in license
	// collection, so a complete run expects one license collection per context.
	Licenses bool
}

type CoverageIssue struct {
	Scope   string `json:"scope"`
	Context string `json:"context,omitempty"`
	Message string `json:"message"`
}

type PolicyViolation struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

type PolicyResult struct {
	Passed     bool              `json:"passed"`
	Violations []PolicyViolation `json:"violations,omitempty"`
}

type VMHistoryEvent struct {
	Kind        string               `json:"kind"`
	Run         Run                  `json:"run"`
	Context     string               `json:"context"`
	Name        string               `json:"name"`
	Changes     []FieldChange        `json:"changes,omitempty"`
	Observation *Observation         `json:"observation,omitempty"`
	Snapshots   []vsphere.VMSnapshot `json:"snapshots,omitempty"`
}
