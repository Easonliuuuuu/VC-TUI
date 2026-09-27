package vsphere

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vapi/rest"
	_ "github.com/vmware/govmomi/vapi/simulator"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/transport"
)

func TestCollectMetadataNormalizesCustomFieldsAndReportsTagAvailability(t *testing.T) {
	ref := types.ManagedObjectReference{Type: "VirtualMachine", Value: "vm-1"}
	c := &Client{
		customFieldsLoaded: true,
		customFields:       []types.CustomFieldDef{{Key: 42, Name: "environment"}},
	}
	values := map[types.ManagedObjectReference][]types.BaseCustomFieldValue{
		ref: {&types.CustomFieldStringValue{CustomFieldValue: types.CustomFieldValue{Key: 42}, Value: "prod"}},
	}
	got := c.collectMetadata(context.Background(), []types.ManagedObjectReference{ref}, values)[ref]
	if got.CustomAttributesStatus != "available" || len(got.CustomAttributes) != 1 {
		t.Fatalf("custom metadata=%+v", got)
	}
	if got.CustomAttributes[0].Name != "environment" || got.CustomAttributes[0].Value != "prod" {
		t.Fatalf("custom attribute=%+v", got.CustomAttributes[0])
	}
	if got.TagsStatus != "unavailable" || got.TagsError == "" {
		t.Fatalf("tag availability=%+v", got)
	}
}

func TestListVMsCollectsSimulatorTagsAndCustomAttributes(t *testing.T) {
	model := simulator.VPX()
	model.Datacenter, model.Machine = 1, 1
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	model.Service.RegisterEndpoints = true
	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	ctx := context.Background()
	gc, err := govmomi.NewClient(ctx, server.URL, true)
	if err != nil {
		t.Fatalf("connect to simulator: %v", err)
	}
	t.Cleanup(func() { _ = gc.Logout(ctx) })
	rc := rest.NewClient(gc.Client)
	if err := rc.Login(ctx, simulator.DefaultLogin); err != nil {
		t.Fatalf("login vAPI: %v", err)
	}
	t.Cleanup(func() { _ = rc.Logout(ctx) })
	cc := &config.Context{Name: "sim", Endpoint: server.URL.String(), Username: "user", TLS: config.TLSConfig{Mode: config.TLSInsecure}}
	cc.Normalize()
	client := NewClientForTest(cc, gc)
	client.tagger = newTaggingClient(gc.Client)
	if err := client.tagger.Login(ctx, simulator.DefaultLogin); err != nil {
		t.Fatalf("login tagging client: %v", err)
	}
	t.Cleanup(func() { _ = client.tagger.Logout(ctx) })
	initial, err := client.ListVMs(ctx)
	if err != nil || len(initial) == 0 {
		t.Fatalf("list initial VMs: %v (%d VMs)", err, len(initial))
	}
	ref := types.ManagedObjectReference{Type: "VirtualMachine", Value: initial[0].ID}
	tagsManager := tags.NewManager(rc)
	categoryID, err := tagsManager.CreateCategory(ctx, &tags.Category{Name: "Environment", Cardinality: "MULTIPLE", AssociableTypes: []string{"VirtualMachine"}})
	if err != nil {
		t.Fatalf("create tag category: %v", err)
	}
	if _, err := tagsManager.CreateTag(ctx, &tags.Tag{Name: "Production", CategoryID: categoryID}); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	tagList, err := tagsManager.GetTags(ctx)
	if err != nil || len(tagList) == 0 {
		t.Fatalf("list tags: %v", err)
	}
	if err := tagsManager.AttachTag(ctx, tagList[0].ID, ref); err != nil {
		t.Fatalf("attach tag: %v", err)
	}

	fields, err := object.GetCustomFieldsManager(gc.Client)
	if err != nil {
		t.Fatalf("get custom fields manager: %v", err)
	}
	field, err := fields.Add(ctx, "environment", "VirtualMachine", nil, nil)
	if err != nil {
		t.Fatalf("add custom field: %v", err)
	}
	if err := fields.Set(ctx, ref, field.Key, "prod"); err != nil {
		t.Fatalf("set custom field: %v", err)
	}
	got, err := client.ListVMs(ctx)
	if err != nil {
		t.Fatalf("list VMs: %v", err)
	}
	if len(got) == 0 || got[0].Metadata.TagsStatus != "available" || got[0].Metadata.CustomAttributesStatus != "available" {
		t.Fatalf("metadata coverage=%+v", got)
	}
	if len(got[0].Metadata.Tags) != 1 || got[0].Metadata.Tags[0].Name != "Production" || got[0].Metadata.Tags[0].Category != "Environment" {
		t.Fatalf("tags=%+v vm=%+v", got[0].Metadata.Tags, got[0])
	}
	if len(got[0].Metadata.CustomAttributes) != 1 || got[0].Metadata.CustomAttributes[0].Name != "environment" || got[0].Metadata.CustomAttributes[0].Value != "prod" {
		t.Fatalf("custom attributes=%+v", got[0].Metadata.CustomAttributes)
	}
}

func TestTaggingClientUsesThePinnedThumbprint(t *testing.T) {
	model := simulator.VPX()
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)
	model.Service.RegisterEndpoints = true
	model.Service.TLS = new(tls.Config)
	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	// The simulator's certificate is not signed by any system CA, like a
	// vCenter's default VMCA certificate: only the pinned thumbprint trusts it.
	cc := &config.Context{
		Name: "sim", Endpoint: server.URL.String(), Username: "user",
		TLS: config.TLSConfig{Mode: config.TLSThumbprint, Thumbprint: ThumbprintSHA256(server.Certificate())},
	}
	cc.Normalize()
	u, err := cc.URL()
	if err != nil {
		t.Fatalf("context URL: %v", err)
	}
	ctx := context.Background()
	vim, err := newVimClient(ctx, cc, u, transport.NewDirect(5*time.Second), "")
	if err != nil {
		t.Fatalf("connect with the pinned thumbprint: %v", err)
	}
	tagger := newTaggingClient(vim)
	if err := tagger.Login(ctx, simulator.DefaultLogin); err != nil {
		t.Fatalf("tagging client login with the pinned thumbprint: %v", err)
	}
	t.Cleanup(func() { _ = tagger.Logout(ctx) })

	// A wrong pin must fail the REST endpoint too, not fall back to the CAs.
	wrong := *cc
	wrong.TLS.Thumbprint = strings.Repeat("AB:", 31) + "AB"
	wrongTLS, err := TLSConfig(&wrong)
	if err != nil {
		t.Fatalf("TLS config for the wrong pin: %v", err)
	}
	vim.Client.Client.Transport.(*http.Transport).TLSClientConfig = wrongTLS
	vim.Client.Client.Transport.(*http.Transport).CloseIdleConnections()
	var mismatch *ThumbprintMismatchError
	if err := newTaggingClient(vim).Login(ctx, simulator.DefaultLogin); !errors.As(err, &mismatch) {
		t.Fatalf("tagging client login with a wrong pin: %v, want a thumbprint mismatch", err)
	}
}
