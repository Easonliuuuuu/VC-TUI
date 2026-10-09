// Package credentials resolves the secrets a vCenter context needs without
// ever storing them in the configuration file. Configuration holds a Ref such
// as "keyring:customer-a"; a Provider turns that reference into a Credential.
package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrNotFound is returned by a Provider when the reference is well formed but
// no credential is stored for it. Callers may treat this as "ask the user".
var ErrNotFound = errors.New("credential not found")

// ErrUnsupported is returned when a Provider cannot service an operation, for
// example storing into a read-only source.
var ErrUnsupported = errors.New("operation not supported by credential provider")

// Credential is a resolved secret. Username is optional: when empty the
// context's configured username is used.
type Credential struct {
	Username string
	Password string
}

// Ref is a reference to a credential, never the credential itself. It is
// written in configuration as "<scheme>:<value>", e.g. "keyring:customer-a".
// A bare "prompt" (no value) is also valid.
type Ref struct {
	Scheme string
	Value  string
}

// Known credential schemes.
const (
	SchemeKeyring = "keyring"
	SchemePrompt  = "prompt"
	SchemeEnv     = "env"
	SchemeFile    = "file"
	SchemeExec    = "exec"
)

// nonInteractiveSchemes are the schemes that resolve a secret without a
// terminal, which is what makes cron, systemd, container and CI collection
// possible. They are read-only sources: nothing is stored under them, and a
// miss must never fall through to an interactive prompt — see Resolve.
var nonInteractiveSchemes = map[string]bool{
	SchemeEnv:  true,
	SchemeFile: true,
	SchemeExec: true,
}

// NonInteractive reports whether a reference resolves without a terminal.
func (r Ref) NonInteractive() bool { return nonInteractiveSchemes[r.Scheme] }

// ParseRef parses the textual form of a credential reference. An empty string
// yields the zero Ref, which means "prompt interactively".
func ParseRef(s string) (Ref, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Ref{}, nil
	}
	scheme, value, ok := strings.Cut(s, ":")
	scheme = strings.TrimSpace(scheme)
	if !ok {
		// Bare scheme, e.g. "prompt".
		value = ""
	}
	switch scheme {
	case SchemeKeyring:
		if strings.TrimSpace(value) == "" {
			return Ref{}, fmt.Errorf("credential reference %q: keyring requires a key, e.g. keyring:customer-a", s)
		}
	case SchemePrompt:
	case SchemeEnv:
		if strings.TrimSpace(value) == "" {
			return Ref{}, fmt.Errorf("credential reference %q: env requires a variable name, e.g. env:VSFLEET_PROD_PASSWORD", s)
		}
	case SchemeFile:
		if strings.TrimSpace(value) == "" {
			return Ref{}, fmt.Errorf("credential reference %q: file requires a path, e.g. file:/run/secrets/vcenter", s)
		}
	case SchemeExec:
		if strings.TrimSpace(value) == "" {
			return Ref{}, fmt.Errorf("credential reference %q: exec requires a program, e.g. exec:/usr/local/bin/vsfleet-credential", s)
		}
	default:
		return Ref{}, fmt.Errorf("credential reference %q: unknown scheme %q (supported: keyring, prompt, env, file, exec)", s, scheme)
	}
	return Ref{Scheme: scheme, Value: strings.TrimSpace(value)}, nil
}

// IsZero reports whether the reference is unset.
func (r Ref) IsZero() bool { return r.Scheme == "" }

// WithDefaultLabel returns r unchanged, except a bare "prompt" reference —
// one with no value of its own — is given label as its value. Several
// contexts may all be configured as plain "prompt", and disambiguating which
// one is being asked about only matters once a concrete value is needed for
// it: as what Get shows the operator, or as the key Prime records an answer
// under. Every other reference, including a labelled "prompt:x", is already
// specific enough and passes through untouched.
func (r Ref) WithDefaultLabel(label string) Ref {
	if r.Scheme == SchemePrompt && r.Value == "" {
		r.Value = label
	}
	return r
}

// String renders the reference in its configuration form. It never contains a
// secret, so it is safe to log.
func (r Ref) String() string {
	switch {
	case r.Scheme == "":
		return ""
	case r.Value == "":
		return r.Scheme
	default:
		return r.Scheme + ":" + r.Value
	}
}

// MarshalText implements encoding.TextMarshaler so refs round-trip through TOML.
func (r Ref) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *Ref) UnmarshalText(b []byte) error {
	ref, err := ParseRef(string(b))
	if err != nil {
		return err
	}
	*r = ref
	return nil
}

// Provider resolves, stores and removes credentials for one scheme.
type Provider interface {
	// Scheme is the reference scheme this provider handles.
	Scheme() string
	Get(ctx context.Context, ref Ref) (Credential, error)
	Store(ctx context.Context, ref Ref, credential Credential) error
	Delete(ctx context.Context, ref Ref) error
}

// Prober is implemented by a Provider whose backing store can be missing at
// run time — the OS keyring on a machine with no Secret Service — so setup
// can find out before relying on it.
type Prober interface {
	Probe(ctx context.Context) error
}

// Resolver dispatches to the Provider registered for a reference's scheme. It
// is itself a Provider, so the rest of the application depends on one type.
type Resolver struct {
	providers map[string]Provider
	// Default handles the zero Ref. It is normally the prompt provider.
	Default Provider

	primedMu sync.RWMutex
	// primed holds credentials already resolved outside the normal Get path —
	// see Prime.
	primed map[Ref]Credential
}

// NewResolver builds a Resolver from the given providers. The first provider
// whose scheme is "prompt" becomes the default for unset references.
func NewResolver(providers ...Provider) *Resolver {
	r := &Resolver{providers: make(map[string]Provider, len(providers))}
	for _, p := range providers {
		r.providers[p.Scheme()] = p
		if p.Scheme() == SchemePrompt && r.Default == nil {
			r.Default = p
		}
	}
	return r
}

// Provider returns the provider registered for a scheme.
func (r *Resolver) Provider(scheme string) (Provider, bool) {
	p, ok := r.providers[scheme]
	return p, ok
}

// SetProvider registers or replaces the provider for one scheme, updating
// Default too when that scheme is "prompt".
//
// It exists for callers that already built a Resolver around the process's
// real providers but need to swap out one of them for a context they did not
// anticipate at construction time — namely the terminal interface, which
// cannot let a background load read a password from the same stdin Bubble
// Tea is reading keystrokes from, and so registers its own prompt provider
// that asks through the UI instead.
func (r *Resolver) SetProvider(p Provider) {
	r.providers[p.Scheme()] = p
	if p.Scheme() == SchemePrompt {
		r.Default = p
	}
}

// CheckAvailable reports whether the provider registered for scheme can be
// used right now. A provider that cannot go missing — an in-memory one in a
// test or the testbed, a read-only source — is always available; only a
// Prober is asked. Going through the registered provider, rather than the OS
// keyring directly, is what keeps a resolver built for isolation isolated.
func (r *Resolver) CheckAvailable(ctx context.Context, scheme string) error {
	p, ok := r.providers[scheme]
	if !ok {
		return fmt.Errorf("no credential provider registered for scheme %q", scheme)
	}
	if pr, ok := p.(Prober); ok {
		return pr.Probe(ctx)
	}
	return nil
}

// Prime records a credential already resolved outside the normal Get path. A
// later Get for the exact same reference returns it directly, without asking
// the registered provider again; Get for any other reference is unaffected.
func (r *Resolver) Prime(ref Ref, cred Credential) {
	r.primedMu.Lock()
	defer r.primedMu.Unlock()
	if r.primed == nil {
		r.primed = map[Ref]Credential{}
	}
	r.primed[ref] = cred
}

func (r *Resolver) primedCredential(ref Ref) (Credential, bool) {
	r.primedMu.RLock()
	defer r.primedMu.RUnlock()
	c, ok := r.primed[ref]
	return c, ok
}

func (r *Resolver) lookup(ref Ref) (Provider, error) {
	if ref.IsZero() {
		if r.Default == nil {
			return nil, errors.New("no credential reference configured and no interactive prompt available")
		}
		return r.Default, nil
	}
	p, ok := r.providers[ref.Scheme]
	if !ok {
		return nil, fmt.Errorf("no credential provider registered for scheme %q", ref.Scheme)
	}
	return p, nil
}

// Scheme implements Provider.
func (r *Resolver) Scheme() string { return "" }

// Get implements Provider.
func (r *Resolver) Get(ctx context.Context, ref Ref) (Credential, error) {
	if c, ok := r.primedCredential(ref); ok {
		return c, nil
	}
	p, err := r.lookup(ref)
	if err != nil {
		return Credential{}, err
	}
	return p.Get(ctx, ref)
}

// Store implements Provider.
func (r *Resolver) Store(ctx context.Context, ref Ref, c Credential) error {
	p, err := r.lookup(ref)
	if err != nil {
		return err
	}
	return p.Store(ctx, ref, c)
}

// Delete implements Provider.
func (r *Resolver) Delete(ctx context.Context, ref Ref) error {
	p, err := r.lookup(ref)
	if err != nil {
		return err
	}
	return p.Delete(ctx, ref)
}

// Static is a Provider backed by an in-memory map. It exists for tests and for
// credentials supplied on the command line for a single invocation.
type Static struct {
	scheme string
	creds  map[string]Credential
}

// NewStatic returns a Static provider answering for the given scheme.
func NewStatic(scheme string, creds map[string]Credential) *Static {
	if creds == nil {
		creds = map[string]Credential{}
	}
	return &Static{scheme: scheme, creds: creds}
}

func (s *Static) Scheme() string { return s.scheme }

func (s *Static) Get(_ context.Context, ref Ref) (Credential, error) {
	c, ok := s.creds[ref.Value]
	if !ok {
		return Credential{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
	}
	return c, nil
}

func (s *Static) Store(_ context.Context, ref Ref, c Credential) error {
	s.creds[ref.Value] = c
	return nil
}

func (s *Static) Delete(_ context.Context, ref Ref) error {
	delete(s.creds, ref.Value)
	return nil
}
