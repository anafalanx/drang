---
type: overview
title: drang
description: A small, parallel, Perl-inspired scripting language for text, system glue, and orchestration — implemented in Go, Windows-only.
tags: [drang, overview, readme, scripting-language]
timestamp: 2026-07-21
---

# drang

A small, parallel, **Perl-inspired** scripting language for text processing, system glue, and
orchestration, implemented in Go (standard library, plus `golang.org/x/sys/windows` for the Win32 APIs). *Reads like Ruby, thinks like Perl, runs
like Go.*

*(drang is Dutch/German for drive, urge, momentum.)*

```
$xs := [1, 2, 3, 4]
say(map(filter($xs, |$x| $x % 2 == 0), |$x| $x * $x))   # [4, 16]
```

## Highlights

- **First-class errors**: failures are ordinary values; `?` propagates them, `//` recovers. No
  exceptions by default, so a dropped failure is a deliberate choice, not an accident.
- **Effortless parallelism**: `pmap` runs across every core with no GIL, made safe *by subtraction*:
  top-level bindings are frozen and there are no mutable globals, so data-parallel code is lock-free.
- **Perl's soul, not its warts**: one `$` sigil on every variable, string interpolation and heredocs,
  `qr//` regex literals, `q//`/`qq//`/`qw//` quotes, and `|>` pipelines.
- **Glue built in**: `run`/`capture`/`pipe`/`start` with `{cwd, env, env_add, stdin, timeout}` options and
  process-tree kill on timeout, `stream_lines` streaming, plus channels and tasks.
- **Batteries, curated**: modules (`use`) — private-by-default, `export` names the API, exports
  deeply frozen — a standard library of ~120 builtins plus a drang-written prelude, JSON & CSV,
  `qr//` regexes, date/time, hashing/encoding, a persistent key-value store, boundary shape
  checking (`validate`), and a minimal robust HTTP client (`http_get`/`http_post`). Broad, not a
  kitchen sink.
- **Local GUI cockpits**: `serve({routes: ...})` runs a token-gated local htmx server and opens a
  clamped browser app window; `drang build tool.dr --web web --gui` packs the tool, its assets, and
  the runtime into one double-clickable exe.
- **Functions are first-class**: pass any lambda *or builtin* by name: `map($xs, basename)`,
  `reduce(0, max)`, `filter(bool)`.
- **Tooling**: `drang fmt` formats faithfully (provenance-preserving), `drang test` runs `example`
  assertions, and `drang build` produces a standalone executable.
- **Fast for an interpreter**: a register bytecode VM kept byte-for-byte in lockstep with a
  tree-walking oracle. Roughly 3× CPython's wall-clock (geometric mean) on a mixed suite, with faster
  startup, and real multi-core parallelism the GIL can't match.
- **A REPL**: run `drang` with no arguments (or `drang --repl`); state persists across lines.

## Install

drang is **Windows-only** (Windows 11 23H2+ / Windows Server 2025+). Grab the prebuilt
`drang_*_windows_amd64.exe` from the [latest release](https://github.com/anafalanx/drang/releases/latest),
put it on your `PATH`, or build from source below.

## Build & run

```
go build -o drang ./cmd/drang

./drang app.dr              # run a file
./drang -e 'say("hello")'  # run inline
echo 'say(6 * 7)' | ./drang # run from stdin
./drang                     # start the REPL

./drang fmt -w app.dr       # format in place (respects read-only files)
./drang test app.dr         # run the script's `example` assertions
```

Flags: `--run` (default), `--ast`, `--tokens`, `--version`, `--help`. Arguments after the program are
exposed to the script as `$ARGV`; the environment is the `$ENV` map.

## Standalone executables

Compile a script into a single self-contained executable (the drang runtime with your program
embedded) that needs no separate interpreter:

```
drang build app.dr -o app.exe
app.exe one two             # runs the embedded program; args become $ARGV

# Finished GUI: embed web assets and suppress the console window.
drang build cockpit.dr --web web --gui -o cockpit.exe
```

`drang build` validates that the script parses, refuses to overwrite the source or the running
interpreter, and writes atomically. Console mode remains the default so errors stay visible during
development. `--gui` changes only the standalone's Windows subsystem: Explorer launches it without
a console, so stdout/stderr and startup errors are normally invisible; use it for finished GUI apps.

## Documentation

- **[MANUAL.md](MANUAL.md)**: the full language manual. Every example in it was executed against the
  interpreter, so the shown output is real.
- **[DESIGN.md](DESIGN.md)**: the design and build log.
- **[TESTING.md](TESTING.md)**: how drang is verified — the local preflight (`drang tools/verify.dr`),
  the `-race` suite, and the three fuzzers. There is no hosted CI; the preflight is the release gate.

## Status

**drang 0.12.0**: modules are **private-by-default** (`export` marks the API; unmarked top-level
names never leave their file), **`validate`** checks map shapes at boundaries (type tokens are the
conversion builtins used first-class: `{host: str, port: int}`), and local GUI tools ship as single
signed executables (`serve` + `drang build --web --gui`). The road here: 0.11.0 brought local htmx
GUIs to a clamped browser window; 0.10.0 completed single-process orchestration
(`recv_stdout`/`recv_stderr` — drive a child both ways); 0.9 added the persistent store, `walk`,
and in-place one-liner editing (`-i`); 0.7–0.8 froze the pre-1.0 vocabulary, added kernel-enforced
resource caps, and made the VM fast. Full history in [CHANGELOG.md](CHANGELOG.md). See the
*"Not Yet"* section of the manual for the remaining gaps: no character ranges, no `match`/`switch`
dispatch yet, no implicit string↔number coercion (deliberate), and no structs — maps plus
`validate` stand in, deliberately.

## License

[MIT](LICENSE).
