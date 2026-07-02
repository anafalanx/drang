# Changelog

All notable changes to drang are recorded here. Dates are the release dates; the format loosely
follows [Keep a Changelog](https://keepachangelog.com/). Versions are git tags `vX.Y`.

## [0.6] — Unreleased

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
