package tui

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/easonliuuuuu/vsfleet/internal/config"
	"github.com/easonliuuuuu/vsfleet/internal/perf"
	"github.com/easonliuuuuu/vsfleet/internal/vsphere"
)

// groupPerfBackend adds the vApp chart extension to perfFakeBackend. Every
// counter reads a steady value; a member's CPU in MHz is mhz[VM ID].
type groupPerfBackend struct {
	*perfFakeBackend
	mhz        map[string]int64
	fail       map[string]error
	groupCalls []groupCall
}

type groupCall struct {
	vms      []string
	interval int
}

func (b *groupPerfBackend) VMsPerfSeries(_ context.Context, _ *config.Context, vms []vsphere.VM, window time.Duration, interval int, now time.Time) ([]vsphere.VMSeriesResult, error) {
	call := groupCall{interval: interval}
	out := make([]vsphere.VMSeriesResult, len(vms))
	n := int(window.Seconds()) / interval
	for i, vm := range vms {
		call.vms = append(call.vms, vm.ID)
		if err := b.fail[vm.ID]; err != nil {
			out[i] = vsphere.VMSeriesResult{VM: vm, Err: err}
			continue
		}
		set := perf.SeriesSet{IntervalSeconds: interval, WindowStart: now.Add(-window), WindowEnd: now}
		for _, c := range perf.DashboardCounters {
			value := int64(2000)
			if c.Metric == perf.CPUUsageMHz {
				value = b.mhz[vm.ID]
			}
			raw := make([]int64, n)
			for j := range raw {
				raw[j] = value
			}
			set.Series = append(set.Series, perf.NewSeries(c, raw, n, interval, vm.CPU))
		}
		out[i] = vsphere.VMSeriesResult{VM: vm, Set: set}
	}
	sort.Strings(call.vms)
	b.groupCalls = append(b.groupCalls, call)
	return out, nil
}

// openChartedVApp opens prod's vApp (app-01 and db-01, 1 GHz CPU limit) on a
// backend that charts its members.
func openChartedVApp(t *testing.T, mhz map[string]int64, fail map[string]error) (*Model, *groupPerfBackend) {
	t.Helper()
	b := &groupPerfBackend{perfFakeBackend: perfHealthy(), mhz: mhz, fail: fail}
	withAllocatedVApp(b.fakeBackend)
	m := newTestModel(t, b.fakeBackend, Options{Current: "prod"})
	m.backend = b
	m.height = 40
	press(t, m, "7", "enter")
	return m, b
}

func TestVAppWorkspaceChartsMembersAgainstTheLimit(t *testing.T) {
	m, b := openChartedVApp(t, map[string]int64{"prod-vm-1": 700, "prod-vm-db": 300}, nil)
	if len(b.groupCalls) != 1 || b.groupCalls[0].interval != vsphere.RealtimePerfInterval ||
		strings.Join(b.groupCalls[0].vms, ",") != "prod-vm-1,prod-vm-db" {
		t.Fatalf("calls = %+v; want one realtime read of both members", b.groupCalls)
	}
	if len(b.calls) != 0 {
		t.Fatalf("the workspace read members one at a time: %+v", b.calls)
	}
	out := ansi.Strip(m.View())
	for _, want := range []string{"Performance", "[1h]", "CPU usage", "limit 1.0GHz", "Memory active", "CPU PK", "READY", "LIMITED"} {
		if !strings.Contains(out, want) {
			t.Errorf("workspace is missing %q:\n%s", want, out)
		}
	}
	// 700 + 300 MHz is the whole 1 GHz limit: the chart says the vApp is at
	// its ceiling.
	if _, l := lineWith(out, "CPU usage"); !strings.Contains(l, "peak 1.0GHz") || !strings.Contains(l, glyphCheckWarn) {
		t.Errorf("summed CPU at the limit should peak at 1.0GHz with a warning, got %q", l)
	}
	if _, l := lineWith(out, "app-01  "); !strings.Contains(l, "700 MHz") {
		t.Errorf("app-01 row should carry its peak CPU, got %q", l)
	}
}

func TestVAppWorkspaceRangeKeysReReadTheMembers(t *testing.T) {
	m, b := openChartedVApp(t, map[string]int64{"prod-vm-1": 100, "prod-vm-db": 100}, nil)
	press(t, m, ">")
	if len(b.groupCalls) != 2 || b.groupCalls[1].interval != 300 {
		t.Fatalf("calls = %+v; want the 24h roll-up read after >", b.groupCalls)
	}
	press(t, m, "<")
	if len(b.groupCalls) != 2 {
		t.Fatalf("going back to a cached range re-read it: %+v", b.groupCalls)
	}
	press(t, m, "r")
	if len(b.groupCalls) != 3 {
		t.Fatalf("r should re-read the range on screen: %+v", b.groupCalls)
	}
}

func TestVAppWorkspaceLeavesOutUnreadableMembers(t *testing.T) {
	m, _ := openChartedVApp(t, map[string]int64{"prod-vm-1": 400}, map[string]error{"prod-vm-db": errors.New("gone")})
	out := ansi.Strip(m.View())
	if !strings.Contains(out, "1 of 2 members could not be read") {
		t.Errorf("an unreadable member should be named in the totals:\n%s", out)
	}
	if _, l := lineWith(out, "CPU usage"); !strings.Contains(l, "1 of 2 members") || !strings.Contains(l, "peak 400 MHz") {
		t.Errorf("totals should cover the readable member only, got %q", l)
	}
	if _, l := lineWith(out, "db-01  "); strings.Contains(l, "MHz") {
		t.Errorf("the unreadable member should have no peak, got %q", l)
	}
}

func TestVAppWorkspaceSortsBusiestFirst(t *testing.T) {
	m, _ := openChartedVApp(t, map[string]int64{"prod-vm-1": 900, "prod-vm-db": 50}, nil)
	order := func() (int, int) {
		out := m.View()
		db, _ := lineWith(out, "db-01  ")
		app, _ := lineWith(out, "app-01  ")
		return db, app
	}
	if db, app := order(); db > app {
		t.Fatalf("start order should list db-01 (start 1) first")
	}
	press(t, m, "s")
	if db, app := order(); app > db {
		t.Fatalf("busiest first should list app-01 (900 MHz) before db-01 (50 MHz)")
	}
	if _, l := lineWith(m.View(), "Members"); !strings.Contains(l, "busiest first") {
		t.Errorf("the members header should say how it is sorted, got %q", l)
	}
}

func TestVAppWorkspaceReturningFromAMemberKeepsTheChartsCached(t *testing.T) {
	m, b := openChartedVApp(t, map[string]int64{"prod-vm-1": 100, "prod-vm-db": 100}, nil)
	press(t, m, "down", "enter")
	if m.mode != modeVAppVMDetail {
		t.Fatalf("enter on a member VM should open it, mode=%v", m.mode)
	}
	press(t, m, "esc")
	if m.mode != modeVAppDetail || len(b.groupCalls) != 1 {
		t.Fatalf("back in the workspace (mode %v) the cached member read should be reused: %+v", m.mode, b.groupCalls)
	}
}

// A member that is off now may have run earlier in the chart's window, so
// the ceiling counts every member.
func TestVAppCeilingCountsEveryMember(t *testing.T) {
	inv := &vsphere.Inventory{Hosts: []vsphere.Host{{Name: "esx-1", CPUMHz: 2400}}}
	cpu, mem := vappCeiling([]vsphere.VM{
		{ID: "on", Host: "esx-1", PowerState: "poweredOn", CPU: 2, MemoryMB: 4096},
		{ID: "off", Host: "esx-1", PowerState: "poweredOff", CPU: 1, MemoryMB: 1024},
		{ID: "unknown-host", Host: "esx-9", PowerState: "poweredOn", CPU: 4, MemoryMB: 512},
	}, inv)
	if cpu != 3*2400 || mem != 4096+1024+512 {
		t.Fatalf("ceiling = %.0f MHz, %.0f MB", cpu, mem)
	}
}
