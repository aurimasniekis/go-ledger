package money

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// moneyPackages are the packages that must not use binary floating point:
// the money and calendar primitives, their SQL encoding, and the bank-fact
// boundary (ADR-0013) with its fake provider, the import engine
// (ADR-0020) and the Enable Banking adapter (ADR-0023). Paths are
// relative to this package's directory.
var moneyPackages = []string{
	".",
	"../calendar",
	"../../store/sqlvalue",
	"../banking",
	"../banking/bankingtest",
	"../bankimport",
	"../../providers/enablebanking",
}

// scanOnlyPackages are money packages that link the SQLite driver, so they
// are scanned for float type names and literals (every file, test files
// included) instead of being type-checked: type-checking them from source
// would mean type-checking the whole driver.
var scanOnlyPackages = []string{
	"../../store/bankfacts",
}

// TestNoFloatInMoneyPackages is the float ban the plan asks for (F9). It
// type-checks the non-test files of each package and fails on any
// expression whose type is a float or complex number (including untyped
// constants such as 1.5, and calls like time.Duration.Hours) or a
// math/big.Float. Test files are scanned for the same type names, since
// type-checking them would mean compiling the SQLite driver from source.
func TestNoFloatInMoneyPackages(t *testing.T) {
	for _, dir := range moneyPackages {
		findings, err := floatUses(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range findings {
			t.Errorf("binary floating point in a money package: %s", f)
		}
	}
	for _, dir := range scanOnlyPackages {
		findings, err := floatNamesIn(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range findings {
			t.Errorf("binary floating point in a money package: %s", f)
		}
	}
}

// floatNamesIn scans every Go file in dir with scanFloatNames.
func floatNamesIn(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var findings []string
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		n++
		findings = append(findings, scanFloatNames(fset, f)...)
	}
	if n == 0 {
		return nil, fmt.Errorf("no Go files in %s", dir)
	}
	return findings, nil
}

// TestNoFloatCheckerCatchesFloats proves the checker is not vacuous.
func TestNoFloatCheckerCatchesFloats(t *testing.T) {
	dir := t.TempDir()
	src := `package bad

import (
	"math/big"
	"strconv"
	"time"
)

func a() int { return int(1.5 * 2) }
func b(d time.Duration) int { return int(d.Hours()) }
func c() *big.Float { return nil }
func d(s string) bool { _, err := strconv.ParseFloat(s, 64); return err == nil }
`
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad_test.go"), []byte("package bad\n\nvar _ float32\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := floatUses(dir)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{"bad.go:9", "bad.go:10", "bad.go:11", "bad.go:12", "bad_test.go:3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("checker missed %s; findings:\n%s", want, joined)
		}
	}
}

// The source importer type-checks every imported package, the standard
// library included, so one importer (and its cache) serves every check.
var (
	fset        = token.NewFileSet()
	srcImporter = importer.ForCompiler(fset, "source", nil)
)

func floatUses(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	var findings []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(name, "_test.go") {
			findings = append(findings, scanFloatNames(fset, f)...)
			continue
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no Go files in %s", dir)
	}
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}}
	conf := types.Config{Importer: srcImporter}
	if _, err := conf.Check(files[0].Name.Name, fset, files, info); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for expr, tv := range info.Types {
		if tv.Type == nil || !isFloaty(tv.Type) {
			continue
		}
		p := fset.Position(expr.Pos())
		key := fmt.Sprintf("%s:%d: %s", filepath.Base(p.Filename), p.Line, tv.Type)
		if !seen[key] {
			seen[key] = true
			findings = append(findings, key)
		}
	}
	sort.Strings(findings)
	return findings, nil
}

func isFloaty(t types.Type) bool {
	switch u := t.(type) {
	case *types.Basic:
		return u.Info()&(types.IsFloat|types.IsComplex) != 0
	case *types.Pointer:
		return isFloaty(u.Elem())
	case *types.Named:
		if o := u.Obj(); o.Pkg() != nil && o.Pkg().Path() == "math/big" && o.Name() == "Float" {
			return true
		}
		return isFloaty(u.Underlying())
	case *types.Slice:
		return isFloaty(u.Elem())
	case *types.Array:
		return isFloaty(u.Elem())
	case *types.Map:
		return isFloaty(u.Key()) || isFloaty(u.Elem())
	case *types.Tuple:
		for i := range u.Len() {
			if isFloaty(u.At(i).Type()) {
				return true
			}
		}
		return false
	case *types.Signature:
		return false // a call's result is checked on the call expression
	}
	return false
}

var floatNames = map[string]bool{
	"float32": true, "float64": true, "complex64": true, "complex128": true,
	"ParseFloat": true, "FormatFloat": true, "AppendFloat": true, "Float": true,
	"Float32": true, "Float64": true, "NewFloat": true,
}

func scanFloatNames(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Ident:
			if floatNames[x.Name] {
				p := fset.Position(x.Pos())
				out = append(out, fmt.Sprintf("%s:%d: %s", filepath.Base(p.Filename), p.Line, x.Name))
			}
		case *ast.BasicLit:
			if x.Kind == token.FLOAT || x.Kind == token.IMAG {
				p := fset.Position(x.Pos())
				out = append(out, fmt.Sprintf("%s:%d: literal %s", filepath.Base(p.Filename), p.Line, x.Value))
			}
		}
		return true
	})
	return out
}
