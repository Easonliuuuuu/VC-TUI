package demo

import "fmt"

func numbered(prefix string, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("%s-%02d", prefix, i))
	}
	return out
}

func joinLists(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

func vmfs(names []string, browse ...string) []dsSpec {
	var out []dsSpec
	for _, n := range names {
		out = append(out, dsSpec{name: n, typ: "VMFS", browse: contains(browse, n)})
	}
	return out
}

// prodSite is the Taipei production vCenter: six clusters, 56 hosts and 1000
// virtual machines. The proportions follow a mid-sized enterprise estate — a
// general application tier, a database tier, a VDI farm, management and a DMZ.
func prodSite() siteSpec {
	nvme := numbered("nvme", 1, 8)
	t1 := numbered("san-tier1", 1, 8)
	t2 := numbered("san-tier2", 1, 6)
	s := siteSpec{ctx: "prod-vc", dc: "Taipei", prefix: "10.20", anchor: "prod", fastDS: "san-tier1-03"}
	s.clusters = []clusterSpec{
		{name: "compute-a", letter: "a", hostPrefix: "esxi-a", hosts: 16, vms: 300, role: "app", subnet: 0,
			services:   []string{"api", "web", "auth", "cache", "queue", "search", "batch", "gateway", "worker", "cdn"},
			datastores: joinLists([]string{"vsan-compute-a"}, nvme[:4], t1[:4]),
			pools:      []poolSpec{{"api-pool", []string{"api"}}, {"web-pool", []string{"web", "cdn", "gateway"}}, {"batch-pool", []string{"batch", "worker"}}},
			folder: map[string]string{"*": "/Applications", "api": "/Applications/API", "web": "/Applications/Web/Frontend", "cdn": "/Applications/Web/Frontend",
				"gateway": "/Applications/Web/Gateway", "auth": "/Applications/Security", "cache": "/Applications/Platform/Cache", "queue": "/Applications/Platform/Messaging",
				"search": "/Applications/Platform/Search", "batch": "/Applications/Batch", "worker": "/Applications/Batch"}},
		{name: "compute-b", letter: "b", hostPrefix: "esxi-b", hosts: 12, vms: 240, role: "app", subnet: 1,
			services:   []string{"erp", "crm", "reporting", "etl", "sap-app", "build-runner", "jira", "wiki", "files", "print"},
			datastores: joinLists([]string{"vsan-compute-b"}, nvme[4:], t1[4:]),
			pools:      []poolSpec{{"erp-pool", []string{"erp", "sap-app"}}, {"reporting-pool", []string{"reporting", "etl"}}},
			folder: map[string]string{"*": "/Applications", "erp": "/Applications/ERP", "sap-app": "/Applications/ERP", "crm": "/Applications/CRM",
				"reporting": "/Applications/BI", "etl": "/Applications/BI", "build-runner": "/Platform/CI", "jira": "/Applications/Collaboration",
				"wiki": "/Applications/Collaboration", "files": "/Infrastructure/FileServices", "print": "/Infrastructure/FileServices"}},
		{name: "db-cluster", letter: "d", hostPrefix: "esxi-db", hosts: 8, vms: 120, role: "db", subnet: 2,
			services:   []string{"postgres", "mysql", "mssql", "oracle", "mongo", "redis"},
			datastores: joinLists(t1, t2[:3]),
			pools:      []poolSpec{{"prod-db-pool", []string{"postgres", "mysql", "mssql", "oracle"}}, {"nosql-pool", []string{"mongo", "redis"}}},
			folder: map[string]string{"*": "/Databases", "postgres": "/Databases/PostgreSQL", "mysql": "/Databases/MySQL", "mssql": "/Databases/SQLServer",
				"oracle": "/Databases/Oracle", "mongo": "/Databases/NoSQL", "redis": "/Databases/NoSQL"}},
		{name: "vdi-pool", letter: "v", hostPrefix: "esxi-vdi", hosts: 10, vms: 240, role: "vdi", subnet: 3,
			services:   []string{"vdi-fin", "vdi-hr", "vdi-eng", "vdi-ops"},
			datastores: []string{"vdi-vsan", "vdi-nvme-01", "vdi-nvme-02"},
			pools:      []poolSpec{{"vdi-finance", []string{"vdi-fin"}}, {"vdi-engineering", []string{"vdi-eng"}}, {"vdi-general", []string{"vdi-hr", "vdi-ops"}}},
			folder:     map[string]string{"*": "/VDI/Pools", "vdi-fin": "/VDI/Pools/Finance", "vdi-hr": "/VDI/Pools/HR", "vdi-eng": "/VDI/Pools/Engineering", "vdi-ops": "/VDI/Pools/Operations"}},
		{name: "mgmt", letter: "m", hostPrefix: "esxi-mgmt", hosts: 4, vms: 60, role: "mgmt", subnet: 4,
			services:   []string{"dns", "ntp", "ad-dc", "dhcp", "syslog", "prometheus", "grafana", "backup-proxy", "jump", "vcenter-proxy"},
			datastores: joinLists([]string{"local-esxi-mgmt-01", "local-esxi-mgmt-02"}, t2[3:]),
			pools:      []poolSpec{{"infra-pool", []string{"dns", "ntp", "ad-dc", "dhcp"}}, {"monitoring-pool", []string{"syslog", "prometheus", "grafana"}}},
			folder: map[string]string{"*": "/Infrastructure", "dns": "/Infrastructure/Core", "ntp": "/Infrastructure/Core", "ad-dc": "/Infrastructure/Core", "dhcp": "/Infrastructure/Core",
				"syslog": "/Infrastructure/Monitoring", "prometheus": "/Infrastructure/Monitoring", "grafana": "/Infrastructure/Monitoring"}},
		{name: "dmz-edge", letter: "e", hostPrefix: "esxi-dmz", hosts: 6, vms: 40, role: "dmz", subnet: 5,
			services:   []string{"proxy", "waf", "lb", "mail-gw", "vpn"},
			datastores: []string{"san-tier2-02", "san-tier2-03"},
			folder:     map[string]string{"*": "/DMZ"}},
	}

	ds := vmfs(nvme, "nvme-01", "nvme-02")
	ds[0].sharedUUID = "demo-vmfs-nvme-01"
	ds = append(ds, vmfs(t1, "san-tier1-01", "san-tier1-02")...)
	tier2 := vmfs(t2, "san-tier2-01")
	tier2[2].fill = 0.93 // san-tier2-03 is nearly full: datastore-space-low evidence
	ds = append(ds, tier2...)
	ds = append(ds,
		dsSpec{name: "vsan-compute-a", typ: "vsan"}, dsSpec{name: "vsan-compute-b", typ: "vsan"}, dsSpec{name: "vdi-vsan", typ: "vsan"},
		dsSpec{name: "vdi-nvme-01", typ: "VMFS", browse: true}, dsSpec{name: "vdi-nvme-02", typ: "VMFS"})
	for _, n := range numbered("nfs-archive", 1, 4) {
		ds = append(ds, dsSpec{name: n, typ: "NFS", capTiB: 64, offline: n == "nfs-archive-03"})
	}
	ds = append(ds, dsSpec{name: "nfs-backup-01", typ: "NFS", capTiB: 96, fill: 0.71}, dsSpec{name: "nfs-backup-02", typ: "NFS", capTiB: 96, fill: 0.64},
		dsSpec{name: "iso-library-01", typ: "NFS", capTiB: 4, fill: 0.31},
		dsSpec{name: "local-esxi-mgmt-01", typ: "VMFS", local: true}, dsSpec{name: "local-esxi-mgmt-02", typ: "VMFS", local: true})
	s.datastores = ds

	s.templates = []string{"ubuntu-24.04-golden", "ubuntu-22.04-golden", "rhel-9-golden", "rhel-8-golden", "windows-2025-core", "windows-2022-std", "windows-2019-std",
		"windows-11-vdi", "photon-5-base", "oracle-linux-9", "debian-12-base", "k8s-node-template", "db-postgres-16-template", "sql-2022-template", "vdi-master-eng", "vdi-master-fin"}
	s.portgroups = []pgSpec{{"mgmt-vlan-20", 20}, {"web-vlan-110", 110}, {"frontend-vlan-120", 120}, {"app-vlan-130", 130}, {"api-vlan-140", 140}, {"backend-vlan-240", 240},
		{"db-vlan-250", 250}, {"cache-vlan-260", 260}, {"mq-vlan-270", 270}, {"vdi-vlan-300", 300}, {"vdi-mgmt-vlan-310", 310}, {"dmz-vlan-400", 400}, {"dmz-web-vlan-410", 410},
		{"backup-vlan-500", 500}, {"monitoring-vlan-510", 510}, {"k8s-vlan-600", 600}, {"k8s-storage-vlan-610", 610}, {"voip-vlan-700", 700}, {"guest-vlan-800", 800},
		{"lab-vlan-900", 900}, {"transit-vlan-999", 999},
		// The nested-lab trunk carries every VLAN to the nested hosts, the
		// one trunk the VLAN map lists on its own.
		{"lab-trunk", trunkAll}}
	s.storagePGs = []pgSpec{{"iscsi-a-vlan-2010", 2010}, {"iscsi-b-vlan-2011", 2011}, {"nfs-vlan-2020", 2020}, {"vmotion-vlan-2030", 2030}}
	s.vapps = []vappSpec{
		{"api-stack", "compute-a", "", []string{"api", "gateway", "auth"}, 10, "api-pool"},
		{"api-cache", "compute-a", "api-stack", []string{"cache"}, 6, ""},
		{"empty-vapp", "compute-a", "", nil, 0, ""},
		{"web-frontend", "compute-a", "", []string{"web", "cdn"}, 20, ""},
		{"search-cluster", "compute-a", "", []string{"search"}, 8, ""},
		{"messaging", "compute-a", "", []string{"queue"}, 6, ""},
		{"batch-runners", "compute-a", "", []string{"batch", "worker"}, 12, ""},
		{"sap-prod", "compute-b", "", []string{"sap-app", "erp"}, 14, "erp-pool"},
		{"sap-batch", "compute-b", "sap-prod", []string{"etl"}, 5, ""},
		{"crm-suite", "compute-b", "", []string{"crm"}, 8, ""},
		{"bi-platform", "compute-b", "", []string{"reporting", "etl"}, 10, ""},
		{"ci-farm", "compute-b", "", []string{"build-runner"}, 6, ""},
		{"collab", "compute-b", "", []string{"jira", "wiki"}, 6, ""},
		{"fileservers", "compute-b", "", []string{"files", "print"}, 6, ""},
		{"pg-cluster-a", "db-cluster", "", []string{"postgres"}, 6, ""},
		{"mysql-galera", "db-cluster", "", []string{"mysql"}, 5, ""},
		{"mssql-ag", "db-cluster", "", []string{"mssql"}, 4, ""},
		{"oracle-rac", "db-cluster", "", []string{"oracle"}, 4, ""},
		{"mongo-rs", "db-cluster", "", []string{"mongo"}, 6, ""},
		{"redis-sentinel", "db-cluster", "", []string{"redis"}, 4, ""},
		{"vdi-finance", "vdi-pool", "", []string{"vdi-fin"}, 20, ""},
		{"vdi-engineering", "vdi-pool", "", []string{"vdi-eng"}, 20, ""},
		{"monitoring-stack", "mgmt", "", []string{"prometheus", "grafana", "syslog"}, 6, ""},
		{"identity", "mgmt", "", []string{"ad-dc", "dns", "ntp", "dhcp"}, 8, ""},
	}
	return s
}

// edgeSite is the Hsinchu edge vCenter. Its clusters deliberately reuse the
// production names and a set of well-known VM names, so the duplicate-names
// scenario has real collisions to disambiguate by context.
func edgeSite() siteSpec {
	s := siteSpec{ctx: "edge-vc", dc: "Hsinchu", prefix: "10.42", anchor: "edge"}
	s.clusters = []clusterSpec{
		{name: "compute-a", letter: "a", hostPrefix: "edge-esx-a", hosts: 6, vms: 100, role: "app", subnet: 0,
			services:   []string{"api", "web", "auth", "cache", "gateway"},
			datastores: []string{"nvme-edge-01", "nvme-edge-02", "san-edge-01", "san-edge-02"},
			pools:      []poolSpec{{"api-pool", []string{"api"}}, {"web-pool", []string{"web", "gateway"}}},
			folder:     map[string]string{"*": "/Applications", "api": "/Applications/API", "web": "/Applications/Web/Frontend"}},
		{name: "compute-b", letter: "b", hostPrefix: "edge-esx-b", hosts: 6, vms: 80, role: "app", subnet: 1, fixed: []string{"finance01"},
			services:   []string{"postgres", "build-runner", "erp", "files", "worker", "redis"},
			datastores: []string{"san-prod-01", "nvme-edge-03", "san-edge-03", "san-edge-04", "san-edge-05"},
			pools:      []poolSpec{{"erp-pool", []string{"erp"}}},
			folder:     map[string]string{"*": "/Applications", "postgres": "/Databases/PostgreSQL", "build-runner": "/Platform/CI", "finance": "/Applications/Finance", "fixed": "/Applications/Finance"}},
	}
	ds := []dsSpec{{name: "san-prod-01", typ: "VMFS", browse: true, sharedUUID: "demo-vmfs-nvme-01"}}
	ds = append(ds, vmfs(numbered("nvme-edge", 1, 3))...)
	ds = append(ds, vmfs(numbered("san-edge", 1, 5))...)
	ds = append(ds, dsSpec{name: "nfs-backup-01", typ: "NFS", capTiB: 24, fill: 0.58}, dsSpec{name: "iso-library-01", typ: "NFS", capTiB: 2, fill: 0.4},
		dsSpec{name: "local-esxi-01", typ: "VMFS", local: true})
	s.datastores = ds
	s.templates = []string{"ubuntu-24.04-golden", "ubuntu-22.04-golden", "rhel-9-golden", "windows-2025-core", "windows-2022-std", "photon-5-base", "debian-12-base", "k8s-node-template"}
	s.portgroups = []pgSpec{{"mgmt-vlan-20", 20}, {"web-vlan-110", 110}, {"frontend-vlan-120", 120}, {"app-vlan-130", 130}, {"api-vlan-140", 140}, {"backend-vlan-240", 240},
		{"db-vlan-250", 250}, {"guest-vlan-800", 800}, {"lab-vlan-900", 900}}
	// The edge site named its API network before the naming standard did,
	// so the same VLAN 140 has another name here.
	s.shown = map[string]string{"api-vlan-140": "api-v140"}
	s.storagePGs = []pgSpec{{"iscsi-a-vlan-2010", 2010}, {"vmotion-vlan-2030", 2030}}
	s.vapps = []vappSpec{
		{"api-stack", "compute-a", "", []string{"api", "gateway"}, 8, "api-pool"},
		{"api-cache", "compute-a", "api-stack", []string{"cache"}, 5, ""},
		{"empty-vapp", "compute-a", "", nil, 0, ""},
		{"web-tier", "compute-a", "", []string{"web"}, 10, ""},
		{"erp-suite", "compute-b", "", []string{"erp"}, 8, ""},
		{"ci-farm", "compute-b", "", []string{"build-runner"}, 6, ""},
	}
	return s
}
