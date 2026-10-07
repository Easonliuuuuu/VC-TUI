package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/humanize"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func fieldValue(fields []field, label string) (string, bool) {
	for _, f := range fields {
		if f.label == label && !f.heading() {
			return f.value, true
		}
	}
	return "", false
}

func TestClusterFieldsJudgeFailoverCapacity(t *testing.T) {
	c := vsphere.Cluster{
		Name: "compute", Hosts: 4, EffectiveHost: 3, HAEnabled: true, DRSEnabled: true, DRSBehavior: "manual",
		OverallStatus: "red", ConfigIssues: []string{"Insufficient vSphere HA failover resources", "second"},
		HA: &vsphere.ClusterHA{
			HostMonitoring: "enabled", VMMonitoring: "vmMonitoringDisabled", AdmissionControl: true,
			Policy: vsphere.HAPolicyResources, CPUReservePct: 25, MemReservePct: 25, CPUFailoverPct: 40, MemFailoverPct: 11,
		},
	}
	fields, marks := clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "Config issues"); v != "2 · Insufficient vSphere HA failover resources (+1 more)" {
		t.Fatalf("Config issues = %q", v)
	}
	if v, _ := fieldValue(fields, "HA failover"); v != "CPU 40% · memory 11% free for failover" {
		t.Fatalf("HA failover = %q", v)
	}
	for label, want := range map[string]rowStatus{"Status": statusBad, "Config issues": statusBad, "HA failover": statusBad, "DRS": statusWarn} {
		if marks[label].status != want {
			t.Fatalf("%s mark = %+v, want status %v", label, marks[label], want)
		}
	}
	if v, _ := fieldValue(fields, "VM monitoring"); v != "off" {
		t.Fatalf("VM monitoring = %q", v)
	}

	c.HA = &vsphere.ClusterHA{AdmissionControl: false, Policy: vsphere.HAPolicyHostFailures, FailoverLevel: 1, CurrentFailoverLevel: 1}
	fields, marks = clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "HA failover"); v != "tolerates 1 host failure (configured 1)" {
		t.Fatalf("HA failover = %q", v)
	}
	if marks["HA failover"].status != statusGood || marks["Admission control"].status != statusWarn {
		t.Fatalf("marks = %+v", marks)
	}
	if v, _ := fieldValue(fields, "Admission control"); v != "off" {
		t.Fatalf("Admission control = %q", v)
	}
}

func TestClusterFieldsDoNotInventEvidence(t *testing.T) {
	// An older capture or an RVTools import has none of the health fields:
	// they read as unknown, never as "none" or "off".
	c := vsphere.Cluster{Name: "compute", Hosts: 2, EffectiveHost: 2, HAEnabled: true, DRSEnabled: true}
	fields, marks := clusterFields(c, clusterMembers{})
	for _, label := range []string{"Status", "Config issues", "Alarms", "EVC mode", "Host states", "VMs", "Datastores"} {
		if v, ok := fieldValue(fields, label); !ok || v != "-" {
			t.Fatalf("%s = %q, want -", label, v)
		}
	}
	if len(marks) != 0 {
		t.Fatalf("unread cluster carries marks: %+v", marks)
	}
}

func TestClusterFieldsSummarizeAlarms(t *testing.T) {
	c := vsphere.Cluster{Name: "compute", OverallStatus: "red", AlarmsRead: true}
	fields, marks := clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "Alarms"); v != "none" {
		t.Fatalf("Alarms = %q, want none", v)
	}
	if _, ok := marks["Alarms"]; ok {
		t.Fatalf("no alarms carries a mark: %+v", marks["Alarms"])
	}

	c.Alarms = []vsphere.Alarm{
		{Name: "Host memory usage", Status: "red", Entity: "esxi-a-03", EntityType: "HostSystem"},
		{Name: "Datastore usage on disk", Status: "yellow", Entity: "ds-01"},
		{Name: "vSphere HA failover in progress", Status: "yellow", Entity: "compute"},
	}
	fields, marks = clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "Alarms"); v != "1 critical · 2 warning · Host memory usage on esxi-a-03" {
		t.Fatalf("Alarms = %q", v)
	}
	if marks["Alarms"].status != statusBad {
		t.Fatalf("critical alarm mark = %+v", marks["Alarms"])
	}

	// An alarm on the cluster itself does not name the cluster again.
	c.Alarms = c.Alarms[2:]
	fields, marks = clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "Alarms"); v != "1 warning · vSphere HA failover in progress" {
		t.Fatalf("Alarms = %q", v)
	}
	if marks["Alarms"].status != statusWarn {
		t.Fatalf("warning alarm mark = %+v", marks["Alarms"])
	}

	// An account that cannot see every host gets an incomplete list: what
	// it shows is still evidence, but its absence is not "none".
	c.AlarmsRead = false
	fields, _ = clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "Alarms"); v != "1 warning · vSphere HA failover in progress · some hosts not visible" {
		t.Fatalf("Alarms = %q", v)
	}
	c.Alarms = nil
	fields, _ = clusterFields(c, clusterMembers{})
	if v, _ := fieldValue(fields, "Alarms"); v != "-" {
		t.Fatalf("Alarms = %q, want -", v)
	}
}

func TestClusterFieldsDoNotCompareHostUseWithEffectiveCapacity(t *testing.T) {
	c := vsphere.Cluster{
		Name: "compute", TotalCPUMHz: 20000, EffectiveCPUMHz: 15000,
		TotalMemoryMB: 20412, EffectiveMemoryMB: 15204,
	}
	members := clusterMembers{hosts: []vsphere.Host{{
		CPUUsageMHz: 16000, MemoryUsageMB: 16520,
	}}}

	fields, marks := clusterFields(c, members)
	for label, want := range map[string]string{
		"Effective CPU":    humanize.MHz(c.EffectiveCPUMHz),
		"Effective memory": humanize.MB(c.EffectiveMemoryMB),
	} {
		if got, ok := fieldValue(fields, label); !ok || got != want {
			t.Fatalf("%s = %q, want %q", label, got, want)
		}
		if _, ok := marks[label]; ok {
			t.Fatalf("%s has a utilization mark from host-wide usage", label)
		}
	}
	for _, label := range []string{"CPU", "Memory"} {
		if got, ok := fieldValue(fields, label); !ok || !strings.Contains(got, "%") {
			t.Fatalf("%s = %q, want overall utilization", label, got)
		}
	}
}

func TestStandaloneHostHasNoClusterSections(t *testing.T) {
	c := vsphere.Cluster{Name: "esx-edge-01", Standalone: true, Hosts: 1, EffectiveHost: 1, OverallStatus: "green"}
	fields, _ := clusterFields(c, clusterMembers{})
	for _, label := range []string{"Resilience", "HA", "DRS", "EVC mode"} {
		for _, f := range fields {
			if f.label == label {
				t.Fatalf("standalone host shows %q", label)
			}
		}
	}
}

func TestCoverageTellsGapsFromLocalDisks(t *testing.T) {
	hosts := []vsphere.Host{
		{Name: "a", Datastores: []string{"shared", "local-a"}},
		{Name: "b", Datastores: []string{"shared", "partial"}},
		{Name: "c", Datastores: []string{"shared", "partial"}},
	}
	cov := coverage(hosts)
	if cov[0].name != "partial" || !cov[0].gap() || strings.Join(cov[0].missing, ",") != "a" {
		t.Fatalf("gap should sort first and name the host without it: %+v", cov)
	}
	for _, d := range cov {
		if d.name == "local-a" && (d.gap() || !d.local()) {
			t.Fatalf("a datastore on one host is local, not a gap: %+v", d)
		}
		if d.name == "shared" && (d.gap() || d.local()) {
			t.Fatalf("a datastore on every host is neither: %+v", d)
		}
	}
}

func TestDatastoreHostsNamesTheHostWithoutIt(t *testing.T) {
	inv := &vsphere.Inventory{Hosts: []vsphere.Host{
		{Name: "esx-1", Cluster: "prod", Datastores: []string{"ds"}},
		{Name: "esx-2", Cluster: "prod", Datastores: []string{"ds"}},
		{Name: "esx-3", Cluster: "prod", Datastores: []string{"other"}},
	}}
	v, mark, marked := datastoreHosts(vsphere.Datastore{Name: "ds"}, inv)
	if v != "2 of 3 in prod · not on esx-3" || !marked || mark.status != statusWarn {
		t.Fatalf("datastoreHosts = %q %+v %v", v, mark, marked)
	}
	if v, _, marked := datastoreHosts(vsphere.Datastore{Name: "ds"}, &vsphere.Inventory{Hosts: []vsphere.Host{{Name: "esx-1"}}}); v != "-" || marked {
		t.Fatalf("unread mounts = %q %v, want - unmarked", v, marked)
	}
}

func TestClusterPaneHeadingsAreNotCursorStops(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "4", "enter")
	r, ok := m.detailRow()
	if !ok || r.cluster == nil {
		t.Fatalf("cluster pane not open: %+v", r)
	}
	for i := 0; i < len(r.detail)+2; i++ {
		press(t, m, "j")
		if idx := m.detailCursor - 2; idx >= 0 && r.detail[idx].heading() {
			t.Fatalf("cursor stopped on heading %q", r.detail[idx].label)
		}
	}
}

func TestClusterPagesShowHostsAndStorage(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "4", "enter")
	view := ansi.Strip(m.View())
	for _, want := range []string{"[0 Summary]", "Health (live)", "Resilience", "Contents"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Summary is missing %q:\n%s", want, view)
		}
	}

	press(t, m, "1")
	view = ansi.Strip(m.View())
	if !strings.Contains(view, "[1 Hosts & VMs]") || !strings.Contains(view, "esxi-01") || strings.Contains(view, "app-01") {
		t.Fatalf("Hosts & VMs should list the host folded:\n%s", view)
	}
	press(t, m, " ")
	if view = ansi.Strip(m.View()); !strings.Contains(view, "app-01") {
		t.Fatalf("space should unfold the host's VMs:\n%s", view)
	}

	press(t, m, "2")
	if view = ansi.Strip(m.View()); !strings.Contains(view, "mounts were not read") {
		t.Fatalf("Storage should say mounts are unknown, not empty:\n%s", view)
	}

	// Reopening the pane from the table starts on Summary again.
	press(t, m, "esc", "enter")
	if m.clusterState(mustRow(t, m)).page != 0 {
		t.Fatalf("reopened pane should start on Summary")
	}
}

func TestClusterHostsPageOpensTheHost(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	press(t, m, "4", "enter", "1", "enter")
	if m.mode != modeBrowse || m.kind != vsphere.KindHost || m.filter.Value() != "esxi-01" {
		t.Fatalf("enter on a host should show it in the Hosts tab: mode %v kind %v filter %q", m.mode, m.kind, m.filter.Value())
	}
}

func mustRow(t *testing.T, m *Model) row {
	t.Helper()
	r, ok := m.detailRow()
	if !ok {
		t.Fatal("no detail row")
	}
	return r
}
