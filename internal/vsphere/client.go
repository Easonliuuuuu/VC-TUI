// Package vsphere turns a configured context into a usable vCenter connection
// and exposes inventory as plain domain objects. Nothing above this package
// sees a govmomi type.
package vsphere

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/credentials"
	"github.com/easonliuuuuu/vsfleet/internal/transport"
)

// Client is an authenticated connection to one vCenter.
type Client struct {
	// Context is the configuration this client was built from.
	Context *config.Context
	// About describes the remote server.
	About About
	// Latency is how long the login round trip took.
	Latency time.Duration

	vim                *govmomi.Client
	dialer             transport.Dialer
	tagger             *taggingClient
	restErr            error
	metadataMu         sync.Mutex
	customFields       []types.CustomFieldDef
	customFieldsErr    error
	customFieldsLoaded bool
	tagCache           map[string]Tag
	categoryCache      map[string]string

	// idxMu guards the cached path index, which several fetch groups running
	// concurrently on one client may all ask for.
	idxMu sync.Mutex
	idx   *Index
	idxAt time.Time
}

// IndexTTL is how long a built path index is reused before it is walked
// again. The index is name and parent for every managed entity in the
// vCenter, and it has to complete before any group can be retrieved, so
// rebuilding it on every refresh made the whole estate's folder tree a toll
// paid every few seconds to re-read one resource kind.
//
// The tree it describes changes far more slowly than the objects inside it,
// and the one thing a stale index cannot place — an object created since it
// was built — is resolved through its parent instead (see newVM), so what
// this trades away is a newly created folder's name for at most this long.
const IndexTTL = 2 * time.Minute

// About is the subset of vCenter's identity that operators care about.
type About struct {
	Name       string
	Version    string
	Build      string
	APIType    string
	APIVersion string
	OSType     string
	Vendor     string
	InstanceID string
}

// FullVersion renders "VMware vCenter 8.0.3 build-12345".
func (a About) FullVersion() string {
	s := a.Name
	if s == "" {
		s = "vCenter"
	}
	if a.Version != "" {
		s += " " + a.Version
	}
	if a.Build != "" {
		s += " build-" + a.Build
	}
	return s
}

// ConnectOptions carry everything Connect needs beyond the context itself.
type ConnectOptions struct {
	// Resolver supplies the password for the context's credential reference.
	Resolver *credentials.Resolver
	// Credential, when set, is used as-is and the resolver is not consulted.
	// The interactive commands use this after prompting once.
	Credential *credentials.Credential
	// ProxyCredential is the same escape hatch as Credential, for the proxy's
	// own password rather than the vCenter's — needed to test an
	// authenticated proxy route before its password has been stored.
	ProxyCredential *credentials.Credential
	// DialTimeout bounds a single TCP connect.
	DialTimeout time.Duration
	// UserAgent identifies this tool to vCenter.
	UserAgent string
}

// Connect builds the route, verifies the certificate according to the context's
// TLS policy, logs in, and returns a ready client.
func Connect(ctx context.Context, cc *config.Context, opts ConnectOptions) (*Client, error) {
	if err := cc.Validate(); err != nil {
		return nil, err
	}
	u, err := cc.URL()
	if err != nil {
		return nil, err
	}
	reportStage(ctx, StageConnecting)
	dialer, err := transport.New(ctx, cc.Transport, transport.Options{
		Timeout:         opts.DialTimeout,
		Resolver:        opts.Resolver,
		CredentialLabel: cc.Name + " proxy",
		ProxyCredential: opts.ProxyCredential,
	})
	if err != nil {
		return nil, err
	}
	reportStage(ctx, StageResolvingCredentials)
	cred, err := resolveCredential(ctx, cc, opts)
	if err != nil {
		return nil, err
	}
	username := cc.Username
	if cred.Username != "" {
		username = cred.Username
	}

	reportStage(ctx, StageConnecting)
	start := time.Now()
	vim, err := newVimClient(ctx, cc, u, dialer, opts.UserAgent)
	if err != nil {
		return nil, err
	}
	reportStage(ctx, StageAuthenticating)
	gc := &govmomi.Client{Client: vim, SessionManager: session.NewManager(vim)}
	if err := gc.Login(ctx, url.UserPassword(username, cred.Password)); err != nil {
		return nil, fmt.Errorf("authenticate to %s as %s: %w", cc.Endpoint, username, err)
	}
	latency := time.Since(start)

	c := &Client{Context: cc, Latency: latency, vim: gc, dialer: dialer}
	// vAPI is optional: older vCenters and ESXi endpoints may not expose the
	// tagging service. Keep the authenticated SOAP connection usable and make
	// only tag metadata unavailable in that case.
	tagger := newTaggingClient(vim)
	if err := tagger.Login(ctx, url.UserPassword(username, cred.Password)); err != nil {
		c.restErr = err
	} else {
		c.tagger = tagger
	}
	c.About = aboutFrom(vim)
	return c, nil
}

func resolveCredential(ctx context.Context, cc *config.Context, opts ConnectOptions) (credentials.Credential, error) {
	if opts.Credential != nil {
		return *opts.Credential, nil
	}
	if opts.Resolver == nil {
		return credentials.Credential{}, fmt.Errorf("context %q: no credential source available", cc.Name)
	}
	// Several contexts may all be set to prompt, and they are connected to
	// concurrently: name the one being asked about.
	ref := cc.Credential.WithDefaultLabel(cc.Name)
	cred, _, err := credentials.Resolve(ctx, opts.Resolver, ref, cc.Name)
	if err != nil {
		return credentials.Credential{}, fmt.Errorf("context %q: %w", cc.Name, err)
	}
	return cred, nil
}

func newVimClient(ctx context.Context, cc *config.Context, u *url.URL, dialer transport.Dialer, userAgent string) (*vim25.Client, error) {
	tlsCfg, err := TLSConfig(cc)
	if err != nil {
		return nil, err
	}
	soapClient := soap.NewClient(u, cc.TLS.Mode == config.TLSInsecure)
	ht := transport.HTTPTransport(dialer)
	ht.TLSClientConfig = tlsCfg
	soapClient.Client.Transport = ht
	if userAgent != "" {
		soapClient.UserAgent = userAgent
	}
	vim, err := vim25.NewClient(ctx, soapClient)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", cc.Endpoint, err)
	}
	return vim, nil
}

func aboutFrom(vim *vim25.Client) About {
	a := vim.ServiceContent.About
	return About{
		Name:       a.Name,
		Version:    a.Version,
		Build:      a.Build,
		APIType:    a.ApiType,
		APIVersion: a.ApiVersion,
		OSType:     a.OsType,
		Vendor:     a.Vendor,
		InstanceID: a.InstanceUuid,
	}
}

// VIM exposes the underlying govmomi client. Only this package and its tests
// should need it; the rest of the application uses the inventory API.
func (c *Client) VIM() *vim25.Client { return c.vim.Client }

// Route renders how this client reaches its vCenter.
func (c *Client) Route() string { return c.dialer.Describe() }

// Ping issues a cheap authenticated call and returns how long it took. It is
// how the session manager decides a connection is still usable.
//
// It reads SessionManager.currentSession rather than calling SessionIsActive:
// the latter needs Sessions.ValidateSession, which the built-in ReadOnly role
// lacks, so a correctly scoped read-only account would fail its own
// connection test. The property read needs no privilege beyond a session and
// comes back empty once the session has expired.
func (c *Client) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	us, err := c.vim.SessionManager.UserSession(ctx)
	if err != nil {
		return 0, err
	}
	if us == nil {
		return 0, fmt.Errorf("session for %s is no longer active", c.Context.Name)
	}
	return time.Since(start), nil
}

// Close logs out. A vCenter session that is never released lingers for
// half an hour, which is visible to whoever else operates that vCenter.
func (c *Client) Close(ctx context.Context) error {
	if c.vim == nil {
		return nil
	}
	var errs []error
	if c.tagger != nil {
		if err := c.tagger.Logout(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if err := c.vim.Logout(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// NewClientForTest wraps an existing govmomi client so tests can exercise the
// inventory API against vcsim without going through Connect.
func NewClientForTest(cc *config.Context, gc *govmomi.Client) *Client {
	return &Client{
		Context: cc,
		About:   aboutFrom(gc.Client),
		vim:     gc,
		dialer:  transport.NewDirect(0),
	}
}
