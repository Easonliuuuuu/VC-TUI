package assessment

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func TestHostCPUCapacityHelper(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    float64
		wantOK  bool
	}{
		{
			name: "explicit total_cpu_mhz",
			payload: map[string]any{
				"cpu_cores":     float64(32),
				"cpu_mhz":       float64(2400),
				"total_cpu_mhz": float64(76800),
			},
			want:   76800,
			wantOK: true,
		},
		{
			name: "multi-core cores x mhz",
			payload: map[string]any{
				"cpu_cores": float64(16),
				"cpu_mhz":   float64(2500),
			},
			want:   40000,
			wantOK: true,
		},
		{
			name: "single-core fallback when cores is missing",
			payload: map[string]any{
				"cpu_mhz": float64(2200),
			},
			want:   2200,
			wantOK: true,
		},
		{
			name: "single-core fallback when cores is zero",
			payload: map[string]any{
				"cpu_cores": float64(0),
				"cpu_mhz":   float64(2200),
			},
			want:   2200,
			wantOK: true,
		},
		{
			name:    "empty payload",
			payload: map[string]any{},
			want:    0,
			wantOK:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := hostCPUCapacity(tc.payload)
			if ok != tc.wantOK {
				t.Fatalf("ok=%v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHostMetricProjectionAndFallbackSymmetry(t *testing.T) {
	host := vsphere.Host{
		ID:          "host-1",
		Name:        "esxi-01",
		CPUCores:    32,
		CPUMHz:      2400,
		TotalCPUMHz: 76800,
		CPUUsageMHz: 18400,
		MemoryMB:    524288,
	}
	payload, err := json.Marshal(host)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Persisted metric projection
	metrics := resourceMetrics("host", payload)
	projCap, ok := metrics[0].(float64)
	if !ok || projCap != 76800 {
		t.Fatalf("resourceMetrics projected cpu_capacity=%v, want 76800", metrics[0])
	}
	projUsed, ok := metrics[1].(float64)
	if !ok || projUsed != 18400 {
		t.Fatalf("resourceMetrics projected cpu_used=%v, want 18400", metrics[1])
	}

	// 2. Fallback calculation when observation has nil CPUCapacity
	stored := storedResource{
		observation: ResourceObservation{
			Kind:    "host",
			ID:      host.ID,
			Name:    host.Name,
			Payload: payload,
		},
	}
	fallbackPoint := capacityForResources("host", []storedResource{stored}, "estate", "")
	if fallbackPoint.CPUCapacity == nil || *fallbackPoint.CPUCapacity != 76800 {
		t.Fatalf("fallback CPUCapacity=%v, want 76800", fallbackPoint.CPUCapacity)
	}
	if fallbackPoint.CPUUsed == nil || *fallbackPoint.CPUUsed != 18400 {
		t.Fatalf("fallback CPUUsed=%v, want 18400", fallbackPoint.CPUUsed)
	}

	// 3. Calculation when observation has projected CPUCapacity
	capVal := 76800.0
	usedVal := 18400.0
	storedProjected := storedResource{
		observation: ResourceObservation{
			Kind:        "host",
			ID:          host.ID,
			Name:        host.Name,
			Payload:     payload,
			CPUCapacity: &capVal,
			CPUUsed:     &usedVal,
		},
	}
	projectedPoint := capacityForResources("host", []storedResource{storedProjected}, "estate", "")
	if projectedPoint.CPUCapacity == nil || *projectedPoint.CPUCapacity != *fallbackPoint.CPUCapacity {
		t.Fatalf("projected (%v) and fallback (%v) CPUCapacity do not match", projectedPoint.CPUCapacity, fallbackPoint.CPUCapacity)
	}
}

func TestHostClusterCapacityUnitCompatibility(t *testing.T) {
	// A cluster with 4 hosts, each 32 cores @ 2,400 MHz (76,800 MHz total each)
	// Cluster total CPU = 4 * 76,800 = 307,200 MHz.
	hosts := []storedResource{
		{
			observation: ResourceObservation{
				Kind:        "host",
				ID:          "host-1",
				Name:        "esx-1",
				CPUCapacity: floatPtr(76800),
				CPUUsed:     floatPtr(15000),
			},
		},
		{
			observation: ResourceObservation{
				Kind:        "host",
				ID:          "host-2",
				Name:        "esx-2",
				CPUCapacity: floatPtr(76800),
				CPUUsed:     floatPtr(20000),
			},
		},
		{
			observation: ResourceObservation{
				Kind:        "host",
				ID:          "host-3",
				Name:        "esx-3",
				CPUCapacity: floatPtr(76800),
				CPUUsed:     floatPtr(18000),
			},
		},
		{
			observation: ResourceObservation{
				Kind:        "host",
				ID:          "host-4",
				Name:        "esx-4",
				CPUCapacity: floatPtr(76800),
				CPUUsed:     floatPtr(22000),
			},
		},
	}

	hostPoint := capacityForResources("host", hosts, "estate", "")
	if hostPoint.CPUCapacity == nil || *hostPoint.CPUCapacity != 307200 {
		t.Fatalf("host aggregate CPUCapacity=%v, want 307200", hostPoint.CPUCapacity)
	}

	clusterPayload, _ := json.Marshal(vsphere.Cluster{
		ID:          "cluster-1",
		Name:        "compute-a",
		Hosts:       4,
		CPUCores:    128,
		TotalCPUMHz: 307200,
	})
	clusterRes := []storedResource{
		{
			observation: ResourceObservation{
				Kind:        "cluster",
				ID:          "cluster-1",
				Name:        "compute-a",
				CPUCapacity: floatPtr(307200),
				Payload:     clusterPayload,
			},
		},
	}
	clusterPoint := capacityForResources("cluster", clusterRes, "estate", "")
	if clusterPoint.CPUCapacity == nil || *clusterPoint.CPUCapacity != 307200 {
		t.Fatalf("cluster CPUCapacity=%v, want 307200", clusterPoint.CPUCapacity)
	}

	// Units and totals are identical
	if *hostPoint.CPUCapacity != *clusterPoint.CPUCapacity {
		t.Fatalf("host total (%f) does not match cluster total (%f)", *hostPoint.CPUCapacity, *clusterPoint.CPUCapacity)
	}
}

func TestHostCPUUtilizationNotOverflowing(t *testing.T) {
	// Issue #111: CPU usage across cores exceeds single core clock speed.
	// 32-core host @ 2400 MHz = 76,800 MHz total capacity.
	// 18,400 MHz CPU usage.
	// Previously: 18,400 / 2,400 = 766.7% utilization.
	// Fixed: 18,400 / 76,800 = 23.96% utilization.
	host := vsphere.Host{
		ID:          "host-1",
		Name:        "esxi-01",
		CPUCores:    32,
		CPUMHz:      2400,
		TotalCPUMHz: 76800,
		CPUUsageMHz: 18400,
	}
	payload, _ := json.Marshal(host)

	stored := storedResource{
		observation: ResourceObservation{
			Kind:        "host",
			ID:          host.ID,
			Name:        host.Name,
			Payload:     payload,
			CPUCapacity: floatPtr(76800),
			CPUUsed:     floatPtr(18400),
		},
	}
	point := capacityForResources("host", []storedResource{stored}, "estate", "")
	if point.CPUUtilization == nil {
		t.Fatal("expected CPUUtilization to be calculated")
	}
	if *point.CPUUtilization > 100 {
		t.Fatalf("CPU utilization %.2f%% exceeded 100%%", *point.CPUUtilization)
	}
	wantUtil := 18400.0 / 76800.0 * 100.0
	if diff := *point.CPUUtilization - wantUtil; diff < -0.01 || diff > 0.01 {
		t.Fatalf("CPU utilization = %.2f%%, want %.2f%%", *point.CPUUtilization, wantUtil)
	}
}

func TestCapacitySeriesPointsRetainRunIDsAcrossScopes(t *testing.T) {
	series := make(map[string]*CapacitySeries)
	for _, runID := range []int64{11, 12, 13} {
		run := Run{ID: runID}
		point := CapacityPoint{CPUCapacity: floatPtr(float64(runID))}
		appendCapacityPoint(series, "host", "estate", "", run, point)
		appendCapacityPoint(series, "host", "context", "prod", run, point)
		appendCapacityPoint(series, "host", "resource", "esx-01", run, point)
	}
	for key, item := range series {
		if len(item.Points) != 3 {
			t.Fatalf("series %q has %d points, want 3", key, len(item.Points))
		}
		for i, point := range item.Points {
			want := int64(i + 11)
			if point.Run.ID != want {
				t.Fatalf("series %q point %d run=%d, want %d", key, i, point.Run.ID, want)
			}
		}
	}
	encoded, err := json.Marshal(series["estate\x00"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"run":{"id":11,`) || !strings.Contains(string(encoded), `"run":{"id":13,`) {
		t.Fatalf("JSON capacity points lost run IDs: %s", encoded)
	}
}

func TestMigrateV4RecomputesHostCPUCapacity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// Create a v3 schema database manually
	stmts := []string{
		`CREATE TABLE runs (id INTEGER PRIMARY KEY AUTOINCREMENT, source TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '', started_at INTEGER NOT NULL, finished_at INTEGER, requested_contexts INTEGER NOT NULL DEFAULT 0, successful_contexts INTEGER NOT NULL DEFAULT 0, label TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', pinned INTEGER NOT NULL DEFAULT 0, tool_version TEXT NOT NULL DEFAULT '', inventory_schema_version TEXT NOT NULL DEFAULT '', requested_collections INTEGER NOT NULL DEFAULT 0, successful_collections INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE context_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, run_id INTEGER NOT NULL REFERENCES runs(id) ON DELETE CASCADE, name TEXT NOT NULL, vcenter_id TEXT NOT NULL, endpoint TEXT NOT NULL DEFAULT '', datacenter TEXT NOT NULL DEFAULT '', vm_status TEXT NOT NULL DEFAULT '', vm_count INTEGER NOT NULL DEFAULT 0, vm_error TEXT NOT NULL DEFAULT '', started_at INTEGER NOT NULL, finished_at INTEGER)`,
		`CREATE TABLE context_collections (id INTEGER PRIMARY KEY AUTOINCREMENT, context_run_id INTEGER NOT NULL REFERENCES context_runs(id) ON DELETE CASCADE, kind TEXT NOT NULL, started_at INTEGER NOT NULL, finished_at INTEGER, status TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', item_count INTEGER NOT NULL DEFAULT 0, UNIQUE(context_run_id, kind))`,
		`CREATE TABLE resource_observations (id INTEGER PRIMARY KEY AUTOINCREMENT, collection_id INTEGER NOT NULL REFERENCES context_collections(id) ON DELETE CASCADE, kind TEXT NOT NULL, moref TEXT NOT NULL, name TEXT NOT NULL, payload BLOB NOT NULL, cpu_capacity REAL, cpu_used REAL, memory_capacity REAL, memory_used REAL, storage_capacity REAL, storage_free REAL, UNIQUE(collection_id, moref))`,
		`CREATE TABLE vm_observations (id INTEGER PRIMARY KEY AUTOINCREMENT, context_run_id INTEGER NOT NULL REFERENCES context_runs(id) ON DELETE CASCADE, moref TEXT NOT NULL, name TEXT NOT NULL, power_state TEXT NOT NULL DEFAULT '', memory_mb INTEGER NOT NULL DEFAULT 0, cpu INTEGER NOT NULL DEFAULT 0, instance_uuid TEXT NOT NULL DEFAULT '', bios_uuid TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '', cluster TEXT NOT NULL DEFAULT '', storage_bytes INTEGER NOT NULL DEFAULT 0, guest_os TEXT NOT NULL DEFAULT '', tools_state TEXT NOT NULL DEFAULT '', tools_version TEXT NOT NULL DEFAULT '', tools_version_status TEXT NOT NULL DEFAULT '', ip_address TEXT NOT NULL DEFAULT '', is_template INTEGER NOT NULL DEFAULT 0, payload BLOB NOT NULL, UNIQUE(context_run_id, moref))`,
		`CREATE TABLE snapshot_observations (id INTEGER PRIMARY KEY AUTOINCREMENT, vm_observation_id INTEGER NOT NULL REFERENCES vm_observations(id) ON DELETE CASCADE, moref TEXT NOT NULL, numeric_id INTEGER NOT NULL, parent_moref TEXT NOT NULL DEFAULT '', name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', create_time INTEGER NOT NULL, power_state TEXT NOT NULL, quiesced INTEGER NOT NULL, current_snapshot INTEGER NOT NULL)`,
		`CREATE TABLE capture_lease (id INTEGER PRIMARY KEY CHECK(id=1), token TEXT NOT NULL, run_id INTEGER NOT NULL DEFAULT 0, acquired_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, operation TEXT NOT NULL DEFAULT 'capture')`,
		`PRAGMA user_version = 3`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("setup v3 stmt %q: %v", stmt, err)
		}
	}

	// Insert a run and a host with OLD buggy cpu_capacity = 2400 (per-core speed)
	// while payload has 32 cores @ 2400 MHz
	host := vsphere.Host{
		ID:          "host-1",
		Name:        "esxi-01",
		CPUCores:    32,
		CPUMHz:      2400,
		CPUUsageMHz: 18400,
		MemoryMB:    524288,
	}
	payload, _ := json.Marshal(host)
	now := time.Now().UTC().UnixMilli()

	res, err := db.Exec(`INSERT INTO runs(source, label, status, started_at, finished_at) VALUES('test', 'run-1', 'complete', ?, ?)`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	runID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO context_runs(run_id, name, vcenter_id, started_at, finished_at) VALUES(?, 'prod', 'vc-1', ?, ?)`, runID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	cRunID, _ := res.LastInsertId()

	res, err = db.Exec(`INSERT INTO context_collections(context_run_id, kind, started_at, finished_at, status) VALUES(?, 'host', ?, ?, 'success')`, cRunID, now, now)
	if err != nil {
		t.Fatal(err)
	}
	colID, _ := res.LastInsertId()

	// Intentionally insert old faulty projection (2400 instead of 76800)
	_, err = db.Exec(`INSERT INTO resource_observations(collection_id, kind, moref, name, payload, cpu_capacity, cpu_used) VALUES(?, 'host', 'host-1', 'esxi-01', ?, 2400, 18400)`, colID, payload)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Now open using assessment.Open(dbPath), which should run migrateV4
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer store.Close()

	// The migration ladder now runs through to the current schema version.
	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("expected user_version=%d, got %d", currentSchemaVersion, version)
	}

	// Verify cpu_capacity was updated from 2400 to 76800
	var cpuCap float64
	if err := store.db.QueryRow(`SELECT cpu_capacity FROM resource_observations WHERE moref='host-1'`).Scan(&cpuCap); err != nil {
		t.Fatal(err)
	}
	if cpuCap != 76800 {
		t.Fatalf("migrated cpu_capacity=%f, want 76800", cpuCap)
	}

	// Verify CapacityTrend returns the corrected capacity
	trend, err := store.CapacityTrend(context.Background(), TrendOptions{}, []string{"host"})
	if err != nil {
		t.Fatalf("CapacityTrend: %v", err)
	}
	if len(trend.Series) == 0 || len(trend.Series[0].Points) == 0 {
		t.Fatalf("expected trend series, got %+v", trend)
	}
	p := trend.Series[0].Points[0]
	if p.CPUCapacity == nil || *p.CPUCapacity != 76800 {
		t.Fatalf("trend CPUCapacity=%v, want 76800", p.CPUCapacity)
	}
	if p.CPUUtilization == nil || *p.CPUUtilization > 100 {
		t.Fatalf("trend CPUUtilization=%v, want < 100%%", p.CPUUtilization)
	}
}
