package assessment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// ExportVM is the complete, persisted VM observation used by offline
// exporters. Snapshots are loaded from the normalized snapshot table rather
// than requiring another vCenter request.
type ExportVM struct {
	Observation Observation
	Snapshots   []vsphere.VMSnapshot
}

// ExportData is a consistent point-in-time view of one stored assessment.
// It intentionally contains only data already present in the history ledger.
type ExportData struct {
	Run       Run
	Contexts  []ContextRun
	VMs       []ExportVM
	Resources []ResourceObservation
	// Performance holds the newest usable performance window for each
	// context, matched by vCenter identity. Unlike the rest of ExportData it
	// is not part of the run: it was collected separately, possibly long
	// after or before the capture, and carries its own window dates. A context
	// with no window has no entry, which readers must treat as "not
	// collected", never as idle.
	Performance []perf.Window
}

// LoadExportData reads all evidence for a finished assessment in one read
// transaction. The method never touches configuration, credentials, sessions,
// or the collector, making it safe for completely offline exports.
func (s *Store) LoadExportData(ctx context.Context, runID int64) (ExportData, error) {
	return s.LoadExportDataForContexts(ctx, runID, nil)
}

// LoadExportDataForContexts loads one run and scopes its persisted evidence to
// the requested stored contexts. An empty selector list keeps the full run.
func (s *Store) LoadExportDataForContexts(ctx context.Context, runID int64, selectors []string) (ExportData, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ExportData{}, err
	}
	defer tx.Rollback()

	run, err := getRunTx(ctx, tx, runID)
	if err != nil {
		return ExportData{}, err
	}
	if run.Status == RunRunning || run.FinishedAt.IsZero() {
		return ExportData{}, fmt.Errorf("assessment %d is still running", run.ID)
	}
	if run.Status != RunComplete && run.Status != RunPartial && run.Status != RunFailed {
		return ExportData{}, fmt.Errorf("assessment %d has unsupported status %q", run.ID, run.Status)
	}

	contexts, contextIDs, err := loadExportContexts(ctx, tx, runID)
	if err != nil {
		return ExportData{}, err
	}
	vms, err := loadExportVMs(ctx, tx, contextIDs)
	if err != nil {
		return ExportData{}, err
	}
	resources, err := loadExportResources(ctx, tx, runID)
	if err != nil {
		return ExportData{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExportData{}, err
	}
	sortExportData(contexts, vms, resources)
	data := ExportData{Run: run, Contexts: contexts, VMs: vms, Resources: resources}
	data, err = ScopeExportDataChecked(data, selectors)
	if err != nil {
		return ExportData{}, err
	}
	data.Performance, err = s.loadExportPerformance(ctx, data.Contexts)
	return data, err
}

// loadExportPerformance finds each context's newest usable performance window.
// Windows are matched by vCenter identity where the run recorded one, so a
// renamed context still finds its history.
func (s *Store) loadExportPerformance(ctx context.Context, contexts []ContextRun) ([]perf.Window, error) {
	var out []perf.Window
	seen := map[int64]bool{}
	for _, c := range contexts {
		w, found, err := s.LatestPerfWindow(ctx, c.Name, c.VCenterID)
		if err != nil {
			return nil, err
		}
		if !found || seen[w.ID] {
			continue
		}
		seen[w.ID] = true
		out = append(out, w)
	}
	return out, nil
}

// ScopeExportData returns an independent view containing only the requested
// contexts. It is used by offline queries before they build a graph so an
// unscoped object or relationship cannot leak into a context-scoped result.
// An empty context list means all contexts, matching the history query
// contract.
func ScopeExportData(data ExportData, contexts []string) ExportData {
	scoped, err := ScopeExportDataChecked(data, contexts)
	if err != nil {
		return ExportData{}
	}
	return scoped
}

// ScopeExportDataChecked is the validating form used by CLI entry points.
func ScopeExportDataChecked(data ExportData, contexts []string) (ExportData, error) {
	selected, err := ValidateStoredContexts(contexts, data.Contexts)
	if err != nil {
		return ExportData{}, err
	}
	if len(selected) == 0 {
		return data, nil
	}
	allowed := make(map[string]bool, len(selected))
	for _, contextName := range selected {
		allowed[contextName] = true
	}
	scoped := data
	scoped.Contexts = make([]ContextRun, 0, len(data.Contexts))
	for _, contextRun := range data.Contexts {
		if allowed[strings.ToLower(contextRun.Name)] {
			scoped.Contexts = append(scoped.Contexts, contextRun)
		}
	}
	scoped.VMs = make([]ExportVM, 0, len(data.VMs))
	for _, item := range data.VMs {
		if allowed[strings.ToLower(item.Observation.Context)] {
			scoped.VMs = append(scoped.VMs, item)
		}
	}
	scoped.Performance = make([]perf.Window, 0, len(data.Performance))
	scopedVCenters := map[string]bool{}
	for _, c := range scoped.Contexts {
		if c.VCenterID != "" {
			scopedVCenters[c.VCenterID] = true
		}
	}
	for _, w := range data.Performance {
		if allowed[strings.ToLower(w.Context)] || (w.VCenterID != "" && scopedVCenters[w.VCenterID]) {
			scoped.Performance = append(scoped.Performance, w)
		}
	}
	scoped.Resources = make([]ResourceObservation, 0, len(data.Resources))
	for _, resource := range data.Resources {
		if allowed[strings.ToLower(resource.Context)] {
			scoped.Resources = append(scoped.Resources, resource)
		}
	}
	return scoped, nil
}

func getRunTx(ctx context.Context, tx *sql.Tx, id int64) (Run, error) {
	var r Run
	var start, finish sql.NullInt64
	var pinned int
	err := tx.QueryRowContext(ctx, `SELECT id,source,label,note,pinned,tool_version,inventory_schema_version,started_at,finished_at,status,requested_contexts,successful_contexts,requested_collections,successful_collections FROM runs WHERE id=?`, id).
		Scan(&r.ID, &r.Source, &r.Label, &r.Note, &pinned, &r.ToolVersion, &r.InventorySchemaVersion, &start, &finish, &r.Status, &r.RequestedContexts, &r.SuccessfulContexts, &r.RequestedCollections, &r.SuccessfulCollections)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("assessment %d not found", id)
	}
	if err != nil {
		return Run{}, err
	}
	r.StartedAt, r.FinishedAt = fromMillis(start), fromMillis(finish)
	r.Pinned = pinned != 0
	return r, nil
}

func loadExportContexts(ctx context.Context, tx *sql.Tx, runID int64) ([]ContextRun, map[int64]ContextRun, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,name,endpoint,datacenter,vcenter_id,started_at,finished_at,vm_status,error FROM context_runs WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	byID := make(map[int64]ContextRun)
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		var c ContextRun
		var start, finish sql.NullInt64
		if err := rows.Scan(&id, &c.Name, &c.Endpoint, &c.Datacenter, &c.VCenterID, &start, &finish, &c.VMStatus, &c.Error); err != nil {
			return nil, nil, err
		}
		c.RunID = runID
		c.StartedAt, c.FinishedAt = fromMillis(start), fromMillis(finish)
		byID[id] = c
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	collectionRows, err := tx.QueryContext(ctx, `SELECT context_run_id,kind,started_at,finished_at,status,error,item_count,tags_status,tags_error,custom_attributes_status,custom_attributes_error FROM context_collections WHERE context_run_id IN (SELECT id FROM context_runs WHERE run_id=?) ORDER BY context_run_id,kind`, runID)
	if err != nil {
		return nil, nil, err
	}
	defer collectionRows.Close()
	for collectionRows.Next() {
		var contextID int64
		var c CollectionRun
		var start, finish sql.NullInt64
		if err := collectionRows.Scan(&contextID, &c.Kind, &start, &finish, &c.Status, &c.Error, &c.ItemCount, &c.TagsStatus, &c.TagsError, &c.CustomAttributesStatus, &c.CustomAttributesError); err != nil {
			return nil, nil, err
		}
		c.StartedAt, c.FinishedAt = fromMillis(start), fromMillis(finish)
		if contextRun, ok := byID[contextID]; ok {
			contextRun.Collections = append(contextRun.Collections, c)
			byID[contextID] = contextRun
		}
	}
	if err := collectionRows.Err(); err != nil {
		return nil, nil, err
	}
	contexts := make([]ContextRun, 0, len(ids))
	for _, id := range ids {
		contexts = append(contexts, byID[id])
	}
	return contexts, byID, nil
}

func loadExportVMs(ctx context.Context, tx *sql.Tx, contextIDs map[int64]ContextRun) ([]ExportVM, error) {
	ids := make([]int64, 0, len(contextIDs))
	for id := range contextIDs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []ExportVM
	for _, contextID := range ids {
		rows, err := tx.QueryContext(ctx, `SELECT id,moref,instance_uuid,bios_uuid,payload FROM vm_observations WHERE context_run_id=? ORDER BY id`, contextID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var observationID int64
			var moref, instanceUUID, biosUUID string
			var payload []byte
			if err := rows.Scan(&observationID, &moref, &instanceUUID, &biosUUID, &payload); err != nil {
				rows.Close()
				return nil, err
			}
			var vm vsphere.VM
			if err := json.Unmarshal(payload, &vm); err != nil {
				rows.Close()
				return nil, fmt.Errorf("assessment %d VM %q has malformed payload: %w", contextIDs[contextID].RunID, moref, err)
			}
			vm.ID, vm.InstanceUUID, vm.BIOSUUID = moref, instanceUUID, biosUUID
			snapshots, err := loadExportSnapshots(ctx, tx, observationID)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if len(snapshots) == 0 {
				snapshots = append([]vsphere.VMSnapshot(nil), vm.Snapshots...)
			}
			c := contextIDs[contextID]
			vm.Location.Context = c.Name
			if vm.Location.Datacenter == "" {
				vm.Location.Datacenter = c.Datacenter
			}
			out = append(out, ExportVM{Observation: Observation{VCenterID: c.VCenterID, Context: c.Name, VM: vm}, Snapshots: snapshots})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func loadExportSnapshots(ctx context.Context, tx *sql.Tx, vmID int64) ([]vsphere.VMSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT moref,numeric_id,parent_moref,name,description,create_time,power_state,quiesced,current_snapshot FROM snapshot_observations WHERE vm_observation_id=? ORDER BY create_time,moref,numeric_id`, vmID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vsphere.VMSnapshot
	for rows.Next() {
		var snapshot vsphere.VMSnapshot
		var createTime int64
		var quiesced, current int
		if err := rows.Scan(&snapshot.ID, &snapshot.NumericID, &snapshot.ParentID, &snapshot.Name, &snapshot.Description, &createTime, &snapshot.PowerState, &quiesced, &current); err != nil {
			return nil, err
		}
		snapshot.CreateTime = time.UnixMilli(createTime).UTC()
		snapshot.Quiesced, snapshot.Current = quiesced != 0, current != 0
		out = append(out, snapshot)
	}
	return out, rows.Err()
}

func loadExportResources(ctx context.Context, tx *sql.Tx, runID int64) ([]ResourceObservation, error) {
	rows, err := tx.QueryContext(ctx, `SELECT cr.name,cr.vcenter_id,cc.kind,ro.moref,ro.name,ro.payload,ro.cpu_capacity,ro.cpu_used,ro.memory_capacity,ro.memory_used,ro.storage_capacity,ro.storage_free FROM resource_observations ro JOIN context_collections cc ON cc.id=ro.collection_id JOIN context_runs cr ON cr.id=cc.context_run_id WHERE cr.run_id=? ORDER BY cc.kind,cr.name,ro.name,ro.moref`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResourceObservation
	for rows.Next() {
		var contextName, vcenterID, kind, id, name string
		var payload []byte
		var cpuCap, cpuUsed, memCap, memUsed, storageCap, storageFree sql.NullFloat64
		if err := rows.Scan(&contextName, &vcenterID, &kind, &id, &name, &payload, &cpuCap, &cpuUsed, &memCap, &memUsed, &storageCap, &storageFree); err != nil {
			return nil, err
		}
		var raw map[string]any
		if err := json.Unmarshal(payload, &raw); err != nil {
			return nil, fmt.Errorf("assessment %d %s %q has malformed payload: %w", runID, kind, id, err)
		}
		_ = raw
		out = append(out, ResourceObservation{VCenterID: vcenterID, Context: contextName, Kind: strings.ToLower(kind), ID: id, Name: name, Payload: append(json.RawMessage(nil), payload...), CPUCapacity: nullFloatPtr(cpuCap), CPUUsed: nullFloatPtr(cpuUsed), MemoryCapacity: nullFloatPtr(memCap), MemoryUsed: nullFloatPtr(memUsed), StorageCapacity: nullFloatPtr(storageCap), StorageFree: nullFloatPtr(storageFree)})
	}
	return out, rows.Err()
}

func sortExportData(contexts []ContextRun, vms []ExportVM, resources []ResourceObservation) {
	sort.SliceStable(contexts, func(i, j int) bool {
		if contexts[i].Name != contexts[j].Name {
			return contexts[i].Name < contexts[j].Name
		}
		return contexts[i].RunID < contexts[j].RunID
	})
	sort.SliceStable(vms, func(i, j int) bool {
		a, b := vms[i].Observation, vms[j].Observation
		return exportObjectLess(a.Context, a.VM.Location.Datacenter, a.VM.Name, a.VM.ID, b.Context, b.VM.Location.Datacenter, b.VM.Name, b.VM.ID)
	})
	for i := range vms {
		sort.SliceStable(vms[i].Snapshots, func(a, b int) bool {
			x, y := vms[i].Snapshots[a], vms[i].Snapshots[b]
			if !x.CreateTime.Equal(y.CreateTime) {
				return x.CreateTime.Before(y.CreateTime)
			}
			if x.ID != y.ID {
				return x.ID < y.ID
			}
			return x.NumericID < y.NumericID
		})
	}
	sort.SliceStable(resources, func(i, j int) bool {
		a, b := resources[i], resources[j]
		return exportObjectLess(a.Context, resourceDatacenter(a), a.Name, a.ID, b.Context, resourceDatacenter(b), b.Name, b.ID)
	})
}

func resourceDatacenter(r ResourceObservation) string {
	var loc struct {
		Datacenter string `json:"datacenter"`
	}
	_ = json.Unmarshal(r.Payload, &loc)
	return loc.Datacenter
}

func exportObjectLess(ac, ad, an, ai, bc, bd, bn, bi string) bool {
	for _, pair := range [][2]string{{ac, bc}, {ad, bd}, {strings.ToLower(an), strings.ToLower(bn)}, {ai, bi}} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}
