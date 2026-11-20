package domain

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestFinancePackagesImportNoProvider enforces ADR-0001 rule 3 and the B1
// acceptance criterion: no finance package (internal/domain/...) and no
// persistence package (internal/store/...) reaches a provider adapter
// (internal/providers/...), directly or through any other package of this
// module. depguard in .golangci.yml catches direct imports at lint time;
// this test also catches transitive ones and runs with go test.
func TestFinancePackagesImportNoProvider(t *testing.T) {
	root, module := moduleRoot(t)
	for _, start := range []string{"internal/domain", "internal/store"} {
		findings, err := providerReach(root, module, start)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range findings {
			t.Errorf("finance or store package reaches a provider adapter: %s", f)
		}
	}
}

// TestProviderReachCatchesTransitiveImports proves the checker is not vacuous.
func TestProviderReachCatchesTransitiveImports(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                          "module example.com/m\n",
		"internal/domain/x/x.go":          "package x\n\nimport _ \"example.com/m/internal/helper\"\n",
		"internal/domain/y/y_test.go":     "package y\n\nimport _ \"example.com/m/internal/providers/direct\"\n",
		"internal/helper/h.go":            "package helper\n\nimport _ \"example.com/m/internal/providers/bank\"\n",
		"internal/providers/bank/b.go":    "package bank\n",
		"internal/providers/direct/d.go":  "package direct\n",
		"internal/domain/clean/c.go":      "package clean\n\nimport _ \"fmt\"\n",
		"internal/domain/clean/c_test.go": "package clean\n",
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := providerReach(root, "example.com/m", "internal/domain")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(findings, "\n")
	for _, want := range []string{"internal/domain/x -> example.com/m/internal/helper -> example.com/m/internal/providers/bank", "internal/domain/y -> example.com/m/internal/providers/direct"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missed %q; findings:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "clean") {
		t.Errorf("false positive: %s", joined)
	}
}

func moduleRoot(t *testing.T) (dir, module string) {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if m, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
					return dir, strings.TrimSpace(m)
				}
			}
			t.Fatal("go.mod without a module line")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// providerReach returns, for every package directory under start, each
// import chain inside the module that ends in internal/providers. The
// starting package's test files count; dependencies are followed through
// their non-test files, which is what gets linked.
func providerReach(root, module, start string) ([]string, error) {
	providers := module + "/internal/providers"
	fset := token.NewFileSet()
	cache := map[string][]string{}
	imports := func(dir string, withTests bool) ([]string, error) {
		key := dir + "|" + strconv.FormatBool(withTests)
		if v, ok := cache[key]; ok {
			return v, nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".go") || (!withTests && strings.HasSuffix(n, "_test.go")) {
				continue
			}
			f, err := parser.ParseFile(fset, filepath.Join(dir, n), nil, parser.ImportsOnly)
			if err != nil {
				return nil, err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if p == module || strings.HasPrefix(p, module+"/") {
					seen[p] = true
				}
			}
		}
		out := make([]string, 0, len(seen))
		for p := range seen {
			out = append(out, p)
		}
		sort.Strings(out)
		cache[key] = out
		return out, nil
	}
	var findings []string
	err := filepath.WalkDir(filepath.Join(root, start), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		first, err := imports(path, true)
		if err != nil {
			return err
		}
		// Breadth-first over module packages, remembering one chain each.
		chain := map[string]string{}
		queue := []string{}
		for _, p := range first {
			chain[p] = rel + " -> " + p
			queue = append(queue, p)
		}
		for len(queue) > 0 {
			p := queue[0]
			queue = queue[1:]
			if p == providers || strings.HasPrefix(p, providers+"/") {
				findings = append(findings, chain[p])
				continue
			}
			dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(p, module), "/")))
			next, err := imports(dir, false)
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			for _, n := range next {
				if _, ok := chain[n]; !ok {
					chain[n] = chain[p] + " -> " + n
					queue = append(queue, n)
				}
			}
		}
		return nil
	})
	sort.Strings(findings)
	return findings, err
}
