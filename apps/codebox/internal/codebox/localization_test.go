package codebox

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var englishUserProse = regexp.MustCompile(`(?i)\b(the|and|to|from|for|of|by|this|that|these|those|it|its|as|with|without|cannot|could not|must|failed|failure|found|missing|invalid|unsupported|requires|required|when|while|after|before|using|is|are|was|were|should|would|could|available|unable|expected|unexpected|refusing|refused|exceeds|exceeded|not|no|already|still|only|unknown|duplicate|malformed|warning|returned|all|any|each|every|some|none|more|less|than|then|there|here|where|which|whose|what|how|why|got|empty|too|newer|older|larger|smaller|once|twice|again|next|previous|first|last|current|existing|directory|profile|environment|settings|review|ready|target|credential|credentials|download|downloaded|upload|checksum|request|response|body|value|following)\b`)
var technicalLiteralTokens = regexp.MustCompile("--[A-Za-z0-9][A-Za-z0-9-]*|`[^`]*`|'[A-Za-z0-9][A-Za-z0-9_-]*'|<[A-Za-z][A-Za-z0-9_-]*>|(?i:pull requests?)|Windows Credential Manager|KEY=value|codebox secrets profile")
var shellSyntaxKeywords = regexp.MustCompile(`\b(then|fi|do|done|if|else|elif|case|esac)\b`)

// TestUserFacingStringsAvoidEnglish guards the localization contract at the
// source boundary. It deliberately inspects display and error calls instead of
// matching every string literal: commands, configuration keys, scripts, and
// protocol values must remain unchanged.
func TestUserFacingStringsAvoidEnglish(t *testing.T) {
	t.Parallel()

	roots := []string{".", "../../cmd/codebox", "../gatewayclient", "../../../../packages/secrets", "../../../../apps/codebox-server"}
	for _, root := range roots {
		root := root
		t.Run(root, func(t *testing.T) {
			fset := token.NewFileSet()
			err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.IsDir() {
					if path != root && (entry.Name() == "testdata" || entry.Name() == "tests" || strings.HasPrefix(entry.Name(), ".")) {
						return filepath.SkipDir
					}
					return nil
				}
				if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
					return nil
				}

				file, err := parser.ParseFile(fset, path, nil, 0)
				if err != nil {
					t.Errorf("parse %s: %v", path, err)
					return nil
				}
				ast.Inspect(file, func(node ast.Node) bool {
					switch node := node.(type) {
					case *ast.CallExpr:
						for _, index := range userFacingCallArgs(node) {
							if index < len(node.Args) {
								checkEnglishLiterals(t, fset, path, node.Args[index])
							}
						}
					case *ast.KeyValueExpr:
						key, ok := node.Key.(*ast.Ident)
						if ok && userFacingField(key.Name) {
							checkEnglishLiterals(t, fset, path, node.Value)
						}
					case *ast.ReturnStmt:
						// Formatters and preview/screen builders often return prose
						// directly instead of passing it to a print call.
						if enclosingDisplayFunction(file, node.Pos()) {
							for _, result := range node.Results {
								checkEnglishLiterals(t, fset, path, result)
							}
						}
					}
					return true
				})
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", root, err)
			}
		})
	}
}

func userFacingCallArgs(call *ast.CallExpr) []int {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	name := selector.Sel.Name
	if ident, ok := selector.X.(*ast.Ident); ok {
		switch ident.Name {
		case "fmt":
			switch name {
			case "Errorf", "Printf", "Sprintf":
				if name == "Sprintf" {
					return nil // Covered when nested in a display call.
				}
				return []int{0}
			case "Fprintf":
				return []int{1}
			case "Print", "Println":
				return allArgIndexes(call.Args)
			case "Fprint", "Fprintln":
				indexes := allArgIndexes(call.Args[1:])
				for index := range indexes {
					indexes[index]++
				}
				return indexes
			}
		case "errors":
			if name == "New" {
				return []int{0}
			}
		case "http":
			if name == "Error" {
				return []int{1}
			}
		}
	}

	switch name {
	case "Say", "say", "print", "AddWarning", "Title", "Description", "Placeholder", "PrintErr", "PrintErrf", "PrintErrln":
		return []int{0}
	case "warn":
		return []int{0, 1}
	case "storeErrorf":
		return []int{3}
	case "writeError":
		return []int{3}
	}
	return nil
}

func allArgIndexes(args []ast.Expr) []int {
	indexes := make([]int, len(args))
	for index := range args {
		indexes[index] = index
	}
	return indexes
}

func userFacingField(name string) bool {
	switch name {
	case "Short", "Long", "Example", "Title", "Description", "Summary", "Reason":
		return true
	}
	return false
}

func enclosingDisplayFunction(file *ast.File, position token.Pos) bool {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || position < function.Pos() || position > function.End() {
			continue
		}
		name := function.Name.Name
		if name == "Error" || name == "String" {
			return true
		}
		for _, marker := range []string{"Format", "Screen", "Preview", "Summary", "Message", "Warning", "Title", "Description", "Line"} {
			if strings.Contains(name, marker) {
				return true
			}
		}
	}
	return false
}

func checkEnglishLiterals(t *testing.T, fset *token.FileSet, path string, expression ast.Expr) {
	t.Helper()
	ast.Inspect(expression, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Errorf("unquote %s: %v", fset.Position(literal.Pos()), err)
			return true
		}
		text = technicalLiteralTokens.ReplaceAllString(text, " ")
		if strings.Contains(text, ">&2") || strings.Contains(text, "$(") {
			// Preserve the diagnostic echo text while ignoring the surrounding
			// shell grammar in an embedded guest script.
			text = shellSyntaxKeywords.ReplaceAllString(text, " ")
		}
		if match := englishUserProse.FindString(text); match != "" {
			t.Errorf("%s: codebox-owned user-facing text contains English %q: %s", fset.Position(literal.Pos()), match, strconv.Quote(text))
		}
		return true
	})
}
