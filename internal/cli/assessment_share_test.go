package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestShareFlagsFailClosed(t *testing.T) {
	cases := map[string]shareFlags{
		"pseudonymize without profile": {pseudonymize: true},
		"preview without profile":      {preview: true},
		"pseudonymize without key":     {profile: "sizing-summary", pseudonymize: true},
		"key without pseudonymize":     {profile: "sizing-summary", keyFile: "k"},
		"link without pseudonymize":    {profile: "sizing-summary", linkExports: true},
	}
	for name, f := range cases {
		if err := f.validate("rvtools"); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := (&shareFlags{profile: "sizing-summary"}).validate("csv"); err == nil {
		t.Error("a profile must not combine with --format csv")
	}
	if err := (&shareFlags{}).validate("csv"); err != nil {
		t.Errorf("the ordinary export must be untouched: %v", err)
	}
	if err := (&shareFlags{profile: "sizing-summary", pseudonymize: true, preview: true}).validate("rvtools"); err != nil {
		t.Errorf("a preview needs no key: %v", err)
	}
}

func TestReadShareKey(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.key")
	if err := os.WriteFile(good, []byte("0123456789abcdef0123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := readShareKey(good, strings.NewReader(""))
	if err != nil || string(key) != "0123456789abcdef0123" {
		t.Fatalf("key %q err %v", key, err)
	}
	short := filepath.Join(dir, "short.key")
	_ = os.WriteFile(short, []byte("short"), 0o600)
	if _, err := readShareKey(short, nil); err == nil {
		t.Error("a short key must be refused")
	}
	if _, err := readShareKey(filepath.Join(dir, "missing"), nil); err == nil {
		t.Error("a missing key file must be refused")
	}
	if runtime.GOOS != "windows" {
		open := filepath.Join(dir, "open.key")
		_ = os.WriteFile(open, []byte("0123456789abcdef0123"), 0o644)
		if _, err := readShareKey(open, nil); err == nil {
			t.Error("a group/world-readable key file must be refused")
		}
	}
	if key, err := readShareKey("-", strings.NewReader("stdin-key-0123456789\n")); err != nil || string(key) != "stdin-key-0123456789" {
		t.Errorf("stdin key %q err %v", key, err)
	}
}
