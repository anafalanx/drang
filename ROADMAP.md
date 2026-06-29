# drang — Roadmap: what's left to complete

*Inventory refreshed 2026-06-28, at git `45617b4` — **drang 0.4 released**. Grounded in
DESIGN.md, MANUAL.md, a code-level scan, and a vision-gap analysis against drang's niche (a
small, parallel, Perl-inspired scripting language for text / glue / orchestration — "reads
like Ruby, thinks like Perl, runs like Go").*

## Release status

- **0.3 (2026-06-28)** — this round shipped: modules (`use`) + value-level immutability
  (frozen constants & module exports), printf-grade `format` specs, date/time + `sleep`,
  hashing/encoding/randomness, `drang test` (`example` assertions), array slices +
  rune-aware string indexing, and default parameters. The doc/reality drift (bucket 1)
  is closed and the binary is version-stamped. Several adversarial-review passes hardened
  each feature.
- **0.4 (2026-06-28) — RELEASED: the first *complete* version.** Published as GitHub
  release `v0.4` with four platform binaries (darwin amd64/arm64, linux amd64, windows
  amd64). What landed on top of 0.3: a **proper, expanded standard library** (~120 builtins
  + a 24-function drang prelude — the curated batteries), a robust minimal **HTTP client**,
  **`drang fmt`** (the provenance-faithful formatter), and **first-class builtins**
  (`map($xs, basename)` works — a bare builtin name is a function value).
  - **Pre-0.4 core hardening (2026-06-28):** a seven-front adversarial sweep of the
    foundation (parity, freeze-under-concurrency, value model, capture, parser/lexer,
    errors, concurrency) confirmed the architecture is sound and fixed **9 real bugs** —
    2 CRITICAL (a `pmap` shared-env data race; a `drang fmt` miscompile), 3 HIGH (cyclic
    `Equal`/`Display` stack overflow; the VM swallowing a top-level `return`; `x |> f() == y`
    always erroring), and 4 MED/LOW. All have regression tests; the suite is `-race` clean.
    See DESIGN.md → *Pre-0.4 core hardening*.
- **Next (post-0.4).** The genuinely-remaining items below: named-capture→map +
  `replace_first`, one-liner in-place `-i`, character ranges (`'a'..'z'`), the stringy-
  coercion decision, and `match`/`switch`.

## State of the language

With 0.4 shipped, drang is **a credible daily-driver**, and the *engine* is done:
register VM + tree-walker fallback, closures/lambdas/pipelines, a full HOF toolkit,
first-class functions *and builtins*, errors-as-values (`?` / `//`), first-class RE2
regexes (`qr//`), real concurrency (`spawn` / channels / `pmap`), external-command
orchestration, files/paths, JSON, CSV, an HTTP client, one-liner `-n`/`-p` mode with
`BEGIN`/`END`, `dispatch` task-running, modules (`use`), value-level immutability (frozen
constants + module exports), an expanded standard library (~120 builtins + a drang
prelude), `drang fmt`, and standalone `build`. What remains is narrower than it was:
**(1)** a few `[LOCKED]`-in-DESIGN-but-unbuilt language features (char ranges, `match`/
`switch`), and **(2)** stdlib edges a glue language occasionally reaches for. The earlier
doc/reality drift is closed.

Status tags: **NOT-STARTED** · **PARTIAL** · **DEFERRED-BY-DESIGN** (a deliberate,
recorded deferral — not a bug).

---

## (a) Language core / semantics

These four are marked `[LOCKED]` in DESIGN (design ratified) but are **not built** —
the dangerous class, because the manual implied them. The manual is now honest about
them (they're listed under "Not Yet"); building them is tracked here.

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| Default params (`fn .f($a, $b=8080)`) | option/config/task fns — **DONE**: call-time eval, may reference earlier params, works in fns + lambdas + both backends. Named args deferred indefinitely; variadic params scrapped (pass an array) | M | ✅ DONE |
| ~~Slices + string indexing/substring (`$a[1..3]`, `$s[2..5]`)~~ | core text moves — **DONE**: inclusive range slices on arrays + strings, rune-aware string indexing, negatives, clamping (read-only; slice-assignment deferred) | M | ✅ DONE |
| Char ranges `'a'..'z'` (needs char literals) | lower frequency; `'a'` lexes as ILLEGAL | M | NOT-STARTED |
| Stringy-numeric coercion (`"5" + 3`) | genuine unresolved tension (§2 locked, §14 deferred) — **decide and document** | S–M | DEFERRED-BY-DESIGN |
| Ratify provisional bits (truthiness, language name / `.dr`) | working but never formally locked; doc/decision close | S | PARTIAL |
| `match`/`switch` multi-way dispatch | value/regex dispatch for text; `dispatch({...})` partly covers it | M | NOT-STARTED |

## (b) Standard library (builtins + prelude) — the biggest real gaps

**Curation principle:** add *powers* drang can't express (→ a Go builtin) plus a thin
layer of *ergonomic shaping* over existing powers (→ a drang prelude — it dogfoods and
ships as readable, self-testing source; prototype there, promote to Go only on evidence).
Curated, not kitchen-sink: one obvious way per common task; no second mini-language, no
new value types the maps/arrays already stand in for. 🧱 = wall (blocks real work).

| Item | Why it matters | Go/drang | Status |
|------|----------------|----------|--------|
| ~~printf-grade `format` verbs (`{:spec}`)~~ | aligned columns + fixed decimals | Go | ✅ DONE |
| ~~Date/time family + `sleep`~~ | timestamps, durations, backoff (epoch-float) | Go | ✅ DONE |
| ~~Hashing + encodings~~ | `sha*`/`md5`, base64, hex, url | Go | ✅ DONE |
| ~~Random~~ | `rand`/`rand_int`/`shuffle`/`sample`/`uuid` | Go | ✅ DONE |
| ~~Conversions `str`/`float`/`bool`/`type`~~ | only `int()` existed (asymmetric) — **DONE** | Go | ✅ DONE |
| ~~Math `sqrt`/`pow`/`log`/`div`~~ | `/` is float-only — **DONE** (`%` already existed) | Go | ✅ DONE |
| ~~`index_of`~~ | "where is X" — **DONE** (rune-indexed) | Go | ✅ DONE |
| ~~`tempfile`/`tempdir` + file-append~~ | atomic-write / log-append — **DONE** (`write_file {append}`; binary already works since strings carry bytes) | Go | ✅ DONE |
| ~~`os()`/`arch()`/`home`~~ | cross-platform branching — **DONE** | Go | ✅ DONE |
| ~~UTC time option on `strftime`/`parse_time`/`date_parts`~~ | local-only is a cross-machine trap — **DONE** (`{utc: true}` flag) | Go | ✅ DONE |
| ~~`group_by`, `partition`, `uniq_by`, `enumerate`, `mean`, `median`, set ops (`intersect`/`union`/`difference`)~~ | high-value ergonomic shaping — **DONE** (prelude batch 1) | **drang prelude** | ✅ DONE |
| ~~`pad`, `capitalize`, `reverse`~~ | string conveniences — **DONE** (prelude batch 1) | **drang prelude** | ✅ DONE |
| ~~`clamp`/`sign`, `get_in`, `deep_merge`, `retry`, `dedent`~~ | ergonomic helpers — **DONE** (prelude finish-up) | **drang prelude** | ✅ DONE |
| `indent`, `title`, array `reverse` | leftover string/array conveniences | **drang prelude** | NICE |
| `replace_first`, named-capture→map `match`, `parse_url`, `hmac`/`sha512`, `walk`, `chmod` | targeted Go bindings | Go | NICE |
| ~~`http`/`http_get`/`http_post` client~~ | minimal+robust net/http binding — **DONE**: transport-fail→Err (timeout code 124), HTTP status is data; defaults: 30s timeout, ≤10 redirects, TLS on, 32 MiB cap, gzip, shared pooled transport | Go | ✅ DONE |
| ~~HTTP server / browser-GUI serving~~ | explored (serve + cell + htmx model) then **SCRAPPED by decision** — out of scope; drang is not a web framework | — | ❌ OUT OF SCOPE |
| TOML / INI config parsing | no Go-stdlib parser → conflicts with stdlib-only pillar | Go | GATED (decision-record first) |
| ~~First-class builtin values (`map($xs, basename)`)~~ | the long-standing HOF wart — **DONE**: a bare builtin name is a function value on both backends | (language) | ✅ DONE |
| `sh()` shell-escape; SQL; templating; compressed I/O; `embed()`; signals | lower-frequency batteries; build on demand | mixed | DEFERRED-BY-DESIGN |

## (c) Tooling & developer experience

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| Rebuild + release/version discipline | the local `drang.exe` had silently fallen ~9h behind HEAD; add `z build` + version stamp + a release check | S | PARTIAL (binary now current) |
| ~~`drang test`~~ | **DONE**: `example` assertions (`== `/ truthy / `fails`, a no-op in normal runs) + the runner (per-file pass/fail, non-zero exit) + **golden-output snapshots** (sibling `.golden`, captured-stdout diff, `--update` to re-bless) | M | ✅ DONE |
| ~~`drang fmt` (+ `--fix` = the edition/migration mechanism)~~ | **DONE**: faithful canonical formatter (comments preserved + drop-guard, surface-faithful via AST provenance, width-100 wrapping); CLI `-w`/`--check`/`-l`/`-d`; `--fix` ships the AST-rewrite migration hook (empty rule set today) | L | ✅ DONE |
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
| **Exhaustively test process supervision (`{supervise: true}`) on Unix** | the reaper side-car is hard-tested on Windows (15 funcs) but the Unix path (Setsid / Setpgid / `kill(-pid)`) has only been built+vetted, never *run*. Port the `cmd/drang/supervise_*_test.go` battery (only `pidAlive`/`taskkill` are Windows-specific) to Linux + macOS and run it before trusting supervision there. See the note in `internal/eval/supervise_unix.go` and DESIGN.md | M | **NOT-STARTED — blocks trusting supervise on Unix** |

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
context and the punctuation-variable zoo; **Perl's regex operators (`=~`, `s///`) and `$1..$n` capture variables** (drang keeps `qr//` + the `match`/`gsub`/`matches` builtins + pipelines; named-capture→map is the ergonomics path — `s///` reconsiderable only for one-liner mode); string `eval`; a package registry;
sandboxing; an HTTP *client* (orchestrate `run(["curl", …])`); bignum; GUI/Tk hosting;
the ops/observability and distributed/multi-host growth verticals (locked to
"personal daily-driver"). **YAML/TOML** is the one genuine judgment-call: no Go-stdlib
parser, so it needs a decision-record (hand-rolled exception vs out-of-scope).

## Recommended next 3–5

1. ~~**Ship the binary + close the doc/reality gap.**~~ ✅ Done — **0.4 released** with four
   platform binaries; the manual is version-stamped and honest about the `[LOCKED]`-but-
   unbuilt features.
2. ~~**printf-grade `format` verbs (b).**~~ ✅ Done — `{:spec}` mini-language over the existing `{}`.
3. ~~**Date/time + `sleep` (b).**~~ ✅ Done — epoch-float model + `now`/`sleep`/`strftime`/`parse_time`/`date_parts`.
4. ~~**Hashing + encodings + random (b).**~~ ✅ Done — `sha256`/`md5`, base64/hex/url, `rand`/`shuffle`/`uuid`.
5. ~~**`drang test` (c).**~~ ✅ Done — `example` assertions + the `drang test` runner.

**The recommended next-5 is complete, and 0.4 has shipped** — `drang fmt`, the expanded
stdlib, the HTTP client, and first-class builtins all landed, on top of a 9-bug core-
hardening sweep. A Perl/Python refugee can now do real text+glue work, keep it tidy, and
trust the foundation. The strongest *remaining* items (see the grouped lists above):
one-liner **`-i`** in-place edit (c), then the §(a) items — character ranges and the
stringy-coercion decision — the **named-capture→map** / `replace_first` regex ergonomics,
and **`match`/`switch`**. (Default params, slices, modules, and immutability are done;
`=~`/`s///` is deliberately out of scope.)
