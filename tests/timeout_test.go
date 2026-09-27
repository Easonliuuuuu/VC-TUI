package tests

import (
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi/simulator"
)

// TestTimeoutBoundsInventoryEnumeration reproduces issue #28: --timeout used
// to bound only the connection phase, so a vCenter that connected quickly but
// then hung enumerating inventory ran unbounded — client.ListInventory was
// called with the parent, un-timed-out context rather than the one Connect
// itself had already been bounded by.
//
// RetrievePropertiesEx is the SOAP method every inventory listing goes
// through (the property collector call behind view.ContainerView.Retrieve),
// so delaying it here well past --timeout stands in for a slow or overloaded
// vCenter. The command must return once --timeout elapses, not once the
// simulator's artificial delay does.
func TestTimeoutBoundsInventoryEnumeration(t *testing.T) {
	vc := startVCenter(t, func(m *simulator.Model) {
		m.Datacenter = 1
		m.Cluster = 1
		m.ClusterHost = 2
		m.Machine = 3
	})
	r := newRunner(t)
	r.addContext("lab", vc)

	// Installed only once the context is saved: "context add" runs the
	// connection test, whose liveness probe is itself a property read.
	vc.Service.AddFaultRule(&simulator.FaultInjectionRule{
		MethodName:  "RetrievePropertiesEx",
		ObjectType:  "*",
		ObjectName:  "*",
		Probability: 1.0,
		FaultType:   simulator.FaultTypeTimeout,
		Delay:       1500, // milliseconds — far past the --timeout below
		Enabled:     true,
	})

	start := time.Now()
	_, stderr, err := r.run(testPassword+"\n", "vm", "list", "--timeout", "300ms")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected \"vm list\" to fail once enumeration outran --timeout, stderr:\n%s", stderr)
	}
	// The fix under test: this must return once --timeout elapses, nowhere
	// near the simulator's 1.5s injected delay. Before it, the command would
	// have blocked for the full 1.5s because ListInventory ran with the
	// parent, un-timed-out context.
	if elapsed > time.Second {
		t.Fatalf("\"vm list\" took %s to fail, want well under the simulator's 1.5s delay — "+
			"the timeout is not bounding inventory enumeration", elapsed)
	}
	if !strings.Contains(stderr, "timed out after 300ms") {
		t.Errorf("expected an error naming the configured timeout, got:\n%s", stderr)
	}
}
