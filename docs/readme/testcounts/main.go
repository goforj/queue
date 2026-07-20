//go:build ignore || testcounts
// +build ignore testcounts

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	testCountStart          = "<!-- test-count:embed:start -->"
	testCountEnd            = "<!-- test-count:embed:end -->"
	integrationCountTimeout = 30 * time.Minute
	integrationManifestName = "integration_count.json"
)

// Counts records the executed unit and integration test counts rendered in the README.
type Counts struct {
	Unit        int
	Integration int
}

type integrationCountManifest struct {
	Count        int    `json:"count"`
	SourceHash   string `json:"source_hash"`
	BackendScope string `json:"backend_scope"`
}

// main updates README test-count badges from executed evidence or a verified integration manifest.
func main() {
	if err := run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	fmt.Println(testCountSuccessMessage(os.Getenv("TESTCOUNT_USE_INTEGRATION_MANIFEST") == "1"))
}

// testCountSuccessMessage describes the evidence source used for a successful badge update.
func testCountSuccessMessage(useIntegrationManifest bool) string {
	if useIntegrationManifest {
		return "✔ Test badges updated from unit execution and verified integration evidence"
	}
	return "✔ Test badges updated from executed unit and integration runs"
}

// run calculates test evidence and updates the checked-in README and integration manifest.
func run() error {
	root, err := findRoot()
	if err != nil {
		return err
	}
	return runAtRoot(root)
}

// runAtRoot calculates test evidence for one repository root and updates its generated artifacts.
func runAtRoot(root string) error {
	useIntegrationManifest := os.Getenv("TESTCOUNT_USE_INTEGRATION_MANIFEST") == "1"
	if !useIntegrationManifest {
		if err := validateFullIntegrationScope(os.Getenv("INTEGRATION_BACKEND")); err != nil {
			return err
		}
	}

	unitCount, err := countRunEvents(root, nil)
	if err != nil {
		return fmt.Errorf("count unit test runs: %w", err)
	}

	integrationRoot := filepath.Join(root, "integration")
	integrationSourceHash, err := integrationTestSourceHash(root, integrationRoot)
	if err != nil {
		return fmt.Errorf("hash integration test sources: %w", err)
	}
	manifestPath := filepath.Join(root, "docs", "readme", "testcounts", integrationManifestName)

	var integrationCount int
	if useIntegrationManifest {
		manifest, manifestErr := loadIntegrationCountManifest(manifestPath, integrationSourceHash)
		if manifestErr != nil {
			return manifestErr
		}
		integrationCount = manifest.Count
	} else {
		rootIntegrationNames, namesErr := integrationTopLevelTests(root)
		if namesErr != nil {
			return fmt.Errorf("root integration top-level tests: %w", namesErr)
		}
		rootIntegrationCount, countErr := countRunEvents(root, rootIntegrationNames)
		if countErr != nil {
			return fmt.Errorf("count root integration test runs: %w", countErr)
		}
		integrationNames, namesErr := integrationTopLevelTests(integrationRoot)
		if namesErr != nil {
			return fmt.Errorf("integration top-level tests: %w", namesErr)
		}
		integrationCount, err = countIntegrationRunEvents(integrationRoot, integrationNames)
		if err != nil {
			return fmt.Errorf("count integration test runs: %w", err)
		}
		integrationCount += rootIntegrationCount
		if err := writeIntegrationCountManifest(manifestPath, integrationCountManifest{
			Count:        integrationCount,
			SourceHash:   integrationSourceHash,
			BackendScope: "all",
		}); err != nil {
			return err
		}
	}

	readmePath := filepath.Join(root, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		return fmt.Errorf("read README: %w", err)
	}

	out, err := updateTestsSection(string(data), Counts{
		Unit:        unitCount,
		Integration: integrationCount,
	})
	if err != nil {
		return fmt.Errorf("update README test counts: %w", err)
	}

	return writeTestCountREADME(readmePath, out)
}

// writeTestCountREADME persists the rendered badge block with a contextual failure.
func writeTestCountREADME(path, contents string) error {
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return fmt.Errorf("write README test counts: %w", err)
	}
	return nil
}

// loadIntegrationCountManifest returns full-run evidence only when it covers the current integration sources.
func loadIntegrationCountManifest(path, expectedSourceHash string) (integrationCountManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return integrationCountManifest{}, fmt.Errorf("read integration count manifest: %w", err)
	}
	var manifest integrationCountManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return integrationCountManifest{}, fmt.Errorf("decode integration count manifest: %w", err)
	}
	if manifest.Count <= 0 {
		return integrationCountManifest{}, fmt.Errorf("integration count manifest has nonpositive count %d", manifest.Count)
	}
	if manifest.SourceHash == "" {
		return integrationCountManifest{}, fmt.Errorf("integration count manifest has empty source hash")
	}
	if manifest.BackendScope != "all" {
		return integrationCountManifest{}, fmt.Errorf("integration count manifest has backend scope %q, want %q", manifest.BackendScope, "all")
	}
	if manifest.SourceHash != expectedSourceHash {
		return integrationCountManifest{}, fmt.Errorf("integration test sources changed: manifest has %s, current sources have %s; run `cd docs && go run ./readme/testcounts/main.go` with integration services available", manifest.SourceHash, expectedSourceHash)
	}
	return manifest, nil
}

// validateFullIntegrationScope prevents a partial backend selection from replacing full-suite evidence.
func validateFullIntegrationScope(value string) error {
	scope := strings.ToLower(strings.TrimSpace(value))
	if scope == "" || scope == "all" {
		return nil
	}
	return fmt.Errorf("full integration count requires INTEGRATION_BACKEND=all or unset, got %q", value)
}

// writeIntegrationCountManifest records the source identity behind a full integration count.
func writeIntegrationCountManifest(path string, manifest integrationCountManifest) error {
	// The fixed scalar fields cannot contain values unsupported by encoding/json.
	data, _ := json.MarshalIndent(manifest, "", "  ")
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write integration count manifest: %w", err)
	}
	return nil
}

// integrationTestSourceHash fingerprints the integration module plus root integration-tagged sources and module inputs.
func integrationTestSourceHash(root, integrationRoot string) (string, error) {
	type sourceFile struct {
		path string
		src  []byte
	}
	var sources []sourceFile
	integrationRoot = filepath.Clean(integrationRoot)
	integrationInfo, err := os.Stat(integrationRoot)
	if err != nil {
		return "", fmt.Errorf("inspect integration module: %w", err)
	}
	if !integrationInfo.IsDir() {
		return "", fmt.Errorf("integration module path is not a directory: %s", integrationRoot)
	}
	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			if path != root && path != integrationRoot && fileExists(filepath.Join(path, "go.mod")) {
				return filepath.SkipDir
			}
			return nil
		}
		// Walk only yields descendants of root, so the relative path cannot cross filesystem volumes.
		relativePath, _ := filepath.Rel(root, path)
		relativePath = filepath.ToSlash(relativePath)
		inIntegrationModule := path == integrationRoot || strings.HasPrefix(path, integrationRoot+string(filepath.Separator))
		isModuleInput := relativePath == "go.mod" || relativePath == "go.sum" || relativePath == "integration/go.mod" || relativePath == "integration/go.sum"
		if !strings.HasSuffix(path, ".go") && !isModuleInput {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !inIntegrationModule && !isModuleInput && !hasIntegrationBuildTag(src) {
			return nil
		}
		sources = append(sources, sourceFile{path: relativePath, src: src})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].path < sources[j].path })
	hash := sha256.New()
	for _, source := range sources {
		_, _ = fmt.Fprintf(hash, "%s%c", source.path, byte(0))
		_, _ = hash.Write(source.src)
		_, _ = hash.Write([]byte{0})
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil)), nil
}

// countRunEvents executes the selected Go tests and counts their run events.
func countRunEvents(root string, integrationPrefixes map[string]struct{}) (int, error) {
	args := []string{"test", "./...", "-run", "Test", "-count=1", "-json"}
	if integrationPrefixes != nil {
		runPattern := buildTopLevelRunPattern(integrationPrefixes)
		if runPattern == "" {
			return 0, nil
		}
		args = []string{"test", "-tags=integration", "./...", "-run", runPattern, "-count=1", "-json"}
	}

	cmd := exec.Command("go", args...)
	cmd.Dir = root

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out.String())
	}

	var total int
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))

	for dec.More() {
		var event struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if err := dec.Decode(&event); err != nil {
			return 0, err
		}
		if event.Action != "run" || event.Test == "" {
			continue
		}

		if integrationPrefixes == nil {
			total++
			continue
		}

		top := event.Test
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		if _, ok := integrationPrefixes[top]; ok {
			total++
		}
	}

	return total, nil
}

// countIntegrationRunEvents executes integration-tagged tests within a bounded count-generation window.
func countIntegrationRunEvents(integrationRoot string, integrationPrefixes map[string]struct{}) (int, error) {
	if integrationPrefixes == nil || len(integrationPrefixes) == 0 {
		return 0, nil
	}
	runPattern := buildTopLevelRunPattern(integrationPrefixes)
	if runPattern == "" {
		return 0, nil
	}

	args := []string{"test", "-tags=integration", "./...", "-run", runPattern, "-count=1", "-json"}
	ctx, cancel := context.WithTimeout(context.Background(), integrationCountTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = integrationRoot
	cmd.Env = append(cmd.Environ(),
		"INTEGRATION_BACKEND=all",
		"RUN_CHAOS=0",
		"RUN_SOAK=0",
	)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return 0, fmt.Errorf("go %s (in %s): %w", strings.Join(args, " "), integrationRoot, ctx.Err())
		}
		return 0, fmt.Errorf("go %s (in %s): %w\n%s", strings.Join(args, " "), integrationRoot, err, out.String())
	}

	var total int
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	for dec.More() {
		var event struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if err := dec.Decode(&event); err != nil {
			return 0, err
		}
		if event.Action != "run" || event.Test == "" {
			continue
		}
		top := event.Test
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		if _, ok := integrationPrefixes[top]; ok {
			total++
		}
	}
	return total, nil
}

// buildTopLevelRunPattern builds an exact pattern for selected top-level tests and their subtests.
func buildTopLevelRunPattern(names map[string]struct{}) string {
	if len(names) == 0 {
		return ""
	}
	keys := _sortedKeys(names)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, regexp.QuoteMeta(k))
	}
	return "^(" + strings.Join(parts, "|") + ")(/.*)?$"
}

// integrationTopLevelTests discovers integration-tagged top-level test functions.
func integrationTopLevelTests(root string) (map[string]struct{}, error) {
	names := map[string]struct{}{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "vendor" {
				return filepath.SkipDir
			}
			if path != root && fileExists(filepath.Join(path, "go.mod")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !hasIntegrationBuildTag(src) {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") {
				names[fn.Name.Name] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return names, nil
}

// updateTestsSection replaces only the generated README badge block.
func updateTestsSection(readme string, counts Counts) (string, error) {
	start := strings.Index(readme, testCountStart)
	end := strings.Index(readme, testCountEnd)
	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("test count anchors not found or malformed")
	}

	before := readme[:start+len(testCountStart)]
	body := readme[start+len(testCountStart) : end]
	after := readme[end:]

	leading := ""
	if strings.HasPrefix(body, "\n") {
		leading = "\n"
	}

	lines := []string{
		fmt.Sprintf("    <img src=\"https://img.shields.io/badge/unit_tests-%d-brightgreen\" alt=\"Unit tests (executed count)\">", counts.Unit),
		fmt.Sprintf("    <img src=\"https://img.shields.io/badge/integration_tests-%d-blue\" alt=\"Integration tests (executed count)\">", counts.Integration),
	}
	return before + leading + strings.Join(lines, "\n") + "\n" + after, nil
}

// hasIntegrationBuildTag reports whether a source file opts into the integration suite.
func hasIntegrationBuildTag(src []byte) bool {
	lines := strings.Split(string(src), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "package ") {
			break
		}
		if strings.Contains(trimmed, "go:build") && strings.Contains(trimmed, "integration") {
			return true
		}
		if strings.HasPrefix(trimmed, "// +build") && strings.Contains(trimmed, "integration") {
			return true
		}
	}
	return false
}

// findRoot locates the queue module from any supported generator working directory.
func findRoot() (string, error) {
	wd, _ := os.Getwd()
	candidates := []string{wd, filepath.Join(wd, ".."), filepath.Join(wd, "..", ".."), filepath.Join(wd, "..", "..", "..")}
	for _, c := range candidates {
		c = filepath.Clean(c)
		if fileExists(filepath.Join(c, "go.mod")) && fileExists(filepath.Join(c, "README.md")) {
			return filepath.Clean(c), nil
		}
	}
	return "", fmt.Errorf("could not find project root from %s", wd)
}

// fileExists reports whether a generator input path exists.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// _sortedKeys returns map keys in deterministic lexical order.
func _sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
