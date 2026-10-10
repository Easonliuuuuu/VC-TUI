package credentials

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zalando/go-keyring"
)

// KeyringService is the service name vsfleet registers under in the operating
// system secret store.
const KeyringService = "vsfleet"

// Keyring stores credentials in the operating system secret store: Keychain on
// macOS, Secret Service on Linux, Credential Manager on Windows.
type Keyring struct {
	// Service overrides KeyringService. Tests set this to stay isolated.
	Service string
}

// NewKeyring returns a Keyring provider using the default service name.
func NewKeyring() *Keyring { return &Keyring{} }

func (k *Keyring) service() string {
	if k.Service != "" {
		return k.Service
	}
	return KeyringService
}

func (k *Keyring) Scheme() string { return SchemeKeyring }

func (k *Keyring) Get(_ context.Context, ref Ref) (Credential, error) {
	secret, err := keyring.Get(k.service(), ref.Value)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return Credential{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	case err != nil:
		return Credential{}, fmt.Errorf("read %s from system keyring: %w", ref, err)
	}
	return Credential{Password: secret}, nil
}

func (k *Keyring) Store(_ context.Context, ref Ref, c Credential) error {
	if err := keyring.Set(k.service(), ref.Value, c.Password); err != nil {
		return fmt.Errorf("write %s to system keyring: %w", ref, err)
	}
	return nil
}

// keyringProbeKey is looked up, never written, by Probe. Any key would do:
// what matters is how the store answers, not whether it holds anything.
const keyringProbeKey = "vsfleet-keyring-probe"

// keyringProbeTimeout bounds Probe. A session bus with no Secret Service
// refuses at once, but an activatable one that never starts can hold a call
// open for D-Bus's own 25-second default. A locked desktop keyring asks to be
// unlocked during the lookup, so the bound leaves time to answer that dialog.
const keyringProbeTimeout = 10 * time.Second

// Probe reports whether the OS secret store can be used, without writing to
// it. A lookup of a key that does not exist answers "not found" from a working
// store; anything else — no D-Bus session, no Secret Service, no Keychain
// access — means a password stored now would not be there next run. Setup
// asks this before it asks for a password, so an operator can pick another
// source instead of having the choice made for them after the fact.
func (k *Keyring) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, keyringProbeTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := keyring.Get(k.service(), keyringProbeKey)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("system keyring unavailable: %w", err)
	case <-ctx.Done():
		return fmt.Errorf("system keyring unavailable: no answer within %s", keyringProbeTimeout)
	}
}

func (k *Keyring) Delete(_ context.Context, ref Ref) error {
	err := keyring.Delete(k.service(), ref.Value)
	switch {
	case errors.Is(err, keyring.ErrNotFound):
		return fmt.Errorf("%w: %s", ErrNotFound, ref)
	case err != nil:
		return fmt.Errorf("delete %s from system keyring: %w", ref, err)
	}
	return nil
}
