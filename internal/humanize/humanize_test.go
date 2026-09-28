package humanize

import (
	"testing"
	"time"
)

func TestMB(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "-"},
		{-1, "-"},
		{512, "512M"},
		{1024, "1G"},
		{16384, "16G"},
		{6144, "6G"},
		{1536, "1.5G"},
		{1 << 20, "1T"},
		{2 << 20, "2T"},
	} {
		if got := MB(tc.in); got != tc.want {
			t.Errorf("MB(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "-"},
		{512, "512B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{8 << 40, "8.0T"},
		{300 << 30, "300G"},
	} {
		if got := Bytes(tc.in); got != tc.want {
			t.Errorf("Bytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFileBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{-1, "-"},
		{512, "512B"},
		{1536, "1.5K"},
	} {
		if got := FileBytes(tc.in); got != tc.want {
			t.Errorf("FileBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMHz(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "-"},
		{2400, "2.4GHz"},
		{800, "800MHz"},
		{307200, "307GHz"},
	} {
		if got := MHz(tc.in); got != tc.want {
			t.Errorf("MHz(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDuration(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "-"},
		{500 * time.Microsecond, "500 us"},
		{67 * time.Millisecond, "67 ms"},
		{1500 * time.Millisecond, "1.50 s"},
	} {
		if got := Duration(tc.in); got != tc.want {
			t.Errorf("Duration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAge(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want string
	}{
		{0, "-"},
		{-time.Second, "-"},
		{time.Nanosecond, "1s"},
		{59*time.Second + 999*time.Millisecond, "59s"},
		{time.Minute, "1m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{24 * time.Hour, "1d"},
		{7*24*time.Hour - time.Second, "6d"},
		{7 * 24 * time.Hour, "1w"},
		{90 * 24 * time.Hour, "12w"},
	} {
		if got := Age(tc.in); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDash(t *testing.T) {
	if got := Dash("   "); got != "-" {
		t.Errorf("Dash(spaces) = %q, want %q", got, "-")
	}
	if got := Dash("esxi-01"); got != "esxi-01" {
		t.Errorf("Dash trimmed a real value: %q", got)
	}
}
