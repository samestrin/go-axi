package goaxi

import (
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
)

// Every exported symbol must carry a doc comment, and this is checked the way
// pkg.go.dev resolves one rather than by looking for a comment nearby.
//
// WHY THIS TEST EXISTS. Declaring a package-level var between a function's doc
// comment and the function silently reattaches the whole comment to the var. The
// source still reads correctly — the prose sits directly above the function it
// describes — and nothing warns: it compiles, vets, and formats clean.
//
// It happened to Sanitize. A `var pathPool` was introduced above it and 60 lines
// of documentation stopped belonging to the function, including the rule that a
// concurrent write while the result is being encoded is a fatal error recover()
// cannot catch. `go doc Sanitize` printed the signature and nothing else, and
// pkg.go.dev would have shown the package's main entry point as undocumented.
//
// go/doc is used deliberately instead of scanning for comment text. It resolves
// attachment exactly as the documentation tooling does, so this test fails for
// the same reason a reader would be failed.
func TestExportedSymbolsAreDocumented(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing the package: %v", err)
	}

	pkg, ok := pkgs["goaxi"]
	if !ok {
		t.Fatalf("package goaxi not found; parsed %d package(s)", len(pkgs))
	}

	d := doc.New(pkg, "github.com/samestrin/go-axi", 0)

	undocumented := func(name, kind, docText string) {
		t.Helper()
		if strings.TrimSpace(docText) == "" {
			t.Errorf("exported %s %s has no doc comment that go/doc can see. "+
				"A package-level declaration inserted between the comment and the "+
				"declaration reattaches the comment to that declaration, which "+
				"compiles and vets clean while the symbol renders as undocumented.",
				kind, name)
		}
	}

	for _, fn := range d.Funcs {
		undocumented(fn.Name, "func", fn.Doc)
	}
	for _, typ := range d.Types {
		undocumented(typ.Name, "type", typ.Doc)
		for _, fn := range typ.Funcs {
			undocumented(fn.Name, "func", fn.Doc)
		}
		for _, m := range typ.Methods {
			undocumented(typ.Name+"."+m.Name, "method", m.Doc)
		}
	}

	if len(d.Funcs) == 0 && len(d.Types) == 0 {
		t.Fatal("no exported symbols found; the test is not looking at the package")
	}
}
