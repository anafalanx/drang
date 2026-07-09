---
type: changelog
title: drang changelog
description: The release history of drang, newest first (Keep a Changelog style).
tags: [drang, changelog, releases]
timestamp: 2026-07-09
---

# Changelog

All notable changes to drang are recorded here. Dates are the release dates; the format loosely
follows [Keep a Changelog](https://keepachangelog.com/). Versions are git tags `vX.Y.Z` (`vX.Y` through 0.9).

## [0.11.0] — 2026-07-09

The GUI release. drang can now build **local htmx GUIs** — single-user tool
"cockpits" served to a clamped system-browser window — with no external runtime,
no bundled Chromium, and no web framework. A design line previously scrapped as out
of scope is brought back **deliberately but narrowly**: `serve` is a *local* server
for building tools, not a production web server or framework. Additive and
backward-compatible; still Windows-only.

### Added
- **`serve` — a local htmx GUI server.** `serve({routes, static?, port?, open?})`
  binds `127.0.0.1` on an ephemeral port, routes request paths to drang handler
  functions that return HTML (a full page or an htmx fragment), and gates every
  request on a per-launch random token so no other local process can drive it.
  Handlers get a `{method, path, query, form, headers}` request map and return a
  string (sent as text/html), a `{status?, headers?, body}` map, or nil. VM calls
  are serialized (one handler at a time — right for a single browser); a handler
  panic becomes a 500, never a crashed server.
- **Embedded htmx.** The htmx runtime (2.0.4) is baked into the binary and served
  at `/_/htmx.js` — reference it with `<script src="/_/htmx.js"></script>`. Nothing
  to install, fully offline.
- **Clamped system-browser launch.** By default `serve` opens the page in Microsoft
  Edge `--app` mode (a chrome-less window) against a throwaway isolated profile,
  with background-networking/sync/extensions off. Closing the window shuts the
  server down and wipes the profile. Falls back to the default browser (unclamped)
  if Edge is absent; `open: false` skips the launch for headless/test use.
- **`drang build --web <dir>`.** Bundles a `web/` asset tree into the standalone exe
  (payload format v3), served from memory by `serve`. A GUI tool — drang + htmx +
  your HTML/CSS/JS — is now a single self-contained file. `static: "web"` serves
  from disk in dev and from the embedded copy when built (identical program).

## [0.10.0] — 2026-07-05

The release that completes single-process orchestration — and the first with a
three-part version number (tags are `vX.Y.Z` from here on). Additive and
backward-compatible: every 0.9 program runs unchanged. Two new builtins let a
script read a started child's output live, so it can now drive a subprocess in
**both** directions — write its stdin, read its stdout and stderr — the one
capability a started child was still missing. Still Windows-only.

### Added
- **Live child stdout reading (`recv_stdout`).** Completes single-process bidirectional steering.
  `start(cmd, …, {stdout_pipe: true})` keeps a started child's stdout on a pipe, and `recv_stdout(p)`
  reads the next chunk (raw bytes, untrimmed) — blocking until the child writes, returning nil once
  it closes its stdout. Paired with the existing `send_stdin`/`close_stdin`, a script can now drive a
  live child **both ways** — write a prompt, read the reply, repeat — which was impossible before (a
  started child's stdout was detached to the null device). The read is direct (the script is the
  drainer), so drain to nil before `await`; combine with `{merge_stderr}` to fold stderr in. Note
  many programs block-buffer stdout when it is a pipe, so their output appears only on flush/exit
  (a pty/ConPTY is not provided). Additive and backward-compatible; VM↔walker parity + `-race` tested.
  (Graceful `stop`/`signal` remains reserved and deferred — see DESIGN.md.)
- **Live child stderr reading (`recv_stderr`).** Fills the last gap in the single-process surface:
  `start(cmd, …, {stderr_pipe: true})` keeps a started child's stderr on its **own** pipe, and
  `recv_stderr(p)` reads it as a stream distinct from stdout (same raw-chunks / nil-at-EOF / direct-read
  contract as `recv_stdout`). It is the counterpart to `{merge_stderr}` (which folds stderr into
  stdout); the two are **mutually exclusive** — asking for both is rejected rather than silently
  merging. To read both streams from one child, drain them **concurrently** (read one in a `spawn`ed
  task), since they are independent pipes and draining only one can back-pressure the child. This is
  a genuinely new name (never reserved in the 0.7 freeze), added under the additive-only contract.
  VM↔walker parity + `-race` tested (separate-streams and cross-reader concurrency covered).

## [0.9] — 2026-07-05

A capabilities release — additive and backward-compatible, so every 0.8 program runs unchanged
(byte-for-byte; all the new names are new builtins). The headline is a **persistent JSON
key-value store** for scripts that need to remember something between runs; around it, recursive
directory `walk` with symlink introspection, the interpreter's own `drang_pid`, and one-liner
in-place editing (`-i`). Still Windows-only.

### Added
- **Persistent JSON store.** A `store` is a durable key-value map backed by a single JSON file,
  for scripts that need to remember something between runs — a cursor, a checkpoint, an
  accumulator, a cache. `store(path?)` opens one; `store_get` / `store_set` / `store_has` /
  `store_delete` / `store_keys` / `store_all` / `store_clear` read and mutate it;
  `store_update(s, key, default, fn)` is an atomic read-modify-write (correct counters even when
  runs race); `with_store(s, fn)` commits a group of writes all-or-nothing, rolling back on error;
  `store_path` / `store_close` round it out. Values are any JSON-serializable drang value (a
  channel/task/process/function is a catchable Err); `int` stays 64-bit exact.
  - **Durability:** atomic snapshot per write (temp + fsync + rename; the previous copy kept as
    `.bak`), so the file is never observed torn.
  - **One writer:** a process-exclusive advisory lock (a `.lock` sidecar) — a second process
    opening the same store gets a catchable `store busy` Err; the data file stays readable by
    other tools.
  - **Location:** `store()` defaults to `.drang/<script>.store` next to the script — a
    predictable, environment-variable-free path that travels with the script, never derived from
    `%LOCALAPPDATA%` / `%APPDATA%` / OneDrive. The runtime keeps no state of its own. `-e`/stdin
    pass an explicit path.
  - The handle is a shared, mutex-guarded reference like a channel — safe to hand to `spawn`/`pmap`
    (access serialized, not a parallelism win). Compiles identically on the register VM and the
    tree-walker; hand-written parity + `-race` tests.
  - Not a database, by design: no queries/indexes/joins — the smallest primitive that covers
    "remember something between runs." An embedded SQLite was evaluated and rejected to keep the
    single static binary with zero dependencies.
- **Recursive directory walk + symlink introspection.** `walk(dir)` recursively lists a tree as
  `{name, path, is_dir, is_symlink, size, mtime}` records (the root excluded; symlinks reported
  but never followed, so no cycles). `is_symlink(p)` and `readlink(p)` inspect links; `is_file(p)`
  rounds out the stat guards alongside `exists`/`is_dir`; and `read_dir` records now also carry
  `is_symlink`.
- **`drang_pid()`** — the running interpreter's own process id, distinct from `pid(proc)` (a
  spawned child's id).
- **One-liner `-i` in-place edit.** `drang -pi -e '...' file` writes each file's `-p` output back
  to the file atomically; `-i.bak` keeps a backup of the original first. Requires `-p` and one or
  more real input files (it will not edit stdin or run without files). Clusters as `-pi` / `-pi.bak`.

## [0.8] — 2026-07-04

A speed release: *change nothing except the speed.* Every program produces byte-identical output,
errors, and exit codes to 0.7 — verified program-for-program against the 0.7 binary across the
benchmark corpus, the examples, and a targeted differential over every changed path. The single
observable difference is that `drang_gc()` reports the relaxed startup baseline (see below), an
honest reflection of the new GC policy. Still Windows-only.

### Faster
- **Relaxed GC on one-shot runs.** A `drang script.dr` / `-e` / `-n`/`-p` run now relaxes the
  garbage collector (a higher GOGC, so it collects less often) and installs a soft memory limit
  sized from available RAM as an OOM backstop. Short-lived scripts do fewer collections — ~9% faster
  geomean across the benchmark suite. The REPL keeps the default GC. The policy is skipped entirely
  if `GOGC` or `GOMEMLIMIT` is set in the environment. Turning the GC fully off was measured *slower*
  on garbage-heavy work (the working set falls out of CPU cache), so it collects less, not never.
  One program-visible consequence: `drang_gc()` reports the relaxed baseline instead of Go's default
  on a one-shot run, so the documented `$old := drang_gc(...); ...; drang_gc($old)` idiom still works.
- **Int-specialized VM arithmetic and comparison.** The register VM's binary arithmetic (`+ - * %`
  and their constant-operand forms) and ordered comparisons take an inline integer fast path,
  falling through to the shared implementation for non-int operands, overflow, modulo-by-zero, and
  `/` (always float). Integer comparisons stay exact above 2^53. ~22% faster on a recursive integer
  benchmark's inner loop.
- **Int-specialized compound-assignment and loop conditions.** `$x += 1`, `$total += $v * $k`, the
  index/slot forms `$a[i] += n` / `$m[k] += n`, and the fused `if`/`while` comparison branches take
  the same integer fast path. ~23% faster on a map-counting glue loop and ~33% on a builtin-in-a-loop;
  behaviour (overflow/modulo-zero aborts, `//=`/`~=`/`/=`, nil-slot seeding, frozen-target errors) is
  unchanged.
- **Bottom-testing `while` loops.** Register-mode `while` loops were inverted so the fused condition
  test *is* the loop back-edge, removing the unconditional per-iteration jump. Up to ~15% faster on a
  pure spin loop (less as the body grows). Zero-iteration, `break`, `next`, nested loops,
  side-effecting conditions, and while-as-nil-value all behave exactly as before.

Net: pure numeric loops now run about as fast as CPython 3.14 (previously several times slower),
while staying byte-for-byte compatible with 0.7.

### Fixed
- **Indexing an error with an erroring index.** `errBase[0%0]` — an out-of-range error indexed by a
  modulo-by-zero — aborted on the compiled backend but flowed the base error on the tree-walker
  (found by the backend-parity fuzzer). The two backends now agree: the index is always evaluated
  (matching the compiled backend and drang's binary operators, which evaluate both operands then
  short-circuit an error at the value level), so a hard error in the index aborts on both; an error
  base with a valid index still flows the base error.

## [0.7] — 2026-07-04

The single-process-control release: kernel-enforced resource limits and the process-control gaps
filled, plus a first math/ergonomics batch, a pre-1.0 vocabulary freeze, a defense-in-depth
security pass, and two robustness fixes. Still Windows-only.

### Security — defense-in-depth hardening
A follow-up security review (no high-severity findings) closed a set of hardening gaps:
- **Batch script-path `%`-neutralization.** Launching a `.bat`/`.cmd` already neutralized `%` in the
  ARGUMENTS (so cmd cannot expand a `%VAR%` out of them); the same is now applied to the script
  PATH, and a CR/LF in the path is rejected as it already is in arguments. An unneutralized `%VAR%`
  in the path could otherwise redirect which file runs, or — if the variable's value carried a
  quote — break out of the command-line quoting.
- **Glob `**` can no longer hang.** The `**` matcher backtracked exponentially on a pattern with
  several non-adjacent `**` (`a/**/b/**/c/**/d` against a deep tree). It now collapses adjacent `**`
  and memoizes on position, so matching is polynomial regardless of `**` count.
- **Bounded the compiled-regex cache** (~4096 patterns): a program compiling an unbounded stream of
  DISTINCT dynamic patterns can no longer pin one compiled regex per pattern forever. Past the cap,
  patterns still compile correctly — just uncached.
- **`read_file` size cap (1 GiB).** read_file loads a whole file into one string; an unbounded
  source (a huge file, or a pipe/device that never reaches EOF) is now a catchable Err, not an OOM.
- **`capture` / `capture_all` / `pipe` output cap (256 MiB).** A child that streams without end can
  no longer grow the capture buffer until the process OOMs; the overflow is a catchable Err (code
  **137** for `capture_all`).
- **`to_csv {sanitize}` — opt-in CSV-injection defense.** A cell beginning with `=`, `+`, `-`, `@`,
  or a leading tab/CR/LF (the OWASP dangerous-lead set) can run as a formula when the file is opened
  in Excel / Sheets; `{sanitize: true}` prefixes a `'` so such cells stay literal text. Opt-in,
  because it changes the data (`-5` → `'-5`).

### Added — process control & resource limits
- **Kernel-enforced resource caps** on every exec form (`run`/`capture`/`capture_all`/`stream_lines`/
  `pipe`/`start`), via Windows Job Objects: `{max_memory}` / `{max_job_memory}` (per-process /
  whole-job commit, bytes), `{max_cpu}` / `{max_job_cpu}` (per-process / whole-job user CPU,
  milliseconds), and `{max_job_procs}` (active-process cap). A breach terminates the child with exit
  **137**, and the job's IOCP monitor names which cap (memory / CPU-time) tripped.
- **Process-control builtins & options:** `status(proc)` polls a child without blocking, always
  returning the uniform shape `{running, ok, code, pid}` (while alive: `running` is true, `code` is
  the `-1` sentinel, `ok` false); a `kill()`'d process reports `"was killed"` (137), distinct from a real
  exit code; `{stdin_file: path}` feeds stdin from a file; `{merge_stderr: true}` folds stderr into
  stdout (`2>&1`); `{cwd}` is validated up front (a bad directory is a clean catchable Err); and
  `start(..., {stdin_pipe: true})` with `send_stdin(proc, s)` / `close_stdin(proc)` drives a live
  child's stdin. Option hygiene: `cwd`/`stdin`/`arg0` require a string (a non-string is a clean
  error, not a silent stringification), and `supervise` is rejected on the waiting forms (it is
  start-only). Reserved but **not in 0.7**: `recv_stdout`/`{stdout_pipe}` and `stop`/`signal`.

### Added — language
- **Trigonometry & a small extended-math line** (radians), the capability area the manual had
  promised: `sin cos tan asin acos atan atan2 exp`, plus `log(x, base?)` and the constant `pi()`.
  Thin bindings over Go's `math`; a domain or type error is a catchable Err, never a silent NaN.
- **Compound assignment extended** to `%=`, `~=`, and `//=`. `~=` appends/concatenates (seeding a
  fresh slot with `""`); `//=` is defined-or in place — it takes the right-hand side only when the
  slot is nil or an error, and keeps a present value (even a falsy `0`).

### Fixed
- **Branching runaway recursion no longer hangs the interpreter.** The recursion guard bounds
  each call PATH (depth 4000), but a base-case-less branching recursion like
  `fn .f($n) { .f($n - 1) * .f($n - 2) }` explores ~2^4000 sibling paths — each path terminated,
  the tree never did. A storm of depth-guard hits (>100,000 in one run) now escalates to a loud
  aborting error ("runaway recursion") within milliseconds, on both backends. A single overflow
  (or thousands) is still the same catchable Err as before — `deep() // fallback` keeps working —
  and the normal call path pays nothing (the counter moves only when the guard fires). This also
  de-flakes `FuzzBackendParity` and the `z verify` release gate, which previously hung when the
  fuzzer mutated a recursion seed into the branching shape. Separately, the parity harness now
  bounds each fuzz execution with a test-only call budget and restricts fuzzed ranges to small
  literals — a finite-but-astronomically-slow program (exponential recursion WITH a base case,
  a two-billion-iteration loop) is skipped as unverifiable rather than stalling the gate; such
  programs remain perfectly legal in production, where slow is not a bug.
- **`reverse` no longer infinite-loops on an array.** It now reverses arrays as well as strings, and a
  non-string / non-array argument is a catchable Err instead of a hang (`reverse([1,2,3])` → `[3,2,1]`).
- **A channel `send`/`recv` that could only ever deadlock** — no counterparty and no other task
  running — is now a catchable Err rather than a raw Go `all goroutines are asleep` process abort. A
  genuine multi-goroutine deadlock still fails loudly.

### Changed
- **Namespace:** the (brand-new, undocumented) `max_procs` exec option is renamed `max_job_procs` — it
  is job-scoped, and the bare `max_` prefix wrongly implied per-process. The builtin-naming rule is
  amended in DESIGN.md to "bare when unambiguous, `domain_` prefix only on collision."
- **`exit(negative)` now exits with status `1`**, not `0` — a negative status is a failure, so the
  explicit-exit path now matches the Err-dispatch path (both map a negative code to 1). `exit(0)`
  remains a deliberate success.
- **A duplicate top-level `fn .foo` in one file now warns** (on stderr) instead of silently keeping
  only the last definition. Last-definition-wins is unchanged — it is just no longer silent.

### Changed — pre-1.0 namespace coherence pass (breaking; `drang fmt --fix` migrates)
A whole-namespace naming audit (six domain reviews + a law synthesis, recorded in DESIGN.md)
pulled the last inconsistent names into line before 1.0 freezes them. Every rename ships with a
`drang fmt --fix` rewrite rule — the first real rules in the edition mechanism — so migration is
one mechanical command:
- **`gsub` → `replace_all`, `replace` → `replace_all`, new `replace_first`.** One discoverable
  pair replaces the literal/regex verb fork: the needle's TYPE picks the mode (a plain string is
  a **literal**; a `qr//` / `re(...)` regex matches as a pattern, with `$1`/`${name}` backrefs) —
  the Ruby `gsub` convention. The bare-form polarity now matches `match`/`match_all` (bare = first,
  `_all` = every); old `gsub` string patterns are auto-wrapped in `re(...)` by `--fix` to keep
  their regex semantics.
- **`each_line` → `stream_lines`** — the lone verb_noun compound in the bare process block, and
  its `each` head falsely rhymed with the `each` collection HOF. Reads as the "stream vs buffer"
  counterpart of `capture`.
- **`strftime` → `format_time`** — the spelled-out inverse of `parse_time` (the `%`-codes are
  unchanged).
- **`url_encode`/`url_decode` → `to_url`/`from_url`** — joins the `to_hex`/`from_hex`,
  `to_base64`/`from_base64` codec family (LOCKED `from_X`/`to_X` law).
- **`slash` → `to_slash`** — names the conversion, not the character; same codec family.
- **`index_of` → `find_index`** — "first occurrence" joins the `find_` stem (`find`/`find_index`);
  drops the lone `of`-connector.
- **`abspath` → `abs_path`** — composed underscore form, matching its sibling `is_abs` and Perl's
  `Cwd::abs_path` (Python's glued spelling was never on the blessed-abbreviation list).
- **`sys_gc` → `drang_gc`** — `sys_` misread as the operating system; `drang_` marks knobs and
  introspection on the drang interpreter itself (reserved siblings: `drang_version`, `drang_mem`),
  unambiguous in orchestration scripts that steer OTHER runtimes.
- **`tally` removed** — it was exactly `count_by` with the identity key; `--fix` rewrites
  `tally($xs)` → `count_by($xs, |$e| $e)`.
- **`find_all` → `match_all`** — the exhaustive regex matcher joins the `match`/`matches` family
  under one shared stem (the array-HOF `find` is unrelated and stays as is).
- **`within` → `is_within`** — the `is_` prefix marks it a bool guard, like `is_abs`/`is_dir`, and
  frees the bare word `within` for a planned `within(5s){}` deadline construct.
- **`join` split into array-only `join` + new `path_join`** — one builtin quietly meant two things
  (render-and-join an array, *or* assemble path segments), told apart only by the first argument's
  runtime type. Each name now has one job: `join(array, sep?)` renders and joins; `path_join(seg, …)`
  builds an OS-native path.
- Deliberately **kept** (documented exceptions): `uniq`/`uniq_by`, `rm`/`mkdir`/`cwd` (blessed
  Unix muscle memory), `dirname`/`basename` (entrenched single words), `start` vs `spawn` (a real
  Proc-vs-Task distinction), and polymorphic `await`.
- **Not auto-migrated** (`--fix` leaves these to fail loudly rather than silently change
  behavior): a FIRST-CLASS `gsub` reference (`$f := gsub`, `map($fs, gsub)`) — rewrite by hand to
  `|$s, $p, $r| replace_all($s, re($p), $r)`; a FIRST-CLASS `tally` reference — rewrite to
  `|$xs| count_by($xs, |$e| $e)`; any gsub/tally call whose arity was already an error in 0.7; and
  a path-join written as `join(...)` — the split's meaning is a runtime-type decision `--fix` cannot
  make, so such a call now fails loudly at `join` with a note pointing to `path_join`.
  Rewrites inside interpolating strings (`$"...${...}"`, `$qq{...}`) ARE migrated (the
  interpolation is re-rendered canonically when its parts change).

### Testing & tooling
- **Local preflight** ([tools/verify.dr](tools/verify.dr), `z verify`): one on-demand command —
  `go build` + `go vet` + the full `go test -race ./...` suite + a bounded fuzz burst on each
  target — that is the release gate. drang verifying drang. There is deliberately **no hosted CI
  and no scheduled fuzzing**; the preflight is run locally before a release and after major work.
- **Three fuzz targets**, seeded so they also run as ordinary regression tests: `FuzzParse` (the
  parser never panics/hangs on any input), `FuzzFmtRoundTrip` (`drang fmt` stays a fixed point), and
  `FuzzBackendParity` (the register VM and the tree-walking oracle agree on every pure, deterministic,
  terminating program). See [TESTING.md](TESTING.md).

## [0.6] — 2026-07-02

The Windows-native release: drang commits to modern Windows and rebuilds its process layer on Job
Objects, closes a security hole in that layer, and hardens the interpreter and the errors-as-values
model. drang is now **Windows-only** (Windows 11 23H2+ / Windows Server 2025+).

### Security
- **BatBadBut / CVE-2024-24576 — batch-argument injection (CRITICAL).** Running a `.bat`/`.cmd`
  through `run`/`capture`/`pipe`/`each_line`/`start` no longer lets an argument break out of quoting
  and execute an injected command. Batch targets launch through a defensively quoted
  `cmd.exe /e:ON /v:OFF /d /c "…"` (ported from Rust's `std::process`), with cmd.exe resolved from
  drang's own environment, never a child's `ComSpec`.

### Platform
- **Windows-only.** Non-Windows builds are dropped; releases ship a Windows binary only. The
  cross-platform abstractions that capped process control are gone (DESIGN §3.0).
- **Process substrate rebuilt on Job Objects** (`internal/winjob`): every child is launched
  *born-in-job* (`KILL_ON_JOB_CLOSE`) for native die-with-parent and race-free whole-tree kill; the
  old portable reaper side-car is deleted. An IOCP job-event monitor is in place as the substrate
  for future supervision.
- **Real Windows isatty + UTF-8 console:** `is_terminal()` and the REPL detect mintty/Git-Bash ptys
  correctly, and non-ASCII output is no longer mojibake (`SetConsoleOutputCP(CP_UTF8)`).

### Fixed (interpreter correctness)
- **Runaway recursion no longer crashes.** Unbounded user recursion overflowed Go's stack — a fatal,
  unrecoverable abort. It now returns a catchable Err past a depth bound (4000), with no data race
  under `pmap`/`spawn` and no allocation added to the hot register path.
- **`int == int` and `<=>` are exact.** They compared via `float64`, so values above 2⁵³ collapsed
  (`9007199254740993 == …992` was `true`). Two ints now compare as `int64`.
- **Structural equality is linear**, not exponential, on values with shared substructure (a
  visited-pair memo that also breaks reference cycles).
- Three concurrency/semantics defects from the Job-Object migration (shared-writer race under
  concurrent `pmap`, a swallowed non-last-stage pipe timeout, a handle-close/terminate race).

### Changed (errors-as-values, made consistent)
- An unhandled `Err` flowing into arithmetic, ordering, `<=>`, unary minus, or `len` now returns the
  Err in place (message preserved, recoverable by `//`) instead of aborting; `for`-in over an Err
  propagates it. `==`/`!=`/`!` stay total.
- **Wrong-TYPE arguments to string/fs/encoding/json builtins are now catchable Err values** (they
  used to abort uncatchably); wrong argument *count* still aborts.
- **Source positions on more errors:** a `?` that propagates to the top level, and runtime errors in
  `-n`/`-p` one-liner mode, now print `file:line:col` with a caret, like the normal script path.

### Changed (process control)
- `run`/`capture`/`pipe`/`each_line`/`start` reject options they cannot honor, as a catchable Err:
  `start` rejects `{timeout}` (detached, unbounded); a `.bat`/`.cmd` target rejects `{arg0}`
  (cmd.exe owns argv[0]).
- **`env` option renamed to `env_exact`** (exact child environment); `env_add` overlays the inherited
  one. **[breaking]**
- **CSV writes CRLF (`\r\n`) line endings by default** (RFC 4180); pass `{crlf: false}` for `\n`.
  **[breaking]** `mtime` now returns float seconds.

### Added / other
- `to_json` / `to_csv` reject distinct map keys that stringify identically (invalid JSON / duplicate
  CSV headers).
- `index_of` is polymorphic over arrays (the sibling of `contains`).
- `datetime` (`strftime`/`parse_time`/`date_parts`) and `write_file` reject unknown option keys, so a
  misspelled `{UTC: true}` can't silently fall back to local time.
- Command-not-found messages carry a single `exec:` prefix; `repeat` with a bad count is a catchable
  Err.

### Docs
- Added this CHANGELOG. README/manual version and status refreshed; DESIGN §3 stale `[LOCKED]`
  entries annotated as superseded; ROADMAP HTTP client/server typo fixed; the unsafe
  `$cond and $a or $b` ternary-substitute is documented as a trap.

## [0.5] — 2026-06-29
- Opt-in string interpolation: plain `'…'`/`"…"` no longer interpolate — use `$"…"`, `$qq{}`, or a
  `<<$TAG` heredoc. **[breaking]**
- `exe()` and `is_terminal()`; portable process supervision (`{supervise: true}`, later superseded by
  Job Objects); a self-documenting `format()` error for the `%`-verb habit.
- Decision: stringy-numeric coercion rejected — `"5" + 3` stays a type error.

## [0.4] — 2026-06-28
First complete release. Expanded standard library (~120 builtins + a drang prelude), a robust minimal
HTTP client (`http_get`/`http_post`), `drang fmt` (provenance-faithful formatter), and first-class
builtins (`map($xs, basename)`). Preceded by a seven-front adversarial hardening sweep that fixed 9
bugs (2 critical).

## [0.3] — 2026-06-28
Modules (`use`) + value-level immutability (frozen constants & exports), printf-grade `format` specs,
date/time + `sleep`, hashing/encoding/randomness, `drang test`, array slices + rune-aware string
indexing, default parameters.

## [0.2] · [0.1]
Earlier milestones: the register-VM/tree-walker engine, closures/lambdas/pipelines, errors-as-values
(`?`/`//`), first-class `qr//` regexes, real concurrency (`spawn`/channels/`pmap`), external-command
orchestration, JSON/CSV, one-liner `-n`/`-p` mode, and standalone `drang build`.
