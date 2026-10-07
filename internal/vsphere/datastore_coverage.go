package vsphere

import (
	"slices"
	"sort"
)

// DatastoreCoverage is one datastore as the hosts of one cluster see it. The
// TUI cluster pages and the datastore-host-coverage health rule both read it,
// so they never disagree about which datastores are a gap.
type DatastoreCoverage struct {
	Name    string
	Mounted []string
	Missing []string
}

// Gap reports a datastore most hosts share but some do not: the case that
// breaks HA restart and vMotion onto those hosts. A datastore on exactly one
// host of several is that host's local disk, not a gap.
func (d DatastoreCoverage) Gap() bool { return len(d.Missing) > 0 && len(d.Mounted) > 1 }

// Local reports a datastore only one host of several mounts.
func (d DatastoreCoverage) Local() bool { return len(d.Mounted) == 1 && len(d.Missing) > 0 }

// HostMountsRead reports whether any host carries its datastore mounts. Older
// captures and imported RVTools workbooks have none, which must not read as
// "mounted nowhere".
func HostMountsRead(hosts []Host) bool {
	for _, h := range hosts {
		if len(h.Datastores) > 0 {
			return true
		}
	}
	return false
}

// ClusterDatastoreCoverage lists every datastore any of hosts mounts, gaps
// first, then by name. Mounted and Missing follow the order of hosts.
func ClusterDatastoreCoverage(hosts []Host) []DatastoreCoverage {
	byName := map[string]*DatastoreCoverage{}
	for _, h := range hosts {
		for _, ds := range h.Datastores {
			if byName[ds] == nil {
				byName[ds] = &DatastoreCoverage{Name: ds}
			}
		}
	}
	for _, d := range byName {
		for _, h := range hosts {
			if slices.Contains(h.Datastores, d.Name) {
				d.Mounted = append(d.Mounted, h.Name)
			} else {
				d.Missing = append(d.Missing, h.Name)
			}
		}
	}
	out := make([]DatastoreCoverage, 0, len(byName))
	for _, d := range byName {
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Gap() != out[j].Gap() {
			return out[i].Gap()
		}
		return out[i].Name < out[j].Name
	})
	return out
}
