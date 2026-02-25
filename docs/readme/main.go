//go:build ignore
// +build ignore

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	apiStart = "<!-- api:embed:start -->"
	apiEnd   = "<!-- api:embed:end -->"
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

	return os.WriteFile(readmePath, []byte(out), 0o644)
}

//
// ------------------------------------------------------------
// Data model
// ------------------------------------------------------------
//

type FuncDoc struct {
	Category    string
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

func includeInReadmeAPI(fd *FuncDoc) bool {
	if fd.Package == "queue" && fd.Owner == "FakeQueue" && (fd.Name == "BusRegister" || fd.Name == "BusDispatch") {
		return false
	}
	if fd.Package == "queue" && (fd.Group == "Queue Runtime" || fd.Group == "Driver Integration") {
		return false
	}
	return true
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
	type parseTarget struct {
		dir      string
		category string
	}
	targets := []parseTarget{{dir: root, category: "Core"}}
	queuefakeDir := filepath.Join(root, "queuefake")
	if st, err := os.Stat(queuefakeDir); err == nil && st.IsDir() {
		targets = append(targets, parseTarget{dir: queuefakeDir, category: "Testing"})
	}
	driverDirs, err := filepath.Glob(filepath.Join(root, "driver", "*queue"))
	if err != nil {
		return nil, err
	}
	sort.Strings(driverDirs)
	for _, dir := range driverDirs {
		targets = append(targets, parseTarget{dir: dir, category: "Driver Modules"})
	}

	var out []*FuncDoc
	for _, target := range targets {
		funcs, err := parseFuncsInDir(target.dir, target.category)
		if err != nil {
			return nil, err
		}
		out = append(out, funcs...)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Category == out[j].Category {
			if out[i].Package == out[j].Package {
				if out[i].Owner == out[j].Owner {
					return out[i].Name < out[j].Name
				}
				return out[i].Owner < out[j].Owner
			}
			return out[i].Package < out[j].Package
		}
		return out[i].Category < out[j].Category
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

func bareName(fd *FuncDoc) string {
	return fd.Name
}

func ownerQualifiedName(fd *FuncDoc) string {
	if fd.Owner == "" {
		return fd.Name
	}
	return fd.Owner + "." + fd.Name
}

func packageCategoryLabel(category, pkg string) string {
	if category == "Core" && pkg == "queue" {
		return "Queue (root package)"
	}
	return pkg
}

func parseFuncsInDir(dir string, category string) ([]*FuncDoc, error) {
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
					Category:    category,
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
								Category:    category,
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
	key := fd.Category + "." + fd.Package + "." + fd.Owner + "." + fd.Name
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
	byCategoryPackageGroup := map[string]map[string]map[string][]*FuncDoc{}
	bareCounts := map[string]int{}
	ownerQualifiedCounts := map[string]int{}
	for _, fd := range funcs {
		if !includeInReadmeAPI(fd) {
			continue
		}
		if byCategoryPackageGroup[fd.Category] == nil {
			byCategoryPackageGroup[fd.Category] = map[string]map[string][]*FuncDoc{}
		}
		if byCategoryPackageGroup[fd.Category][fd.Package] == nil {
			byCategoryPackageGroup[fd.Category][fd.Package] = map[string][]*FuncDoc{}
		}
		byCategoryPackageGroup[fd.Category][fd.Package][fd.Group] = append(byCategoryPackageGroup[fd.Category][fd.Package][fd.Group], fd)
		bareCounts[bareName(fd)]++
		ownerQualifiedCounts[ownerQualifiedName(fd)]++
	}

	categories := make([]string, 0, len(byCategoryPackageGroup))
	for c := range byCategoryPackageGroup {
		categories = append(categories, c)
	}
	sort.Strings(categories)
	totalPackages := 0
	for _, c := range categories {
		totalPackages += len(byCategoryPackageGroup[c])
	}
	singlePackage := totalPackages == 1
	qualifiedLabel := func(fd *FuncDoc) string {
		bare := bareName(fd)
		if bareCounts[bare] <= 1 {
			return bare
		}
		ownerQualified := ownerQualifiedName(fd)
		if singlePackage || ownerQualifiedCounts[ownerQualified] <= 1 {
			return ownerQualified
		}
		return fd.Package + "." + ownerQualified
	}

	var buf bytes.Buffer

	// ---------------- Index ----------------
	buf.WriteString("## API Index\n\n")
	buf.WriteString("| Group | Functions |\n")
	buf.WriteString("|------:|:-----------|\n")

	// Keep the original root-package index layout and add a single driver category row.
	corePkg := "queue"
	if coreGroups, ok := byCategoryPackageGroup["Core"][corePkg]; ok {
		groupNames := make([]string, 0, len(coreGroups))
		for g := range coreGroups {
			groupNames = append(groupNames, g)
		}
		sort.Strings(groupNames)

		for _, group := range groupNames {
			if group == "Testing" {
				continue
			}
			groupFns := coreGroups[group]
			sort.Slice(groupFns, func(i, j int) bool {
				left := groupFns[i]
				right := groupFns[j]
				if left.Name == right.Name {
					return left.Owner < right.Owner
				}
				return left.Name < right.Name
			})

			groupBareCounts := map[string]int{}
			groupOwnerQualifiedCounts := map[string]int{}
			for _, fn := range groupFns {
				groupBareCounts[bareName(fn)]++
				groupOwnerQualifiedCounts[ownerQualifiedName(fn)]++
			}
			indexLabel := func(fn *FuncDoc) string {
				bare := bareName(fn)
				if groupBareCounts[bare] <= 1 {
					return bare
				}
				ownerQualified := ownerQualifiedName(fn)
				if groupOwnerQualifiedCounts[ownerQualified] <= 1 {
					return ownerQualified
				}
				return ownerQualified
			}

			var links []string
			for _, fn := range groupFns {
				links = append(links, fmt.Sprintf("[%s](#%s)", indexLabel(fn), anchorFor(fn)))
			}
			buf.WriteString(fmt.Sprintf("| **%s** | %s |\n", group, strings.Join(links, " ")))
		}
	}

	if driverPkgs, ok := byCategoryPackageGroup["Driver Modules"]; ok && len(driverPkgs) > 0 {
		packages := make([]string, 0, len(driverPkgs))
		for p := range driverPkgs {
			packages = append(packages, p)
		}
		sort.Strings(packages)
		var links []string
		for _, pkg := range packages {
			ctors := driverPkgs[pkg]["Constructors"]
			if len(ctors) == 0 {
				continue
			}
			// Prefer package-qualified constructor links in a compact format.
			hasNew := false
			hasNewWithConfig := false
			for _, fn := range ctors {
				if fn.Name == "New" {
					hasNew = true
				}
				if fn.Name == "NewWithConfig" {
					hasNewWithConfig = true
				}
			}
			var pkgLinks []string
			if hasNew {
				pkgLinks = append(pkgLinks, fmt.Sprintf("[%s.New](#%s)", pkg, strings.ToLower(pkg+"-new")))
			}
			if hasNewWithConfig {
				pkgLinks = append(pkgLinks, fmt.Sprintf("[%s.NewWithConfig](#%s)", pkg, strings.ToLower(pkg+"-newwithconfig")))
			}
			if len(pkgLinks) > 0 {
				links = append(links, strings.Join(pkgLinks, " "))
			}
		}
		if len(links) > 0 {
			buf.WriteString(fmt.Sprintf("| **Driver Constructors** | %s |\n", strings.Join(links, " ")))
		}
	}

	if testingPkgs, ok := byCategoryPackageGroup["Testing"]; ok && len(testingPkgs) > 0 {
		var links []string
		if queuefakeGroups, ok := testingPkgs["queuefake"]; ok {
			var fns []*FuncDoc
			for _, groupFns := range queuefakeGroups {
				fns = append(fns, groupFns...)
			}
			sort.Slice(fns, func(i, j int) bool {
				if fns[i].Name == fns[j].Name {
					return fns[i].Owner < fns[j].Owner
				}
				return fns[i].Name < fns[j].Name
			})
			// Compact labels: keep all methods, but drop repeated package/receiver prefixes in the row.
			for _, fn := range fns {
				label := fn.Name
				links = append(links, fmt.Sprintf("[%s](#%s)", label, anchorFor(fn)))
			}
		}
		if len(links) == 0 {
			links = append(links, "[Testing API](#testing-api)")
		}
		if len(links) > 0 {
			buf.WriteString(fmt.Sprintf("| **Testing** | %s |\n", strings.Join(links, " ")))
		}
	}

	buf.WriteString("\n")

	buf.WriteString("\n\n")

	// ---------------- Details ----------------
	for _, category := range categories {
		if category == "Core" {
			buf.WriteString("## API\n\n")
		} else if category == "Driver Modules" {
			buf.WriteString("## Driver Constructors\n\n")
		} else {
			buf.WriteString("## " + category + " API\n\n")
		}
		packages := make([]string, 0, len(byCategoryPackageGroup[category]))
		for p := range byCategoryPackageGroup[category] {
			packages = append(packages, p)
		}
		sort.Strings(packages)

		for _, pkg := range packages {
			if category == "Core" && pkg == "queue" {
				// Preserve original root API detail layout (no extra package heading).
			} else {
				buf.WriteString("### " + packageCategoryLabel(category, pkg) + "\n\n")
			}
			groupNames := make([]string, 0, len(byCategoryPackageGroup[category][pkg]))
			for g := range byCategoryPackageGroup[category][pkg] {
				groupNames = append(groupNames, g)
			}
			sort.Strings(groupNames)

			flattenDriverConstructors := category == "Driver Modules"
			for _, group := range groupNames {
				if !(flattenDriverConstructors && group == "Constructors") {
					buf.WriteString("#### " + group + "\n\n")
				}
				sort.Slice(byCategoryPackageGroup[category][pkg][group], func(i, j int) bool {
					left := byCategoryPackageGroup[category][pkg][group][i]
					right := byCategoryPackageGroup[category][pkg][group][j]
					if left.Name == right.Name {
						return left.Owner < right.Owner
					}
					return left.Name < right.Name
				})
				for _, fn := range byCategoryPackageGroup[category][pkg][group] {
					anchor := anchorFor(fn)

					header := qualifiedLabel(fn)
					if fn.Group == "Testing" && fn.Owner != "" {
						header = ownerQualifiedName(fn)
					}
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
