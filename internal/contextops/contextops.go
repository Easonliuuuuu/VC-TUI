// Package contextops is where a context actually gets created, tested, saved
// and removed. The CLI wizard and the terminal interface both collect the
// same fields through different input methods — one over stdin prompts, one
// over a form — and both hand the result to this package rather than each
// re-implementing what "save a context" means.
package contextops

import (
	"context"
	"errors"
	"fmt"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// Input is every field a context needs, already resolved to concrete values.
// Gathering them — prompting on a terminal, reading form fields — is the
// front end's job; this package never asks a question.
type Input struct {
	Name     string
	Endpoint string
	Username string
	// Via and ViaMoRef preserve where a nested context was discovered. They
	// describe provenance only and do not affect how the connection is made.
	Via        string
	ViaMoRef   string
	Datacenter string
	Transport  config.TransportConfig
	TLS        config.TLSConfig
	// Credential is the reference to save. The zero value defaults to
	// keyring:<name>, which is what a new context gets when nothing else was
	// specified.
	Credential credentials.Ref
	// Password, when HavePassword is set, is stored under Credential (if it
	// is a keyring reference) and used for the connection test.
	Password     string
	HavePassword bool
	// ProxyCredential is the reference under which the proxy's own password
	// is saved, when Transport has a Username. The zero value defaults to
	// keyring:<name>-proxy, distinct from the vCenter's own keyring entry.
	ProxyCredential credentials.Ref
	// ProxyPassword, when HaveProxyPassword is set, is stored under
	// ProxyCredential and used for the connection test — the same relationship
	// Password has to Credential, for the proxy instead of the vCenter.
	ProxyPassword     string
	HaveProxyPassword bool
	// Replace allows Save to overwrite an existing context of the same name,
	// which is what editing one means.
	Replace bool
	// SaveOnTestFailure keeps a context that failed its connection test,
	// for an operator who wants to fix the fault after saving rather than
	// before.
	SaveOnTestFailure bool
	// SetCurrent makes this the current context once saved.
	SetCurrent bool
	// DropReplacedKeyring, with Replace, deletes the keyring entry the
	// replaced context used when the new one no longer refers to it. Once
	// the reference is gone nothing would ever remove that password — not
	// even "context remove --with-credential" — so an edit that moves a
	// context off the keyring asks for it to be cleaned up.
	DropReplacedKeyring bool
}

// Build constructs and normalizes a context from Input without touching the
// configuration or the network. It is also what a "test before saving" action
// runs against.
func Build(in Input) *config.Context {
	cc := &config.Context{
		Name:       in.Name,
		Endpoint:   in.Endpoint,
		Username:   in.Username,
		Via:        in.Via,
		ViaMoRef:   in.ViaMoRef,
		Datacenter: in.Datacenter,
		Transport:  in.Transport,
		TLS:        in.TLS,
	}
	cc.Normalize()
	cred := in.Credential
	if cred.IsZero() {
		cred = credentials.Ref{Scheme: credentials.SchemeKeyring, Value: cc.Name}
	}
	cc.Credential = cred
	if cc.Transport.Username != "" {
		pref := in.ProxyCredential
		if pref.IsZero() {
			pref = credentials.Ref{Scheme: credentials.SchemeKeyring, Value: cc.Name + "-proxy"}
		}
		cc.Transport.Credential = pref
	}
	return cc
}

func connectOptions(connOpts vsphere.ConnectOptions, in Input) vsphere.ConnectOptions {
	if in.HavePassword {
		c := credentials.Credential{Password: in.Password}
		connOpts.Credential = &c
	}
	if in.HaveProxyPassword {
		c := credentials.Credential{Password: in.ProxyPassword}
		connOpts.ProxyCredential = &c
	}
	return connOpts
}

// Test builds a context from Input and walks its connection, without saving
// anything. It is what a "test connection" action runs before an operator
// commits to a context, and what Save runs again just before it writes.
func Test(ctx context.Context, in Input, connOpts vsphere.ConnectOptions) (*config.Context, *vsphere.Diagnosis) {
	cc := Build(in)
	if err := cc.Validate(); err != nil {
		return cc, &vsphere.Diagnosis{
			Context: cc.Name, Endpoint: cc.Endpoint, Route: cc.Transport.Describe(), TLS: cc.TLS.Describe(),
			Checks: []vsphere.Check{{Name: "Configuration valid", Status: vsphere.CheckFail, Err: err}},
		}
	}
	d, client := vsphere.Diagnose(ctx, cc, connectOptions(connOpts, in))
	if client != nil {
		_ = client.Close(context.WithoutCancel(ctx))
	}
	return cc, d
}

// Result is the outcome of Save: the context as written (or as it would have
// been), the diagnosis if a test ran, and a non-fatal warning when a keyring
// write failed and the context was saved as a prompt credential instead.
type Result struct {
	Context      *config.Context
	Diagnosis    *vsphere.Diagnosis
	StoreWarning error
	// Dropped is the keyring entry removed because of DropReplacedKeyring;
	// zero when nothing was removed. DropWarning is set instead when the
	// removal failed, which never undoes the save.
	Dropped     credentials.Ref
	DropWarning error
}

// Save validates Input, optionally tests the connection, stores the password
// and writes the context to the configuration — the one place both front ends
// go through, so "what does saving a context actually do" has one answer.
//
// On a validation error or a failed test that SaveOnTestFailure does not
// override, the context is not written and Save returns a non-nil error;
// Result is still populated so the caller can show what was attempted.
func Save(ctx context.Context, cfg *config.Config, resolver *credentials.Resolver, connOpts vsphere.ConnectOptions, in Input, test bool) (*Result, error) {
	cc := Build(in)
	res := &Result{Context: cc}
	if err := cc.Validate(); err != nil {
		return res, err
	}
	if test {
		var client *vsphere.Client
		res.Diagnosis, client = vsphere.Diagnose(ctx, cc, connectOptions(connOpts, in))
		if client != nil {
			_ = client.Close(context.WithoutCancel(ctx))
		}
		if !res.Diagnosis.OK() && !in.SaveOnTestFailure {
			if derr := res.Diagnosis.Err(); derr != nil {
				return res, fmt.Errorf("connection test failed: %w", derr)
			}
			return res, errors.New("connection test failed")
		}
	}
	// A keyring write can fail for reasons that have nothing to do with this
	// context — no OS secret store on a headless machine, a locked D-Bus
	// session, a container with none configured. Leaving cc.Credential as
	// "keyring:<name>" in that case would save a lie: the config would claim
	// a password is stored where none is, until the next connection asks the
	// keyring for it, gets nothing back, and falls through to a prompt
	// anyway. Downgrading to the prompt scheme here makes the saved context
	// exactly what choosing --credential prompt from the start would have
	// produced — no password manager, so it behaves as if there were none —
	// with the warning as the only difference an operator sees.
	if in.HavePassword && cc.Credential.Scheme == credentials.SchemeKeyring {
		if err := resolver.Store(ctx, cc.Credential, credentials.Credential{Password: in.Password}); err != nil {
			res.StoreWarning = fmt.Errorf("%w — saved as a prompt credential instead", err)
			cc.Credential = credentials.Ref{Scheme: credentials.SchemePrompt}
		}
	}
	if in.HaveProxyPassword && cc.Transport.Credential.Scheme == credentials.SchemeKeyring {
		if err := resolver.Store(ctx, cc.Transport.Credential, credentials.Credential{Password: in.ProxyPassword}); err != nil {
			warning := fmt.Errorf("proxy: %w — saved as a prompt credential instead", err)
			if res.StoreWarning != nil {
				res.StoreWarning = fmt.Errorf("%v; %w", res.StoreWarning, warning)
			} else {
				res.StoreWarning = warning
			}
			cc.Transport.Credential = credentials.Ref{Scheme: credentials.SchemePrompt}
		}
	}
	var replaced credentials.Ref
	if in.Replace && in.DropReplacedKeyring {
		if old, err := cfg.Context(cc.Name); err == nil {
			replaced = old.Credential
		}
	}
	if err := cfg.Add(cc, in.Replace); err != nil {
		return res, err
	}
	if in.SetCurrent || cfg.CurrentContext == "" {
		cfg.CurrentContext = cc.Name
	}
	if err := cfg.Save(); err != nil {
		return res, err
	}
	// Only after the save: a failed save keeps the old context, and with it
	// the password it still needs.
	if replaced.Scheme == credentials.SchemeKeyring && !refInUse(cfg, replaced) {
		switch err := resolver.Delete(ctx, replaced); {
		case err == nil:
			res.Dropped = replaced
		case !errors.Is(err, credentials.ErrNotFound):
			res.DropWarning = fmt.Errorf("could not remove the old password %s from the keyring: %w", replaced, err)
		}
	}
	return res, nil
}

// refInUse reports whether any configured context still resolves a password
// through ref, as its own credential or its proxy's. A keyring key can be
// shared by naming it explicitly, and deleting it would break the others.
func refInUse(cfg *config.Config, ref credentials.Ref) bool {
	for _, c := range cfg.Contexts {
		if c.Credential == ref || c.Transport.Credential == ref {
			return true
		}
	}
	return false
}

// Remove deletes a context from the configuration and saves. It returns the
// removed context so the caller can decide whether to also delete its stored
// credential — that is a separate, explicit choice in both front ends, never
// a side effect of removing the context.
func Remove(cfg *config.Config, name string) (*config.Context, error) {
	cc, err := cfg.Context(name)
	if err != nil {
		return nil, err
	}
	if err := cfg.Remove(cc.Name); err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}
	return cc, nil
}

// DeleteCredential removes a context's stored password from its credential
// provider. Errors.Is(err, credentials.ErrNotFound) is expected and harmless
// when nothing was ever stored.
func DeleteCredential(ctx context.Context, resolver *credentials.Resolver, ref credentials.Ref) error {
	return resolver.Delete(ctx, ref)
}

// DeleteCredentials removes every password a context might have stored — its
// own, and its proxy's if the route has one — so "also delete the stored
// credential" on a remove means all of them, not just the vCenter's. A
// reference on any other scheme (prompt, say) has nothing stored to delete
// and is silently skipped, the same as ErrNotFound.
func DeleteCredentials(ctx context.Context, resolver *credentials.Resolver, cc *config.Context) error {
	var errs []error
	if cc.Credential.Scheme == credentials.SchemeKeyring {
		if err := DeleteCredential(ctx, resolver, cc.Credential); err != nil && !errors.Is(err, credentials.ErrNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", cc.Credential, err))
		}
	}
	if cc.Transport.Credential.Scheme == credentials.SchemeKeyring {
		if err := DeleteCredential(ctx, resolver, cc.Transport.Credential); err != nil && !errors.Is(err, credentials.ErrNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", cc.Transport.Credential, err))
		}
	}
	return errors.Join(errs...)
}
