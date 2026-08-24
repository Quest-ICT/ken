// Package audit holds whole-tree invariants that no single package can check about itself.
package audit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// EVERY EXPORTED STORE METHOD MUST BE REACHED BY SOMETHING THAT IS NOT A TEST.
//
// WHY THIS EXISTS. A sweep on 2026-08-21 found THIRTEEN exported methods on `*store.Store`
// and `*comm.Store` with no production caller. Among them: a targeted deny that shipped in
// every deployment's schema and denied nothing; a rename the human had been promised and
// could not reach; a vault history read; four console counters whose doc comments each named
// a badge, a stat or a confirm that did not exist.
//
// NOTHING CAUGHT IT AND NOTHING WOULD HAVE. An unused EXPORTED method is never a compile
// error, `go vet` does not look, and staticcheck's `unused` deliberately skips exported
// identifiers in a library package. So the doc comment was the only evidence any reader had
// — and it was false in all twelve cases, which is the actual finding. "Is the console's
// badge source", "used when severing", "the read behind the authorization check on a
// room-addressed send": each written confidently, each untrue the day it was written.
//
// THE ALLOWLIST IS THE POINT, NOT THE ESCAPE HATCH. Every entry needs a written reason, and
// the test FAILS IN BOTH DIRECTIONS — an allowlisted method that gains a production caller
// must leave the list. Without that second half the list rots into an exemption dump and this
// test becomes a formality that green-lights the thing it was written to stop. In practice
// that makes it a work tracker: a `pending:` entry that is still here at the next release is
// the review signal, and writing "no caller yet, and here is why" is uncomfortable enough to
// make wiring it the easier path.
func TestEveryExportedStoreMethodIsReachable(t *testing.T) {
	root := filepath.Join("..", "..")

	// Methods that may have no production caller, each with the reason it is allowed.
	// REMOVE an entry the moment its method gains one — the check below enforces that.
	allowed := map[string]string{
		"comm.Store.Poll": "one-line wrapper over PollScoped; its 77 test callers exercise the real path, " +
			"and production takes PollScoped directly with an empty scope",
		"comm.Store.PendingReplies": "pending: the capability gap is real (nothing on any surface answers " +
			"'what am I waiting for a reply on'), but placement is a design call — console-first and the " +
			"MCP freeze pull opposite ways. Two defects in the body are fixed; the routing is not decided",
		"store.Store.StationVaultHistoryFor": "pending: the console history list. The restore repair landed " +
			"first on purpose — painting 16 rows that all look recoverable when one was would have been the " +
			"same defect amplified by the fix",
		"store.Store.CountPendingPromotions": "pending: the /stations badge and the live-refresh count. " +
			"Its doc calls it 'the console's badge source' and there is no badge",
		"comm.Store.MirrorEpoch": "pending: it reports FRESH on a partial mirror rebuild, which is the one " +
			"case it exists for. Fixing that is a schema question (per-projection epochs) and ships alone",
	}

	type method struct{ pkg, name, pos string }
	var declared []method
	used := map[string]bool{}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil // a generated or broken file must not fail the invariant
		}
		isTest := strings.HasSuffix(path, "_test.go")

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv != nil && len(fn.Recv.List) == 1 && !isTest && fn.Name.IsExported() {
				// Receiver *Store, in either store package.
				if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok && id.Name == "Store" {
						declared = append(declared, method{f.Name.Name, fn.Name.Name, fset.Position(fn.Pos()).String()})
					}
				}
			}
		}
		if isTest {
			return nil // a test caller does not count as reachability; that is the whole point
		}
		// Every selector in a CALL position, plus every method value passed around, plus
		// string literals matching a name (a cheap belt against reflection and SQL — the
		// repo has zero MethodByName today, and this is what would notice if it gained one).
		ast.Inspect(f, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				used[v.Sel.Name] = true
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					used[strings.Trim(v.Value, "`\"")] = true
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// POSITIVE CONTROL ON THE INSTRUMENT. A parse that silently finds nothing exits green and
	// reads as "all clean" — the same shape as a `-run` filter matching zero tests, which this
	// project has already been burned by twice. If the walk breaks, this fails loudly instead.
	if len(declared) < 150 {
		t.Fatalf("only %d exported *Store methods found — the AST walk is broken, not the tree clean", len(declared))
	}
	if !used["PollScoped"] || !used["Migrate"] {
		t.Fatal("the usage scan found neither PollScoped nor Migrate — it is not seeing call sites, so every absence below would be spurious")
	}

	var unreachable, staleAllow []string
	seen := map[string]bool{}
	for _, m := range declared {
		key := m.pkg + ".Store." + m.name
		seen[key] = true
		reachable := used[m.name]
		_, isAllowed := allowed[key]
		switch {
		case !reachable && !isAllowed:
			unreachable = append(unreachable, key+"  ("+m.pos+")")
		case reachable && isAllowed:
			staleAllow = append(staleAllow, key+"  ("+m.pos+")")
		}
	}
	sort.Strings(unreachable)
	sort.Strings(staleAllow)

	if len(unreachable) > 0 {
		t.Errorf("%d exported *Store method(s) have no production caller. Wire it, delete it, or\n"+
			"add it to `allowed` above WITH A WRITTEN REASON — and if the reason is 'not yet', say\n"+
			"'pending:' and what it is waiting for:\n  %s", len(unreachable), strings.Join(unreachable, "\n  "))
	}
	// THE OTHER DIRECTION, and it is what stops the list rotting into an exemption dump.
	if len(staleAllow) > 0 {
		t.Errorf("%d allowlisted method(s) now HAVE a production caller — remove them from `allowed`:\n  %s",
			len(staleAllow), strings.Join(staleAllow, "\n  "))
	}
	// And an entry naming a method that no longer exists is a stale reason nobody will read.
	for key := range allowed {
		if !seen[key] {
			t.Errorf("allowlist names %q, which is not a declared *Store method — delete the entry", key)
		}
	}
}
