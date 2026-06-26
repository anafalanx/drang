# drang — Language Design

*Working name (provisional). A simpler, parallel, Perl-inspired text-processing language, implemented in Go.*

**Tagline:** *Reads like Ruby, thinks like Perl, runs like Go.*

**Status:** Living document. Every decision below is tagged:

- `[LOCKED]` — decided
- `[PROPOSED]` — strawman, awaiting ratification
- `[OPEN]` — not yet decided

Last updated: 2026-06-25.

---

## 1. Vision

drang is a small scripting language for **text processing and system glue** — the niche Perl/awk/sed own — rebuilt for the modern world:

- **Simpler than Perl.** Keep Perl's *soul* (first-class regex, terse one-liners, sigils, interpolation, autovivification) without its warts (scalar/list context, `bless`, typeglobs, the punctuation-variable zoo, string `eval`).
- **Fast.** A register bytecode VM over an unboxed value representation, with variables resolved to slots at compile time.
- **Complete via Go's stdlib.** The standard library is a *curated binding* over Go's — text, files, `os/exec`, encoding, net — not a from-scratch reimplementation.
- **Genuinely parallel.** Real multi-core execution (no GIL), made *safe by construction* rather than by locks.
- **Ships as a single exe.** `build` turns a source file into a standalone Windows executable.

### Non-goals

- **Not** Perl-compatible — no CPAN, no `$/`, no context.
- **Not** a general-purpose application language — no class hierarchies, no metaobject protocol.
- **Not** hermetic/sandboxed (the deliberate opposite of Starlark) — it's a glue language; it touches the world.

---

## 2. The three pillars — and why the design serves them

### Speed
- **Unboxed values:** a tagged struct, not `interface{}`. Numbers live inline; no per-number heap allocation. (See §6.)
- **Compile-time slot resolution:** lexical variables become array indices, never runtime map lookups.
- **Register bytecode VM** with a serializable instruction stream.
- **Immutable strings:** sharing a string is a header copy, not a deep copy.

### Completeness
- The stdlib is a **binding layer over Go's stdlib**. Go's batteries (text, files, OS, net, encoding) map almost 1:1 onto Perl's domain, so "fairly complete" is curation work, not implementation work.

### Parallelism — achieved by *subtraction*
This is the central design insight worth preserving:

> We never *added* a concurrency model. We *removed* every source of shared mutable state — no context, no mutable globals (constants are frozen), no `$@`/punctuation globals, lexical-only scoping, per-goroutine specials, immutable strings, no string `eval`. With almost nothing shared and mutable, goroutine-parallel execution is safe **without locks**.

The frozen-constants idea is borrowed directly from Starlark's frozen-values model; here it is the foundation of lock-free parallelism.

---

## 3. Decision record

### 3.1 Variables & scope
- **One sigil `$`** on every variable; type comes from the value, not the name. Unifies regular vars with the already-`$` magic vars; keeps `"$x"` interpolation trivial. `[LOCKED]` *(revised from an earlier "three invariant sigils" choice.)*
- **No scalar/list context**; every function returns one well-defined thing. `[LOCKED]`
- **Lexical declaration only**, keyword `let`. Enables slot resolution and goroutine-local safety. No dynamic scoping. `[LOCKED]`
- **Curated magic vars:** `$_`, `$1..$n`, `$ARGV`, `$ENV` — held as per-goroutine state. The punctuation globals (`$/ $\ $,`) are dropped. `[LOCKED]`
- **Top-level bindings are constants** — frozen after init, so they're shareable across goroutines for free. Mutable state must be lexical or passed. `[LOCKED]`

### 3.2 Types & data
- **Scalars:** `int64`, `float64`, immutable `string`, `bool`, `undef`. `[LOCKED]`
- **Numbers:** `int64` + `float64` with auto-promotion; arbitrary-precision **deferred**. `[LOCKED]`
- **Strings are immutable**; `$x =~ s/a/b/` rebinds the lvalue (sugar for `$x = replace($x, …)`). `[LOCKED]`
- **Stringy-numeric coercion** (`"5" + 3 == 8`), with distinct numeric vs string operators. `[LOCKED]`
- **Arrays & hashes**, with **transparent nesting** — reach in with `.` chains (`$data.users[0].name`); no explicit `\`-refs or deref gymnastics. `[LOCKED]`
- **`[]` indexes both** arrays and hashes (`$x[0]`, `$x["k"]`). `[LOCKED]`
- **Write-side autovivification** — deep *writes* create intermediate containers; deep *reads* of missing keys do not. `[LOCKED]`
- **Slices** (`$a[1,3,5]`, `$h["x","y"]`). `[LOCKED]`
- **Ranges** — numbers and `'a'..'z'`. `[LOCKED]`
- **Lightweight structs/records** with named fields and methods — no inheritance. `[LOCKED]` Default field values: `[PROPOSED]`
- **Truthiness:** clean rule — falsy = `undef`/`false`/`0`/`""`/empty collection; drops Perl's `"0"`-is-false wart. Exact rule `[OPEN]`

### 3.3 Regex (the core)
- **RE2 dialect** (Go-native): linear-time, ReDoS-proof, zero C dependency. No backreferences/lookaround (upgrade path: `regexp2`). `[LOCKED]`
- **Operators:** `=~`, `!~`, `m//`, `s///`, `tr//`. `[LOCKED]`
- **`s///` computed replacement via callback** (`s/pat/<fn>/`); no `/e`. Plain flags `/g /i /m /s /x` stay. `[LOCKED]`
- **Captures:** `$1..$n` and named captures (per-goroutine); whole/pre/post-match via a returned **match object**, not `$& $' $\``. `[LOCKED]`
- **Regex literals are first-class values**, compiled once and cached. `[LOCKED]`

### 3.4 Syntax & control flow
- `.` field/method access · `~` string concat · `[]` indexing. `[LOCKED]`
- **Newline-terminated**; `;` is an optional separator (for packing one-liners). `[LOCKED]`
- **Paren-free conditions, mandatory braces** (`if $x > 0 { … }`). `[LOCKED]`
- **Block loops: `for`-in and `while` only**; ranges replace C-style `for`; no `foreach`/block-`until`. `[LOCKED]`
- **Postfix modifiers:** `if`/`unless`/`while`/`until`/`for`. `[LOCKED]`
- **`last`/`next` + loop labels**; no `redo`. `[LOCKED]`
- **`:` for pairs** (over `=>`); **`#` line comments**. `[LOCKED]`
- **String interpolation:** bare `$var`; `${ expr }` blocks for anything complex; lists interpolate space-joined; heredocs supported. `[LOCKED]`
- **Quote operators: full set** — `qw// q// qq//` with custom delimiters (the one place we spend lexer complexity, for slash/quote-heavy text). `[LOCKED]`
- **`-n`/`-p` autoloop** binding `$_` per input line. `[LOCKED]`
- **`BEGIN`/`END`** blocks as the autoloop's pre/post companions. `[PROPOSED]`
- **Implicit return** (value of last expression). `[OPEN]`

### 3.5 Subs & modules
- **Named subs with signatures:** `sub f($a, $b) { … }`. `[LOCKED]`
- **Closures.** `[LOCKED]`
- **Lambda syntax:** `|$a, $b| expr-or-block` (terse; every combinator takes one). `[PROPOSED]`
- **File modules:** one file = one module, explicit exports, import by path; loaded once and frozen (build-then-freeze). `[LOCKED]`

### 3.6 Errors & the impure side
- **`try`/`catch`** blocks; no `$@` global. Maps onto Go error returns underneath. `[LOCKED]`
- **No string `eval`** — preserves compile-then-freeze and closes an injection hole. `[LOCKED]`
- **External commands via `os/exec` builtins** (`run`/`capture`), explicit args, no shell by default. `[LOCKED]`

### 3.7 Concurrency
- **Real multi-core** execution, goroutine-backed (no GIL). `[LOCKED]`
- **Transfer semantics:** copy-on-send by default + an explicit **`freeze()`** hatch for big read-only data. (Immutable strings make the common copy nearly free.) `[LOCKED]`
- **Surface:** high-level **data-parallel combinators** (`pmap`/`pfor`, a `-P` parallel autoloop) over **exposed `spawn` + channels** for power users. Model `[LOCKED]`; exact combinator API `[PROPOSED]`

---

## 4. Cheat-sheet

```
$x                 one sigil for everything
let $x = 1         lexical declaration
$user.name         field / method access
$a ~ $b            string concat
$arr[0]  $h["k"]   indexing (arrays and hashes both use [])
$h{a: 1, b: 2}     hash literal (':' pairs)
[1, 2, 3]          array literal
1..10  'a'..'z'    ranges
"$x and ${expr}"   interpolation (bare var; ${} for complex; lists space-joined)
qw(a b c)          word list (full q// qq// quote ops available)
/pat/  $s =~ /…/   regex literal + match (RE2); $1.. and match object
$s =~ s/a/b/       in-place-feel substitution (rebinds; strings immutable)
if $c { … }        paren-free, braces mandatory
do() if $c         postfix modifier
for $x in $xs { }  / while $c { }   (only block loops)
sub f($a, $b) { }  signatures
|$a, $b| $a + $b   lambda                          [PROPOSED]
try { } catch ($e) { }
run("git", "log")  / capture(...)   external commands, no shell
pmap($xs, |$x| …)  parallel map                    [PROPOSED API]
```

---

## 5. Worked example — access-log analyzer

```ruby
#!/usr/bin/env drang

# 1.2.3.4 - - [10/Oct/2024:13:55:36] "GET /api HTTP/1.1" 200 1234
let $LINE = /^(\S+) .* "[^"]*" (\d+) (\d+)/        # first-class regex value, compiled once

struct Stats {
    $reqs   = 0
    $bytes  = 0
    $errors = 0
    $by_ip  = {}
}

sub scan_file($path) {
    let $s = Stats()
    for $line in lines($path) {
        let $m = $line =~ $LINE or next            # match object; falsy -> skip
        $s.reqs   += 1
        $s.bytes  += $m[3]                          # "1234" auto-coerces to number
        $s.errors += 1 if $m[2] >= 400             # postfix if, numeric coercion
        $s.by_ip[$m[1]] += 1                        # [] index + write-side autoviv
    }
    return $s
}

sub merge($a, $b) {                                 # runs serially -> in-place is safe
    $a.reqs += $b.reqs;  $a.bytes += $b.bytes;  $a.errors += $b.errors
    for $ip in keys($b.by_ip) { $a.by_ip[$ip] += $b.by_ip[$ip] }
    return $a
}

sub report($s) {
    say "requests ${$s.reqs}  ·  bytes ${$s.bytes}  ·  errors ${$s.errors}"
    let $top = sort(pairs($s.by_ip), |$a, $b| $b.value <=> $a.value)
    say "  ${$_.value}\t${$_.key}" for $top[0..4]
}

# serial:
report( reduce(map($ARGV,  |$f| scan_file($f)), Stats(), merge) )

# parallel — the ONLY change is map -> pmap; safe by construction:
report( reduce(pmap($ARGV, |$f| scan_file($f)), Stats(), merge) )
```

One-liner form (needs the proposed `END`):

```
drang -ne '$c[$1]++ if /^(\S+)/; END { for $ip in keys($c) { say "${$c[$ip]}\t$ip" } }' access.log
```

Why the parallel version is race-free: `scan_file` touches no shared mutable state; each returns a `Stats` partial that copy-on-send isolates; the `reduce`/`merge` runs serially on the main goroutine. The language offers no shared accumulator to mutate, so the racy version is unwriteable.

---

## 6. Implementation architecture

**Pipeline:** `source → lexer → parser → resolver (slot assignment) → [tree-walk eval | bytecode compiler] → register VM`.

- **Build order:** tree-walk first (iterate on semantics), then the register bytecode VM — *mandatory*, because the serialized bytecode is the standalone-exe payload (§7).
- **Value:** tagged struct `{ tag; int64/float64 inline; *Obj }`. No `interface{}` (avoids per-number boxing). **No NaN-boxing** — it hides pointers from Go's GC and causes use-after-free; heap refs stay honest `*Obj` pointers.
- **Variables:** compile-time slot indices; no runtime name lookup.
- **Strings:** immutable — share freely across goroutines, "copy" is a header copy.
- **GC:** Go's, unfought. The language exposes no GC surface (no weak refs/finalizers/`collectgarbage`) and is immutability-heavy, so Go's collector just works.
- **Regex:** compiled and cached by pattern; regex literals are values.
- **Concurrency:** each goroutine gets its own VM execution state (stack, frames, `$_`, specials). The compiled program image is **built once, frozen, shared read-only** — so all goroutines share code/constants with no locks. Channels carry `Value`s; copy-on-send isolates workers; mutable runtime caches (e.g. regex cache) are guarded or precomputed.

---

## 7. Packaging & platform (Windows 11+ first)

**CLI:**
- `drang run foo.dr` — interpret.
- `drang build foo.dr -o foo.exe` — produce a standalone executable.

**Exe mechanism:** runtime stub + **appended frozen bytecode image** (overlay). At startup the binary inspects its own tail: overlay present → run it; absent → act as the interpreter/CLI. One binary, two modes — the same mechanism `els.exe` uses (appended zipfs payload), and what Deno/Bun `compile` do. Chosen over transpile-to-Go (toolchain dependency, slow) and AOT codegen (years of work).

**Windows specifics:**
- Pure Go (RE2, **no CGo**) → a fully **static PE, zero DLLs** — the textbook standalone. (The RE2 regex choice is what makes this trivial.)
- **Console subsystem** (a CLI, unlike els's GUI subsystem) so stdout/stderr work.
- Ship **amd64 + arm64** stubs (Win11-and-later includes ARM).
- **Staged-swap on rebuild** — Windows locks a running exe, so `build` can't overwrite an executing target (reuse els's staged-swap recipe).
- **Distribution reality:** appending overlay breaks Authenticode, and unsigned exes hit SmartScreen reputation. v1 ships unsigned; later, sign the *generated* exe (EV cert for instant reputation).

**The convergence:** the frozen program image is simultaneously the **exe payload**, the **goroutine-shareable** artifact, and the **instant-startup** format (no runtime parse — beats Perl/Python's per-invocation compile). One artifact, three wins.

---

## 8. Open questions — the "evaluate further" agenda

Ratify or revise:
- Lambda syntax `|…|` — `[PROPOSED]`
- Combinator API: `map`/`pmap`/`reduce`/`sort`/`filter` signatures; `pmap` ordering (ordered by default?) — `[PROPOSED]`
- `BEGIN`/`END` blocks for the autoloop — `[PROPOSED]`
- Struct default field values — `[PROPOSED]`

Still open:
- Exact truthiness rule — `[OPEN]`
- Implicit return (last expression) vs explicit-only — `[OPEN]`
- Integer overflow policy (promote to float? error? wrap?) — `[OPEN]`
- Operator set: keep `//` (defined-or), `x` (repeat), `<=>`, `cmp`? — `[OPEN]`
- `for`-loop destructuring (`for $k, $v in $h`) vs `pairs()` — `[OPEN]`
- File extension (`.dr`?) and final name — `[OPEN]`

---

## 9. Build roadmap

1. **Front end + tree-walker.** Lexer, parser, resolver, tree-walk evaluator over the tagged `Value`. *Milestone:* run hello-world, a regex match, and the `-n` autoloop.
2. **Core stdlib bindings.** strings, `lines`/IO, regex, `os/exec`, json.
3. **Bytecode VM.** Register compiler + VM; serializable bytecode + constant pool.
4. **Concurrency.** `spawn`/channels + `pmap`/`reduce`; copy-on-send + `freeze()`.
5. **`build`.** Frozen-image serialization + overlay append; Windows stub (amd64/arm64), staged swap.
6. **Ratify** the `[PROPOSED]` items; resolve the `[OPEN]` ones.

---

## 10. Decision-log summary

| Area | Decision | Status |
|---|---|---|
| Sigil | One `$` for everything | LOCKED |
| Context | None | LOCKED |
| Scope | Lexical `let` only | LOCKED |
| Magic vars | Curated, per-goroutine | LOCKED |
| Globals | Frozen constants only | LOCKED |
| Numbers | int64 + float64, defer bignum | LOCKED |
| Strings | Immutable, `s///` rebinds | LOCKED |
| Nesting | Transparent `.`; `[]` for all indexing | LOCKED |
| Autoviv | Write-side only | LOCKED |
| Regex | RE2; callbacks; match object; literals as values | LOCKED |
| Syntax | `.` `~` `[]`; newline-term; paren-free braces | LOCKED |
| Loops | `for`-in + `while`; postfix modifiers; labels | LOCKED |
| Quotes | Full `qw// q// qq//` | LOCKED |
| Subs | Signatures; closures; file modules | LOCKED |
| Errors | `try`/`catch`; no string eval | LOCKED |
| Exec | `os/exec` builtins, no shell | LOCKED |
| Parallelism | Real multi-core; copy-on-send + `freeze()` | LOCKED |
| Engine | Tagged value; slot resolution; register bytecode VM | LOCKED |
| Packaging | Stub + appended frozen image; static PE; Win11+ | LOCKED |
| Lambda / combinators / BEGIN-END | — | PROPOSED |
| Truthiness / implicit-return / overflow / operators | — | OPEN |

---

## 11. Update (2026-06-25) — direction locked + decision backlog

### Project direction: **C — personal daily-driver**
drang's purpose is to be *the author's own language* — the tool reached for **instead of Python** for glue, text, and one-off scripting. Broad adoption is explicitly **not** a near-term goal. Two later evolutions are noted but not pursued yet:

- **A** — specialize the *pitch* around one vertical. Leading candidate: **ops / observability glue** (scan logs across cores, match, alert, expose `/metrics` — one static binary).
- **B** — add a *categorical* capability. Leading candidate: **distributed / multi-machine execution**, extending the concurrency model from multicore to multi-host over Go's net stack.

**Implications**
- **Success metric:** "Do I reach for drang instead of Python?" The eval set is the author's *real* Python scripts (the dependency-light glue ones — drang does not replace the PyPI ecosystem).
- **Open questions resolve by personal taste, fast, revised on friction** — not by abstract correctness.
- **The path to value is short:** a tree-walk interpreter + the stdlib bindings actually used is enough to start. The VM, exe-gen, parallelism, and distribution are the A/B growth, *not* the entry.

### Syntax decisions since §3
- Pipeline operator `|>`. `[LOCKED]`
- `sub` → **`fn`** keyword. `[LOCKED]`
- Named + default parameters: `fn f($a, $b = 8080)`. `[LOCKED]`
- One `$` sigil and regex operators-primary (`=~`/`s///`) — both reaffirmed.

### Standard library / batteries (all planned as thin Go-stdlib bindings)
- Lazy streams / generators (Go 1.23 `iter.Seq` protocol)
- Structured concurrency + deadlines (`within(5s){}`); duration literals (`5s`, `2h30m`); timers/tickers (`every`/`after`)
- HTTP server + client one-liners (`serve` / `fetch`)
- Actors (goroutine + mailbox) and `select` over channels
- Transparent compressed I/O (`lines("x.gz")`)
- Cross-compile `build --target os/arch,…`
- Codecs + struct auto-mapping (json/csv/toml/xml)
- Embed files into the exe (`embed(…)`)
- Templating (`render`, text/html template)
- Crypto / hash / uuid / base64
- Filesystem walk / glob (+ watch)
- Test runner (`test "…" { assert }`, `drang test`)
- Signal handling (`on_signal`)
- Config / flag binding via reflection
- `--profile` pprof output (freebie)
- **Deferred:** SQL access (`database/sql`)

### Re-cut roadmap (MVP-first, supersedes §9)
1. **Usable core — the daily-driver MVP.** Lexer → parser → resolver → tree-walk eval over the tagged `Value`. Core stdlib: string ops, regex (RE2), `lines`/file I/O, `os/exec`, json, basic collection ops (map/filter/sort/reduce), interpolation, the `-n/-p` autoloop. *Goal: port the author's real Python scripts and feel the friction.*
2. **Harden by use.** Resolve `[OPEN]` questions from real scripts; ratify `[PROPOSED]` syntax.
3. **Speed.** Register bytecode VM (serializable).
4. **Single exe.** Frozen-image + overlay append; Windows stub (amd64/arm64), staged swap.
5. **The special stuff (A/B growth).** Pipe-driven combinators, parallelism (`pmap`/actors/`select`/structured concurrency), HTTP, and the rest of the batteries — added as wanted.

---

## 12. Comb update (2026-06-25) — Go-filter, orchestration re-centering, error model

### Principle: properties are filtered through the Go implementation
Every property and harvested lesson is re-cut to Go's grain: *what does Go make cheap here, and does that reshape the feature?* Lessons from gen-1 (C) and gen-2 (Zig) do **not** transfer verbatim.

### Re-centering: orchestration is co-equal with text
Per the zmal survey, the real daily-driver workload is **orchestration** — subprocess, files, paths, env, exit-code/error handling (currently Tcl task-runners + Node workspace scripts) — as much as text. Orchestration is elevated to first-class. **Text/regex stays first-class too** (more text work is expected). Autoloop one-liners are *secondary* to the task-runner/script shape. **Agent-writability: explicitly out of scope.**

### Error model — supersedes Cluster F (try/catch retired)
Go's value-error grain + gen-2's must-use gem + the orchestration re-centering all converge:
- **Value-results, not exceptions.** Fallible ops return a result (value-or-error; fields `.ok` / `.out` / `.code` / `.err`). Reading `.out` of a failed result raises unless checked. `[LOCKED]`
- **Must-use.** Dropping a fallible result is an error — "you cannot silently ignore a failed subprocess." Enforced *dynamically* in the tree-walk MVP, *statically* in the bytecode/checker phase. `[LOCKED]`
- **Propagate: postfix `?`.** `run("gcc", ...)?` propagates the error, aborting at top level with the exit code. `[LOCKED]`
- **Handle: `or`.** `run(...) or { ... }` (err bound) or `... or fallback`. `[LOCKED]`
- **Discard: `ignore`** (explicit). `[LOCKED]`
- **Fallibility is per-call, inferred** — builtins tagged fallible; a user function is fallible if it propagates/returns an error. No signatures, no `nothrow`. `[LOCKED]`

### Lineage harvest (the few gems, Go-shaped)
- **Examples-as-tests** — gen-2's inline `example 2 3 => 5`, run on definition via the test machinery (Go has testable examples natively). `[PROPOSED]`
- **Frozen constants** — already in drang; lineage-validated, doubles as the parallelism foundation.
- **Dev vs run/build mode** — gen-1's development-image vs release-image: REPL/dev is mutable & redefinable; run/build is the frozen, goroutine-shareable image. `[PROPOSED]`
- **Stay rejected:** static typing, agent-first, prefix `[head args]`, length taxonomy, declaration noise, no-truthiness.

### Orchestration core = ergonomic binding over Go stdlib (nearly free)
- Paths → `path/filepath` (cross-platform for free).
- Subprocess → `os/exec` + `CommandContext` (timeouts/cancellation, ties to `within(5s)`); `run` flattens list args.
- Incremental "rebuild-if-stale" → `os.Stat().ModTime()` (a `make`-like primitive — the #1 build pattern).
- Atomic writes / staged swap → `os.Rename`.

---

## 13. Comb complete (2026-06-25) — orchestration core

The orchestration re-centering is combed end to end.

### Program shape
- Three coexisting modes: **script** (top-level code runs; *no mandatory `main`*), **autoloop** (`-n/-p`, secondary), **task-runner**. `[LOCKED]`
- **Task-runner dispatch: `dispatch({ build, test, ... })` builtin** — routes `$ARGV[0]` to the matching `fn`, passes remaining args, auto `--list`, errors on unknown command; a task's `?` sets the process exit code. No new syntax. `[LOCKED]`

### Subprocess
- `run(...)` streams stdio through (status result); `capture(...)` returns stdout; a process is **iterable as lazy lines** (`for $l in stream(...)`). `run` flattens list args. All fallible → `?`. `[LOCKED]`
- Pipelines: native **`pipe(run(...), run(...))`** (os/exec wiring, no shell) + explicit **`sh("a | b")`** escape for real shell features. `[LOCKED]`
- **Per-command `{cwd, env}` options** (os/exec `Cmd.Dir`/`Env`) — no global `cd` (goroutines share process cwd; a global chdir would race). `[LOCKED]`
- Parallel processes = `pmap(items, |x| run(...)?)` (free; each gets a `CommandContext` timeout). Prefer `|>` + builtins over shelling out for text (grep/wc/sort → native, in-process).

### Stdlib API style
- **Free functions** over built-in values (paths/strings/lists/maps), chained with **`|>`** where `x |> f(a)` = `f(x, a)`. No method dispatch on builtins. `.` reserved for struct/result/nested-data members. `[LOCKED]`
- Paths are plain strings (Go `filepath`/`os` bindings); `glob` eager, `walk` lazy; reads/writes fallible.

### Examples-as-tests (lineage gem, adopted)
- Inline **`example f(args) == result`** / **`example f(bad) fails`** clauses, run on definition (instant REPL feedback) and collected by `drang test`. Standalone `test "name" { }` blocks kept for integration tests. `[LOCKED]`

### Dev vs run/build mode (lineage gem, adopted)
- **REPL/dev:** mutable, redefinable top-level; **every eval runs from clean state** (code-only image, no persistent mutable heap) — redefine freely, reproducible runs.
- **Run/build:** frozen constants, goroutine-shareable image. `[LOCKED]`

### Still open (resolve by use)
Truthiness rule · implicit return · integer-overflow policy · operator set · `example … fails` exact syntax · `dispatch` arg-mapping details · file extension + final name.

---

## 14. Build progress (2026-06-25) — the walking skeleton runs

Implementation underway in Go: stdlib-only, `module github.com/anafalanx/drang`,
`go 1.26`, vendored toolchain (`r/go/1.26.4`). Layout mirrors `_kuu`:
`cmd/drang` + `internal/{token,lexer,ast,parser,value,eval}`.

**DONE — runs end to end (lexer → parser → tree-walk eval):**
- **lexer** with Go-style automatic terminator insertion: a newline ends a
  statement only after a value-ending token and at bracket depth 0; `;` also ends.
- **Pratt parser:** literals, `$var`, idents, grouping, prefix `-`/`!`, infix
  arithmetic/comparison/concat, `|>` (desugared to a call), `f(...)`, `x[i]`,
  `x.name`, postfix `?`. Verified: precedence, left-assoc, `|>` desugaring, ASI both ways.
- **value:** tagged unboxed struct (nil/bool/int64/float64/string), no `interface{}`.
- **eval:** `let`/`$var` (lexical scope), arithmetic, concat, comparison, prefix
  `-`/`!`, the `say` builtin. Runs and prints; runtime errors reported and exit 1.
- **CLI:** `drang [--run|--ast|--tokens] (-e '<src>' | <file>)`; `--run` is default.

**Decisions resolved while building:**
- Calls require parentheses — `f(args)`; no paren-less command calls. `[LOCKED]`
- Go-style automatic terminator insertion, with bracket-depth suppression. `[LOCKED]`
- Truthiness: `nil`/`false`/`0`/`0.0`/`""` are falsy. `[PROVISIONAL]`
- `/` is float division (`10/4 == 2.5`). `[PROVISIONAL]`
- Stringy-numeric coercion (`"5" + 3`) deferred; string + number errors for now. `[DEFERRED]`

**Build order from here:** control flow (`if/else`, `while`, `for`-in) + blocks +
assignment → user functions (`fn`) → the `?`/must-use error model → orchestration
builtins (`run`/`capture`/`glob`/…) on it → `dispatch`. Target: run a real `tasks.dr`.

---

## 15. Build progress (2026-06-25) — declarations, scope, control flow

**Declaration form:** `$x := 5` declares (mutable), `$x ::= 5` declares a **constant**, `$x = 5` reassigns; no keyword (replaced `let`). Assigning to or redeclaring a constant *in its own scope* is an error; shadowing it in a nested scope is allowed (consistent with the scope model). `[LOCKED]`

**Scope model** (lexical; confirmed by running programs):
- Block-scoped — every `{ }` is a scope. `[LOCKED]`
- Shadowing allowed — an inner `:=` may shadow an outer binding. `[LOCKED]`
- Assignment requires a prior declaration — `$x = v` on an undeclared `$x` is an error. `[LOCKED]`
- Per-iteration loop scope — each loop-body iteration gets a fresh child scope. `[LOCKED]`
- Closures: lexical, capture-by-reference within a goroutine. `[PLANNED]`
- **Top-level `:=` is mutable**; "frozen" applies to the shared *code/constant image*, not runtime bindings (refines Cluster F). `[LOCKED]`

**DONE this slice (verified):** `:=`/`=`, `if`/`else`/`else if`, `while`, brace
blocks with child scopes, postfix `if`/`unless`/`while`/`until`. Block-form `for`-in
awaits the collections slice; `break`/`continue` await a later slice.

**NEXT:** user functions (`fn name($a, $b) { … return … }`, calls, closures) →
`?`/must-use error model → orchestration builtins → `dispatch`.

---

## 16. Build progress (2026-06-25) — functions

**DONE (verified):** user functions `fn name($a, $b) { … }`; calls to user
functions and builtins; **closures** (capture the defining scope); **recursion**;
**first-class function values** (pass/return functions — `dispatch({build,test})`
becomes expressible); explicit `return` and **implicit return of the last
expression**; postfix `return … if`; arity checking.

**Decisions:**
- Implicit return of the last expression (Ruby/Rust-style), alongside explicit `return` — resolves the open implicit-return item. `[LOCKED]`
- Functions are first-class values via a `Func` tag + an `Obj` interface in the `value` package (heap-backed values referenced without an import cycle). `[LOCKED]`

The language core is now complete: variables, constants, lexical/block scope,
control flow, and functions/closures.

**NEXT:** the `?`/must-use error model — the headline feature (value-results;
`run(...)?` propagates, `or` handles, must-use enforced dynamically first) →
orchestration builtins (`run`/`capture`/`glob`/…) → `dispatch`.

---

## 17. Build progress (2026-06-25) — scoped error model

**Decision (supersedes §12's must-use):** must-use enforcement is **dropped** —
low value for a solo daily-driver (you're the author; you know which calls fail)
and it's the fiddly part. Errors are plain values: Go's model minus the verbosity. `[LOCKED]`

**DONE (verified):**
- First-class **error value** (`Err`: message + code).
- **`?` propagation** — an error short-circuits: it propagates out of the enclosing
  function as its error result, or aborts at the top level with the message.
  Mechanically a sibling of the `return` signal, caught at the call boundary.
- **`or` fallback** — `expr or fallback` yields the fallback when `expr` is an error.
- An unhandled error simply **flows as a value** (no auto-abort, no `ignore`).
- Test builtins `fail(msg)`, `int(x)`.

Exit-code plumbing **shipped**: a top-level `?` aborts with the failing Err's code
(`ExitCode`/`clampCode`), and `dispatch` exits with a task's `result.ErrCode()`. The
tasks.dr port (§27) confirmed this fully covers subprocess exit-code propagation —
the block-form `or { … }` (binding the error to read `.code`/`.message`) turned out
**not** to be needed for it, so it stays deferred until a real use demands it.
Static checking: not planned.

## 18. Build progress (2026-06-25) — collections (arrays, maps, ranges, for-in)

Built under Ultracode: a design workflow settled the semantics, then the slice
was implemented in eight verified sub-steps (value layer → lexer `..`/`//`/`+=`
→ ast/parser literals + lvalues → eval construct+read → eval write+autoviv+
compound → for-in → builtins → tests), then an adversarial review workflow.

**DONE (verified):**
- **Heap types** behind the `Obj` interface (widened with `Len`/`Equal`/`DeepCopy`):
  `Array` (held as `*Array` — reference semantics, aliased appends visible),
  `OrderedMap` (insertion-ordered; scalar keys), `IntRange` (inclusive, lazy).
- **Literals:** `[a, b]`, `{k: v}` (keys are expressions — quote string keys),
  `lo..hi`. Multi-line literals via parser newline-skipping.
- **Indexing reads:** `$a[i]` (negative indexing; out-of-range → catchable `Err`),
  `$m[k]` (miss → `undef`), `$m.field` (= `$m["field"]`).
- **Lvalues & writes:** `$a[i] = v` (set / push-at-`len` / gap-error), `$m[k] = v`,
  `$m.f = v`, compound `+= -= *= /=` (single place-eval, `undef` seeds 0), and
  **write-side autovivification** (`$s.by_ip[ip] += 1` from `{}`); place-validated
  at parse (only `Var`/`Index`/`Field`). Const interior mutable; rebind refused.
- **for-in:** block (`for $x in e`, `for $i,$x in e`) and postfix (`stmt for e`,
  binding `$_`); snapshot-on-iterate; per-iteration child scope; arrays/maps/
  ranges/strings (strings by **rune**). Non-iterable aborts.
- **Builtins:** `len push pop keys values pairs has delete chars contains`
  (arity → aborting error; type/runtime → catchable `Err`; `keys/values/pairs`
  return fresh arrays).
- **Equality:** container `==` is structural/deep; functions by identity; empty
  containers are falsy.
- Go test suite (`internal/eval/eval_test.go`, table-driven over say output).

**Forks resolved (all → the recommended option):**
1. Missing element is **asymmetric** — array out-of-range read is a catchable
   `Err` (a bug), map miss is `undef` (a question) — plus a new **`//` defined-or**
   that fires on `nil` *or* error (`or` stays error-only).
2. Map one-var `for` yields **values** (two-var yields key, value).
3. **`$s[0]` is an `Err`** — strings aren't indexable; use `chars()`/`substr`.
4. Container `==` is **structural**.
5. *(technical)* array write at `i==len` pushes; `i>len` is a gap error.
6. *(technical)* **integral-float keys collide with ints** (`1` and `1.0` are one
   key; the first-stored key Value is kept on overwrite) — matches `==`.

**Adversarial review (Ultracode workflow, 6 finders × per-finding verify):** four
confirmed bugs found and fixed, each pinned by a regression test —
(1) `IntRange.Len()` int64 overflow on wide ranges (negative `len`, a huge range
reading as *falsy*) plus the twin `for`-range loop that span forever at
`MaxInt64` — fixed with a uint64 span + saturation and a break-before-overflow
loop; (2) map `for`-in read the *live* key/value slices, so a `delete` mid-loop
corrupted iteration; (3) array `for`-in froze length but shared the backing
array, so `$a[i] = v` mid-loop leaked through — both fixed by copying the slice
(a true snapshot); (4) `keys`/`values`/`pairs` returned a catchable `Err` on
wrong arity instead of aborting — `mapArg` now splits arity (aborts) from type
(catchable). One smell — write-side bad-key/hashability errors abort while the
read side is catchable — was judged **intentional and kept**: a write is a
statement, so a catchable `Err` would be silently dropped; aborting loudly is
correct and consistent with the gap-write abort rule.

Deferred: a `range()` builtin (redundant with `..`; index iteration already
covered by two-var `for` and `0..len-1`); char ranges (`'a'..'z'` — no char
literals yet); `break`/`continue`; lambdas; `{name}` map-key shorthand.

**NEXT:** orchestration builtins — `run`/`capture`/`glob`/path/`mkdir`/`newer`
→ `dispatch` (takes a map of tasks) → a real `tasks.dr`.

## 19. Build progress (2026-06-25) — orchestration core (run/capture, fs, dispatch)

A design workflow first grounded the API in the *actual* zmal tooling (the
per-project Tcl runners behind `z`, e.g. `_els/tools/tasks.tcl`): the dominant
primitives are `stream` (exec, inherit stdio, propagate child exit code via
`CHILDSTATUS→exit n`), `exec`-capture for version probes, `file join` (2693×),
`exists`/`isdir` (~1036×), `glob -nocomplain` (244×), `file mkdir` (159×),
`file mtime` incremental gates (64×), and an identical `argv[0]→task_<name>`
dispatch in every runner (deps = plain proc calls + build-if-missing guards).
Built in five verified sub-slices, then an adversarial review.

**Decisions (polled, all → recommended):** subprocess failure is a **catchable
`Err` carrying the exit code**; the filesystem surface is the **full family**
(paths + guards + glob + mkdir + newer/mtime + IO + atomic-swap); the rebuild
gate is **`newer`/`mtime` plus a `stale` helper**.

**DONE (verified):**
- **Foundation:** `$ARGV` / `$ENV` globals; CLI `drang [flags] <prog> [args...]`
  (mode flags consumed only up to the first non-flag, so flags after the program
  reach `$ARGV`); top-level `?` now sets the **process exit code** from the
  failing `Err`'s code (the bit §17 deferred "until `run`").
- **`run`** — no shell; streams stdio; `nil` on exit 0, `Err(code=child exit)` on
  non-zero, `Err(127)` if it can't start. `run(...)?` aborts with the child's
  code; `run(...) or`/`//` recover.
- **`capture`** — buffers stdout, returns the **trimmed string** on success, an
  `Err` (child stderr folded into the message) on failure.
- Both: top-level array args **flatten one level**; trailing **`{cwd, env, stdin}`**
  options map; `env` overlays the environment **case-insensitively** (Windows).
- **Filesystem (20 builtins):** `join dirname basename ext stem abs slash`
  (pure), `exists isdir` (bool guards), `glob` (sorted, empty-on-no-match,
  `**` recursive), `mkdir mtime newer stale`, `read_file write_file`, and the
  atomic-swap family `rename rm copy size` (`rm` because `delete` is the map-key
  remover). Arity/non-string aborts; runtime failures are catchable `Err`s.
- **`dispatch(tasks)`** — exit-terminal argv router: `argv[0]` selects a task fn
  (called with the rest as one array, or no args if it declares none); empty /
  `--list` lists tasks (exit 0); unknown → exit 2; a task's `?`-propagated or
  returned `Err` → process exit code. Inner `dispatchResolve` is os.Exit-free for
  unit testing. Tasks call each other as ordinary fns (no DAG).
- A working **`examples/tasks.dr`** drives build/test(dep)/ver(capture)/clean/
  unknown(2)/fails(propagates 5).
- Go tests for path helpers, the fs family (`t.TempDir`), `newer`/`stale`,
  `exec` arg-flatten/env-merge/exit-code, and `dispatchResolve`.

**Refinement surfaced by building (adopted):** a **bare identifier map key is its
name as a string** — `{cwd: x}` ≡ `{"cwd": x}` — so options/config maps and
`dispatch({build: build})` read naturally, and `{cwd: …}` aligns with `$m.cwd`
field access. A `$var`, quoted, or non-identifier key is still evaluated.

**Adversarial review (Ultracode workflow, 4 finders × per-finding verify):** three
issues found and fixed, each pinned by a regression test — (1, bug) a bare or
trailing `**` glob (`glob("dir/**")`) wrongly yielded the walk root itself (the
base dir, or the literal `.` when globbing from the CWD — a footgun for
`for $f in glob("**") { rm($f) }`): `doublestarGlob` now skips the root entry;
(2, smell) a malformed pattern Err'd on the plain path but silently returned `[]`
on the `**` path: it now validates wildcard segments and Errs consistently;
(3, smell) the unknown-task listing went to stdout while its header went to
stderr: `listTasks` takes a writer so the whole exit-2 diagnostic stays on stderr
(the `--list` result still prints to stdout). No exit-code, subprocess, or
env-merge defects were found.

Deferred: `sh()` shell escape + `pipe()`/`stream()` and `pmap` (parallelism by
subtraction — `run`/`capture` are already pure value→value with per-call
`cwd`/`env`, so they slot in lock-free); block-form `or { … $err … }` with the
error bound (the only way to read an `Err`'s code/message from script — until
then, propagate with `?` or recover with `or`/`//`); `read_file`/`write_file`
encoding knobs (bytes vs text); `within(5s)`/timeouts via `CommandContext`;
char ranges. (The string-escape policy is revisited in §20.)

## 20. Build progress (2026-06-25) — text manipulation & regex

The Perl-heritage strength, and the biggest remaining daily-driver gap. Built in
two verified sub-slices: core string builtins, then regex + an escape refinement.

**DONE (verified):**
- **String builtins:** `split` (whitespace / runes / separator), `replace`
  (literal), `trim` (whitespace or cutset), `upper`/`lower`,
  `starts_with`/`ends_with`, `format` (`{}` placeholders rendered like `say`;
  `{{`/`}}` literal braces; missing arg → literal `{}`), `lines` (CRLF-normalized,
  one trailing newline dropped), `repeat` (negative count → `Err`).
- **`join` made polymorphic:** `join(array, sep?)` joins strings — the universal
  meaning, what a Perl/Python user reaches for — while `join(str, str, …)` still
  joins path segments; disambiguated by first-arg type, so shipped path usage is
  unaffected.
- **Regex (Go RE2):** `matches` (bool), `match` (`[full, g1, …]` or nil),
  `find_all` (all full matches), `gsub` (global replace, `$1`/`${name}` backrefs).
  A malformed pattern is a catchable `Err`. RE2 is linear-time with no
  backreferences-in-pattern and no lookaround — a deliberate safety trade.
- ~30 new Go test cases (strings, regex, the escape policy).

**Refinement surfaced by building (adopted):** the string-escape policy is now
**lenient** — known escapes (`\n \t \r \\ \"`) process, but an *unknown* escape
**keeps its backslash** (`"\d"` → `\d`, `"C:\dir"` → `C:\dir`). Without it every
regex metacharacter and Windows path would need doubled backslashes — a
nonstarter for a regex-central, Windows-first, Perl-inspired language. (Supersedes
§19's "standard escape policy" note.)

**Adversarial review (Ultracode workflow, 3 finders × per-finding verify):** two
issues found and fixed, each pinned by a regression test — (1, bug) `repeat(s, n)`
with a huge count panicked `strings.Repeat` and **crashed the interpreter**
(uncatchable exit 2) instead of returning a catchable `Err`; the root cause was
that no builtin panic was ever recovered. Fixed two ways: `repeat` now caps the
result size, and a `safeBuiltin` wrapper converts ANY builtin panic into a
catchable `Err`, so ordinary script input can never crash the interpreter.
(2, smell) the regex builtins reported "expects two string paths" (leftover
path-helper wording that also hit `starts_with`/`ends_with`); the shared
`twoStrings` helper now gives a precise per-argument message. No correctness,
hang, or escape-policy defects were found.

Deferred: `%`-style `sprintf` verbs (the `{}` form covers the common case);
a named-capture → map variant of `match`; regex literals (`/re/`) and raw
strings; more string helpers (`pad`, `index_of`, `substr`) as real use demands.

## 21. Build progress (2026-06-25) — lambdas + higher-order functions

The piece that makes everything compose, and the prerequisite for `pmap`. A
design workflow settled the grammar (the `|` delimiter has real ambiguity
surface); built in two verified sub-slices (lambdas, then the array operations).

**Decisions (polled):** callbacks receive **element + optional index** (a 2-param
lambda gets the index); ship the **full toolkit** (Core 9 + take/drop/uniq/flat_map).

**DONE (verified):**
- **Lambdas:** `|$x| expr` and `|$x| { block }` — anonymous function VALUES
  capturing the defining scope, reusing the named-`fn` closure machinery verbatim.
  `|$a, $b|` multi-param, `||` zero-param. A bare `|` lexes to a new `BAR` token;
  `|>` still wins (greedy two-char), so the pipeline is unaffected. The body
  parses at lowest precedence — it absorbs operators/`|>`/`?` but stops at
  `,`/`)`/`]`/newline (none are infix), and since a lambda is always the last HOF
  argument its body runs cleanly to the `)`. A `{` after the params is a block;
  `|$x| ({a: 1})` returns a map. (`||` is reserved — there is no `||` operator.)
- **13 array operations.** The callback HOFs are evaluator special-forms (they
  need `callFunction`, which a plain builtin can't reach), **array-first** so
  `$xs |> map(f)` composes: `map filter reject each find any all count reduce
  flat_map`. Plus no-callback builtins `take drop uniq`.
  - Arity-flex callbacks: 1 param → element, 2 → (element, index); `reduce`'s fn
    is (acc, el) or (acc, el, index).
  - **Fail-loud:** the first `Err` a callback produces stops the HOF and becomes
    its result — every HOF `.IsErr()`-checks *before* testing truthiness (an `Err`
    is truthy, so this is load-bearing) — so `map(...)?` propagates.
  - Empty-array identities, snapshot-on-iterate, `reduce(arr, init, fn)`, `find`
    miss → nil (composes with `//`), `each` returns the original array.
- ~25 new Go test cases.

This unlocks the real glue style:

```
glob("src/**/*.go")
  |> map(|$f| basename($f))
  |> reject(|$f| ends_with($f, "_test.go"))
  |> reduce(0, |$a, $f| $a + size($f))
```

Point-free works for **named user functions** (`map($xs, my_fn)`); builtins
aren't first-class values yet, so wrap them in a lambda (`map($xs, |$f| f($f))`).

**Adversarial review (Ultracode workflow, 3 finders × per-finding verify):**
**clean — zero findings.** The finders probed closure capture, the `|`-vs-`|>`
boundary, every HOF's `.IsErr()` propagation, snapshot-on-iterate, arity-flex,
`take`/`drop` bounds, and `uniq` equality, and confirmed the slice sound — the
first slice to pass review with no defects. The design workflow's grammar work
and the earlier reviews' lessons (notably the load-bearing `.IsErr()` check) were
already baked in.

Deferred: `sort`/`sort_by`/`min_by`/`max_by` (need a comparator operator `<=>`/
`cmp` the language lacks); `uniq_by`/`group_by`; **first-class builtins** (a
native-function value so `map($xs, basename)` works point-free).

**NEXT — the parallelism pillar:** `spawn` + channels + `pmap`, wiring the
already-built cycle-safe `DeepCopy` hook to channel-send / goroutine isolation.

## 22. Build progress (2026-06-25) — real-task validation + boolean operators

Before building parallelism on top of it, validated the whole stack against a
**real task**: a drang port of `_els/tools/toolcheck.tcl` (probe the project-local
`.toolchain` for the components `els` needs; report presence + version). It runs
cleanly against the actual toolchain — exercising `capture`+stdin, `read_file`,
regex, maps, `map`/`reduce`/`each`/`filter`/`count`, `format`, and the error model
on genuinely real data (`examples/toolcheck.dr`).

**One real bug, fixed:** a multi-line block body inside a call argument (e.g.
`each(|$c| { …newline-separated stmts… })`) lost statement termination — the lexer
suppressed newlines inside *any* `(`, including a `{` block nested in a call.
Fixed with a bracket **stack**: a newline terminates when the innermost open
bracket is `{` (a block) or none, and is suppressed inside `(`/`[` (so long
expressions/pipelines still wrap freely). Regression-tested. (Synthetic tests
missed it; a real multi-line callback hit it immediately.)

**Gaps the port surfaced** — the big one: no boolean operators (`or` was
error-fallback, `||` is the lambda delimiter). Resolved (polled → keyword direction):
- **`and` / `or` / `not`** are now boolean operators (short-circuit, Lua/Python
  value-returning: `0 or 5` → 5, `7 and 9` → 9). `!` is an alias for `not`.
  Precedence low→high: `or` < `and` < `//` < `|>` < comparisons < arithmetic.
- **Recovery folds into `//`** (nil-or-error): the old `or`-fallback role moves to
  `//`, which is safe because fallible builtins return a real value or an `Err`
  (never a falsy-valid result), and `//` preserves `0`/`""`/`false` —
  `int("0") // -1` → 0 where boolean `or` would wrongly give -1.
- **`run` returns `true` on success** (was nil), composing with `//` and `if`.
- `?` (propagate) unchanged; `||` remains the zero-param-lambda delimiter.

The logic/recovery surface is now orthogonal: `and`/`or`/`not` for booleans, `//`
for recovery, `?` for propagation.

Smaller gaps noted (deferred): `if` is a statement not an expression; no `pad`
builtin (composed from `repeat` + `~`). (`cwd()` shipped in §27.)

**Adversarial review (Ultracode workflow, 2 finders × per-finding verify):** clean —
the only finding was a stale `run` doc comment (fixed). The finders confirmed
precedence (`a and b or c`, `x // d and y`, `not a == b`), short-circuit (the RHS
is not evaluated), value-returning semantics (`0 or 5` → 5), the `//` recovery
migration, and `run`→true all sound.

**Then: the parallelism pillar** (`spawn` + channels + `pmap`).

## 23. Build progress (2026-06-25) — the parallelism pillar (pmap)

The third pillar, finally real. A design workflow confirmed the key insight: the
tree-walker is **already race-safe for the core case** — a pure callback reading
frozen top-level constants + its own per-call params, over a deep-copy-isolated
element — because `callFunction` makes a fresh child scope per call and top-level
bindings are frozen constants. Scoped (polled) to ship `pmap` now, `spawn`+channels next.

**DONE (verified, race-detector clean):**
- **`pmap(arr, fn)`** — parallel map, same contract as `map` (array-first so
  `$xs |> pmap(f)` works; arity-flex 1/2-param callback; fail-loud first-`Err`),
  fanned across a **bounded `NumCPU` worker pool**, results in **input order**
  (each worker writes its own disjoint `out[i]` — no result lock).
- **Copy-on-send wired:** each element is `DeepCopyValue`'d (fresh visited map)
  before its callback, so workers never alias the same `*Array`/`*OrderedMap` —
  the cycle-safe `DeepCopy` hook from the collections slice finally does its job
  (aliased input elements `[$a, $a]` get independent copies).
- **Race-safe by construction:** fresh per-call child env, frozen top-level
  constants, disjoint result indices, a `sync.Once`/`cancelled`-flag first-error
  record with early-stop, and a `sync.Mutex` serializing `say`'s writes. A worker
  **panic is recovered into a catchable `Err`** so a goroutine can never crash
  the interpreter. The whole suite passes under `go test -race`.
- Demonstrated: `[1,2,3,4] |> pmap(|$x| capture("ping","-n","2","127.0.0.1"))`
  runs in **~1.3s vs ~4.3s** for serial `map` — real multi-core; "the only change
  is `map → pmap`."

**Safety model (locked "parallelism by subtraction"):** a `pmap` callback must be
**pure** over frozen consts + its own params + its (copied) element. Mutating a
*captured mutable lexical container* from a parallel callback is documented-undefined
— the language exposes no shared accumulator, so the canonical racy form
(reduce-into-shared) is largely unwriteable; a static resolver to reject the
residual case is future work.

**Adversarial review (Ultracode workflow, 2 finders × verify, with `-race` + stress):**
**clean — zero findings.** The finders hammered result ordering, fail-loud/early-stop,
the worker-panic recovery (`1/0` and undefined-var callbacks become catchable
`Err`s, not crashes), copy-on-send isolation of aliased elements, the `say`-mutex
under hundreds of concurrent workers, and re-ran the suite under `-race` — all
sound. The second slice in a row to pass review with no defects.

Deferred (next slice): **spawn + channels** — the power-user CSP layer (a `Task`
handle from `spawn(fn)`, a `Chan` value type, `send`/`recv`/`recv2`/`close`/
`drain`), plus optional per-task timeouts. `pfor` and a `freeze()` hatch for
passing big read-only data without copying are also deferred.

## 24. Build progress (2026-06-26) — spawn + channels (the CSP layer)

The power-user concurrency layer beneath `pmap`, from the same design workflow.
Polled to use **builtins** (not `<-` operators), consistent with the keyword
choice for booleans.

**DONE (verified, race-detector clean):**
- **`spawn(fn, args...)`** → a `Task` handle: runs `fn` on its own goroutine with
  deep-copied args (copy-on-send), over a snapshot of its captured env. A worker
  error or panic is captured as the task's `Err`. `spawn` is a special form (it
  calls `callFunction`).
- **`await(task)`** → blocks for the result (deep-copied out), idempotent; a
  task's `Err` (returned, `?`-propagated, or panicked) surfaces so `await(t)?`
  propagates and `await(t) // x` recovers. (Named `await` because `join` is the
  path/string-join builtin.)
- **Channels:** `chan()` / `chan(n)` (a new `Chan` value type — the intentionally
  *shared* rendezvous; its `DeepCopy` returns itself), `send` (copy-on-send,
  closed → `Err` not a crash), `recv` (closed → nil), `recv2` (`[v, ok]`), `close`
  (idempotent), `drain` (collect-until-closed).
- New value tags `Chan`/`Task`; wrong contexts (index/arith/iterate) give clean
  errors, `==` is identity. Demonstrated: producer/consumer and fan-out
  (`map(|$x| spawn(...)) |> map(|$t| await($t))`).

**`-race` caught a real bug, fixed:** `spawn` returns *asynchronously*, so the
spawned goroutine read its captured env *while the main goroutine kept mutating
the top-level scope* (defining the next `$var`) — a concurrent Go-map access the
design's "frozen top-level" assumption glossed over (pmap dodged it because main
*blocks* during pmap). Fixed with `Env.snapshot`: `spawn` runs over an isolated
copy of the captured env chain (binding values shared — frozen consts/scalars are
safe — but the *maps* are copied), so the goroutine never races main's
defines/sets. The whole suite passes `go test -race`.

**Adversarial review (Ultracode workflow, 2 finders × verify, with `-race` + hang
detection):** one real bug found and fixed — a concurrent `send` vs `close` on the
same channel was a data race (Go UB), benign in practice (the `recover` caught the
panic) but tripping `-race` and breaking the layer's advertised race-free
invariant. Fixed with the idiomatic **graceful-close** pattern: `close` now signals
a separate `done` channel and the *data* channel is never closed, so `send`/`recv`
`select` on `done` and a concurrent send/close can't race. Pinned by a
producer+closer+drainer regression test under `-race`. All other paths (fan-out,
nested spawn, copy-on-send isolation, the env snapshot, recv2/drain semantics)
verified clean.

Deferred: `<-` channel operators (sugar over the builtins); `select` over
channels; per-task timeouts (`within(5s)`); `pfor`; a `freeze()` hatch.

## 25. Build progress (2026-06-26) — a steerable GC knob

From a user question (a RAM↔speed pragma). drang has no GC of its own — it rides
Go's — and Go's GC is directly steerable, so this is a tiny addition: a single
`gc()` builtin (no CLI flag, by request).

- `gc("off" | "lean" | "normal" | "relaxed")` — friendly presets over Go's GOGC
  growth target (`off=-1`, `lean=20`, `normal=100`, `relaxed=400`).
- `gc(n)` — set the percent directly (advanced; negative disables GC).
- Returns the PREVIOUS percent, so a heavy phase can relax then restore:
  `$old := gc("relaxed"); …; gc($old)`.

Measured on 400k allocation-heavy iterations: `relaxed` ~1.09s vs `lean` ~1.46s —
**~25% faster** with less-frequent collection. Honest nuance: `off` can *backfire*
on a long churning loop (the heap balloons → cache/page pressure outweighs the GC
savings), so `relaxed` is the safer general "more RAM for speed" setting; `off`
shines for short runs and long-lived data. drang is GC-light by design anyway
(immutable shared strings, frozen top-level constants, unboxed scalars), so this is
a secondary lever — the register-VM rewrite remains the big speed item.

## 26. The register VM (2026-06-26) — architecture + build log

The "speed" pillar. A register-based bytecode VM that runs the *whole* language and
is verified byte-identical to the tree-walker, which stays the permanent parity
oracle. Architected via an Ultracode design workflow (instruction set, closures,
integration/parity strategy); the forks are engineering, not language-facing — the
language doesn't change — so they were decided, not polled.

**Architecture (decided):**
- **One choke point.** Every "call a function value" already funnels through
  `callFunction` (evalCall, all HOFs, `spawn`, `dispatch`). Give `*Function` an
  optional compiled `*Proto`; `callFunction` dispatches walker-vs-VM on it. HOFs,
  channels, copy-on-send, `Env.snapshot`, builtins — all untouched, because they
  only ever go through that one seam (`resolveAndCall`, extracted this slice).
- **Reuse the hard logic.** `arith`/`compare`/`equal` (and later
  `resolveContainer`/`assignSlot`) are opcode *runtime helpers*, not reimplemented,
  so the number tower, autoviv, and aliasing stay bit-identical.
- **`value.Value` is the register cell** — scalars stay unboxed (the CPython-beating
  property). Fixed-width `Instr{Op,A,B,C}`, 3-address. Big-`switch` dispatch with
  dense opcodes (Go has no computed-goto).
- **Closures via the retained `*Env`** in v1 (params/locals → registers, free vars →
  `Env` lookups) — lowest-risk for exact shadowing / per-iteration-scope / const
  semantics; upvalue cells are a later optimization.
- **`?` is an explicit typed return** (unwind the frame stack in the loop), not Go
  panic/recover. **Both backends behind a flag**, the full suite diffed through each.

**Build plan (9 steps):** 0 baseline VM · 1 scalars→registers · 2 control-flow jumps ·
3 error model · 4 calls/builtins/HOF interop · 5 closures+upvalue cells ·
6 per-iteration loop scope · 7 const · 8 goroutine-safety (`-race`) · 9 benchmark.

**Increment 1 — done & parity-verified.** `opcodes.go` / `compiler.go` / `vm.go`: a
working compiler + Env-backed register VM covering the expression core (literals,
arithmetic, concat, comparison, `-`/`!`, short-circuit `and`/`or` keeping the
operand), variables (`:=` / `::=` / `=`, Env-backed), control flow as jumps
(`if`/`else`/else-if, `while`, with `PushScope`/`PopScope` mirroring the walker's
fresh child per body/iteration), and calls (Ident callee via the shared seam — so a
VM frame already calls builtins, HOFs, `spawn`, and walker-run user functions).
Unsupported nodes (functions, lambdas, collections, `?`/`//`, compound/index assign,
`for`) cleanly fall the whole unit back to the walker. `TestVMCompilesSubset` asserts
the corpus compiles (no silent fallback); `TestVMParity` asserts VM output ==
walker output and identical error-vs-success across ~24 programs. Full suite green,
`-race` clean.

**Increment 2 — functions on the VM, done & parity-verified.** `*Function` carries an
optional `*Proto`; `callFunction` dispatches on it after the shared arity check.
`newFunction` compiles every function/lambda body when it can, so functions run on
the VM *regardless of which backend created them* — even a lambda passed to a
walker-run HOF executes on the VM. `OpMakeClosure` builds a closure from a nested
`FuncTemplate` capturing the current env; `return` and implicit last-expression
return both compile (a result register threads each statement's value). The spawn
worker copies the `Proto` so spawned functions run on the VM over their isolated
env snapshot. A `vmEnabled` flag lets the tests obtain a *pure* tree-walking oracle.
Result: the **entire ~150-test suite now executes every compilable function/lambda
on the VM** and still passes (and `-race` clean) — strong evidence the function path
is byte-identical. VM parity adds recursion (`fib`), early `return`, closures with
captured-variable mutation (`counter`), and lambdas through HOFs.

**Honest benchmark (the key finding).** Recursive `fib(28)`, register VM vs pure
walker: **1076 ms / 1.22 GB per op (VM) vs 1046 ms / 0.92 GB (walker)** — the VM is
~3% *slower* and allocates ~32% more. This is exactly the expected STEP-0 state: the
VM is still **Env-backed**, so every variable read/write is an `env.get`/`env.set`
map lookup (identical cost to the walker) *plus* a per-call register-frame
allocation — pure overhead with no offsetting win. The dispatch-loop saving over AST
traversal is real but small, and it's swamped by the unchanged map lookups. **The
speedup is entirely gated on the next increment: register-resident locals** — params
and non-captured locals indexed in registers (no map lookup, à la CPython's
LOAD_FAST vs dict access), with only *captured* variables left in the env. That
needs compile-time capture analysis (the design's "single trickiest correctness
point"), so it's its own carefully-verified step. Correctness first, then speed:
this increment delivered the first half.

**Increment 3 — register-resident locals + capture analysis. The speedup landed.**
A function is *register-eligible* when conservative capture analysis (`capture.go`)
shows none of its params/locals are reached by a nested closure, and it declares no
`const` and no nested named `fn`. Such a function compiles in **register mode**:
params occupy slots 0..n-1, a result slot follows, locals and temporaries above;
`$x` reads/writes are register ops, only free variables (globals/enclosing) hit the
env. Crucially it runs with **no per-call env and no per-block env at all** —
`vmCallFunction` preloads the args into the low registers and executes directly in
the captured env. Everything not eligible (closures' definers, `const` users,
nested-`fn` definers, the top-level program whose vars are globals) stays Env-backed
and correct. The soundness hinge: a capture-free function's locals are never visible
to a closure, so reusing a register slot across loop iterations is unobservable —
which is exactly why per-iteration freshness can be dropped here.

Capture analysis is deliberately conservative (over-approximates captures → at worst
a missed optimization, never a wrong result). Result on `fib(28)`, register VM vs
pure walker: **471 ms / 428 MB / 1.03 M allocs vs 1031 ms / 921 MB / 6.17 M allocs —
~2.2× faster, ~2.2× less memory, 6× fewer allocations.** The `$n` map lookups and
the per-call child env are gone. Full suite green (every eligible function across
~150 tests now runs register-mode), `-race` clean, and dedicated parity tests cover
block shadowing, loop-local reuse, mixed local/global access, and reassignment.

**Increment 3b — pooled register frames. Per-call allocation eliminated.** The one
remaining per-call cost was the register backing array (`make([]value.Value,
NumRegs)`). Now `vmRun` borrows a frame from a `sync.Pool` and returns it via an
open-coded `defer` (zero-alloc). The frame never escapes — the return value is
copied out and closures capture the env, not the registers — so reuse is sound; the
pool grows only to recursion depth and is goroutine-safe (pmap/spawn workers each
borrow their own, verified under `-race`). Get-time re-init (copy params, nil the
rest) also clears stale values, so no Put-time clearing is needed.

Result on `fib(28)`: **274 ms / 3.6 KB / 30 allocs** vs the walker's 924 ms / 921 MB
/ 6.17 M allocs — now **~3.4× faster** (up from 2.2×) with allocation reduced from
~1 per call to ~30 *total* for the whole run. Removing the GC pressure is what
bought the extra speed beyond saving the `make`. Full suite green, `-race` clean.

Next levers: cache call-target resolution (the by-name env lookup for the callee,
still ~2 map hits per `fib` call), then compile the error model (`?`/`//`) and
collections so more real code leaves the walker entirely.

**Increment 4 — error model + collection literals/reads.** Broadening VM coverage so
real glue code stays off the walker.
- **Error model.** `?` compiles to `OpPropagate`: an error unwinds as an `errSignal`
  out of `vmRun`, which `vmCallFunction` *catches* (→ the function's error result)
  while `RunProgramVM` lets *propagate* (→ top-level abort) — mirroring the walker's
  `callFunction`/top-level split with no in-function flag. `//` compiles to
  `OpJumpIfDefined` over the fallback (nil/error → fallback, else keep the value).
- **Collections.** `OpMakeArray`/`OpMakeMap`/`OpMakeRange` build literals from
  contiguous registers (map keeps the bare-identifier-key rule and the
  unhashable-key→`Err`; range keeps non-int-bounds→`Err`); `OpIndex`/`OpField`
  read via `indexRead`/`fieldRead`, value-based helpers extracted from
  `evalIndexRead`/`evalFieldRead` and shared with the walker so negative indices,
  out-of-bounds→`Err`, map miss→nil, and error pass-through are bit-identical.

Parity tests cover `?` (function-result and top-level abort), `//` recovery and
pass-through, literals, negative/OOB indexing, nested index, field-then-index, and
register-mode functions that index their params. A `stats(xs)` function (params,
locals, `while`, index reads, map-literal return) now runs **entirely** on the VM.
Full suite green, `-race` clean.

**Increment 5 — collection assignment + autovivification.** Single-level `$a[i] = x`,
`$m.f = x`, `$m[k] = x` and their compound forms (incl. the autovivifying
`$counts[k] += 1`), plus variable compound (`$x += 1`). Reuses the walker's
`assignSlot` and a shared `containerForWrite` helper, so number-tower, append, and
const interior mutability are bit-identical. The base resolves through one of two
opcodes depending on whether it's a register-local (`OpResolveLocalContainer`,
autoviv writes back to the slot) or env-resident (`OpResolveVarContainer`, autoviv
writes back to the env); `OpAssignSlot`/`OpCompoundLocal`/`OpCompoundVar` do the
write. Evaluation order matches `evalAssign` (rhs → key → container → assign).
*Nested* targets (`$a[i][j]`, the recursive `resolveContainer` path) still fall
back. A `histogram(xs)` (autoviv map, computed key, register mode) runs entirely on
the VM.

**Increment 6 — `for`-in.** A reified iterator (`forIter`) that **snapshots**
arrays/maps/strings (so body mutation can't disturb iteration, matching the walker)
but stays **lazy over ranges** — `for $i in 1..10_000_000` allocates nothing per
element (verified: summed to 5e13 with no materialization). Iterators live in a
pooled per-frame slice referenced by slot (recursion- and goroutine-safe).
`OpIterNew` builds one (non-iterable → abort, like the walker); `OpIterNext1`/`2`
advance and bind one/two loop vars or jump to the exit. Register mode reuses loop-
var slots across iterations (sound because register-eligible functions are
capture-free); Env mode binds fresh per iteration via `OpPushScope`/`OpDeclVar`, so
the canonical closure-in-loop case (`for $i in 1..3 { push($fns, ||$i) }` →
1,2,3) is correct — and parity-tested. All four iterables, one/two-var, snapshot
semantics, and register-mode `total`/`wordcount`/`dotproduct` all verified; full
suite green, `-race` clean.

With assignment and `for`-in compiled, essentially all of a typical glue script —
functions, closures, the error model, collection build/read/write, and loops — now
runs on the VM.

**Increment 7 — completeness. The whole language compiles.** Closed the last three
fallbacks:
- **Non-identifier callees** (`$f(x)`, `$fns[0](x)`, returned closures): `OpCallValue`
  evaluates the callee to a function value and calls it (args-first eval order,
  "cannot call a T" otherwise) — so stored/returned/indexed closures run on the VM.
- **Bare identifier-as-value** (point-free `$f := inc`): `OpGetIdent` does the
  sigil-blind `env.get`; it checks register-locals first, because `$x` and `x` share
  one namespace in the walker.
- **Nested slot assignment** (`$a[i][j]`, `$cfg.a.b.c = x`): a recursive
  `compileResolveContainer` mirrors the walker's `resolveContainer`, autovivifying
  each link, reusing `resolveSlot`/`assignSlot`. `OpResolveSlot` reuses its key
  register as output and lets a field name double as the parent's autoviv-kind hint
  to stay within three operands.

Result: **every AST node type now compiles** — no expression or statement falls
back. A `TestVMCompilesExamples` probe confirms both real scripts (`toolcheck.dr`,
`tasks.dr`) compile top-to-bottom with **zero** functions tree-walking. The CLI
(`RunProgramWithArgs`) now runs on the VM by default (walker kept as the fallback
for any future uncompilable node and as the test oracle); `toolcheck.dr` end-to-end
output is byte-identical before/after the switch. ~70 parity programs, the full
~150-test suite, and `-race` all green.

Measured speedups (register VM vs pure walker): **`fib(28)` 3.3×** (31 vs 6.17M
allocs), **realistic glue loop 1.74×** (44 vs 400k allocs — map autoviv +
compound + for-in fold). The remaining fallback is only the pathological assignment
base (`f()[0] = x`), which the walker errors on anyway.

**Increment 8 — three optimizations (all parity-verified, `-race` clean).**
- **Move elision** (`compileOperand`): binary/unary operands that are register-locals
  feed the op directly instead of via an `OpMove`. Safe because a register-local
  can't be mutated by an intervening evaluation (callees get their own frame;
  register-eligible functions are capture-free). *Measured impact: marginal on these
  workloads* — register-register moves were already cheap; instruction count isn't
  the bottleneck. Kept (tighter code, no downside).
- **Compare-branch fusion** (`OpJmpFalseLt`…`Ne`): an `if`/`while` condition that is
  a comparison compiles to one fused compare-and-jump instead of materializing a
  bool + a separate jump. Computes the *exact* comparison op and jumps on false (not
  the inverse), so NaN floats behave identically to the walker. *The biggest win:*
  fib 3.3×→3.9×, glue 1.74×→2.1× (the condition runs every iteration).
- **Direct call dispatch** (`OpCallBuiltin`): whole-program shadow analysis
  (`collectBoundNames`) finds names the program never binds; a call to such a
  builtin/HOF/special-form skips the `env.get` and dispatches straight to
  `dispatchNonUser`. Sound (an unshadowed name can't resolve to a user binding),
  `-race` clean (read-only Proto, no atomics). *Builtin-in-a-loop: 2.4× vs the
  walker.*

On the **user-function inline cache** (fib's recursive `env.get`): evaluated and
deliberately *deferred*. A correct cache needs a program-wide generation guard to
catch reassignment through ancestor scopes; that guard's atomic contention would
erode the parallel-scaling pillar, for a gain concentrated on recursion micro-
benchmarks rather than real glue. The wrong trade for this codebase.

Final speedups (register VM vs pure walker): **fib 3.9×, realistic glue 2.1×,
builtin-in-loop 2.4×**, with allocation reduced from millions per run to dozens.

**Increment 9 — const-immediate superinstructions.** `OpAddK`/`SubK`/`MulK`/`DivK`/
`ModK`/`ConcatK` and `OpCompoundLocalK` fold a literal operand into the op, removing
a `LoadConst` + a temporary per occurrence (`$n - 1`, `$i % 7`, `$i += 1`; commutative
`+`/`*` also fire when the constant is on the left). Comparisons are intentionally
omitted — condition comparisons already fuse via compare-branch (increment 8).
Read-only Proto, no atomics, parity- and `-race`-clean. Measured: **fib 3.9×→4.4×**
(its two const subtractions per call fold away); glue/builtin flat (allocation- and
call-bound, not arithmetic-bound) — an honest, targeted win, not a universal one.

Performance is now in a good place and aligned with the design: every gain has been
a pure, race-clean, parity-preserving bytecode transform. The remaining lever — a
contiguous per-goroutine register stack to retire the `sync.Pool` — is deliberately
**not** pursued: it would thread per-goroutine state through the shared call seam,
trading simplicity and parallel-safety margin for a single-thread gain. Against the
grain; left alone.

## 27. Build progress (2026-06-26) — displacement: porting the els task runner

The first real-world displacement: porting `_els`'s flagship Tcl task runner
(`tools/tasks.tcl`, ~300 lines) to a drang `tools/tasks.dr`. Chosen over abstract
feature work because the language is feature-complete and fast — value now comes
from carrying a real daily workload, not more engine.

**Scope finding (the honest correction).** A gap-analysis workflow proposed two
blocking primitives (exit-code-as-value via block-form `or {}`, and launch-detach).
Reading the actual code, only **one new orchestration primitive** was truly needed
plus a small path helper:
- **`start(cmd, args…)`** — launch a detached background/GUI child (the `exec … &`
  case for `z run`/`z colors`), returning its PID; stdio detached, handle released.
  Distinct from `spawn` (a drang function in a goroutine).
- **`cwd()`** — current working directory, for root discovery (the `z` front door
  runs from the project root; tasks.tcl used `[info script]`).

The proposed **exit-code-as-value primitive was NOT needed**: `run` already returns
an `Err` carrying the child's code, top-level `?` already aborts with it, and
`dispatch` already exits with a task's `result.ErrCode()`. Together with `//`
(ignore failure) and `fail()?` (raise), the existing error model expresses the
whole runner — `stream`/CHILDSTATUS, the `catch`-ignore steps, and the atomic-swap
fallback alike. Verifying against the source rather than building on the survey's
assumption saved a speculative language feature.

**The port.** All 14 tasks ported: `dispatch` table, toolchain discovery via
`cwd()`, a `child_env()` putting the toolchain bins on PATH (so gcc/tclsh/wish find
their DLLs) built fresh per call and mutated where a task needs an extra var, the
`stream`-style `run(tclsh, script, $args, {env})?` delegations, `start` for the GUI
launches, and the full native build pipeline (gcc compile/link/strip + genres +
windres + package + atomic staged-swap with the locked-output fallback). The
Tk-image/twapi/C-extension *internals* stay in their Tcl helper scripts, invoked as
subprocesses — a pure-Go console language can't host them, and shouldn't try.

**Verified against the real toolchain** (`C:\zmal\_els`): `z env` resolves paths +
probes gcc 16.1.0 / tcl 9.0.3 (via `capture` with `{stdin, env}`); `z toolcheck`
output is **byte-identical** to the Tcl runner with matching exit code; a failing
child propagates its exit code (`run` → `?` → `dispatch` → exit 7); an unknown task
exits 2. The drang runner is a drop-in for the orchestration spine; wiring `z.json`
to it is the user's switch to flip when ready. drang is now carrying real work.

## 28. Build progress (2026-06-26) — error inspection (the read side)

Closing the one real tool-invocation gap from the "is drang good at tool
invocation?" review: a script could *propagate* (`?`) or *recover from* (`//`) an
error, but couldn't *read* its details — so it couldn't branch on a command's
specific exit code (e.g. grep's 1-for-no-match vs 2-for-error) or parse a tool's
message. Three builtins close it, in the "errors are values you inspect" style
(no new syntax — block-form `or { … }` was rejected because `or` is now boolean):

- `is_err(x)` — is x an error value?
- `err_code(x)` — its exit code; **0 for a non-error**, so `err_code(run(cmd))`
  reads as "the exit code, 0 on success".
- `err_msg(x)` — its message; `""` for a non-error.

```
$r := capture("grep", $pat, $f)
if is_err($r) {
  if err_code($r) == 1 { say("no match") } else { say("grep:", err_msg($r)) }
} else { use($r) }
```

With this, the error model's read side is complete (propagate / recover / inspect),
and **tool invocation is genuinely complete** for branch-on-code and parse-stderr
patterns. Deliberately still deferred (advanced, not blockers): streaming
pipelines, incremental output, per-command timeouts, and `start`ed-process
management (`kill`/`wait`). Verified by unit + parity tests; `-race` clean.

## 29. Build progress (2026-06-26) — advanced orchestration

Pushing tool invocation from "good" to complete: the four items previously
deferred as "advanced, not blockers". All errors-as-values, shared options,
`-race` clean, and parity-preserving.

- **Timeouts** — `{timeout: <ms>}` on run/capture/pipe/each_line (via
  `exec.CommandContext`). A deadline hit is a catchable Err code 124 (like GNU
  `timeout`). Crucially it **kills the process TREE**, not just the direct child:
  `setTreeKill` uses `taskkill /F /T` on Windows (+ a 3s `WaitDelay` backstop), so
  a `cmd /c <spawner>` whose grandchild holds the stdout pipe can't keep the call
  blocked — found and fixed when a timeout test took 5s instead of 0.2s.
- **Pipelines** — `pipe([cmd,args], [cmd,args], …, {opts})` streams stage→stage
  through real OS pipes (no full-buffering between stages). Returns the last
  stage's trimmed stdout, or Err (127 cannot-start, 124 timeout, else the last
  stage's exit — bash's default pipeline semantics). stdin feeds stage 1.
- **Streaming output** — `each_line(cmd, args…, {opts}?, |$line| …)` invokes the
  callback per stdout line AS IT STREAMS (for build logs, tails), returning the
  exit status. A special form (not a map builtin) to avoid the callFunction init
  cycle.
- **Process handles** — `start` now returns a `Proc` (the process analogue of
  `Task`); a goroutine reaps the child and records its exit status. `await(proc)`
  yields it (await was extended to accept Task OR Proc — one "await any async
  handle"), `kill(proc)` terminates it, `pid(proc)` reads the PID. New value tag
  `Proc` (Equal-by-identity, DeepCopy-returns-self, like Task/Chan).

Also added `is_err`/`err_code`/`err_msg` (§28) so a script can branch on a
specific exit code or parse a tool's message. Tool invocation is now genuinely
complete: run/capture/pipe/start, stream or buffer, with cwd/env/stdin/timeout,
exit-code inspection, and process management — verified by unit + integration
tests (real child processes) under `-race`.

**Adversarial review (Ultracode workflow, 3 finders × per-finding verify, with real
child-process repros):** 6 raised, 5 confirmed (1 refuted), collapsing to three real
issues, all fixed:
- **each_line hung forever on a stdout line >4MB** (bug): `bufio.Scanner` stopped
  with `ErrTooLong` (indistinguishable from EOF), `scanner.Err()` was never checked,
  and the still-writing child blocked the final `Wait` with no backstop. Fixed:
  check `scanner.Err()` → `killTree` + reap + a distinct "token too long" Err, plus
  an unconditional `WaitDelay`. Was an indefinite hang + silent data loss; now a
  fast catchable Err.
- **kill/start used a bare `Process.Kill`** (smell): orphaned grandchildren, unlike
  the timeout path. Extracted a shared `killTree` (taskkill /T on Windows) now used
  by the timeout, each_line-abort, and `kill()` paths alike.
- **Negative `{timeout}` silently disabled the limit** (nit): now rejected (0 still
  means no limit), matching GNU `timeout`.
Refuted: a claimed each_line callback-abort hang (the reaper returns promptly).
Re-verified: full suite + `-race` clean after the fixes.

## 30. Build progress (2026-06-26) — runtime error locations (DX)

First of the "finish the language" items. Runtime errors now point at the source:
line, column, and a caret under the offending token.

```
drang: cannot use int and string with '*' (stringy coercion is a later slice)
  at build.dr:2:6
      $w * $h
         ^
```

Threading: every AST node embeds a `Pos{Line, Col}` (the parser stamps it — leaf
expressions at their token, infix at the operator, a Call at its callee, statements
at their first token via a single `parseStmt` hook). The compiler records a source
position per instruction (`Proto.Positions`, parallel to `Code`), updating a
`curPos` as it descends (keep-last-nonzero; re-asserted before a call emit). On an
aborting error, `vmRun` tags it via a deferred wrap with `Positions[ip-1]` into a
`posError` — explicitly skipping the control-flow signals (`returnSignal`/
`errSignal`) and already-positioned errors, so the type assertions that catch them
still work and the innermost (real) location wins across nested calls. The CLI
(`ErrorPos` + `reportRuntimeError`) renders the line and caret from the source.

Scope note: only the VM (the production path) positions errors; the tree-walker
(the test oracle + rare fallback) does not, and parity tests compare error-vs-
success, not messages — so they're unaffected. Cost: ~5 extra allocations per
*compile* (the Positions slices), zero per call — the deferred wrap stack-allocates
(fib stayed at ~35 allocs/run, no per-call regression). Errors inside a function
correctly point *into* the function body, not the call site. Verified by
`TestErrorPositions` + full suite + `-race`.

## 31. Build progress (2026-06-26) — string interpolation (the Perl soul)

The LOCKED-but-deferred headline ergonomic, now real: `"$name"` splices a variable
and `"${expr}"` any expression, with `\$` escaping a literal dollar.

```
say("Hello, $name! ${$n * $n} items, a literal \$n, nested ${ {a:7}.a }")
```

Design: interpolation is **parse-time desugaring to `~` concatenation** — no new AST
node, no eval/VM changes, so it works identically on both backends and composes
with everything (the VM compiles the resulting concat tree). The lexer now returns
the *raw* string body (just finding the close quote); the parser's `interpolate`
decodes escapes and interpolation *together*, which is what lets it distinguish a
literal `\$` from an interpolated `$x` (escape processing moved from lexer to
parser — safe, since escapes were only ever tested end-to-end). `$name` reads an
identifier; `${...}` is brace-matched (nesting- and string-aware) and sub-parsed as
a full expression. A leading `""` is prepended when the first piece is an
interpolation, so the result is always a string (every piece is `Display`'d via
`~`). Verified by parity tests on both backends, `-race`, and the example scripts
still compiling.

Two DX/ergonomics items down (error locations, interpolation). Remaining in the
track: `sort`/`sort_by` + the `<=>` comparator, `break`/`next`, `--version`/`--help`,
and an optional REPL.

## 32. Build progress (2026-06-26) — ordering: <=> and the sort family (DX)

The functional-toolkit ordering gap, closed. New comparator operator `<=>` plus
four ordering HOFs:

```
sort($xs)                          # natural ascending (numbers / strings)
sort($xs, |$a, $b| $b <=> $a)      # custom comparator (here: descending)
sort_by($people, |$p| $p.age)      # by a key (Schwartzian: keyfn runs O(n))
min_by($xs, |$x| $x.cost)          # element with the smallest key (undef if empty)
max_by($xs, |$x| $x.cost)          # element with the largest key
```

`<=>` returns -1/0/1 and is the natural building block for comparators. Its core is
`threeway(l, r)` — the same -1/0/1 computation that already backed `compare`, now
extracted and shared by `<=>`, the relational operators, and all four HOFs (one
ordering semantics everywhere). Wiring: a `SPACESHIP` token (`<=>` lexed by
extending the `<=` path), `equals`-level precedence (Ruby-style), an `OpSpaceship`
opcode, and `binOp`/`evalBinary` cases — so it runs on both backends.

The HOFs are evaluator special forms (they call user callbacks, like `map`), added
to `hofNames` so both backends dispatch them. `sort` is non-mutating (sorts a copy)
and stable. Error discipline follows the HOF convention: wrong arity aborts (Go
error); a type mismatch (sorting mixed types, a non-int comparator result) is a
catchable `Err` that composes with `//`; a comparator that propagates passes its
`Err` through. Verified by parity tests on both backends, `-race`, and full suite.

Three DX/ergonomics items down (error locations, interpolation, ordering).
Remaining: `break`/`next`, `--version`/`--help`, optional REPL.

## 33. Build progress (2026-06-26) — break / next (loop control)

The last real capability gap in the DX track. `break` exits the innermost loop;
`next` skips to its next iteration. Both work in `for` (over arrays, maps, ranges,
strings) and `while`, on both backends.

```
for $line in $lines {
  if blank($line) { next }
  if $line == "END" { break }
  process($line)
}
```

Three layers, each handling loop control in its own idiom:

- **Parser** — a `loopDepth` counter gates break/next: incremented around `while`/`for`
  bodies, **reset to 0 at every `fn`/lambda boundary** (so a break can't escape a
  function into a caller's loop) and restored after. break/next outside a loop is a
  *parse* error ("'break' outside a loop"), caught before execution — including a
  break inside a lambda passed to `map` inside a loop.
- **Walker** — `breakSignal`/`nextSignal` (like the existing `returnSignal`/`?`
  signals): the loop catches them (`break` stops, `next` continues); `callFunction`
  converts any that escape a function body into a hard error, never letting them
  cross a call boundary.
- **VM/compiler** — a loop-context stack records each loop's *continue target* (the
  `while` condition, or the `for` iterator-advance) and the `OpPushScope` nesting at
  loop entry. break/next emit `(currentDepth − loopDepth)` `OpPopScope`s **then** an
  `OpJump` (break → loop exit, back-patched; next → continue target). The scope-pop
  accounting is the subtle part: in Env mode every nested if/loop body pushes a
  runtime env scope, so jumping out must pop exactly the right number or the env
  chain corrupts. In register mode there are no runtime scopes, so zero pops. No new
  opcode — break/next reuse `OpPopScope` + `OpJump`.

Verified by walker-vs-VM parity tests (for/while, nested loops, all iterable types,
deep Env-mode nesting, register-mode functions), env-chain integrity after breaking
out of nested scopes, `-race`, and the full suite.

### 33a. Adversarial review of break/next (and two fixes)

A 6-reviewer adversarial workflow (3324 programs run, every finding independently
re-verified) stress-tested the feature. The hard parts held up: the Env-mode
scope-pop accounting survived ~3050 programs with no env-chain corruption, and the
walker-vs-VM parity agent found zero divergence across 62 adversarial programs
(including closures capturing per-iteration loop vars with `next`). It found two
real defects — both parser/lexer completeness, not VM logic — now fixed:

1. **Postfix modifiers didn't work on break/next.** `parseStmtDispatch` routed
   BREAK/NEXT straight to `parseLoopControl`, returning before `applyPostfix` (the
   path every other statement, including `return`, takes). So `break if $cond` /
   `next unless $cond` were parse errors. Fix: route through `applyPostfix`, like
   `return`. The idiomatic `break if`/`next if` now parse and behave as the block
   form.
2. **A bare break/next followed by a newline + statement was a parse error.** The
   lexer's `terminates()` (which decides when a newline becomes a statement
   terminator) listed `return` but omitted `break`/`next`, so the newline was
   swallowed and the next statement collided. Fix: add BREAK/NEXT to `terminates()`.

Regression coverage added: parity cases for both fixed forms (both backends) and a
`TestLoopControlParseGating` test asserting break/next are rejected outside a loop
(including inside a fn/lambda within a loop) and accepted in-loop with postfix
modifiers. Lesson logged: the block form was tested and claimed to cover postfix —
it didn't; the review caught the unverified claim.

That completes the loop-control item with both backends in agreement. Remaining in
the DX track: `--version`/`--help`, optional REPL.

## 34. Build progress (2026-06-26) — CLI hygiene: --version / --help

Standard CLI manners. `drang --version` (or `-V`) prints `drang 0.1` and exits 0;
`drang --help` (or `-h`) prints full usage and exits 0; bare `drang` prints a short
usage plus "try 'drang --help'" and exits 2. The version is a `var version`
(default "0.1", the first usable release) so a build can stamp it via
`-ldflags "-X main.version=..."`. The new flags are recognized in the existing
leading-flag loop, so `-e`, the mode flags, and `$ARGV` pass-through are unchanged.

Five DX/ergonomics items done: error locations, string interpolation, ordering,
loop control, CLI hygiene. The only remaining track item is an optional REPL.

## 35. Build progress (2026-06-26) — the REPL (DX track complete)

The last item. Running `drang` with no program on an interactive terminal starts a
read-eval-print loop — which is also what double-clicking the executable does (a
console app's stdin is a character device, so it's detected as interactive). Piped
input (`cat foo.dr | drang`) is instead read and run as a program; `--repl` forces
the loop regardless.

```
drang 0.1 — type 'exit' (or Ctrl+D / Ctrl+Z) to quit
drang> $x := 10
10
drang> fn sq($n) {
  ...> $n * $n
  ...> }
drang> sq($x)
100
drang> "answer is ${sq($x) - 58}"
answer is 42
```

Design: one persistent env across submissions (so vars/functions stick); a
submission whose only parse errors are unexpected-EOF (unclosed brace, dangling
operator) continues on a `...>` prompt; real parse/runtime errors are reported and
the buffer resets (the loop never dies); non-nil results are echoed via `Display`,
and a top-level `?`-propagated error echoes as its `Err` value rather than aborting.
Evaluation goes through a new `eval.EvalREPL` (tree-walker — the VM's parity oracle
— so results match normal execution; functions defined here still compile and run
on the VM when called). Interactive detection is `os.Stdin` being a char device
(stdlib only). The loop is factored over explicit reader/writer for a regression
test (`TestREPL`: persistence, multi-line, echo, parse-error recovery).

**DX/ergonomics track complete (6/6):** runtime error locations, string
interpolation, ordering (`<=>`/sort/sort_by/min_by/max_by), loop control
(break/next), CLI hygiene (`--version`/`--help`), and the REPL. The language is now
a genuinely usable daily driver. Deferred "Perl soul" lexer extras (regex literals,
qw//, q//qq//, heredocs) remain optional future work.

## 36. Build progress (2026-06-26) — Perl-soul quote operators + heredocs

Quote operators and heredocs — the deferred "Perl soul" text features.

```
qw(red green blue)            # -> ["red", "green", "blue"]
q{C:\Users\me\n literal}      # raw string: no interpolation, no escapes
qq{she said "hi", ${1 + 2}}   # interpolated, flexible delimiters (no quote-escaping)

$readme := <<~END             # heredoc; <<~ strips common indentation
    # $project
    version $ver
    END
```

Design: one lexer hook does the quote operators — after reading an identifier, if
it is `q`/`qq`/`qw` and is *immediately* followed by a delimiter, the body is read
to the matching close (`( [ {` nest by depth; `/ |` run to the next one; no
delimiter escaping by design — pick a non-conflicting or nesting delimiter).
`qw`→`QW` token (parser splits on whitespace into an array), `q`→`RAWSTR` (literal
StringLit), `qq`→`STRING` (reuses the existing interpolation path, so it behaves
exactly like `"..."`). Because the trigger requires an immediately-following
delimiter, `$qw` variables and identifiers like `queue` are unaffected.

Heredocs (`<<TAG`) read the following lines up to a line equal to `TAG`: `<<TAG`/
`<<"TAG"` interpolate, `<<'TAG'` is raw, `<<~TAG` strips the smallest common
indentation (relative indentation preserved). The opener must be last on its line;
after the terminator a NEWLINE is emitted so the statement closes and normal lexing
resumes. Interpolated bodies reuse the same `interpolate` path as `qq`/`"..."`.

Everything desugars to existing string/array AST, so walker-vs-VM parity holds for
free (added to the parity corpus). Unterminated quotes/heredocs error cleanly (no
hang). Regex literals stay deferred: drang's regex builtins take string patterns,
raw `q(\d+)` patterns avoid escape-doubling, and lenient string escapes already
make `"\d+"` a valid pattern — a `/.../`literal would add the slash-vs-division
ambiguity for little gain without a new compiled-regex value type.

### 36a. Adversarial review of quote operators + heredocs

A 5-reviewer adversarial pass (250 programs; walker-vs-VM parity agent found zero
divergence across 64 programs) surfaced two minor edge cases, both resolved as
documentation rather than behavior changes:

1. **Empty heredoc body is "" (not "\n").** This is correct — it matches Perl/Ruby
   and the line-based model (each body line carries a newline; zero lines → ""; one
   blank line → "\n"). The misleading "always a trailing newline" comments were
   corrected; a `TestQuoteHeredocEdges` test locks the behavior in.
2. **Brace-delimited `qq{ ${ "}" } }` mis-nests.** The lexer counts raw `{`/`}`
   bytes and isn't aware of strings/`${...}` inside a qq body, so a brace inside an
   interpolation closes the quote early. A proper fix would duplicate the parser's
   interpolation-aware brace matching into the lexer — not worth it for a narrow
   case with a trivial workaround (use a non-brace delimiter: `qq[ ${ "}" } ]`).
   Documented as a limitation.

Net: the quote/heredoc machinery is solid; no real defects, no parity issues.

## 37. Build progress (2026-06-26) — first-class regexes (the good parts of Perl, none of the warts)

Regexes are now a first-class value, deliberately taking Perl/Python/Go's good
ideas and skipping the criticized corners (Perl's `/`-ambiguity and magic capture
globals; JS's stateful/mutable RegExp):

```
$id := qr/[a-z]\w*/i          # compiled-regex literal, with flags
matches($s, $id)              # builtins accept a regex OR a string pattern
match("k=42", qr{(\w+)=(\d+)})# capture groups returned as an array (no $1 globals)
$re := re("$prefix\d+")      # re() compiles a dynamic pattern into a regex value
```

What avoids the warts:
- **Immutable value.** A `Regex` wraps Go's `*regexp.Regexp`, which is immutable and
  concurrency-safe, so the value is *shared* (its `DeepCopy` returns itself) and
  carries no per-match state — no JS `lastIndex` foot-gun. `pmap` hands the same
  compiled regex to every worker safely.
- **`qr/.../` syntax** via the quote-operator machinery — no `/`-vs-division
  ambiguity. Flags `i m s U` are baked in as Go inline flags (`qr/foo/i` → `(?i)foo`).
- **No magic globals** — `match` already returns captures as an explicit array.
- **Bad pattern → a catchable `Err`** (consistent with first-class errors), not a
  crash; an invalid flag is a clean parse error.

Why it fit the grain (the "stop if it fights the design" bar was never hit):
- `value.go` changed by **two lines**: a `Regex` tag and adding it to `Equal`'s
  delegating case. `Truthy` (heap default true), `DeepCopyValue` (generic), and
  `TypeName`/`Display` (delegate to the `Obj`) needed nothing — a regex slots in
  exactly like `Func`/`Proc`. `normalizeKey` already rejects it as a map key.
- A `regexObj` implements the existing `Obj` interface; equality is by source
  (flags included). `value` never imports `regexp`.
- A shared compile **cache** (`sync.Map`, race-safe) memoizes compilation — the
  "compile once" win for both `qr//` and string-pattern builtins.
- The VM gets one opcode, `OpMakeRegex` (same family as `OpMakeArray`/`OpMakeMap`),
  building the regex from a *string* constant at runtime via the cache — so the
  constant pool stays scalar (no `constKey` coupling) and is symmetric with the
  walker. Both backends call `makeRegex`, so parity holds.
- The dual "string or regex" acceptance is centralized in one `regexArg` helper, so
  the builtins don't each grow a branch.

Verified by walker-vs-VM parity cases, `TestRegexValue`, `-race`, the full suite,
and edge checks (pmap concurrency, map-key rejection, interpolation, truthiness).

## 38. Build progress (2026-06-26) — standalone executables (`drang build`)

`drang build <script.dr> [-o out]` compiles a script into a self-contained
executable. Mechanism: a self-hosting appended payload — drang copies its own
binary, appends the gzip-compressed source, and writes a 20-byte trailer
`[payloadLen u64][version u32][magic "DRANGsfx"]`. At startup drang inspects its
own tail (`embeddedProgram`/`extractPayload`); a present trailer means standalone
mode (run the embedded source, every arg → `$ARGV`), absent means the normal CLI.

Choices, against the `lana` prototype this was modeled on:
- One self-hosting binary (drang is both builder and runtime), not lana's three
  tools (`lanac`/`bundler`/`lana-run`) with a hand-passed runtime path.
- Embed source, not bytecode. drang has no `Proto` serializer, and one would also
  have to serialize the AST for walker-fallback functions — leaky and version-
  brittle. Parsing the embedded source costs well under a millisecond and startup
  is dominated by process init anyway; source-embed is robust across versions and
  far simpler. (A bytecode mode stays a future option behind the same trailer.)
- Build-time validation: `drang build` parses first and refuses to build a script
  that doesn't parse, so a built exe is guaranteed to load.
- A present-but-corrupt or incompatible trailer is a hard error, never a silent
  fall-through to CLI mode.
- Safe writes: the build refuses to overwrite the source script or the running
  interpreter, and writes to a temp file renamed into place, so a failed or
  mis-targeted build never truncates an existing file. (An adversarial review
  caught the original truncate-before-copy: `build x.dr -o x.dr` destroyed the
  source; on POSIX, `-o <the running drang>` would have corrupted the install.)

Cross-platform: trailing bytes after the image are ignored by the Windows and
Linux loaders, so standalones run there natively. macOS — especially Apple
Silicon — requires a valid (even ad-hoc) signature that appending invalidates, so
on darwin `drang build` best-effort ad-hoc-signs the output
(`codesign --force --sign -`) and, if that fails, prints the exact command to run.
`drang build` produces an executable for the OS it runs on (it copies the running
binary).

Verified by payload round-trip and atomic-write unit tests
(`TestStandalonePayloadRoundTrip`: valid / plain / corrupt / version-mismatch;
`TestWriteStandaloneRoundTrip`, `TestSameFile`) and end-to-end (build → run with
args, `$ARGV`, exit-code propagation, build-time rejection of invalid scripts,
same-file refusal with the source intact, pmap/subprocess inside a standalone);
`vet` + full `-race` suite green.

## 39. Build progress (2026-06-26) — JSON (`from_json` / `to_json`)

Two builtins binding Go's `encoding/json`: `from_json(s)` parses, `to_json(v,
indent?)` renders. Mapping: object↔map, array↔array, JSON number→int when integral
and in int64 range else float, true/false↔bool, null↔nil, string↔string.

Choices:
- Key order round-trips. `from_json` builds the `OrderedMap` by streaming
  `json.Decoder` tokens (not `Unmarshal` into a Go map, which randomizes), and
  `to_json` walks the map in insertion order. So parse→render preserves object key
  order — which matters for diffs and config tooling.
- int/float kept distinct via `dec.UseNumber()`: integral & int64-range → Int,
  else Float (integers beyond int64 fall back to Float, the language's only option).
- Errors as values: malformed input, trailing data, and non-encodable values
  (function, regex, range, channel, error, …) are catchable Err; only misuse
  (wrong arity/type, bad indent) aborts. NaN/Inf → Err (not valid JSON).
- Output is not HTML-escaped: `<`, `>`, `&` pass through (unlike `encoding/json`'s
  default), via a hand-rolled string escaper for quotes/backslash/control chars.
- Both parse and render recursion are depth-capped (`maxJSONDepth`), so deeply
  nested input or a cyclic structure yields a clean Err instead of a stack overflow
  — which Go's fatal stack-overflow would otherwise make unrecoverable.

An adversarial review found four issues, all fixed: (1, critical) the parse path
was initially uncapped, so a ~1.7 MB 850k-deep document hard-crashed the process
(`recover` can't catch a Go fatal stack-overflow); (2) invalid-UTF-8 strings (from
`read_file`/`capture` of binary data) were silently mangled to U+FFFD — now a
catchable Err; (3) integral floats render as `N.0` so they stay floats across a
round-trip; (4) the indent width is capped (`maxJSONIndent`) so a giant indent
can't exhaust memory.

Verified by `TestJSON` and `TestJSONEdgeCases` (round-trip, key-order, int/float,
pretty indent, HTML chars, malformed/trailing/non-encodable/deep-nesting/invalid-
UTF-8 → Err, integral-float stability), the manual's JSON section examples, and a
direct rerun of the original crash input (now an Err); full `-race` suite green.
