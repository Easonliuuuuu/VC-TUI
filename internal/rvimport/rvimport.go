// Package rvimport adapts RVTools-compatible XLSX exports into vsfleet's
// normalized assessment model.
//
// It is deliberately an adapter, not a second domain model: an RVTools
// workbook layout is a source format this package translates from, and
// nothing about that layout is allowed to leak into vsphere or assessment
// types. A worksheet or column this package does not recognize is reported
// as unrecognized rather than silently dropped or guessed at, and a field
// the workbook does not carry is left absent rather than defaulted to a
// value that would read as confirmed evidence.
//
// The invariant every mapping here answers to: imported evidence may reduce
// confidence, but the lack of a workbook field must never improve a verdict.
// Evidence a workbook cannot answer for is recorded as an "unavailable"
// collection, never as "empty" — a missing vHost worksheet is not an estate
// with no hosts.
//
// The whole workbook is parsed into memory before anything is written to the
// assessment store, and a run is written in one all-or-nothing pass — a
// malformed or partially-mappable workbook must not leave a half-written run
// behind.
package rvimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// ProfileVersion identifies this adapter's worksheet/column mapping. It is
// recorded on every imported run's note so a richer profile can be told apart
// from runs an earlier version of this importer produced.
const ProfileVersion = "rvtools-v3"

// importSource marks an imported run's Run.Source, the one field every other
// command already reads, so "vsfleet assessment list" tells an imported run
// apart from a live capture without inspecting its note text.
const importSource = "rvtools-import"

// baseSchemaVersion is the inventory schema level every import can honestly
// claim: "2", the version that added per-VM disks and network adapters, which
// vDisk and vNetwork give it. Claiming assessment.CurrentInventorySchemaVersion
// instead would tell every schema-gated health rule that evidence added by a
// later schema was collected and came back empty — turning "this workbook
// never carried that evidence" into a false "confirmed absent". Rules gated
// above the claimed level stay not-evaluated, the same way they do for an old
// live capture. See schemaVersionFor for how a workbook earns more.
const baseSchemaVersion = "2"

// noteSHAPrefix labels the workbook fingerprint in a run's note. Repeat-import
// detection searches for it, so it is part of the note format.
const noteSHAPrefix = "Source SHA-256: "

// Recognized worksheets. Names are matched exactly except for explicitly
// supported historical vsfleet aliases; anything else is reported as ignored.
const (
	sheetVInfo      = "vInfo"
	sheetVCPU       = "vCPU"
	sheetVMemory    = "vMemory"
	sheetVDisk      = "vDisk"
	sheetVPartition = "vPartition"
	sheetVNetwork   = "vNetwork"
	sheetVTools     = "vTools"
	sheetVHost      = "vHost"
	sheetVHBA       = "vHBA"
	sheetVNIC       = "vNIC"
	sheetVSwitch    = "vSwitch"
	sheetVPort      = "vPort"
	sheetVSCVMK     = "vSC+VMK"
	sheetVMultiPath = "vMultiPath"
	sheetDVSwitch   = "dvSwitch"
	sheetDVPort     = "dvPort"
	sheetVCluster   = "vCluster"
	sheetVDatastore = "vDatastore"
	sheetVSnapshot  = "vSnapshot"
	sheetVCD        = "vCD"
	sheetVUSB       = "vUSB"
	sheetVMetaData  = "vMetaData"
)

// headerAliases maps known RVTools spellings to the canonical headers used by
// this importer. Matching remains case-insensitive and accepts the original
// vsfleet-compatible spelling as well as these aliases.
var headerAliases = map[string]map[string]string{
	sheetVDisk: {
		"shared bus": "SharedBus",
	},
	sheetDVSwitch: {
		"switch":    "DVS",
		"max ports": "# Max ports",
	},
	sheetDVPort: {
		"port":                "Port group",
		"switch":              "DVS",
		"allow promiscuous":   "Promiscuous mode",
		"policy":              "Teaming policy",
		"active uplink":       "Active uplinks",
		"standby uplink":      "Standby uplinks",
		"notify switch":       "Notify switches",
		"rolling order":       "Failback",
		"in traffic shaping":  "Ingress shaping",
		"out traffic shaping": "Egress shaping",
	},
}

// sheetAliases accepts both the old vsfleet worksheet spelling and the name
// emitted by newer RVTools-compatible exports.
var sheetAliases = map[string]string{
	"vSC_VMK": sheetVSCVMK,
}

// Persisted collection kinds this profile records for every imported context.
const (
	kindVM        = "vm"
	kindHost      = "host"
	kindCluster   = "cluster"
	kindDatastore = "datastore"
	kindSnapshot  = "snapshot"
	kindDVSwitch  = "dvswitch"
	kindPool      = "resourcepool"
	kindNetwork   = "network"
)

// neverImported are the persisted kinds this profile version cannot answer
// for from any workbook, with the reason. They are recorded as an explicit,
// named gap on every imported context rather than left silently absent, so a
// diff or health rule that needs them reports "not evaluated" rather than
// "collection was not recorded" with no explanation.
var neverImported = []struct{ kind, reason string }{
	{kindPool, "the vRP worksheet carries pool configuration but only a VM count, never which VMs belong to a pool; importing it would make every pool look empty"},
	{kindNetwork, "RVTools carries no managed-object ID for a network, so importing one would rest on a display name alone"},
}

// criticalColumns are the columns whose absence would let a zero improve a
// verdict: a host, cluster or datastore whose capacity columns are missing
// would import as a zero-capacity object that looks less utilized. When any is
// absent the whole collection is recorded unavailable. Columns that only
// enrich an object (a vendor, a model) are not critical — their absence leaves
// the field absent and is reported as a missing column.
var criticalColumns = map[string][]string{
	sheetVHost:      {"# Cores", "# Memory", "Speed"},
	sheetVCluster:   {"NumCpuCores", "TotalCpu", "TotalMemory"},
	sheetVDatastore: {"Capacity MiB", "Free MiB"},
	sheetVSnapshot:  {"Date / time"},
	sheetDVSwitch:   {"Object ID", "DVS"},
	sheetDVPort:     {"Port group", "DVS", "Key"},
}

// snapshotTimeLayouts covers the "Date / time" formats this profile knows how
// to read: vsfleet's own RVTools-compatible export (yyyy/mm/dd hh:mm:ss) and
// the ISO and US locale layouts real RVTools builds are known to use. A row
// whose date matches none of these is skipped with a warning rather than
// imported with a guessed or zero time — a wrong snapshot age is worse than a
// missing snapshot.
var snapshotTimeLayouts = []string{
	time.RFC3339,
	"2006/01/02 15:04:05",
	"2006-01-02 15:04:05",
	"1/2/2006 15:04:05",
	"1/2/2006 3:04:05 PM",
}

const (
	// maxWorkbookBytes bounds the file this package will open at all, applied
	// before excelize ever sees the bytes.
	maxWorkbookBytes = 200 << 20
	// maxUnzipBytes and maxUnzipXMLBytes bound what excelize itself will
	// inflate the archive to, so a small file that is a zip bomb cannot
	// exhaust memory once past the size check above.
	maxUnzipBytes    = 512 << 20
	maxUnzipXMLBytes = 128 << 20
	// maxSheetRows bounds one worksheet's row count after inflation, a second
	// line of defense for a workbook that is small on disk and huge in rows.
	maxSheetRows = 2_000_000
)

// mib mirrors internal/report's own conversion constant. RVTools sizes disks
// and datastores in MiB; vsphere types are bytes.
const mib = float64(1 << 20)

// OpenFile opens a workbook for import with bounded memory use and no
// formula evaluation or external link resolution — excelize.GetRows never
// triggers either on its own, and these limits are this package's own
// defense against a hostile or corrupt file.
func OpenFile(path string) (*excelize.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxWorkbookBytes {
		return nil, fmt.Errorf("workbook is %d bytes, over the %d byte import limit", info.Size(), int64(maxWorkbookBytes))
	}
	f, err := excelize.OpenFile(path, excelize.Options{UnzipSizeLimit: maxUnzipBytes, UnzipXMLSizeLimit: maxUnzipXMLBytes})
	if err != nil {
		return nil, fmt.Errorf("open workbook: %w", err)
	}
	return f, nil
}

// FileSHA256 fingerprints the workbook's bytes. It is what repeat-import
// detection compares, so it hashes the file itself rather than anything parsed
// out of it: two workbooks with identical bytes are the same import.
func FileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxWorkbookBytes+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Options configures one import.
type Options struct {
	// SourceLabel is a human-readable name for the workbook — typically its
	// filename — recorded in the run's provenance. It is never parsed.
	SourceLabel string
	// SourceSHA256 is the workbook fingerprint from FileSHA256, recorded in the
	// run's provenance so a repeated import of the same file can be noticed.
	SourceSHA256 string
	// CapturedAt is the explicit --captured-at time, or the zero value to
	// fall back to the workbook's own metadata and then import time.
	CapturedAt time.Time
	// Timezone interprets local wall-clock values in RVTools worksheets such
	// as vMetaData and vSnapshot. Values with an explicit offset keep that
	// offset. A nil location means local vMetaData timestamps are unusable.
	Timezone *time.Location
	// ContextMap renames a reconstructed context key (see Report.Contexts)
	// to the desired final context name. A key not present here keeps its
	// reconstructed name.
	ContextMap map[string]string
	Label      string
	Note       string
}

// ContextSummary is one reconstructed vCenter context and what would be stored
// for it. A count is zero when that collection is unavailable, because
// nothing would be stored — the gap itself is listed in Report.Gaps.
type ContextSummary struct {
	// Key is the identity this workbook grouped rows by — the source
	// "vsfleet Context" value when present, otherwise the VI SDK Server
	// endpoint. It is what --context-map keys against.
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Endpoint        string   `json:"endpoint,omitempty"`
	VCenterID       string   `json:"vcenter_id,omitempty"`
	VMCount         int      `json:"vm_count"`
	HostCount       int      `json:"host_count"`
	ClusterCount    int      `json:"cluster_count"`
	DatastoreCount  int      `json:"datastore_count"`
	SnapshotCount   int      `json:"snapshot_count"`
	DVSwitchCount   int      `json:"dvswitch_count"`
	UnavailableKind []string `json:"unavailable,omitempty"`
}

// SheetReport is what the importer made of one recognized worksheet: which of
// its columns it read, which it did not, and which it looked for and did not
// find.
type SheetReport struct {
	Name              string   `json:"name"`
	Rows              int      `json:"rows"`
	RecognizedColumns []string `json:"recognized_columns"`
	IgnoredColumns    []string `json:"ignored_columns,omitempty"`
	MissingColumns    []string `json:"missing_columns,omitempty"`
}

// Gap is one piece of evidence the workbook could not answer for. It is the
// import-time counterpart of a failed live collection: recorded, named, and
// never read as "confirmed absent".
type Gap struct {
	Evidence string `json:"evidence"`
	Reason   string `json:"reason"`
	// Context is set when the gap applies to one reconstructed context rather
	// than to the whole workbook.
	Context string `json:"context,omitempty"`
}

// Ambiguity is an identity the workbook did not pin down to one object. The
// importer records it rather than guessing which object a row meant.
type Ambiguity struct {
	Sheet    string `json:"sheet"`
	Context  string `json:"context"`
	Identity string `json:"identity"`
	Detail   string `json:"detail"`
}

// Report describes what an import recognized and would do, in enough detail
// that a --dry-run needs nothing else to decide whether to proceed.
type Report struct {
	ProfileVersion   string           `json:"profile_version"`
	SourceLabel      string           `json:"source_label,omitempty"`
	SourceSHA256     string           `json:"source_sha256,omitempty"`
	SchemaVersion    string           `json:"inventory_schema_version"`
	RecognizedSheets []string         `json:"recognized_sheets"`
	IgnoredSheets    []string         `json:"ignored_sheets,omitempty"`
	Sheets           []SheetReport    `json:"sheets"`
	Contexts         []ContextSummary `json:"contexts"`
	CapturedAt       time.Time        `json:"captured_at"`
	CapturedAtSource string           `json:"captured_at_source"`
	Gaps             []Gap            `json:"gaps,omitempty"`
	Ambiguities      []Ambiguity      `json:"ambiguities,omitempty"`
	Warnings         []string         `json:"warnings,omitempty"`
	// SkippedKinds names every persisted collection that will be recorded
	// unavailable rather than answered, in any context.
	SkippedKinds []string `json:"skipped_kinds,omitempty"`
}

// importedContext accumulates one reconstructed vCenter's rows while parsing.
// It is a plain slice-of-objects builder, not assessment.ContextResult
// itself, because a dry run needs to summarize it without ever touching the
// store.
type importedContext struct {
	key       string
	name      string
	endpoint  string
	vcenterID string
	hostScope string

	vms        []*vsphere.VM
	hosts      []*vsphere.Host
	clusters   []*vsphere.Cluster
	datastores []*vsphere.Datastore
	dvswitches []*vsphere.DVSwitch

	// gaps records, per persisted kind, why this context cannot answer for it.
	gaps map[string]string
	// seen tracks the identities already claimed per kind, to catch a
	// duplicate that would otherwise silently overwrite its twin.
	seen map[string]map[string]bool
}

func (c *importedContext) setGap(kind, reason string) {
	if c.gaps == nil {
		c.gaps = make(map[string]string)
	}
	if _, exists := c.gaps[kind]; !exists {
		c.gaps[kind] = reason
	}
}

// claim reserves identity id for kind in this context, reporting false when it
// is empty or already taken.
func (c *importedContext) claim(kind, id string) bool {
	if id == "" {
		return false
	}
	if c.seen == nil {
		c.seen = make(map[string]map[string]bool)
	}
	if c.seen[kind] == nil {
		c.seen[kind] = make(map[string]bool)
	}
	if c.seen[kind][id] {
		return false
	}
	c.seen[kind][id] = true
	return true
}

func (c *importedContext) snapshotCount() int {
	n := 0
	for _, vm := range c.vms {
		n += len(vm.Snapshots)
	}
	return n
}

// Result is a fully parsed workbook, ready to report or to write. Parsing
// never touches the assessment store; only Write does, and only after every
// row that could be read has been read.
type Result struct {
	Report   Report
	contexts []*importedContext
	opts     Options
	schema   string
	// kindGaps are the workbook-wide reasons a kind cannot be answered for.
	kindGaps map[string]string
}

// gapFor returns why kind is unavailable in c, or "" when it can be answered.
func (r *Result) gapFor(c *importedContext, kind string) string {
	if reason, ok := r.kindGaps[kind]; ok {
		return reason
	}
	return c.gaps[kind]
}

// colTrack records, across every mapper that reads a worksheet, which of the
// worksheet's columns were read and which the mappers asked for and found
// absent. Recording it at the single cell() choke point keeps the report
// unable to drift from what the mappers actually do.
type colTrack struct {
	used    map[string]bool
	missing map[string]string
}

// sheetTable is one worksheet's header-indexed rows. Columns are looked up by
// header name rather than position, so a workbook with columns reordered, or
// with extra columns this adapter does not know about, still imports.
type sheetTable struct {
	name       string
	sourceName string
	headers    []string
	index      map[string]int
	rawIndex   map[string]int
	rows       [][]string
	track      *colTrack
	date1904   bool // workbook epoch for raw metadata date serials
}

func newSheetTable(name, sourceName string, rows [][]string) sheetTable {
	t := sheetTable{name: name, sourceName: sourceName, track: &colTrack{used: map[string]bool{}, missing: map[string]string{}}}
	if len(rows) == 0 {
		return t
	}
	t.index = make(map[string]int, len(rows[0]))
	t.rawIndex = make(map[string]int, len(rows[0]))
	for i, h := range rows[0] {
		rawKey := normalizeHeader(h)
		key := canonicalHeader(name, h)
		if key == "" {
			continue
		}
		if _, exists := t.rawIndex[rawKey]; !exists {
			t.rawIndex[rawKey] = i
			t.headers = append(t.headers, strings.TrimSpace(h))
		}
		// If both spellings occur, prefer the canonical header independent of
		// column order. That keeps a legacy field authoritative in mixed files.
		prior, exists := t.index[key]
		if !exists || normalizeHeader(rows[0][prior]) != key || rawKey == key {
			t.index[key] = i
		}
	}
	t.rows = rows[1:]
	return t
}

func normalizeHeader(h string) string { return strings.ToLower(strings.TrimSpace(h)) }

func canonicalHeader(sheet, header string) string {
	key := normalizeHeader(header)
	if alias, ok := headerAliases[sheet][key]; ok {
		return normalizeHeader(alias)
	}
	return key
}

// has reports whether the worksheet carries header, without counting it as
// read.
func (t sheetTable) has(header string) bool {
	_, ok := t.index[canonicalHeader(t.name, header)]
	return ok
}

func (t sheetTable) hasSourceHeader(header string) bool {
	_, ok := t.rawIndex[normalizeHeader(header)]
	return ok
}

func (t sheetTable) cell(row []string, header string) string {
	key := canonicalHeader(t.name, header)
	i, ok := t.index[key]
	if !ok {
		if t.track != nil {
			t.track.missing[key] = header
		}
		return ""
	}
	if t.track != nil {
		t.track.used[key] = true
	}
	if i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

// missingCritical lists the critical columns the worksheet lacks.
func (t sheetTable) missingCritical() []string {
	var out []string
	for _, h := range criticalColumns[t.name] {
		if !t.has(h) {
			out = append(out, h)
		}
	}
	return out
}

// optionalColumns are read when present but never worth reporting as missing:
// a workbook that lacks them is ordinary (vsfleet Context exists only in
// workbooks vsfleet itself exported; VM SMBIOS UUID only on vInfo).
var optionalColumns = map[string]bool{"vsfleet context": true, "vm smbios uuid": true}

// probe runs every mapper that reads this worksheet over an empty row, so the
// column report reflects what the importer reads rather than which columns
// happened to be exercised by the rows present. A worksheet with no data rows
// would otherwise report every column as ignored.
func (t sheetTable) probe() {
	if t.name != sheetVMetaData {
		rowContext(t, nil)
	}
	switch t.name {
	case sheetVInfo, sheetVCPU, sheetVMemory, sheetVDisk, sheetVNetwork, sheetVTools, sheetVPartition, sheetVSnapshot, sheetVCD, sheetVUSB:
		// Only per-VM worksheets identify a VM; host and infrastructure
		// worksheets identify their own objects by Object ID.
		vmKeys(t, nil)
	case sheetVSwitch, sheetVPort, sheetVHBA, sheetVNIC, sheetVSCVMK, sheetVMultiPath:
		t.cell(nil, "Object ID") // the host join column
		t.cell(nil, "Host")
	}
	scratchVM, scratchHost := &vsphere.VM{}, &vsphere.Host{}
	switch t.name {
	case sheetVInfo:
		vmFromInfoRow(t, nil, "")
	case sheetVCPU:
		t.cell(nil, "CPUs")
	case sheetVMemory:
		t.cell(nil, "Size MiB")
	case sheetVDisk:
		applyDiskRow(t, nil, scratchVM)
	case sheetVNetwork:
		applyNetworkRow(t, nil, scratchVM)
	case sheetVTools:
		applyToolsRow(t, nil, scratchVM)
	case sheetVPartition:
		applyPartitionRow(t, nil, scratchVM)
	case sheetVSnapshot:
		applySnapshotRow(t, nil, scratchVM, nil)
		t.cell(nil, "Date / time")
	case sheetDVPort:
		t.cell(nil, "DVS")
		t.cell(nil, "Datacenter")
		dvPortFromRow(t, nil, "")
	case sheetVMetaData:
		for _, header := range t.headers {
			if isMetadataTimeHeader(header) {
				t.cell(nil, header)
			}
		}
	case sheetVCD:
		applyCDRow(t, nil, scratchVM)
	case sheetVUSB:
		applyUSBRow(t, nil, scratchVM)
	case sheetVHost:
		hostFromRow(t, nil, "")
	case sheetVSwitch:
		applyVSwitchRow(t, nil, scratchHost)
	case sheetVPort:
		applyVPortRow(t, nil, scratchHost)
	case sheetVHBA:
		applyHbaRow(t, nil, scratchHost)
	case sheetVNIC:
		applyHostNICRow(t, nil, scratchHost)
	case sheetVSCVMK:
		applyVMKRow(t, nil, scratchHost)
	case sheetVMultiPath:
		applyMultipathRow(t, nil, scratchHost)
	case sheetVCluster:
		clusterFromRow(t, nil, "")
	case sheetVDatastore:
		datastoreFromRow(t, nil, "")
	case sheetDVSwitch:
		dvSwitchFromRow(t, nil, "")
	}
}

func (t sheetTable) report() SheetReport {
	t.probe()
	r := SheetReport{Name: t.sourceName, Rows: len(t.rows), RecognizedColumns: []string{}}
	for _, h := range t.headers {
		if t.track.used[canonicalHeader(t.name, h)] {
			r.RecognizedColumns = append(r.RecognizedColumns, h)
		} else {
			r.IgnoredColumns = append(r.IgnoredColumns, h)
		}
	}
	for key, h := range t.track.missing {
		if !optionalColumns[key] {
			r.MissingColumns = append(r.MissingColumns, h)
		}
	}
	sort.Strings(r.MissingColumns)
	return r
}

func canonicalSheetName(name string) (string, bool) {
	if alias, ok := sheetAliases[name]; ok {
		name = alias
	}
	switch name {
	case sheetVInfo, sheetVCPU, sheetVMemory, sheetVDisk, sheetVPartition, sheetVNetwork, sheetVTools,
		sheetVHost, sheetVHBA, sheetVNIC, sheetVSwitch, sheetVPort, sheetVSCVMK, sheetVMultiPath,
		sheetDVSwitch, sheetDVPort, sheetVCluster, sheetVDatastore, sheetVSnapshot, sheetVCD, sheetVUSB, sheetVMetaData:
		return name, true
	default:
		return "", false
	}
}

// parseSnapshotTime tries every layout this profile recognizes and reports
// whether one matched, so a caller can tell "no usable timestamp" apart from
// "this is genuinely the epoch".
func parseSnapshotTime(s string, timezone *time.Location) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range snapshotTimeLayouts {
		var t time.Time
		var err error
		if layout == time.RFC3339 {
			t, err = time.Parse(layout, s)
		} else if timezone != nil {
			t, err = time.ParseInLocation(layout, s, timezone)
		} else {
			t, err = time.Parse(layout, s)
		}
		if err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

func hostScopeKey(contextKey, vcenterID string) string {
	if strings.TrimSpace(vcenterID) != "" {
		return "vcenter:" + strings.TrimSpace(vcenterID)
	}
	return "context:" + contextKey
}

func filterHosts(hosts []*vsphere.Host, keep func(*vsphere.Host) bool) []*vsphere.Host {
	out := make([]*vsphere.Host, 0, len(hosts))
	for _, host := range hosts {
		if keep(host) {
			out = append(out, host)
		}
	}
	return out
}

func isBlankRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

// rowContext extracts the identity this row's vCenter is grouped and named
// by. "vsfleet Context" is preferred when present — it is the literal
// original context name for a workbook vsfleet itself exported, the
// strongest identity this profile can have. Otherwise the VI SDK Server
// endpoint is the grouping key real RVTools output carries. A row with
// neither has no usable provenance and is not silently merged into whatever
// context came before it.
func rowContext(t sheetTable, row []string) (key, name, endpoint, vcenterID string, ok bool) {
	endpoint = t.cell(row, "VI SDK Server")
	vcenterID = t.cell(row, "VI SDK UUID")
	if vsfleetContext := t.cell(row, "vsfleet Context"); vsfleetContext != "" {
		return vsfleetContext, vsfleetContext, endpoint, vcenterID, true
	}
	if endpoint != "" {
		return endpoint, endpoint, endpoint, vcenterID, true
	}
	return "", "", "", "", false
}

// vmKeys returns the identity keys a row can be matched by, strongest first:
// managed-object ID, then instance UUID, then BIOS UUID (vInfo only). A
// display name is a key only when the row carries nothing stronger at all,
// so a name never competes with, or joins two rows that disagree on, a real
// identity.
func vmKeys(t sheetTable, row []string) []string {
	var keys []string
	if id := t.cell(row, "VM ID"); id != "" {
		keys = append(keys, "id:"+id)
	}
	if uuid := t.cell(row, "VM UUID"); uuid != "" {
		keys = append(keys, "uuid:"+uuid)
	}
	if bios := t.cell(row, "VM SMBIOS UUID"); bios != "" {
		keys = append(keys, "bios:"+bios)
	}
	if len(keys) == 0 {
		if name := t.cell(row, "VM"); name != "" {
			keys = append(keys, "name:"+name)
		}
	}
	return keys
}

// vmRef is one VM a per-VM worksheet row can attach to. ambiguous is set when
// two vInfo rows in one context shared the key, so no row may attach by it.
type vmRef struct {
	vm        *vsphere.VM
	ambiguous bool
}

type ambiguityLog struct {
	seen map[string]bool
	out  []Ambiguity
}

func (l *ambiguityLog) add(a Ambiguity) {
	key := a.Sheet + "|" + a.Context + "|" + a.Identity
	if l.seen == nil {
		l.seen = make(map[string]bool)
	}
	if l.seen[key] {
		return
	}
	l.seen[key] = true
	l.out = append(l.out, a)
}

// Parse reads every recognized worksheet into memory and reconstructs
// contexts, VMs, hosts, clusters, datastores, snapshots and distributed
// switches from them. It never touches the assessment store: a caller decides
// separately, from the returned Report, whether to write the result or was
// only asked for --dry-run.
func Parse(f *excelize.File, opts Options) (*Result, error) {
	sheetNames := f.GetSheetList()
	tables := make(map[string]sheetTable, len(sheetNames))
	var recognized, ignored []string
	for _, name := range sheetNames {
		canonical, ok := canonicalSheetName(name)
		if !ok {
			ignored = append(ignored, name)
			continue
		}
		if _, exists := tables[canonical]; exists {
			return nil, fmt.Errorf("workbook contains more than one supported spelling of worksheet %q", canonical)
		}
		rows, err := readRows(f, name)
		if err != nil {
			return nil, err
		}
		table := newSheetTable(canonical, name, rows)
		if canonical == sheetVMetaData {
			props, err := f.GetWorkbookProps()
			if err != nil {
				return nil, fmt.Errorf("read workbook date system: %w", err)
			}
			table.date1904 = props.Date1904 != nil && *props.Date1904
		}
		tables[canonical] = table
		recognized = append(recognized, name)
	}
	sort.Strings(ignored)
	sort.Strings(recognized)

	vInfo, ok := tables[sheetVInfo]
	if !ok {
		return nil, fmt.Errorf("workbook has no %s worksheet; VM identity cannot be reconstructed without it", sheetVInfo)
	}

	var (
		warnings    []string
		gaps        []Gap
		ambiguities ambiguityLog
	)
	warn := func(format string, args ...any) { warnings = append(warnings, fmt.Sprintf(format, args...)) }

	byKey := make(map[string]*importedContext)
	var order []string
	contextFor := func(key, name, endpoint, vcenterID string) *importedContext {
		if c, exists := byKey[key]; exists {
			return c
		}
		final := name
		if mapped, remapped := opts.ContextMap[key]; remapped && strings.TrimSpace(mapped) != "" {
			final = strings.TrimSpace(mapped)
		}
		hostScope := hostScopeKey(key, vcenterID)
		// Live capture falls back to the endpoint when vCenter reports no
		// instance UUID, and diff/history ignore a context with no vCenter ID
		// at all, so an import does the same rather than be invisible to them.
		if vcenterID == "" {
			vcenterID = endpoint
			if vcenterID == "" {
				vcenterID = key
			}
			gaps = append(gaps, Gap{Evidence: "vcenter.id", Context: final, Reason: "the workbook carries no VI SDK UUID; the endpoint or context name stands in as the vCenter identity, which is not stable if that vCenter is re-addressed"})
		}
		c := &importedContext{key: key, name: final, endpoint: endpoint, vcenterID: vcenterID, hostScope: hostScope}
		byKey[key] = c
		order = append(order, key)
		return c
	}

	// vInfo: one VM per row, indexed under every identity it carries.
	vmIndex := make(map[string]*vmRef)
	for _, row := range vInfo.rows {
		if isBlankRow(row) {
			continue
		}
		key, name, endpoint, vcenterID, ok := rowContext(vInfo, row)
		if !ok {
			warn("%s: row for VM %q has neither a VI SDK Server nor a vsfleet Context column; skipped", sheetVInfo, vInfo.cell(row, "VM"))
			continue
		}
		keys := vmKeys(vInfo, row)
		if len(keys) == 0 {
			warn("%s: row has no VM ID, VM UUID or VM name; skipped", sheetVInfo)
			continue
		}
		ctx := contextFor(key, name, endpoint, vcenterID)
		vm := vmFromInfoRow(vInfo, row, ctx.name)
		ctx.vms = append(ctx.vms, vm)
		for _, k := range keys {
			full := key + "|" + k
			if existing, dup := vmIndex[full]; dup {
				existing.ambiguous = true
				ambiguities.add(Ambiguity{Sheet: sheetVInfo, Context: ctx.name, Identity: k, Detail: "more than one VM row carries this identity; rows that reference it are not attached to either"})
				continue
			}
			vmIndex[full] = &vmRef{vm: vm}
		}
	}

	// lookupVM finds the one VM a per-VM row refers to, or reports why not.
	lookupVM := func(sheet string, t sheetTable, row []string) (*vsphere.VM, bool) {
		key, _, _, _, ok := rowContext(t, row)
		if !ok {
			return nil, false
		}
		keys := vmKeys(t, row)
		if len(keys) == 0 {
			return nil, false
		}
		for _, k := range keys {
			ref, found := vmIndex[key+"|"+k]
			if !found {
				continue
			}
			if ref.ambiguous {
				warn("%s: row for VM %q references an ambiguous identity (%s); not attached", sheet, t.cell(row, "VM"), k)
				return nil, false
			}
			return ref.vm, true
		}
		warn("%s: row for VM %q references a VM not present in %s; skipped", sheet, t.cell(row, "VM"), sheetVInfo)
		return nil, false
	}
	attachVMRows := func(sheetName string, apply func(sheetTable, []string, *vsphere.VM)) {
		t, ok := tables[sheetName]
		if !ok {
			return
		}
		for _, row := range t.rows {
			if isBlankRow(row) {
				continue
			}
			if vm, found := lookupVM(sheetName, t, row); found {
				apply(t, row, vm)
			}
		}
	}

	attachVMRows(sheetVDisk, applyDiskRow)
	attachVMRows(sheetVNetwork, applyNetworkRow)
	attachVMRows(sheetVTools, applyToolsRow)
	attachVMRows(sheetVPartition, applyPartitionRow)
	attachVMRows(sheetVCD, applyCDRow)
	attachVMRows(sheetVUSB, applyUSBRow)
	attachVMRows(sheetVSnapshot, func(t sheetTable, row []string, vm *vsphere.VM) {
		if !applySnapshotRow(t, row, vm, opts.Timezone) {
			warn("%s: row for VM %q has an unparseable or missing Date / time; snapshot skipped", sheetVSnapshot, vm.Name)
		}
	})

	// vCPU and vMemory corroborate vInfo. When vInfo lacks a column they carry
	// it; when both carry it and disagree, vInfo wins and the disagreement is
	// reported rather than settled silently.
	infoCPU, infoMemory := vInfo.has("CPUs"), vInfo.has("Memory")
	cpuCovered, memoryCovered := infoCPU, infoMemory
	if t, ok := tables[sheetVCPU]; ok && t.has("CPUs") {
		cpuCovered = true
		attachVMRows(sheetVCPU, func(t sheetTable, row []string, vm *vsphere.VM) {
			cpus := int32(parseInt(t.cell(row, "CPUs")))
			switch {
			case !infoCPU:
				vm.CPU = cpus
			case vm.CPU != cpus:
				warn("%s: VM %q has %d CPUs but %s says %d; keeping %s", sheetVCPU, vm.Name, cpus, sheetVInfo, vm.CPU, sheetVInfo)
			}
		})
	}
	if t, ok := tables[sheetVMemory]; ok && t.has("Size MiB") {
		memoryCovered = true
		attachVMRows(sheetVMemory, func(t sheetTable, row []string, vm *vsphere.VM) {
			mb := parseInt(t.cell(row, "Size MiB"))
			switch {
			case !infoMemory:
				vm.MemoryMB = mb
			case vm.MemoryMB != mb:
				warn("%s: VM %q has %d MiB but %s says %d; keeping %s", sheetVMemory, vm.Name, mb, sheetVInfo, vm.MemoryMB, sheetVInfo)
			}
		})
	}
	if !cpuCovered {
		gaps = append(gaps, Gap{Evidence: "vm.cpu", Reason: "neither vInfo nor vCPU carries a CPUs column; VM CPU counts are unknown, not zero"})
	}
	if !memoryCovered {
		gaps = append(gaps, Gap{Evidence: "vm.memory", Reason: "neither vInfo nor vMemory carries a memory column; VM memory sizes are unknown, not zero"})
	}
	if !vInfo.has("In Use MiB") {
		gaps = append(gaps, Gap{Evidence: "vm.storage", Reason: "vInfo has no In Use MiB column; VM storage use is unknown, not zero"})
	}

	// Workbook-wide gaps: a worksheet that is absent, or present without the
	// columns that make its rows trustworthy, cannot answer for its kind.
	kindGaps := make(map[string]string)
	needSheet := func(kind, sheet string) bool {
		t, present := tables[sheet]
		if !present {
			kindGaps[kind] = fmt.Sprintf("workbook has no %s worksheet", sheet)
			return false
		}
		if missing := t.missingCritical(); len(missing) > 0 {
			kindGaps[kind] = fmt.Sprintf("%s is missing required column(s): %s", sheet, strings.Join(missing, ", "))
			return false
		}
		return true
	}
	needSheet(kindHost, sheetVHost)
	needSheet(kindCluster, sheetVCluster)
	needSheet(kindDatastore, sheetVDatastore)
	needSheet(kindSnapshot, sheetVSnapshot)
	if needSheet(kindDVSwitch, sheetDVSwitch) {
		needSheet(kindDVSwitch, sheetDVPort)
	}
	for _, skip := range neverImported {
		kindGaps[skip.kind] = skip.reason
	}

	// Hosts, clusters and datastores are one-row-per-object sheets that both
	// identify their context and are self-contained.
	hostIndex := make(map[string]*vsphere.Host)
	hostNameIndex := make(map[string][]*vsphere.Host)
	attachResourceRows(tables, sheetVHost, kindHost, &warnings, &ambiguities, contextFor, func(c *importedContext, t sheetTable, row []string) string {
		h := hostFromRow(t, row, c.name)
		c.hosts = append(c.hosts, &h)
		scope := c.hostScope
		if h.ID != "" {
			hostIndex[scope+"|"+h.ID] = &h
		}
		if h.Name != "" {
			key := scope + "|" + h.Name
			hostNameIndex[key] = append(hostNameIndex[key], &h)
		}
		return h.ID
	})
	attachResourceRows(tables, sheetVCluster, kindCluster, &warnings, &ambiguities, contextFor, func(c *importedContext, t sheetTable, row []string) string {
		v := clusterFromRow(t, row, c.name)
		c.clusters = append(c.clusters, &v)
		return v.ID
	})
	attachResourceRows(tables, sheetVDatastore, kindDatastore, &warnings, &ambiguities, contextFor, func(c *importedContext, t sheetTable, row []string) string {
		v := datastoreFromRow(t, row, c.name)
		c.datastores = append(c.datastores, &v)
		return v.ID
	})

	// Host sub-sheets prefer the host managed-object ID. RVTools omits that ID
	// from some sheets, so a row without it may join by host name only when one
	// vHost row in this workbook matches within the same vCenter UUID (and any
	// supplied datacenter/cluster further narrows the match).
	hostSub := func(sheet string, apply func(sheetTable, []string, *vsphere.Host)) {
		t, ok := tables[sheet]
		if !ok {
			gaps = append(gaps, Gap{Evidence: "host." + strings.ToLower(strings.TrimPrefix(sheet, "v")), Reason: fmt.Sprintf("workbook has no %s worksheet; host networking is unknown, not absent", sheet)})
			return
		}
		for _, row := range t.rows {
			if isBlankRow(row) {
				continue
			}
			ctxKey, _, _, vcenterID, ok := rowContext(t, row)
			id := t.cell(row, "Object ID")
			if !ok {
				warn("%s: row has no vCenter context; skipped", sheet)
				continue
			}
			scope := hostScopeKey(ctxKey, vcenterID)
			host, found := hostIndex[scope+"|"+id]
			if id == "" {
				name := t.cell(row, "Host")
				matches := hostNameIndex[scope+"|"+name]
				if datacenter := t.cell(row, "Datacenter"); datacenter != "" {
					matches = filterHosts(matches, func(h *vsphere.Host) bool { return h.Datacenter == datacenter })
				}
				if cluster := t.cell(row, "Cluster"); cluster != "" {
					matches = filterHosts(matches, func(h *vsphere.Host) bool { return h.Cluster == cluster })
				}
				switch len(matches) {
				case 1:
					host, found = matches[0], true
				case 0:
					warn("%s: row names host %q, but no matching host is in this workbook context; skipped", sheet, name)
					continue
				default:
					ambiguities.add(Ambiguity{Sheet: sheet, Context: ctxKey, Identity: name, Detail: "more than one vHost row matches this host name and location; row was not attached"})
					warn("%s: row names host %q, which is ambiguous in this workbook context; skipped", sheet, name)
					continue
				}
			}
			if !found {
				warn("%s: row references host Object ID %q, which is not in %s for this vCenter; skipped", sheet, id, sheetVHost)
				continue
			}
			apply(t, row, host)
		}
	}
	for _, sheet := range []string{sheetVHBA, sheetVNIC, sheetVSCVMK, sheetVMultiPath} {
		var apply func(sheetTable, []string, *vsphere.Host)
		switch sheet {
		case sheetVHBA:
			apply = applyHbaRow
		case sheetVNIC:
			apply = applyHostNICRow
		case sheetVSCVMK:
			apply = applyVMKRow
		case sheetVMultiPath:
			apply = applyMultipathRow
		}
		hostSub(sheet, apply)
	}
	hostSub(sheetVSwitch, applyVSwitchRow)
	hostSub(sheetVPort, applyVPortRow)

	// Distributed switches. dvPort names its switch by display name, so the
	// join is scoped to context and datacenter and refused when it is not
	// unique — a distributed port group is never attached to a guess. Rows are
	// read even when the kind is already unavailable so the dry-run column
	// report is complete; they are simply never stored.
	if _, present := tables[sheetDVSwitch]; present {
		attachResourceRows(tables, sheetDVSwitch, kindDVSwitch, &warnings, &ambiguities, contextFor, func(c *importedContext, t sheetTable, row []string) string {
			v := dvSwitchFromRow(t, row, c.name)
			c.dvswitches = append(c.dvswitches, &v)
			return v.ID
		})
		if t, ok := tables[sheetDVPort]; ok {
			for _, row := range t.rows {
				if isBlankRow(row) {
					continue
				}
				ctxKey, _, _, _, ok := rowContext(t, row)
				if !ok {
					warn("%s: row has neither a VI SDK Server nor a vsfleet Context column; skipped", sheetDVPort)
					continue
				}
				c := byKey[ctxKey]
				if c == nil {
					warn("%s: row belongs to a context with no %s rows; skipped", sheetDVPort, sheetDVSwitch)
					continue
				}
				var match []*vsphere.DVSwitch
				for _, sw := range c.dvswitches {
					if sw.Name == t.cell(row, "DVS") && sw.Datacenter == t.cell(row, "Datacenter") {
						match = append(match, sw)
					}
				}
				switch len(match) {
				case 1:
					match[0].PortGroups = append(match[0].PortGroups, dvPortFromRow(t, row, match[0].Name))
				case 0:
					warn("%s: port group %q names switch %q, which is not in %s; skipped", sheetDVPort, t.cell(row, "Port group"), t.cell(row, "DVS"), sheetDVSwitch)
					c.setGap(kindDVSwitch, fmt.Sprintf("port group %q could not be attached to any distributed switch", t.cell(row, "Port group")))
				default:
					ambiguities.add(Ambiguity{Sheet: sheetDVPort, Context: c.name, Identity: t.cell(row, "DVS"), Detail: "more than one distributed switch shares this name and datacenter; port groups are not attached to either"})
					c.setGap(kindDVSwitch, fmt.Sprintf("distributed switch name %q is ambiguous", t.cell(row, "DVS")))
				}
			}
		}
	}

	if len(order) == 0 {
		return nil, fmt.Errorf("no vCenter/context identity could be reconstructed from this workbook")
	}
	sort.Strings(order)

	metadata, hasMetadata := tables[sheetVMetaData]
	var metadataPtr *sheetTable
	if hasMetadata {
		metadataPtr = &metadata
	}
	capturedAt, capturedAtSource, capturedAtWarning := resolveCapturedAt(f, opts.CapturedAt, opts.Timezone, metadataPtr)
	if capturedAtWarning != "" {
		warn("%s", capturedAtWarning)
	}
	schema := schemaVersionFor(tables)

	contexts := make([]*importedContext, 0, len(order))
	summaries := make([]ContextSummary, 0, len(order))
	res := &Result{contexts: contexts, opts: opts, schema: schema, kindGaps: kindGaps}
	for _, key := range order {
		c := byKey[key]
		contexts = append(contexts, c)
		summary := ContextSummary{Key: c.key, Name: c.name, Endpoint: c.endpoint, VCenterID: c.vcenterID, VMCount: len(c.vms)}
		count := func(kind string, n int) int {
			if res.gapFor(c, kind) != "" {
				return 0
			}
			return n
		}
		summary.HostCount = count(kindHost, len(c.hosts))
		summary.ClusterCount = count(kindCluster, len(c.clusters))
		summary.DatastoreCount = count(kindDatastore, len(c.datastores))
		summary.SnapshotCount = count(kindSnapshot, c.snapshotCount())
		summary.DVSwitchCount = count(kindDVSwitch, len(c.dvswitches))
		for _, kind := range persistedImportKinds {
			if reason := res.gapFor(c, kind); reason != "" {
				summary.UnavailableKind = append(summary.UnavailableKind, kind)
				if _, workbookWide := kindGaps[kind]; !workbookWide {
					gaps = append(gaps, Gap{Evidence: kind, Reason: reason, Context: c.name})
				}
			}
		}
		summaries = append(summaries, summary)
	}
	res.contexts = contexts

	skipped := make([]string, 0, len(kindGaps))
	for kind, reason := range kindGaps {
		skipped = append(skipped, kind)
		gaps = append(gaps, Gap{Evidence: kind, Reason: reason})
	}
	for _, c := range contexts {
		for kind := range c.gaps {
			if _, workbookWide := kindGaps[kind]; !workbookWide && !hasString(skipped, kind) {
				skipped = append(skipped, kind)
			}
		}
	}
	sort.Strings(skipped)
	sort.SliceStable(gaps, func(i, j int) bool {
		if gaps[i].Evidence != gaps[j].Evidence {
			return gaps[i].Evidence < gaps[j].Evidence
		}
		return gaps[i].Context < gaps[j].Context
	})

	sheetReports := make([]SheetReport, 0, len(recognized))
	for _, name := range recognized {
		canonical, _ := canonicalSheetName(name)
		sheetReports = append(sheetReports, tables[canonical].report())
	}

	res.Report = Report{
		ProfileVersion:   ProfileVersion,
		SourceLabel:      opts.SourceLabel,
		SourceSHA256:     opts.SourceSHA256,
		SchemaVersion:    schema,
		RecognizedSheets: recognized,
		IgnoredSheets:    ignored,
		Sheets:           sheetReports,
		Contexts:         summaries,
		CapturedAt:       capturedAt,
		CapturedAtSource: capturedAtSource,
		Gaps:             gaps,
		Ambiguities:      ambiguities.out,
		Warnings:         warnings,
		SkippedKinds:     skipped,
	}
	return res, nil
}

// persistedImportKinds is every kind an imported context records, in a stable
// order.
var persistedImportKinds = []string{kindVM, kindHost, kindCluster, kindDatastore, kindSnapshot, kindDVSwitch, kindPool, kindNetwork}

func hasString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// readRows reads one worksheet, refusing one large enough to be an allocation
// attack rather than an estate.
func readRows(f *excelize.File, name string) ([][]string, error) {
	// Metadata date formats can omit seconds or shorten the year. Read the
	// stored timestamp instead of its display text; other sheets retain their
	// existing formatted-value mappings, including snapshot dates.
	rows, err := f.GetRows(name, excelize.Options{RawCellValue: name == sheetVMetaData})
	if err != nil {
		return nil, fmt.Errorf("read worksheet %q: %w", name, err)
	}
	if len(rows) > maxSheetRows {
		return nil, fmt.Errorf("worksheet %q has %d rows, over the %d row import limit", name, len(rows), maxSheetRows)
	}
	return rows, nil
}

// schemaVersionFor is the inventory schema level this workbook earns. The
// schema number is linear but a workbook supplies a sparse subset, so a level
// is claimed only when the evidence that level added is really present: tools
// state needs vTools (3), guest partitions need vPartition (4), and the disks
// behind them need the Disk Key join column (5). Claiming a level the workbook
// cannot back would tell schema-gated rules that evidence was collected and
// came back empty.
func schemaVersionFor(tables map[string]sheetTable) string {
	schema := baseSchemaVersion
	tools, ok := tables[sheetVTools]
	if !ok || !tools.has("Tools") {
		return schema
	}
	schema = "3"
	parts, ok := tables[sheetVPartition]
	if !ok || !parts.has("Filesystem") {
		return schema
	}
	schema = "4"
	if parts.has("Disk Key") {
		schema = "5"
	}
	return schema
}

// attachResourceRows is the shared walk for the one-row-per-object sheets:
// each row both identifies its context and is entirely self-contained, unlike
// vDisk/vNetwork which attach to a VM identified elsewhere. apply appends the
// object and returns the identity it claims. A row with no identity, or one
// that repeats an identity, cannot be stored without overwriting or guessing,
// so it makes the whole kind unavailable for that context.
func attachResourceRows(tables map[string]sheetTable, sheetName string, kind string, warnings *[]string, ambiguities *ambiguityLog, contextFor func(key, name, endpoint, vcenterID string) *importedContext, apply func(*importedContext, sheetTable, []string) string) {
	t, ok := tables[sheetName]
	if !ok {
		return
	}
	for _, row := range t.rows {
		if isBlankRow(row) {
			continue
		}
		key, name, endpoint, vcenterID, ok := rowContext(t, row)
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("%s: row has neither a VI SDK Server nor a vsfleet Context column; skipped", sheetName))
			continue
		}
		ctx := contextFor(key, name, endpoint, vcenterID)
		id := apply(ctx, t, row)
		if id == "" {
			*warnings = append(*warnings, fmt.Sprintf("%s: row has no Object ID; %s evidence for %s is unavailable rather than guessed", sheetName, kind, ctx.name))
			ctx.setGap(kind, fmt.Sprintf("%s has a row without an Object ID", sheetName))
			continue
		}
		if !ctx.claim(kind, id) {
			ambiguities.add(Ambiguity{Sheet: sheetName, Context: ctx.name, Identity: id, Detail: "more than one row carries this Object ID"})
			ctx.setGap(kind, fmt.Sprintf("%s repeats Object ID %q", sheetName, id))
		}
	}
}

func resolveCapturedAt(f *excelize.File, explicit time.Time, timezone *time.Location, metadata *sheetTable) (time.Time, string, string) {
	if !explicit.IsZero() {
		return explicit.UTC(), "explicit --captured-at", ""
	}
	if metadata != nil {
		if meta, ok := workbookMetadataCaptureTime(*metadata, timezone); ok {
			return meta.UTC(), "vMetaData worksheet", ""
		}
	}
	warning := ""
	if metadata != nil && metadataHasCaptureTime(*metadata) && timezone == nil {
		warning = "vMetaData contains a timezone-free capture time; pass --timezone with the collector's IANA zone to use it"
	}
	if props, err := f.GetDocProps(); err == nil && props != nil {
		for _, value := range []string{props.Created, props.Modified} {
			if value == "" {
				continue
			}
			if t, err := time.Parse(time.RFC3339, value); err == nil {
				return t.UTC(), "workbook document properties", warning
			}
		}
	}
	return time.Now().UTC(), "import time (the workbook carried no usable captured-at metadata)", warning
}

var metadataTimeHeaders = map[string]bool{
	"creation date/time":     true,
	"creation date time":     true,
	"creation date":          true,
	"creation time":          true,
	"creation datetime":      true,
	"xlsx creation datetime": true,
	"created":                true,
	"created at":             true,
	"date created":           true,
	"date / time":            true,
	"date/time":              true,
	"date time":              true,
	"export date/time":       true,
	"export time":            true,
	"timestamp":              true,
	"time stamp":             true,
}

func isMetadataTimeHeader(value string) bool {
	header := normalizeHeader(value)
	if metadataTimeHeaders[header] {
		return true
	}
	hasTime := strings.Contains(header, "time") || strings.Contains(header, "timestamp") || strings.Contains(header, "date")
	return hasTime && (strings.Contains(header, "creat") || strings.Contains(header, "export") || strings.Contains(header, "date time"))
}

func metadataHasCaptureTime(t sheetTable) bool {
	for _, header := range t.headers {
		if isMetadataTimeHeader(header) {
			return true
		}
	}
	for _, row := range t.rows {
		if len(row) == 0 {
			continue
		}
		if isMetadataTimeHeader(row[0]) {
			return true
		}
	}
	return false
}

func workbookMetadataCaptureTime(t sheetTable, timezone *time.Location) (time.Time, bool) {
	// RVTools metadata has appeared both as a normal header/data table and as
	// key/value rows, so accept either shape while keeping the timestamp label
	// explicit rather than guessing from an arbitrary cell.
	for _, row := range t.rows {
		for _, header := range t.headers {
			if !isMetadataTimeHeader(header) {
				continue
			}
			if stamp, ok := parseMetadataTime(t.cell(row, header), timezone, t.date1904); ok {
				return stamp, true
			}
		}
		if len(row) > 1 && isMetadataTimeHeader(row[0]) {
			if len(t.headers) > 0 {
				t.cell(row, t.headers[0])
			}
			if len(t.headers) > 1 {
				t.cell(row, t.headers[1])
			}
			if stamp, ok := parseMetadataTime(row[1], timezone, t.date1904); ok {
				return stamp, true
			}
		}
	}
	return time.Time{}, false
}

func parseMetadataTime(value string, timezone *time.Location, date1904 bool) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if stamp, err := time.Parse(time.RFC3339, value); err == nil {
		return stamp, true
	}
	if timezone == nil {
		return time.Time{}, false
	}
	// ISO date cells may store a wall-clock timestamp without a UTC offset.
	if stamp, err := time.ParseInLocation("2006-01-02T15:04:05", value, timezone); err == nil {
		return stamp, true
	}
	if stamp, ok := parseSnapshotTime(value, timezone); ok {
		return stamp, true
	}
	if serial, err := strconv.ParseFloat(value, 64); err == nil && serial >= 1 {
		stamp, err := excelize.ExcelDateToTime(serial, date1904)
		if err == nil {
			return time.Date(stamp.Year(), stamp.Month(), stamp.Day(), stamp.Hour(), stamp.Minute(), stamp.Second(), stamp.Nanosecond(), timezone), true
		}
	}
	return time.Time{}, false
}

func vmFromInfoRow(t sheetTable, row []string, contextName string) *vsphere.VM {
	return &vsphere.VM{
		Location:     vsphere.Location{Context: contextName, Datacenter: t.cell(row, "Datacenter")},
		ID:           t.cell(row, "VM ID"),
		InstanceUUID: t.cell(row, "VM UUID"),
		BIOSUUID:     t.cell(row, "VM SMBIOS UUID"),
		Name:         t.cell(row, "VM"),
		PowerState:   t.cell(row, "Powerstate"),
		IsTemplate:   parseBool(t.cell(row, "Template")),
		GuestState:   t.cell(row, "Guest state"),
		CPU:          int32(parseInt(t.cell(row, "CPUs"))),
		MemoryMB:     parseInt(t.cell(row, "Memory")),
		IPAddress:    t.cell(row, "Primary IP Address"),
		Host:         t.cell(row, "Host"),
		Cluster:      t.cell(row, "Cluster"),
		Folder:       t.cell(row, "Folder"),
		StorageGB:    parseFloat(t.cell(row, "In Use MiB")) / 1024,
		Annotation:   t.cell(row, "Annotation"),
		GuestOS:      t.cell(row, "OS according to the configuration file"),
		// ConfigurationAvailable is deliberately left false: RVTools exposes
		// only the subset of configuration mapped below, never the deep
		// hardware configuration that flag promises to callers.
	}
}

func applyDiskRow(t sheetTable, row []string, vm *vsphere.VM) {
	vm.Disks = append(vm.Disks, vsphere.VMDisk{
		Label:                t.cell(row, "Disk"),
		Key:                  int32(parseInt(t.cell(row, "Disk Key"))),
		UUID:                 t.cell(row, "Disk UUID"),
		CapacityBytes:        int64(parseFloat(t.cell(row, "Capacity MiB")) * mib),
		Raw:                  parseBool(t.cell(row, "Raw")),
		DiskMode:             t.cell(row, "Disk Mode"),
		Sharing:              t.cell(row, "Sharing mode"),
		ThinProvisioned:      parseOptBool(t.cell(row, "Thin")),
		EagerlyScrub:         parseOptBool(t.cell(row, "Eagerly Scrub")),
		Split:                parseOptBool(t.cell(row, "Split")),
		WriteThrough:         parseOptBool(t.cell(row, "Write Through")),
		SharesLevel:          t.cell(row, "Level"),
		Shares:               parseOptInt32(t.cell(row, "Shares")),
		Reservation:          parseOptInt32(t.cell(row, "Reservation")),
		Limit:                parseOptInt64(t.cell(row, "Limit")),
		Controller:           t.cell(row, "Controller"),
		ControllerLabel:      t.cell(row, "SCSI label"),
		UnitNumber:           parseOptInt32(t.cell(row, "Unit number")),
		SharedBus:            t.cell(row, "SharedBus"),
		BackingPath:          t.cell(row, "Path"),
		RawLUNID:             t.cell(row, "Raw LUN ID"),
		RawCompatibilityMode: t.cell(row, "Raw Compatibility Mode"),
	})
}

func applyNetworkRow(t sheetTable, row []string, vm *vsphere.VM) {
	vm.NICs = append(vm.NICs, vsphere.VMNIC{
		Label:           t.cell(row, "NIC label"),
		Adapter:         t.cell(row, "Adapter"),
		Network:         t.cell(row, "Network"),
		Connected:       parseOptBool(t.cell(row, "Connected")),
		StartsConnected: parseOptBool(t.cell(row, "Starts Connected")),
		MACAddress:      t.cell(row, "Mac Address"),
		MACAddressType:  t.cell(row, "Mac Address type"),
		IPv4:            splitList(t.cell(row, "IPv4 Address")),
		IPv6:            splitList(t.cell(row, "IPv6 Address")),
		UPTCompatible:   parseOptBool(t.cell(row, "Direct Path IO")),
	})
}

func applyCDRow(t sheetTable, row []string, vm *vsphere.VM) {
	vm.CDROMs = append(vm.CDROMs, vsphere.VMCDROM{
		Label:            t.cell(row, "Device"),
		Key:              int32(parseInt(t.cell(row, "Device Key"))),
		Connected:        parseOptBool(t.cell(row, "Connected")),
		StartsConnected:  parseOptBool(t.cell(row, "Starts Connected")),
		BackingType:      t.cell(row, "Backing type"),
		BackingPath:      t.cell(row, "Backing path"),
		BackingDevice:    t.cell(row, "Backing device"),
		BackingHost:      t.cell(row, "Backing host"),
		BackingDatastore: t.cell(row, "Backing datastore ID"),
		BackingObjectID:  t.cell(row, "Backing object ID"),
		UseAutoDetect:    parseOptBool(t.cell(row, "Use auto detect")),
		Controller:       t.cell(row, "Controller"),
		ControllerLabel:  t.cell(row, "Controller label"),
		UnitNumber:       parseOptInt32(t.cell(row, "Unit number")),
	})
}

func applyUSBRow(t sheetTable, row []string, vm *vsphere.VM) {
	vm.USBs = append(vm.USBs, vsphere.VMUSB{
		Label:            t.cell(row, "Device"),
		Key:              int32(parseInt(t.cell(row, "Device Key"))),
		Connected:        parseOptBool(t.cell(row, "Connected")),
		Vendor:           int32(parseInt(t.cell(row, "Vendor ID"))),
		Product:          int32(parseInt(t.cell(row, "Product ID"))),
		Family:           splitList(t.cell(row, "Family")),
		Speed:            splitList(t.cell(row, "Speed")),
		BackingType:      t.cell(row, "Backing type"),
		BackingPath:      t.cell(row, "Backing path"),
		BackingDevice:    t.cell(row, "Backing device"),
		BackingHost:      t.cell(row, "Backing host"),
		BackingDatastore: t.cell(row, "Backing datastore ID"),
		BackingObjectID:  t.cell(row, "Backing object ID"),
		UseAutoDetect:    parseOptBool(t.cell(row, "Use auto detect")),
		Controller:       t.cell(row, "Controller"),
		ControllerLabel:  t.cell(row, "Controller label"),
		UnitNumber:       parseOptInt32(t.cell(row, "Unit number")),
	})
}

// applySnapshotRow appends the row's snapshot to vm, reporting false when its
// date is unusable — a snapshot with a guessed age is worse than none.
func applySnapshotRow(t sheetTable, row []string, vm *vsphere.VM, timezone *time.Location) bool {
	createTime, ok := parseSnapshotTime(t.cell(row, "Date / time"), timezone)
	name := t.cell(row, "Name")
	description, state, quiesced := t.cell(row, "Description"), t.cell(row, "State"), parseBool(t.cell(row, "Quiesced"))
	if !ok {
		return false
	}
	vm.Snapshots = append(vm.Snapshots, vsphere.VMSnapshot{
		// RVTools' flat export carries no snapshot moref or parent/tree
		// structure, so ParentID and Current are left at their zero value
		// (unknown, not "not a parent" / "not current") and ID is synthesized
		// from name and creation time — the strongest identity this profile
		// has, not a real managed-object ID.
		ID:          fmt.Sprintf("import:%s@%d", name, createTime.UnixNano()),
		Name:        name,
		Description: description,
		CreateTime:  createTime,
		PowerState:  state,
		Quiesced:    quiesced,
	})
	return true
}

func applyToolsRow(t sheetTable, row []string, vm *vsphere.VM) {
	vm.ToolsState = t.cell(row, "Tools")
	vm.ToolsVersion = t.cell(row, "Tools Version")
	vm.ToolsVersionStatus = t.cell(row, "Tools Version Status")
}

func applyPartitionRow(t sheetTable, row []string, vm *vsphere.VM) {
	var keys []int32
	for _, part := range splitList(t.cell(row, "Disk Key")) {
		if v := parseOptInt32(part); v != nil {
			keys = append(keys, *v)
		}
	}
	vm.Partitions = append(vm.Partitions, vsphere.VMPartition{
		Path:           t.cell(row, "Disk"),
		DiskKeys:       keys,
		CapacityBytes:  int64(parseFloat(t.cell(row, "Capacity MiB")) * mib),
		FreeBytes:      int64(parseFloat(t.cell(row, "Free MiB")) * mib),
		FilesystemType: t.cell(row, "Filesystem"),
	})
}

func hostFromRow(t sheetTable, row []string, contextName string) vsphere.Host {
	return vsphere.Host{
		Location:      vsphere.Location{Context: contextName, Datacenter: t.cell(row, "Datacenter")},
		ID:            t.cell(row, "Object ID"),
		Name:          t.cell(row, "Host"),
		Cluster:       t.cell(row, "Cluster"),
		InMaintenance: parseBool(t.cell(row, "in Maintenance Mode")),
		CPUMHz:        int32(parseInt(t.cell(row, "Speed"))),
		CPUCores:      int32(parseInt(t.cell(row, "# Cores"))),
		MemoryMB:      parseInt(t.cell(row, "# Memory")),
		VMCount:       int(parseInt(t.cell(row, "# VMs total"))),
		Version:       t.cell(row, "ESX Version"),
		Vendor:        t.cell(row, "Vendor"),
		Model:         t.cell(row, "Model"),
	}
}

func applyVSwitchRow(t sheetTable, row []string, host *vsphere.Host) {
	host.VSwitches = append(host.VSwitches, vsphere.HostVSwitch{
		Name:            t.cell(row, "Switch"),
		NumPorts:        int32(parseInt(t.cell(row, "# Ports"))),
		FreePorts:       int32(parseInt(t.cell(row, "Free ports"))),
		MTU:             int32(parseInt(t.cell(row, "MTU"))),
		Uplinks:         splitList(t.cell(row, "Uplinks")),
		Promiscuous:     parseOptBool(t.cell(row, "Promiscuous mode")),
		MACChanges:      parseOptBool(t.cell(row, "MAC changes")),
		ForgedTransmits: parseOptBool(t.cell(row, "Forged transmits")),
		TrafficShaping:  parseOptBool(t.cell(row, "Traffic shaping")),
	})
}

func applyVPortRow(t sheetTable, row []string, host *vsphere.Host) {
	host.PortGroups = append(host.PortGroups, vsphere.HostPortGroup{
		Name:            t.cell(row, "Port group"),
		Switch:          t.cell(row, "Switch"),
		VLAN:            int32(parseInt(t.cell(row, "VLAN"))),
		Promiscuous:     parseOptBool(t.cell(row, "Promiscuous mode")),
		MACChanges:      parseOptBool(t.cell(row, "MAC changes")),
		ForgedTransmits: parseOptBool(t.cell(row, "Forged transmits")),
	})
}

func applyHbaRow(t sheetTable, row []string, host *vsphere.Host) {
	host.HBAs = append(host.HBAs, vsphere.HostHBA{
		Device:          t.cell(row, "Device"),
		Bus:             int32(parseInt(t.cell(row, "Bus"))),
		Status:          t.cell(row, "Status"),
		Model:           t.cell(row, "Model"),
		Driver:          t.cell(row, "Driver"),
		PCI:             t.cell(row, "PCI"),
		StorageProtocol: t.cell(row, "Storage protocol"),
		WWNN:            parseWWN(t.cell(row, "WWNN")),
		WWPN:            parseWWN(t.cell(row, "WWPN")),
		IScsiName:       t.cell(row, "iSCSI name"),
		IScsiAlias:      t.cell(row, "iSCSI alias"),
		Type:            t.cell(row, "Type"),
	})
}

func applyHostNICRow(t sheetTable, row []string, host *vsphere.Host) {
	host.NICs = append(host.NICs, vsphere.HostNIC{
		Device:      t.cell(row, "Device"),
		PCI:         t.cell(row, "PCI"),
		Driver:      t.cell(row, "Driver"),
		MAC:         t.cell(row, "Mac Address"),
		LinkSpeedMB: parseOptInt32(t.cell(row, "Link speed Mb")),
		Duplex:      parseOptBool(t.cell(row, "Duplex")),
		WakeOnLAN:   parseBool(t.cell(row, "Wake on LAN")),
		Switch:      t.cell(row, "Switch"),
	})
}

func applyVMKRow(t sheetTable, row []string, host *vsphere.Host) {
	host.VMKs = append(host.VMKs, vsphere.HostVMKernel{
		Device:         t.cell(row, "Device"),
		PortGroup:      t.cell(row, "Port group"),
		MAC:            t.cell(row, "Mac Address"),
		MTU:            int32(parseInt(t.cell(row, "MTU"))),
		TSO:            parseOptBool(t.cell(row, "TSO")),
		Netstack:       t.cell(row, "Netstack"),
		DHCP:           parseOptBool(t.cell(row, "DHCP")),
		IP:             t.cell(row, "IP Address"),
		SubnetMask:     t.cell(row, "Subnet mask"),
		ServiceConsole: parseBool(t.cell(row, "Service console")),
	})
}

func applyMultipathRow(t sheetTable, row []string, host *vsphere.Host) {
	host.Multipaths = append(host.Multipaths, vsphere.HostMultipath{
		LUN:          t.cell(row, "LUN"),
		DevicePath:   t.cell(row, "Device path"),
		Policy:       t.cell(row, "Policy"),
		LocalDisk:    parseOptBool(t.cell(row, "Local disk")),
		PathCount:    int(parseInt(t.cell(row, "Path count"))),
		Active:       int(parseInt(t.cell(row, "Active paths"))),
		Standby:      int(parseInt(t.cell(row, "Standby paths"))),
		Dead:         int(parseInt(t.cell(row, "Dead paths"))),
		Disabled:     int(parseInt(t.cell(row, "Disabled paths"))),
		WorkingPaths: int(parseInt(t.cell(row, "Working paths"))),
	})
}

func parseWWN(value string) *int64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if n, err := strconv.ParseInt(value, 0, 64); err == nil {
		return &n
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil {
		return &n
	}
	if n, err := strconv.ParseUint(value, 16, 64); err == nil && n <= uint64(^uint64(0)>>1) {
		converted := int64(n)
		return &converted
	}
	return nil
}

func clusterFromRow(t sheetTable, row []string, contextName string) vsphere.Cluster {
	return vsphere.Cluster{
		Location:      vsphere.Location{Context: contextName, Datacenter: t.cell(row, "Datacenter")},
		ID:            t.cell(row, "Object ID"),
		Name:          t.cell(row, "Name"),
		Hosts:         int(parseInt(t.cell(row, "NumHosts"))),
		EffectiveHost: int(parseInt(t.cell(row, "NumEffectiveHosts"))),
		CPUCores:      int32(parseInt(t.cell(row, "NumCpuCores"))),
		TotalCPUMHz:   parseInt(t.cell(row, "TotalCpu")),
		TotalMemoryMB: parseInt(t.cell(row, "TotalMemory")),
		HAEnabled:     parseBool(t.cell(row, "HA enabled")),
		DRSEnabled:    parseBool(t.cell(row, "DRS enabled")),
	}
}

func datastoreFromRow(t sheetTable, row []string, contextName string) vsphere.Datastore {
	return vsphere.Datastore{
		Location:      vsphere.Location{Context: contextName, Datacenter: t.cell(row, "Datacenter")},
		ID:            t.cell(row, "Object ID"),
		Name:          t.cell(row, "Name"),
		Type:          t.cell(row, "Type"),
		CapacityBytes: int64(parseFloat(t.cell(row, "Capacity MiB")) * mib),
		FreeBytes:     int64(parseFloat(t.cell(row, "Free MiB")) * mib),
		Accessible:    parseBool(t.cell(row, "Accessible")),
		Maintenance:   t.cell(row, "Maintenance mode"),
	}
}

func dvSwitchFromRow(t sheetTable, row []string, contextName string) vsphere.DVSwitch {
	return vsphere.DVSwitch{
		Location:               vsphere.Location{Context: contextName, Datacenter: t.cell(row, "Datacenter")},
		ID:                     t.cell(row, "Object ID"),
		Name:                   t.cell(row, "DVS"),
		UUID:                   t.cell(row, "UUID"),
		Vendor:                 t.cell(row, "Vendor"),
		Version:                t.cell(row, "Version"),
		Description:            t.cell(row, "Description"),
		Contact:                t.cell(row, "Contact"),
		ContactDetail:          t.cell(row, "Contact detail"),
		NumPorts:               int32(parseInt(t.cell(row, "# Ports"))),
		MaxPorts:               int32(parseInt(t.cell(row, "# Max ports"))),
		MaxMTU:                 int32(parseInt(t.cell(row, "MTU"))),
		Hosts:                  splitList(t.cell(row, "Hosts")),
		UplinkPorts:            splitList(t.cell(row, "Uplink ports")),
		LinkDiscoveryProtocol:  t.cell(row, "Link discovery protocol"),
		LinkDiscoveryOperation: t.cell(row, "Link discovery operation"),
		LACPVersion:            t.cell(row, "LACP version"),
	}
}

func dvPortFromRow(t sheetTable, row []string, switchName string) vsphere.DVPortGroup {
	failback := parseOptBool(t.cell(row, "Failback"))
	if t.hasSourceHeader("Rolling Order") && failback != nil {
		inverted := !*failback
		failback = &inverted
	}
	return vsphere.DVPortGroup{
		ID:                t.cell(row, "Object ID"),
		Key:               t.cell(row, "Key"),
		Name:              t.cell(row, "Port group"),
		Switch:            switchName,
		Type:              t.cell(row, "Type"),
		BackingType:       t.cell(row, "Backing type"),
		NumPorts:          int32(parseInt(t.cell(row, "# Ports"))),
		VLAN:              t.cell(row, "VLAN"),
		Uplink:            parseBool(t.cell(row, "Uplink")),
		Promiscuous:       parseOptBool(t.cell(row, "Promiscuous mode")),
		MACChanges:        parseOptBool(t.cell(row, "MAC changes")),
		ForgedTransmits:   parseOptBool(t.cell(row, "Forged transmits")),
		TeamingPolicy:     t.cell(row, "Teaming policy"),
		NotifySwitches:    parseOptBool(t.cell(row, "Notify switches")),
		Failback:          failback,
		IngressShaping:    parseOptBool(t.cell(row, "Ingress shaping")),
		EgressShaping:     parseOptBool(t.cell(row, "Egress shaping")),
		Blocked:           parseOptBool(t.cell(row, "Blocked")),
		AutoExpand:        parseOptBool(t.cell(row, "Auto expand")),
		ActiveUplinks:     splitList(t.cell(row, "Active uplinks")),
		StandbyUplinks:    splitList(t.cell(row, "Standby uplinks")),
		LogicalSwitchUUID: t.cell(row, "Logical switch UUID"),
		SegmentID:         t.cell(row, "Segment ID"),
	}
}

func parseBool(s string) bool {
	v, _ := strconv.ParseBool(strings.TrimSpace(s))
	return v
}

func parseOptBool(s string) *bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return nil
	}
	return &v
}

// parseInt tolerates the numeric-cell forms excelize's GetRows can produce
// (plain integers and "123.0"-style floats alike) and returns zero for
// anything it cannot parse or an empty cell. A caller that must tell "zero"
// from "absent" checks the column's presence first — see criticalColumns.
func parseInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func parseOptInt32(s string) *int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	v := int32(f)
	return &v
}

func parseOptInt64(s string) *int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	v := int64(f)
	return &v
}

func splitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// FindDuplicate reports the first stored import of a workbook with the same
// fingerprint, so a repeated import can be noticed. It never blocks one:
// importing the same file twice is legal and produces two runs.
func FindDuplicate(ctx context.Context, store *assessment.Store, sha string) (assessment.Run, bool, error) {
	if sha == "" {
		return assessment.Run{}, false, nil
	}
	runs, err := store.Runs(ctx)
	if err != nil {
		return assessment.Run{}, false, err
	}
	needle := noteSHAPrefix + sha
	for _, run := range runs {
		if run.Source == importSource && strings.Contains(run.Note, needle) {
			return run, true, nil
		}
	}
	return assessment.Run{}, false, nil
}

// Write persists a parsed result as one new assessment run. It is
// all-or-nothing: if any context fails to save after the run row itself was
// created, the run is deleted rather than left behind half-written. The run is
// never pinned, which is what makes that deletion always possible.
//
// The run and every context are stamped with the capture time, not the import
// time: history, diff and trends order by run time, so stamping an import of a
// January workbook with today's date would collapse a whole estate's history
// onto the day it was imported. importedAt is recorded in the run's note.
func (r *Result) Write(ctx context.Context, store *assessment.Store, importedAt time.Time) (assessment.Run, error) {
	now := r.Report.CapturedAt
	if len(r.contexts) == 0 {
		return assessment.Run{}, fmt.Errorf("nothing to import")
	}
	configContexts := make([]*config.Context, 0, len(r.contexts))
	for _, c := range r.contexts {
		configContexts = append(configContexts, &config.Context{Name: c.name, Endpoint: c.endpoint})
	}
	run, err := store.StartRunWithMetadata(ctx, importSource, configContexts, now, assessment.RunMetadata{
		Label:                  r.opts.Label,
		Note:                   buildNote(r.opts, r.Report, importedAt),
		InventorySchemaVersion: r.schema,
	})
	if err != nil {
		return assessment.Run{}, err
	}
	for _, c := range r.contexts {
		result := assessment.ContextResult{Name: c.name, VCenterID: c.vcenterID, Status: "success"}
		for _, vm := range c.vms {
			result.VMs = append(result.VMs, assessment.Observation{VCenterID: c.vcenterID, Context: c.name, VM: *vm})
		}
		result.Collections = append(result.Collections, assessment.CollectionResult{Kind: kindVM, Status: countStatus(len(c.vms)), ItemCount: len(c.vms)})
		result.Collections = append(result.Collections,
			resource(r, c, kindHost, c.hosts, func(v *vsphere.Host) (string, string) { return v.ID, v.Name }),
			resource(r, c, kindCluster, c.clusters, func(v *vsphere.Cluster) (string, string) { return v.ID, v.Name }),
			resource(r, c, kindDatastore, c.datastores, func(v *vsphere.Datastore) (string, string) { return v.ID, v.Name }),
			resource(r, c, kindDVSwitch, c.dvswitches, func(v *vsphere.DVSwitch) (string, string) { return v.ID, v.Name }),
		)
		if reason := r.gapFor(c, kindSnapshot); reason != "" {
			result.Collections = append(result.Collections, assessment.CollectionResult{Kind: kindSnapshot, Status: "unavailable", Error: reason})
		} else {
			n := c.snapshotCount()
			result.Collections = append(result.Collections, assessment.CollectionResult{Kind: kindSnapshot, Status: countStatus(n), ItemCount: n})
		}
		for _, skip := range neverImported {
			result.Collections = append(result.Collections, assessment.CollectionResult{Kind: skip.kind, Status: "unavailable", Error: skip.reason})
		}
		if err := store.SaveContext(ctx, run.ID, result, now); err != nil {
			_ = store.DeleteRun(context.WithoutCancel(ctx), run.ID)
			return assessment.Run{}, fmt.Errorf("save imported context %q: %w", c.name, err)
		}
	}
	finished, err := store.FinishRun(ctx, run.ID, now)
	if err != nil {
		_ = store.DeleteRun(context.WithoutCancel(ctx), run.ID)
		return assessment.Run{}, err
	}
	return finished, nil
}

func countStatus(n int) string {
	if n == 0 {
		return "empty"
	}
	return "success"
}

// resource builds one resource collection. A kind this context cannot answer
// for is recorded unavailable with its reason and no resources — never
// "empty", which would read as a confirmed answer of zero objects.
func resource[T any](r *Result, c *importedContext, kind string, values []*T, identity func(*T) (id, name string)) assessment.CollectionResult {
	if reason := r.gapFor(c, kind); reason != "" {
		return assessment.CollectionResult{Kind: kind, Status: "unavailable", Error: reason}
	}
	return resourceCollection(kind, c.vcenterID, c.name, values, identity)
}

func resourceCollection[T any](kind, vcenterID, contextName string, values []*T, identity func(*T) (id, name string)) assessment.CollectionResult {
	collection := assessment.CollectionResult{Kind: kind, Status: "success", ItemCount: len(values)}
	if len(values) == 0 {
		collection.Status = "empty"
		return collection
	}
	for _, v := range values {
		payload, err := json.Marshal(v)
		if err != nil {
			collection.Status, collection.Error = "failed", err.Error()
			continue
		}
		id, name := identity(v)
		collection.Resources = append(collection.Resources, assessment.ResourceObservation{VCenterID: vcenterID, Context: contextName, Kind: kind, ID: id, Name: name, Payload: payload})
	}
	return collection
}

func buildNote(opts Options, report Report, importedAt time.Time) string {
	var b strings.Builder
	if note := strings.TrimSpace(opts.Note); note != "" {
		b.WriteString(note)
		b.WriteString("\n\n")
	}
	b.WriteString("Imported via \"vsfleet import rvtools\"\n")
	if report.SourceLabel != "" {
		fmt.Fprintf(&b, "Source file: %s\n", report.SourceLabel)
	}
	if report.SourceSHA256 != "" {
		fmt.Fprintf(&b, "%s%s\n", noteSHAPrefix, report.SourceSHA256)
	}
	fmt.Fprintf(&b, "Captured at: %s (%s)\n", report.CapturedAt.Format(time.RFC3339), report.CapturedAtSource)
	fmt.Fprintf(&b, "Imported at: %s\n", importedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "Parser profile: %s (inventory schema %s)\n", report.ProfileVersion, report.SchemaVersion)
	fmt.Fprintf(&b, "Worksheets recognized: %s\n", strings.Join(report.RecognizedSheets, ", "))
	if len(report.IgnoredSheets) > 0 {
		fmt.Fprintf(&b, "Worksheets ignored: %s\n", strings.Join(report.IgnoredSheets, ", "))
	}
	if len(report.SkippedKinds) > 0 {
		fmt.Fprintf(&b, "Not collected by this import: %s\n", strings.Join(report.SkippedKinds, ", "))
	}
	if len(report.Ambiguities) > 0 {
		fmt.Fprintf(&b, "Identity ambiguities: %d\n", len(report.Ambiguities))
	}
	return strings.TrimRight(b.String(), "\n")
}
