package credentials_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/easonliuuuuu/vsfleet/internal/credentials"
)

func TestKeyringProbeAcceptsAWorkingStoreWithoutWriting(t *testing.T) {
	keyring.MockInit()
	k := &credentials.Keyring{Service: "vsfleet-probe-test"}

	if err := k.Probe(context.Background()); err != nil {
		t.Fatalf("Probe on a working, empty store = %v, want nil", err)
	}
	// The probe is a lookup only. Writing a test entry would leave something
	// behind in the operator's keychain every time setup ran.
	if _, err := keyring.Get("vsfleet-probe-test", "vsfleet-keyring-probe"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("Probe left an entry behind: lookup = %v, want not found", err)
	}
}

func TestKeyringProbeReportsAnUnavailableStore(t *testing.T) {
	keyring.MockInitWithError(errors.New("The name org.freedesktop.secrets was not provided by any .service files"))
	t.Cleanup(keyring.MockInit)
	k := &credentials.Keyring{Service: "vsfleet-probe-test"}

	err := k.Probe(context.Background())
	if err == nil {
		t.Fatal("Probe on a store that cannot answer = nil, want an error")
	}
	for _, want := range []string{"system keyring unavailable", "org.freedesktop.secrets"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Probe error %q does not mention %q", err, want)
		}
	}
}

func TestResolverCheckAvailableAsksOnlyAProber(t *testing.T) {
	keyring.MockInitWithError(errors.New("no session bus"))
	t.Cleanup(keyring.MockInit)
	ctx := context.Background()

	// An in-memory keyring — what the testbed registers — cannot go missing,
	// and checking it must not reach the OS store behind the mock above.
	isolated := credentials.NewResolver(credentials.NewStatic(credentials.SchemeKeyring, nil))
	if err := isolated.CheckAvailable(ctx, credentials.SchemeKeyring); err != nil {
		t.Errorf("an in-memory keyring reported unavailable: %v", err)
	}

	real := credentials.NewResolver(&credentials.Keyring{Service: "vsfleet-probe-test"})
	if err := real.CheckAvailable(ctx, credentials.SchemeKeyring); err == nil {
		t.Error("the OS keyring with no session bus reported available")
	}

	if err := isolated.CheckAvailable(ctx, credentials.SchemeExec); err == nil {
		t.Error("a scheme with no registered provider reported available")
	}
}
