package vsphere_test

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/simulator"
)

// TestPingSucceedsWithoutSessionValidatePrivilege reproduces a real vCenter's
// built-in ReadOnly role, which lacks Sessions.ValidateSession and so is
// denied SessionIsActive. vcsim does not enforce that privilege, so the
// denial is injected; the connection test must not depend on the call.
func TestPingSucceedsWithoutSessionValidatePrivilege(t *testing.T) {
	c, faults := newSimulator(t, nil)
	faults.AddRule(&simulator.FaultInjectionRule{
		MethodName:  "SessionIsActive",
		ObjectType:  "*",
		ObjectName:  "*",
		Probability: 1,
		Enabled:     true,
		FaultType:   simulator.FaultTypeNoPermission,
		Message:     "Permission to perform this operation was denied.",
	})

	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping failed for a session that cannot validate sessions: %v", err)
	}
}

func TestPingReportsAnEndedSession(t *testing.T) {
	c, _ := newSimulator(t, nil)
	ctx := context.Background()
	if _, err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping on a live session: %v", err)
	}
	if err := c.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := c.Ping(ctx); err == nil {
		t.Fatal("Ping succeeded after the session was logged out")
	}
}
