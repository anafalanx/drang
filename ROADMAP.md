# drang — Roadmap: what's left to complete

*Inventory dated 2026-06-27, at git `08213ea`. Grounded in DESIGN.md, MANUAL.md, a
code-level scan, and a vision-gap analysis against drang's niche (a small, parallel,
Perl-inspired scripting language for text / glue / orchestration — "reads like Ruby,
thinks like Perl, runs like Go").*

## State of the language

Roughly **80% of the way to a credible daily-driver**, and the *engine* is done:
register VM + tree-walker fallback, closures/lambdas/pipelines, a full HOF toolkit,
errors-as-values (`?` / `//`), first-class RE2 regexes (`qr//`), real concurrency
(`spawn` / channels / `pmap`), external-command orchestration, files/paths, JSON,
CSV, one-liner `-n`/`-p` mode with `BEGIN`/`END`, modules (`use`), value-level
immutability (frozen constants + module exports), a drang prelude, and standalone
`build`. What remains clusters in three honest buckets: **(1) doc/reality drift**,
**(2) `[LOCKED]`-in-DESIGN-but-unbuilt language features**, and **(3) whole stdlib
domains** a glue language reaches for hourly.

Status tags: **NOT-STARTED** · **PARTIAL** · **DEFERRED-BY-DESIGN** (a deliberate,
recorded deferral — not a bug).

---

## (a) Language core / semantics

These four are marked `[LOCKED]` in DESIGN (design ratified) but are **not built** —
the dangerous class, because the manual implied them. The manual is now honest about
them (they're listed under "Not Yet"); building them is tracked here.

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| Default + named + variadic params (`fn .f($a, $b=8080)`, `$a...`) | option maps / task fns; parse-errors today | M | NOT-STARTED |
| Slices + string indexing/substring (`$a[1..3]`, `$s[2..5]`) | core text moves; today only `take`/`drop`/`chars`. **Parses but errors at runtime** | M | PARTIAL |
| `=~` match / `s///` substitution + `$1..$n` in script mode | the Perl soul; power exists (`match`/`gsub`), operator layer doesn't | M | NOT-STARTED |
| Char ranges `'a'..'z'` (needs char literals) | lower frequency; `'a'` lexes as ILLEGAL | M | NOT-STARTED |
| Stringy-numeric coercion (`"5" + 3`) | genuine unresolved tension (§2 locked, §14 deferred) — **decide and document** | S–M | DEFERRED-BY-DESIGN |
| Ratify provisional bits (truthiness, language name / `.dr`) | working but never formally locked; doc/decision close | S | PARTIAL |
| `match`/`switch` multi-way dispatch | value/regex dispatch for text; `dispatch({...})` partly covers it | M | NOT-STARTED |

## (b) Standard library (builtins + prelude) — the biggest real gaps

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| ~~printf-grade `format` verbs (`{:.2f}`, `{:>20}`, `{:08x}`)~~ | aligned columns + fixed decimals — **DONE**: `{:spec}` extends `{}` (Python/Rust-style: fill/align/sign/#/0/width/.prec/type) | M | ✅ DONE |
| Date/time family + `sleep` (`now`, parse/format, arithmetic) | timestamps, "newer than N days", durations, backoff — wholly absent | M | NOT-STARTED |
| Hashing + encodings (`sha256`/`md5`, base64, hex, url) | checksum artifacts, decode tokens — tiny bindings, hourly use | S | NOT-STARTED |
| Random (`rand`, `rand_int`, `shuffle`, `uuid`) | jitter/backoff, temp names, sampling | S | NOT-STARTED |
| Type conversions `str()`, `float()`, `bool()` | only `int()` exists today (asymmetric) | S | NOT-STARTED |
| More math (`sqrt`, `pow`, `log`, integer-division) | `/` is always float; no int-div operator | S | NOT-STARTED |
| String helpers (`index_of`, `substr`, `pad`, `reverse`, `capitalize`) | ubiquitous; some composable but verbose | S | NOT-STARTED |
| Collection helpers (`group_by`, `uniq_by`, `partition`, `enumerate`) | common; could be prelude `.dr` | S | NOT-STARTED |
| First-class builtin values (`map($xs, basename)`) | HOF style forces wrapper lambdas — a persistent wart | M | NOT-STARTED |
| Named-capture → map `match` variant | RE2 named groups → record | S | NOT-STARTED |
| TOML / INI config parsing | fits the zmal world, but **no Go-stdlib parser** → conflicts with stdlib-only pillar; needs a decision-record | M | NOT-STARTED |
| `read_file`/`write_file` bytes/encoding knobs | binary / non-UTF-8 | S | NOT-STARTED |
| `sh()` shell-escape helper; SQL; templating; compressed I/O; `embed()`; signals | lower-frequency §11 batteries; build on demand | S–M each | DEFERRED-BY-DESIGN |

## (c) Tooling & developer experience

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| Rebuild + release/version discipline | the local `drang.exe` had silently fallen ~9h behind HEAD; add `z build` + version stamp + a release check | S | PARTIAL (binary now current) |
| `drang test` (assertions, golden output/exit-code; `example f(x)==y` / `example f(bad) fails`) | the named next phase; examples are tested externally today | M | NOT-STARTED |
| `drang fmt` (+ `--fix` = the edition/migration mechanism) | own-the-AST migrations as the taxonomy evolves; `--fix` rule design OPEN | M | NOT-STARTED |
| `-i` in-place edit for one-liner mode | `perl -i -pe` is the canonical text-munge | S–M | DEFERRED |
| REPL polish / editor support / LSP | real adoption infra, but one-user project — low priority | L | NOT-STARTED |

## (d) Runtime & quality

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| Source positions in one-liner / stream errors | a `-pe` error with no line number is a daily papercut | M | DEFERRED |
| VM↔walker: bare `use "path"` statement not compiled | `compiler.go` `default: c.fail()` → whole program falls back to the tree-walker when a bare `use` is present (captured `use(...)` compiles fine). No `OpUseMerge` | M | PARTIAL |
| VM↔walker: bare ident-as-value not compiled | forces walker fallback; minor | S | PARTIAL |
| Startup benchmark + prelude precompile (go:generate bytecode) | reserved optimization; gate behind a real benchmark first | S / M | DEFERRED-BY-DESIGN |
| `--profile` pprof output | called a freebie in §11; `sys_gc` exists, no flag | S | NOT-STARTED |
| Parser/lexer unit-test coverage | only via eval integration tests today | M | NOT-STARTED |

## (e) Polish / recent-feature follow-ups

| Item | Status |
|------|--------|
| Module privacy (every top-level `.foo` is exported) | NOT-STARTED |
| Duplicate `fn .foo` in one file = silent last-wins (should warn/error) | NOT-STARTED |
| Extensionless path precedence (bare file shadows `<name>.dr`) | DEFERRED-BY-DESIGN |
| Bare `use("x")` with parens as a discarded statement = silent no-op | DEFERRED-BY-DESIGN |
| `pmap`/`spawn` mutable-`:=`-global caveat (only `::=` consts are frozen/pmap-safe; a static resolver to reject the rest is reserved) | DEFERRED-BY-DESIGN |
| CSV follow-ups (streaming row reader, TSV, quoting styles) | DEFERRED |

---

## Already complete — don't reopen

Value-level immutability/freeze; modules (`use`); `qr//` regex literals;
`q{}`/`qq{}`/heredocs; break/continue; lambdas; integer-overflow→error; implicit
return; two-var `for $k,$v in`; `//` defined-or; `<=>`; all Phase-1 path/fs/env wins;
the prelude (`flatten`/`sum_by`/`tally`/`count_by`/`chunk`/`zip`); JSON; CSV;
one-liner `-n`/`-p` + `BEGIN`/`END`; concurrency (`spawn`/`chan`/`pmap`, input-ordered).
Must-use enforcement was deliberately dropped (`[LOCKED]`), not missing.

## Deliberately out of scope — not missing work

Ternary `?:`, `**`, bitwise, `++`/`--`; classes/inheritance/`bless`/MOP; scalar-list
context and the punctuation-variable zoo; string `eval`; a package registry;
sandboxing; an HTTP *client* (orchestrate `run(["curl", …])`); bignum; GUI/Tk hosting;
the ops/observability and distributed/multi-host growth verticals (locked to
"personal daily-driver"). **YAML/TOML** is the one genuine judgment-call: no Go-stdlib
parser, so it needs a decision-record (hand-rolled exception vs out-of-scope).

## Recommended next 3–5

1. **Ship the binary + close the doc/reality gap.** Rebuild from HEAD (done); make
   the manual honest about the `[LOCKED]`-but-unbuilt features (done); add a
   version stamp / release check. *A daily-driver whose own examples don't run fails
   "complete" on trust before any feature.*
2. ~~**printf-grade `format` verbs (b).**~~ ✅ Done — `{:spec}` mini-language over the existing `{}`.
3. **Date/time + `sleep` (b).** Conspicuous absence for orchestration. *(next)*
4. **Hashing + encodings + random (b).** Tiny bindings, highest value-per-byte.
5. **`drang test` (c).** The named next phase; lets daily-driver scripts stop rotting.

After this set, a Perl/Python refugee can do real text+glue work for an hour without
hitting a wall. `drang fmt` and `-i` are the strong follow-ups once these land.
