//go:build testcounts
// +build testcounts

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTestCountSuccessMessage verifies generator output identifies whether integration evidence was executed or verified.
func TestTestCountSuccessMessage(t *testing.T) {
	if got := testCountSuccessMessage(false); !strings.Contains(got, "executed unit and integration") {
		t.Fatalf("full success message = %q", got)
	}
	if got := testCountSuccessMessage(true); !strings.Contains(got, "verified integration evidence") {
		t.Fatalf("manifest success message = %q", got)
	}
}

// TestRunAtRootGeneratesAndConsumesFullEvidence verifies both full and manifest-backed badge paths on a minimal multi-module repository.
func TestRunAtRootGeneratesAndConsumesFullEvidence(t *testing.T) {
	root := newTestCountRepository(t)
	t.Setenv("GOWORK", "off")
	t.Setenv("TESTCOUNT_USE_INTEGRATION_MANIFEST", "")
	t.Setenv("INTEGRATION_BACKEND", "all")
	t.Setenv("RUN_CHAOS", "1")
	t.Setenv("RUN_SOAK", "1")

	if err := runAtRoot(root); err != nil {
		t.Fatalf("generate full evidence: %v", err)
	}
	manifestPath := filepath.Join(root, "docs", "readme", "testcounts", integrationManifestName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read generated manifest: %v", err)
	}
	if !strings.Contains(string(data), `"count": 3`) || !strings.Contains(string(data), `"backend_scope": "all"`) {
		t.Fatalf("generated manifest = %s, want three all-backend run events", data)
	}
	assertTestCountBadges(t, root, 1, 3)

	t.Setenv("TESTCOUNT_USE_INTEGRATION_MANIFEST", "1")
	t.Setenv("INTEGRATION_BACKEND", "null")
	t.Chdir(root)
	if err := run(); err != nil {
		t.Fatalf("consume verified manifest: %v", err)
	}
	assertTestCountBadges(t, root, 1, 3)
}

// TestRunAtRootRejectsPartialFullEvidence verifies scope validation happens before any test execution or artifact write.
func TestRunAtRootRejectsPartialFullEvidence(t *testing.T) {
	root := newTestCountRepository(t)
	t.Setenv("TESTCOUNT_USE_INTEGRATION_MANIFEST", "")
	t.Setenv("INTEGRATION_BACKEND", "redis")
	err := runAtRoot(root)
	if err == nil || !strings.Contains(err.Error(), "requires INTEGRATION_BACKEND=all") {
		t.Fatalf("partial full evidence error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "readme", "testcounts", integrationManifestName)); !os.IsNotExist(err) {
		t.Fatalf("partial generation wrote a manifest: %v", err)
	}
}

// TestRunAtRootRejectsInvalidManifest verifies the manifest-backed path propagates evidence validation failures.
func TestRunAtRootRejectsInvalidManifest(t *testing.T) {
	root := newTestCountRepository(t)
	t.Setenv("GOWORK", "off")
	t.Setenv("TESTCOUNT_USE_INTEGRATION_MANIFEST", "1")
	t.Setenv("INTEGRATION_BACKEND", "null")
	manifestPath := filepath.Join(root, "docs", "readme", "testcounts", integrationManifestName)
	writeTestFile(t, manifestPath, `{"count":3,"source_hash":"sha256:stale","backend_scope":"all"}`)
	err := runAtRoot(root)
	if err == nil || !strings.Contains(err.Error(), "integration test sources changed") {
		t.Fatalf("invalid manifest error = %v", err)
	}
}

// TestRunAtRootReportsEvidenceFailures verifies each generation boundary preserves a specific diagnostic.
func TestRunAtRootReportsEvidenceFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "unit execution",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "unit_test.go"), "package testcounts\n\nimport \"testing\"\n\nfunc TestUnit(t *testing.T) { missing() }\n")
			},
			want: "count unit test runs",
		},
		{
			name: "missing integration module",
			mutate: func(t *testing.T, root string) {
				if err := os.Rename(filepath.Join(root, "integration"), filepath.Join(root, "integration-missing")); err != nil {
					t.Fatalf("hide integration module: %v", err)
				}
			},
			want: "hash integration test sources",
		},
		{
			name: "root discovery",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "tagged_integration_test.go"), "//go:build integration\n\npackage testcounts\n\nfunc TestBroken(")
			},
			want: "root integration top-level tests",
		},
		{
			name: "root execution",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "tagged_integration_test.go"), "//go:build integration\n\npackage testcounts\n\nimport \"testing\"\n\nfunc TestRootIntegration(t *testing.T) { missing() }\n")
			},
			want: "count root integration test runs",
		},
		{
			name: "integration discovery",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "integration", "suite_integration_test.go"), "//go:build integration\n\npackage integration\n\nfunc TestBroken(")
			},
			want: "integration top-level tests",
		},
		{
			name: "integration execution",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "integration", "suite_integration_test.go"), "//go:build integration\n\npackage integration\n\nimport \"testing\"\n\nfunc TestIntegration(t *testing.T) { missing() }\n")
			},
			want: "count integration test runs",
		},
		{
			name: "manifest write",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "docs", "readme", "testcounts", integrationManifestName)
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("create manifest directory collision: %v", err)
				}
			},
			want: "write integration count manifest",
		},
		{
			name: "README read",
			mutate: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
					t.Fatalf("remove README: %v", err)
				}
			},
			want: "read README",
		},
		{
			name: "README anchors",
			mutate: func(t *testing.T, root string) {
				writeTestFile(t, filepath.Join(root, "README.md"), "missing generated anchors\n")
			},
			want: "update README test counts",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := newTestCountRepository(t)
			t.Setenv("GOWORK", "off")
			t.Setenv("TESTCOUNT_USE_INTEGRATION_MANIFEST", "")
			t.Setenv("INTEGRATION_BACKEND", "all")
			t.Setenv("RUN_CHAOS", "")
			t.Setenv("RUN_SOAK", "")
			test.mutate(t, root)
			err := runAtRoot(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generation error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestIntegrationTestSourceHashTracksExecutionInputs verifies the manifest identity follows code and module inputs but ignores documentation.
func TestIntegrationTestSourceHashTracksExecutionInputs(t *testing.T) {
	root := t.TempDir()
	integrationRoot := filepath.Join(root, "integration")
	if err := os.MkdirAll(filepath.Join(root, "bus"), 0o755); err != nil {
		t.Fatalf("create root package: %v", err)
	}
	if err := os.MkdirAll(integrationRoot, 0o755); err != nil {
		t.Fatalf("create integration module: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create ignored metadata directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatalf("create ignored nested module: %v", err)
	}
	testPath := filepath.Join(root, "bus", "hooks_integration_test.go")
	helperPath := filepath.Join(integrationRoot, "helper.go")
	readmePath := filepath.Join(root, "README.md")
	writeTestFile(t, testPath, "//go:build integration\n\npackage fixture\n\nfunc TestCounted() {}\n")
	writeTestFile(t, helperPath, "package fixture\n\nvar backends = []string{\"one\"}\n")
	writeTestFile(t, filepath.Join(root, "go.mod"), "module fixture\n")
	writeTestFile(t, filepath.Join(integrationRoot, "go.mod"), "module fixture/integration\n")
	writeTestFile(t, filepath.Join(root, "nested", "go.mod"), "module fixture/nested\n")
	writeTestFile(t, filepath.Join(root, "nested", "ignored.go"), "package nested\n")
	writeTestFile(t, readmePath, "initial docs\n")

	initial, err := integrationTestSourceHash(root, integrationRoot)
	if err != nil {
		t.Fatalf("initial source hash: %v", err)
	}
	writeTestFile(t, readmePath, "changed docs\n")
	afterDocumentationChange, err := integrationTestSourceHash(root, integrationRoot)
	if err != nil {
		t.Fatalf("hash after documentation change: %v", err)
	}
	if afterDocumentationChange != initial {
		t.Fatalf("documentation changed hash from %q to %q", initial, afterDocumentationChange)
	}

	writeTestFile(t, helperPath, "package fixture\n\nvar backends = []string{\"one\", \"two\"}\n")
	afterHelperChange, err := integrationTestSourceHash(root, integrationRoot)
	if err != nil {
		t.Fatalf("hash after helper change: %v", err)
	}
	if afterHelperChange == initial {
		t.Fatalf("integration source hash remained %q after a helper change", initial)
	}

	writeTestFile(t, testPath, "//go:build integration\n\npackage fixture\n\nfunc TestCountedChanged() {}\n")
	afterRootTaggedChange, err := integrationTestSourceHash(root, integrationRoot)
	if err != nil {
		t.Fatalf("hash after root tagged change: %v", err)
	}
	if afterRootTaggedChange == afterHelperChange {
		t.Fatalf("integration source hash remained %q after a root tagged change", afterHelperChange)
	}
}

// TestIntegrationTestSourceHashRejectsInvalidInputs verifies missing modules and unreadable source entries fail closed.
func TestIntegrationTestSourceHashRejectsInvalidInputs(t *testing.T) {
	root := t.TempDir()
	integrationPath := filepath.Join(root, "integration")
	writeTestFile(t, integrationPath, "not a directory")
	if _, err := integrationTestSourceHash(root, integrationPath); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file integration path error = %v", err)
	}

	root = t.TempDir()
	integrationPath = filepath.Join(root, "integration")
	if err := os.MkdirAll(integrationPath, 0o755); err != nil {
		t.Fatalf("create integration directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target"), filepath.Join(integrationPath, "broken.go")); err != nil {
		t.Fatalf("create broken source link: %v", err)
	}
	if _, err := integrationTestSourceHash(root, integrationPath); err == nil {
		t.Fatal("broken integration source link was accepted")
	}
}

// TestIntegrationTopLevelTestsSkipsNestedModules verifies root evidence cannot double-count the dedicated integration module.
func TestIntegrationTopLevelTestsSkipsNestedModules(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "integration")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested module: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "root_test.go"), "//go:build integration\n\npackage fixture\n\nfunc TestRoot() {}\n")
	writeTestFile(t, filepath.Join(nested, "go.mod"), "module fixture/integration\n")
	writeTestFile(t, filepath.Join(nested, "nested_test.go"), "//go:build integration\n\npackage integration\n\nfunc TestNested() {}\n")

	names, err := integrationTopLevelTests(root)
	if err != nil {
		t.Fatalf("discover root tests: %v", err)
	}
	if _, ok := names["TestRoot"]; !ok {
		t.Fatal("root integration test was not discovered")
	}
	if _, ok := names["TestNested"]; ok {
		t.Fatal("nested integration module test was double-counted")
	}
}

// TestIntegrationCountManifestValidation verifies malformed, invalid, and stale evidence fails closed.
func TestIntegrationCountManifestValidation(t *testing.T) {
	_, err := loadIntegrationCountManifest(filepath.Join(t.TempDir(), "missing.json"), "sha256:current")
	if err == nil || !strings.Contains(err.Error(), "read integration count manifest") {
		t.Fatalf("missing manifest error = %v, want read failure", err)
	}

	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "malformed", contents: "{", want: "decode integration count manifest"},
		{name: "missing count", contents: `{"source_hash":"sha256:current","backend_scope":"all"}`, want: "nonpositive count"},
		{name: "null count", contents: `{"count":null,"source_hash":"sha256:current","backend_scope":"all"}`, want: "nonpositive count"},
		{name: "zero count", contents: `{"count":0,"source_hash":"sha256:current","backend_scope":"all"}`, want: "nonpositive count"},
		{name: "negative count", contents: `{"count":-1,"source_hash":"sha256:current","backend_scope":"all"}`, want: "nonpositive count"},
		{name: "empty hash", contents: `{"count":1,"source_hash":"","backend_scope":"all"}`, want: "empty source hash"},
		{name: "empty scope", contents: `{"count":1,"source_hash":"sha256:current"}`, want: "backend scope"},
		{name: "partial scope", contents: `{"count":1,"source_hash":"sha256:current","backend_scope":"redis"}`, want: "backend scope"},
		{name: "stale hash", contents: `{"count":1,"source_hash":"sha256:old","backend_scope":"all"}`, want: "integration test sources changed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), integrationManifestName)
			writeTestFile(t, path, test.contents)
			_, err := loadIntegrationCountManifest(path, "sha256:current")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load manifest error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestIntegrationCountManifestRoundTrip verifies full-run evidence remains readable by the unit-only guard.
func TestIntegrationCountManifestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), integrationManifestName)
	want := integrationCountManifest{Count: 612, SourceHash: "sha256:current", BackendScope: "all"}
	if err := writeIntegrationCountManifest(path, want); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	got, err := loadIntegrationCountManifest(path, want.SourceHash)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if got != want {
		t.Fatalf("manifest = %#v, want %#v", got, want)
	}
	if err := writeIntegrationCountManifest(t.TempDir(), want); err == nil || !strings.Contains(err.Error(), "write integration count manifest") {
		t.Fatalf("write manifest to directory error = %v, want write failure", err)
	}
}

// TestWriteTestCountREADME verifies successful persistence and contextual write failures.
func TestWriteTestCountREADME(t *testing.T) {
	path := filepath.Join(t.TempDir(), "README.md")
	if err := writeTestCountREADME(path, "rendered\n"); err != nil {
		t.Fatalf("write README: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "rendered\n" {
		t.Fatalf("written README = %q, %v", data, err)
	}
	if err := writeTestCountREADME(t.TempDir(), "rendered\n"); err == nil || !strings.Contains(err.Error(), "write README test counts") {
		t.Fatalf("write README directory error = %v", err)
	}
}

// TestValidateFullIntegrationScope verifies only the complete backend selection can refresh full-run evidence.
func TestValidateFullIntegrationScope(t *testing.T) {
	for _, value := range []string{"", "all", " ALL "} {
		if err := validateFullIntegrationScope(value); err != nil {
			t.Fatalf("scope %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"null", "redis", "redis,sqs"} {
		if err := validateFullIntegrationScope(value); err == nil {
			t.Fatalf("partial scope %q accepted", value)
		}
	}
}

// writeTestFile writes a test fixture or fails the current test.
func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newTestCountRepository creates a minimal root plus nested integration module with deterministic run counts.
func newTestCountRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	integrationRoot := filepath.Join(root, "integration")
	manifestDir := filepath.Join(root, "docs", "readme", "testcounts")
	if err := os.MkdirAll(integrationRoot, 0o755); err != nil {
		t.Fatalf("create integration module: %v", err)
	}
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("create manifest directory: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "go.mod"), "module example.com/testcounts\n\ngo 1.24.4\n")
	writeTestFile(t, filepath.Join(root, "unit_test.go"), "package testcounts\n\nimport \"testing\"\n\nfunc TestUnit(t *testing.T) {}\n")
	writeTestFile(t, filepath.Join(root, "tagged_integration_test.go"), "//go:build integration\n\npackage testcounts\n\nimport \"testing\"\n\nfunc TestRootIntegration(t *testing.T) {}\n")
	writeTestFile(t, filepath.Join(integrationRoot, "go.mod"), "module example.com/testcounts/integration\n\ngo 1.24.4\n")
	writeTestFile(t, filepath.Join(integrationRoot, "suite_integration_test.go"), "//go:build integration\n\npackage integration\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestIntegration(t *testing.T) {\n\tif os.Getenv(\"INTEGRATION_BACKEND\") != \"all\" || os.Getenv(\"RUN_CHAOS\") != \"0\" || os.Getenv(\"RUN_SOAK\") != \"0\" {\n\t\tt.Fatalf(\"count environment = %q/%q/%q\", os.Getenv(\"INTEGRATION_BACKEND\"), os.Getenv(\"RUN_CHAOS\"), os.Getenv(\"RUN_SOAK\"))\n\t}\n\tt.Run(\"child\", func(t *testing.T) {})\n}\n")
	writeTestFile(t, filepath.Join(root, "README.md"), testCountStart+"\nold\n"+testCountEnd+"\n")
	return root
}

// assertTestCountBadges verifies the generated README contains the expected executed counts.
func assertTestCountBadges(t *testing.T, root string, unit, integration int) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("read generated README: %v", err)
	}
	wantUnit := "unit_tests-" + fmt.Sprint(unit) + "-"
	wantIntegration := "integration_tests-" + fmt.Sprint(integration) + "-"
	if !strings.Contains(string(data), wantUnit) || !strings.Contains(string(data), wantIntegration) {
		t.Fatalf("generated README = %s, want %s and %s", data, wantUnit, wantIntegration)
	}
}
