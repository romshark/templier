// templgofilereg solves a very peculiar task. It provides a comparer
// that keeps track of generated `_templ.go` files given to it and
// tells whether the recent changes to it require Templiér to recompile
// and restart the application server because the internal Go code has
// changed.
//
// In dev mode, Templ uses `_templ.txt` files to allow for fast reloads
// that don't require server recompilation, but Templiér can't know when
// to just refresh the tab and when to actually recompile because Templ
// always changes the `_templ.go` file. However, when it only changes
// the text argument to `templruntime.WriteString` we can tell we don't
// need recompilation and a tab refresh is enough.
package templgofilereg

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
)

// Comparer compares the generated `_templ.go` that Templ generates
// to its previous version passed to Compare and attempts to detect
// whether a server recompilation is necessary.
type Comparer struct {
	buf    bytes.Buffer
	byName map[string]string // file name -> normalized
}

func New() *Comparer { return &Comparer{byName: map[string]string{}} }

func (c *Comparer) printTemplGoFileAST(filePath string) (string, error) {
	fset := token.NewFileSet()

	parsed, err := parser.ParseFile(fset, filePath, nil, parser.AllErrors)
	if err != nil {
		return "", fmt.Errorf("parsing: %w", err)
	}

	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "templruntime" && sel.Sel.Name == "WriteString" {
			if len(call.Args) > 2 {
				// Reset the string values in calls to `templruntime.WriteString`
				// to exclude those from the diff.
				// When a _templ.go file changes and the only changes were made to the
				// string arguments then no recompilation is necessary.
				call.Args[2] = &ast.BasicLit{Kind: token.STRING, Value: `""`}
			}
		}
		return true
	})
	c.buf.Reset()
	if err = printer.Fprint(&c.buf, token.NewFileSet(), parsed); err != nil {
		return "", fmt.Errorf("printing normalized source file: %w", err)
	}
	return c.buf.String(), nil
}

// Compare returns recompile=true if the _templ.go file under filePath
// changed in a way that requires the server to be recompiled.
// Otherwise the file only changed the text arguments in calls to
// templruntime.WriteString which means that the change can be reflected by
// _templ.txt fast reload dev file.
// Compare always returns recompile=true if the file is new.
func (c *Comparer) Compare(filePath string) (recompile bool, err error) {
	serialized, err := c.printTemplGoFileAST(filePath)
	if err != nil {
		return false, err
	}

	recompile = true // By default, assume the code changed.
	if previous := c.byName[filePath]; previous != "" {
		recompile = serialized != previous
	}
	c.byName[filePath] = serialized // Overwrite.
	return recompile, nil
}

// Remove removes the file from the registry.
// No-op if the file isn't registered.
func (c *Comparer) Remove(filePath string) {
	delete(c.byName, filePath)
}
