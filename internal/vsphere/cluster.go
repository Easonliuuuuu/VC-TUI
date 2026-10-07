package vsphere

import (
	"context"
	"sort"
	"strings"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

var computeResourceKinds = []string{"ComputeResource", "ClusterComputeResource"}

var clusterProps = []string{"name", "parent", "summary", "host", "customValue", "overallStatus", "configIssue", "triggeredAlarmState"}

// ListClusters returns the clusters in a vCenter. A host that is not in a
// cluster appears as a standalone compute resource and is reported with
// Standalone set, because operators still need to see where it lives.
func (c *Client) ListClusters(ctx context.Context) ([]Cluster, error) {
	idx, err := newIndex(ctx, c)
	if err != nil {
		return nil, err
	}
	return c.listClusters(ctx, idx)
}

func (c *Client) listClusters(ctx context.Context, idx *index) ([]Cluster, error) {
	var raw []mo.ComputeResource
	if err := retrieve(ctx, c, idx.root, computeResourceKinds, []string{"ComputeResource"}, clusterProps, &raw); err != nil {
		return nil, err
	}
	settings, err := c.clusterSettings(ctx, idx)
	if err != nil {
		return nil, err
	}
	out := make([]Cluster, 0, len(raw))
	refs := make([]types.ManagedObjectReference, 0, len(raw))
	values := make(map[types.ManagedObjectReference][]types.BaseCustomFieldValue, len(raw))
	for i := range raw {
		refs = append(refs, raw[i].Self)
		values[raw[i].Self] = raw[i].CustomValue
	}
	metadata := c.collectMetadata(ctx, refs, values)
	alarmNames := c.alarmNames(ctx, raw)
	for i := range raw {
		m := &raw[i]
		cl := Cluster{
			Location:   idx.locate(c, m.Self, m.Name),
			ID:         m.Self.Value,
			Name:       m.Name,
			Standalone: m.Self.Type != "ClusterComputeResource",
			// The compute resource summary is authoritative when the server
			// fills it in; the member list is the fallback.
			Hosts: len(m.Host),
		}
		cl.OverallStatus = string(m.OverallStatus)
		cl.ConfigIssues = issueMessages(m.ConfigIssue)
		cl.Alarms = triggeredAlarms(idx, m.TriggeredAlarmState, alarmNames)
		if st, ok := settings[m.Self]; ok {
			cl.DRSEnabled = st.drs
			cl.HAEnabled = st.ha
			if st.drs {
				cl.DRSBehavior = st.drsBehavior
			}
			cl.HA = st.haConfig
		}
		applyComputeSummary(&cl, m.Summary)
		// vCenter lists only the alarms on objects the account can see, so
		// a member host it cannot see leaves the list incomplete.
		cl.AlarmsRead = !hidesHosts(idx, m.Host, cl.Hosts)
		cl.Metadata = metadata[m.Self]
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// applyComputeSummary copies capacity from the summary through the base
// interface: a ClusterComputeResource returns *ClusterComputeResourceSummary,
// which embeds ComputeResourceSummary, so asserting the concrete base type
// would match standalone hosts only.
func applyComputeSummary(cl *Cluster, summary types.BaseComputeResourceSummary) {
	if summary == nil {
		return
	}
	if p, ok := summary.(*types.ComputeResourceSummary); ok && p == nil {
		return
	}
	if p, ok := summary.(*types.ClusterComputeResourceSummary); ok && p == nil {
		return
	}
	s := summary.GetComputeResourceSummary()
	if s == nil {
		return
	}
	cl.CPUCores = int32(s.NumCpuCores)
	cl.TotalCPUMHz = int64(s.TotalCpu)
	cl.TotalMemoryMB = s.TotalMemory / (1 << 20)
	cl.EffectiveHost = int(s.NumEffectiveHosts)
	cl.EffectiveCPUMHz = int64(s.EffectiveCpu)
	cl.EffectiveMemoryMB = s.EffectiveMemory
	if s.NumHosts > 0 {
		cl.Hosts = int(s.NumHosts)
	}
	cs, ok := summary.(*types.ClusterComputeResourceSummary)
	if !ok {
		return
	}
	cl.EVCMode = cs.CurrentEVCModeKey
	cl.DRSScore = cs.DrsScore
	if cl.HA == nil {
		return
	}
	switch info := cs.AdmissionControlInfo.(type) {
	case *types.ClusterFailoverLevelAdmissionControlInfo:
		if info != nil {
			cl.HA.CurrentFailoverLevel = info.CurrentFailoverLevel
		}
	case *types.ClusterFailoverResourcesAdmissionControlInfo:
		if info != nil {
			cl.HA.CPUFailoverPct = info.CurrentCpuFailoverResourcesPercent
			cl.HA.MemFailoverPct = info.CurrentMemoryFailoverResourcesPercent
		}
	}
	if cl.HA.Policy == HAPolicyHostFailures && cl.HA.CurrentFailoverLevel == 0 {
		cl.HA.CurrentFailoverLevel = cs.CurrentFailoverLevel
	}
}

// issueMessages renders a managed entity's configIssue events as the
// sentences the vSphere Client shows for them.
func issueMessages(events []types.BaseEvent) []string {
	var out []string
	for _, e := range events {
		if e == nil {
			continue
		}
		ev := e.GetEvent()
		if ev == nil {
			continue
		}
		if msg := strings.TrimSpace(ev.FullFormattedMessage); msg != "" {
			out = append(out, msg)
		}
	}
	return out
}

// alarmNames resolves the alarm definitions the clusters' triggered alarms
// point at. It is best effort: an alarm whose definition cannot be read is
// still counted, under its managed object ID.
func (c *Client) alarmNames(ctx context.Context, raw []mo.ComputeResource) map[types.ManagedObjectReference]string {
	seen := map[types.ManagedObjectReference]bool{}
	var refs []types.ManagedObjectReference
	for i := range raw {
		for _, st := range raw[i].TriggeredAlarmState {
			if !seen[st.Alarm] {
				seen[st.Alarm] = true
				refs = append(refs, st.Alarm)
			}
		}
	}
	if len(refs) == 0 {
		return nil
	}
	var alarms []mo.Alarm
	if err := property.DefaultCollector(c.VIM()).Retrieve(ctx, refs, []string{"info.name"}, &alarms); err != nil {
		return nil
	}
	out := make(map[types.ManagedObjectReference]string, len(alarms))
	for i := range alarms {
		out[alarms[i].Self] = alarms[i].Info.Name
	}
	return out
}

// hidesHosts reports whether a cluster has member hosts the index never saw:
// fewer host references than the summary counts, or references the account
// cannot read.
func hidesHosts(idx *index, hosts []types.ManagedObjectReference, count int) bool {
	if len(hosts) < count {
		return true
	}
	for _, h := range hosts {
		if _, ok := idx.byRef[h]; !ok {
			return true
		}
	}
	return false
}

// triggeredAlarms turns a triggeredAlarmState into alarms, critical before
// warning. vSphere lists only alarms that are not green; a gray state is
// unknown, not a finding, and is left out too.
func triggeredAlarms(idx *index, states []types.AlarmState, names map[types.ManagedObjectReference]string) []Alarm {
	var out []Alarm
	for _, st := range states {
		status := string(st.OverallStatus)
		if status != "red" && status != "yellow" {
			continue
		}
		name := strings.TrimSpace(names[st.Alarm])
		if name == "" {
			name = st.Alarm.Value
		}
		entity := idx.name(&st.Entity)
		if entity == "" {
			entity = st.Entity.Value
		}
		out = append(out, Alarm{
			Name: name, Status: status, Entity: entity, EntityType: st.Entity.Type,
			Acknowledged: st.Acknowledged != nil && *st.Acknowledged,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Status == "red" && out[j].Status != "red"
	})
	return out
}

type clusterSetting struct {
	drs, ha     bool
	drsBehavior string
	haConfig    *ClusterHA
}

// clusterSettings reads DRS and HA configuration, which only exists on the
// cluster subtype and so cannot be part of the ComputeResource property set.
func (c *Client) clusterSettings(ctx context.Context, idx *index) (map[types.ManagedObjectReference]clusterSetting, error) {
	var raw []mo.ClusterComputeResource
	err := retrieve(ctx, c, idx.root, []string{"ClusterComputeResource"}, []string{"ClusterComputeResource"}, []string{"configurationEx"}, &raw)
	if err != nil {
		return nil, err
	}
	out := make(map[types.ManagedObjectReference]clusterSetting, len(raw))
	for i := range raw {
		cfg, ok := raw[i].ConfigurationEx.(*types.ClusterConfigInfoEx)
		if !ok || cfg == nil {
			continue
		}
		var s clusterSetting
		if cfg.DrsConfig.Enabled != nil {
			s.drs = *cfg.DrsConfig.Enabled
		}
		s.drsBehavior = string(cfg.DrsConfig.DefaultVmBehavior)
		if cfg.DasConfig.Enabled != nil {
			s.ha = *cfg.DasConfig.Enabled
		}
		if s.ha {
			s.haConfig = haConfig(idx, &cfg.DasConfig)
		}
		out[raw[i].Self] = s
	}
	return out, nil
}

// haConfig reads the HA settings that decide whether the cluster survives a
// host failure. Admission control defaults to on when the server omits it,
// as the vSphere API documents.
func haConfig(idx *index, das *types.ClusterDasConfigInfo) *ClusterHA {
	ha := &ClusterHA{
		HostMonitoring:   das.HostMonitoring,
		VMMonitoring:     das.VmMonitoring,
		AdmissionControl: das.AdmissionControlEnabled == nil || *das.AdmissionControlEnabled,
	}
	switch p := das.AdmissionControlPolicy.(type) {
	case *types.ClusterFailoverLevelAdmissionControlPolicy:
		if p != nil {
			ha.Policy, ha.FailoverLevel = HAPolicyHostFailures, p.FailoverLevel
		}
	case *types.ClusterFailoverResourcesAdmissionControlPolicy:
		if p != nil {
			ha.Policy = HAPolicyResources
			ha.CPUReservePct, ha.MemReservePct = p.CpuFailoverResourcesPercent, p.MemoryFailoverResourcesPercent
			ha.FailoverLevel = p.FailoverLevel
		}
	case *types.ClusterFailoverHostAdmissionControlPolicy:
		if p != nil {
			ha.Policy = HAPolicyFailoverHosts
			ha.FailoverHosts = idx.names(p.FailoverHosts)
			ha.FailoverLevel = p.FailoverLevel
		}
	}
	if ha.Policy == "" && das.FailoverLevel > 0 {
		ha.Policy, ha.FailoverLevel = HAPolicyHostFailures, das.FailoverLevel
	}
	return ha
}
