package vsphere

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

const (
	taggingRESTBase    = "/rest/com/vmware/cis/tagging"
	tagAssociationPath = taggingRESTBase + "/tag-association"
	tagPath            = taggingRESTBase + "/tag/"
	categoryPath       = taggingRESTBase + "/category/"
	restSessionPath    = "/rest/com/vmware/cis/session"
	restSessionHeader  = "vmware-api-session-id"
	restHeaderAuthn    = "vmware-use-header-authn"
)

type taggingClient struct {
	client    *soap.Client
	mu        sync.Mutex
	sessionID string
}

type associatedObject struct {
	Type  string `json:"type"`
	Value string `json:"id"`
}

type attachedTags struct {
	ObjectID associatedObject `json:"object_id"`
	TagIDs   []string         `json:"tag_ids"`
}

type tagInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CategoryID string `json:"category_id"`
}

type categoryInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func newTaggingClient(vim *vim25.Client) *taggingClient {
	rest := vim.Client.NewServiceClient("/rest", "")
	// NewServiceClient starts from the transport soap.NewClient built, not the
	// one newVimClient installed afterwards, so the REST client would otherwise
	// verify against the system CAs and dial directly. Share the SOAP
	// transport: same TLS policy (pinned thumbprint included) and same dialer.
	rest.Client.Transport = vim.Client.Client.Transport
	return &taggingClient{client: rest}
}

func (c *taggingClient) session() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *taggingClient) Login(ctx context.Context, user *url.Userinfo) error {
	req, err := c.request(http.MethodPost, restSessionPath, "")
	if err != nil {
		return err
	}
	req.Header.Set(restHeaderAuthn, "true")
	if user != nil {
		password, ok := user.Password()
		if ok {
			req.SetBasicAuth(user.Username(), password)
		}
	}
	var id string
	if err := c.do(ctx, req, &id); err != nil {
		return err
	}
	c.mu.Lock()
	c.sessionID = id
	c.mu.Unlock()
	return nil
}

func (c *taggingClient) Logout(ctx context.Context) error {
	if c.session() == "" {
		return nil
	}
	req, err := c.request(http.MethodDelete, restSessionPath, "")
	if err != nil {
		return err
	}
	return c.do(ctx, req, nil)
}

func (c *taggingClient) request(method, path, rawQuery string) (*http.Request, error) {
	u := c.client.URL()
	u.Path = path
	u.RawQuery = rawQuery
	req, err := http.NewRequest(method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if id := c.session(); id != "" {
		req.Header.Set(restSessionHeader, id)
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func (c *taggingClient) do(ctx context.Context, req *http.Request, out any) error {
	if req.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.client.Do(ctx, req, func(res *http.Response) error {
		if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
			detail, _ := io.ReadAll(res.Body)
			return fmt.Errorf("%s: %s", res.Status, bytes.TrimSpace(detail))
		}
		if out == nil || res.StatusCode == http.StatusNoContent {
			return nil
		}
		var envelope struct {
			Value json.RawMessage `json:"value"`
		}
		if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
			return err
		}
		if len(envelope.Value) == 0 || string(envelope.Value) == "null" {
			return nil
		}
		return json.Unmarshal(envelope.Value, out)
	})
}

func (c *taggingClient) listAttachedTags(ctx context.Context, refs []types.ManagedObjectReference) ([]attachedTags, error) {
	objects := make([]map[string]string, 0, len(refs))
	for _, ref := range refs {
		objects = append(objects, map[string]string{"type": ref.Type, "id": ref.Value})
	}
	body, err := json.Marshal(map[string]any{"object_ids": objects})
	if err != nil {
		return nil, err
	}
	req, err := c.request(http.MethodPost, tagAssociationPath, "~action=list-attached-tags-on-objects")
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	returnValue := []attachedTags{}
	if err := c.do(ctx, req, &returnValue); err != nil {
		return nil, err
	}
	return returnValue, nil
}

func (c *taggingClient) getTag(ctx context.Context, id string) (tagInfo, error) {
	req, err := c.request(http.MethodGet, tagPath+"id:"+id, "")
	if err != nil {
		return tagInfo{}, err
	}
	var result tagInfo
	if err := c.do(ctx, req, &result); err != nil {
		return tagInfo{}, err
	}
	return result, nil
}

func (c *taggingClient) getCategory(ctx context.Context, id string) (categoryInfo, error) {
	req, err := c.request(http.MethodGet, categoryPath+"id:"+id, "")
	if err != nil {
		return categoryInfo{}, err
	}
	var result categoryInfo
	if err := c.do(ctx, req, &result); err != nil {
		return categoryInfo{}, err
	}
	return result, nil
}

// collectMetadata performs one bulk tag-association request for the supplied
// objects and resolves custom values from the same PropertyCollector response
// that produced the objects. Tag and category definitions are cached per
// authenticated connection, so work scales with pages and unique metadata,
// never with one request per object.
func (c *Client) collectMetadata(ctx context.Context, refs []types.ManagedObjectReference, values map[types.ManagedObjectReference][]types.BaseCustomFieldValue) map[types.ManagedObjectReference]Metadata {
	out := make(map[types.ManagedObjectReference]Metadata, len(refs))
	for _, ref := range refs {
		out[ref] = Metadata{Tags: []Tag{}, CustomAttributes: []CustomAttribute{}, TagsStatus: "available", CustomAttributesStatus: "available"}
	}

	defs := map[int32]types.CustomFieldDef{}
	var customErr error
	for _, fields := range values {
		if len(fields) > 0 {
			defs, customErr = c.customFieldDefinitions(ctx)
			break
		}
	}
	for ref, fields := range values {
		m, ok := out[ref]
		if !ok {
			continue
		}
		if customErr != nil {
			m.CustomAttributesStatus = "unavailable"
			m.CustomAttributesError = customErr.Error()
		} else {
			for _, field := range fields {
				value := ""
				var base *types.CustomFieldValue
				switch v := field.(type) {
				case *types.CustomFieldStringValue:
					if v == nil {
						continue
					}
					base, value = &v.CustomFieldValue, v.Value
				default:
					// vSphere currently exposes string values only. Preserve
					// the key even when a newer server sends an unknown subtype;
					// its value remains explicitly empty rather than being guessed.
					if field == nil {
						continue
					}
					base = field.GetCustomFieldValue()
				}
				if base == nil {
					continue
				}
				key := base.Key
				name := strconv.Itoa(int(key))
				if def, ok := defs[key]; ok && def.Name != "" {
					name = def.Name
				}
				m.CustomAttributes = append(m.CustomAttributes, CustomAttribute{Key: key, Name: name, Value: value})
			}
			sort.Slice(m.CustomAttributes, func(i, j int) bool { return m.CustomAttributes[i].Key < m.CustomAttributes[j].Key })
		}
		out[ref] = m
	}

	if c.tagger == nil {
		for ref, m := range out {
			m.TagsStatus = "unavailable"
			if c.restErr != nil {
				m.TagsError = c.restErr.Error()
			} else {
				m.TagsError = "vSphere tagging API is unavailable"
			}
			out[ref] = m
		}
		return out
	}
	attached, err := c.restTags(ctx, refs)
	if err != nil {
		for ref, m := range out {
			m.TagsStatus, m.TagsError = "unavailable", err.Error()
			out[ref] = m
		}
		return out
	}
	for _, item := range attached {
		ref := types.ManagedObjectReference{Type: item.ObjectID.Type, Value: item.ObjectID.Value}
		m, ok := out[ref]
		if !ok {
			continue
		}
		for _, id := range item.TagIDs {
			tag, err := c.tagDefinition(ctx, id)
			if err != nil {
				m.TagsStatus, m.TagsError = "unavailable", err.Error()
				break
			}
			m.Tags = append(m.Tags, tag)
		}
		sort.Slice(m.Tags, func(i, j int) bool {
			if m.Tags[i].Category != m.Tags[j].Category {
				return m.Tags[i].Category < m.Tags[j].Category
			}
			return m.Tags[i].Name < m.Tags[j].Name
		})
		out[ref] = m
	}
	return out
}

func (c *Client) restTags(ctx context.Context, refs []types.ManagedObjectReference) ([]attachedTags, error) {
	return c.tagger.listAttachedTags(ctx, refs)
}

func (c *Client) tagDefinition(ctx context.Context, id string) (Tag, error) {
	c.metadataMu.Lock()
	if c.tagCache == nil {
		c.tagCache = make(map[string]Tag)
	}
	if tag, ok := c.tagCache[id]; ok {
		c.metadataMu.Unlock()
		return tag, nil
	}
	c.metadataMu.Unlock()

	v, err := c.tagger.getTag(ctx, id)
	if err != nil {
		return Tag{}, fmt.Errorf("get tag %s: %w", id, err)
	}
	category := ""
	if v.CategoryID != "" {
		c.metadataMu.Lock()
		if c.categoryCache == nil {
			c.categoryCache = make(map[string]string)
		}
		category = c.categoryCache[v.CategoryID]
		c.metadataMu.Unlock()
		if category == "" {
			cat, catErr := c.tagger.getCategory(ctx, v.CategoryID)
			if catErr != nil {
				return Tag{}, fmt.Errorf("get tag category %s: %w", v.CategoryID, catErr)
			}
			category = cat.Name
			c.metadataMu.Lock()
			c.categoryCache[v.CategoryID] = category
			c.metadataMu.Unlock()
		}
	}
	tag := Tag{ID: v.ID, Name: v.Name, CategoryID: v.CategoryID, Category: category}
	c.metadataMu.Lock()
	c.tagCache[id] = tag
	c.metadataMu.Unlock()
	return tag, nil
}

func (c *Client) customFieldDefinitions(ctx context.Context) (map[int32]types.CustomFieldDef, error) {
	c.metadataMu.Lock()
	if c.customFieldsLoaded {
		defs := make(map[int32]types.CustomFieldDef, len(c.customFields))
		for _, def := range c.customFields {
			defs[def.Key] = def
		}
		err := c.customFieldsErr
		c.metadataMu.Unlock()
		return defs, err
	}
	c.metadataMu.Unlock()
	var fields []types.CustomFieldDef
	var err error
	if c.vim == nil || c.vim.Client == nil || c.vim.ServiceContent.CustomFieldsManager == nil {
		err = fmt.Errorf("vSphere custom fields manager is unavailable")
	} else {
		var manager mo.CustomFieldsManager
		err = property.DefaultCollector(c.vim.Client).RetrieveOne(ctx, *c.vim.ServiceContent.CustomFieldsManager, []string{"field"}, &manager)
		fields = manager.Field
	}
	c.metadataMu.Lock()
	c.customFields = append([]types.CustomFieldDef(nil), fields...)
	c.customFieldsErr = err
	c.customFieldsLoaded = true
	defs := make(map[int32]types.CustomFieldDef, len(fields))
	for _, def := range fields {
		defs[def.Key] = def
	}
	c.metadataMu.Unlock()
	return defs, err
}
