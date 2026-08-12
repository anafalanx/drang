---
type: roadmap
title: drang roadmap
description: What is built, what is deliberately out of scope, and what remains — drang's status ledger.
tags: [drang, roadmap, status]
timestamp: 2026-08-12
---

# drang — Roadmap: what's left to complete

*Inventory refreshed 2026-08-12 — **drang 0.12.1 released** (compatibility-preserving runtime and tooling hardening; see CHANGELOG.md).
Grounded in DESIGN.md, MANUAL.md, a code-level scan, and a vision-gap analysis against drang's niche
(a small, parallel, Perl-inspired scripting language for text / glue / orchestration — "reads like
Ruby, thinks like Perl, runs like Go").*

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
- **0.5 (2026-06-29).** Opt-in string interpolation (`$"..."`; `''`/`""` no longer
  interpolate — a breaking change), `exe()` + `is_terminal()`, portable process supervision
  (`{supervise: true}`, Windows-validated; the Unix path was later dropped — see Direction), and a self-documenting `format`
  error for the printf habit. Plus a `gen_manual` table-renderer fix (a pipe inside a
  `code span` no longer over-splits a row).
- **0.6 (2026-07-02) — RELEASED: the Windows-native release.** Full log in CHANGELOG.md.
  Landed since 0.5: the **Windows-only** commitment (DESIGN §3.0); the **Job-Object process
  substrate** (`internal/winjob` — born-in-job launch, native die-with-parent + race-free tree-kill;
  the portable reaper is deleted); real Windows **isatty + UTF-8 console**; the **`env` → `env_exact`**
  rename and **CSV CRLF** default (breaking); a **BatBadBut / CVE-2024-24576** security fix; three
  **interpreter-correctness** fixes (recursion guard, exact int `==`/`<=>`, linear structural
  equality); and a **quality/consistency pass** on the errors-as-values model (Err-operand
  short-circuit, catchable string/fs/encoding/json type errors, source positions on `?`-propagation
  and one-liner errors, rejected-not-ignored exec options, dup-map-key rejection). All `-race` clean.
  Deferred past 0.6: the candidate stdlib powers below (`walk`, named-capture `match`, etc.) remain
  optional. **Remaining papercuts:** the `sturm`/ConPTY/resource-limit *decision record* (deferred, not
  built). *(Testing infra — the three fuzz targets and a local on-demand preflight — landed in 0.7; see
  below. Hosted CI is deliberately not adopted; the preflight is the release gate. See TESTING.md.)*
- **0.7 (2026-07-04) — RELEASED: resource limits + pre-1.0 ratification.** Full log in
  CHANGELOG.md. Landed since 0.6: kernel-enforced Job-Object caps for memory / CPU / active
  process count on every exec form; the frozen process-control surface (`status`, `stdin_file`,
  `merge_stderr`, `stdin_pipe` + `send_stdin`/`close_stdin`, up-front `cwd` validation); trig and
  small extended math; `%=` / `~=` / `//=`; defense-in-depth hardening (batch path `%`
  neutralization, polynomial `**` globs, bounded regex cache, read/capture caps, CSV injection
  sanitization); the pre-1.0 namespace vocabulary freeze plus real `fmt --fix` migration rules;
  local preflight + fuzz targets; and two hang fixes (`reverse` on arrays, impossible channel
  deadlock). Deferred past 0.7: `recv_stdout`/`stdout_pipe`, graceful `stop`/`signal`, ConPTY, and
  the `sturm` tree.
- **0.8 (2026-07-04) — RELEASED: a speed-only release.** VM int-specialization (arithmetic,
  comparison, compound-assignment, fused compare-branch) plus register-mode while-loop inversion,
  verified byte-identical to 0.7. Full log in CHANGELOG.md.
- **0.9 (2026-07-05) — RELEASED: persistent storage + cheap-win builtins.** A **persistent JSON
  key-value store** (`store`/`store_get`/`store_set`/`store_update`/`with_store`/… — atomic snapshot
  per write, advisory lock, a `.drang/<script>.store` default, no external DB); recursive **`walk`**
  plus `is_symlink`/`readlink`/`is_file` symlink introspection (and `is_symlink` on `read_dir`
  records); **`drang_pid`**; and one-liner **`-i`** in-place edit. Additive and backward-compatible.
  Full log in CHANGELOG.md.
- **0.10.0 (2026-07-05) — RELEASED: single-process orchestration completed; three-part versioning
  begins.** Two additive builtins close the last gap in the single-process surface: **`recv_stdout`**
  reads a started child's stdout live (via `{stdout_pipe: true}`), and **`recv_stderr`** reads its
  stderr as a *separate* stream (via `{stderr_pipe: true}`, mutually exclusive with `{merge_stderr}`).
  Paired with the existing `send_stdin`/`close_stdin`, a script can now drive a subprocess **both
  ways** — write its stdin, read its stdout and stderr. Graceful `stop`/`signal` stays reserved and
  deferred (decision record in DESIGN.md); ConPTY and the *sturm* tree remain future work. Additive
  and backward-compatible. Full log in CHANGELOG.md.
- **0.11.0 (2026-07-09) — RELEASED: the GUI release.** Local htmx GUIs: `serve` (127.0.0.1 +
  per-launch token, embedded htmx at `/_/htmx.js`, clamped Edge `--app` window with throwaway
  profile) and `drang build --web <dir>` asset bundling (payload v3). The docs became an OKF v0.1
  bundle, and `tools/verify.dr` grew into the full preflight (build/vet/`-race`,
  `fmt --check`, MANUAL/REFERENCE example checks, OKF lint, 3 fuzz targets). Additive and
  backward-compatible. Full log in CHANGELOG.md.
- **0.12.0 (2026-07-21) — RELEASED: module privacy + validation.** Modules are
  **private-by-default**: exports are marked with the contextual `export` keyword (`export fn .foo`,
  `export $C ::=`) — breaking for module files, with the exports-nothing warning as the migration
  guide (decision record in DESIGN.md, including the one-day `e` spelling and the rename ground:
  agent legibility). **`validate(value, shape)`** boundary shape checking — type tokens are the
  first-class conversion builtins, `"key?"` optional / `"..."` open-map keys, predicate fns +
  prelude `one_of`/`lit`; structs declined and `valid` autocheck parked (records in DESIGN.md).
  Plus **`e()`** (Euler, beside `pi()`), **`drang build --gui`** (GUI-subsystem standalones), and
  the Job-Object **Edge lifetime** fix for `serve`. Full log in CHANGELOG.md.
- **0.12.1 (2026-08-12) — RELEASED: bounded, robust execution.** A compatibility-preserving
  hardening pass put explicit ceilings around untrusted whole-value work, isolated mutable
  concurrent captures, strengthened process/module/store/copy lifecycles, hardened the CLI,
  formatter, and standalone format, and added source-positioned migration warnings for directly
  recognizable Err misuse. The full adversarial inventory and accepted residuals are recorded in
  CHANGELOG.md and MANUAL.md.
- **Remaining candidates (triaged 2026-06-29; refreshed 2026-07-05 — build on real daily-driver need,
  not speculatively).** Ordered by likely use: **named-capture `match` → map** is the most likely to
  be hit and a genuine *power*; then `parse_url`, `hmac`/`sha512`, `indent`, `title`, `chmod`. Landed:
  `replace_first`, array `reverse`, and (0.9) **`walk`**, symlink introspection
  (`is_symlink`/`readlink`/`is_file`), **`drang_pid`**, and one-liner **`-i`**. Still parked:
  char/string ranges, `match`/`switch`. **Decision (2026-06-29): stringy-numeric coercion is
  rejected** — `"5" + 3` stays a type error.
- **Direction (2026-07-01): drang is Windows-only.** Targets **Windows 11 23H2+** and **Windows
  Server 2025+** (baseline may rise to 25H2 if a technical boundary requires it, never lower).
  Non-Windows builds are dropped; future releases ship Windows binaries only. The cross-platform
  abstractions that capped the process-control features come out, replaced by native mechanisms —
  **Job Objects** for supervision / resource limits / the *sturm* tree, **ConPTY**, and the full
  Win32 process API. A Linux port, if it ever happens, is a separate clean-room effort. Full
  rationale in DESIGN.md §3.0.

## State of the language

With 0.12.1 shipped, drang is **a credible Windows-native daily-driver**, and the *engine* is done:
register VM + tree-walker fallback, closures/lambdas/pipelines, a full HOF toolkit,
first-class functions *and builtins*, errors-as-values (`?` / `//`), first-class RE2
regexes (`qr//`), real concurrency (`spawn` / channels / `pmap`), external-command
orchestration with Job-Object tree kill and resource caps, process polling and live stdin,
files/paths, JSON, CSV, an HTTP client, one-liner `-n`/`-p` mode with `BEGIN`/`END`,
`dispatch` task-running, modules (`use`), value-level immutability (frozen constants + module
exports), 161 direct builtins + 15 higher-order forms + a 25-function prelude, `drang fmt --fix`,
local preflight/fuzz verification, and standalone `build`. What remains is narrower than it was:
**(1)** a few `[LOCKED]`-in-DESIGN-but-unbuilt language features (char ranges, `match`/
`switch`), and **(2)** stdlib edges a glue language occasionally reaches for. The earlier
doc/reality drift is closed.

Status tags: **NOT-STARTED** · **PARTIAL** · **DEFERRED-BY-DESIGN** (a deliberate,
recorded deferral — not a bug).

---

## (a) Language core / semantics

This table preserves the decision history as well as the remaining work. Completed or
rejected rows stay recorded; the two still-unbuilt language items are explicitly marked
`NOT-STARTED` and are listed honestly under the manual's “Not Yet” section.

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| Default params (`fn .f($a, $b=8080)`) | option/config/task fns — **DONE**: call-time eval, may reference earlier params, works in fns + lambdas + both backends. Named args deferred indefinitely; variadic params scrapped (pass an array) | M | ✅ DONE |
| ~~Slices + string indexing/substring (`$a[1..3]`, `$s[2..5]`)~~ | core text moves — **DONE**: inclusive range slices on arrays + strings, rune-aware string indexing, negatives, clamping (read-only; slice-assignment deferred) | M | ✅ DONE |
| Char ranges `'a'..'z'` (needs char literals) | lower frequency; `'a'` lexes as ILLEGAL | M | NOT-STARTED |
| Stringy-numeric coercion (`"5" + 3`) | **DECIDED 2026-06-29: rejected.** drang will not coerce; `"5" + 3` stays a type error (convert with `int()`/`str()`). Keeps the "explicit over implicit, no surprises" stance | — | ✅ DECIDED |
| ~~Ratify provisional bits (truthiness, language name / `.dr`)~~ | **DONE in 0.7**: contract surfaces settled as part of the pre-1.0 ratification pass | S | ✅ DONE |
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
| ~~Math `sqrt`/`pow`/`log`/`div` + trig~~ | `/` is float-only — **DONE** (`%` already existed; 0.7 added trig, `exp`, `pi`, and optional-base `log`) | Go | ✅ DONE |
| ~~`index_of` / `find_index`~~ | "where is X" — **DONE** (rune-indexed; renamed to `find_index` in 0.7) | Go | ✅ DONE |
| ~~`tempfile`/`tempdir` + file-append~~ | atomic-write / log-append — **DONE** (`write_file {append}`; binary already works since strings carry bytes) | Go | ✅ DONE |
| ~~`os()`/`arch()`/`home`~~ | cross-platform branching — **DONE** | Go | ✅ DONE |
| ~~UTC time option on `strftime`/`parse_time`/`date_parts`~~ | local-only is a cross-machine trap — **DONE** (`{utc: true}` flag) | Go | ✅ DONE |
| ~~`group_by`, `partition`, `uniq_by`, `enumerate`, `mean`, `median`, set ops (`intersect`/`union`/`difference`)~~ | high-value ergonomic shaping — **DONE** (prelude batch 1) | **drang prelude** | ✅ DONE |
| ~~`pad`, `capitalize`, `reverse`~~ | string conveniences — **DONE** (prelude batch 1) | **drang prelude** | ✅ DONE |
| ~~`clamp`/`sign`, `get_in`, `deep_merge`, `retry`, `dedent`~~ | ergonomic helpers — **DONE** (prelude finish-up) | **drang prelude** | ✅ DONE |
| `indent`, `title` | leftover string conveniences | **drang prelude** | CANDIDATE |
| ~~array `reverse`~~ | array convenience — **DONE in 0.7**; `reverse` now handles strings and arrays | Go | ✅ DONE |
| ~~`replace_first`~~, named-capture→map `match`, `parse_url`, `hmac`/`sha512`, `walk`, `chmod` | targeted Go bindings (`walk` + named-capture `match` are the likeliest hits). `replace_first` — **DONE** (landed with the pre-1.0 naming pass as `replace_first`/`replace_all`, literal-or-regex needle) | Go | MIXED |
| ~~`http`/`http_get`/`http_post` client~~ | minimal+robust net/http binding — **DONE**: transport-fail→Err (timeout code 124), HTTP status is data; defaults: 30s timeout, ≤10 redirects, TLS on, 32 MiB cap, gzip, shared pooled transport | Go | ✅ DONE |
| ~~Local htmx GUI serving (`serve`)~~ | **DONE in 0.11.0** — a *local, single-user* htmx server for tool cockpits: 127.0.0.1 + per-launch token, embedded htmx, clamped Edge `--app` window, `drang build --web` asset bundling. Deliberately narrow — a production web server / framework stays out of scope | Go | ✅ DONE |
| TOML / INI config parsing | no Go-stdlib parser → would need a third-party library (against the dependency-light pillar) | Go | GATED (decision-record first) |
| ~~First-class builtin values (`map($xs, basename)`)~~ | the long-standing HOF wart — **DONE**: a bare builtin name is a function value on both backends | (language) | ✅ DONE |
| ~~`validate` boundary shape checking~~ | **DONE (2026-07-21)** — value-or-Err shape check with path'd mismatch reports; type tokens are the first-class conversion builtins; `"key?"` optional / `"..."` open-map keys; predicate fns + prelude `one_of`/`lit` combinators. Structs/`valid` autocheck considered and declined/parked — decision record in DESIGN.md | Go + prelude | ✅ DONE |
| `sh()` shell-escape; SQL; templating; compressed I/O; `embed()`; signals | lower-frequency batteries; build on demand | mixed | DEFERRED-BY-DESIGN |

## (c) Tooling & developer experience

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| ~~Rebuild + release/version discipline~~ | **DONE in 0.7**: version-stamped binary, signed Windows release asset, and the local preflight is the release gate | S | ✅ DONE |
| ~~`drang test`~~ | **DONE**: `example` assertions (`== `/ truthy / `fails`, a no-op in normal runs) + the runner (per-file pass/fail, non-zero exit) + **golden-output snapshots** (sibling `.golden`, captured-stdout diff, `--update` to re-bless) | M | ✅ DONE |
| ~~`drang fmt` (+ `--fix` = the edition/migration mechanism)~~ | **DONE**: faithful canonical formatter (comments preserved + drop-guard, surface-faithful via AST provenance, width-100 wrapping); CLI `-w`/`--check`/`-l`/`-d`; `--fix` now ships real 0.7 migration rules for the vocabulary freeze | L | ✅ DONE |
| ~~`-i` in-place edit for one-liner mode~~ | **DONE in 0.9** — `perl -i -pe`-style editing with an optional backup suffix | S–M | ✅ DONE |
| REPL polish / editor support / LSP | real adoption infra, but one-user project — low priority | L | NOT-STARTED |

## (d) Runtime & quality

| Item | Why it matters | Size | Status |
|------|----------------|------|--------|
| ~~Source positions in one-liner / top-level propagation errors~~ | **DONE in 0.6**: `-n`/`-p` runtime errors and top-level `?` propagation carry source positions | M | ✅ DONE |
| VM↔walker: bare `use "path"` statement not compiled | `compiler.go` `default: c.fail()` → whole program falls back to the tree-walker when a bare `use` is present (captured `use(...)` compiles fine). No `OpUseMerge` | M | PARTIAL |
| VM↔walker: bare ident-as-value not compiled | forces walker fallback; minor | S | PARTIAL |
| Startup benchmark + prelude precompile (go:generate bytecode) | reserved optimization; gate behind a real benchmark first | S / M | DEFERRED-BY-DESIGN |
| `--profile` pprof output | called a freebie in §11; `drang_gc` exists, no flag | S | NOT-STARTED |
| ~~Parser / formatter / backend fuzz coverage~~ | **DONE in 0.7**: `FuzzParse`, `FuzzFmtRoundTrip`, and `FuzzBackendParity` run as seeds under ordinary tests and as bounded fuzz targets in the preflight | M | ✅ DONE |
| ~~Exhaustively test process supervision (`{supervise: true}`) on Unix~~ | **DROPPED (2026-07-01)** — superseded by the Windows-only decision (DESIGN §3.0). The Unix reaper (`supervise_unix.go` / `reap_unix.go`) is retired, not validated. On Windows-only, supervision now runs on **Job Objects** (`internal/winjob`: born-in-job `KILL_ON_JOB_CLOSE` → die-with-parent + race-free whole-tree kill) — the reaper side-car is deleted. | — | DROPPED |
| ~~`is_terminal()` / the REPL's `interactive()` use a coarse `os.ModeCharDevice` check~~ | **FIXED (2026-07-01):** replaced with a real Windows isatty — `GetConsoleMode` plus the MSYS2/Cygwin pty-name heuristic (`internal/eval/terminal.go`, shared by `is_terminal()` and the REPL), so mintty / Git Bash now start the REPL and `is_terminal()` reports correctly there. Also added `SetConsoleOutputCP(CP_UTF8)` at startup so non-ASCII output isn't mojibake on a stock console. | S–M | ✅ DONE |

## (e) Polish / recent-feature follow-ups

| Item | Status |
|------|--------|
| ~~Module privacy (every top-level `.foo` is exported)~~ | ✅ DONE — modules are private-by-default; exports are marked with the contextual `export` keyword (`export fn .foo`, `export $C ::=`). Breaking for module files; decision record in DESIGN.md |
| ~~Duplicate `fn .foo` in one file = silent last-wins~~ | ✅ DONE — 0.7 warns on stderr while preserving last-definition-wins |
| Extensionless path precedence (bare file shadows `<name>.dr`) | DEFERRED-BY-DESIGN |
| Bare `use("x")` with parens as a discarded statement = silent no-op | DEFERRED-BY-DESIGN |
| `pmap`/`spawn` mutable-`:=`-global caveat (only `::=` consts are frozen/pmap-safe; a static resolver to reject the rest is reserved) | DEFERRED-BY-DESIGN |
| CSV follow-ups (streaming row reader, TSV, quoting styles) | DEFERRED |

---

## Already complete — don't reopen

Value-level immutability/freeze; modules (`use`); `qr//` regex literals;
`q{}`/`qq{}`/heredocs; break/next; lambdas; integer-overflow→error; implicit
return; two-var `for $k,$v in`; `//` defined-or; `<=>`; all Phase-1 path/fs/env wins;
the prelude (`flatten`/`sum_by`/`count_by`/`chunk`/`zip` — `tally` was folded into
`count_by` identity in the pre-1.0 naming pass); JSON; CSV;
one-liner `-n`/`-p` + `BEGIN`/`END`; concurrency (`spawn`/`chan`/`pmap`, input-ordered).
Must-use enforcement was deliberately dropped (`[LOCKED]`), not missing.

## Deliberately out of scope — not missing work

Ternary `?:`, `**`, bitwise, `++`/`--`; classes/inheritance/`bless`/MOP; scalar-list
context and the punctuation-variable zoo; **Perl's regex operators (`=~`, `s///`) and `$1..$n` capture variables** (drang keeps `qr//` + the `match`/`replace_all`/`matches` builtins + pipelines; named-capture→map is the ergonomics path — `s///` reconsiderable only for one-liner mode); string `eval`; a package registry;
sandboxing; a *production* HTTP server / web framework (the local `serve` tool-GUI is in scope; a general web server is not); bignum; GUI/Tk hosting;
the ops/observability and distributed/multi-host growth verticals (locked to
"personal daily-driver"). **YAML/TOML** is the one genuine judgment-call: no Go-stdlib
parser, so it needs a decision-record (hand-rolled exception vs out-of-scope).

**Console-UI / TUI facility** (ANSI color, tables, spinners, prompts): studied 2026-06-30 and
declined. The attractive parts (16-color styling, tables, cooked prompts) are cheap, but the
value-add (color under Git Bash/mintty, hidden password input) forces ownership of an
undocumented MSYS pipe-name heuristic and per-arch termios that cannot be tested from a Windows
dev box, and the facility would be invisibly degraded in the maintainer's own shell. A
dependency-light, Windows-first language should not take that on; do not re-propose without new
information. (The `is_terminal` brittleness this surfaced is now FIXED — see §(d).)

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
