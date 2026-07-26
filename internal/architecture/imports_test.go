package architecture_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestWorkflowDependencyDirection keeps the internal workflow engine independent from root public packages.
func TestWorkflowDependencyDirection(t *testing.T) {
	repository := repositoryRoot(t)
	assertProductionImportsExclude(t, filepath.Join(repository, "internal", "workflow"), map[string]struct{}{
		"github.com/goforj/queue":           {},
		"github.com/goforj/queue/queuecore": {},
	})
}

// repositoryRoot resolves the module root from this test file instead of relying on the caller's working directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

// assertProductionImportsExclude parses direct production files so tests and outward-facing subpackages remain free to exercise compatibility APIs.
func assertProductionImportsExclude(t *testing.T, directory string, forbidden map[string]struct{}) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read production package %s: %v", directory, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filename := filepath.Join(directory, entry.Name())
		file, err := parser.ParseFile(token.NewFileSet(), filename, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse production imports for %s: %v", filename, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("decode import in %s: %v", filename, err)
			}
			if _, blocked := forbidden[path]; blocked {
				t.Errorf("forbidden production dependency: %s imports %s", filename, path)
			}
		}
	}
}
