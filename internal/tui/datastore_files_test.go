package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// browsingBackend is a fakeBackend that also answers directory queries, from
// a scripted map of directory to entries. It counts every query per path,
// which is what the laziness assertions are made against: navigation must
// cost one query per directory an operator opens and no more, and opening a
// datastore's detail pane must cost none at all.
type browsingBackend struct {
	*fakeBackend
	dirs      map[string][]vsphere.DatastoreEntry
	listCalls map[string]int
	findCalls int
	listErr   error
	findErr   error
	results   []vsphere.DatastoreEntry
	truncated bool
	refs      vsphere.DatastoreReferenceListing
	refCalls  int
	// block, when non-nil, holds every query until it is closed — the way a
	// slow datastore browser is simulated.
	block chan struct{}
}

func (b *browsingBackend) ListDatastoreVMReferences(context.Context, *config.Context, string) (vsphere.DatastoreReferenceListing, error) {
	b.refCalls++
	return b.refs, nil
}

func (b *browsingBackend) ListDatastoreDirectory(ctx context.Context, _ *config.Context, _, datastore, relative string) (vsphere.DatastoreListing, error) {
	if b.listCalls == nil {
		b.listCalls = map[string]int{}
	}
	b.listCalls[relative]++
	if b.block != nil {
		select {
		case <-b.block:
		case <-ctx.Done():
			return vsphere.DatastoreListing{}, ctx.Err()
		}
	}
	if b.listErr != nil {
		return vsphere.DatastoreListing{}, b.listErr
	}
	return vsphere.DatastoreListing{Entries: b.dirs[relative]}, nil
}

func (b *browsingBackend) FindInDatastore(ctx context.Context, _ *config.Context, _, datastore, pattern string) (vsphere.DatastoreListing, error) {
	b.findCalls++
	if b.block != nil {
		select {
		case <-b.block:
		case <-ctx.Done():
			return vsphere.DatastoreListing{}, ctx.Err()
		}
	}
	if b.findErr != nil {
		return vsphere.DatastoreListing{}, b.findErr
	}
	return vsphere.DatastoreListing{Entries: b.results, Truncated: b.truncated}, nil
}

func file(name, path string, size int64) vsphere.DatastoreEntry {
	return vsphere.DatastoreEntry{
		Name: name, Path: path, Type: vsphere.DatastoreEntryFile,
		SizeBytes: size, Modified: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
}

func dir(name, path string) vsphere.DatastoreEntry {
	return vsphere.DatastoreEntry{Name: name, Path: path, Type: vsphere.DatastoreEntryFolder}
}

func browsing() *browsingBackend {
	return &browsingBackend{
		fakeBackend: twoHealthy(),
		dirs: map[string][]vsphere.DatastoreEntry{
			"": {
				dir("ISO", "[nvme-01] ISO"),
				dir("database01", "[nvme-01] database01"),
				file("README.txt", "[nvme-01] README.txt", 2100),
			},
			"ISO": {
				dir("linux", "[nvme-01] ISO/linux"),
			},
			"ISO/linux": {
				file("ubuntu.vmdk", "[nvme-01] ISO/linux/ubuntu.vmdk", 3<<30),
			},
		},
	}
}

// openBrowser walks the way an operator does: to the datastore tab, into the
// detail pane, then onto "Browse files" in the action popup.
func openBrowser(t *testing.T, m *Model) {
	t.Helper()
	r := findRow(t, m, vsphere.KindDatastore, "nvme-01")
	items := m.actionsFor(r, 0)
	a, ok := findAction(items, "Browse files")
	if !ok {
		t.Fatalf("datastore actions have no Browse files: %+v", items)
	}
	if a.disabled != "" {
		t.Fatalf("Browse files is disabled: %s", a.disabled)
	}
	drive(t, m, a.run(m))
}

func TestBrowseFilesListsTheRootLazily(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b

	// Reaching the datastore's detail pane must not have touched the
	// filesystem. This is the regression the feature is built around:
	// ordinary datastore inventory makes zero directory queries.
	findRow(t, m, vsphere.KindDatastore, "nvme-01")
	if len(b.listCalls) != 0 {
		t.Fatalf("listing a datastore queried its filesystem: %v", b.listCalls)
	}

	openBrowser(t, m)
	if m.mode != modeDatastoreFiles {
		t.Fatalf("mode=%v, want modeDatastoreFiles", m.mode)
	}
	if b.listCalls[""] != 1 {
		t.Fatalf("root listed %d times, want exactly 1", b.listCalls[""])
	}
	if len(b.listCalls) != 1 {
		t.Fatalf("opening the root queried more than the root: %v", b.listCalls)
	}
	out := m.View()
	for _, want := range []string{"nvme-01", "ISO", "README.txt", "DIR", "FILE"} {
		if !strings.Contains(out, want) {
			t.Errorf("browser is missing %q:\n%s", want, out)
		}
	}
}

// Entering a directory queries that directory and nothing else, and walking
// back up leaves the workspace at the root.
func TestBrowseFilesNavigatesOneDirectoryAtATime(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)

	press(t, m, "enter") // ISO sorts first: folders lead
	if m.ds.path != "ISO" {
		t.Fatalf("path=%q, want ISO", m.ds.path)
	}
	if b.listCalls["ISO"] != 1 {
		t.Fatalf("ISO listed %d times, want 1", b.listCalls["ISO"])
	}
	press(t, m, "enter")
	if m.ds.path != "ISO/linux" {
		t.Fatalf("path=%q, want ISO/linux", m.ds.path)
	}
	if b.listCalls["ISO/linux"] != 1 {
		t.Fatalf("ISO/linux listed %d times, want 1", b.listCalls["ISO/linux"])
	}
	if !strings.Contains(m.View(), "/ISO/linux") {
		t.Fatalf("breadcrumb does not say where we are:\n%s", m.View())
	}

	press(t, m, "esc")
	if m.ds.path != "ISO" {
		t.Fatalf("esc left path=%q, want ISO", m.ds.path)
	}
	press(t, m, "esc")
	if m.ds.path != "" {
		t.Fatalf("esc left path=%q, want the root", m.ds.path)
	}
	press(t, m, "esc")
	if m.mode != modeDetail || m.ds != nil {
		t.Fatalf("esc at the root should close the browser; mode=%v ds=%v", m.mode, m.ds)
	}
}

// A directory that cannot be read says so. Rendering a denied or unreachable
// directory as an empty one is the failure this asserts against.
func TestBrowseFilesShowsErrorsRatherThanAnEmptyDirectory(t *testing.T) {
	b := browsing()
	b.listErr = errors.New("Permission to perform this operation was denied")
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)

	out := m.View()
	if !strings.Contains(out, "Permission to perform this operation was denied") {
		t.Fatalf("the browser hid a permission failure:\n%s", out)
	}
	if strings.Contains(out, "empty directory") {
		t.Fatalf("a failed listing was rendered as an empty directory:\n%s", out)
	}
}

// An inaccessible datastore is refused before any query is made, with the
// reason on the action rather than the action missing.
func TestBrowseFilesIsDisabledForAnInaccessibleDatastore(t *testing.T) {
	b := browsing()
	b.inventories["prod"].Datastores[0].Accessible = false
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b

	r := findRow(t, m, vsphere.KindDatastore, "nvme-01")
	a, ok := findAction(m.actionsFor(r, 0), "Browse files")
	if !ok {
		t.Fatal("Browse files vanished for an inaccessible datastore instead of explaining itself")
	}
	if a.disabled != "datastore is inaccessible" {
		t.Fatalf("disabled reason=%q", a.disabled)
	}
}

// A backend with no browser at all still offers the action, disabled — the
// same bargain openAction makes.
func TestBrowseFilesIsDisabledWithoutABrowserBackend(t *testing.T) {
	m := newTestModel(t, twoHealthy(), Options{Current: "prod"})
	r := findRow(t, m, vsphere.KindDatastore, "nvme-01")
	a, ok := findAction(m.actionsFor(r, 0), "Browse files")
	if !ok {
		t.Fatal("Browse files is missing entirely")
	}
	if a.disabled == "" {
		t.Fatal("Browse files claims to work against a backend that cannot browse")
	}
}

// While a directory is in flight the interface stays alive: busy() is true so
// the spinner keeps turning, and the pane says what it is waiting for.
func TestBrowseFilesStaysResponsiveWhileListing(t *testing.T) {
	b := browsing()
	b.block = make(chan struct{})
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b

	r := findRow(t, m, vsphere.KindDatastore, "nvme-01")
	a, _ := findAction(m.actionsFor(r, 0), "Browse files")
	// The command is deliberately not driven: a blocked query is exactly the
	// state under test.
	a.run(m)
	if !m.ds.loading {
		t.Fatal("the workspace is not marked loading")
	}
	if !m.busy() {
		t.Fatal("a directory in flight does not count as busy, so the spinner will freeze")
	}
	if !strings.Contains(m.View(), "listing") {
		t.Fatalf("the pane does not say it is listing:\n%s", m.View())
	}
	close(b.block)
}

// A reply for a directory the operator has already left is dropped. Without
// the generation check a slow root listing would repaint over the
// subdirectory that was opened while it was still coming.
func TestBrowseFilesDropsAStaleListing(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)
	press(t, m, "enter")

	stale := dsListingMsg{
		context: "prod", path: "", generation: m.ds.generation - 1,
		listing: vsphere.DatastoreListing{Entries: []vsphere.DatastoreEntry{file("stale.txt", "[nvme-01] stale.txt", 1)}},
	}
	drive(t, m, m.applyDSListing(stale))
	if m.ds.path != "ISO" {
		t.Fatalf("a stale reply moved the workspace to %q", m.ds.path)
	}
	if strings.Contains(m.View(), "stale.txt") {
		t.Fatalf("a stale reply was painted:\n%s", m.View())
	}
}

func TestBrowseFilesFiltersTheCurrentDirectory(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)

	press(t, m, "/")
	typeText(t, m, "read")
	out := m.View()
	if !strings.Contains(out, "README.txt") {
		t.Fatalf("the filter hid the matching entry:\n%s", out)
	}
	if strings.Contains(out, "ISO") {
		t.Fatalf("the filter kept a non-matching entry:\n%s", out)
	}
	// Filtering narrows what is already in memory; it must never go back to
	// the datastore.
	if len(b.listCalls) != 1 {
		t.Fatalf("filtering issued a query: %v", b.listCalls)
	}
	press(t, m, "esc")
	if m.filter.Value() != "" {
		t.Fatalf("esc left the filter set to %q", m.filter.Value())
	}
}

func TestBrowseFilesCopiesTheSelectedPath(t *testing.T) {
	b := browsing()
	h := &fakeHandoff{}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod", Handoff: h})
	m.backend = b
	openBrowser(t, m)

	press(t, m, "down", "down", "y") // README.txt
	if len(h.copied) != 1 || h.copied[0] != "[nvme-01] README.txt" {
		t.Fatalf("copied=%v, want the entry's datastore path", h.copied)
	}
}

func TestFindInDatastoreSearchesAndJumpsToTheMatch(t *testing.T) {
	b := browsing()
	b.results = []vsphere.DatastoreEntry{file("ubuntu.iso", "[nvme-01] ISO/linux/ubuntu.iso", 3<<30)}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)

	press(t, m, "f")
	if m.mode != modeDatastoreFind {
		t.Fatalf("mode=%v, want modeDatastoreFind", m.mode)
	}
	typeText(t, m, "ubuntu.iso")
	press(t, m, "enter")
	if b.findCalls != 1 {
		t.Fatalf("find ran %d times, want 1", b.findCalls)
	}
	out := m.View()
	if !strings.Contains(out, "ISO/linux/ubuntu.iso") {
		t.Fatalf("the result is not shown:\n%s", out)
	}

	// Enter on a result answers the question the search was asked: where is
	// it. That means opening the directory holding it.
	press(t, m, "enter")
	if m.mode != modeDatastoreFiles || m.ds.path != "ISO/linux" {
		t.Fatalf("jumping to a result left mode=%v path=%q", m.mode, m.ds.path)
	}
}

// Recursion never happens on its own: nothing about entering the browser or
// walking it calls the recursive search.
func TestFindIsNeverImplicit(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)
	press(t, m, "enter", "enter", "esc", "esc")
	if b.findCalls != 0 {
		t.Fatalf("browsing triggered %d recursive searches", b.findCalls)
	}
}

func TestDatastoreFileDetailShowsReferencesAndJumpsExactly(t *testing.T) {
	b := browsing()
	b.refs = vsphere.DatastoreReferenceListing{
		TotalVMs: 2, CheckedVMs: 2,
		References: []vsphere.DatastoreVMReference{{Context: "prod", VMID: "vm-1", VMName: "app-01", BackingPath: "[nvme-01] ISO/linux/ubuntu.vmdk"}},
	}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)
	press(t, m, "enter", "enter") // ISO/linux
	press(t, m, "enter")          // ubuntu.iso detail
	if m.mode != modeDatastoreEntry || b.refCalls != 1 {
		detailName := ""
		if m.ds != nil && m.ds.detail != nil {
			detailName = m.ds.detail.entry.Name
		}
		t.Fatalf("mode=%v refCalls=%d detail=%q path=%q, want file detail and one lazy lookup", m.mode, b.refCalls, detailName, m.ds.path)
	}
	if !strings.Contains(m.View(), "app-01 @ prod") {
		t.Fatalf("file detail omitted the owning VM:\n%s", m.View())
	}
	press(t, m, "enter")
	if m.mode != modeBrowse || m.kind != vsphere.KindVM || m.jump == nil || m.jump.value != "vm-1" {
		t.Fatalf("jump=%+v mode=%v kind=%v", m.jump, m.mode, m.kind)
	}
}

// A truncated search says so, next to the results and for as long as they are
// on screen — not as a message that scrolls by.
func TestFindShowsTruncationAlongsideResults(t *testing.T) {
	b := browsing()
	b.results = []vsphere.DatastoreEntry{file("ubuntu.iso", "[nvme-01] ISO/linux/ubuntu.iso", 1)}
	b.truncated = true
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	openBrowser(t, m)

	press(t, m, "f")
	typeText(t, m, "*.iso")
	press(t, m, "enter")
	out := m.View()
	if !strings.Contains(out, "ubuntu.iso") {
		t.Fatalf("results are missing:\n%s", out)
	}
	if !strings.Contains(out, "partial result") {
		t.Fatalf("a truncated search reads as a complete one:\n%s", out)
	}
}

// Esc during a search stops it rather than closing the pane, and the reply
// that eventually arrives is discarded.
func TestFindIsCancellable(t *testing.T) {
	b := browsing()
	b.block = make(chan struct{})
	b.results = []vsphere.DatastoreEntry{file("late.iso", "[nvme-01] late.iso", 1)}
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.ds = &dsWorkspace{context: "prod", datastore: "nvme-01", datastoreID: "prod-ds-1"}
	m.mode = modeDatastoreFind

	cmd := m.findInDatastore("*.iso")
	if !m.ds.finding || !m.busy() {
		t.Fatal("a search in flight does not register as work in progress")
	}
	generation := m.ds.generation

	press(t, m, "esc")
	if m.ds.finding {
		t.Fatal("esc did not stop the search")
	}
	if m.mode != modeDatastoreFind {
		t.Fatalf("esc closed the pane instead of cancelling; mode=%v", m.mode)
	}
	if !strings.Contains(m.View(), "cancelled") {
		t.Fatalf("the cancellation is invisible:\n%s", m.View())
	}

	// The abandoned query answers eventually. It must change nothing.
	close(b.block)
	drive(t, m, cmd)
	if strings.Contains(m.View(), "late.iso") {
		t.Fatalf("a cancelled search painted its result anyway:\n%s", m.View())
	}
	if m.ds.generation == generation {
		t.Fatal("cancelling did not advance the generation, so a late reply would be accepted")
	}
}

func TestDatastoreEntrySizeShowsEmptyFilesAsZeroBytes(t *testing.T) {
	if got := datastoreEntrySize(file("vm.vmsd", "[nvme-01] vm/vm.vmsd", 0)); got != "0B" {
		t.Errorf("empty file size = %q, want 0B", got)
	}
	if got := datastoreEntrySize(dir("vm", "[nvme-01] vm")); got != "-" {
		t.Errorf("folder size = %q, want -", got)
	}
}

func TestDatastoreEntryShowsDiskLabelAndBackingPath(t *testing.T) {
	m := newTestModel(t, browsing().fakeBackend, Options{Current: "prod"})
	m.width, m.height = 200, 40
	m.ds = &dsWorkspace{context: "prod", datastore: "nfs-shared", datastoreID: "ds-1"}
	m.ds.detail = &dsEntryDetail{
		entry:     file("shared-disk.vmdk", "[nfs-shared] shared/shared-disk.vmdk", 1),
		refLoaded: true,
		refs: vsphere.DatastoreReferenceListing{
			TotalVMs: 1, CheckedVMs: 1,
			References: []vsphere.DatastoreVMReference{{
				Context: "lab-vcenter", VMID: "vm-1", VMName: "vm-shared-a",
				DiskLabel: "Hard disk 2", BackingPath: "[nfs-shared] shared/shared-disk.vmdk",
			}},
		},
	}
	out := strings.Join(m.viewDatastoreEntry(), "\n")
	want := "vm-shared-a @ lab-vcenter · Hard disk 2 · [nfs-shared] shared/shared-disk.vmdk"
	if !strings.Contains(out, want) {
		t.Fatalf("missing %q in:\n%s", want, out)
	}
}

func TestBrowseFilesIgnoresAndRestoresTheInventoryFilter(t *testing.T) {
	b := browsing()
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	findRow(t, m, vsphere.KindDatastore, "nvme-01")
	m.filter.SetValue("nvme")
	openBrowser(t, m)

	if m.filter.Value() != "" {
		t.Fatalf("browser inherited the inventory filter %q", m.filter.Value())
	}
	out := m.View()
	if strings.Contains(out, "empty directory") || !strings.Contains(out, "README.txt") {
		t.Fatalf("root did not list its entries:\n%s", out)
	}

	press(t, m, "/")
	typeText(t, m, "iso")
	if out := m.View(); !strings.Contains(out, "1 here") {
		t.Fatalf("filter count should match the one visible row:\n%s", out)
	}
	typeText(t, m, "zzz")
	if out := m.View(); !strings.Contains(out, "no entries match") || strings.Contains(out, "empty directory") {
		t.Fatalf("a filtered-out listing must not read as empty:\n%s", out)
	}

	press(t, m, "esc")
	press(t, m, "esc")
	if m.mode != modeDetail || m.filter.Value() != "nvme" {
		t.Fatalf("mode=%v filter=%q, want detail with the inventory filter restored", m.mode, m.filter.Value())
	}
}
