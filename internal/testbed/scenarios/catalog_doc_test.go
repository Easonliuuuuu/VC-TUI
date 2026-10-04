package scenarios

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var fuzzFunc = regexp.MustCompile(`(?m)^func (Fuzz\w+)\(`)

// TestCatalogueDocumentsEveryScenarioAndFuzzTarget keeps docs/test-catalog.md
// and the weekly fuzz workflow in step with the code: a scenario or fuzz
// target added without a catalogue row, or a fuzz target the workflow never
// runs, fails here.
func TestCatalogueDocumentsEveryScenarioAndFuzzTarget(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	catalogue := readFile(t, filepath.Join(root, "docs", "test-catalog.md"))
	workflow := readFile(t, filepath.Join(root, ".github", "workflows", "fuzz.yml"))

	for _, definition := range Definitions() {
		if !strings.Contains(catalogue, "| `"+definition.Name+"` |") {
			t.Errorf("scenario %q has no row in docs/test-catalog.md", definition.Name)
		}
	}

	var targets []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".agents", "bin", "dist", "node_modules", "site":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for _, match := range fuzzFunc.FindAllStringSubmatch(readFile(t, path), -1) {
			targets = append(targets, match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) == 0 {
		t.Fatal("found no fuzz targets; the repository walk is broken")
	}
	for _, target := range targets {
		if !strings.Contains(catalogue, "| `"+target+"` |") {
			t.Errorf("fuzz target %s has no row in docs/test-catalog.md", target)
		}
		if !strings.Contains(workflow, "target: "+target+" ") {
			t.Errorf("fuzz target %s is not run by .github/workflows/fuzz.yml", target)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
