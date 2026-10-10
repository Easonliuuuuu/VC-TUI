package tests

import (
	"bytes"
	"encoding/xml"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/vmware/govmomi/simulator"
)

// This file is the standing proof that vsfleet is read-only against a real
// vCenter. Reading the source says the same thing — nothing imports
// govmomi/object, where every mutating operation lives — but source reading
// is not evidence anyone can re-run before pointing this at production. This
// records every SOAP operation the tool actually puts on the wire during a
// full exercise of every command that talks to vCenter, and fails on
// anything outside a small, explicitly justified allowlist.
//
// The allowlist is deliberately exhaustive rather than a deny-list of known
// dangerous verbs: a new call has to be justified here before it can ship,
// which is the property that actually protects an operator's estate.
//
// What this guarantees is specifically about the vSphere API: vsfleet cannot
// mutate a vCenter's state through it. The TUI's detail-pane handoff actions
// (package tui, see actions.go) are a deliberately separate class of action
// outside that guarantee — SSH, "open in browser" and clipboard copy launch
// real local processes on the operator's own workstation, through
// os/exec and a browser, never through vSphere's SOAP or REST APIs. That is
// why they are not, and should not be, reachable from this file's allowlist
// or from mutatingPackages below: the guarantee this file proves is that
// vsfleet's own calls to vCenter cannot cause harm, not that every action a
// keypress can trigger is a no-op.

// readOnlyMethods are the vSphere operations vsfleet is allowed to invoke.
//
// Several of these are not literal reads, and each is here for a stated
// reason. What they have in common is that every object they create or
// destroy is one this tool made for its own query, is scoped to this
// session, and holds nothing but references — none of them can reach an
// object in inventory.
//
//   - Login/Logout create and release a session. Nothing else can be read
//     without one, and Logout is what keeps abandoned sessions from
//     lingering for half an hour on someone else's vCenter.
//   - CreateContainerView/DestroyView create and destroy a transient,
//     session-scoped view object — the standard govmomi way to enumerate
//     inventory. A view holds references to objects; creating one changes
//     nothing about the objects themselves, and destroying it is the cleanup
//     of the thing this tool just created, never of anything in inventory.
//   - CreatePropertyCollector/CreateFilter/DestroyPropertyCollector are the
//     same bargain for the paged read the interface uses on a large estate.
//     The filter describes which properties to report; the collector is a
//     private cursor over them, needed because the shared default collector
//     serves one caller at a time and the interface reads several resource
//     kinds at once. Destroying the collector releases the filter with it.
//     DestroyPropertyFilter is therefore absent: it is never called.
//
// Everything else is a pure read.
//
// Nothing else belongs here without a specific reason. In particular
// RetrieveManagedMethodExecuter is deliberately absent: it is a mechanism
// for invoking arbitrary operations, so approving it would defeat the point
// of the list.
var readOnlyMethods = map[string]string{
	"RetrieveServiceContent":         "read: the service's own capability document, on connect",
	"Login":                          "session: nothing is readable without one",
	"Logout":                         "session: release it rather than leaving it to time out",
	"CreateContainerView":            "transient view object this tool creates for its own enumeration",
	"DestroyView":                    "cleanup of that same view; never touches inventory",
	"RetrievePropertiesEx":           "read: the property collector, how all inventory is enumerated",
	"ContinueRetrievePropertiesEx":   "read: the next page of that same enumeration",
	"CancelRetrievePropertiesEx":     "read path: abandons a paged enumeration",
	"RetrieveProperties":             "read: the pre-6.0 property collector call",
	"CreatePropertyCollector":        "this tool's own private cursor for a paged read; session-scoped, holds no inventory",
	"CreateFilter":                   "read: declares which properties that cursor should report",
	"WaitForUpdatesEx":               "read: reports those property values, a page at a time",
	"DestroyPropertyCollector":       "cleanup of that same cursor and its filter; never touches inventory",
	"SearchDatastoreSubFolders_Task": "read: creates a task only as a handle for directory listing and returns file metadata; cannot modify inventory",
	"SearchDatastore_Task":           "read: the same bargain for one directory rather than a whole tree — the interactive datastore file browser; returns file metadata and cannot modify inventory",
	"QueryAssignedLicenses":          "read: lists which entities hold which licenses for opt-in license collection (assessment run --include-licenses); returns license metadata and cannot assign, remove or alter a license. The license list itself is the LicenseManager.licenses property, read through the property collector",
	"QueryPerf":                      "read: returns performance statistics for the entities it is given; changes nothing on the server. Counters and intervals come from the property collector, not a second operation",
	"QueryEvents":                    "read: returns the event log entries matching a filter (vm events, the TUI timeline's vCenter events); unlike CreateCollectorForEvents it leaves no collector behind, and it changes nothing on the server",
}

// soapRecorder collects the operation name of every SOAP request that
// reaches the simulator.
type soapRecorder struct {
	mu      sync.Mutex
	methods []string
}

func (r *soapRecorder) record(method string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.methods = append(r.methods, method)
}

// distinct returns the set of operations seen, sorted, for a stable report.
func (r *soapRecorder) distinct() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, m := range r.methods {
		seen[m] = true
	}
	out := make([]string, 0, len(seen))
	for m := range seen {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// soapMethod pulls the operation name out of a SOAP envelope: the first
// element inside <Body>. Reading it off the wire rather than instrumenting
// govmomi is the point — this sees exactly what a real vCenter would.
func soapMethod(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	inBody := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if !inBody {
			if se.Name.Local == "Body" {
				inBody = true
			}
			continue
		}
		return se.Name.Local
	}
}

// startRecordingVCenter starts a plain-HTTP vcsim with a recording reverse
// proxy in front of it, and returns the endpoint to point a context at.
// Plain HTTP keeps the recorder to a few lines; the TLS paths are covered by
// the other tests in this package.
func startRecordingVCenter(t *testing.T) (string, *soapRecorder) {
	t.Helper()
	model := simulator.VPX()
	model.Datacenter = 1
	model.Cluster = 1
	model.ClusterHost = 2
	model.App = 1
	model.Machine = 2
	model.Datastore = 2
	if err := model.Create(); err != nil {
		t.Fatalf("create simulator model: %v", err)
	}
	t.Cleanup(model.Remove)

	server := model.Service.NewServer()
	t.Cleanup(server.Close)

	target := &url.URL{Scheme: "http", Host: server.URL.Host}
	proxy := httputil.NewSingleHostReverseProxy(target)
	rec := &soapRecorder{}

	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		if m := soapMethod(body); m != "" {
			rec.record(m)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	return front.URL, rec
}

// TestEveryCommandIsReadOnly drives every command that talks to a vCenter and
// asserts that nothing outside readOnlyMethods was ever sent.
func TestEveryCommandIsReadOnly(t *testing.T) {
	// The assessment commands below write a history ledger. Keep it in a
	// throwaway location so this test never reads or writes an operator's real
	// history database.
	t.Setenv("VSFLEET_HISTORY_DB", filepath.Join(t.TempDir(), "history.db"))
	endpoint, rec := startRecordingVCenter(t)

	r := newRunner(t)
	r.mustRun(testPassword+"\n"+testPassword+"\n", "context", "add",
		"--name", "audit",
		"--endpoint", endpoint,
		"--username", "operator@vsphere.local",
		"--credential", "prompt",
		"--password-stdin",
		"--tls", "insecure",
	)

	// Every command that reaches a vCenter, including the ones that connect
	// as a side effect of doing something else.
	for _, args := range [][]string{
		{"context", "test", "audit"},
		{"doctor", "audit"},
		{"status"},
		{"vm", "list"},
		{"template", "list"},
		{"host", "list"},
		{"cluster", "list"},
		{"vapp", "list"},
		{"datastore", "list"},
		{"datastore", "files", "list", "LocalDS_0"},
		{"datastore", "files", "find", "LocalDS_0", "*.vmdk"},
		{"network", "list"},
		{"vm", "list", "--all-contexts"},
		{"vm", "show", "DC0_C0_APP0_VM0"},
		{"vm", "events", "DC0_C0_APP0_VM0", "--all"},
		{"host", "show", "DC0_C0_H0"},
		{"cluster", "show", "DC0_C0"},
		{"vapp", "show", "DC0_C0_APP0"},
		{"datastore", "show", "LocalDS_0"},
		{"network", "show", "VM Network"},
		{"host", "hba", "list"},
		{"host", "multipath", "list"},
		{"host", "pnic", "list"},
		{"host", "vswitch", "list"},
		{"host", "portgroup", "list"},
		{"host", "vmkernel", "list"},
		{"dvswitch", "list"},
		{"dvportgroup", "list"},
		{"resourcepool", "list"},
		{"snapshot", "list"},
		{"search", "DC0"},
		{"search", "LocalDS", "--kind", "datastore"},
		{"assessment", "run", "--browse-datastores"},
		{"assessment", "run", "--include-licenses"},
		{"assessment", "run", "--datastore-file-inventory"},
		{"assessment", "perf", "collect", "--window", "24h"},
		{"assessment", "perf", "list"},
		{"assessment", "perf", "show", "latest"},
	} {
		stdout, stderr, err := r.run(testPassword+"\n", args...)
		if err != nil {
			t.Fatalf("vsfleet %s failed: %v\nstdout:\n%s\nstderr:\n%s",
				strings.Join(args, " "), err, stdout, stderr)
		}
	}

	seen := rec.distinct()
	if len(seen) == 0 {
		t.Fatal("no SOAP traffic recorded; the recording proxy is not in the path")
	}
	t.Logf("operations sent to vCenter across every command:\n  %s", strings.Join(seen, "\n  "))

	for _, m := range seen {
		if _, ok := readOnlyMethods[m]; !ok {
			t.Errorf("vsfleet sent %q, which is not an approved read-only operation.\n"+
				"If this is genuinely safe, add it to readOnlyMethods with the reason. "+
				"If it modifies a vCenter, it must not ship.", m)
		}
	}
}

// TestOnlyPerfAndBrowserShimsDefineFault keeps the deliberate exceptions to
// the package-level mutation guard narrow and reviewable. A hand-rolled SOAP
// body must not quietly become a second escape hatch for arbitrary vSphere
// operations, so each one lives in a file a reviewer reads before approving a
// new call to a vCenter.
func TestOnlyPerfAndBrowserShimsDefineFault(t *testing.T) {
	root := ".."
	var faultMethods []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if ok && fn.Recv != nil && fn.Name.Name == "Fault" {
				faultMethods = append(faultMethods, path)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	// The count is not pinned; the location is. Each hand-rolled operation
	// needs its own body type and so its own Fault method, and what this test
	// protects is that all of them stay in the reviewed files below — not that
	// there is exactly one of them.
	if len(faultMethods) == 0 {
		t.Fatal("no hand-rolled SOAP Fault implementation found; this test has stopped watching anything")
	}
	reviewedShims := []string{
		filepath.Join("internal", "vsphere", "datastore_browse.go"),
		filepath.Join("internal", "vsphere", "perf_query.go"),
		filepath.Join("internal", "vsphere", "license_query.go"),
		filepath.Join("internal", "vsphere", "event_query.go"),
	}
	for _, path := range faultMethods {
		reviewed := false
		for _, shim := range reviewedShims {
			reviewed = reviewed || strings.HasSuffix(path, shim)
		}
		if !reviewed {
			t.Fatalf("hand-rolled SOAP Fault implementations=%v, want all of them in %v", faultMethods, reviewedShims)
		}
	}

	// The license shim may name exactly one request type: the read-only
	// QueryAssignedLicenses. govmomi's license package also offers AddLicense,
	// RemoveLicense and UpdateAssignedLicense, which must never be reachable.
	licenseSrc, err := os.ReadFile(filepath.Join(root, "internal", "vsphere", "license_query.go"))
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]bool{}
	for _, m := range regexp.MustCompile(`Req\s+\*types\.([A-Za-z]+)`).FindAllStringSubmatch(string(licenseSrc), -1) {
		requests[m[1]] = true
	}
	if len(requests) != 1 || !requests["QueryAssignedLicenses"] {
		t.Fatalf("license SOAP shim names requests %v, want only QueryAssignedLicenses", requests)
	}

	// The event shim may name exactly one request type: the read-only
	// QueryEvents. CreateCollectorForEvents would leave a collector on the
	// server, and PostEvent writes to the event log; neither may be reachable.
	eventSrc, err := os.ReadFile(filepath.Join(root, "internal", "vsphere", "event_query.go"))
	if err != nil {
		t.Fatal(err)
	}
	eventRequests := map[string]bool{}
	for _, m := range regexp.MustCompile(`Req\s+\*types\.([A-Za-z]+)`).FindAllStringSubmatch(string(eventSrc), -1) {
		eventRequests[m[1]] = true
	}
	if len(eventRequests) != 1 || !eventRequests["QueryEvents"] {
		t.Fatalf("event SOAP shim names requests %v, want only QueryEvents", eventRequests)
	}

	path := filepath.Join(root, "internal", "vsphere", "datastore_browse.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	operationNames := regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*_Task\b`).FindAllString(string(src), -1)
	seen := map[string]bool{}
	for _, name := range operationNames {
		seen[name] = true
	}
	// The allowed set is exhaustive on purpose: adding a third operation here
	// has to be an edit to this list, which is the point at which somebody
	// asks whether a new call to a vCenter is really read-only.
	//
	// SearchDatastore_Task lists exactly one directory and descends nowhere;
	// SearchDatastoreSubFolders_Task is the recursive form. Both create a
	// task only as a handle for returning file metadata.
	allowed := map[string]bool{
		"SearchDatastoreSubFolders_Task": true,
		"SearchDatastore_Task":           true,
	}
	for name := range seen {
		if !allowed[name] {
			t.Fatalf("datastore browser SOAP shim names operation %q, which is not an approved read", name)
		}
	}
	for name := range allowed {
		if !seen[name] {
			t.Fatalf("datastore browser SOAP shim no longer names %q; prune this list rather than leaving it stale", name)
		}
	}
}

// mutatingPackages are the govmomi packages through which a vSphere object
// can be changed. object is the big one: VirtualMachine.PowerOff,
// Destroy_Task, Reconfigure and every other mutation hangs off the wrappers
// it defines. The rest reach the same surface by other routes.
//
// Checking imports rather than the built binary is deliberate. Every
// operation name in the vSphere schema is present as a string in any binary
// that so much as names a govmomi type, because vim25/types registers the
// whole schema in an init() the linker cannot eliminate — so searching the
// binary for "Destroy_Task" says nothing about whether it can be called.
// What the code imports does say something: a mutation has to come from one
// of these packages, and shipping one of them is a reviewable event.
var mutatingPackages = []string{
	"github.com/vmware/govmomi/object",
	"github.com/vmware/govmomi/task",
	"github.com/vmware/govmomi/vapi/rest",
	"github.com/vmware/govmomi/vim25/methods",
	"github.com/vmware/govmomi/guest",
	"github.com/vmware/govmomi/nfc",
	"github.com/vmware/govmomi/ovf",
}

// TestNoMutationCapablePackageIsImported is the standing guard on the
// property the wire-level test measures: the shipped code has no access to
// the API surface a mutation would have to come from. Test files are exempt
// — they run against vcsim, never a real vCenter, and never ship.
func TestNoMutationCapablePackageIsImported(t *testing.T) {
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			got := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range mutatingPackages {
				if got == bad || strings.HasPrefix(got, bad+"/") {
					t.Errorf("%s imports %s, which can modify a vCenter; "+
						"vsfleet is read-only and must not reach that surface", path, got)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
