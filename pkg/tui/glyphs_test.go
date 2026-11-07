package tui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The Linux console's font has no em dash, ellipsis, tick or cross: it
// draws each as a filled box. Text the installer puts on screen keeps to
// glyphs the console has (·, », •, the box and block characters do).
func TestOnScreenTextUsesConsoleGlyphs(t *testing.T) {
	const missing = "—…✓✗"
	for _, dir := range []string{".", "../../cmd/slinstaller"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, f, nil, 0) // comments are not parsed
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.ContainsAny(lit.Value, missing) {
					t.Errorf("%s: %s has a glyph the console cannot draw", fset.Position(lit.Pos()), lit.Value)
				}
				return true
			})
		}
	}
}
