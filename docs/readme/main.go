//go:build ignore
// +build ignore

package main

import (
	"bytes"
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
)

const (
	apiStart       = "<!-- api:embed:start -->"
	apiEnd         = "<!-- api:embed:end -->"
	testCountStart = "<!-- test-count:embed:start -->"
	testCountEnd   = "<!-- test-count:embed:end -->"
)

func main() {
	if err := run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	fmt.Println("✔ API section updated in README.md")
}

func run() error {
	root, err := findRoot()
	if err != nil {
		return err
	}

	testsCount, err := countTests(root)
	if err != nil {
		return fmt.Errorf("count tests: %w", err)
	}

	funcs, err := parseFuncs(root)
	if err != nil {
		return err
	}

	api := renderAPI(funcs)

	readmePath := filepath.Join(root, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}

	out, err := replaceAPISection(string(data), api)
	if err != nil {
		return err
	}

	out, err = updateTestsSection(out, testsCount)
	if err != nil {
		return err
	}

	return os.WriteFile(readmePath, []byte(out), 0o644)
}

//
// ------------------------------------------------------------
// Data model
// ------------------------------------------------------------
//

type FuncDoc struct {
	Package     string
	Owner       string
	Name        string
	Group       string
	Behavior    string
	Fluent      string
	Description string
	Examples    []Example
}

type Example struct {
	Label string
	Code  string
	Line  int
}

//
// ------------------------------------------------------------
// Parsing
// ------------------------------------------------------------
//

var (
	groupHeader    = regexp.MustCompile(`(?i)^\s*@group\s+(.+)$`)
	behaviorHeader = regexp.MustCompile(`(?i)^\s*@behavior\s+(.+)$`)
	fluentHeader   = regexp.MustCompile(`(?i)^\s*@fluent\s+(.+)$`)
	exampleHeader  = regexp.MustCompile(`(?i)^\s*Example:\s*(.*)$`)
)

func parseFuncs(root string) ([]*FuncDoc, error) {
	dirs := []string{
		root,
	}

	var out []*FuncDoc
	for _, dir := range dirs {
		funcs, err := parseFuncsInDir(dir)
		if err != nil {
			return nil, err
		}
		out = append(out, funcs...)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Package == out[j].Package {
			if out[i].Owner == out[j].Owner {
				return out[i].Name < out[j].Name
			}
			return out[i].Owner < out[j].Owner
		}
		return out[i].Package < out[j].Package
	})

	return out, nil
}

func receiverOwner(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return receiverOwner(t.X)
	case *ast.IndexExpr:
		return receiverOwner(t.X)
	case *ast.IndexListExpr:
		return receiverOwner(t.X)
	default:
		return ""
	}
}

func anchorFor(fd *FuncDoc) string {
	parts := []string{fd.Package}
	if fd.Owner != "" {
		parts = append(parts, fd.Owner)
	}
	parts = append(parts, fd.Name)
	return strings.ToLower(strings.Join(parts, "-"))
}

func displayName(fd *FuncDoc) string {
	if fd.Owner != "" {
		return fd.Owner + "." + fd.Name
	}
	return fd.Name
}

func parseFuncsInDir(dir string) ([]*FuncDoc, error) {
	fset := token.NewFileSet()

	pkgs, err := parser.ParseDir(
		fset,
		dir,
		func(info os.FileInfo) bool {
			return !strings.HasSuffix(info.Name(), "_test.go")
		},
		parser.ParseComments,
	)
	if err != nil {
		return nil, err
	}

	pkgName, err := selectPackage(pkgs)
	if err != nil {
		return nil, err
	}

	pkg, ok := pkgs[pkgName]
	if !ok {
		return nil, fmt.Errorf(`package %q not found`, pkgName)
	}

	funcs := map[string]*FuncDoc{}

	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Doc == nil || !ast.IsExported(d.Name.Name) {
					continue
				}
				owner := ""
				if d.Recv != nil && len(d.Recv.List) > 0 {
					owner = receiverOwner(d.Recv.List[0].Type)
					if owner != "" && !ast.IsExported(owner) {
						continue
					}
				}
				fd := &FuncDoc{
					Package:     pkgName,
					Owner:       owner,
					Name:        d.Name.Name,
					Group:       extractGroup(d.Doc),
					Behavior:    extractBehavior(d.Doc),
					Fluent:      extractFluent(d.Doc),
					Description: extractDescription(d.Doc),
					Examples:    extractExamplesFromCommentGroup(fset, d.Doc),
				}
				mergeFuncDoc(funcs, fd)
			case *ast.GenDecl:
				if d.Tok != token.TYPE {
					continue
				}
				for _, spec := range d.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					iface, ok := typeSpec.Type.(*ast.InterfaceType)
					if !ok || iface.Methods == nil {
						continue
					}
					for _, field := range iface.Methods.List {
						if len(field.Names) == 0 {
							continue
						}
						doc := field.Doc
						if doc == nil {
							doc = field.Comment
						}
						if doc == nil {
							continue
						}
						for _, name := range field.Names {
							if !ast.IsExported(name.Name) {
								continue
							}
							fd := &FuncDoc{
								Package:     pkgName,
								Owner:       typeSpec.Name.Name,
								Name:        name.Name,
								Group:       extractGroup(doc),
								Behavior:    extractBehavior(doc),
								Fluent:      extractFluent(doc),
								Description: extractDescription(doc),
								Examples:    extractExamplesFromCommentGroup(fset, doc),
							}
							mergeFuncDoc(funcs, fd)
						}
					}
				}
			}
		}
	}

	out := make([]*FuncDoc, 0, len(funcs))
	for _, fd := range funcs {
		sort.Slice(fd.Examples, func(i, j int) bool {
			return fd.Examples[i].Line < fd.Examples[j].Line
		})
		out = append(out, fd)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Package == out[j].Package {
			return out[i].Name < out[j].Name
		}
		return out[i].Package < out[j].Package
	})

	return out, nil
}

func mergeFuncDoc(funcs map[string]*FuncDoc, fd *FuncDoc) {
	key := fd.Package + "." + fd.Owner + "." + fd.Name
	if existing, ok := funcs[key]; ok {
		existing.Examples = append(existing.Examples, fd.Examples...)
		if existing.Description == "" && fd.Description != "" {
			existing.Description = fd.Description
		}
		if existing.Group == "Other" && fd.Group != "Other" {
			existing.Group = fd.Group
		}
		if existing.Behavior == "" && fd.Behavior != "" {
			existing.Behavior = fd.Behavior
		}
		if existing.Fluent == "" && fd.Fluent != "" {
			existing.Fluent = fd.Fluent
		}
		return
	}
	funcs[key] = fd
}

func extractGroup(group *ast.CommentGroup) string {
	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if m := groupHeader.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return "Other"
}

func extractBehavior(group *ast.CommentGroup) string {
	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if m := behaviorHeader.FindStringSubmatch(line); m != nil {
			return strings.ToLower(strings.TrimSpace(m[1]))
		}
	}
	return ""
}

func extractFluent(group *ast.CommentGroup) string {
	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if m := fluentHeader.FindStringSubmatch(line); m != nil {
			return strings.ToLower(strings.TrimSpace(m[1]))
		}
	}
	return ""
}

func extractDescription(group *ast.CommentGroup) string {
	var lines []string

	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))

		if exampleHeader.MatchString(line) ||
			groupHeader.MatchString(line) ||
			behaviorHeader.MatchString(line) ||
			fluentHeader.MatchString(line) {
			break
		}

		if len(lines) == 0 && line == "" {
			continue
		}

		lines = append(lines, line)
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func extractExamplesFromCommentGroup(fset *token.FileSet, group *ast.CommentGroup) []Example {
	var out []Example
	var current []string
	var label string
	var start int
	inExample := false

	flush := func() {
		if len(current) == 0 {
			return
		}

		out = append(out, Example{
			Label: label,
			Code:  strings.Join(normalizeIndent(current), "\n"),
			Line:  start,
		})

		current = nil
		label = ""
		inExample = false
	}

	for _, c := range group.List {
		raw := strings.TrimPrefix(c.Text, "//")
		line := strings.TrimSpace(raw)

		if m := exampleHeader.FindStringSubmatch(line); m != nil {
			flush()
			inExample = true
			label = strings.TrimSpace(m[1])
			start = fset.Position(c.Slash).Line
			continue
		}

		if !inExample {
			continue
		}

		current = append(current, raw)
	}

	flush()
	return out
}

// selectPackage picks the primary package to document.
// Strategy:
//  1. If only one package exists, use it.
//  2. Prefer the non-"main" package with the most files.
//  3. Fall back to the first package alphabetically.
func selectPackage(pkgs map[string]*ast.Package) (string, error) {
	if len(pkgs) == 0 {
		return "", fmt.Errorf("no packages found")
	}

	if len(pkgs) == 1 {
		for name := range pkgs {
			return name, nil
		}
	}

	type candidate struct {
		name  string
		count int
	}

	candidates := make([]candidate, 0, len(pkgs))
	for name, pkg := range pkgs {
		candidates = append(candidates, candidate{
			name:  name,
			count: len(pkg.Files),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count == candidates[j].count {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].count > candidates[j].count
	})

	for _, cand := range candidates {
		if cand.name != "main" {
			return cand.name, nil
		}
	}

	return candidates[0].name, nil
}

//
// ------------------------------------------------------------
// Rendering
// ------------------------------------------------------------
//

func renderAPI(funcs []*FuncDoc) string {
	byPackageGroup := map[string]map[string][]*FuncDoc{}
	labelCounts := map[string]int{}
	for _, fd := range funcs {
		if byPackageGroup[fd.Package] == nil {
			byPackageGroup[fd.Package] = map[string][]*FuncDoc{}
		}
		byPackageGroup[fd.Package][fd.Group] = append(byPackageGroup[fd.Package][fd.Group], fd)
		labelCounts[displayName(fd)]++
	}

	packages := make([]string, 0, len(byPackageGroup))
	for p := range byPackageGroup {
		packages = append(packages, p)
	}
	sort.Strings(packages)
	singlePackage := len(packages) == 1
	qualifiedLabel := func(fd *FuncDoc) string {
		label := displayName(fd)
		if singlePackage || labelCounts[label] <= 1 {
			return label
		}
		return fd.Package + "." + label
	}

	var buf bytes.Buffer

	// ---------------- Index ----------------
	buf.WriteString("## API Index\n\n")
	for _, pkg := range packages {
		if !singlePackage {
			buf.WriteString("### " + pkg + "\n\n")
		}
		buf.WriteString("| Group | Functions |\n")
		buf.WriteString("|------:|:-----------|\n")

		groupNames := make([]string, 0, len(byPackageGroup[pkg]))
		for g := range byPackageGroup[pkg] {
			groupNames = append(groupNames, g)
		}
		sort.Strings(groupNames)

		for _, group := range groupNames {
			sort.Slice(byPackageGroup[pkg][group], func(i, j int) bool {
				left := byPackageGroup[pkg][group][i]
				right := byPackageGroup[pkg][group][j]
				if left.Name == right.Name {
					return left.Owner < right.Owner
				}
				return left.Name < right.Name
			})

			var links []string
			for _, fn := range byPackageGroup[pkg][group] {
				anchor := anchorFor(fn)
				label := qualifiedLabel(fn)
				links = append(links, fmt.Sprintf("[%s](#%s)", label, anchor))
			}

			buf.WriteString(fmt.Sprintf("| **%s** | %s |\n",
				group,
				strings.Join(links, " "),
			))
		}
		buf.WriteString("\n")
	}

	buf.WriteString("\n\n")

	// ---------------- Details ----------------
	for _, pkg := range packages {
		buf.WriteString("## " + pkg + " API\n\n")
		groupNames := make([]string, 0, len(byPackageGroup[pkg]))
		for g := range byPackageGroup[pkg] {
			groupNames = append(groupNames, g)
		}
		sort.Strings(groupNames)

		for _, group := range groupNames {
			buf.WriteString("### " + group + "\n\n")
			sort.Slice(byPackageGroup[pkg][group], func(i, j int) bool {
				left := byPackageGroup[pkg][group][i]
				right := byPackageGroup[pkg][group][j]
				if left.Name == right.Name {
					return left.Owner < right.Owner
				}
				return left.Name < right.Name
			})
			for _, fn := range byPackageGroup[pkg][group] {
				anchor := anchorFor(fn)

				header := qualifiedLabel(fn)
				if fn.Behavior != "" {
					header += " · " + fn.Behavior
				}
				if fn.Fluent == "true" {
					header += " · fluent"
				}

				buf.WriteString(fmt.Sprintf("#### <a id=\"%s\"></a>%s\n\n", anchor, header))

				if fn.Description != "" {
					buf.WriteString(fn.Description + "\n\n")
				}

				for _, ex := range fn.Examples {
					if ex.Label != "" && len(fn.Examples) > 1 {
						buf.WriteString(fmt.Sprintf("_Example: %s_\n\n", ex.Label))
					}

					buf.WriteString("```go\n")
					for _, line := range strings.Split(ex.Code, "\n") {
						trimmed := strings.TrimSpace(line)
						if trimmed == "" {
							continue
						}
						if strings.HasPrefix(trimmed, "_ =") {
							continue
						}
						buf.WriteString(line + "\n")
					}
					buf.WriteString("```\n\n")
				}
			}
		}
		buf.WriteString("\n")
	}

	return strings.TrimRight(buf.String(), "\n")
}

//
// ------------------------------------------------------------
// README replacement
// ------------------------------------------------------------
//

func replaceAPISection(readme, api string) (string, error) {
	start := strings.Index(readme, apiStart)
	end := strings.Index(readme, apiEnd)

	if start == -1 || end == -1 || end < start {
		return "", fmt.Errorf("API anchors not found or malformed")
	}

	var out bytes.Buffer
	out.WriteString(readme[:start+len(apiStart)])
	out.WriteString("\n\n")
	out.WriteString(api)
	out.WriteString("\n")
	out.WriteString(readme[end:])

	return out.String(), nil
}

func countTests(root string) (int, error) {
	cmd := exec.Command("go", "test", "./...", "-count=1", "-json")
	cmd.Dir = root

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("go test -json: %w\n%s", err, out.String())
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
		if event.Action == "run" && event.Test != "" {
			total++
		}
	}

	return total, nil
}

var testsBadgePattern = regexp.MustCompile(`tests-\d+-brightgreen`)

func updateTestsSection(readme string, tests int) (string, error) {
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

	badge := fmt.Sprintf("%s    <img src=\"https://img.shields.io/badge/tests-%d-brightgreen\" alt=\"Tests\">\n", leading, tests)

	return before + badge + after, nil
}

//
// ------------------------------------------------------------
// Helpers
// ------------------------------------------------------------
//

func findRoot() (string, error) {
	wd, _ := os.Getwd()
	if fileExists(filepath.Join(wd, "go.mod")) {
		return wd, nil
	}
	parent := filepath.Join(wd, "..")
	if fileExists(filepath.Join(parent, "go.mod")) {
		return filepath.Clean(parent), nil
	}
	return "", fmt.Errorf("could not find project root")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func normalizeIndent(lines []string) []string {
	min := -1

	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if min == -1 || n < min {
			min = n
		}
	}

	if min <= 0 {
		return lines
	}

	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= min {
			out[i] = l[min:]
		} else {
			out[i] = strings.TrimLeft(l, " \t")
		}
	}

	return out
}
