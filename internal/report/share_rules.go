package report

// Sensitivity classifies what a column can reveal about the source estate.
type Sensitivity string

const (
	ClassNone     Sensitivity = "none"
	ClassName     Sensitivity = "name"
	ClassIP       Sensitivity = "ip"
	ClassPath     Sensitivity = "path"
	ClassID       Sensitivity = "id"
	ClassFreeText Sensitivity = "free-text"
	ClassTopology Sensitivity = "topology"
)

// Pseudonym kinds. A kind is both the token prefix and the namespace an
// equality is preserved in: the same original value under the same kind
// always maps to the same token, in every sheet.
const (
	kindVM         = "vm"
	kindHost       = "host"
	kindCluster    = "cluster"
	kindDatacenter = "dc"
	kindDatastore  = "ds"
	kindNetwork    = "net"
	kindSwitch     = "sw"
	kindContext    = "ctx"
	kindEndpoint   = "ep"
	kindName       = "name"
	kindSnapshot   = "snap"
	kindIP         = "ip"
	kindMAC        = "mac"
	kindPath       = "path"
	kindID         = "id"
	kindUUID       = "uuid"
	kindText       = "text"
	kindDevice     = "dev"
)

// colRule declares how one column is handled when pseudonymization is on.
// kind is empty for a column that is always kept as-is. scrub marks
// system-generated free text that is not hashed but has every known
// pseudonymized value replaced in place. list marks a cell holding several
// values joined by ", " or "; ".
type colRule struct {
	class Sensitivity
	kind  string
	list  bool
	scrub bool
}

func keep() colRule                { return colRule{class: ClassNone} }
func topo() colRule                { return colRule{class: ClassTopology} }
func name(kind string) colRule     { return colRule{class: ClassName, kind: kind} }
func id(kind string) colRule       { return colRule{class: ClassID, kind: kind} }
func nameList(kind string) colRule { return colRule{class: ClassName, kind: kind, list: true} }
func text() colRule                { return colRule{class: ClassFreeText, kind: kindText} }
func scrubbed() colRule            { return colRule{class: ClassFreeText, scrub: true} }

var (
	ipRule   = colRule{class: ClassIP, kind: kindIP, list: true}
	pathRule = colRule{class: ClassPath, kind: kindPath}
)

// columnRules is the default rule for a column header. A header that is not
// here and has no sheet override is an error: the exporter never guesses.
var columnRules = map[string]colRule{
	// Identity, location and provenance.
	"VM": name(kindVM), "Host": name(kindHost), "Cluster": name(kindCluster), "Datacenter": name(kindDatacenter),
	"Folder": pathRule, "VM ID": id(kindID), "VM UUID": id(kindUUID), "VM SMBIOS UUID": id(kindUUID),
	"VI SDK Server": name(kindEndpoint), "VI SDK UUID": id(kindUUID), "vsfleet Context": name(kindContext),
	"Context": name(kindContext), "Endpoint": name(kindEndpoint), "vCenter ID": id(kindUUID),
	"Object ID": id(kindID), "Annotation": text(), "Description": text(),

	// vInfo / per-VM tabs.
	"Powerstate": keep(), "Template": keep(), "Guest state": keep(), "CPUs": keep(), "Memory": keep(),
	"Primary IP Address": ipRule, "In Use MiB": keep(), "OS according to the configuration file": keep(),
	"Size MiB": keep(), "Tools": keep(), "Tools Version": keep(), "Tools Version Status": keep(),

	// vDisk / vPartition.
	"Disk": keep(), "Disk Key": keep(), "Disk UUID": id(kindUUID), "Capacity MiB": keep(), "Raw": keep(),
	"Disk Mode": keep(), "Sharing mode": keep(), "Thin": keep(), "Eagerly Scrub": keep(), "Split": keep(),
	"Write Through": keep(), "Level": keep(), "Shares": keep(), "Reservation": keep(), "Limit": keep(),
	"Controller": keep(), "SCSI label": keep(), "Unit number": keep(), "Shared Bus": keep(), "Path": pathRule,
	"Raw LUN ID": id(kindID), "Raw Compatibility Mode": keep(), "Consumed MiB": keep(), "Free MiB": keep(),
	"Free %": keep(), "Filesystem": keep(),

	// vNetwork / vCD / vUSB.
	"NIC label": keep(), "Adapter": keep(), "Network": name(kindNetwork), "Connected": keep(), "Starts Connected": keep(),
	"Mac Address": id(kindMAC), "Mac Address type": keep(), "IPv4 Address": ipRule, "IPv6 Address": ipRule,
	"Direct Path IO": keep(), "Device": keep(), "Device Key": keep(), "Backing type": keep(), "Backing path": pathRule,
	"Backing device": name(kindDevice), "Backing host": name(kindHost), "Backing datastore ID": id(kindID),
	"Backing object ID": id(kindID), "Use auto detect": keep(), "Controller label": keep(), "Vendor ID": keep(),
	"Product ID": keep(), "Family": keep(), "Speed": keep(),

	// vSnapshot.
	"Date / time": keep(), "Quiesced": keep(), "State": keep(),

	// vSource.
	"OS type": keep(), "API type": keep(), "API version": keep(), "Version": keep(), "Patch level": keep(),
	"Build": keep(), "Fullname": keep(), "Product name": keep(), "Product version": keep(), "Product line": keep(),
	"Vendor": keep(),

	// vHost / vHBA / vNIC / vSwitch / vPort.
	"in Maintenance Mode": keep(), "# Cores": keep(), "CPU usage %": keep(), "# Memory": keep(), "Memory usage %": keep(),
	"# VMs total": keep(), "ESX Version": keep(), "Model": keep(), "Bus": keep(), "Status": keep(), "Driver": keep(),
	"Pci": keep(), "PCI": keep(), "Storage protocol": keep(), "WWNN": id(kindID), "WWPN": id(kindID),
	"iSCSI name": id(kindID), "iSCSI alias": name(kindName), "Type": keep(), "Link speed Mb": keep(), "Duplex": keep(),
	"Wake on LAN": keep(), "Switch": name(kindSwitch), "# Ports": keep(), "Free Ports": keep(), "MTU": keep(),
	"Uplinks": topo(), "Promiscuous Mode": keep(), "Mac Changes": keep(), "Forged Transmits": keep(),
	"Traffic Shaping": keep(), "Port Group": name(kindNetwork), "VLAN": topo(),

	// dvSwitch / dvPort / vSC_VMK / vMultiPath.
	"Max Ports": keep(), "UUID": id(kindUUID), "Contact": text(), "Contact detail": text(), "Hosts": nameList(kindHost),
	"Uplink ports": topo(), "Link discovery protocol": keep(), "Link discovery operation": keep(), "LACP version": keep(),
	"Port": name(kindNetwork), "Key": id(kindID), "Uplink": topo(), "Allow Promiscuous": keep(),
	"Policy": keep(), "Notify Switch": keep(), "Rolling Order": keep(), "In Traffic Shaping": keep(),
	"Out Traffic Shaping": keep(), "Blocked": keep(), "Auto expand": keep(), "Active Uplink": topo(),
	"Standby Uplink": topo(), "Logical switch UUID": id(kindUUID), "Segment ID": topo(), "Port group": name(kindNetwork),
	"TSO": keep(), "Netstack": keep(), "DHCP": keep(), "IP Address": ipRule, "Subnet mask": topo(),
	"Service console": keep(), "LUN": id(kindID), "Device path": pathRule, "Local disk": keep(), "Path count": keep(),
	"Active paths": keep(), "Standby paths": keep(), "Dead paths": keep(), "Disabled paths": keep(), "Working paths": keep(),

	// vCluster / vRP / vDatastore.
	"NumHosts": keep(), "numEffectiveHosts": keep(), "TotalCpu": keep(), "NumCpuCores": keep(), "TotalMemory": keep(),
	"HA enabled": keep(), "DRS enabled": keep(), "Resource pool": pathRule, "VMs": keep(), "vCPUs": keep(),
	"CPU limit": keep(), "CPU overheadLimit": keep(), "CPU reservation": keep(), "CPU level": keep(), "CPU shares": keep(),
	"CPU expandableReservation": keep(), "Mem Configured": keep(), "Mem limit": keep(), "Mem overheadLimit": keep(),
	"Mem reservation": keep(), "Mem level": keep(), "Mem shares": keep(), "Mem expandableReservation": keep(),
	"Config status": keep(), "Accessible": keep(), "Maintenance mode": keep(),

	// vFileInfo.
	"Friendly Path Name": pathRule, "File Name": pathRule, "File Type": keep(), "File Size in bytes": keep(),
	"Internal Sort Column": pathRule, "Datastore": name(kindDatastore), "Datastore ID": id(kindID),

	// vHealth.
	"Message": scrubbed(), "Message type": keep(), "Category": keep(), "vsfleet Rule": keep(),
	"Recommendation": scrubbed(), "Evidence": scrubbed(), "Object type": keep(),

	// vsfleetCoverage.
	"Run ID": keep(), "Run label": text(), "Run started": keep(), "Run finished": keep(), "Run status": keep(),
	"Sheet": keep(), "Collection status": keep(), "Item count": keep(), "Error": scrubbed(),

	// vsfleetPerformance.
	"Inventory match": keep(), "Counter": keep(), "Unit": keep(), "Aggregation": keep(), "Window start": keep(),
	"Window end": keep(), "Interval s": keep(), "Expected samples": keep(), "Successful samples": keep(),
	"Missing samples": keep(), "Average": keep(), "Peak": keep(), "P95": keep(), "Reason": scrubbed(),
	"Sizing signal": keep(), "Signal reason": scrubbed(), "Source": keep(),

	// vLicense / vsfleetLicenseAssignment. The key column is already redacted.
	"Labels": text(), "Cost Unit": keep(), "Total": keep(), "Used": keep(), "Expiration Date": keep(),
	"Features": keep(), "License": name(kindName), "Edition key": keep(), "Entity": name(kindName),
	"Entity type": keep(), "Entity ID": id(kindID), "Scope": name(kindName),
}

// sheetRules override columnRules where the same header means something else.
var sheetRules = map[string]colRule{
	"vSource/Name":          keep(),
	"vCluster/Name":         name(kindCluster),
	"vRP/Name":              name(kindName),
	"vDatastore/Name":       name(kindDatastore),
	"vSnapshot/Name":        name(kindSnapshot),
	"vSnapshot/Description": text(),
	"vHealth/Name":          name(kindName),
	"vLicense/Name":         name(kindName),
	"vPartition/Disk":       pathRule,
	"vLicense/Key":          keep(),

	// vsfleetMetadata. Category, attribute and tag names are operator free
	// text and are pseudonymized like annotations; the same name maps to the
	// same token, so grouping by field or value still works. Object IDs share
	// the kindID namespace with "VM ID" and "Object ID" elsewhere, which is
	// what links a row back to its object.
	"vsfleetMetadata/Captured at":     keep(),
	"vsfleetMetadata/Kind":            keep(),
	"vsfleetMetadata/Object name":     name(kindName),
	"vsfleetMetadata/Metadata source": keep(),
	"vsfleetMetadata/Field ID":        id(kindID),
	"vsfleetMetadata/Field":           text(),
	"vsfleetMetadata/Value ID":        id(kindID),
	"vsfleetMetadata/Value":           text(),
	"vsfleetMetadata/Source status":   keep(),
	"vsfleetMetadata/Source error":    scrubbed(),
}

// ruleFor resolves a column's rule and reports whether one exists.
func ruleFor(sheetName, header string) (colRule, bool) {
	if r, ok := sheetRules[sheetName+"/"+header]; ok {
		return r, true
	}
	r, ok := columnRules[header]
	return r, ok
}
