package assessment

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/session"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

type Collector struct {
	Store   *Store
	Manager *session.Manager
}

// Service is the shared historical API consumed by both front ends.
type Service struct {
	Store     *Store
	Collector *Collector
}

func (s *Service) Runs(ctx context.Context) ([]Run, error) { return s.Store.Runs(ctx) }
func (s *Service) ContextRuns(ctx context.Context, runID int64) ([]ContextRun, error) {
	return s.Store.ContextRuns(ctx, runID)
}
func (s *Service) Resources(ctx context.Context, runID int64, kind string) ([]ResourceObservation, error) {
	return s.Store.Resources(ctx, runID, kind)
}
func (s *Service) Diff(ctx context.Context, base, target int64, runtime bool) (Diff, error) {
	return s.Store.Diff(ctx, base, target, runtime)
}
func (s *Service) DiffForContexts(ctx context.Context, base, target int64, runtime bool, contexts []string) (Diff, error) {
	return s.Store.DiffForContexts(ctx, base, target, runtime, contexts)
}
func (s *Service) SnapshotAges(ctx context.Context, run int64, older time.Duration) ([]SnapshotAge, error) {
	return s.Store.SnapshotAges(ctx, run, older)
}
func (s *Service) DeleteRun(ctx context.Context, id int64) error { return s.Store.DeleteRun(ctx, id) }
func (s *Service) History(ctx context.Context, query, contextName string) ([]VMHistoryEntry, error) {
	return s.Store.History(ctx, query, contextName)
}
func (s *Service) Timeline(ctx context.Context, query, contextName string, includeUnchanged, includeRuntime bool) ([]VMHistoryEvent, error) {
	return s.Store.Timeline(ctx, query, contextName, includeUnchanged, includeRuntime)
}
func (s *Service) ChurnTrend(ctx context.Context, opts TrendOptions) (ChurnTrend, error) {
	return s.Store.ChurnTrend(ctx, opts)
}
func (s *Service) SnapshotTrend(ctx context.Context, opts TrendOptions, olderThan time.Duration) (SnapshotTrend, error) {
	return s.Store.SnapshotTrend(ctx, opts, olderThan)
}
func (s *Service) CapacityTrend(ctx context.Context, opts TrendOptions, kinds []string) (CapacityTrend, error) {
	return s.Store.CapacityTrend(ctx, opts, kinds)
}

func (s *Service) CapacityReport(ctx context.Context, opts TrendOptions, thresholds CapacityThresholds) (CapacityReport, error) {
	return s.Store.CapacityReport(ctx, opts, thresholds)
}
func (s *Service) Report(ctx context.Context, runID int64, olderThan time.Duration) (AssessmentReport, error) {
	return s.Store.Report(ctx, runID, olderThan)
}

// LatestExportDataForContext returns the newest finished assessment that
// recorded the named context and, when supplied, the same vCenter identity.
// The complete run is loaded because datastore ownership can be established
// by a VM observed in another context sharing the same backing store.
func (s *Service) LatestExportDataForContext(ctx context.Context, contextName, vcenterID string) (ExportData, error) {
	if s == nil || s.Store == nil {
		return ExportData{}, fmt.Errorf("historical assessments are unavailable")
	}
	runs, err := s.Store.Runs(ctx)
	if err != nil {
		return ExportData{}, err
	}
	for _, run := range runs {
		contexts, err := s.Store.ContextRuns(ctx, run.ID)
		if err != nil {
			return ExportData{}, err
		}
		for _, recorded := range contexts {
			if !strings.EqualFold(recorded.Name, strings.TrimSpace(contextName)) {
				continue
			}
			if strings.TrimSpace(vcenterID) != "" && !strings.EqualFold(recorded.VCenterID, strings.TrimSpace(vcenterID)) {
				continue
			}
			return s.Store.LoadExportData(ctx, run.ID)
		}
	}
	return ExportData{}, fmt.Errorf("no stored assessment for context %q", contextName)
}

// CanCapture reports whether this service can create new assessments. A
// store-only service — the demo's seeded history, or any read-only consumer —
// can read, diff and report on assessments but has no collector to run a live
// capture, so callers must not advertise or attempt one.
func (s *Service) CanCapture() bool {
	return s != nil && s.Collector != nil
}

func (s *Service) Capture(ctx context.Context, opts CaptureOptions) (Run, error) {
	if !s.CanCapture() {
		return Run{}, fmt.Errorf("assessment collector is not configured")
	}
	return s.Collector.Capture(ctx, opts)
}

type CaptureOptions struct {
	Contexts               []*config.Context
	Source                 string
	Label                  string
	Note                   string
	Pinned                 bool
	ToolVersion            string
	InventorySchemaVersion string
	BrowseDatastores       bool
	// IncludeLicenses opts in to read-only license metadata collection. It is
	// off by default because it needs an extra privilege and reads a
	// credential-adjacent API; license keys are never stored either way.
	IncludeLicenses bool
	Now             func() time.Time
	Progress        func(ContextProgress)
}

// Capture creates one immutable run while preserving the session manager's
// partial-success behavior. Every context has its own timeout and commit.
func (c *Collector) Capture(ctx context.Context, opts CaptureOptions) (Run, error) {
	if c.Store == nil || c.Manager == nil {
		return Run{}, fmt.Errorf("assessment collector is not configured")
	}
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	if strings.TrimSpace(opts.InventorySchemaVersion) == "" {
		opts.InventorySchemaVersion = CurrentInventorySchemaVersion
	}
	lease, err := c.Store.AcquireCaptureLease(ctx, now())
	if err != nil {
		return Run{}, err
	}
	defer c.Store.ReleaseCaptureLease(context.Background(), lease)
	run, err := c.Store.StartRunWithMetadata(ctx, opts.Source, opts.Contexts, now(), RunMetadata{Label: opts.Label, Note: opts.Note, Pinned: opts.Pinned, ToolVersion: opts.ToolVersion, InventorySchemaVersion: opts.InventorySchemaVersion, Licenses: opts.IncludeLicenses})
	if err != nil {
		return Run{}, err
	}
	if err := c.Store.SetCaptureLeaseRun(ctx, lease, run.ID); err != nil {
		return Run{}, err
	}
	leaseCtx, stopLease := context.WithCancel(context.Background())
	defer stopLease()
	var leaseWG sync.WaitGroup
	leaseWG.Add(1)
	go func() {
		defer leaseWG.Done()
		t := time.NewTicker(leaseHeartbeat)
		defer t.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case tick := <-t.C:
				_, _ = c.Store.RenewCaptureLease(context.Background(), lease, tick.UTC())
			}
		}
	}()
	var wg sync.WaitGroup
	sem := make(chan struct{}, session.DefaultConcurrency)
	for _, cc := range opts.Contexts {
		cc := cc
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if opts.Progress != nil {
				opts.Progress(ContextProgress{Context: cc.Name, Status: "connecting"})
			}
			result := c.captureContext(ctx, cc, opts.BrowseDatastores, opts.IncludeLicenses)
			if err := c.Store.SaveContextWithLease(ctx, run.ID, result, now(), lease); err != nil {
				result.Status = "failed"
				result.Error = fmt.Sprintf("save assessment: %v", err)
			}
			if opts.Progress != nil {
				p := ContextProgress{Context: cc.Name, Status: result.Status, VMs: len(result.VMs), Error: errorFrom(result.Error)}
				for _, collection := range result.Collections {
					p.Collections = append(p.Collections, CollectionProgress{Kind: collection.Kind, Status: collection.Status, ItemCount: collection.ItemCount, Error: errorFrom(collection.Error)})
				}
				opts.Progress(p)
			}
		}()
	}
	wg.Wait()
	run, err = c.Store.FinishRunWithLease(ctx, run.ID, now(), lease)
	stopLease()
	leaseWG.Wait()
	return run, err
}

func errorFrom(s string) error {
	if s == "" {
		return nil
	}
	return fmt.Errorf("%s", s)
}

func (c *Collector) captureContext(parent context.Context, cc *config.Context, browseDatastores, includeLicenses bool) ContextResult {
	r := ContextResult{Name: cc.Name, Status: "failed"}
	opCtx, cancel, tracker := c.Manager.Operation(parent)
	defer cancel()
	s, err := c.Manager.Connect(opCtx, cc)
	if err != nil {
		r.Error = c.Manager.TimeoutError(err, tracker).Error()
		return r
	}
	client := s.Client()
	if client == nil {
		r.Error = "context is not connected"
		return r
	}
	r.VCenterID = client.About.InstanceID
	r.Source = SourceInfoFromAbout(client.About)
	// Real vCenters expose About.InstanceUuid. Keep the ledger comparable for
	// compatible endpoints (and deterministic test backends) that omit it by
	// falling back to the configured endpoint rather than an empty identity.
	if r.VCenterID == "" {
		r.VCenterID = cc.Endpoint
	}
	idx, err := client.NewIndex(opCtx)
	if err != nil {
		r.Error = c.Manager.TimeoutError(err, tracker).Error()
		kinds := persistedKinds
		if includeLicenses {
			kinds = append(append([]string(nil), kinds...), LicenseKind)
		}
		for _, kind := range kinds {
			r.Collections = append(r.Collections, CollectionResult{Kind: kind, Status: "failed", Error: r.Error})
		}
		return r
	}
	// A single index is reused for all groups. Groups are intentionally
	// sequential within one vCenter to keep API load predictable; contexts are
	// still collected concurrently by Capture.
	for _, group := range []vsphere.FetchGroup{vsphere.GroupVMs, vsphere.GroupHosts, vsphere.GroupClusters, vsphere.GroupResourcePools, vsphere.GroupDVSwitches, vsphere.GroupDatastores, vsphere.GroupNetworks} {
		var part *vsphere.Inventory
		if group == vsphere.GroupDatastores {
			part = client.FetchGroupWith(opCtx, idx, group, vsphere.FetchOptions{BrowseDatastoreFiles: browseDatastores})
		} else if group == vsphere.GroupHosts {
			part = client.FetchGroupWith(opCtx, idx, group, vsphere.FetchOptions{HostConfig: true})
		} else {
			part = client.FetchGroup(opCtx, idx, group)
		}
		switch group {
		case vsphere.GroupVMs:
			itemCount := len(part.VMs) + len(part.Templates)
			collection := CollectionResult{Kind: "vm", ItemCount: itemCount}
			// snapshot is recorded as its own persisted kind, not because it is
			// fetched separately (it rides along on the same VM property
			// collection), but so coverage-gated consumers can tell "this run
			// never looked at snapshots" (an import with no vSnapshot worksheet)
			// apart from "this run looked and found none".
			snapshotCollection := CollectionResult{Kind: "snapshot"}
			if msg, failed := part.ErrorFor(vsphere.KindVM); failed {
				collection.Status, collection.Error = "failed", msg
				snapshotCollection.Status, snapshotCollection.Error = "failed", msg
			} else {
				collection.Status = "success"
				if itemCount == 0 {
					collection.Status = "empty"
				}
				snapshotCount := 0
				for _, vm := range part.VMs {
					r.VMs = append(r.VMs, Observation{VCenterID: r.VCenterID, Context: cc.Name, VM: vm})
					snapshotCount += len(vm.Snapshots)
				}
				for _, vm := range part.Templates {
					r.VMs = append(r.VMs, Observation{VCenterID: r.VCenterID, Context: cc.Name, VM: vm})
					snapshotCount += len(vm.Snapshots)
				}
				collection.TagsStatus, collection.CustomAttributesStatus = metadataStatuses(part.VMs, part.Templates)
				snapshotCollection.Status = "success"
				if snapshotCount == 0 {
					snapshotCollection.Status = "empty"
				}
				snapshotCollection.ItemCount = snapshotCount
			}
			r.Collections = append(r.Collections, collection, snapshotCollection)
		case vsphere.GroupHosts:
			r.Collections = append(r.Collections, resourceCollection("host", r.VCenterID, cc.Name, part.Hosts, part.ErrorFor))
		case vsphere.GroupClusters:
			r.Collections = append(r.Collections, resourceCollection("cluster", r.VCenterID, cc.Name, part.Clusters, part.ErrorFor))
		case vsphere.GroupResourcePools:
			r.Collections = append(r.Collections, resourceCollection("resourcepool", r.VCenterID, cc.Name, part.ResourcePools, part.ErrorFor))
		case vsphere.GroupDVSwitches:
			r.Collections = append(r.Collections, resourceCollection("dvswitch", r.VCenterID, cc.Name, part.DVSwitches, part.ErrorFor))
		case vsphere.GroupDatastores:
			r.Collections = append(r.Collections, resourceCollection("datastore", r.VCenterID, cc.Name, part.Datastores, part.ErrorFor))
		case vsphere.GroupNetworks:
			r.Collections = append(r.Collections, resourceCollection("network", r.VCenterID, cc.Name, part.Networks, part.ErrorFor))
		}
	}
	if includeLicenses {
		r.Collections = append(r.Collections, licenseCollection(opCtx, client, r.VCenterID, cc.Name))
	}
	for _, collection := range r.Collections {
		if collection.Status == "failed" {
			r.Error = strings.TrimSpace(strings.Join([]string{r.Error, collection.Kind + ": " + collection.Error}, "; "))
		}
	}
	for _, collection := range r.Collections {
		if collection.Kind == "vm" {
			r.Status = collection.Status
			break
		}
	}
	return r
}

func resourceCollection[T any](kind, vcenter, contextName string, values []T, errorFor func(vsphere.Kind) (string, bool)) CollectionResult {
	collection := CollectionResult{Kind: kind, Status: "success", ItemCount: len(values)}
	if msg, failed := errorFor(vsphere.Kind(kind)); failed {
		collection.Status, collection.Error = "failed", msg
		return collection
	}
	if len(values) == 0 {
		collection.Status = "empty"
	}
	collection.TagsStatus, collection.CustomAttributesStatus = metadataStatuses(values)
	for _, value := range values {
		payload, err := json.Marshal(value)
		if err != nil {
			collection.Status, collection.Error = "failed", err.Error()
			continue
		}
		var id, name string
		switch v := any(value).(type) {
		case vsphere.Host:
			id, name = v.ID, v.Name
		case vsphere.Cluster:
			id, name = v.ID, v.Name
		case vsphere.ResourcePool:
			id, name = v.ID, v.Name
		case vsphere.Datastore:
			id, name = v.ID, v.Name
		case vsphere.DVSwitch:
			id, name = v.ID, v.Name
		case vsphere.Network:
			id, name = v.ID, v.Name
		case vsphere.VApp:
			id, name = v.ID, v.Name
		}
		collection.Resources = append(collection.Resources, ResourceObservation{VCenterID: vcenter, Context: contextName, Kind: kind, ID: id, Name: name, Payload: payload})
	}
	return collection
}

func metadataStatuses(values ...any) (string, string) {
	if len(values) == 0 {
		return "unavailable", "unavailable"
	}
	var tagStatus, customStatus string
	var seen bool
	var visit func(any)
	visit = func(value any) {
		switch v := value.(type) {
		case []vsphere.VM:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.Host:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.Cluster:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.VApp:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.Datastore:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.Network:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.ResourcePool:
			for _, item := range v {
				visit(item)
			}
		case []vsphere.DVSwitch:
			for _, item := range v {
				visit(item)
			}
		case vsphere.VM:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.Host:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.Cluster:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.VApp:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.Datastore:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.Network:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.ResourcePool:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		case vsphere.DVSwitch:
			if !seen {
				tagStatus, customStatus, seen = v.Metadata.TagsStatus, v.Metadata.CustomAttributesStatus, true
			}
		}
	}
	for _, value := range values {
		visit(value)
	}
	if tagStatus == "" {
		tagStatus = "unavailable"
	}
	if customStatus == "" {
		customStatus = "unavailable"
	}
	return tagStatus, customStatus
}
