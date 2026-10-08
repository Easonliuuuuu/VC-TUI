package network

import (
	"sort"
	"strconv"
	"strings"
)

// VLANKind says what a port group's VLAN setting is.
type VLANKind int

const (
	// VLANNone is an untagged port group: VLAN ID 0 on a standard switch, no
	// VLAN on a distributed one.
	VLANNone VLANKind = iota
	// VLANID tags one VLAN.
	VLANID
	// VLANTrunk passes a set of VLANs through to the guest. A standard port
	// group's 4095 is a trunk of every VLAN.
	VLANTrunk
	// VLANPrivate is a private VLAN, identified by its secondary ID.
	VLANPrivate
)

// VLANRange is an inclusive range of VLAN IDs.
type VLANRange struct{ Start, End int }

// VLAN is a port group's VLAN setting in one form for both switch kinds. The
// inventory keeps a distributed port group's VLAN as a string ("", "120",
// "trunk 100-200,300", "pvlan 5") and a standard port group's as the host's
// integer, and the two must compare equal when they mean the same thing.
type VLAN struct {
	Kind VLANKind
	// ID is the VLAN for VLANID and the secondary ID for VLANPrivate.
	ID int
	// Ranges are a trunk's VLANs, sorted and merged.
	Ranges []VLANRange
}

// ParseVLAN reads the inventory's string form. It also takes the decimal
// form the network comparison gives standard port groups, so "0" is untagged
// and "4095" is a trunk of every VLAN, the way the vSphere Client shows them.
func ParseVLAN(s string) VLAN {
	s = strings.ToLower(strings.TrimSpace(s))
	switch {
	case s == "" || s == "none":
		return VLAN{Kind: VLANNone}
	case strings.HasPrefix(s, "pvlan"):
		id, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(s, "pvlan")))
		if err != nil {
			return VLAN{Kind: VLANNone}
		}
		return VLAN{Kind: VLANPrivate, ID: id}
	case strings.HasPrefix(s, "trunk"):
		return trunk(strings.TrimSpace(strings.TrimPrefix(s, "trunk")))
	}
	id, err := strconv.Atoi(s)
	if err != nil {
		return trunk(s)
	}
	return StandardVLAN(int32(id))
}

// StandardVLAN reads a standard port group's VLAN ID.
func StandardVLAN(id int32) VLAN {
	switch {
	case id <= 0:
		return VLAN{Kind: VLANNone}
	case id >= 4095:
		return VLAN{Kind: VLANTrunk, Ranges: []VLANRange{{0, 4094}}}
	}
	return VLAN{Kind: VLANID, ID: int(id)}
}

// trunk parses "100-200,300". A trunk with no ranges listed carries every
// VLAN, which is what an empty trunk specification means to vCenter.
func trunk(s string) VLAN {
	var ranges []VLANRange
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		start, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			continue
		}
		end := start
		if isRange {
			if end, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
				continue
			}
		}
		if end < start {
			start, end = end, start
		}
		ranges = append(ranges, VLANRange{start, end})
	}
	if len(ranges) == 0 {
		ranges = []VLANRange{{0, 4094}}
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].Start < ranges[j].Start })
	merged := ranges[:1]
	for _, r := range ranges[1:] {
		last := &merged[len(merged)-1]
		if r.Start <= last.End+1 {
			last.End = max(last.End, r.End)
			continue
		}
		merged = append(merged, r)
	}
	return VLAN{Kind: VLANTrunk, Ranges: merged}
}

// String is the canonical form: "none", "120", "trunk 0-4094" or "pvlan 5".
// Two settings that mean the same thing have the same string.
func (v VLAN) String() string {
	switch v.Kind {
	case VLANID:
		return strconv.Itoa(v.ID)
	case VLANPrivate:
		return "pvlan " + strconv.Itoa(v.ID)
	case VLANTrunk:
		parts := make([]string, 0, len(v.Ranges))
		for _, r := range v.Ranges {
			if r.Start == r.End {
				parts = append(parts, strconv.Itoa(r.Start))
			} else {
				parts = append(parts, strconv.Itoa(r.Start)+"-"+strconv.Itoa(r.End))
			}
		}
		return "trunk " + strings.Join(parts, ",")
	}
	return "none"
}

// Carries reports whether traffic tagged id passes through the port group.
func (v VLAN) Carries(id int) bool {
	switch v.Kind {
	case VLANID:
		return v.ID == id
	case VLANTrunk:
		for _, r := range v.Ranges {
			if id >= r.Start && id <= r.End {
				return true
			}
		}
	}
	return false
}
