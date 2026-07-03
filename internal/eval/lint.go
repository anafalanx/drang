package eval

import (
	"fmt"
	"io"

	"github.com/anafalanx/drang/internal/ast"
)

// DuplicateTopFns returns the names of functions declared more than once at a program's
// top level, in first-duplication order (each duplicated name appears once). At run time a
// later `fn .foo` silently shadows an earlier one — last definition wins — which is almost
// always a mistake when both are top-level in one file. Callers turn this list into a
// warning so the shadowing is visible instead of silent.
//
// Only the top level is scanned on purpose: a `fn` nested inside an if/else branch or a
// loop is a deliberate conditional definition, not a duplicate, and must not warn.
func DuplicateTopFns(prog *ast.Program) []string {
	counts := map[string]int{}
	var dups []string
	for _, s := range prog.Stmts {
		fd, ok := s.(*ast.FnDecl)
		if !ok {
			continue
		}
		counts[fd.Name]++
		if counts[fd.Name] == 2 { // report each name once, the moment it repeats
			dups = append(dups, fd.Name)
		}
	}
	return dups
}

// WarnDuplicateTopFns writes a one-line warning to w for each function DuplicateTopFns
// reports, tagged with origin (the file path or `-e`/stream label). EVERY path that runs a
// program FILE calls this — a direct run, a `-n`/`-p` stream one-liner, and a `use` import —
// so last-wins shadowing is surfaced identically everywhere. The interactive REPL and the
// embedded prelude deliberately do NOT call it: redefining a function at the REPL is normal,
// and the stdlib is trusted. Writes take the shared output mutex, matching warn(), so a
// warning never interleaves mid-line with a concurrent stderr write.
func WarnDuplicateTopFns(prog *ast.Program, origin string, w io.Writer) {
	dups := DuplicateTopFns(prog)
	if len(dups) == 0 {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	for _, name := range dups {
		fmt.Fprintf(w, "drang: warning: %s: function %s is defined more than once at the top level; the last definition wins\n", origin, name)
	}
}
