package network

import "testing"

func TestParseVLANGivesBothSwitchKindsOneForm(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"", "none"},
		{"0", "none"},
		{"none", "none"},
		{"120", "120"},
		{" 120 ", "120"},
		{"4095", "trunk 0-4094"},
		{"trunk", "trunk 0-4094"},
		{"trunk 300,100-200,201-210", "trunk 100-210,300"},
		{"trunk 0-4094", "trunk 0-4094"},
		{"pvlan 5", "pvlan 5"},
	} {
		if got := ParseVLAN(tc.in).String(); got != tc.want {
			t.Errorf("ParseVLAN(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if StandardVLAN(0).String() != ParseVLAN("").String() {
		t.Error("an untagged standard port group and an untagged distributed one differ")
	}
	if StandardVLAN(4095).String() != ParseVLAN("trunk 0-4094").String() {
		t.Error("standard VLAN 4095 is not the all-VLAN trunk")
	}
}

func TestVLANCarries(t *testing.T) {
	trunk := ParseVLAN("trunk 100-200,300")
	for id, want := range map[int]bool{99: false, 100: true, 150: true, 200: true, 201: false, 300: true} {
		if got := trunk.Carries(id); got != want {
			t.Errorf("trunk carries %d = %v, want %v", id, got, want)
		}
	}
	if !ParseVLAN("120").Carries(120) || ParseVLAN("120").Carries(121) {
		t.Error("a tagged port group carries only its own VLAN")
	}
	if ParseVLAN("").Carries(0) || ParseVLAN("pvlan 5").Carries(5) {
		t.Error("untagged and private VLAN port groups carry no tagged VLAN")
	}
}
