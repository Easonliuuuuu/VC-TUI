package network

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/topology"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

const (
	severityBlocker  = "blocker"
	severityAdvisory = "advisory"
)

// Compare compares the networks reachable from two named clusters using only
// persisted assessment evidence. contexts limits cluster resolution and the
// coverage scope; an empty list means every context in the assessment.
func Compare(data assessment.ExportData, source, target string, contexts []string) Comparison {
	result := Comparison{
		SchemaVersion:   inventorySchema(data.Run.InventorySchemaVersion),
		RunID:           data.Run.ID,
		Source:          ClusterRef{Name: strings.TrimSpace(source)},
		Target:          ClusterRef{Name: strings.TrimSpace(target)},
		Matched:         make([]NetworkMatch, 0),
		MappingGaps:     make([]MappingGap, 0),
		TargetOnly:      make([]NetworkSummary, 0),
		Differences:     make([]Difference, 0),
		CheckedContexts: checkedContexts(data.Contexts, contexts),
		Blind:           make([]Blindness, 0),
		Candidates:      make([]ClusterRef, 0),
	}

	graph := topology.Build(data)
	selected := normalizeContexts(contexts)
	sourceSubjects := graph.Resolve(topology.KindCluster, source, contexts)
	targetSubjects := graph.Resolve(topology.KindCluster, target, contexts)

	if len(sourceSubjects) == 1 {
		result.Source = clusterReference(data, sourceSubjects[0])
	}
	if len(targetSubjects) == 1 {
		result.Target = clusterReference(data, targetSubjects[0])
	}
	if len(sourceSubjects) != 1 || len(targetSubjects) != 1 {
		result.Ambiguous = len(sourceSubjects) > 1 || len(targetSubjects) > 1
		if len(sourceSubjects) > 1 {
			for _, subject := range sourceSubjects {
				result.Candidates = append(result.Candidates, clusterReference(data, subject))
			}
		}
		if len(targetSubjects) > 1 {
			for _, subject := range targetSubjects {
				candidate := clusterReference(data, subject)
				if !containsClusterRef(result.Candidates, candidate) {
					result.Candidates = append(result.Candidates, candidate)
				}
			}
		}
		sortClusterRefs(result.Candidates)
		result.Blind = comparisonBlindness(data, selected, []string{"host", "dvswitch"})
		result.Confidence = "unknown"
		return result
	}

	sourceNetworks := reachableNetworks(data, result.Source)
	targetNetworks := reachableNetworks(data, result.Target)
	pairs, sourceOnly, targetOnly := matchNetworks(sourceNetworks, targetNetworks)
	for _, pair := range pairs {
		result.Matched = append(result.Matched, NetworkMatch{Source: pair.source, Target: pair.target, MatchBasis: pair.matchBasis})
		result.Differences = append(result.Differences, networkDifferences(pair)...)
	}
	result.TargetOnly = append(result.TargetOnly, targetOnly...)

	partial := result.SchemaVersion < 12
	for _, sourceNetwork := range sourceOnly {
		attached, resolved := attachedVMs(graph, sourceNetwork, result.Source.Context)
		gap := MappingGap{
			Network:     sourceNetwork,
			AttachedVMs: attached,
			VMsResolved: resolved,
			Severity:    severityAdvisory,
		}
		if len(attached) > 0 {
			gap.Severity = severityBlocker
		}
		if !resolved {
			partial = true
		}
		result.MappingGaps = append(result.MappingGaps, gap)
	}

	// VM collection is required only to prove which guests are affected by a
	// source-only network. DVS and host evidence is required on every side.
	needs := []string{"host", "dvswitch"}
	if len(result.MappingGaps) > 0 {
		needs = append(needs, "vm")
	}
	result.Blind = comparisonBlindness(data, selected, needs)
	if len(result.Blind) > 0 {
		for i := range result.Differences {
			result.Differences[i].Severity = severityAdvisory
		}
		for i := range result.MappingGaps {
			result.MappingGaps[i].Severity = severityAdvisory
		}
		result.Confidence = "unknown"
	} else if partial {
		result.Confidence = "partial"
	} else {
		result.Confidence = "complete"
	}
	return result
}

// Verdict derives the conservative migration decision from a comparison.
func Verdict(comparison Comparison) Readiness {
	out := Readiness{
		Comparison: comparison,
		Verdict:    "ready",
		Blockers:   make([]string, 0),
		Advisories: make([]string, 0),
	}
	unknown := comparison.Confidence == "unknown" || comparison.Ambiguous || !comparison.Source.Resolved || !comparison.Target.Resolved
	if unknown {
		out.Verdict = "unknown"
	}
	for _, gap := range comparison.MappingGaps {
		if len(gap.AttachedVMs) > 0 {
			message := fmt.Sprintf("network %q is absent on target and is used by %s", gap.Network.Name, attachedNames(gap.AttachedVMs))
			if gap.Severity == severityAdvisory {
				out.Advisories = append(out.Advisories, message)
			} else {
				out.Blockers = append(out.Blockers, message)
			}
		} else if len(gap.AttachedVMs) == 0 {
			out.Advisories = append(out.Advisories, fmt.Sprintf("network %q is present on source but has no confirmed attached VM", gap.Network.Name))
		}
	}
	for _, difference := range comparison.Differences {
		message := fmt.Sprintf("network %q %s differs (source: %s, target: %s)", difference.Network, difference.Field, difference.Source, difference.Target)
		if difference.Severity == severityBlocker {
			out.Blockers = append(out.Blockers, message)
		} else {
			out.Advisories = append(out.Advisories, message)
		}
	}
	if !unknown && len(out.Blockers) > 0 {
		out.Verdict = "blocked"
	}
	return out
}

type networkPair struct {
	source     NetworkSummary
	target     NetworkSummary
	matchBasis string
}

// clusterReference turns a topology subject into the stable fields exposed by
// the comparison API. Clusters currently have one member per context, but
// choosing the first sorted member keeps this correct if a future identity
// join adds more observations.
func clusterReference(data assessment.ExportData, subject topology.Subject) ClusterRef {
	ref := ClusterRef{Name: subject.Name, Resolved: true}
	if len(subject.Members) > 0 {
		member := subject.Members[0]
		ref.Context, ref.VCenterID = member.Context, member.VCenterID
	}
	for _, resource := range data.Resources {
		if resource.Kind != "host" || !sameContext(resource.Context, ref.Context) {
			continue
		}
		var host vsphere.Host
		if assessment.DecodeResource(resource, &host) && strings.EqualFold(host.Cluster, ref.Name) {
			ref.HostCount++
		}
	}
	return ref
}

func reachableNetworks(data assessment.ExportData, cluster ClusterRef) []NetworkSummary {
	hosts := make([]vsphere.Host, 0)
	switches := make([]vsphere.DVSwitch, 0)
	for _, resource := range data.Resources {
		if !sameContext(resource.Context, cluster.Context) {
			continue
		}
		switch resource.Kind {
		case "host":
			var host vsphere.Host
			if assessment.DecodeResource(resource, &host) && strings.EqualFold(host.Cluster, cluster.Name) {
				hosts = append(hosts, host)
			}
		case "dvswitch":
			var switchValue vsphere.DVSwitch
			if assessment.DecodeResource(resource, &switchValue) {
				switches = append(switches, switchValue)
			}
		}
	}
	return Reachable(hosts, switches)
}

// Reachable is the networks a cluster's hosts can reach: every standard port
// group on any of them and every port group of every distributed switch,
// each with how many of the hosts have it. hosts are the cluster's hosts with
// their network configuration; switches are the distributed switches of the
// cluster's vCenter. It is the evidence Compare matches, taken from whatever
// source the caller has, a stored assessment or a live read.
func Reachable(hostList []vsphere.Host, switchList []vsphere.DVSwitch) []NetworkSummary {
	type hostRecord struct {
		host vsphere.Host
	}
	hosts := make([]hostRecord, 0, len(hostList))
	for _, host := range hostList {
		hosts = append(hosts, hostRecord{host: host})
	}
	sort.SliceStable(hosts, func(i, j int) bool {
		return objectSortKey(hosts[i].host.Name, hosts[i].host.ID) < objectSortKey(hosts[j].host.Name, hosts[j].host.ID)
	})

	result := make([]NetworkSummary, 0)
	// Standard port groups are aggregated by their stable display identity.
	standard := make(map[string]int)
	for _, item := range hosts {
		for _, pg := range item.host.PortGroups {
			key := networkKey(pg.Name, strconv.FormatInt(int64(pg.VLAN), 10), pg.Switch)
			index, ok := standard[key]
			if !ok {
				summary := NetworkSummary{
					Name:            pg.Name,
					VLAN:            strconv.FormatInt(int64(pg.VLAN), 10),
					ParentSwitch:    pg.Switch,
					Type:            "standard",
					CoveredHosts:    1,
					TotalHosts:      len(hosts),
					Promiscuous:     cloneBool(pg.Promiscuous),
					MACChanges:      cloneBool(pg.MACChanges),
					ForgedTransmits: cloneBool(pg.ForgedTransmits),
				}
				if sw := owningVSwitch(item.host, pg.Switch); sw != nil {
					summary.MTU = sw.MTU
					summary.ActiveUplinks = append([]string(nil), sw.Uplinks...)
				}
				standard[key] = len(result)
				result = append(result, summary)
				continue
			}
			result[index].CoveredHosts++
			mergeStandardEvidence(&result[index], item.host, pg)
		}
	}

	// A distributed switch lists its host membership independently from each
	// host's cluster placement. Match by both host name and managed-object ID.
	switches := make([]struct {
		switchValue vsphere.DVSwitch
	}, 0, len(switchList))
	for _, switchValue := range switchList {
		switches = append(switches, struct {
			switchValue vsphere.DVSwitch
		}{switchValue: switchValue})
	}
	sort.SliceStable(switches, func(i, j int) bool {
		return objectSortKey(switches[i].switchValue.Name, switches[i].switchValue.ID) < objectSortKey(switches[j].switchValue.Name, switches[j].switchValue.ID)
	})
	for _, item := range switches {
		covered := 0
		for _, host := range hosts {
			if switchHasHost(item.switchValue, host.host) {
				covered++
			}
		}
		portGroups := append([]vsphere.DVPortGroup(nil), item.switchValue.PortGroups...)
		sort.SliceStable(portGroups, func(i, j int) bool {
			return objectSortKey(portGroups[i].Name, portGroups[i].ID, portGroups[i].Key) < objectSortKey(portGroups[j].Name, portGroups[j].ID, portGroups[j].Key)
		})
		seen := make(map[string]bool)
		for _, pg := range portGroups {
			identity := pg.ID + "\x00" + pg.Key + "\x00" + pg.Name
			if seen[identity] {
				continue
			}
			seen[identity] = true
			parent := pg.Switch
			if parent == "" {
				parent = item.switchValue.Name
			}
			result = append(result, NetworkSummary{
				Name:            pg.Name,
				VLAN:            pg.VLAN,
				ParentSwitch:    parent,
				Type:            "distributed",
				MTU:             item.switchValue.MaxMTU,
				TeamingPolicy:   pg.TeamingPolicy,
				ActiveUplinks:   append([]string(nil), pg.ActiveUplinks...),
				StandbyUplinks:  append([]string(nil), pg.StandbyUplinks...),
				Promiscuous:     cloneBool(pg.Promiscuous),
				MACChanges:      cloneBool(pg.MACChanges),
				ForgedTransmits: cloneBool(pg.ForgedTransmits),
				CoveredHosts:    covered,
				TotalHosts:      len(hosts),
				Contact:         item.switchValue.Contact,
				ContactDetail:   item.switchValue.ContactDetail,
			})
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return summarySortKey(result[i]) < summarySortKey(result[j]) })
	return result
}

// MatchNetworks pairs each source network with a target network the way
// Compare does: by VLAN first, preferring the same name among networks that
// share one, then by name. It returns the pairs and the networks on one side
// only. Callers with live data should give both sides VLANs in the same form
// (ParseVLAN(...).String()), since an empty VLAN means no VLAN evidence here.
func MatchNetworks(source, target []NetworkSummary) (matched []NetworkMatch, sourceOnly, targetOnly []NetworkSummary) {
	pairs, sourceOnly, targetOnly := matchNetworks(source, target)
	matched = make([]NetworkMatch, 0, len(pairs))
	for _, pair := range pairs {
		matched = append(matched, NetworkMatch{Source: pair.source, Target: pair.target, MatchBasis: pair.matchBasis})
	}
	return matched, sourceOnly, targetOnly
}

// Differences is what differs within a matched pair, with each difference's
// severity as Compare and Verdict judge it.
func Differences(match NetworkMatch) []Difference {
	return networkDifferences(networkPair{source: match.Source, target: match.Target, matchBasis: match.MatchBasis})
}

// GapSeverity is how Compare judges a network found only on the source: a
// blocker when VMs use it, an advisory otherwise.
func GapSeverity(attachedVMs int) string {
	if attachedVMs > 0 {
		return severityBlocker
	}
	return severityAdvisory
}

// IsBlocker reports whether a severity stops a migration.
func IsBlocker(severity string) bool { return severity == severityBlocker }

func matchNetworks(source, target []NetworkSummary) ([]networkPair, []NetworkSummary, []NetworkSummary) {
	used := make([]bool, len(target))
	pairs := make([]networkPair, 0)
	sourceOnly := make([]NetworkSummary, 0)
	for _, left := range source {
		index, basis := findTarget(left, target, used)
		if index < 0 {
			sourceOnly = append(sourceOnly, left)
			continue
		}
		used[index] = true
		pairs = append(pairs, networkPair{source: left, target: target[index], matchBasis: basis})
	}
	targetOnly := make([]NetworkSummary, 0)
	for index, right := range target {
		if !used[index] {
			targetOnly = append(targetOnly, right)
		}
	}
	return pairs, sourceOnly, targetOnly
}

func findTarget(source NetworkSummary, targets []NetworkSummary, used []bool) (int, string) {
	// VLAN is a stronger migration identity than a display name. When several
	// networks share a VLAN, prefer the same name as a deterministic tie-break.
	vlanCandidates := make([]int, 0)
	if strings.TrimSpace(source.VLAN) != "" {
		for index, target := range targets {
			if !used[index] && equal(source.VLAN, target.VLAN) && strings.TrimSpace(target.VLAN) != "" {
				vlanCandidates = append(vlanCandidates, index)
			}
		}
	}
	if len(vlanCandidates) > 0 {
		for _, index := range vlanCandidates {
			if equal(source.Name, targets[index].Name) {
				return index, "vlan"
			}
		}
		return vlanCandidates[0], "vlan"
	}
	for index, target := range targets {
		if !used[index] && equal(source.Name, target.Name) {
			return index, "name"
		}
	}
	return -1, ""
}

func networkDifferences(pair networkPair) []Difference {
	source, target := pair.source, pair.target
	differences := make([]Difference, 0)
	add := func(field, left, right, severity string, different bool) {
		if different {
			differences = append(differences, Difference{Network: source.Name, Field: field, Source: left, Target: right, Severity: severity})
		}
	}
	if pair.matchBasis == "name" {
		add("vlan", source.VLAN, target.VLAN, severityBlocker, !equal(source.VLAN, target.VLAN))
	}
	if source.MTU > 0 && target.MTU > 0 {
		add("mtu", strconv.FormatInt(int64(source.MTU), 10), strconv.FormatInt(int64(target.MTU), 10), severityBlocker, source.MTU != target.MTU)
	}
	if source.TeamingPolicy != "" && target.TeamingPolicy != "" {
		add("teaming_policy", source.TeamingPolicy, target.TeamingPolicy, severityBlocker, source.TeamingPolicy != target.TeamingPolicy)
	}
	if len(source.ActiveUplinks) > 0 && len(target.ActiveUplinks) > 0 {
		add("active_uplinks", strings.Join(source.ActiveUplinks, ","), strings.Join(target.ActiveUplinks, ","), severityAdvisory, !sameStrings(source.ActiveUplinks, target.ActiveUplinks))
	}
	if len(source.StandbyUplinks) > 0 && len(target.StandbyUplinks) > 0 {
		add("standby_uplinks", strings.Join(source.StandbyUplinks, ","), strings.Join(target.StandbyUplinks, ","), severityAdvisory, !sameStrings(source.StandbyUplinks, target.StandbyUplinks))
	}
	if securityDiff(source, target) {
		add("security", securityText(source), securityText(target), severityBlocker, true)
	}
	if source.TotalHosts > 0 && target.TotalHosts > 0 {
		left, right := coverageText(source), coverageText(target)
		// Clusters of different sizes that each reach the network from every
		// host do not differ in any way a migration cares about.
		add("host_coverage", left, right, severityBlocker, source.CoveredHosts < source.TotalHosts || target.CoveredHosts < target.TotalHosts)
	}
	if source.Contact != "" && target.Contact != "" {
		add("contact", source.Contact, target.Contact, severityAdvisory, source.Contact != target.Contact)
	}
	if source.ContactDetail != "" && target.ContactDetail != "" {
		add("contact_detail", source.ContactDetail, target.ContactDetail, severityAdvisory, source.ContactDetail != target.ContactDetail)
	}
	return differences
}

func attachedVMs(graph topology.Graph, network NetworkSummary, context string) ([]AttachedVM, bool) {
	subjects := graph.Resolve(topology.KindNetwork, network.Name, []string{context})
	if len(subjects) != 1 {
		return []AttachedVM{}, false
	}
	result := graph.BlastRadius(subjects[0], 1)
	attached := make([]AttachedVM, 0)
	seen := make(map[string]bool)
	for _, edge := range result.Edges {
		if !strings.EqualFold(edge.From.Kind, string(topology.KindVM)) {
			continue
		}
		key := strings.ToLower(edge.From.Context + "\x00" + edge.From.ID + "\x00" + edge.From.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		attached = append(attached, AttachedVM{Name: edge.From.Name, ID: edge.From.ID, Context: edge.From.Context, VCenterID: edge.From.VCenterID, Confidence: string(edge.Confidence), Basis: string(edge.Basis)})
	}
	sort.SliceStable(attached, func(i, j int) bool {
		return objectSortKey(attached[i].Context, attached[i].Name, attached[i].ID) < objectSortKey(attached[j].Context, attached[j].Name, attached[j].ID)
	})
	return attached, len(result.Unresolved) == 0
}

func comparisonBlindness(data assessment.ExportData, selected, needs []string) []Blindness {
	allowed := make(map[string]bool, len(selected))
	for _, context := range selected {
		allowed[strings.ToLower(context)] = true
	}
	out := make([]Blindness, 0)
	for _, context := range assessment.BlindContexts(data.Contexts, needs) {
		if len(allowed) > 0 && !allowed[strings.ToLower(context)] {
			continue
		}
		out = append(out, Blindness{Context: context, Reason: assessment.CoverageReason(data.Contexts, context, needs)})
	}
	return out
}

func checkedContexts(data []assessment.ContextRun, requested []string) []string {
	if len(requested) > 0 {
		return normalizeContexts(requested)
	}
	contexts := make([]string, 0, len(data))
	for _, context := range data {
		contexts = append(contexts, context.Name)
	}
	return normalizeContexts(contexts)
}

func normalizeContexts(values []string) []string {
	seen := make(map[string]string)
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; !ok {
			seen[key] = value
		}
	}
	out := make([]string, 0, len(seen))
	for _, value := range seen {
		out = append(out, value)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out
}

func owningVSwitch(host vsphere.Host, switchName string) *vsphere.HostVSwitch {
	for i := range host.VSwitches {
		if (switchName != "" && strings.EqualFold(host.VSwitches[i].Key, switchName)) || strings.EqualFold(host.VSwitches[i].Name, switchName) {
			return &host.VSwitches[i]
		}
	}
	return nil
}

func mergeStandardEvidence(summary *NetworkSummary, host vsphere.Host, pg vsphere.HostPortGroup) {
	if sw := owningVSwitch(host, pg.Switch); sw != nil {
		if summary.MTU != sw.MTU {
			summary.MTU = 0
		}
		if !sameStrings(summary.ActiveUplinks, sw.Uplinks) {
			summary.ActiveUplinks = nil
		}
	}
	mergeBool(&summary.Promiscuous, pg.Promiscuous)
	mergeBool(&summary.MACChanges, pg.MACChanges)
	mergeBool(&summary.ForgedTransmits, pg.ForgedTransmits)
}

func mergeBool(dst **bool, value *bool) {
	if *dst == nil || value == nil {
		if *dst != nil && value == nil {
			*dst = nil
		}
		return
	}
	if **dst != *value {
		*dst = nil
	}
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func switchHasHost(switchValue vsphere.DVSwitch, host vsphere.Host) bool {
	for _, member := range switchValue.Hosts {
		if equal(member, host.Name) || (host.ID != "" && equal(member, host.ID)) {
			return true
		}
	}
	return false
}

func securityDiff(source, target NetworkSummary) bool {
	return boolDiff(source.Promiscuous, target.Promiscuous) || boolDiff(source.MACChanges, target.MACChanges) || boolDiff(source.ForgedTransmits, target.ForgedTransmits)
}

func boolDiff(source, target *bool) bool { return source != nil && target != nil && *source != *target }

func securityText(value NetworkSummary) string {
	return "promiscuous=" + boolString(value.Promiscuous) + ",mac_changes=" + boolString(value.MACChanges) + ",forged_transmits=" + boolString(value.ForgedTransmits)
}

func boolString(value *bool) string {
	if value == nil {
		return "unknown"
	}
	return strconv.FormatBool(*value)
}

func coverageText(value NetworkSummary) string {
	return strconv.Itoa(value.CoveredHosts) + "/" + strconv.Itoa(value.TotalHosts)
}

func attachedNames(values []AttachedVM) string {
	names := make([]string, 0, len(values))
	for _, value := range values {
		names = append(names, value.Name)
	}
	return strings.Join(names, ", ")
}

func networkKey(name, vlan, parent string) string {
	return strings.ToLower(strings.Join([]string{name, vlan, parent}, "\x00"))
}

func summarySortKey(value NetworkSummary) string {
	return objectSortKey(value.Name, value.VLAN, value.ParentSwitch, value.Type)
}

func objectSortKey(values ...string) string {
	for index := range values {
		values[index] = strings.ToLower(strings.TrimSpace(values[index]))
	}
	return strings.Join(values, "\x00")
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !equal(left[index], right[index]) {
			return false
		}
	}
	return true
}

func equal(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func sameContext(left, right string) bool { return equal(left, right) }

func containsClusterRef(values []ClusterRef, wanted ClusterRef) bool {
	for _, value := range values {
		if equal(value.Name, wanted.Name) && equal(value.Context, wanted.Context) {
			return true
		}
	}
	return false
}

func sortClusterRefs(values []ClusterRef) {
	sort.SliceStable(values, func(i, j int) bool {
		return objectSortKey(values[i].Name, values[i].Context, values[i].VCenterID) < objectSortKey(values[j].Name, values[j].Context, values[j].VCenterID)
	})
}

func inventorySchema(value string) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return n
}
