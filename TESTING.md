---
type: guide
title: testing drang
description: How to run drang's local preflight — the -race suite, the fuzzers, the example checks, and the release gate.
tags: [drang, testing, preflight, quality]
timestamp: 2026-07-21
---

# Testing drang

drang has **no hosted CI and no scheduled fuzzing**. That is a deliberate choice, not
a gap: the whole test apparatus — the `-race` suite and the fuzzers — lives in the
repo and runs **locally, on demand**. This document is how to run it and what it
guarantees.

The single entry point is the **preflight**:

```
drang tools/verify.dr          # build + vet + -race suite + docs checks, then a 20s fuzz burst per target
drang tools/verify.dr 60s      # longer fuzz burst per target
drang tools/verify.dr none     # gate + docs checks only, no fuzzing
```

or, via the [`z.json`](z.json) task runner, `z check` (which runs it straight from
source with `go run`, so there is never a stale interpreter).

> **Run the preflight before cutting a release, and after any major work.** With no CI
> watching the tree, this manual pass *is* the gate. A green preflight is the bar for
> tagging a version.

---

## The preflight, stage by stage

`tools/verify.dr` is drang orchestrating its own verification (drang testing drang). It
runs these stages and exits non-zero if any fails:

| Stage | Command | What it catches |
|-------|---------|-----------------|
| build | `go build ./...` | anything that doesn't compile (incl. `cmd/drang`) |
| vet | `go vet ./...` | suspicious constructs the compiler allows |
| race suite | `go test -race ./...` | logic regressions **and** data races, across every package |
| fmt gate | `drang fmt --check bench tools examples` | canonical-format drift in the repo's own scripts |
| manual examples | `drang tools/check_examples.dr MANUAL.md` | manual/reality drift — shown output byte-compared |
| reference examples | `drang tools/check_examples.dr REFERENCE.md` | the same, for any runnable REFERENCE blocks |
| OKF lint | `drang tools/okf_lint.dr` | a concept doc missing its OKF `type` frontmatter |
| fuzz: parser | `go test -fuzz=FuzzParse ./internal/parser` | inputs that panic/hang the front end |
| fuzz: printer | `go test -fuzz=FuzzFmtRoundTrip ./internal/printer` | `drang fmt` losing its fixed point |
| fuzz: eval | `go test -fuzz=FuzzBackendParity ./internal/eval` | the VM and the oracle disagreeing |

The build/vet/race stages are a **gate**: the first failure stops the run (no point
checking docs or fuzzing a broken tree). The docs group then runs as a batch (so one
pass surfaces every doc regression), and the three fuzz stages all run even if one
fails — ten stages in all, and a single pass reports everything at once.

The `-race` suite runs the **full** suite with no `-short`, so the slow behavioral
tests (e.g. the Job-Object resource-limit and CPU-breach tests, which spin a real
busy-loop child for several seconds) are included. That is intentional for a
release gate.

---

## Everyday commands

You don't need the full preflight for a tight edit loop. The plain suite is fast:

```
go test ./...            # whole suite, ~seconds
go test -short ./...     # skips the multi-second behavioral limit tests
go test -race ./...      # add the race detector (slower; what the preflight uses)
go test ./internal/eval  # one package
go test -run TestVMParity ./internal/eval   # one test
```

The toolchain is **Go 1.26** (it is not on the global `PATH` in this environment — it
lives in the z workspace tree, e.g. `.z/go/1.26.4/bin`; the `-race` suite additionally
needs `CGO_ENABLED=1` and the workspace's MSYS2 gcc).

---

## The parity backbone

drang runs on two backends: a **register bytecode VM** (the production path) and a
**tree-walking oracle**. They are kept byte-for-byte in lockstep — every program must
produce identical stdout and the same success/error outcome on both. `runBackend` in
[internal/eval/vm_test.go](internal/eval/vm_test.go) runs a source string on either
backend; `TestVMParity` asserts the two agree over a hand-written corpus.

This is drang's deepest correctness invariant, and it is what `FuzzBackendParity`
automates: instead of a fixed corpus, it lets the fuzzer *discover* programs that make
the two backends disagree. Historically, that is exactly where the subtle bugs have
lived (int-vs-float equality, structural equality of shared sub-values, the recursion
guard).

---

## The three fuzz targets

Go's native fuzzing (`func FuzzXxx(f *testing.F)`) drives all three. Each ships a seed
corpus inline via `f.Add(...)`, so **the seeds also run as ordinary tests** under
`go test` — regressions are caught even without `-fuzz`.

### `FuzzParse` — the front end is total
[internal/parser/fuzz_test.go](internal/parser/fuzz_test.go)

`New(src).ParseProgram()` must never panic or hang on *any* byte string. A parse
failure is a normal outcome (it lands in `Errors()`); a crash is a bug. Seeds cover
real surface forms plus adversarial fragments (unterminated strings/regex, lone
sigils, deep nesting, NUL bytes).

### `FuzzFmtRoundTrip` — the formatter is a fixed point
[internal/printer/fuzz_test.go](internal/printer/fuzz_test.go)

For any input `Format` accepts: (1) the output re-formats without error, and (2)
`Format(Format(x)) == Format(x)`. Together these are the guarantee `drang fmt` makes —
it never emits something it can't read back, and running it twice changes nothing.
Inputs that don't parse are out of scope.

### `FuzzBackendParity` — the VM matches the oracle
[internal/eval/fuzz_test.go](internal/eval/fuzz_test.go)

The heart of the parity net. A generated program is **run on both backends** and their
stdout and error-outcome must match. To keep this safe and meaningful, it only runs
programs that are **pure, deterministic, and terminating**, decided by `pureProgram`:

- **Allowlist, not blocklist.** Only side-effect-free, deterministic builtins are
  permitted (arithmetic, strings, collections, JSON/CSV, regex matching, the pure
  higher-order functions, `say`). Anything else — `run`, `read_file`, `now`, `rand`,
  `pmap`, `http`, `env`, `use`, … — makes the program ineligible and it is **skipped,
  not failed**. This fails *closed*: an unrecognized builtin or a future AST node is
  treated as impure, so a fuzz input can never drive the suite to touch the
  filesystem, the network, a process, the clock, or an RNG.
- **User functions are fine.** Leading-dot identifiers (`.fib`) resolve in-program; any
  impure builtin they call would itself appear (bare) in the walked AST and trip the
  gate.
- **`while`/`until` are out of scope.** They are the only unbounded constructs — `for`
  ranges are finite and runaway recursion is depth-guarded into a clean error — so a
  pure `while` can be a *non-terminating* program, which is a hang, not a parity
  divergence. Excluding it structurally keeps the fuzzer flowing. Ordinary while-loop
  parity is still covered by the deterministic `TestVMParity` corpus.

Because both backends share the same value types and map ordering, a pure program is
*guaranteed* to agree unless there is a real compiler/VM bug — which is precisely what
this target hunts.

---

## Working with a fuzz finding

When a fuzzer finds a crashing/failing input, it prints the failure and writes a
minimized reproducer to `internal/<pkg>/testdata/fuzz/<Target>/<hash>`. To reproduce
it deterministically:

```
go test -run='FuzzBackendParity/<hash>' ./internal/eval
```

That reproducer file is a regression seed. If it represents a real bug, **commit it**
(the `testdata/fuzz/` tree is not git-ignored) so the case is locked in forever; the
next `go test` will replay it. If it is a false alarm (e.g. an input the scope rules
should have excluded), fix the scope rule and delete the file.

The *evolving* corpus of interesting inputs the engine accumulates lives in the Go
build cache (`$GOCACHE/fuzz`), **outside** the repo — it is a local accelerator, not
source, and is safe to discard (`go clean -fuzzcache`).

To fuzz one target directly, for longer:

```
go test -run='^$' -fuzz='^FuzzBackendParity$' -fuzztime=5m ./internal/eval
```

`-run='^$'` disables the normal tests so only the fuzzer runs; `-fuzztime` bounds it
(omit it and the fuzzer runs until interrupted).

---

## Why no CI?

Because for a solo, Windows-only project, a green checkmark on someone else's machine
buys less than a disciplined local pass buys — and it costs a maintenance surface
(runner images, Windows-in-the-cloud quirks, secrets) that isn't worth carrying yet.
The tests are the asset; *where* they run is a detail. When the project wants hosted CI
later, the preflight is already the exact script a workflow would call.
