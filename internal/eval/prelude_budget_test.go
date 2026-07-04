package eval

import (
	"strings"
	"testing"
)

// TestPreludeSizeBudget is a deliberate tripwire, not a normal test.
//
// The prelude is re-parsed (and compiled) on every one-shot startup. Measured, that costs ~linear
// in its size: ~0.76 µs/line, so the current ~290-line prelude is ~0.22 ms — about 1% of the ~24 ms
// startup. That is why the prelude is NOT precompiled: at this size the win is <1% for real
// serialization/versioning machinery.
//
// But that conclusion holds ONLY while the prelude stays small. If drang grows a rich drang-written
// standard library, the per-startup parse cost climbs with it (~3k lines ≈ 2 ms ≈ 10% of startup;
// ~10k ≈ 8 ms). This budget fires well before that hurts, forcing a conscious decision:
//   - precompile the prelude (embed its AST/bytecode at build time → skip parse+compile per run), or
//   - load it lazily (only compile+define the functions a script actually uses → scales with usage,
//     not prelude size — often the better answer when a big prelude is used sparsely),
//
// then raise or remove this budget. See the 0.8 speed roadmap.
func TestPreludeSizeBudget(t *testing.T) {
	const budget = 2000 // lines; ~7x today's prelude, ~1.5 ms parse, ~6% of startup
	n := strings.Count(preludeSource, "\n") + 1
	if n > budget {
		t.Errorf("prelude is %d lines (> %d budget): its per-startup parse cost is now a meaningful "+
			"slice of startup — precompile it or load it lazily, then raise this budget (see roadmap)", n, budget)
	}
}
