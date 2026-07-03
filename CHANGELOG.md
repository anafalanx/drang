# Changelog

All notable changes to drang are recorded here. Dates are the release dates; the format loosely
follows [Keep a Changelog](https://keepachangelog.com/). Versions are git tags `vX.Y`.

## [Unreleased]

The single-process-control release: kernel-enforced resource limits and the process-control gaps
filled, plus a first math/ergonomics batch and two robustness fixes. Still Windows-only.

### Added — process control & resource limits
- **Kernel-enforced resource caps** on every exec form (`run`/`capture`/`capture_all`/`stream_lines`/
  `pipe`/`start`), via Windows Job Objects: `{max_memory}` / `{max_job_memory}` (per-process /
  whole-job commit, bytes), `{max_cpu}` / `{max_job_cpu}` (per-process / whole-job user CPU,
  milliseconds), and `{max_job_procs}` (active-process cap). A breach terminates the child with exit
  **137**, and the job's IOCP monitor names which cap (memory / CPU-time) tripped.
- **Process-control builtins & options:** `status(proc)` polls a child without blocking
  (`{running, ok, code}`); a `kill()`'d process reports `"was killed"` (137), distinct from a real
  exit code; `{stdin_file: path}` feeds stdin from a file; `{merge_stderr: true}` folds stderr into
  stdout (`2>&1`); `{cwd}` is validated up front (a bad directory is a clean catchable Err); and
  `start(..., {stdin_pipe: true})` with `send_stdin(proc, s)` / `close_stdin(proc)` drives a live
  child's stdin.

### Added — language
- **Trigonometry & extended math** (radians), the capability area the manual had promised:
  `sin cos tan asin acos atan atan2 exp log2 log10 hypot cbrt`, plus the constants `pi()` / `e()`.
  Thin bindings over Go's `math`; a domain or type error is a catchable Err, never a silent NaN.
- **Compound assignment extended** to `%=`, `~=`, and `//=`. `~=` appends/concatenates (seeding a
  fresh slot with `""`); `//=` is defined-or in place — it takes the right-hand side only when the
  slot is nil or an error, and keeps a present value (even a falsy `0`).

### Fixed
- **`reverse` no longer infinite-loops on an array.** It now reverses arrays as well as strings, and a
  non-string / non-array argument is a catchable Err instead of a hang (`reverse([1,2,3])` → `[3,2,1]`).
- **A channel `send`/`recv` that could only ever deadlock** — no counterparty and no other task
  running — is now a catchable Err rather than a raw Go `all goroutines are asleep` process abort. A
  genuine multi-goroutine deadlock still fails loudly.

### Changed
- **Namespace:** the (brand-new, undocumented) `max_procs` exec option is renamed `max_job_procs` — it
  is job-scoped, and the bare `max_` prefix wrongly implied per-process. The builtin-naming rule is
  amended in DESIGN.md to "bare when unambiguous, `domain_` prefix only on collision."

### Changed — pre-1.0 namespace coherence pass (breaking; `drang fmt --fix` migrates)
A whole-namespace naming audit (six domain reviews + a law synthesis, recorded in DESIGN.md)
pulled the last inconsistent names into line before 1.0 freezes them. Every rename ships with a
`drang fmt --fix` rewrite rule — the first real rules in the edition mechanism — so migration is
one mechanical command:
- **`gsub` → `replace_all`, `replace` → `replace_all`, new `replace_first`.** One discoverable
  pair replaces the literal/regex verb fork: the needle's TYPE picks the mode (a plain string is
  a **literal**; a `qr//` / `re(...)` regex matches as a pattern, with `$1`/`${name}` backrefs) —
  the Ruby `gsub` convention. The bare-form polarity now matches `find`/`find_all` (bare = first,
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
- **`index_of` → `find_index`** — "first occurrence" joins the `find`/`find_all` stem; drops the
  lone `of`-connector.
- **`abspath` → `abs_path`** — composed underscore form, matching its sibling `is_abs` and Perl's
  `Cwd::abs_path` (Python's glued spelling was never on the blessed-abbreviation list).
- **`sys_gc` → `drang_gc`** — `sys_` misread as the operating system; `drang_` marks knobs and
  introspection on the drang interpreter itself (reserved siblings: `drang_version`, `drang_mem`),
  unambiguous in orchestration scripts that steer OTHER runtimes.
- **`tally` removed** — it was exactly `count_by` with the identity key; `--fix` rewrites
  `tally($xs)` → `count_by($xs, |$e| $e)`.
- Deliberately **kept** (documented exceptions): `uniq`/`uniq_by`, `rm`/`mkdir`/`cwd` (blessed
  Unix muscle memory), `dirname`/`basename` (entrenched single words), `start` vs `spawn` (a real
  Proc-vs-Task distinction), and polymorphic `await`.
- **Not auto-migrated** (`--fix` leaves these to fail loudly rather than silently change
  behavior): a FIRST-CLASS `gsub` reference (`$f := gsub`, `map($fs, gsub)`) — rewrite by hand to
  `|$s, $p, $r| replace_all($s, re($p), $r)`; a FIRST-CLASS `tally` reference — rewrite to
  `|$xs| count_by($xs, |$e| $e)`; and any gsub/tally call whose arity was already an error in 0.7.
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
