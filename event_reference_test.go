package queue

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

type eventKindDefinition struct {
	name string
	kind EventKind
}

type documentedEventKind struct {
	layer EventLayer
	line  int
}

// TestEventReferenceMatchesExportedKinds prevents the public event catalog,
// layer mapping, and human-facing references from drifting independently.
func TestEventReferenceMatchesExportedKinds(t *testing.T) {
	t.Parallel()

	root := eventReferenceRoot(t)
	definitions := parsePackageEventKindDefinitions(t, root)
	readmeKinds := parseReadmeEventReference(t, filepath.Join(root, "README.md"))
	eventsDocKinds := parseEventsDocEventReference(t, filepath.Join(root, "docs", "events.md"))

	if len(readmeKinds) != len(definitions) {
		t.Errorf("README event rows = %d, exported EventKind constants = %d", len(readmeKinds), len(definitions))
	}
	if len(eventsDocKinds) != len(definitions) {
		t.Errorf("docs/events.md event identifiers = %d, exported EventKind constants = %d", len(eventsDocKinds), len(definitions))
	}
	for _, definition := range definitions {
		documented, ok := readmeKinds[definition.kind]
		if !ok {
			t.Errorf("README Events reference is missing %s (%q)", definition.name, definition.kind)
		} else if got := eventLayerForKind(definition.kind); got != documented.layer {
			t.Errorf("README line %d assigns %s (%q) to layer %q, runtime maps it to %q", documented.line, definition.name, definition.kind, documented.layer, got)
		}
		if _, ok := eventsDocKinds[definition.name]; !ok {
			t.Errorf("docs/events.md Event kinds section is missing %s", definition.name)
		}
	}

	exported := make(map[EventKind]string, len(definitions))
	for _, definition := range definitions {
		exported[definition.kind] = definition.name
	}
	for kind, documented := range readmeKinds {
		if _, ok := exported[kind]; !ok {
			t.Errorf("README line %d documents unknown EventKind %q", documented.line, kind)
		}
	}
	exportedNames := exportedEventKindNames(definitions)
	for name, line := range eventsDocKinds {
		if _, ok := exportedNames[name]; !ok {
			t.Errorf("docs/events.md line %d documents unknown EventKind identifier %s", line, name)
		}
	}
}

// TestParseEventKindDefinitionsFindsInferredConstants prevents syntactically
// valid typed constants from bypassing the documentation parity contract.
func TestParseEventKindDefinitionsFindsInferredConstants(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "event_kinds.go")
	source := `package fixture

type EventKind string

const (
	EventExplicit EventKind = "explicit"
	EventConverted = EventKind("converted")
	eventInheritedBase EventKind = "inherited"
	EventInherited
	EventComposed = EventConverted + "_composed"
)
`
	if err := os.WriteFile(filename, []byte(source), 0o600); err != nil {
		t.Fatalf("write EventKind parser fixture: %v", err)
	}

	directory := filepath.Dir(filename)
	additionalSource := `package fixture

type EventAlias = EventKind

const (
	EventSeparate EventAlias = "separate"
	EventCrossFile = EventConverted + "_cross_file"
)
`
	if err := os.WriteFile(filepath.Join(directory, "event_kinds_additional.go"), []byte(additionalSource), 0o600); err != nil {
		t.Fatalf("write additional EventKind parser fixture: %v", err)
	}

	definitions := parsePackageEventKindDefinitions(t, directory)
	want := map[string]EventKind{
		"EventExplicit":  "explicit",
		"EventConverted": "converted",
		"EventInherited": "inherited",
		"EventComposed":  "converted_composed",
		"EventSeparate":  "separate",
		"EventCrossFile": "converted_cross_file",
	}
	if len(definitions) != len(want) {
		t.Fatalf("EventKind definitions = %d, want %d: %+v", len(definitions), len(want), definitions)
	}
	for _, definition := range definitions {
		if wantKind, ok := want[definition.name]; !ok || definition.kind != wantKind {
			t.Errorf("EventKind definition %s = %q, want %q", definition.name, definition.kind, wantKind)
		}
	}
}

// parsePackageEventKindDefinitions discovers EventKind constants across every
// production file so moving or adding a declaration cannot bypass the catalog.
func parsePackageEventKindDefinitions(t *testing.T, directory string) []eventKindDefinition {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("read package directory %s: %v", directory, err)
	}

	filenames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		filenames = append(filenames, filepath.Join(directory, entry.Name()))
	}

	kindTypes := parseEventKindTypeAliases(t, filenames)
	knownKinds := make(map[string]EventKind)
	definitionsByName := make(map[string]eventKindDefinition)
	seenKinds := make(map[EventKind]string)
	for {
		knownBefore := len(knownKinds)
		for _, filename := range filenames {
			for _, definition := range parseEventKindDefinitionsFromFile(t, filename, kindTypes, knownKinds) {
				if previous, duplicate := definitionsByName[definition.name]; duplicate {
					if previous.kind != definition.kind {
						t.Fatalf("EventKind name %s resolves to both %q and %q", definition.name, previous.kind, definition.kind)
					}
					continue
				}
				if previous, duplicate := seenKinds[definition.kind]; duplicate {
					t.Fatalf("EventKind value %q is shared by %s and %s across package files", definition.kind, previous, definition.name)
				}
				definitionsByName[definition.name] = definition
				seenKinds[definition.kind] = definition.name
			}
		}
		if len(knownKinds) == knownBefore {
			break
		}
	}
	if len(definitionsByName) == 0 {
		t.Fatalf("%s contains no typed EventKind constants", directory)
	}
	definitions := make([]eventKindDefinition, 0, len(definitionsByName))
	for _, definition := range definitionsByName {
		definitions = append(definitions, definition)
	}
	return definitions
}

// parseEventKindTypeAliases resolves aliases of EventKind across production
// files so a renamed spelling retains the same catalog obligation.
func parseEventKindTypeAliases(t *testing.T, filenames []string) map[string]struct{} {
	t.Helper()
	types := map[string]struct{}{"EventKind": {}}
	for {
		countBefore := len(types)
		for _, filename := range filenames {
			parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", filename, err)
			}
			for _, declaration := range parsed.Decls {
				group, ok := declaration.(*ast.GenDecl)
				if !ok || group.Tok != token.TYPE {
					continue
				}
				for _, specification := range group.Specs {
					alias, ok := specification.(*ast.TypeSpec)
					if !ok || !alias.Assign.IsValid() || !isEventKindType(alias.Type, types) {
						continue
					}
					types[alias.Name.Name] = struct{}{}
				}
			}
		}
		if len(types) == countBefore {
			return types
		}
	}
}

// eventReferenceRoot resolves documentation relative to this test file so the
// contract remains stable when tests are launched from another directory.
func eventReferenceRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve event reference test path")
	}
	return filepath.Dir(filename)
}

// parseEventKindDefinitionsFromFile resolves the EventKind constants declared
// by one source file and permits files that contain no event declarations.
func parseEventKindDefinitionsFromFile(t *testing.T, filename string, kindTypes map[string]struct{}, knownKinds map[string]EventKind) []eventKindDefinition {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", filename, err)
	}

	definitions := make([]eventKindDefinition, 0)
	seenNames := make(map[string]struct{})
	seenKinds := make(map[EventKind]string)
	for _, declaration := range parsed.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		var inheritedType ast.Expr
		var inheritedValues []ast.Expr
		for _, specification := range group.Specs {
			values, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			effectiveType := values.Type
			effectiveValues := values.Values
			if len(effectiveValues) == 0 {
				effectiveType = inheritedType
				effectiveValues = inheritedValues
			} else {
				inheritedType = effectiveType
				inheritedValues = effectiveValues
			}
			if len(effectiveValues) != len(values.Names) {
				if isEventKindType(effectiveType, kindTypes) || expressionsReferenceEventKind(effectiveValues, kindTypes, knownKinds) {
					t.Fatalf("%s EventKind declaration must pair every name with one value", filename)
				}
				continue
			}
			for index, nameIdentifier := range values.Names {
				kind, inferred, err := eventKindExpressionValue(effectiveValues[index], kindTypes, knownKinds)
				if err != nil {
					t.Fatalf("resolve %s value: %v", nameIdentifier.Name, err)
				}
				if isEventKindType(effectiveType, kindTypes) && !inferred {
					kind, err = explicitEventKindValue(effectiveValues[index], kindTypes, knownKinds)
					if err != nil {
						continue
					}
					inferred = true
				}
				if !inferred {
					continue
				}
				name := nameIdentifier.Name
				knownKinds[name] = kind
				if !ast.IsExported(name) {
					continue
				}
				if previous, duplicate := seenKinds[kind]; duplicate {
					t.Fatalf("EventKind value %q is shared by %s and %s", kind, previous, name)
				}
				if _, duplicate := seenNames[name]; duplicate {
					t.Fatalf("EventKind name %s is duplicated", name)
				}
				seenKinds[kind] = name
				seenNames[name] = struct{}{}
				definitions = append(definitions, eventKindDefinition{name: name, kind: kind})
			}
		}
	}
	return definitions
}

// expressionsReferenceEventKind reports whether any expression has the public
// event kind type through conversion, inheritance, or composition.
func expressionsReferenceEventKind(expressions []ast.Expr, kindTypes map[string]struct{}, knownKinds map[string]EventKind) bool {
	for _, expression := range expressions {
		if _, ok, _ := eventKindExpressionValue(expression, kindTypes, knownKinds); ok {
			return true
		}
	}
	return false
}

// eventKindExpressionValue resolves expressions whose inferred constant type is EventKind.
func eventKindExpressionValue(expression ast.Expr, kindTypes map[string]struct{}, knownKinds map[string]EventKind) (EventKind, bool, error) {
	switch typed := expression.(type) {
	case *ast.CallExpr:
		if !isEventKindType(typed.Fun, kindTypes) || len(typed.Args) != 1 {
			return "", false, nil
		}
		value, err := stringConstantValue(typed.Args[0], kindTypes, knownKinds)
		return EventKind(value), true, err
	case *ast.Ident:
		value, ok := knownKinds[typed.Name]
		return value, ok, nil
	case *ast.ParenExpr:
		return eventKindExpressionValue(typed.X, kindTypes, knownKinds)
	case *ast.BinaryExpr:
		if typed.Op != token.ADD {
			return "", false, nil
		}
		_, leftTyped, err := eventKindExpressionValue(typed.X, kindTypes, knownKinds)
		if err != nil {
			return "", false, err
		}
		_, rightTyped, err := eventKindExpressionValue(typed.Y, kindTypes, knownKinds)
		if err != nil {
			return "", false, err
		}
		if !leftTyped && !rightTyped {
			return "", false, nil
		}
		left, err := stringConstantValue(typed.X, kindTypes, knownKinds)
		if err != nil {
			return "", false, err
		}
		right, err := stringConstantValue(typed.Y, kindTypes, knownKinds)
		if err != nil {
			return "", false, err
		}
		return EventKind(left + right), true, nil
	default:
		return "", false, nil
	}
}

// explicitEventKindValue resolves the string value of an explicitly typed EventKind constant.
func explicitEventKindValue(expression ast.Expr, kindTypes map[string]struct{}, knownKinds map[string]EventKind) (EventKind, error) {
	value, err := stringConstantValue(expression, kindTypes, knownKinds)
	return EventKind(value), err
}

// stringConstantValue resolves the string forms permitted by the EventKind catalog.
func stringConstantValue(expression ast.Expr, kindTypes map[string]struct{}, knownKinds map[string]EventKind) (string, error) {
	switch typed := expression.(type) {
	case *ast.BasicLit:
		if typed.Kind != token.STRING {
			return "", fmt.Errorf("expression is not a string constant")
		}
		value, err := strconv.Unquote(typed.Value)
		if err != nil {
			return "", fmt.Errorf("unquote %s: %w", typed.Value, err)
		}
		return value, nil
	case *ast.Ident:
		value, ok := knownKinds[typed.Name]
		if !ok {
			return "", fmt.Errorf("identifier %s is not a known EventKind constant", typed.Name)
		}
		return string(value), nil
	case *ast.ParenExpr:
		return stringConstantValue(typed.X, kindTypes, knownKinds)
	case *ast.CallExpr:
		if !isEventKindType(typed.Fun, kindTypes) || len(typed.Args) != 1 {
			return "", fmt.Errorf("expression is not an EventKind conversion")
		}
		return stringConstantValue(typed.Args[0], kindTypes, knownKinds)
	case *ast.BinaryExpr:
		if typed.Op != token.ADD {
			return "", fmt.Errorf("EventKind string expression uses unsupported operator %s", typed.Op)
		}
		left, err := stringConstantValue(typed.X, kindTypes, knownKinds)
		if err != nil {
			return "", err
		}
		right, err := stringConstantValue(typed.Y, kindTypes, knownKinds)
		if err != nil {
			return "", err
		}
		return left + right, nil
	default:
		return "", fmt.Errorf("unsupported EventKind expression %T", expression)
	}
}

// isEventKindType reports whether an AST expression names the public event kind type.
func isEventKindType(expression ast.Expr, kindTypes map[string]struct{}) bool {
	identifier, ok := expression.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = kindTypes[identifier.Name]
	return ok
}

// parseReadmeEventReference returns the exact kind-to-layer mapping from the
// manual Events reference table while rejecting duplicate rows.
func parseReadmeEventReference(t *testing.T, filename string) map[EventKind]documentedEventKind {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatalf("open %s: %v", filename, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close %s: %v", filename, closeErr)
		}
	}()

	kinds := make(map[EventKind]documentedEventKind)
	scanner := bufio.NewScanner(file)
	inReference := false
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if text == "### Events reference" {
			inReference = true
			continue
		}
		if !inReference {
			continue
		}
		if strings.HasPrefix(text, "## ") || strings.HasPrefix(text, "### ") {
			break
		}
		if !strings.HasPrefix(text, "|") || strings.HasPrefix(text, "| Layer ") || strings.HasPrefix(text, "| ---") {
			continue
		}
		columns := strings.Split(text, "|")
		if len(columns) != 5 {
			t.Fatalf("%s:%d event row has %d columns, want 3", filename, line, len(columns)-2)
		}
		layer := EventLayer(strings.Trim(strings.TrimSpace(columns[1]), "*"))
		kind := EventKind(strings.TrimSpace(columns[2]))
		meaning := strings.TrimSpace(columns[3])
		if layer == "" || kind == "" || meaning == "" {
			t.Fatalf("%s:%d event row has an empty layer, kind, or meaning", filename, line)
		}
		if previous, duplicate := kinds[kind]; duplicate {
			t.Fatalf("%s:%d duplicates EventKind %q from line %d", filename, line, kind, previous.line)
		}
		kinds[kind] = documentedEventKind{layer: layer, line: line}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", filename, err)
	}
	if !inReference {
		t.Fatalf("%s does not contain an Events reference heading", filename)
	}
	return kinds
}

// parseEventsDocEventReference returns the exact identifiers listed in the
// detailed Event kinds section while rejecting duplicate entries.
func parseEventsDocEventReference(t *testing.T, filename string) map[string]int {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatalf("open %s: %v", filename, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			t.Errorf("close %s: %v", filename, closeErr)
		}
	}()

	identifierPattern := regexp.MustCompile("`(Event[A-Za-z0-9]+)`")
	kinds := make(map[string]int)
	scanner := bufio.NewScanner(file)
	inReference := false
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if text == "## Event kinds" {
			inReference = true
			continue
		}
		if !inReference {
			continue
		}
		if strings.HasPrefix(text, "## ") {
			break
		}
		if !strings.HasPrefix(text, "- ") {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(text, "- "), ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			t.Fatalf("%s:%d event catalog entry must have a nonempty meaning", filename, line)
		}
		matches := identifierPattern.FindAllStringSubmatch(parts[0], -1)
		if len(matches) != 1 {
			t.Fatalf("%s:%d event catalog entry has %d EventKind identifiers, want exactly one", filename, line, len(matches))
		}
		name := matches[0][1]
		if previousLine, duplicate := kinds[name]; duplicate {
			t.Fatalf("%s:%d duplicates EventKind identifier %s from line %d", filename, line, name, previousLine)
		}
		kinds[name] = line
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan %s: %v", filename, err)
	}
	if !inReference {
		t.Fatalf("%s does not contain an Event kinds heading", filename)
	}
	return kinds
}

// exportedEventKindNames indexes the source definitions by public identifier.
func exportedEventKindNames(definitions []eventKindDefinition) map[string]struct{} {
	names := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		names[definition.name] = struct{}{}
	}
	return names
}
