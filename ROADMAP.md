# drang — Roadmap: what's left to complete

*Inventory dated 2026-06-27, at git `08213ea`. Grounded in DESIGN.md, MANUAL.md, a
code-level scan, and a vision-gap analysis against drang's niche (a small, parallel,
Perl-inspired scripting language for text / glue / orchestration — "reads like Ruby,
thinks like Perl, runs like Go").*

## Release status

- **0.3 (2026-06-28)** — this round shipped: modules (`use`) + value-level immutability
  (frozen constants & module exports), printf-grade `format` specs, date/time + `sleep`,
  hashing/encoding/randomness, `drang test` (`example` assertions), array slices +
  rune-aware string indexing, and default parameters. The doc/reality drift (bucket 1)
  is closed and the binary is version-stamped. Several adversarial-review passes hardened
  each feature.
- **0.4 — the target: first *complete* version.** The remaining items below (named-
  capture→map + `replace_first`, `drang fmt`, one-liner `-i`, char ranges, the stringy-
  coercion decision) plus a **proper, expanded standard library** (the curated
  batteries — grow the prelude / builtins toward the daily-driver bar).

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

1. **Ship the binary + close the doc/reality gap.** Rebuild from HEAD (done); make
   the manual honest about the `[LOCKED]`-but-unbuilt features (done); add a
   version stamp / release check. *A daily-driver whose own examples don't run fails
   "complete" on trust before any feature.*
2. ~~**printf-grade `format` verbs (b).**~~ ✅ Done — `{:spec}` mini-language over the existing `{}`.
3. ~~**Date/time + `sleep` (b).**~~ ✅ Done — epoch-float model + `now`/`sleep`/`strftime`/`parse_time`/`date_parts`.
4. ~~**Hashing + encodings + random (b).**~~ ✅ Done — `sha256`/`md5`, base64/hex/url, `rand`/`shuffle`/`uuid`.
5. ~~**`drang test` (c).**~~ ✅ Done — `example` assertions + the `drang test` runner.

**The recommended next-5 is complete, and `drang fmt` has since shipped** (a faithful,
comment-preserving canonical formatter with width-100 wrapping and the `--fix` migration
hook). A Perl/Python refugee can now do real text+glue work, and keep it tidy, without
hitting a wall. The strongest remaining items (see the grouped lists above): one-liner
**`-i`** in-place edit (c), then the remaining §(a) items — char ranges and the
stringy-coercion decision — and the **named-capture→map** regex-ergonomics builtin.
(Default params and slices are done; `=~`/`s///` is deliberately out of scope.)
