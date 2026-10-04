package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/assessment"
	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

func newNetworkHistoryDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := assessment.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	contexts := []*config.Context{{Name: "prod", Endpoint: "https://prod.example"}, {Name: "dr", Endpoint: "https://dr.example"}}
	run, err := store.StartRunWithMetadata(context.Background(), "test", contexts, when, assessment.RunMetadata{InventorySchemaVersion: "12"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		context, vcenter, clusterID, clusterName, hostID, hostName, dvsID, dvsName string
	}{
		{context: "prod", vcenter: "vc-prod", clusterID: "cluster-prod", clusterName: "cluster-prod", hostID: "host-prod", hostName: "esx-prod", dvsID: "dvs-prod", dvsName: "dvs-prod"},
		{context: "dr", vcenter: "vc-dr", clusterID: "cluster-dr", clusterName: "cluster-dr", hostID: "host-dr", hostName: "esx-dr", dvsID: "dvs-dr", dvsName: "dvs-dr"},
	} {
		cluster := vsphere.Cluster{Location: vsphere.Location{Context: item.context, Datacenter: "dc"}, ID: item.clusterID, Name: item.clusterName}
		host := vsphere.Host{Location: vsphere.Location{Context: item.context, Datacenter: "dc"}, ID: item.hostID, Name: item.hostName, Cluster: item.clusterName}
		switchValue := vsphere.DVSwitch{Location: vsphere.Location{Context: item.context, Datacenter: "dc"}, ID: item.dvsID, Name: item.dvsName, UUID: item.dvsID + "-uuid", MaxMTU: 1500, Hosts: []string{item.hostID}, PortGroups: []vsphere.DVPortGroup{{ID: item.dvsID + "-pg", Key: item.dvsID + "-pg-key", Name: "app", VLAN: "100"}}}
		resources := []assessment.ResourceObservation{
			resourcePayload(t, item.context, item.vcenter, "cluster", cluster.ID, cluster.Name, cluster),
			resourcePayload(t, item.context, item.vcenter, "host", host.ID, host.Name, host),
			resourcePayload(t, item.context, item.vcenter, "dvswitch", switchValue.ID, switchValue.Name, switchValue),
		}
		collections := make([]assessment.CollectionResult, 0, 8)
		for _, kind := range []string{"vm", "host", "cluster", "resourcepool", "vapp", "dvswitch", "datastore", "network", "snapshot"} {
			collection := assessment.CollectionResult{Kind: kind, Status: "success", Resources: resourcesForKind(resources, kind)}
			if kind == "vm" {
				collection.ItemCount = 0
			}
			collections = append(collections, collection)
		}
		if err := store.SaveContext(context.Background(), run.ID, assessment.ContextResult{Name: item.context, VCenterID: item.vcenter, Status: "success", Collections: collections}, when.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.FinishRun(context.Background(), run.ID, when.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	return path
}

func resourcePayload(t *testing.T, context, vcenter, kind, id, name string, value any) assessment.ResourceObservation {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return assessment.ResourceObservation{Context: context, VCenterID: vcenter, Kind: kind, ID: id, Name: name, Payload: payload}
}

func resourcesForKind(resources []assessment.ResourceObservation, kind string) []assessment.ResourceObservation {
	result := make([]assessment.ResourceObservation, 0)
	for _, resource := range resources {
		if resource.Kind == kind {
			result = append(result, resource)
		}
	}
	return result
}

func runNetworkCommand(t *testing.T, dbPath string, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	a := &App{HistoryPath: dbPath, Out: &out, Err: &errOut}
	root := NewRootCommand(a)
	root.SetArgs(append([]string{"--history-db", dbPath}, args...))
	err := root.ExecuteContext(context.Background())
	_ = a.Close(context.Background())
	return out.String(), errOut.String(), err
}

func TestNetworkCompareCommandJSONShape(t *testing.T) {
	dbPath := newNetworkHistoryDB(t)
	stdout, stderr, err := runNetworkCommand(t, dbPath, "network", "compare", "cluster-prod", "cluster-dr", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("compare err=%v stderr=%q stdout=%s", err, stderr, stdout)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result["run_id"] != float64(1) || result["confidence"] != "complete" {
		t.Fatalf("unexpected JSON envelope: %s", stdout)
	}
	if matched, ok := result["matched"].([]any); !ok || len(matched) != 1 {
		t.Fatalf("expected one matched network: %s", stdout)
	}
}

func TestNetworkCommandsValidateArgumentsAndFlags(t *testing.T) {
	dbPath := newNetworkHistoryDB(t)
	_, _, err := runNetworkCommand(t, dbPath, "network", "compare", "only-one-cluster")
	if err == nil || !strings.Contains(err.Error(), "accepts between 2 and 3 arg") {
		t.Fatalf("compare argument validation error=%v", err)
	}
	_, _, err = runNetworkCommand(t, dbPath, "assessment", "network-readiness")
	if err == nil || !strings.Contains(err.Error(), "required flag") {
		t.Fatalf("readiness flag validation error=%v", err)
	}
}
