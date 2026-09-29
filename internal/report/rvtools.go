// Package report contains offline renderers for persisted assessment data.
package report

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/health"
	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const (
	dateFormat = "yyyy/mm/dd hh:mm:ss"
	miB        = float64(1 << 20)
	// coverageSheetName is vsfleet's own worksheet, not an RVTools layout.
	coverageSheetName = "vsfleetCoverage"
	vmkSheetName      = "vSC_VMK"
	sourceSheetName   = "vSource"
)

var (
	// vmTailHeaders is the identity/location/provenance tail shared by every
	// per-VM tab: vInfo has its own tail (it also carries the SMBIOS UUID and
	// omits Folder), but vDisk, vNetwork, vSnapshot, vCPU, vMemory, and vTools
	// all end in exactly these columns.
	vmTailHeaders    = []string{"Annotation", "Datacenter", "Cluster", "Host", "Folder", "OS according to the configuration file", "VM ID", "VM UUID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	vmHeaders        = []string{"VM", "Powerstate", "Template", "Guest state", "CPUs", "Memory", "Primary IP Address", "Folder", "In Use MiB", "Annotation", "Datacenter", "Cluster", "Host", "OS according to the configuration file", "VM ID", "VM SMBIOS UUID", "VM UUID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	cpuHeaders       = append([]string{"VM", "Powerstate", "Template", "CPUs"}, vmTailHeaders...)
	memoryHeaders    = append([]string{"VM", "Powerstate", "Template", "Size MiB"}, vmTailHeaders...)
	diskHeaders      = []string{"VM", "Powerstate", "Template", "Disk", "Disk Key", "Disk UUID", "Capacity MiB", "Raw", "Disk Mode", "Sharing mode", "Thin", "Eagerly Scrub", "Split", "Write Through", "Level", "Shares", "Reservation", "Limit", "Controller", "SCSI label", "Unit number", "Shared Bus", "Path", "Raw LUN ID", "Raw Compatibility Mode", "Annotation", "Datacenter", "Cluster", "Host", "Folder", "OS according to the configuration file", "VM ID", "VM UUID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	networkHeaders   = []string{"VM", "Powerstate", "Template", "NIC label", "Adapter", "Network", "Connected", "Starts Connected", "Mac Address", "Mac Address type", "IPv4 Address", "IPv6 Address", "Direct Path IO", "Annotation", "Datacenter", "Cluster", "Host", "Folder", "OS according to the configuration file", "VM ID", "VM UUID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	toolsHeaders     = append([]string{"VM", "Powerstate", "Template", "Tools", "Tools Version", "Tools Version Status"}, vmTailHeaders...)
	cdHeaders        = append([]string{"VM", "Powerstate", "Template", "Device", "Device Key", "Connected", "Starts Connected", "Backing type", "Backing path", "Backing device", "Backing host", "Backing datastore ID", "Backing object ID", "Use auto detect", "Controller", "Controller label", "Unit number"}, vmTailHeaders...)
	usbHeaders       = append([]string{"VM", "Powerstate", "Template", "Device", "Device Key", "Connected", "Vendor ID", "Product ID", "Family", "Speed", "Backing type", "Backing path", "Backing device", "Backing host", "Backing datastore ID", "Backing object ID", "Use auto detect", "Controller", "Controller label", "Unit number"}, vmTailHeaders...)
	partitionHeaders = append([]string{"VM", "Powerstate", "Template", "Disk Key", "Disk", "Capacity MiB", "Consumed MiB", "Free MiB", "Free %", "Filesystem"}, vmTailHeaders...)
	// sourceHeaders are the RVTools 4.8.2 vSource columns, in RVTools order,
	// followed by vsfleet's own context column. The two VI SDK columns are the
	// same provenance pair every other worksheet carries.
	sourceHeaders       = []string{"Name", "OS type", "API type", "API version", "Version", "Patch level", "Build", "Fullname", "Product name", "Product version", "Product line", "Vendor", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	hostHeaders         = []string{"Host", "Datacenter", "Cluster", "in Maintenance Mode", "Speed", "# Cores", "CPU usage %", "# Memory", "Memory usage %", "# VMs total", "ESX Version", "Vendor", "Model", "Object ID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	hbaHeaders          = append([]string{"Device", "Bus", "Status", "Model", "Driver", "Pci", "Storage protocol", "WWNN", "WWPN", "iSCSI name", "iSCSI alias", "Type"}, hostTailHeaders...)
	nicHeaders          = append([]string{"Device", "PCI", "Driver", "Mac Address", "Link speed Mb", "Duplex", "Wake on LAN", "Switch"}, hostTailHeaders...)
	switchHeaders       = append([]string{"Switch", "# Ports", "Free Ports", "MTU", "Uplinks", "Promiscuous Mode", "Mac Changes", "Forged Transmits", "Traffic Shaping"}, hostTailHeaders...)
	portHeaders         = append([]string{"Port Group", "Switch", "VLAN", "Promiscuous Mode", "Mac Changes", "Forged Transmits"}, hostTailHeaders...)
	dvsTailHeaders      = []string{"Datacenter", "Object ID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	dvSwitchHeaders     = append([]string{"Switch", "# Ports", "Max Ports", "MTU", "Vendor", "Version", "UUID", "Description", "Contact", "Contact detail", "Hosts", "Uplink ports", "Link discovery protocol", "Link discovery operation", "LACP version"}, dvsTailHeaders...)
	dvPortHeaders       = append([]string{"Port", "Switch", "Key", "Type", "Backing type", "# Ports", "VLAN", "Uplink", "Allow Promiscuous", "Mac Changes", "Forged Transmits", "Policy", "Notify Switch", "Rolling Order", "In Traffic Shaping", "Out Traffic Shaping", "Blocked", "Auto expand", "Active Uplink", "Standby Uplink", "Logical switch UUID", "Segment ID"}, dvsTailHeaders...)
	vmkHeaders          = append([]string{"Device", "Port group", "Mac Address", "MTU", "TSO", "Netstack", "DHCP", "IP Address", "Subnet mask", "Service console"}, hostTailHeaders...)
	multipathHeaders    = append([]string{"LUN", "Device path", "Policy", "Local disk", "Path count", "Active paths", "Standby paths", "Dead paths", "Disabled paths", "Working paths"}, hostTailHeaders...)
	clusterHeaders      = []string{"Name", "NumHosts", "numEffectiveHosts", "TotalCpu", "NumCpuCores", "TotalMemory", "HA enabled", "DRS enabled", "Object ID", "Datacenter", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	resourcePoolHeaders = []string{"Resource pool", "Name", "Status", "VMs", "vCPUs", "CPU limit", "CPU overheadLimit", "CPU reservation", "CPU level", "CPU shares", "CPU expandableReservation", "Mem Configured", "Mem limit", "Mem overheadLimit", "Mem reservation", "Mem level", "Mem shares", "Mem expandableReservation", "Config status", "Object ID", "Datacenter", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	datastoreHeaders    = []string{"Name", "Datacenter", "Type", "Capacity MiB", "In Use MiB", "Free MiB", "Free %", "Accessible", "Maintenance mode", "Object ID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	snapshotHeaders     = []string{"VM", "Powerstate", "Name", "Description", "Date / time", "Quiesced", "State", "Annotation", "Datacenter", "Cluster", "Host", "Folder", "OS according to the configuration file", "VM ID", "VM UUID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	healthHeaders       = []string{"Name", "Message", "Message type", "Category", "vsfleet Rule", "Recommendation", "Evidence", "Object type", "Datacenter", "Object ID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}
	coverageHeaders     = []string{"Run ID", "Run label", "Run started", "Run finished", "Run status", "Context", "Endpoint", "Datacenter", "vCenter ID", "Sheet", "Collection status", "Item count", "Error"}
)

// sheet is one rendered RVTools tab: a header row plus its data rows, in the
// canonical order shared by every export format. dateCols names the column
// indexes (0-based) that hold a time.Value and should render as dates rather
// than raw numbers where the format distinguishes the two (XLSX only; CSV
// renders every value as text).
type sheet struct {
	name     string
	headers  []string
	rows     [][]any
	dateCols []int
}

// rvtoolsSheets canonicalizes and validates the export data, then returns
// every RVTools tab in tab order. WriteRVTools and RVToolsCSV both build on
// this so the two formats render identical content on identical terms.
func rvtoolsSheets(data assessment.ExportData, healthReport health.Report) ([]sheet, error) {
	return rvtoolsSheetsFor(data, healthReport, false)
}

// rvtoolsSheetsFor is rvtoolsSheets with control over the opt-in license
// worksheets. They are written only for a run that recorded license
// collection, unless describeAll is set, which the compatibility profile uses
// to enumerate every sheet the exporter can produce.
func rvtoolsSheetsFor(data assessment.ExportData, healthReport health.Report, describeAll bool) ([]sheet, error) {
	data = canonicalData(data)
	if err := validateResources(data.Resources); err != nil {
		return nil, err
	}
	all := []sheet{
		{name: "vInfo", headers: vmHeaders, rows: vmRows(data)},
		{name: "vCPU", headers: cpuHeaders, rows: cpuRows(data)},
		{name: "vMemory", headers: memoryHeaders, rows: memoryRows(data)},
		{name: "vDisk", headers: diskHeaders, rows: diskRows(data)},
		{name: "vPartition", headers: partitionHeaders, rows: partitionRows(data)},
		{name: "vNetwork", headers: networkHeaders, rows: networkRows(data)},
		{name: "vCD", headers: cdHeaders, rows: cdRows(data)},
		{name: "vUSB", headers: usbHeaders, rows: usbRows(data)},
		{name: "vSnapshot", headers: snapshotHeaders, rows: snapshotRows(data), dateCols: []int{4}},
		{name: "vTools", headers: toolsHeaders, rows: toolsRows(data)},
		{name: sourceSheetName, headers: sourceHeaders, rows: sourceRows(data)},
		{name: "vRP", headers: resourcePoolHeaders, rows: resourcePoolRows(data)},
		{name: "vCluster", headers: clusterHeaders, rows: clusterRows(data)},
		{name: "vHost", headers: hostHeaders, rows: hostRows(data)},
		{name: "vHBA", headers: hbaHeaders, rows: hbaRows(data)},
		{name: "vNIC", headers: nicHeaders, rows: nicRows(data)},
		{name: "vSwitch", headers: switchHeaders, rows: switchRows(data)},
		{name: "vPort", headers: portHeaders, rows: portRows(data)},
		{name: "dvSwitch", headers: dvSwitchHeaders, rows: dvSwitchRows(data)},
		{name: "dvPort", headers: dvPortHeaders, rows: dvPortRows(data)},
		{name: vmkSheetName, headers: vmkHeaders, rows: vmkRows(data)},
		{name: "vDatastore", headers: datastoreHeaders, rows: datastoreRows(data)},
		{name: "vMultiPath", headers: multipathHeaders, rows: multipathRows(data)},
	}
	// RVTools 4.8 places vLicense directly after vMultiPath. The assignment
	// detail is a vsfleet extension and follows it.
	if vLicense, assignments := licenseSheets(data, describeAll); vLicense != nil {
		all = append(all, *vLicense, *assignments)
	}
	return append(all,
		sheet{name: "vHealth", headers: healthHeaders, rows: healthRows(data, healthReport)},
		sheet{name: coverageSheetName, headers: coverageHeaders, rows: coverageRows(data, healthReport), dateCols: []int{2, 3}},
		sheet{name: performanceSheetName, headers: performanceHeaders, rows: performanceRows(data), dateCols: performanceDateCols},
	), nil
}

// WriteRVTools writes the twenty-four RVTools-compatible sheets plus the
// vsfleetCoverage and vsfleetPerformance extension sheets. vHealth is derived from the supplied
// report; callers evaluate it before entering the renderer. The output is normalized as a ZIP archive
// with fixed entry order and timestamps, making repeated writes byte-identical.
func WriteRVTools(w io.Writer, data assessment.ExportData, healthReport health.Report) error {
	sheets, err := rvtoolsSheets(data, healthReport)
	if err != nil {
		return err
	}
	f := excelize.NewFile()
	defer f.Close()
	if err := f.SetDocProps(&excelize.DocProperties{
		Title:       "vsfleet RVTools assessment export",
		Subject:     "Persisted vsfleet assessment",
		Creator:     "vsfleet",
		Description: "Offline export of a persisted vsfleet assessment",
		Created:     data.Run.StartedAt.UTC().Format(time.RFC3339),
		Modified:    data.Run.StartedAt.UTC().Format(time.RFC3339),
	}); err != nil {
		return err
	}
	styles, err := newStyles(f)
	if err != nil {
		return err
	}
	if err := f.SetSheetName("Sheet1", sheets[0].name); err != nil {
		return err
	}
	for _, s := range sheets[1:] {
		if _, err := f.NewSheet(s.name); err != nil {
			return err
		}
	}
	for _, s := range sheets {
		if err := writeSheet(f, s.name, s.headers, s.rows, s.dateCols, styles); err != nil {
			return err
		}
	}
	buf, err := f.WriteToBuffer()
	if err != nil {
		return err
	}
	normalized, err := normalizeZip(buf.Bytes())
	if err != nil {
		return err
	}
	n, err := w.Write(normalized)
	if err == nil && n != len(normalized) {
		return io.ErrShortWrite
	}
	return err
}

// CSVFile is one rendered RVTools tab, ready to write to disk as
// "<Name>.csv".
type CSVFile struct {
	Name string
	Data []byte
}

// RVToolsCSV renders every RVTools tab as its own CSV document, in tab
// order, sharing row generation and canonical ordering with WriteRVTools so
// the two formats agree and each is deterministic on the same terms.
//
// Cells favor pipeline consumption over spreadsheet display: timestamps are
// RFC3339 in UTC, booleans are "true"/"false", numbers are unformatted, and
// an absent value is an empty field.
func RVToolsCSV(data assessment.ExportData, healthReport health.Report) ([]CSVFile, error) {
	sheets, err := rvtoolsSheets(data, healthReport)
	if err != nil {
		return nil, err
	}
	files := make([]CSVFile, 0, len(sheets))
	for _, s := range sheets {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		if err := w.Write(s.headers); err != nil {
			return nil, err
		}
		record := make([]string, len(s.headers))
		for _, row := range s.rows {
			for i, value := range row {
				record[i] = csvCell(value)
			}
			if err := w.Write(record); err != nil {
				return nil, err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return nil, err
		}
		files = append(files, CSVFile{Name: s.name + ".csv", Data: buf.Bytes()})
	}
	return files, nil
}

// csvCell renders one sheet cell as CSV text. The set of types here matches
// exactly what the row builders below produce.
func csvCell(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case bool:
		if v {
			return "true"
		}
		return "false"
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

type styles struct {
	header, date int
}

func newStyles(f *excelize.File) (styles, error) {
	date := dateFormat
	header, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "FFFFFF"}, Fill: excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"1F4E78"}}, Alignment: &excelize.Alignment{Vertical: "center", WrapText: true}})
	if err != nil {
		return styles{}, err
	}
	dateStyle, err := f.NewStyle(&excelize.Style{CustomNumFmt: &date})
	if err != nil {
		return styles{}, err
	}
	return styles{header: header, date: dateStyle}, nil
}

func writeSheet(f *excelize.File, name string, headers []string, rows [][]any, dateCols []int, s styles) error {
	header := make([]any, len(headers))
	for i, h := range headers {
		header[i] = h
	}
	if err := f.SetSheetRow(name, "A1", &header); err != nil {
		return err
	}
	lastCol, err := excelize.ColumnNumberToName(len(headers))
	if err != nil {
		return err
	}
	if err := f.SetCellStyle(name, "A1", lastCol+"1", s.header); err != nil {
		return err
	}
	for i, row := range rows {
		rowNumber := i + 2
		cell, err := excelize.CoordinatesToCellName(1, rowNumber)
		if err != nil {
			return err
		}
		if err := f.SetSheetRow(name, cell, &row); err != nil {
			return err
		}
		for _, col := range dateCols {
			if col < 0 || col >= len(row) || row[col] == nil {
				continue
			}
			ref, err := excelize.CoordinatesToCellName(col+1, rowNumber)
			if err != nil {
				return err
			}
			if err := f.SetCellStyle(name, ref, ref, s.date); err != nil {
				return err
			}
		}
	}
	lastRow := len(rows) + 1
	if err := f.AutoFilter(name, fmt.Sprintf("A1:%s%d", lastCol, lastRow), nil); err != nil {
		return err
	}
	if err := f.SetPanes(name, &excelize.Panes{Freeze: true, YSplit: 1, TopLeftCell: "A2", ActivePane: "bottomLeft"}); err != nil {
		return err
	}
	for i := range headers {
		col, _ := excelize.ColumnNumberToName(i + 1)
		width := float64(len(headers[i]) + 2)
		if width < 12 {
			width = 12
		}
		if width > 32 {
			width = 32
		}
		if err := f.SetColWidth(name, col, col, width); err != nil {
			return err
		}
	}
	return f.SetSheetDimension(name, fmt.Sprintf("A1:%s%d", lastCol, lastRow))
}

func vmRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0, len(data.VMs))
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		rows = append(rows, []any{vm.Name, vm.PowerState, vm.IsTemplate, vm.GuestState, vm.CPU, vm.MemoryMB, vm.IPAddress, vm.Folder, storageMiB(vm.StorageGB), vm.Annotation, vm.Datacenter, vm.Cluster, vm.Host, vm.GuestOS, vm.ID, vm.BIOSUUID, vm.InstanceUUID, contextEndpoint(data, obs.Context), obs.VCenterID, obs.Context})
	}
	return rows
}

// vmTail returns the identity/location/provenance columns shared by the
// vCPU, vMemory, and vTools tabs (and, inline, by vDisk/vNetwork/vSnapshot).
func vmTail(data assessment.ExportData, obs assessment.Observation) []any {
	vm := obs.VM
	return []any{vm.Annotation, vm.Datacenter, vm.Cluster, vm.Host, vm.Folder, vm.GuestOS, vm.ID, vm.InstanceUUID, contextEndpoint(data, obs.Context), obs.VCenterID, obs.Context}
}

func cpuRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0, len(data.VMs))
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		row := append([]any{vm.Name, vm.PowerState, vm.IsTemplate, vm.CPU}, vmTail(data, obs)...)
		rows = append(rows, row)
	}
	return rows
}

func memoryRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0, len(data.VMs))
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		row := append([]any{vm.Name, vm.PowerState, vm.IsTemplate, vm.MemoryMB}, vmTail(data, obs)...)
		rows = append(rows, row)
	}
	return rows
}

func toolsRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0, len(data.VMs))
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		row := append([]any{vm.Name, vm.PowerState, vm.IsTemplate, vm.ToolsState, optionalString(vm.ToolsVersion), optionalString(vm.ToolsVersionStatus)}, vmTail(data, obs)...)
		rows = append(rows, row)
	}
	return rows
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func diskRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		for _, disk := range vm.Disks {
			rows = append(rows, []any{
				vm.Name, vm.PowerState, vm.IsTemplate, disk.Label, disk.Key, disk.UUID,
				float64(disk.CapacityBytes) / miB, disk.Raw, disk.DiskMode, disk.Sharing,
				optionalBool(disk.ThinProvisioned), optionalBool(disk.EagerlyScrub), optionalBool(disk.Split), optionalBool(disk.WriteThrough),
				disk.SharesLevel, optionalInt32(disk.Shares), optionalInt32(disk.Reservation), optionalInt64(disk.Limit),
				disk.Controller, disk.ControllerLabel, optionalInt32(disk.UnitNumber), disk.SharedBus, disk.BackingPath,
				disk.RawLUNID, disk.RawCompatibilityMode, vm.Annotation, vm.Datacenter, vm.Cluster, vm.Host, vm.Folder,
				vm.GuestOS, vm.ID, vm.InstanceUUID, contextEndpoint(data, obs.Context), obs.VCenterID, obs.Context,
			})
		}
	}
	return rows
}

// partitionRows renders the guest filesystems VMware Tools reported. A VM
// whose Tools were not running contributes no rows at all, which is why
// vsfleetCoverage reports how much of the estate answered rather than leaving
// a reader to infer it from a row count.
func partitionRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		for _, part := range vm.Partitions {
			row := []any{
				vm.Name, vm.PowerState, vm.IsTemplate, diskKeyCell(part.DiskKeys), part.Path,
				float64(part.CapacityBytes) / miB,
				float64(part.UsedBytes()) / miB,
				float64(part.FreeBytes) / miB,
				freePercent(part),
				part.FilesystemType,
			}
			rows = append(rows, append(row, vmTail(data, obs)...))
		}
	}
	return rows
}

// diskKeyCell renders the virtual disks behind a guest filesystem, which is
// what makes vPartition joinable to vDisk. A single key stays a number so it
// matches vDisk's own numeric Disk Key cell; a spanned volume names several
// and they are joined the way vNetwork already joins a NIC's addresses,
// which no longer joins but at least does not silently drop a disk. Tools
// reports no mapping at all before vSphere 7.0, and that empty cell must not
// become key zero.
func diskKeyCell(keys []int32) any {
	switch len(keys) {
	case 0:
		return nil
	case 1:
		return keys[0]
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.FormatInt(int64(key), 10))
	}
	return strings.Join(parts, ", ")
}

// freePercent matches RVTools' own column. A partition of zero capacity is
// reported as zero rather than dividing by it: Tools occasionally reports a
// mounted filesystem it cannot size, and an empty cell there would be read as
// "full" by anything summing the column.
func freePercent(p vsphere.VMPartition) float64 {
	if p.CapacityBytes <= 0 {
		return 0
	}
	return math.Round(float64(p.FreeBytes)/float64(p.CapacityBytes)*10000) / 100
}

func networkRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		for _, nic := range vm.NICs {
			rows = append(rows, []any{
				vm.Name, vm.PowerState, vm.IsTemplate, nic.Label, nic.Adapter, nic.Network,
				optionalBool(nic.Connected), optionalBool(nic.StartsConnected), nic.MACAddress, nic.MACAddressType,
				strings.Join(nic.IPv4, ", "), strings.Join(nic.IPv6, ", "), optionalBool(nic.UPTCompatible), vm.Annotation,
				vm.Datacenter, vm.Cluster, vm.Host, vm.Folder, vm.GuestOS, vm.ID, vm.InstanceUUID,
				contextEndpoint(data, obs.Context), obs.VCenterID, obs.Context,
			})
		}
	}
	return rows
}

func cdRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		for _, cd := range vm.CDROMs {
			row := []any{
				vm.Name, vm.PowerState, vm.IsTemplate, cd.Label, cd.Key,
				optionalBool(cd.Connected), optionalBool(cd.StartsConnected), cd.BackingType,
				cd.BackingPath, cd.BackingDevice, cd.BackingHost, cd.BackingDatastore,
				cd.BackingObjectID, optionalBool(cd.UseAutoDetect), cd.Controller,
				cd.ControllerLabel, optionalInt32(cd.UnitNumber),
			}
			rows = append(rows, append(row, vmTail(data, obs)...))
		}
	}
	return rows
}

func usbRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		for _, usb := range vm.USBs {
			row := []any{
				vm.Name, vm.PowerState, vm.IsTemplate, usb.Label, usb.Key,
				optionalBool(usb.Connected), optionalInt32NonZero(usb.Vendor), optionalInt32NonZero(usb.Product),
				strings.Join(usb.Family, ", "), strings.Join(usb.Speed, ", "), usb.BackingType,
				usb.BackingPath, usb.BackingDevice, usb.BackingHost, usb.BackingDatastore,
				usb.BackingObjectID, optionalBool(usb.UseAutoDetect), usb.Controller,
				usb.ControllerLabel, optionalInt32(usb.UnitNumber),
			}
			rows = append(rows, append(row, vmTail(data, obs)...))
		}
	}
	return rows
}

func optionalBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

// invertedOptionalBool renders the raw value for a vSphere boolean whose
// stored report value represents the opposite user-facing setting.
func invertedOptionalBool(value *bool) any {
	if value == nil {
		return nil
	}
	return !*value
}

func optionalInt32(value *int32) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt32NonZero(value int32) any {
	if value == 0 {
		return nil
	}
	return value
}

func optionalInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

// sourceRows renders one row per context whose run stored a ServiceInstance
// About record. It reads only that stored evidence: a context without one (a
// run captured before schema 17, or a context that never connected) has no
// row, and vsfleetCoverage says why, rather than a version being invented.
func sourceRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0, len(data.Contexts))
	for _, c := range data.Contexts {
		if c.Source == nil {
			continue
		}
		src := c.Source
		rows = append(rows, []any{src.Name, src.OSType, src.APIType, src.APIVersion, src.Version, src.PatchLevel, src.Build, src.FullName, src.LicenseProductName, src.LicenseProductVersion, src.ProductLineID, src.Vendor, c.Endpoint, c.VCenterID, c.Name})
	}
	return rows
}

func hostRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, r := range data.Resources {
		if r.Kind != "host" {
			continue
		}
		var host vsphere.Host
		if err := json.Unmarshal(r.Payload, &host); err != nil {
			continue
		}
		id := nonempty(host.ID, r.ID)
		rows = append(rows, []any{nonempty(host.Name, r.Name), host.Datacenter, host.Cluster, host.InMaintenance, host.CPUMHz, host.CPUCores, hostCPUPercent(host), host.MemoryMB, hostMemoryPercent(host), host.VMCount, host.Version, host.Vendor, host.Model, id, contextEndpoint(data, r.Context), r.VCenterID, r.Context})
	}
	return rows
}

var hostTailHeaders = []string{"Host", "Datacenter", "Cluster", "Object ID", "VI SDK Server", "VI SDK UUID", "vsfleet Context"}

func hostTail(data assessment.ExportData, resource assessment.ResourceObservation, host vsphere.Host) []any {
	return []any{
		nonempty(host.Name, resource.Name), host.Datacenter, host.Cluster,
		nonempty(host.ID, resource.ID), contextEndpoint(data, resource.Context), resource.VCenterID, resource.Context,
	}
}

func hostConfigResources(data assessment.ExportData) []struct {
	resource assessment.ResourceObservation
	host     vsphere.Host
} {
	resources := make([]struct {
		resource assessment.ResourceObservation
		host     vsphere.Host
	}, 0)
	for _, resource := range data.Resources {
		if resource.Kind != "host" {
			continue
		}
		var host vsphere.Host
		if err := json.Unmarshal(resource.Payload, &host); err != nil {
			continue
		}
		resources = append(resources, struct {
			resource assessment.ResourceObservation
			host     vsphere.Host
		}{resource: resource, host: host})
	}
	return resources
}

func hbaRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range hostConfigResources(data) {
		for _, hba := range item.host.HBAs {
			row := []any{hba.Device, hba.Bus, hba.Status, hba.Model, hba.Driver, hba.PCI, hba.StorageProtocol,
				optionalInt64(hba.WWNN), optionalInt64(hba.WWPN), optionalString(hba.IScsiName), optionalString(hba.IScsiAlias), hba.Type}
			rows = append(rows, append(row, hostTail(data, item.resource, item.host)...))
		}
	}
	return rows
}

func nicRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range hostConfigResources(data) {
		for _, nic := range item.host.NICs {
			row := []any{nic.Device, nic.PCI, nic.Driver, nic.MAC, optionalInt32(nic.LinkSpeedMB), optionalBool(nic.Duplex), nic.WakeOnLAN, optionalString(nic.Switch)}
			rows = append(rows, append(row, hostTail(data, item.resource, item.host)...))
		}
	}
	return rows
}

func switchRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range hostConfigResources(data) {
		for _, sw := range item.host.VSwitches {
			row := []any{sw.Name, sw.NumPorts, sw.FreePorts, sw.MTU, strings.Join(sw.Uplinks, ", "), optionalBool(sw.Promiscuous), optionalBool(sw.MACChanges), optionalBool(sw.ForgedTransmits), optionalBool(sw.TrafficShaping)}
			rows = append(rows, append(row, hostTail(data, item.resource, item.host)...))
		}
	}
	return rows
}

func portRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range hostConfigResources(data) {
		for _, port := range item.host.PortGroups {
			row := []any{port.Name, port.Switch, port.VLAN, optionalBool(port.Promiscuous), optionalBool(port.MACChanges), optionalBool(port.ForgedTransmits)}
			rows = append(rows, append(row, hostTail(data, item.resource, item.host)...))
		}
	}
	return rows
}

func dvsTail(data assessment.ExportData, resource assessment.ResourceObservation, sw vsphere.DVSwitch) []any {
	return []any{sw.Datacenter, nonempty(sw.ID, resource.ID), contextEndpoint(data, resource.Context), resource.VCenterID, resource.Context}
}

func dvPortTail(data assessment.ExportData, resource assessment.ResourceObservation, sw vsphere.DVSwitch, port vsphere.DVPortGroup) []any {
	return []any{sw.Datacenter, nonempty(port.ID, resource.ID), contextEndpoint(data, resource.Context), resource.VCenterID, resource.Context}
}

func dvSwitchRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, resource := range data.Resources {
		if resource.Kind != "dvswitch" {
			continue
		}
		var sw vsphere.DVSwitch
		if err := json.Unmarshal(resource.Payload, &sw); err != nil {
			continue
		}
		row := []any{
			nonempty(sw.Name, resource.Name), sw.NumPorts, sw.MaxPorts, sw.MaxMTU, sw.Vendor, sw.Version, sw.UUID,
			optionalString(sw.Description), optionalString(sw.Contact), optionalString(sw.ContactDetail),
			strings.Join(sw.Hosts, ", "), strings.Join(sw.UplinkPorts, ", "), optionalString(sw.LinkDiscoveryProtocol),
			optionalString(sw.LinkDiscoveryOperation), optionalString(sw.LACPVersion),
		}
		rows = append(rows, append(row, dvsTail(data, resource, sw)...))
	}
	return rows
}

func dvPortRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, resource := range data.Resources {
		if resource.Kind != "dvswitch" {
			continue
		}
		var sw vsphere.DVSwitch
		if err := json.Unmarshal(resource.Payload, &sw); err != nil {
			continue
		}
		for _, port := range sw.PortGroups {
			row := []any{
				port.Name, nonempty(sw.Name, resource.Name), port.Key, port.Type, port.BackingType, port.NumPorts,
				optionalString(port.VLAN), port.Uplink, optionalBool(port.Promiscuous), optionalBool(port.MACChanges),
				optionalBool(port.ForgedTransmits), optionalString(port.TeamingPolicy), optionalBool(port.NotifySwitches),
				invertedOptionalBool(port.Failback), optionalBool(port.IngressShaping), optionalBool(port.EgressShaping),
				optionalBool(port.Blocked), optionalBool(port.AutoExpand), strings.Join(port.ActiveUplinks, ", "),
				strings.Join(port.StandbyUplinks, ", "), optionalString(port.LogicalSwitchUUID), optionalString(port.SegmentID),
			}
			rows = append(rows, append(row, dvPortTail(data, resource, sw, port)...))
		}
	}
	return rows
}

func vmkRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range hostConfigResources(data) {
		for _, vmk := range item.host.VMKs {
			row := []any{vmk.Device, vmk.PortGroup, vmk.MAC, vmk.MTU, optionalBool(vmk.TSO), vmk.Netstack, optionalBool(vmk.DHCP), optionalString(vmk.IP), optionalString(vmk.SubnetMask), vmk.ServiceConsole}
			rows = append(rows, append(row, hostTail(data, item.resource, item.host)...))
		}
	}
	return rows
}

func multipathRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range hostConfigResources(data) {
		for _, multipath := range item.host.Multipaths {
			row := []any{multipath.LUN, multipath.DevicePath, multipath.Policy, optionalBool(multipath.LocalDisk), multipath.PathCount, multipath.Active, multipath.Standby, multipath.Dead, multipath.Disabled, multipath.WorkingPaths}
			rows = append(rows, append(row, hostTail(data, item.resource, item.host)...))
		}
	}
	return rows
}

func clusterRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, r := range data.Resources {
		if r.Kind != "cluster" {
			continue
		}
		var cluster vsphere.Cluster
		if err := json.Unmarshal(r.Payload, &cluster); err != nil {
			continue
		}
		rows = append(rows, []any{nonempty(cluster.Name, r.Name), cluster.Hosts, cluster.EffectiveHost, cluster.TotalCPUMHz, cluster.CPUCores, cluster.TotalMemoryMB, cluster.HAEnabled, cluster.DRSEnabled, nonempty(cluster.ID, r.ID), cluster.Datacenter, contextEndpoint(data, r.Context), r.VCenterID, r.Context})
	}
	return rows
}

func resourcePoolRows(data assessment.ExportData) [][]any {
	vmCPUs := make(map[string]int32, len(data.VMs))
	for _, item := range data.VMs {
		vmCPUs[item.Observation.VM.ID] = item.Observation.VM.CPU
	}

	rows := make([][]any, 0)
	for _, r := range data.Resources {
		if r.Kind != "resourcepool" {
			continue
		}
		var pool vsphere.ResourcePool
		if err := json.Unmarshal(r.Payload, &pool); err != nil {
			continue
		}
		vCPUs := int32(0)
		for _, vmRef := range pool.VMRefs {
			vCPUs += vmCPUs[vmRef]
		}
		rows = append(rows, []any{
			pool.Path, nonempty(pool.Name, r.Name), pool.Status, len(pool.VMRefs), vCPUs,
			optionalInt64(pool.CPULimitMHz), optionalInt64(pool.CPUOverheadLimitMHz), optionalInt64(pool.CPUReservationMHz),
			pool.CPULevel, pool.CPUShares, pool.CPUExpandable, pool.MemConfiguredMB,
			optionalInt64(pool.MemLimitMB), optionalInt64(pool.MemOverheadLimitMB), optionalInt64(pool.MemReservationMB),
			pool.MemLevel, pool.MemShares, pool.MemExpandable, pool.ConfigStatus,
			nonempty(pool.ID, r.ID), pool.Datacenter, contextEndpoint(data, r.Context), r.VCenterID, r.Context,
		})
	}
	return rows
}

func datastoreRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, r := range data.Resources {
		if r.Kind != "datastore" {
			continue
		}
		var datastore vsphere.Datastore
		if err := json.Unmarshal(r.Payload, &datastore); err != nil {
			continue
		}
		capacity, free := float64(datastore.CapacityBytes)/miB, float64(datastore.FreeBytes)/miB
		var freePercent any
		if datastore.CapacityBytes > 0 {
			freePercent = float64(datastore.FreeBytes) / float64(datastore.CapacityBytes) * 100
		}
		rows = append(rows, []any{nonempty(datastore.Name, r.Name), datastore.Datacenter, datastore.Type, capacity, float64(datastore.UsedBytes()) / miB, free, freePercent, datastore.Accessible, datastore.Maintenance, nonempty(datastore.ID, r.ID), contextEndpoint(data, r.Context), r.VCenterID, r.Context})
	}
	return rows
}

func snapshotRows(data assessment.ExportData) [][]any {
	rows := make([][]any, 0)
	for _, item := range data.VMs {
		obs, vm := item.Observation, item.Observation.VM
		for _, snapshot := range item.Snapshots {
			var created any
			if !snapshot.CreateTime.IsZero() {
				created = snapshot.CreateTime.UTC()
			}
			rows = append(rows, []any{vm.Name, vm.PowerState, snapshot.Name, snapshot.Description, created, snapshot.Quiesced, snapshot.PowerState, vm.Annotation, vm.Datacenter, vm.Cluster, vm.Host, vm.Folder, vm.GuestOS, vm.ID, vm.InstanceUUID, contextEndpoint(data, obs.Context), obs.VCenterID, obs.Context})
		}
	}
	return rows
}

func healthRows(data assessment.ExportData, healthReport health.Report) [][]any {
	rows := make([][]any, 0, len(healthReport.Findings))
	for _, finding := range healthReport.Findings {
		object := finding.Object
		rows = append(rows, []any{
			object.Name, finding.Message, string(finding.Severity), string(finding.Category), finding.Rule, finding.Recommendation, healthEvidence(finding.Evidence), object.Kind,
			object.Datacenter, object.ID, contextEndpoint(data, object.Context), object.VCenterID, object.Context,
		})
	}
	return rows
}

func healthEvidence(evidence []health.Evidence) string {
	parts := make([]string, 0, len(evidence))
	for _, item := range evidence {
		value := item.Field + "=" + item.Observed
		if item.Expected != "" {
			value += " (expected " + item.Expected + ")"
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "; ")
}

// coverageSpec is one worksheet's coverage accounting for one context.
type coverageSpec struct {
	kind, sheet string
	count       int
	hostConfig  bool
	license     bool
	assignments bool
}

func coverageRows(data assessment.ExportData, healthReport health.Report) [][]any {
	counts := make(map[string]int)
	diskCounts := make(map[string]int)
	networkCounts := make(map[string]int)
	cdCounts := make(map[string]int)
	usbCounts := make(map[string]int)
	for _, item := range data.VMs {
		counts[item.Observation.Context]++
		diskCounts[item.Observation.Context] += len(item.Observation.VM.Disks)
		networkCounts[item.Observation.Context] += len(item.Observation.VM.NICs)
		cdCounts[item.Observation.Context] += len(item.Observation.VM.CDROMs)
		usbCounts[item.Observation.Context] += len(item.Observation.VM.USBs)
	}
	snapshotCounts := make(map[string]int)
	for _, item := range data.VMs {
		snapshotCounts[item.Observation.Context] += len(item.Snapshots)
	}
	// Partitions come from VMware Tools inside the guest, so a successful
	// capture still covers only the VMs whose Tools answered. Track both the
	// rows and how many VMs produced them, so coverage can say which.
	partitionCounts := make(map[string]int)
	partitionVMs := make(map[string]int)
	for _, item := range data.VMs {
		ctx := item.Observation.Context
		partitionCounts[ctx] += len(item.Observation.VM.Partitions)
		if len(item.Observation.VM.Partitions) > 0 {
			partitionVMs[ctx]++
		}
	}
	resources := make(map[string]map[string]int)
	hostConfigCounts := make(map[string]map[string]int)
	dvsCounts := make(map[string]map[string]int)
	for _, r := range data.Resources {
		if resources[r.Context] == nil {
			resources[r.Context] = make(map[string]int)
		}
		resources[r.Context][r.Kind]++
		if r.Kind == "host" {
			var host vsphere.Host
			if err := json.Unmarshal(r.Payload, &host); err == nil {
				counts := hostConfigCounts[r.Context]
				if counts == nil {
					counts = make(map[string]int)
					hostConfigCounts[r.Context] = counts
				}
				counts["vHBA"] += len(host.HBAs)
				counts["vNIC"] += len(host.NICs)
				counts["vSwitch"] += len(host.VSwitches)
				counts["vPort"] += len(host.PortGroups)
				counts[vmkSheetName] += len(host.VMKs)
				counts["vMultiPath"] += len(host.Multipaths)
			}
		}
		if r.Kind == "dvswitch" {
			var sw vsphere.DVSwitch
			if err := json.Unmarshal(r.Payload, &sw); err == nil {
				counts := dvsCounts[r.Context]
				if counts == nil {
					counts = make(map[string]int)
					dvsCounts[r.Context] = counts
				}
				counts["dvSwitch"]++
				counts["dvPort"] += len(sw.PortGroups)
			}
		}
	}
	rows := make([][]any, 0, len(data.Contexts)*25)
	sourceRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 17)
	licenseCounts, licenseAssignmentCounts := licenseCoverageCounts(data)
	devicesRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 2)
	attachedDevicesRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 6)
	toolsRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 3)
	partitionsRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 4)
	poolsRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 8)
	hostConfigRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 9)
	dvsRecorded := inventoryAtLeast(data.Run.InventorySchemaVersion, 10)
	if !devicesRecorded {
		diskCounts = make(map[string]int)
		networkCounts = make(map[string]int)
	}
	if !attachedDevicesRecorded {
		cdCounts = make(map[string]int)
		usbCounts = make(map[string]int)
	}
	if !partitionsRecorded {
		partitionCounts = make(map[string]int)
		partitionVMs = make(map[string]int)
	}
	if !hostConfigRecorded {
		hostConfigCounts = make(map[string]map[string]int)
	}
	for _, c := range data.Contexts {
		collections := make(map[string]assessment.CollectionRun)
		for _, collection := range c.Collections {
			collections[collection.Kind] = collection
		}
		specs := []coverageSpec{
			{kind: "vm", sheet: "vInfo", count: counts[c.Name]},
			{kind: "vcpu", sheet: "vCPU", count: counts[c.Name]},
			{kind: "vmemory", sheet: "vMemory", count: counts[c.Name]},
			{kind: "vdisk", sheet: "vDisk", count: diskCounts[c.Name]},
			{kind: "vpartition", sheet: "vPartition", count: partitionCounts[c.Name]},
			{kind: "vnetwork", sheet: "vNetwork", count: networkCounts[c.Name]},
			{kind: "vcd", sheet: "vCD", count: cdCounts[c.Name]},
			{kind: "vusb", sheet: "vUSB", count: usbCounts[c.Name]},
			{kind: "snapshot", sheet: "vSnapshot", count: snapshotCounts[c.Name]},
			{kind: "vtools", sheet: "vTools", count: counts[c.Name]},
			{kind: "source", sheet: sourceSheetName},
			{kind: "resourcepool", sheet: "vRP", count: resources[c.Name]["resourcepool"]},
			{kind: "cluster", sheet: "vCluster", count: resources[c.Name]["cluster"]},
			{kind: "host", sheet: "vHost", count: resources[c.Name]["host"]},
			{kind: "host", sheet: "vHBA", count: hostConfigCounts[c.Name]["vHBA"], hostConfig: true},
			{kind: "host", sheet: "vNIC", count: hostConfigCounts[c.Name]["vNIC"], hostConfig: true},
			{kind: "host", sheet: "vSwitch", count: hostConfigCounts[c.Name]["vSwitch"], hostConfig: true},
			{kind: "host", sheet: "vPort", count: hostConfigCounts[c.Name]["vPort"], hostConfig: true},
			{kind: "dvswitch", sheet: "dvSwitch", count: dvsCounts[c.Name]["dvSwitch"]},
			{kind: "dvswitch", sheet: "dvPort", count: dvsCounts[c.Name]["dvPort"]},
			{kind: "host", sheet: vmkSheetName, count: hostConfigCounts[c.Name][vmkSheetName], hostConfig: true},
			{kind: "datastore", sheet: "vDatastore", count: resources[c.Name]["datastore"]},
			{kind: "host", sheet: "vMultiPath", count: hostConfigCounts[c.Name]["vMultiPath"], hostConfig: true},
		}
		if licensesRecorded(data) {
			specs = append(specs,
				coverageSpec{kind: assessment.LicenseKind, sheet: licenseSheetName, count: licenseCounts[c.Name], license: true},
				coverageSpec{kind: assessment.LicenseKind, sheet: licenseAssignmentSheetName, count: licenseAssignmentCounts[c.Name], license: true, assignments: true},
			)
		}
		specs = append(specs, coverageSpec{kind: "vhealth", sheet: "vHealth", count: healthFindingsForContext(healthReport, c.Name)})
		for _, spec := range specs {
			status, message := "not recorded", ""
			if spec.license {
				collection, found := collections[assessment.LicenseKind]
				status, message = licenseCoverageRow(collection, found, spec.assignments)
				rows = append(rows, coverageRow(data, c, spec.sheet, status, spec.count, message))
				continue
			}
			if spec.kind == "vhealth" {
				status, _, message = healthCoverage(data, c, healthReport)
				rows = append(rows, coverageRow(data, c, spec.sheet, status, spec.count, message))
				continue
			}
			switch {
			case spec.kind == "source":
				switch {
				case c.Source != nil:
					status, spec.count = "success", 1
				case !sourceRecorded:
					status = "not recorded"
					message = "capture predates source identity inventory; no ServiceInstance About record was stored"
				case c.VMStatus != "" && c.VMStatus != "success" && c.VMStatus != "empty":
					status = "failed"
					message = "no ServiceInstance About record was stored: " + nonempty(c.Error, "context did not connect")
				default:
					message = "no ServiceInstance About record was stored for this context"
				}
			case spec.hostConfig && !hostConfigRecorded:
				status = "not recorded"
				message = "capture predates host storage and network inventory"
			case (spec.kind == "vdisk" || spec.kind == "vnetwork") && !devicesRecorded:
				status = "not recorded"
				message = "capture predates per-VM device inventory"
			case (spec.kind == "vcd" || spec.kind == "vusb") && !attachedDevicesRecorded:
				status = "not recorded"
				message = "capture predates CD-ROM and USB device inventory"
			case spec.kind == "vpartition" && !partitionsRecorded:
				status = "not recorded"
				message = "capture predates guest partition inventory"
			case spec.kind == "resourcepool" && !poolsRecorded:
				status = "not recorded"
				message = "capture predates resource pool inventory"
			case spec.kind == "dvswitch" && !dvsRecorded:
				status = "not recorded"
				message = "capture predates distributed switch inventory"
			// Partitions are reported by VMware Tools rather than by vCenter,
			// so a successful VM capture can still leave this tab partial —
			// a powered-off VM, or one without Tools running, contributes
			// nothing. Report how much of the estate answered instead of
			// letting a short tab read as a small estate.
			case spec.kind == "vpartition":
				status = c.VMStatus
				if status == "" {
					status = "not recorded"
				}
				switch {
				case status != "success" && status != "empty":
					message = c.Error
				case counts[c.Name] == 0:
				case partitionVMs[c.Name] == 0:
					status = "partial"
					message = "no VM reported guest filesystems; VMware Tools must be running"
				case partitionVMs[c.Name] < counts[c.Name]:
					status = "partial"
					message = fmt.Sprintf("%d of %d VMs reported guest filesystems; the rest had no running VMware Tools",
						partitionVMs[c.Name], counts[c.Name])
				}
			// Disks and NICs ride along with the VM capture rather than
			// being their own collection pass, so their coverage mirrors the
			// VM collection's status.
			case spec.kind == "vm" || spec.kind == "snapshot" || spec.kind == "vcpu" || spec.kind == "vmemory" || spec.kind == "vtools" || spec.kind == "vdisk" || spec.kind == "vnetwork" || spec.kind == "vcd" || spec.kind == "vusb":
				status = c.VMStatus
				if status == "" {
					status = "not recorded"
				}
				if status != "success" && status != "empty" {
					message = c.Error
				} else if spec.kind == "vtools" && !toolsRecorded {
					// The vTools tab still has one row per VM here (the
					// running-status column predates schema 3), just without
					// a version — say so rather than claiming the tab is
					// unrecorded.
					message = "capture predates VMware Tools version inventory"
				}
			case spec.hostConfig:
				if collection, ok := collections["host"]; ok {
					status, message = collection.Status, collection.Error
				} else {
					status = "not recorded"
				}
			default:
				if collection, ok := collections[spec.kind]; ok {
					status, message = collection.Status, collection.Error
				}
			}
			rows = append(rows, coverageRow(data, c, spec.sheet, status, spec.count, message))
		}
	}
	return rows
}

func healthCoverage(data assessment.ExportData, c assessment.ContextRun, healthReport health.Report) (string, int, string) {
	status := c.VMStatus
	if status == "" {
		status = "not recorded"
	}
	if status != "success" && status != "empty" {
		return status, healthFindingsForContext(healthReport, c.Name), c.Error
	}

	allRules := health.Rules()
	evaluated, notEvaluated := 0, make([]health.RuleStatus, 0)
	unknown := make([]health.RuleStatus, 0)
	disabled := make([]string, 0)
	for _, rule := range healthReport.Rules {
		switch rule.Status {
		case "evaluated":
			evaluated++
		case "not-evaluated":
			notEvaluated = append(notEvaluated, rule)
		case "disabled":
			disabled = append(disabled, rule.Rule)
		}
		for _, blind := range rule.Blind {
			if blind == c.Name {
				unknown = append(unknown, rule)
				break
			}
		}
	}
	if len(healthReport.Rules) == 0 {
		// A zero report must not turn the derived tab into a false success
		// statement when a caller forgot to evaluate it.
		return "not recorded", 0, "health report was not evaluated"
	}
	if len(notEvaluated) > 0 {
		ids := make([]string, 0, len(notEvaluated))
		needs := make([]string, 0, len(notEvaluated))
		seenNeeds := make(map[string]bool)
		for _, rule := range notEvaluated {
			ids = append(ids, rule.Rule)
			if rule.Reason != "" && !seenNeeds[rule.Reason] {
				needs = append(needs, rule.Reason)
				seenNeeds[rule.Reason] = true
			}
		}
		message := fmt.Sprintf("evaluated %d of %d rules; %s need a capture with %s", evaluated, len(allRules), strings.Join(ids, ", "), strings.Join(needs, " and "))
		if len(unknown) > 0 {
			message += fmt.Sprintf("; %d rule(s) also have blind coverage", len(unknown))
		}
		if len(disabled) > 0 {
			message += "; disabled rules: " + strings.Join(disabled, ", ")
		}
		return "partial", healthFindingsForContext(healthReport, c.Name), message
	}
	if len(unknown) > 0 || healthReport.Coverage.RulesUnknown > 0 {
		ids := make([]string, 0, len(unknown))
		for _, rule := range unknown {
			ids = append(ids, rule.Rule)
		}
		if len(ids) == 0 {
			ids = append(ids, "unknown rules")
		}
		message := fmt.Sprintf("%d of %d rules evaluated; %s; coverage is incomplete", evaluated, len(allRules), strings.Join(ids, ", "))
		if len(healthReport.Coverage.BlindContexts) > 0 {
			message += "; blind contexts: " + strings.Join(healthReport.Coverage.BlindContexts, ", ")
		}
		if len(disabled) > 0 {
			message += "; disabled rules: " + strings.Join(disabled, ", ")
		}
		return "partial", healthFindingsForContext(healthReport, c.Name), message
	}
	message := fmt.Sprintf("%d of %d rules evaluated (%s)", evaluated+len(disabled), len(allRules), healthThresholdMessage(healthReport.Thresholds))
	if len(disabled) > 0 {
		message += "; disabled rules: " + strings.Join(disabled, ", ")
	}
	return "success", healthFindingsForContext(healthReport, c.Name), message
}

func healthFindingsForContext(report health.Report, context string) int {
	n := 0
	for _, finding := range report.Findings {
		if finding.Object.Context == context {
			n++
		}
	}
	return n
}

func healthThresholdMessage(thresholds health.Thresholds) string {
	message := fmt.Sprintf("max-snapshot-age=%s, min-datastore-free=%s%%, min-guest-disk-free=%s%%",
		healthDurationFlag(thresholds.SnapshotAge), formatPercent(thresholds.DatastoreFreePct), formatPercent(thresholds.GuestDiskFreePct))
	if thresholds.DatastoreFreeBytes > 0 {
		message += fmt.Sprintf(", min-datastore-free-bytes=%s", humanize.Bytes(int64(thresholds.DatastoreFreeBytes)))
	}
	return message
}

func healthDurationFlag(d time.Duration) string {
	if d > 0 && d%(24*time.Hour) == 0 {
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	}
	if d > 0 && d%time.Hour == 0 {
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return d.String()
}

func formatPercent(value float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), "0"), ".")
}

func inventoryAtLeast(version string, minVersion int) bool {
	value, err := strconv.Atoi(strings.TrimSpace(version))
	return err == nil && value >= minVersion
}

func coverageRow(data assessment.ExportData, c assessment.ContextRun, sheet, status string, count int, message string) []any {
	var finished any
	if !c.FinishedAt.IsZero() {
		finished = c.FinishedAt.UTC()
	}
	return []any{data.Run.ID, data.Run.Label, data.Run.StartedAt.UTC(), finished, string(data.Run.Status), c.Name, c.Endpoint, c.Datacenter, c.VCenterID, sheet, status, count, message}
}

func canonicalData(data assessment.ExportData) assessment.ExportData {
	data.Contexts = append([]assessment.ContextRun(nil), data.Contexts...)
	data.VMs = append([]assessment.ExportVM(nil), data.VMs...)
	data.Resources = append([]assessment.ResourceObservation(nil), data.Resources...)
	sort.SliceStable(data.Contexts, func(i, j int) bool { return data.Contexts[i].Name < data.Contexts[j].Name })
	sort.SliceStable(data.VMs, func(i, j int) bool {
		a, b := data.VMs[i].Observation, data.VMs[j].Observation
		return less(a.Context, a.VM.Datacenter, a.VM.Name, a.VM.ID, b.Context, b.VM.Datacenter, b.VM.Name, b.VM.ID)
	})
	for i := range data.VMs {
		data.VMs[i].Snapshots = append([]vsphere.VMSnapshot(nil), data.VMs[i].Snapshots...)
		data.VMs[i].Observation.VM.Disks = append([]vsphere.VMDisk(nil), data.VMs[i].Observation.VM.Disks...)
		data.VMs[i].Observation.VM.NICs = append([]vsphere.VMNIC(nil), data.VMs[i].Observation.VM.NICs...)
		data.VMs[i].Observation.VM.CDROMs = append([]vsphere.VMCDROM(nil), data.VMs[i].Observation.VM.CDROMs...)
		data.VMs[i].Observation.VM.USBs = append([]vsphere.VMUSB(nil), data.VMs[i].Observation.VM.USBs...)
		for n := range data.VMs[i].Observation.VM.NICs {
			data.VMs[i].Observation.VM.NICs[n].IPv4 = append([]string(nil), data.VMs[i].Observation.VM.NICs[n].IPv4...)
			data.VMs[i].Observation.VM.NICs[n].IPv6 = append([]string(nil), data.VMs[i].Observation.VM.NICs[n].IPv6...)
			sort.Strings(data.VMs[i].Observation.VM.NICs[n].IPv4)
			sort.Strings(data.VMs[i].Observation.VM.NICs[n].IPv6)
		}
		for n := range data.VMs[i].Observation.VM.USBs {
			data.VMs[i].Observation.VM.USBs[n].Family = append([]string(nil), data.VMs[i].Observation.VM.USBs[n].Family...)
			data.VMs[i].Observation.VM.USBs[n].Speed = append([]string(nil), data.VMs[i].Observation.VM.USBs[n].Speed...)
			sort.Strings(data.VMs[i].Observation.VM.USBs[n].Family)
			sort.Strings(data.VMs[i].Observation.VM.USBs[n].Speed)
		}
		sort.SliceStable(data.VMs[i].Observation.VM.Disks, func(a, b int) bool {
			return data.VMs[i].Observation.VM.Disks[a].Key < data.VMs[i].Observation.VM.Disks[b].Key
		})
		sort.SliceStable(data.VMs[i].Observation.VM.NICs, func(a, b int) bool {
			return data.VMs[i].Observation.VM.NICs[a].Key < data.VMs[i].Observation.VM.NICs[b].Key
		})
		sort.SliceStable(data.VMs[i].Observation.VM.CDROMs, func(a, b int) bool {
			x, y := data.VMs[i].Observation.VM.CDROMs[a], data.VMs[i].Observation.VM.CDROMs[b]
			if x.Key != y.Key {
				return x.Key < y.Key
			}
			if !strings.EqualFold(x.Label, y.Label) {
				return strings.ToLower(x.Label) < strings.ToLower(y.Label)
			}
			return x.BackingPath < y.BackingPath
		})
		sort.SliceStable(data.VMs[i].Observation.VM.USBs, func(a, b int) bool {
			x, y := data.VMs[i].Observation.VM.USBs[a], data.VMs[i].Observation.VM.USBs[b]
			if x.Key != y.Key {
				return x.Key < y.Key
			}
			if !strings.EqualFold(x.Label, y.Label) {
				return strings.ToLower(x.Label) < strings.ToLower(y.Label)
			}
			return x.BackingPath < y.BackingPath
		})
		sort.SliceStable(data.VMs[i].Snapshots, func(a, b int) bool {
			x, y := data.VMs[i].Snapshots[a], data.VMs[i].Snapshots[b]
			if !x.CreateTime.Equal(y.CreateTime) {
				return x.CreateTime.Before(y.CreateTime)
			}
			return x.ID < y.ID
		})
	}
	sort.SliceStable(data.Resources, func(i, j int) bool {
		a, b := data.Resources[i], data.Resources[j]
		return less(a.Context, resourceDC(a), a.Name, a.ID, b.Context, resourceDC(b), b.Name, b.ID)
	})
	for i := range data.Resources {
		switch data.Resources[i].Kind {
		case "host":
			var host vsphere.Host
			if err := json.Unmarshal(data.Resources[i].Payload, &host); err != nil {
				continue
			}
			canonicalHost(&host)
			if payload, err := json.Marshal(host); err == nil {
				data.Resources[i].Payload = payload
			}
		case "dvswitch":
			var sw vsphere.DVSwitch
			if err := json.Unmarshal(data.Resources[i].Payload, &sw); err != nil {
				continue
			}
			canonicalDVSwitch(&sw)
			if payload, err := json.Marshal(sw); err == nil {
				data.Resources[i].Payload = payload
			}
		}
	}
	return data
}

func canonicalHost(host *vsphere.Host) {
	if host == nil {
		return
	}
	sort.SliceStable(host.HBAs, func(i, j int) bool {
		if host.HBAs[i].Device != host.HBAs[j].Device {
			return host.HBAs[i].Device < host.HBAs[j].Device
		}
		return host.HBAs[i].Key < host.HBAs[j].Key
	})
	sort.SliceStable(host.NICs, func(i, j int) bool {
		if host.NICs[i].Device != host.NICs[j].Device {
			return host.NICs[i].Device < host.NICs[j].Device
		}
		return host.NICs[i].Key < host.NICs[j].Key
	})
	sort.SliceStable(host.VSwitches, func(i, j int) bool {
		if host.VSwitches[i].Name != host.VSwitches[j].Name {
			return host.VSwitches[i].Name < host.VSwitches[j].Name
		}
		return host.VSwitches[i].Key < host.VSwitches[j].Key
	})
	for i := range host.VSwitches {
		sort.Strings(host.VSwitches[i].Uplinks)
	}
	sort.SliceStable(host.PortGroups, func(i, j int) bool {
		if host.PortGroups[i].Name != host.PortGroups[j].Name {
			return host.PortGroups[i].Name < host.PortGroups[j].Name
		}
		return host.PortGroups[i].Key < host.PortGroups[j].Key
	})
	sort.SliceStable(host.VMKs, func(i, j int) bool {
		if host.VMKs[i].Device != host.VMKs[j].Device {
			return host.VMKs[i].Device < host.VMKs[j].Device
		}
		return host.VMKs[i].Key < host.VMKs[j].Key
	})
	sort.SliceStable(host.Multipaths, func(i, j int) bool {
		if host.Multipaths[i].LUN != host.Multipaths[j].LUN {
			return host.Multipaths[i].LUN < host.Multipaths[j].LUN
		}
		return host.Multipaths[i].Key < host.Multipaths[j].Key
	})
}

func canonicalDVSwitch(sw *vsphere.DVSwitch) {
	if sw == nil {
		return
	}
	sort.Strings(sw.Hosts)
	sort.Strings(sw.UplinkPorts)
	sort.SliceStable(sw.PortGroups, func(i, j int) bool {
		if sw.PortGroups[i].Name != sw.PortGroups[j].Name {
			return sw.PortGroups[i].Name < sw.PortGroups[j].Name
		}
		return sw.PortGroups[i].Key < sw.PortGroups[j].Key
	})
	for i := range sw.PortGroups {
		sort.Strings(sw.PortGroups[i].ActiveUplinks)
		sort.Strings(sw.PortGroups[i].StandbyUplinks)
	}
}

func less(ac, ad, an, ai, bc, bd, bn, bi string) bool {
	for _, pair := range [][2]string{{ac, bc}, {ad, bd}, {strings.ToLower(an), strings.ToLower(bn)}, {ai, bi}} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

func resourceDC(r assessment.ResourceObservation) string {
	var loc struct {
		Datacenter string `json:"datacenter"`
	}
	_ = json.Unmarshal(r.Payload, &loc)
	return loc.Datacenter
}

func contextEndpoint(data assessment.ExportData, name string) string {
	for _, c := range data.Contexts {
		if c.Name == name {
			return c.Endpoint
		}
	}
	return ""
}

func storageMiB(gb float64) any {
	return gb * 1024
}

func hostCPUPercent(h vsphere.Host) any {
	denom := float64(h.TotalCPU())
	if denom <= 0 {
		return nil
	}
	return float64(h.CPUUsageMHz) / denom * 100
}

func hostMemoryPercent(h vsphere.Host) any {
	if h.MemoryMB <= 0 {
		return nil
	}
	return float64(h.MemoryUsageMB) / float64(h.MemoryMB) * 100
}

func nonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func validateResources(resources []assessment.ResourceObservation) error {
	for _, resource := range resources {
		var value any
		switch resource.Kind {
		case "network":
			// Network inventory is consumed by the headless topology graph. The
			// RVTools profile has no standalone persisted-network worksheet;
			// VM NIC and distributed-port-group sheets remain the compatibility
			// representation, so this collection is intentionally ignored here.
			continue
		case "host":
			value = &vsphere.Host{}
		case "cluster":
			value = &vsphere.Cluster{}
		case "datastore":
			value = &vsphere.Datastore{}
		case "resourcepool":
			value = &vsphere.ResourcePool{}
		case "dvswitch":
			value = &vsphere.DVSwitch{}
		case assessment.LicenseKind:
			value = &vsphere.License{}
		default:
			return fmt.Errorf("unsupported persisted resource kind %q", resource.Kind)
		}
		if err := json.Unmarshal(resource.Payload, value); err != nil {
			return fmt.Errorf("malformed persisted %s %q payload: %w", resource.Kind, resource.ID, err)
		}
	}
	return nil
}

func normalizeZip(input []byte) ([]byte, error) {
	r, err := zip.NewReader(bytes.NewReader(input), int64(len(input)))
	if err != nil {
		return nil, err
	}
	files := append([]*zip.File(nil), r.File...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	var out bytes.Buffer
	w := zip.NewWriter(&out)
	epoch := time.Unix(0, 0).UTC()
	for _, file := range files {
		src, err := file.Open()
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		contents, readErr := io.ReadAll(src)
		closeErr := src.Close()
		if readErr != nil {
			_ = w.Close()
			return nil, readErr
		}
		if closeErr != nil {
			_ = w.Close()
			return nil, closeErr
		}
		header := &zip.FileHeader{Name: file.Name, Method: zip.Deflate, Modified: epoch}
		writer, err := w.CreateHeader(header)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		if _, err := writer.Write(contents); err != nil {
			_ = w.Close()
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
