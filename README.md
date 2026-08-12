---
type: overview
title: drang
description: A small, parallel, Perl-inspired scripting language for text, system glue, and orchestration — implemented in Go, Windows-only.
tags: [drang, overview, readme, scripting-language]
timestamp: 2026-08-12
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
  exceptions by default; migration lint warns at directly recognizable discarded, boolean, and
  output-stringification sites without changing runtime semantics.
- **Parallelism with isolation**: `pmap` and `spawn` run without a GIL, snapshot mutable callback
  captures, and share bounded process-wide capacity. Channels, stores, tasks, and process handles
  are the deliberate shared coordination points.
- **Bounded under hostile input**: whole-value strings and source reads, materialized collections,
  recursive parser/snapshot depth, subprocess output, and standalone payloads have explicit ceilings and fail
  loudly instead of exhausting the process. Streaming paths remain available for larger flows.
- **Perl's soul, not its warts**: one `$` sigil on every variable, string interpolation and heredocs,
  `qr//` regex literals, `q//`/`qq//`/`qw//` quotes, and `|>` pipelines.
- **Glue built in**: `run`/`capture`/`pipe`/`start` with `{cwd, env, env_add, stdin, timeout}` options and
  process-tree kill on timeout, `stream_lines` streaming, plus channels and tasks.
- **Batteries, curated**: modules (`use`) — private-by-default, `export` names the API, exports
  deeply frozen — 161 direct builtins, 15 higher-order forms, and a 25-function drang prelude, JSON & CSV,
  `qr//` regexes, date/time, hashing/encoding, a persistent key-value store, boundary shape
  checking (`validate`), and a minimal robust HTTP client (`http_get`/`http_post`). Broad, not a
  kitchen sink.
- **Local GUI cockpits**: `serve({routes: ...})` runs a token-gated local htmx server and opens a
  clamped browser app window; `drang build tool.dr --web web --gui` packs the tool, its assets, and
  the runtime into one double-clickable exe.
- **Functions are first-class**: pass any lambda *or builtin* by name: `map($xs, basename)`,
  `reduce(0, max)`, `filter(bool)`.
- **Tooling**: `drang fmt` formats faithfully (provenance-preserving) and rewrites atomically,
  `drang test` runs `example` assertions, and `drang build` produces a bounded, validated standalone
  executable.
- **Fast for an interpreter**: a register bytecode VM kept byte-for-byte in lockstep with a
  tree-walking oracle. The current mixed benchmark is about 1.6× CPython's wall-clock (geometric
  mean), with faster startup and real multi-core parallelism the GIL can't match.
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

The generated standalone is a new executable image. Appending its payload does not preserve an
Authenticode signature from the interpreter it was copied from, so sign and timestamp the **final**
`.exe` after `drang build` when distributing it.

## Documentation

- **[MANUAL.md](MANUAL.md)**: the full language manual. Every self-contained runnable example has
  its exit status and declared output stream checked against the interpreter; contextual and shell
  examples are marked and reported separately.
- **[DESIGN.md](DESIGN.md)**: the design and build log.
- **[TESTING.md](TESTING.md)**: how drang is verified — the local preflight (`C:\dev\z.exe check`),
  the `-race` suite, and the three fuzzers. There is no hosted CI; the preflight is the release gate.

## Status

**drang 0.12.1**: a compatibility-preserving hardening release. Untrusted whole-value work has
explicit ceilings; concurrent callbacks isolate mutable captures; child processes, module/store
lifecycles, recursive copy, formatter writes, and standalone payloads fail safely; and migration
lint points out directly recognizable Err misuse. The 0.12 language remains unchanged: modules are
**private-by-default** (`export` marks the API), and **`validate`** checks map shapes at boundaries.
The road here: 0.11.0 brought local htmx GUIs to a clamped browser window; 0.10.0 completed
single-process orchestration
(`recv_stdout`/`recv_stderr` — drive a child both ways); 0.9 added the persistent store, `walk`,
and in-place one-liner editing (`-i`); 0.7–0.8 froze the pre-1.0 vocabulary, added kernel-enforced
resource caps, and made the VM fast. Full history in [CHANGELOG.md](CHANGELOG.md). See the
*"Not Yet"* section of the manual for the remaining gaps: no character ranges, no `match`/`switch`
dispatch yet, no implicit string↔number coercion (deliberate), and no structs — maps plus
`validate` stand in, deliberately.

## License

[MIT](LICENSE).
