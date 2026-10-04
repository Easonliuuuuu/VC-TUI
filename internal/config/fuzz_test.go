package config_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/easonliuuuuu/vsfleet/internal/config"
)

// FuzzLoadSaveRoundTrip loads arbitrary configuration text. Anything Load
// accepts must survive vsfleet's own Save and load back to the same
// contexts: `context add` and `context remove` rewrite the file this way, so
// a lossy round trip would silently change an operator's configuration.
func FuzzLoadSaveRoundTrip(f *testing.F) {
	f.Add(sample)
	f.Add("version = 1\n")
	f.Add("version = 99\n")
	f.Add("[[contexts]]\nname = \"x\"\nendpoint = \"https://10.0.0.1\"\n[contexts.transport]\ntype = \"http\"\naddress = \"127.0.0.1:3128\"\n")
	f.Add("[[contexts]]\nname = \"x\"\nendpoint = \"vc\"\n[[contexts]]\nname = \"x\"\nendpoint = \"vc2\"\n")
	f.Add("[ssh]\n[[ssh.routes]]\ncontext = \"lab\"\nmatch = \"10.0.0.0/8\"\nproxy = \"127.0.0.1:1080\"\n")
	f.Fuzz(func(t *testing.T, text string) {
		dir := t.TempDir()
		first, err := config.Load(write(t, text))
		if err != nil {
			return
		}
		first.SetPath(filepath.Join(dir, "saved", "config.toml"))
		if err := first.Save(); err != nil {
			t.Fatalf("Load accepted the configuration but Save rejected it: %v\n%s", err, text)
		}
		second, err := config.Load(first.Path())
		if err != nil {
			t.Fatalf("a saved configuration does not load back: %v\n%s", err, text)
		}
		if !reflect.DeepEqual(first.Names(), second.Names()) {
			t.Fatalf("context names changed across save: %v -> %v", first.Names(), second.Names())
		}
		for _, name := range first.Names() {
			a, _ := first.Context(name)
			b, _ := second.Context(name)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("context %q changed across save:\n%+v\n%+v", name, a, b)
			}
		}
	})
}

// FuzzNormalizeThumbprintIsIdempotent: a stored thumbprint is compared with
// one read off the wire after both are normalized, so normalizing twice must
// not change the answer.
func FuzzNormalizeThumbprintIsIdempotent(f *testing.F) {
	f.Add("ab:cd:ef:01:23:45:67:89:ab:cd:ef:01:23:45:67:89:ab:cd:ef:01")
	f.Add("ABCDEF0123")
	f.Add(" sha256:AB-CD ")
	f.Fuzz(func(t *testing.T, s string) {
		once := config.NormalizeThumbprint(s)
		if twice := config.NormalizeThumbprint(once); twice != once {
			t.Fatalf("NormalizeThumbprint(%q) = %q, but normalizing that gives %q", s, once, twice)
		}
	})
}
