package vsphere

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// License collection is opt-in and deliberately narrow.
//
// vSphere reports licenses through the read-only LicenseManager.licenses
// property and the read-only LicenseAssignmentManager.QueryAssignedLicenses
// operation. Both return the license KEY, which is a credential-like secret:
// anyone holding it can assign the entitlement elsewhere. This file therefore
// maps wire values into License records that have no key field at all. The key
// is used in memory for exactly one purpose — joining an assignment to the
// license it names — and is never stored, logged, exported or included in an
// error message.

// License coverage statuses. They extend the collection statuses used by the
// rest of the assessment ledger.
const (
	// LicenseStatusUnavailable means the account, or the vSphere version, could
	// not answer. It is never the same thing as a genuinely empty inventory.
	LicenseStatusUnavailable = "unavailable"
	// LicenseStatusPartial means the license list answered but host/entity
	// assignments did not, so per-entity attribution is missing.
	LicenseStatusPartial = "partial"
	// LicenseRedacted is the only value ever rendered in a license key column.
	LicenseRedacted = "[redacted]"
)

// LicenseAssignment is one entity that vSphere reports as holding a license.
type LicenseAssignment struct {
	// EntityID is the managed-object id (for example host-12) or, for the
	// vCenter Server itself, its instance UUID.
	EntityID string `json:"entity_id"`
	// EntityName is the display name vSphere reports for the entity.
	EntityName string `json:"entity_name,omitempty"`
	// EntityType is derived from EntityID: host, cluster, vcenter or other.
	EntityType string `json:"entity_type"`
	// Scope is the vCenter instance that owns the entity, when reported.
	Scope string `json:"scope,omitempty"`
}

// License is the persisted, key-free representation of one license record.
type License struct {
	// ID identifies the record within one collection only. It is an ordinal
	// assigned after a deterministic sort, never derived from the key, so it is
	// not stable across captures and must not be used to track a license over
	// time.
	ID         string `json:"id"`
	Name       string `json:"name"`
	EditionKey string `json:"edition_key,omitempty"`
	CostUnit   string `json:"cost_unit,omitempty"`
	// Total is the number of cost units the license holds. Used is the number
	// of units vSphere reports as consumed by assignments; vSphere omits it
	// when zero, so a zero is "none reported", not a verified zero.
	Total int32 `json:"total"`
	Used  int32 `json:"used"`
	// Expiration is nil for a license with no expiration property (perpetual
	// entitlements, and any server that omits it).
	Expiration *time.Time `json:"expiration,omitempty"`
	// Features are the feature names the license enables, sorted.
	Features []string `json:"features,omitempty"`
	// Assignments lists the entities vSphere says hold this license.
	Assignments []LicenseAssignment `json:"assignments,omitempty"`
	// APIVersion is the vSphere API version that answered, kept so a reader
	// can tell which server generation produced the numbers.
	APIVersion string `json:"api_version,omitempty"`
}

// LicenseInventory is the result of one license collection.
type LicenseInventory struct {
	Licenses []License
	// Status is success, partial or unavailable. Empty is never used: zero
	// records is reported as unavailable (see buildLicenseInventory).
	Status string
	// Message explains any status other than success and empty. It never
	// contains a license key.
	Message string
}

// errLicenseAssignmentsUnsupported means the server has no license assignment
// manager (a standalone ESXi host, for example).
var errLicenseAssignmentsUnsupported = errors.New("license assignment manager is not offered by this server")

// FetchLicenses reads license metadata with two read-only calls: the
// LicenseManager "licenses" property and QueryAssignedLicenses. It always
// returns a status; failure to read is reported as LicenseStatusUnavailable
// rather than as an empty list.
//
// It deliberately avoids govmomi's license package, whose Manager also exposes
// AddLicense, RemoveLicense and UpdateLicenseLabel; see license_query.go.
func (c *Client) FetchLicenses(ctx context.Context) LicenseInventory {
	if c == nil || c.vim == nil || c.vim.Client == nil || c.vim.ServiceContent.LicenseManager == nil {
		return LicenseInventory{Status: LicenseStatusUnavailable, Message: "unsupported: this endpoint does not expose a license manager"}
	}
	var manager mo.LicenseManager
	listErr := property.DefaultCollector(c.vim.Client).RetrieveOne(ctx, *c.vim.ServiceContent.LicenseManager, []string{"licenses", "licenseAssignmentManager"}, &manager)
	var assigned []types.LicenseAssignmentManagerLicenseAssignment
	var assignErr error
	if listErr == nil {
		if manager.LicenseAssignmentManager == nil {
			assignErr = errLicenseAssignmentsUnsupported
		} else {
			assigned, assignErr = queryAssignedLicenses(ctx, c.vim.Client, *manager.LicenseAssignmentManager)
		}
	}
	return buildLicenseInventory(licenseWire{
		Infos:      manager.Licenses,
		ListErr:    listErr,
		Assigned:   assigned,
		AssignErr:  assignErr,
		APIVersion: c.About.APIVersion,
	})
}

// licenseWire is the raw wire data, gathered so the mapping is a pure function
// that tests can drive without a server.
type licenseWire struct {
	Infos      []types.LicenseManagerLicenseInfo
	ListErr    error
	Assigned   []types.LicenseAssignmentManagerLicenseAssignment
	AssignErr  error
	APIVersion string
}

func buildLicenseInventory(w licenseWire) LicenseInventory {
	if w.ListErr != nil {
		return LicenseInventory{Status: LicenseStatusUnavailable, Message: describeLicenseError("license list", w.ListErr, nil)}
	}

	type entry struct {
		key     string
		license License
	}
	byKey := make(map[string]*entry)
	var order []*entry
	add := func(info types.LicenseManagerLicenseInfo) *entry {
		if e, ok := byKey[info.LicenseKey]; ok {
			return e
		}
		e := &entry{key: info.LicenseKey, license: licenseFromInfo(info, w.APIVersion)}
		byKey[info.LicenseKey] = e
		order = append(order, e)
		return e
	}
	for _, info := range w.Infos {
		add(info)
	}
	// An assignment can name a license the list did not return (for example a
	// license the account may see only through its assignment). Keep it: the
	// entity does hold it, and dropping it would understate usage.
	if w.AssignErr == nil {
		for _, a := range w.Assigned {
			e := add(a.AssignedLicense)
			e.license.Assignments = append(e.license.Assignments, LicenseAssignment{
				EntityID:   a.EntityId,
				EntityName: a.EntityDisplayName,
				EntityType: licenseEntityType(a.EntityId),
				Scope:      a.Scope,
			})
		}
	}

	keys := make([]string, 0, len(order))
	for _, e := range order {
		keys = append(keys, e.key)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i].license, order[j].license
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.EditionKey != b.EditionKey {
			return a.EditionKey < b.EditionKey
		}
		if a.Total != b.Total {
			return a.Total < b.Total
		}
		if a.Used != b.Used {
			return a.Used < b.Used
		}
		ae, be := expirationSortKey(a.Expiration), expirationSortKey(b.Expiration)
		if ae != be {
			return ae < be
		}
		// Ties between otherwise identical licenses are broken by the key so the
		// ordinal is reproducible for one server state. The key itself is
		// discarded with this closure.
		return order[i].key < order[j].key
	})
	out := make([]License, 0, len(order))
	for i, e := range order {
		l := e.license
		l.ID = fmt.Sprintf("license-%03d", i+1)
		sort.SliceStable(l.Assignments, func(a, b int) bool {
			if l.Assignments[a].EntityType != l.Assignments[b].EntityType {
				return l.Assignments[a].EntityType < l.Assignments[b].EntityType
			}
			if l.Assignments[a].EntityName != l.Assignments[b].EntityName {
				return l.Assignments[a].EntityName < l.Assignments[b].EntityName
			}
			return l.Assignments[a].EntityID < l.Assignments[b].EntityID
		})
		out = append(out, l)
	}

	inv := LicenseInventory{Licenses: out, Status: "success"}
	assignNote := ""
	if w.AssignErr != nil {
		assignNote = describeLicenseError("license assignments", w.AssignErr, keys)
	}
	switch {
	case len(out) == 0:
		// Never report "empty". A vCenter always holds at least an evaluation
		// license, and an account without the license privilege can be handed
		// an empty list rather than an error (RVTools writes an empty vLicense
		// sheet for such an account without warning). Zero records is
		// therefore indistinguishable from denied access and is reported as
		// unavailable so it cannot read as a complete, license-free estate.
		inv.Status = LicenseStatusUnavailable
		inv.Message = "no license records were returned; vSphere can return an empty list instead of an error to an account without the Global.Licenses privilege, and every vCenter and ESXi host holds at least an evaluation license, so this is treated as unavailable rather than as zero licenses"
		if assignNote != "" {
			inv.Message += "; " + assignNote
		}
	case len(w.Infos) == 0:
		inv.Status = LicenseStatusPartial
		inv.Message = "the license list returned no records; the licenses shown were reported only through entity assignments, so totals and other licenses may be missing"
	case assignNote != "":
		inv.Status = LicenseStatusPartial
		inv.Message = assignNote + "; license totals and usage are recorded but which hosts hold each license is not"
	}
	return inv
}

func licenseFromInfo(info types.LicenseManagerLicenseInfo, apiVersion string) License {
	l := License{
		Name:       info.Name,
		EditionKey: info.EditionKey,
		CostUnit:   info.CostUnit,
		Total:      info.Total,
		Used:       info.Used,
		APIVersion: apiVersion,
	}
	seen := make(map[string]bool)
	for _, p := range info.Properties {
		switch strings.ToLower(p.Key) {
		case "expirationdate":
			if t, ok := licenseTime(p.Value); ok {
				utc := t.UTC()
				l.Expiration = &utc
			}
		case "feature":
			if name := licenseFeatureName(p.Value); name != "" && !seen[name] {
				seen[name] = true
				l.Features = append(l.Features, name)
			}
		}
	}
	sort.Strings(l.Features)
	return l
}

func licenseTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case *time.Time:
		if t != nil {
			return *t, !t.IsZero()
		}
	}
	return time.Time{}, false
}

func licenseFeatureName(v any) string {
	switch f := v.(type) {
	case types.KeyValue:
		return firstNonEmpty(f.Value, f.Key)
	case *types.KeyValue:
		if f != nil {
			return firstNonEmpty(f.Value, f.Key)
		}
	case types.LicenseFeatureInfo:
		return firstNonEmpty(f.FeatureName, f.Key)
	case *types.LicenseFeatureInfo:
		if f != nil {
			return firstNonEmpty(f.FeatureName, f.Key)
		}
	case string:
		return f
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func expirationSortKey(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixNano()
}

func licenseEntityType(id string) string {
	switch {
	case strings.HasPrefix(id, "host-"):
		return "host"
	case strings.HasPrefix(id, "domain-c"):
		return "cluster"
	case isUUIDLike(id):
		return "vcenter"
	}
	return "other"
}

func isUUIDLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}

// describeLicenseError turns a wire error into a message that separates denied
// access and unsupported servers from every other failure. Any known key is
// scrubbed from the text as a last line of defense.
func describeLicenseError(what string, err error, knownKeys []string) string {
	var msg string
	switch {
	case isNoPermission(err):
		privilege := "Global.Licenses"
		if p := noPermissionPrivilege(err); p != "" {
			privilege = p
		}
		msg = fmt.Sprintf("permission denied reading %s: the account lacks the %s privilege", what, privilege)
	case errors.Is(err, errLicenseAssignmentsUnsupported) || isMethodNotFound(err):
		msg = fmt.Sprintf("unsupported: this vSphere version does not provide %s", what)
	default:
		msg = fmt.Sprintf("%s could not be read: %v", what, err)
	}
	return redactLicenseKeys(msg, knownKeys)
}

// licenseFault returns the vSphere fault carried by err, normalized to a
// pointer, or nil. Faults arrive as a SOAP fault detail (a value) from a real
// server and may be wrapped as a pointer by other paths.
func licenseFault(err error) any {
	var fault any
	switch {
	case soap.IsSoapFault(err):
		fault = soap.ToSoapFault(err).VimFault()
	case soap.IsVimFault(err):
		fault = soap.ToVimFault(err)
	default:
		return nil
	}
	switch f := fault.(type) {
	case types.NoPermission:
		return &f
	case types.MethodNotFound:
		return &f
	case types.NotSupported:
		return &f
	case *types.NoPermission, *types.MethodNotFound, *types.NotSupported:
		return f
	}
	return nil
}

func isNoPermission(err error) bool {
	_, ok := licenseFault(err).(*types.NoPermission)
	return ok
}

func noPermissionPrivilege(err error) string {
	if f, ok := licenseFault(err).(*types.NoPermission); ok && f != nil {
		return f.PrivilegeId
	}
	return ""
}

func isMethodNotFound(err error) bool {
	switch licenseFault(err).(type) {
	case *types.MethodNotFound, *types.NotSupported:
		return true
	}
	return false
}

// redactLicenseKeys replaces every occurrence of a known key with a marker.
func redactLicenseKeys(text string, keys []string) string {
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		text = strings.ReplaceAll(text, key, LicenseRedacted)
	}
	return text
}
