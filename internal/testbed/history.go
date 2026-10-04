package testbed

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// seedHistory creates useful history on first launch. The observations are
// collected from the live local simulators once, then older states are derived
// from those observations so IDs and paths line up with a later TUI capture.
func (l *Lab) seedHistory(ctx context.Context) error {
	store, err := assessment.Open(l.HistoryPath)
	if err != nil {
		return err
	}
	defer store.Close()
	runs, err := store.Runs(ctx)
	if err != nil {
		return err
	}
	if len(runs) > 0 {
		return nil
	}

	manager := NewSeedManager()
	defer manager.Close(context.Background())
	current := make(map[string]assessment.ContextResult)
	for _, cc := range l.contexts[:3] {
		result, err := collectContext(ctx, manager, cc)
		if err != nil {
			return fmt.Errorf("seed %s history: %w", cc.Name, err)
		}
		current[cc.Name] = result
	}

	now := time.Now().UTC().Truncate(time.Millisecond)
	baseline := cloneResults(current)
	for name, result := range baseline {
		result.VMs = trimVMs(result.VMs, 8)
		baseline[name] = result
	}
	deployment := cloneResults(current)
	for name, result := range deployment {
		if len(result.VMs) > 0 {
			result.VMs[0].VM.Annotation = "deployment wave 2"
			result.VMs[0].VM.PowerState = "poweredOff"
		}
		result.VMs = trimVMs(result.VMs, 4)
		deployment[name] = result
	}
	preMaintenance := cloneResults(current)
	if result := preMaintenance["prod-vc"]; len(result.VMs) > 0 {
		result.VMs = trimVMs(result.VMs, 1)
		preMaintenance["prod-vc"] = result
	}

	if err := saveSeedRun(ctx, store, now.Add(-72*time.Hour), "baseline", "testbed", false, healthyContexts(l.contexts), baseline); err != nil {
		return err
	}
	if err := saveSeedRun(ctx, store, now.Add(-48*time.Hour), "deployment", "testbed", false, healthyContexts(l.contexts), deployment); err != nil {
		return err
	}
	partial := cloneResults(current)
	partial["edge-vc"] = failedResult("edge-vc", "simulated edge maintenance window")
	partialContexts := append([]*config.Context(nil), l.contexts[:3]...)
	if err := saveSeedRun(ctx, store, now.Add(-24*time.Hour), "edge-outage", "testbed", false, partialContexts, partial); err != nil {
		return err
	}
	if err := saveSeedRun(ctx, store, now.Add(-2*time.Hour), "pre-maintenance", "testbed", true, healthyContexts(l.contexts), preMaintenance); err != nil {
		return err
	}
	return nil
}

func collectContext(ctx context.Context, manager *session.Manager, cc *config.Context) (assessment.ContextResult, error) {
	result := assessment.ContextResult{Name: cc.Name, Status: "failed"}
	opCtx, cancel, tracker := manager.Operation(ctx)
	defer cancel()
	opts := manager.ConnectOptions
	opts.Credential = &credentials.Credential{Username: FixtureUsername, Password: FixturePassword}
	if cc.Transport.Username != "" {
		opts.ProxyCredential = &credentials.Credential{Username: FixtureProxyUser, Password: FixtureProxyPassword}
	}
	client, err := vsphere.Connect(opCtx, cc, opts)
	if err != nil {
		return result, manager.TimeoutError(err, tracker)
	}
	defer client.Close(context.Background())
	result.VCenterID = client.About.InstanceID
	if result.VCenterID == "" {
		result.VCenterID = cc.Endpoint
	}
	idx, err := client.NewIndex(opCtx)
	if err != nil {
		return result, err
	}
	for _, group := range []vsphere.FetchGroup{vsphere.GroupVMs, vsphere.GroupHosts, vsphere.GroupClusters, vsphere.GroupResourcePools, vsphere.GroupVApps, vsphere.GroupDVSwitches, vsphere.GroupDatastores} {
		var part *vsphere.Inventory
		if group == vsphere.GroupHosts {
			part = client.FetchGroupWith(opCtx, idx, group, vsphere.FetchOptions{HostConfig: true})
		} else {
			part = client.FetchGroup(opCtx, idx, group)
		}
		switch group {
		case vsphere.GroupVMs:
			collection := assessment.CollectionResult{Kind: "vm", Status: "success", ItemCount: len(part.VMs)}
			for _, vm := range part.VMs {
				result.VMs = append(result.VMs, assessment.Observation{VCenterID: result.VCenterID, Context: cc.Name, VM: vm})
			}
			if len(part.VMs) == 0 {
				collection.Status = "empty"
			}
			result.Collections = append(result.Collections, collection)
		case vsphere.GroupHosts:
			result.Collections = append(result.Collections, hostCollection("host", result.VCenterID, cc.Name, part.Hosts))
		case vsphere.GroupClusters:
			result.Collections = append(result.Collections, clusterCollection("cluster", result.VCenterID, cc.Name, part.Clusters))
		case vsphere.GroupResourcePools:
			result.Collections = append(result.Collections, resourceCollection("resourcepool", result.VCenterID, cc.Name, part.ResourcePools, func(v vsphere.ResourcePool) string { return v.ID }, func(v vsphere.ResourcePool) string { return v.Name }))
		case vsphere.GroupVApps:
			result.Collections = append(result.Collections, resourceCollection("vapp", result.VCenterID, cc.Name, part.VApps, func(v vsphere.VApp) string { return v.ID }, func(v vsphere.VApp) string { return v.Name }))
		case vsphere.GroupDVSwitches:
			result.Collections = append(result.Collections, resourceCollection("dvswitch", result.VCenterID, cc.Name, part.DVSwitches, func(v vsphere.DVSwitch) string { return v.ID }, func(v vsphere.DVSwitch) string { return v.Name }))
		case vsphere.GroupDatastores:
			result.Collections = append(result.Collections, datastoreCollection("datastore", result.VCenterID, cc.Name, part.Datastores))
		}
	}
	result.Status = "success"
	return result, nil
}

func resourceCollection[T any](kind, vcenter, contextName string, values []T, id func(T) string, name func(T) string) assessment.CollectionResult {
	result := assessment.CollectionResult{Kind: kind, Status: "success", ItemCount: len(values)}
	if len(values) == 0 {
		result.Status = "empty"
	}
	for _, value := range values {
		payload, _ := json.Marshal(value)
		result.Resources = append(result.Resources, assessment.ResourceObservation{VCenterID: vcenter, Context: contextName, Kind: kind, ID: id(value), Name: name(value), Payload: payload})
	}
	return result
}

func hostCollection(kind, vcenter, contextName string, values []vsphere.Host) assessment.CollectionResult {
	return resourceCollection(kind, vcenter, contextName, values, func(v vsphere.Host) string { return v.ID }, func(v vsphere.Host) string { return v.Name })
}
func clusterCollection(kind, vcenter, contextName string, values []vsphere.Cluster) assessment.CollectionResult {
	return resourceCollection(kind, vcenter, contextName, values, func(v vsphere.Cluster) string { return v.ID }, func(v vsphere.Cluster) string { return v.Name })
}
func datastoreCollection(kind, vcenter, contextName string, values []vsphere.Datastore) assessment.CollectionResult {
	return resourceCollection(kind, vcenter, contextName, values, func(v vsphere.Datastore) string { return v.ID }, func(v vsphere.Datastore) string { return v.Name })
}

func saveSeedRun(ctx context.Context, store *assessment.Store, at time.Time, label, source string, pinned bool, contexts []*config.Context, results map[string]assessment.ContextResult) error {
	run, err := store.StartRunWithMetadata(ctx, source, contexts, at, assessment.RunMetadata{Label: label, Pinned: pinned, InventorySchemaVersion: assessment.CurrentInventorySchemaVersion})
	if err != nil {
		return err
	}
	for _, cc := range contexts {
		result, ok := results[cc.Name]
		if !ok {
			result = failedResult(cc.Name, "testbed seed did not produce a result")
		}
		if err := store.SaveContext(ctx, run.ID, result, at.Add(time.Minute)); err != nil {
			return err
		}
	}
	_, err = store.FinishRun(ctx, run.ID, at.Add(2*time.Minute))
	return err
}

func healthyContexts(contexts []*config.Context) []*config.Context {
	return append([]*config.Context(nil), contexts[:3]...)
}

func failedResult(name, message string) assessment.ContextResult {
	result := assessment.ContextResult{Name: name, Status: "failed", Error: message}
	for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore"} {
		result.Collections = append(result.Collections, assessment.CollectionResult{Kind: kind, Status: "failed", Error: message})
	}
	return result
}

func trimVMs(values []assessment.Observation, remove int) []assessment.Observation {
	if remove <= 0 || len(values) <= remove {
		return append([]assessment.Observation(nil), values...)
	}
	return append([]assessment.Observation(nil), values[:len(values)-remove]...)
}

func cloneResults(in map[string]assessment.ContextResult) map[string]assessment.ContextResult {
	out := make(map[string]assessment.ContextResult, len(in))
	for name, result := range in {
		b, _ := json.Marshal(result)
		var clone assessment.ContextResult
		_ = json.Unmarshal(b, &clone)
		out[name] = clone
	}
	return out
}
