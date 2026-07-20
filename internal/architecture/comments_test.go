package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestWorkflowProductionDeclarationsAreDocumented keeps the extracted engine and compatibility facade aligned with repository comment rules.
func TestWorkflowProductionDeclarationsAreDocumented(t *testing.T) {
	repository := repositoryRoot(t)
	for _, directory := range []string{filepath.Join(repository, "internal", "workflow"), filepath.Join(repository, "bus")} {
		for _, missing := range undocumentedProductionDeclarations(t, directory) {
			t.Errorf("missing name-first declaration comment: %s", missing)
		}
	}
}

// undocumentedProductionDeclarations reports functions plus exported values and types that lack a name-first declaration comment.
func undocumentedProductionDeclarations(t *testing.T, directory string) []string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read production declarations in %s: %v", directory, err)
	}
	var missing []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filename := filepath.Join(directory, entry.Name())
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, filename, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse production declarations for %s: %v", filename, err)
		}
		for _, declaration := range file.Decls {
			switch typed := declaration.(type) {
			case *ast.FuncDecl:
				if !commentStartsWith(typed.Doc, typed.Name.Name) {
					missing = append(missing, declarationLocation(set, filename, typed.Pos(), typed.Name.Name))
				}
			case *ast.GenDecl:
				for _, specification := range typed.Specs {
					switch spec := specification.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(spec.Name.Name) && !commentStartsWith(firstComment(spec.Doc, typed.Doc), spec.Name.Name) {
							missing = append(missing, declarationLocation(set, filename, spec.Pos(), spec.Name.Name))
						}
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							if ast.IsExported(name.Name) && !commentStartsWith(firstComment(spec.Doc, typed.Doc), name.Name) {
								missing = append(missing, declarationLocation(set, filename, name.Pos(), name.Name))
							}
						}
					}
				}
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// firstComment prefers a declaration-specific comment while accepting a name-first single declaration group comment.
func firstComment(primary, fallback *ast.CommentGroup) *ast.CommentGroup {
	if primary != nil {
		return primary
	}
	return fallback
}

// commentStartsWith applies the repository's name-first rule after removing ordinary comment whitespace.
func commentStartsWith(comment *ast.CommentGroup, name string) bool {
	if comment == nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(comment.Text()), name+" ")
}

// declarationLocation formats one stable diagnostic for a missing declaration comment.
func declarationLocation(set *token.FileSet, filename string, position token.Pos, name string) string {
	location := set.Position(position)
	return filepath.Base(filename) + ":" + strconv.Itoa(location.Line) + " " + name
}
