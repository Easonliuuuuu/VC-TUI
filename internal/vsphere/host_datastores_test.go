package vsphere_test

import (
	"context"
	"testing"
)

func TestHostsReportMountedDatastores(t *testing.T) {
	c, _ := newSimulator(t, nil)
	ctx := context.Background()
	hosts, err := c.ListHosts(ctx)
	if err != nil {
		t.Fatalf("list hosts: %v", err)
	}
	datastores, err := c.ListDatastores(ctx)
	if err != nil {
		t.Fatalf("list datastores: %v", err)
	}
	known := map[string]bool{}
	for _, ds := range datastores {
		known[ds.Name] = true
	}
	mounted := 0
	for _, h := range hosts {
		for _, name := range h.Datastores {
			if !known[name] {
				t.Fatalf("host %s mounts %q, which is not a listed datastore", h.Name, name)
			}
			mounted++
		}
	}
	if mounted == 0 {
		t.Fatalf("no host reported a mounted datastore: %+v", hosts)
	}
}

func TestClustersReportHAConfiguration(t *testing.T) {
	c, _ := newSimulator(t, nil)
	clusters, err := c.ListClusters(context.Background())
	if err != nil {
		t.Fatalf("list clusters: %v", err)
	}
	for _, cl := range clusters {
		if cl.Standalone && cl.HA != nil {
			t.Fatalf("standalone %s has HA configuration: %+v", cl.Name, cl.HA)
		}
		if !cl.Standalone && cl.HAEnabled != (cl.HA != nil) {
			t.Fatalf("cluster %s: HA enabled %v but configuration %+v", cl.Name, cl.HAEnabled, cl.HA)
		}
	}
}
