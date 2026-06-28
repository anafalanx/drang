# drang

A small, parallel, **Perl-inspired** scripting language for text processing, system glue, and
orchestration, implemented in Go (standard library only). *Reads like Ruby, thinks like Perl, runs
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
- **Glue built in**: `run`/`capture`/`pipe`/`start` with `{cwd, env, stdin, timeout}` options and
  process-tree kill on timeout, `each_line` streaming, plus channels and tasks.
- **Batteries, curated**: modules (`use`) with frozen exports, a standard library of ~120 builtins
  plus a drang-written prelude, JSON & CSV, `qr//` regexes, date/time, hashing/encoding, and a minimal
  robust HTTP client (`http_get`/`http_post`). Broad, not a kitchen sink.
- **Functions are first-class**: pass any lambda *or builtin* by name: `map($xs, basename)`,
  `reduce(0, max)`, `filter(bool)`.
- **Tooling**: `drang fmt` formats faithfully (provenance-preserving), `drang test` runs `example`
  assertions, and `drang build` produces a standalone executable.
- **Fast for an interpreter**: a register bytecode VM kept byte-for-byte in lockstep with a
  tree-walking oracle. Roughly 3× CPython's wall-clock (geometric mean) on a mixed suite, with faster
  startup, and real multi-core parallelism the GIL can't match.
- **A REPL**: run `drang` with no arguments (or `drang --repl`); state persists across lines.

## Install

Grab a prebuilt binary from the [latest release](https://github.com/anafalanx/drang/releases/latest)
(0.4 ships `drang_0.4_darwin_amd64`, `drang_0.4_darwin_arm64`, `drang_0.4_linux_amd64`, and
`drang_0.4_windows_amd64.exe`), put it on your `PATH`, and `chmod +x` it on Linux/macOS, or build from
source below.

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
drang build app.dr -o app   # -> ./app  (app.exe on Windows)
./app one two               # runs the embedded program; args become $ARGV
```

`drang build` validates that the script parses, refuses to overwrite the source or the running
interpreter, and writes atomically. It produces an executable for the platform it runs on: Windows and
Linux work natively, and on macOS the build best-effort ad-hoc re-signs the result (appending the
payload invalidates the Mach-O signature), printing the one `codesign -s -` command to run if it can't.

## Documentation

- **[MANUAL.md](MANUAL.md)**: the full language manual. Every example in it was executed against the
  interpreter, so the shown output is real.
- **[DESIGN.md](DESIGN.md)**: the design and build log.

## Status

**drang 0.4**: the first complete release, and a genuine daily-driver. See the *"Not Yet"* section of
the manual for the remaining gaps: no structs (maps stand in as records), only daily-driver math (no
trig), no character ranges, no implicit string↔number coercion, and no in-place one-liner mode (`-i`).

## License

[MIT](LICENSE).
