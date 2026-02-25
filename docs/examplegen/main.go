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
	manualExampleTag    = "examplegen:manual"
	generatedExampleTag = "examplegen:generated"
)

func main() {
	if err := run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	fmt.Println("✔ Examples generated in ./examples/")
}

func run() error {
	root, err := findRoot()
	if err != nil {
		return err
	}

	examplesDir := filepath.Join(root, "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		return err
	}
	if err := pruneGeneratedExamples(examplesDir); err != nil {
		return err
	}

	modPath, err := modulePath(root)
	if err != nil {
		return err
	}

	type target struct {
		dir        string
		importPath string
	}
	targets := []target{{dir: root, importPath: modPath}}
	if st, err := os.Stat(filepath.Join(root, "queuefake")); err == nil && st.IsDir() {
		targets = append(targets, target{
			dir:        filepath.Join(root, "queuefake"),
			importPath: modPath + "/queuefake",
		})
	}

	for _, target := range targets {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, target.dir, nil, parser.ParseComments)
		if err != nil {
			return err
		}

		pkgName, err := selectPackage(pkgs)
		if err != nil {
			return err
		}

		pkg, ok := pkgs[pkgName]
		if !ok {
			return fmt.Errorf(`package %q not found in %s`, pkgName, target.dir)
		}

		funcs := map[string]*FuncDoc{}

		for filename, file := range pkg.Files {
			if strings.Contains(filename, "_test.go") {
				continue
			}

			for name, fd := range extractFuncDocs(fset, filename, file) {
				fd.Package = pkgName
				if existing, ok := funcs[name]; ok {
					existing.Examples = append(existing.Examples, fd.Examples...)
				} else {
					funcs[name] = fd
				}
			}
		}

		for _, fd := range funcs {
			sort.Slice(fd.Examples, func(i, j int) bool {
				return fd.Examples[i].Line < fd.Examples[j].Line
			})

			if err := writeMain(examplesDir, fd, modPath, target.importPath); err != nil {
				return err
			}
		}
	}

	return nil
}

func pruneGeneratedExamples(examplesDir string) error {
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(examplesDir, e.Name())
		mainPath := filepath.Join(dir, "main.go")
		data, readErr := os.ReadFile(mainPath)
		if readErr != nil {
			// If there's no main.go (or unreadable), treat it as manual and leave it alone.
			continue
		}
		text := string(data)
		if strings.Contains(text, manualExampleTag) {
			continue
		}
		if !strings.Contains(text, generatedExampleTag) {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

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

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}

	return "", fmt.Errorf("module path not found in go.mod")
}

//
// ------------------------------------------------------------
// Data models
// ------------------------------------------------------------
//

type FuncDoc struct {
	Package     string
	Owner       string
	Name        string
	Group       string
	Description string
	Examples    []Example
}

type Example struct {
	FuncName string
	File     string
	Label    string
	Line     int
	Code     string
}

//
// ------------------------------------------------------------
// Example extraction
// ------------------------------------------------------------
//

var exampleHeader = regexp.MustCompile(`(?i)^\s*Example:\s*(.*)$`)
var groupHeader = regexp.MustCompile(`(?i)^\s*@group\s+(.+)$`)

type docLine struct {
	text string
	pos  token.Pos
}

func extractFuncDocs(
	fset *token.FileSet,
	filename string,
	file *ast.File,
) map[string]*FuncDoc {

	out := map[string]*FuncDoc{}

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Doc == nil {
				continue
			}
			name := d.Name.Name
			if !ast.IsExported(name) {
				continue
			}
			owner := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				owner = receiverOwner(d.Recv.List[0].Type)
				if owner != "" && !ast.IsExported(owner) {
					continue
				}
			}
			mergeFuncDoc(out, &FuncDoc{
				Owner:       owner,
				Name:        name,
				Group:       extractGroup(d.Doc),
				Description: extractFuncDescription(d.Doc),
				Examples:    extractBlocks(fset, filename, name, d.Doc),
			})
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
					for _, nameIdent := range field.Names {
						name := nameIdent.Name
						if !ast.IsExported(name) {
							continue
						}
						mergeFuncDoc(out, &FuncDoc{
							Owner:       typeSpec.Name.Name,
							Name:        name,
							Group:       extractGroup(doc),
							Description: extractFuncDescription(doc),
							Examples:    extractBlocks(fset, filename, name, doc),
						})
					}
				}
			}
		}
	}

	return out
}

func mergeFuncDoc(out map[string]*FuncDoc, fd *FuncDoc) {
	key := funcDocKey(fd)
	if existing, ok := out[key]; ok {
		existing.Examples = append(existing.Examples, fd.Examples...)
		if existing.Description == "" && fd.Description != "" {
			existing.Description = fd.Description
		}
		if existing.Group == "Other" && fd.Group != "Other" {
			existing.Group = fd.Group
		}
		return
	}
	out[key] = fd
}

func funcDocKey(fd *FuncDoc) string {
	if fd.Owner == "" {
		return fd.Name
	}
	return fd.Owner + "." + fd.Name
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
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

func extractGroup(group *ast.CommentGroup) string {
	lines := docLines(group)

	for _, dl := range lines {
		trimmed := strings.TrimSpace(dl.text)
		if m := groupHeader.FindStringSubmatch(trimmed); m != nil {
			return strings.TrimSpace(m[1])
		}
	}

	return "Other"
}

func extractFuncDescription(group *ast.CommentGroup) string {
	lines := docLines(group)
	var desc []string

	for _, dl := range lines {
		trimmed := strings.TrimSpace(dl.text)

		// Stop before Example or @group
		if exampleHeader.MatchString(trimmed) || groupHeader.MatchString(trimmed) {
			break
		}

		if len(desc) == 0 && trimmed == "" {
			continue
		}

		desc = append(desc, dl.text)
	}

	for len(desc) > 0 && strings.TrimSpace(desc[len(desc)-1]) == "" {
		desc = desc[:len(desc)-1]
	}

	return strings.Join(desc, "\n")
}

func docLines(group *ast.CommentGroup) []docLine {
	var lines []docLine

	for _, c := range group.List {
		text := c.Text

		if strings.HasPrefix(text, "//") {
			line := strings.TrimPrefix(text, "//")
			if strings.HasPrefix(line, " ") {
				line = line[1:]
			}
			if strings.HasPrefix(line, "\t") {
				line = line[1:]
			}
			lines = append(lines, docLine{
				text: line,
				pos:  c.Slash,
			})
		}
	}

	return lines
}

func extractBlocks(
	fset *token.FileSet,
	filename, funcName string,
	group *ast.CommentGroup,
) []Example {

	var out []Example
	lines := docLines(group)

	var label string
	var collected []string
	var startLine int
	inExample := false

	flush := func() {
		if len(collected) == 0 {
			return
		}

		out = append(out, Example{
			FuncName: funcName,
			File:     filename,
			Label:    label,
			Line:     startLine,
			Code:     strings.Join(collected, "\n"),
		})

		collected = nil
		label = ""
		inExample = false
	}

	for _, dl := range lines {
		raw := dl.text
		trimmed := strings.TrimSpace(raw)

		if m := exampleHeader.FindStringSubmatch(trimmed); m != nil {
			flush()
			inExample = true
			label = strings.TrimSpace(m[1])
			startLine = fset.Position(dl.pos).Line
			continue
		}

		if !inExample {
			continue
		}

		collected = append(collected, raw)
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
// Write ./examples/<func>/main.go
// ------------------------------------------------------------
//

func writeMain(base string, fd *FuncDoc, moduleImportPath, importPath string) error {
	if len(fd.Examples) == 0 {
		return nil
	}

	if importPath == "" {
		return fmt.Errorf("import path cannot be empty")
	}

	dir := filepath.Join(base, exampleDirName(fd))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var buf bytes.Buffer

	// Build tag
	buf.WriteString("//go:build ignore\n")
	buf.WriteString("// +build ignore\n\n")
	buf.WriteString("// " + generatedExampleTag + "\n\n")

	buf.WriteString("package main\n\n")

	imports := map[string]bool{
		importPath: true,
	}

	for _, ex := range fd.Examples {
		if strings.Contains(ex.Code, "fmt.") {
			imports["fmt"] = true
		}
		if strings.Contains(ex.Code, "json.") {
			imports["encoding/json"] = true
		}
		if strings.Contains(ex.Code, "strings.") {
			imports["strings"] = true
		}
		if strings.Contains(ex.Code, "os.") {
			imports["os"] = true
		}
		if strings.Contains(ex.Code, "slog.") {
			imports["log/slog"] = true
		}
		if strings.Contains(ex.Code, "context.") {
			imports["context"] = true
		}
		if strings.Contains(ex.Code, "queue.") && importPath != moduleImportPath {
			imports[moduleImportPath] = true
		}
		if strings.Contains(ex.Code, "queuefake.") && importPath != moduleImportPath+"/queuefake" {
			imports[moduleImportPath+"/queuefake"] = true
		}
		if strings.Contains(ex.Code, "bus.") {
			imports[moduleImportPath+"/bus"] = true
		}
		if strings.Contains(ex.Code, "regexp.") {
			imports["regexp"] = true
		}
		if strings.Contains(ex.Code, "syscall.") {
			imports["syscall"] = true
		}
		if strings.Contains(ex.Code, "redis.") {
			imports["github.com/redis/go-redis/v9"] = true
		}
		if strings.Contains(ex.Code, "time.") {
			imports["time"] = true
		}
		if strings.Contains(ex.Code, "gocron") {
			imports["github.com/go-co-op/gocron/v2"] = true
		}
		if strings.Contains(ex.Code, "scheduler") {
			imports["github.com/goforj/scheduler"] = true
		}
		if strings.Contains(ex.Code, "filepath.") {
			imports["path/filepath"] = true
		}
		if strings.Contains(ex.Code, "godump.") {
			imports["github.com/goforj/godump"] = true
		}
		if strings.Contains(ex.Code, "rand.") {
			imports["crypto/rand"] = true
		}
		if strings.Contains(ex.Code, "base64.") {
			imports["encoding/base64"] = true
		}
	}

	if len(imports) == 1 {
		buf.WriteString("import ")
		for imp := range imports {
			buf.WriteString(fmt.Sprintf("%q", imp))
		}
		buf.WriteString("\n\n")
	} else {
		buf.WriteString("import (\n")
		keys := make([]string, 0, len(imports))
		for k := range imports {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, imp := range keys {
			buf.WriteString("\t\"" + imp + "\"\n")
		}
		buf.WriteString(")\n\n")
	}

	if len(fd.Examples) == 1 {
		buf.WriteString("func main() {\n")
		if fd.Description != "" {
			for _, line := range strings.Split(fd.Description, "\n") {
				buf.WriteString("\t// " + line + "\n")
			}
			buf.WriteString("\n")
		}
		ex := fd.Examples[0]
		if ex.Label != "" {
			buf.WriteString("\t// Example: " + ex.Label + "\n")
		}
		ex.Code = strings.TrimLeft(ex.Code, "\n")
		for _, line := range strings.Split(ex.Code, "\n") {
			if strings.TrimSpace(line) == "" {
				buf.WriteString("\n")
			} else {
				buf.WriteString("\t" + line + "\n")
			}
		}
		buf.WriteString("}\n")
		return os.WriteFile(filepath.Join(dir, "main.go"), buf.Bytes(), 0o644)
	}

	buf.WriteString("func main() {\n")
	for i := range fd.Examples {
		buf.WriteString(fmt.Sprintf("\texample%d()\n", i+1))
	}
	buf.WriteString("}\n\n")

	for i, ex := range fd.Examples {
		buf.WriteString(fmt.Sprintf("func example%d() {\n", i+1))

		if i == 0 && fd.Description != "" {
			for _, line := range strings.Split(fd.Description, "\n") {
				buf.WriteString("\t// " + line + "\n")
			}
			buf.WriteString("\n")
		}

		if ex.Label != "" {
			buf.WriteString("\t// Example: " + ex.Label + "\n")
		}

		ex.Code = strings.TrimLeft(ex.Code, "\n")
		for _, line := range strings.Split(ex.Code, "\n") {
			if strings.TrimSpace(line) == "" {
				buf.WriteString("\n")
			} else {
				buf.WriteString("\t" + line + "\n")
			}
		}
		buf.WriteString("}\n\n")
	}

	return os.WriteFile(filepath.Join(dir, "main.go"), buf.Bytes(), 0o644)
}

func exampleDirName(fd *FuncDoc) string {
	if fd.Package != "" && fd.Package != "queue" && fd.Owner == "" {
		return strings.ToLower(fd.Package) + "-" + strings.ToLower(fd.Name)
	}
	if fd.Owner == "" {
		return strings.ToLower(fd.Name)
	}
	if fd.Package != "" && fd.Package != "queue" {
		return strings.ToLower(fd.Package) + "-" + strings.ToLower(fd.Owner) + "-" + strings.ToLower(fd.Name)
	}
	return strings.ToLower(fd.Owner) + "-" + strings.ToLower(fd.Name)
}
