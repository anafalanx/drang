# drang: Language Manual

*A small, Perl-inspired scripting language for text processing, system glue, and orchestration, implemented in Go.*

*Covers drang 0.8.*

> Every code example in this manual was executed against the interpreter; the shown output is real.

This manual is the worked, example-driven guide. For a terse formal specification (the grammar, the semantics, and every builtin with exact signatures and error modes), see [REFERENCE.md](REFERENCE.md).

## Contents

- [Introduction](#introduction)
- [Lexical structure, declarations, types, and operators](#lexical-structure-declarations-types-and-operators)
- [Strings](#strings)
- [Control flow](#control-flow)
- [Functions, lambdas, closures, and pipelines](#functions-lambdas-closures-and-pipelines)
- [Arrays, maps, and the collection toolkit](#arrays-maps-and-the-collection-toolkit)
- [Errors as values](#errors-as-values)
- [Regular expressions](#regular-expressions)
- [External commands and concurrency](#external-commands-and-concurrency)
- [In-language concurrency](#in-language-concurrency)
- [Files and paths](#files-and-paths)
- [Persistent storage](#persistent-storage)
- [JSON](#json)
- [CSV](#csv)
- [Date and time](#date-and-time)
- [Hashing, encoding, and randomness](#hashing-encoding-and-randomness)
- [HTTP client](#http-client)
- [Task dispatch](#task-dispatch)
- [One-liner mode](#one-liner-mode)
- [Modules: `use`](#modules-use)
- [Testing](#testing)
- [Formatting](#formatting)
- [Not yet: known gaps and surprises](#not-yet-known-gaps-and-surprises)

---

## Introduction

drang is a small scripting language for text processing and system glue: the work of wrangling text, shelling out to other programs, and orchestrating small jobs. It is implemented in Go and is Windows-only.

Three commitments shape it.

**One sigil for all data.** Every variable wears a `$`, whether it holds a number, a string, an array, or a map: `$x` is `$x` regardless of what is inside it. There is no scalar-versus-list distinction and no punctuation-variable menagerie. Names carry their *kind* by sigil, not their type: `$` marks data, a leading `.` marks the functions you define, and a bare name marks a builtin. The three form separate namespaces (covered under Functions), so your `.split` and the builtin `split` never collide.

**Parallelism made safe by subtraction.** drang runs on real threads with no global lock, and it stays correct by removing the things that make shared-memory parallelism dangerous. Constants declared with `::=` are deeply frozen, strings are immutable, and there are no shared mutable globals reachable from a parallel worker. Data-parallel combinators like `pmap` therefore need no locks: each worker gets its own deep copy of the data it touches.

**Errors are values.** A failure is an ordinary value you can inspect (`is_err`, `err_msg`, `err_code`) or forward with a trailing `?`. There is no ambient error variable and nothing is thrown by default. Ignoring a failure is something you write on purpose, not something that happens behind your back.

The standard library is a curated binding over Go's own facilities (strings, files, process spawning, RE2 regex), not a reimplementation of them. Internally drang runs a tree-walking interpreter alongside a register bytecode VM held in lockstep with it, but that is invisible: the language behaves identically either way.

### Running programs

drang reads a program from one of four places.

A **file**, conventionally with a `.dr` extension:

```
drang app.dr
```

**Inline source**, passed with `-e`:

```
drang -e 'say("hello, world")'
```

```
hello, world
```

**Piped stdin.** When stdin is not a terminal, drang runs whatever arrives on it as the program, so a pipeline like `cat app.dr | drang` works:

```
echo 'say("from stdin")' | drang
```

```
from stdin
```

The **REPL.** Run `drang` with no program on an interactive terminal (also what launching the executable directly does), or force it with `--repl`. Bindings persist across submissions, and each entered expression prints its value:

```
drang 0.8 — type 'exit' (or Ctrl+D / Ctrl+Z) to quit
drang> $x := 21
21
drang> $x * 2
42
drang> exit
```

There is also a fifth path that produces no interpreter dependency at all. `drang build app.dr -o app.exe` compiles a script into a single self-contained Windows executable: the drang runtime with your source appended. Running it executes the embedded program, with its command-line arguments exposed as `$ARGV` exactly as for a normal script. The build refuses to overwrite the source or the running interpreter.

```
drang build greet.dr -o greet.exe
```

```
built greet.exe (8209998 bytes) from greet.dr
```

```
greet.exe world     # where greet.dr is: say($"hi, ${$ARGV[0]}")
```

```
hi, world
```

### Flags

Leading flags are consumed up to the first non-flag token, which is taken as the program. Everything after the program becomes script arguments, not flags, so a program can accept its own `--foo` options without drang intercepting them.

| Flag | Effect |
| --- | --- |
| `--run` | Run the program. This is the default and is rarely written. |
| `--ast` | Print the parsed syntax tree instead of running. |
| `--tokens` | Print the token stream instead of running. |
| `--repl` | Start the interactive REPL. |
| `--version`, `-V` | Print the version and exit. |
| `--help`, `-h` | Print usage and exit. |

`--ast` and `--tokens` are windows onto the front end, useful when a parse surprises you. The AST prints in a compact parenthesized form:

```
drang --ast -e 'say(1+2)'
```

```
# ast of <-e>
(call say (+ 1 2))
```

### Script arguments and the environment

Arguments that follow the program are the array `$ARGV`. The process environment is the map `$ENV`, keyed by the exact variable names the process was given.

```
drang -e 'say($ARGV[0], $ARGV[1])' foo bar
```

```
foo bar
```

```
drang -e 'say($ENV["FOO"])'     # with FOO=bar in the environment
```

```
bar
```

For anything beyond raw positionals, `parse_args` folds `$ARGV` into a single flat map. A bare `--flag` becomes `true`. A `--key=value` becomes the string `"value"`. A `--key value` pair also becomes a string, but only when `key` is listed in the optional second argument (the *value options*); otherwise `--key` is a boolean flag and `value` is treated as a positional. Leftover positionals collect, in order, under the reserved `"_"` key, which is always present even when empty.

```
drang -e '$o := parse_args($ARGV, ["out"]); say($o.out); say($o["_"])' --out=build x.dr y.dr
```

```
build
[x.dr, y.dr]
```

The separated form `--out build` yields the same map, because `out` was named as a value option:

```
drang -e '$o := parse_args($ARGV, ["out"]); say($o.out); say($o["_"])' --out build x.dr y.dr
```

```
build
[x.dr, y.dr]
```

`parse_args` is deliberately permissive: unknown options are not errors, and a repeated option keeps its last value. It aborts only on a malformed call (a non-array argv, or a non-string element), never on the content of the arguments themselves.

### A taste

Variables are declared with `:=`, a mutable lexical binding, or with `::=`, a frozen constant. Plain `=` reassigns an existing binding. Builtins are called with parentheses. A `$`-prefixed string literal interpolates a bare `$var`, or `${ expr }` for anything more involved. Data nests transparently, reached with `.` for map fields and `[]` for indices:

```drang
$d := {users: [{name: "ada"}, {name: "alan"}]}
say($d.users[1].name)
say($"count: ${len($d.users)}")
```

```
alan
count: 2
```

Your own subroutines are introduced with `fn` and carry the leading-dot sigil (`fn .name`, called as `.name`). They are ordinary first-class values, so they compose with the higher-order combinators (`map`, `filter`, `reduce`, and friends), which take `|params| body` lambdas. Loops are `for`-in over ranges, and a postfix `if` keeps one-liners tight:

```drang
$xs := [1, 2, 3, 4]
say(map(filter($xs, |$x| $x % 2 == 0), |$x| $x * $x))
for $n in 1..5 { say($n) if $n % 2 == 1 }
```

```
[4, 16]
1
3
5
```

And the payoff: counting words across several files at once, in parallel, forwarding any read failure with `?`, with no locks to take and no threads to manage. `pmap` runs the callback over a worker pool and returns results in input order; the frozen `::=` files list is safe to hand to every worker.

```drang
fn .wc($path) { len(split(trim(read_file($path)?), " ")) }
$files ::= ["a.txt", "b.txt"]
$counts := pmap($files, .wc)
say("total:", reduce($counts, 0, |$a, $b| $a + $b))
```

```
total: 5
```

---

## Lexical structure, declarations, types, and operators

This section covers the surface syntax: how a program breaks into statements, how you bind names, the value types, what counts as true, and the operator set. Every variable carries a leading `$` at every use.

### Comments

A `#` begins a comment that runs to the end of the line. There is no block-comment form; comment out a region line by line.

```drang
# a full-line comment
$x := 10   # a trailing comment
say($x)
```

```
10
```

### Statement termination

A newline ends a statement whenever the line *could* end there: when the previous token is one that can finish an expression (a literal, a `$var`, an identifier, a closing `)` `}` `]`, or a trailing `?`). Inside `(` or `[`, newlines are insignificant, so long calls, array literals, and pipelines wrap freely across lines. Inside `{ }` blocks and at top level, a newline terminates.

Because brackets suppress line breaks, you can lay an expression out over several lines:

```drang
$total := sum([
  1,
  2,
  3
])
say($total)
```

```
6
```

A `;` also separates statements, so several fit on one line:

```drang
$a := 1; $b := 2; say($a + $b)
```

```
3
```

### Declarations and assignment

Three operators cover binding. You introduce a name with `:=` (mutable) or `::=` (constant), and thereafter a plain `=` reassigns the existing binding.

```drang
$count := 0      # declare a mutable binding
$count = $count + 1
$pi ::= 3.14     # declare a constant binding
say($count, $pi)
```

```
1 3.14
```

The distinction is enforced strictly. `=` on a name that was never declared aborts, so a typo cannot silently create a new variable. And `=` on a constant aborts:

```drang
$k ::= 1
$k = 2
```

```
drang: cannot assign to constant $k
  at prog.dr:2:6
    $k = 2
         ^
```

#### The three sigil-namespaces

A drang name's *kind* lives in its sigil, present at every occurrence, so a name always announces what it refers to. There are three disjoint namespaces:

| Sigil | Refers to | Example |
|-------|-----------|---------|
| `$name` | data: variables and constants alike | `$count`, `$pi` |
| `.name` | a user-defined function you declared with `fn` | `.greet`, called `.greet(...)` |
| bare `name` | a builtin or standard-library function | `say`, `map`, `split` |

Because the three spaces never overlap, your `.split` and the builtin `split` coexist without clashing, and a future release adding a new builtin can never shadow code you already wrote. (You can, if you insist, bind a builtin's name as data: `$len := 99` makes `$len` the number, while the builtin `len` remains reachable as a bare word.) The Functions section covers `.name` in depth; here the point is only that `$` is the sigil for every value you store.

#### A constant is deeply immutable

`::=` does more than forbid rebinding the name. If the value is a container, the container and everything reachable through it is frozen. Mutating it is an error.

The mechanism of the error depends on how you attempt the mutation. Index or field assignment aborts the program:

```drang
$TABLE ::= {"a": 1, "b": 2}
$TABLE["c"] = 3
```

```
drang: cannot modify a frozen map
  at prog.dr:2:8
    $TABLE["c"] = 3
           ^
```

The mutating builtins `push`, `pop`, and `delete` instead return a *catchable* error value, which you can recover with `//` or inspect with `is_err`:

```drang
$NAMES ::= ["ana", "bo"]
say(is_err(push($NAMES, "cy")))
$safe := push($NAMES, "cy") // "push refused"
say($safe)
```

```
true
push refused
```

This is what makes a constant safe to share read-only across parallel workers (`pmap`, `spawn`) with no copying and no locks: a worker reads it freely, and an accidental write fails loudly instead of racing. Mutable `:=` containers are *not* frozen; sharing a mutable container into parallel callbacks and writing it is still a data race you must avoid by collecting each callback's return value instead.

Freezing follows the *object*, not the name. Binding an existing mutable container to a constant freezes that object, so every other name for it becomes read-only too:

```drang
$existing := [1, 2, 3]
$C ::= $existing
say(is_err(push($existing, 4)))   # $existing is frozen now
```

```
true
```

If you need the original to stay mutable, bind a fresh literal or a copy to the constant.

### Value types at a glance

| Type | Example literal / how you get one |
|------|-----------------------------------|
| `nil` | the absent value (a missing map key, a bare `return`); there is **no `nil` keyword** |
| `bool` | `true`, `false` |
| `int` | `42` (64-bit signed) |
| `float` | `3.5` (64-bit) |
| `string` | `"hello"` |
| `error` | from `fail("...")` and fallible builtins |
| `array` | `[1, 2, 3]` |
| `map` | `{"a": 1, "b": 2}` (insertion-ordered) |
| `range` | `1..5` (inclusive of both ends) |
| `function` | a lambda `\|$x\| $x * 2`, or `fn .name` referenced as `.name` |
| `regex` | `re("[0-9]+")` or a `qr/.../` literal |

(The concurrency section adds channel, task, and process handles.) `type(x)` reports the tag name of any value.

```drang
say(type(true), type(42), type(3.5), type("hi"))
say(type([1,2]), type({"a":1}), type(1..5))
say(type(|$x| $x), type(re("[0-9]+")), type(fail("x")))
```

```
bool int float string
array map range
function regex error
```

`nil` is a real runtime value, but it has no source literal. You cannot write it. It only *arises*, most often from an absent map key. Attempting to write it is an error:

```drang
$m := {}
say($m["absent"])   # nil arises from the miss
say(nil)            # but the keyword does not exist
```

```
nil
drang: undefined: nil
  at prog.dr:3:5
    say(nil)
        ^
```

One display quirk to internalize now: a whole-valued float prints identically to an int. `say` renders `3.0` as `3`, so a float result can look like an int on screen. Use `type(x)` when the distinction matters (the `/` operator below is the common way to end up holding an int-looking float).

```drang
say(42)
say([1, 2, 3])
say({"a": 1, "b": 2})
say(1..3)
```

```
42
[1, 2, 3]
{a: 1, b: 2}
1..3
```

### Truthiness

Falsy is a short, fixed list: `nil`, `false`, `0`, `0.0`, `""`, and **empty** containers (`[]`, `{}`, and an empty or reversed range). Everything else is truthy: non-empty containers, functions, regexes, the string `"0"`, and error values.

```drang
fn .t($v) { if $v { say("truthy") } else { say("falsy") } }
$m := {}
.t($m["missing"]); .t(false); .t(0); .t(0.0); .t(""); .t([]); .t({})
.t(true); .t(1); .t("0"); .t([1]); .t(5..1); .t(1..5)
```

```
falsy
falsy
falsy
falsy
falsy
falsy
falsy
truthy
truthy
truthy
truthy
falsy
truthy
```

Note the two that catch people out: the string `"0"` is truthy (it is a non-empty string), and a reversed range like `5..1` is falsy (it is empty).

An error value is truthy, so an `if` on it takes the true branch even when the operation failed. Never test for failure with bare truthiness; test with `is_err`:

```drang
$e := fail("boom")
if $e { say("err is truthy") }
say(is_err($e))
```

```
err is truthy
true
```

### Operators

#### Arithmetic: `+ - * / %`

With two ints, `+ - * %` produce an int. Two rules will surprise you, both deliberate.

First, `/` between two ints yields a **float**, always. There is no integer-division operator. For a truncated integer quotient, wrap with `int(...)` or use the `div` builtin.

```drang
say(7 + 2, 7 - 2, 7 * 2, 7 % 2)   # ints in, ints out
say(7 / 2)                        # / is always float division
say(int(7 / 2))                   # truncate back to an int
say(type(6 / 2), 6 / 2)           # even a whole result is a float
```

```
9 5 14 1
3.5
3
float 3
```

Second, integer overflow **aborts** the program. It does not wrap and it does not silently promote to float. If you want float behavior, opt in with `float(...)`.

```drang
say(9223372036854775807 + 1)
```

```
drang: integer overflow: 9223372036854775807 + 1
  at prog.dr:1:5
    say(9223372036854775807 + 1)
        ^
```

`%` requires integer operands; a float operand aborts. Division or modulo by zero aborts. And arithmetic operators do not coerce types at all: a mixed or non-numeric operand aborts. There is no automatic string-to-number promotion.

```drang
say("a" + "b")
```

```
drang: cannot use string and string with '+' (no automatic coercion: convert with int()/float()/str(), or ~ to join strings)
  at prog.dr:1:5
    say("a" + "b")
        ^
```

These aborts are the operator policy and are distinct from the builtin convention. A builtin handed the wrong *type* returns a catchable error you can recover; an operator handed the wrong type ends the program. If you need a recoverable divide, the `div` builtin gives you one: `div(1, 0)` is a catchable error, whereas the `/` operator on zero aborts.

#### String concatenation: `~`

`~` joins strings. It is the only string-join operator; `+` will not do it.

```drang
say("foo" ~ "bar" ~ "!")
```

```
foobar!
```

#### Comparison: `== != < <= > >=` and the spaceship `<=>`

Numbers compare numerically, strings lexicographically. Numeric equality crosses the int/float line: `1 == 1.0` is true. The spaceship `<=>` returns `-1`, `0`, or `1`, and works over numbers and strings alike.

```drang
say(1 < 2, 2 <= 2, "a" < "b", 1 == 1.0)
say(1 <=> 2, 2 <=> 2, 3 <=> 2, "b" <=> "a")
```

```
true true true true
-1 0 1 1
```

Comparing incompatible types aborts; it is not a catchable error. `1 < "a"` ends the program with `cannot compare int and string`. Compare only within a type.

#### Logical: `and`, `or`, `not` (and `!`)

`and` and `or` short-circuit, so the right side is skipped when the left already decides the result. They are also **value-returning**: they yield one of their operands, not a coerced bool. `0 or 5` is `5`; `7 and 9` is `9`. `not` (and its synonym `!`) always yields a bool.

```drang
fn .boom() { say("boom ran"); true }
say(false and .boom())    # right side skipped
say(true or .boom())      # right side skipped
say(0 or 5, 7 and 9)      # operators return an operand
say(!true, not false)
```

```
false
true
5 9
false true
```

`boom ran` never prints because both calls short-circuit. `not` and `!` are *tight prefix* operators: `not 1 == 2` parses as `(not 1) == 2`, which is `false == 2`, so it prints `false`. Parenthesize when you mean to negate the comparison: `not (1 == 2)`.

#### Defined-or: `//`

`expr // fallback` yields the fallback only when the left side is **nil or an error**. Every other value, including the falsy `0`, `""`, `false`, and `[]`, is a real result and passes straight through. This makes `//` the tool for supplying a default without swallowing legitimate falsy values. The right side is evaluated eagerly, so it defaults a value; it does not guard an expensive call (use `if` for that).

```drang
$m := {"port": 0}
say($m["host"] // "localhost")   # key absent -> nil -> fallback
say($m["port"] // 8080)          # present and 0 -> the 0 stands
say(fail("x") // "recovered")    # error -> fallback
```

```
localhost
0
recovered
```

#### Pipeline: `|>`

`x |> f()` feeds `x` in as the first argument of the call on the right, so `x |> f(a)` means `f(x, a)`. A bare callable on the right is invoked with `x` alone: `x |> f` is `f(x)`. Pipelines chain left to right and read as a data-transformation sequence.

```drang
fn .double($x) { $x * 2 }
say(5 |> .double())
say([3, 1, 2] |> sort() |> reverse())
```

```
10
[3, 2, 1]
```

`//` is looser than `|>`, so `$x // "n/a" |> upper()` groups as `$x // ("n/a" |> upper())`.

#### Error propagation: `?`

A postfix `?` on an expression that produces an error short-circuits that error out to the nearest enclosing function boundary, where it becomes an ordinary error value the caller receives. On a non-error value, `?` is transparent and the value flows on. This lets a function thread failures outward without a chain of `if is_err` checks.

```drang
fn .parse_double($s) {
  $n := int($s)?     # on failure, return the error from .parse_double
  $n * 2
}
say(.parse_double("21"))
say(is_err(.parse_double("nope")))
```

```
42
true
```

A `?` that fires at top level (outside any function) aborts the program with the error's message and exit code.

#### Compound assignment: `+= -= *= /= %= ~= //=`

Each works on any assignable target: `$x`, `$a[i]`, or `$m.k`. `/=` inherits `/`'s float rule, so `$n /= 2` leaves `$n` a float.

```drang
$n := 10
$n += 5   # 15
$n -= 2   # 13
$n *= 3   # 39
$n /= 2   # -> float
say($n)
```

```
19.5
```

`~=` appends via string concatenation. `//=` is defined-or *in place*: it replaces the target only when the target is currently nil or an error, reading as "default this if unset." Because `//` treats only nil and error as absent, `//=` leaves a `0` or `""` in place.

```drang
$msg := "line"
$msg ~= "!"                 # "line!"
$cfg := {}
$cfg.host //= "localhost"   # key absent -> set it
$cfg.host //= "other"       # already set -> unchanged
say($msg, $cfg.host)
```

```
line! localhost
```

On a missing array or map slot, the numeric compound operators seed with `0` and `~=` seeds with `""`, so `$counts[k] += 1` and `$groups[k] ~= item` work on a key that does not yet exist:

```drang
$counts := {}
$counts["x"] += 1
$counts["x"] += 1
$groups := {}
$groups["a"] ~= "item"
say($counts, $groups)
```

```
{x: 2} {a: item}
```

#### Ranges: `lo..hi`

A range is inclusive of both ends.

```drang
say(len(1..5))
```

```
5
```

### What is *not* in the language

These are deliberate omissions. Each is a parse error, not a gap you can work around with syntax.

- **No ternary `?:`.** Use `if`/`else`.
- **No exponent `**`.** Use the `pow` builtin.
- **No bitwise operators** (`&`, `|`, `^`, `<<`, `>>`).
- **No increment or decrement** (`++`, `--`). Use `+= 1` and `-= 1`.
- **No inline regex operators** (`=~`, `s///`) and **no `$1`..`$n` capture variables.** Regex work goes through `qr/.../` or `re(...)` literals and the `match`, `match_all`, `matches`, and `replace_all` builtins. `match` returns an *array*, `[full, group1, group2, ...]`; `match_all` returns the full matches only. There is no magic named-capture map and no numbered capture variable, which keeps the three-sigil model clean: a `$1` would need a fourth kind of name.

```drang
$m := match("2026-07-04", qr/(\d+)-(\d+)-(\d+)/)
say($m)
say(type($m))
```

```
[2026-07-04, 2026, 07, 04]
array
```

The absent operators produce parse errors, caught before the program runs:

```drang
say(2 ** 3)
```

```
# parse errors in prog.dr
line 1: unexpected STAR "*"
line 1: expected end of statement, got INT "3"
```

```drang
$x := 1
$x++
```

```
# parse errors in prog.dr
line 2: unexpected PLUS "+"
```

## Strings

drang strings are UTF-8 text. The everyday form is a double-quoted literal. It processes a small set of escapes and does **not** interpolate: a `$` inside `"..."` is a plain dollar sign. Interpolation is opt-in. You request it with a `$`-prefixed form (`$"..."`, `$qq{...}`, or a `<<$TAG` heredoc), and only then are `$name` and `${expr}` spliced. Single-quoted `'...'` strings are raw. A family of quote operators and heredocs covers the raw, escaped, interpolated, and word-list variants with your choice of delimiter.

### String literals and the lenient escape policy

Inside `"..."`, exactly six escapes are decoded: `\n`, `\t`, `\r`, `\\`, `\"`, and `\$`. Any other backslash sequence is **left intact**. The backslash and the character after it are kept verbatim. This lenient policy is deliberate. Regex character classes and Windows paths become far less painful when you do not have to double every backslash.

Because `"..."` does not interpolate, a literal dollar needs no escaping. It just works:

```drang
say("price is $5")
say("$x is not a variable here")
```

```
price is $5
$x is not a variable here
```

An unknown escape survives untouched, ready to hand straight to a regex builtin:

```drang
say("a\tb\nc")
say("\d+")
```

```
a	b
c
\d+
```

`\t` and `\n` decoded, but `\d` passed through as the two characters `\` and `d`.

This lenience has one trap on Windows paths. `\n`, `\t`, and `\r` remain real escapes, so a path segment that begins with `n`, `t`, or `r` gets mangled:

```drang
say("C:\dir\new")
```

```
C:\dir
ew
```

`\d` stayed literal, but `\new` decoded to a backslash, a newline, and `ew`. For paths, use a raw string (`'...'` or `q{...}`, below) or assemble the path with `path_join`.

The decoded escapes in full:

| Escape | Result |
|---|---|
| `\n` | newline |
| `\t` | tab |
| `\r` | carriage return |
| `\\` | backslash |
| `\"` | double quote |
| `\$` | dollar sign |
| any other `\x` | kept verbatim (`\` then `x`) |

`\$` decodes to a literal `$`. In a `"..."` string the `$` is already literal, so `\$` and a bare `$` mean the same thing there. `\$` earns its place in the interpolating forms below, where it suppresses interpolation. This same escape table governs `"..."`, `qq{...}`, the `<<TAG` and `<<"TAG"` heredocs, and their interpolating counterparts `$"..."`, `$qq{...}`, and `<<$TAG`. The raw forms (`'...'`, `q{...}`, `<<'TAG'`) decode nothing at all.

### Interpolation (opt-in)

A plain `"..."` never interpolates. To splice variables and expressions into a string, prefix the literal with `$`. A `$"..."` string is both escaped and interpolated. Inside it, `$name` splices a variable's value and `${expr}` splices any expression. Escape a literal dollar with `\$`.

```drang
$x := 42
say("x is $x")
say($"x is $x")
say($"sum=${$x + 4}")
say($"\$x stays literal, $x splices")
```

```
x is $x
x is 42
sum=46
$x stays literal, 42 splices
```

The first line is the point to internalize: the bare `"x is $x"` printed the dollar literally, and only the `$`-prefixed form interpolated.

A `${...}` body accepts arithmetic, calls, and indexing:

```drang
$a := [10, 20, 30]
say($"second is ${$a[1]}")
```

```
second is 20
```

One limitation is worth knowing before it surprises you. A `${...}` body cannot contain a double-quoted string while it sits inside a `$"..."` literal. The nested `"` breaks the brace matching, and you get a parse error:

```
line 1: unterminated ${...} in string
```

When the interpolated expression needs a string literal of its own, switch delimiters and use `$qq{...}`:

```drang
say($qq{up is ${upper("hi")}})
```

```
up is HI
```

### Raw strings (`'...'`)

A single-quoted `'...'` string is **raw**. No escapes, no interpolation, every byte kept verbatim, and it may span newlines. It is an exact alias of `q{...}` (below) and the clean choice for Windows paths and regex patterns:

```drang
say('a $b and \n stay literal')
say('C:\Users\new\tmp')
```

```
a $b and \n stay literal
C:\Users\new\tmp
```

A `'...'` string has no escape for its own delimiter, so it cannot contain a single quote. When the body needs one, use `q{...}` (or `q(...)`, `q[...]`) instead.

### Quote operators

Four quote operators let you sidestep escaping gymnastics by choosing a delimiter the body avoids. The delimiter follows the operator keyword with **no space** between them; if a space intervenes, the keyword is read as a plain identifier. The allowed delimiters are `(`, `[`, `{`, `/`, and `|`. The paired ones (`()`, `[]`, `{}`) **nest**; `/` and `|` run to the next matching character.

- **`q{...}`** is raw: no interpolation and no escape processing at all, identical to `'...'`.
- **`qq{...}`** is escaped with no interpolation, the same rules as `"..."` but with flexible delimiters.
- **`$qq{...}`** is escaped and interpolated, the opt-in interpolating form of `qq` (the counterpart of `$"..."`). Only `qq` takes the `$` prefix; `$q`, `$qw`, and `$qr` are rejected.
- **`qw{...}`** splits its body on whitespace and produces an **array** of words.

```drang
$x := 9
say(q{no $x interp, \n stays literal})
say(qq{x=$x stays literal, a \t tab})
say($qq{x=$x interpolates})
say(qw{red green blue})
```

```
no $x interp, \n stays literal
x=$x stays literal, a 	 tab
x=9 interpolates
[red, green, blue]
```

`q{...}` is the tidy way to write a Windows path or a backslash-heavy regex:

```drang
say(q(C:\Users\new\tmp))
```

```
C:\Users\new\tmp
```

Paired delimiters nest, and the `$` opt-in works with any delimiter:

```drang
say(q{outer {inner} done})
say($qq|x is ${3 + 4}|)
```

```
outer {inner} done
x is 7
```

`qw{...}` yields a genuine array. It splits on runs of whitespace and works with every array tool:

```drang
$w := qw{one  two   three}
say(len($w))
say($w[1])
say(join(qw{a b c}, "+"))
```

```
3
two
a+b+c
```

The body of any quote operator is taken literally, with no backslash escape for the delimiter itself. Choose a delimiter the content does not use, or use a nesting paired delimiter.

### Heredocs

A heredoc opens with `<<TAG` and runs across the following lines until a line equal to `TAG`. The opener **must be the last token on its line**. Heredocs mirror the quote forms, and interpolation is opt-in here too, signalled by a `$` on the tag:

- **`<<END`** and **`<<"END"`** are escaped with no interpolation (like `"..."` and `qq`).
- **`<<$END`** is escaped and interpolated (like `$"..."` and `$qq`).
- **`<<'END'`** is raw, with no escapes or interpolation (like `'...'` and `q`).
- **`<<~END`** strips the common leading indentation of the body, and the terminator may be indented to match. It combines with any of the above, so `<<~$END` is a dedented interpolating heredoc.

```drang
$name := "world"
$msg := <<$END
Hello, $name!
Sum is ${2 + 3}.
END
say($msg)

$lit := <<END
Literal $name here, no interp.
END
say($lit)

$raw := <<'END'
Literal $name and \n here.
END
say($raw)
```

```
Hello, world!
Sum is 5.

Literal $name here, no interp.

Literal $name and \n here.

```

A non-empty heredoc body keeps its trailing newline, which is why a blank line follows each block above.

The dedenting form removes the smallest shared indent and preserves any indentation beyond it:

```drang
$body := <<~END
    line one
      line two (extra indent)
    line three
    END
say($body)
```

```
line one
  line two (extra indent)
line three
```

### Indexing and slicing

Strings index and slice **by rune**, that is, by Unicode code point rather than by byte, so multi-byte characters are handled correctly. `s[i]` returns the i-th character as a one-character string. A negative index counts from the end. An out-of-range index produces a catchable error value that you can recover with `//`:

```drang
say("hello"[0])        # h
say("hello"[-1])       # o
say("héllo"[1])        # é   (rune-aware, not a byte)
say("hello"[1..3])     # ell (inclusive range)
say("héllo"[0..1])     # hé
say("hi"[9] // "?")    # ?   (out of range, recovered)
```

`s[lo..hi]` is a substring over an **inclusive** rune range. Negatives count from the end, out-of-range bounds clamp, and an empty or reversed range yields the empty string:

```drang
say("[" ~ "hello"[3..1] ~ "]")     # []  (reversed)
say("[" ~ "hello"[10..20] ~ "]")   # []  (past the end)
say("héllo"[-2..-1])               # lo
```

Two things to keep in mind. First, indexing is by code point, not by grapheme cluster: a character assembled from several code points (a base letter plus a combining mark, a flag, or a ZWJ emoji sequence) spans more than one index. Second, indexing reads only. `s[i] = ...` is not a way to mutate a character in place; attempting it aborts the program:

```
drang: cannot index-assign through $s (a string)
```

Build a new string instead.

### String builtins

A note on failure that applies throughout: passing a builtin the **wrong number of arguments** aborts the program with a nonzero exit, and this cannot be caught. Passing the **wrong argument type** (or hitting a runtime failure) returns a first-class error value that you can recover with `//` or propagate with `?`.

| Builtin | Signature | Notes |
|---|---|---|
| `upper` / `lower` | `(s)` | Unicode case fold |
| `trim` | `(s, cutset?)` | trims whitespace, or any character in the given cutset, from both ends |
| `split` | `(s, sep?)` | no `sep`: split on whitespace runs (ends stripped); `""`: split into runes; otherwise split on the literal `sep` |
| `join` | `(array, sep?)` | renders each element and joins with `sep` (default `""`); array-only |
| `replace_first` / `replace_all` | `(s, needle, repl)` | replace the first, or every, occurrence; a string `needle` is a literal, a `qr//` regex matches as a pattern (see the regex chapter) |
| `contains` | `(s, needle)` | substring test; also tests array membership |
| `starts_with` / `ends_with` | `(s, affix)` | boolean |
| `find_index` | `(s, needle)` | rune index of the first occurrence, or `-1` if absent (empty needle gives `0`) |
| `repeat` | `(s, n)` | `n` copies; `n` must be an int, and a negative `n` is an error |
| `chars` | `(s)` | array of single-rune strings |
| `lines` | `(s)` | normalizes CRLF to LF, drops one trailing newline; `""` gives `[]` |
| `format` | `(template, args...)` | `{}` placeholders; counts must match |

```drang
say(upper("Hi"))
say("[" ~ trim("  hi  ") ~ "]")
say(trim("xxhix", "x"))
say(split("a b  c"))
say(split("a,b,c", ","))
say(join(["a", "b", "c"], "-"))
say(replace_all("a.b.c", ".", "-"))
say(contains("hello", "ell"))
say(starts_with("foobar", "foo"))
say(ends_with("foobar", "bar"))
say(find_index("hello world", "world"))
say(repeat("ab", 3))
say(chars("héy"))
say(lines("a\nb\nc\n"))
```

```
HI
[hi]
hi
[a, b, c]
[a, b, c]
a-b-c
a-b-c
true
true
true
6
ababab
[h, é, y]
[a, b, c]
```

`join` is array-only. It renders each element through its display form and joins the pieces with `sep`, so non-string elements are stringified rather than rejected:

```drang
say(join([1, 2, 3], ", "))   # 1, 2, 3
say(join([1, 2, 3]))         # 123
```

To assemble filesystem **path** segments, use `path_join(seg, ...)` from the filesystem section, not `join`. These were a single polymorphic `join` before the pre-1.0 split. A path-join written as `join(...)` now fails loudly with a message that points you at `path_join`.

### `format` and its placeholders

`format` substitutes each `{}` placeholder with the next argument. Write `{{` and `}}` for literal braces.

```drang
say(format("{} + {} = {}", 2, 3, 5))
say(format("set {{x}} to {}", 9))
```

```
2 + 3 = 5
set {x} to 9
```

#### Format specs: `{:spec}`

A placeholder may carry a **format spec** after a colon (`{:spec}`) to control width, alignment, precision, sign, and number base. The grammar is a compact subset:

```
{:[[fill]align][sign][#][0][width][.precision][type]}
```

- **align**: `<` left, `>` right, `^` center. Numbers default to right, everything else to left.
- **fill**: any character placed *before* an align char (`{:*^10}` centers with `*`); the default is a space.
- **sign**: `+` shows a sign on positive numbers too; a space reserves a column for it.
- **`#`**: alternate form, adding the `0x` / `0o` / `0b` prefix for the `x` / `o` / `b` types.
- **`0`**: sign-aware zero-padding to the field width.
- **width** and **`.precision`**: the minimum field width, and the decimal places (for floats) or maximum length (for strings).
- **type**: `d` int; `b` / `o` / `x` / `X` binary/octal/hex; `f` / `e` / `g` (and `F` / `E` / `G`) float; `s` string; `%` percent (multiplies by 100 and appends a `%`).

```drang
say(format("{:.2f}", 3.14159))      # fixed decimals
say(format("[{:>8}]", "hi"))        # right-align in a field of 8
say(format("[{:*^9}]", "hi"))       # center with '*' fill
say(format("{:08.2f}", -3.1))       # sign-aware zero pad
say(format("{:#x}", 255))           # hex with 0x prefix
say(format("{:+d}", 42))            # forced sign
say(format("{:.1%}", 0.1234))       # percent
```

```
3.14
[      hi]
[***hi****]
-0003.10
0xff
+42
12.3%
```

A spec that does not fit its value returns a catchable error rather than aborting. Applying `{:d}` to a string, for instance:

```drang
say(format("{:d}", "hi"))
```

```
error: format: format type "d" needs an int, got string
```

The number of `{}` placeholders must equal the number of arguments. When they differ, `format` returns an **error value** rather than silently dropping arguments or emitting literal braces. This is a deliberate guard, and it also catches the reflex of reaching for percent-style verbs, which `format` does not use:

```drang
say(format("{} and {}", 1))
say(format("%s", 5))
```

```
error: format: template has 2 placeholder(s) but got 1 argument(s)
error: format: template has 0 placeholder(s) but got 1 argument(s). format uses {} / {:spec} placeholders, not %-style verbs (example: format("{} {:.2f}", name, x))
```

The result of each of these is an ordinary error value, so the program does not crash. It renders here because `say` was handed the error directly; in real code the value propagates through `?` like any other drang error. See the error-handling section for the full picture.

## Control flow

Control flow in drang is built from *statements*, not expressions. `if`, `while`, and `for` produce no value, so you cannot bind one to a variable or drop one into the middle of an expression:

```drang
$x := if 1 { 2 } else { 3 }
```

```
# parse errors in ex1.dr
line 1: unexpected IF "if"
line 1: expected end of statement, got INT "1"
```

If you came expecting `if` to be an expression that yields its taken branch, adjust: assign inside each branch instead. There is one place the value still flows out. A function returns the value of its last *statement*, and a block (including an `if`/`else`) in tail position hands out the value of whichever branch it took. See [Implicit and explicit return](#implicit-and-explicit-return). The restriction is narrow: you may not use `if` *inline*, part-way through an expression.

### if / else

`if cond { ... }` runs its block when the condition is truthy. An optional `else` block, or an `else if` chain, handles the rest. Two things differ from what you may reach for by habit: the condition is bare, with no surrounding parentheses, and the braces are always required. There is no braceless single-statement form.

```drang
$g := 75
if $g >= 90 { say("A") } else if $g >= 70 { say("B") } else { say("C") }
```

```
B
```

The `else` may sit on the same line as the closing `}` or drop to the next line. Both parse:

```drang
$g := 40
if $g >= 70 { say("pass") }
else { say("fail") }
```

```
fail
```

One caution when writing conditions: an `error` value is truthy. `if risky() { ... }` takes the true branch even when `risky()` failed, because the failure is carried in a truthy error value rather than signalled by falsiness. Test for failure with `is_err(x)`, never with a bare `if`.

### unless

`unless` exists only as a *postfix modifier* (covered below). There is no block `unless`. Writing `unless cond { ... }` is a parse error:

```drang
unless 0 { say("x") }
```

```
# parse errors in ex3.dr
line 1: unexpected UNLESS "unless"
line 1: expected end of statement, got INT "0"
```

For a negated block, write `if !cond { ... }`:

```drang
$done := false
if !$done { say("working") }
```

```
working
```

### while and until

`while cond { ... }` loops as long as the condition stays truthy. The condition is bare and the braces are required, matching `if`:

```drang
$i := 0
while $i < 3 { say($i); $i += 1 }
```

```
0
1
2
```

`until` mirrors `unless`: it has no block form and exists only as a postfix modifier. For a negated block loop, negate the condition of a `while`:

```drang
$i := 0
while !($i >= 3) { say($i); $i += 1 }
```

```
0
1
2
```

### for-in

`for $x in iter { ... }` iterates a collection. With one loop variable you get each element. With two variables, `for $a, $b in iter`, the first is a position or key and the second is the value.

The iterable is snapshotted when the loop begins, so mutating the collection inside the body does not change what the loop walks or how many times it runs. This example pushes a new element on every pass, yet the loop still runs exactly the three times the array had at entry:

```drang
$xs := [1, 2, 3]
for $x in $xs {
  push($xs, $x * 10)
  say($x)
}
say(len($xs))
```

```
1
2
3
6
```

**Over an array**, one variable binds the element; two bind index then element:

```drang
for $i, $x in ["a", "b"] { say($i ~ ":" ~ $x) }
```

```
0:a
1:b
```

(`~` is the string-concatenation operator. `+` is arithmetic only and does not join strings.)

**Over a map**, one variable iterates the *values*; two iterate *key then value*. This is the point to watch, because a single-variable loop over a map gives you values, not keys:

```drang
for $v in {"a": 1, "b": 2} { say($v) }
```

```
1
2
```

```drang
for $k, $v in {"a": 1, "b": 2} { say($k ~ "=" ~ $v) }
```

```
a=1
b=2
```

Maps preserve insertion order, so the iteration order above is defined, not arbitrary.

**Over an integer range** `lo..hi`, both ends are included. Two variables give a zero-based index and the value:

```drang
for $n in 1..4 { say($n) }
```

```
1
2
3
4
```

```drang
for $i, $n in 10..12 { say($i ~ "->" ~ $n) }
```

```
0->10
1->11
2->12
```

A descending range such as `5..1` is empty and yields no iterations, so the loop body simply never runs:

```drang
for $n in 5..1 { say($n) }
say("done")
```

```
done
```

**Over a string**, the loop walks one character at a time by rune, so multibyte characters stay whole:

```drang
for $c in "héy" { say($c) }
```

```
h
é
y
```

### break and next

`break` exits a loop; `next` skips to the loop's next iteration. Both bind to the *innermost* enclosing loop only.

```drang
for $n in 1..5 {
  if $n == 3 { next }
  if $n == 5 { break }
  say($n)
}
```

```
1
2
4
```

In nested loops, `break` leaves only the inner loop and the outer loop continues:

```drang
for $a in 1..2 {
  for $b in 1..3 {
    if $b == 2 { break }
    say($a ~ "," ~ $b)
  }
}
```

```
1,1
2,1
```

Placement is validated at parse time, not at runtime. A `break` or `next` that sits outside any loop is rejected before the program runs:

```drang
break
```

```
# parse errors in ex13.dr
line 1: 'break' outside a loop
```

The loop-nesting count resets at every function and lambda boundary. A `break` or `next` written inside a lambda or an `fn` body cannot reach a loop outside that function, even when the function is called from within a loop. Because the check is structural, this is a parse error rather than a silent no-op:

```drang
for $n in 1..3 {
  each([10, 20], |$x| { break })
}
```

```
# parse errors in ex14.dr
line 2: 'break' outside a loop
```

The reason is that the lambda passed to `each` is an independent function value. It has no static knowledge of the loop that happens to be running when `each` invokes it, so drang refuses to let control jump across that boundary.

### Postfix modifiers

Any simple statement may carry a single trailing modifier: `if`, `unless`, `while`, `until`, or `for`. This postfix form is the *only* way `unless` and `until` appear in the language.

`if` and `unless` gate whether the statement runs at all:

```drang
$x := 5
say("yes") if $x > 3
say("ok") unless 0
```

```
yes
ok
```

`while` and `until` re-run the statement until the condition flips. `while` repeats while the condition is truthy; `until` repeats while it is falsy:

```drang
$i := 0
$i += 1 while $i < 3
say($i)
```

```
3
```

```drang
$i := 0
$i += 1 until $i >= 3
say($i)
```

```
3
```

Postfix `for` runs the statement once per element of a collection and binds each element to the implicit variable `$_`. There is no place to name a loop variable in this form, so `$_` is how you reach the current element:

```drang
say($_) for [10, 20, 30]
```

```
10
20
30
```

`$_` is an ordinary value inside the statement, usable in any expression:

```drang
say($_ * $_) for [2, 3, 4]
```

```
4
9
16
```

## Functions, lambdas, closures, and pipelines

### Three name kinds, three sigils

drang encodes a name's kind in a sigil at *every* use, so a name's meaning is never ambiguous at the point you read it:

- **`$name`** is data: variables and constants alike (`$count`, `$pi`).
- **`.name`** is a **user-defined function**, declared `fn .name()`, called `.name()`, and passed as a value as bare `.name`.
- A **bare `name`** is a **builtin or standard-library function**, the language's own verbs (`say`, `map`, `split`, and the rest).

The leading `.` is the user-namespace sigil. Read `.foo` as "`foo`, a member of the implicit user namespace." It is the *same* `.` as field access: `.foo` is a member of the implicit user namespace exactly as `$m.foo` is a member of the map `$m`. Because your functions live in that `.` namespace, they can never collide with builtins. Your `.split` and the builtin `split` are distinct names in distinct spaces, so a future release adding a new builtin can never shadow or break your code.

### Named functions: `fn .name`

Declare a named function with `fn`, a **dotted** name, a parenthesized parameter list of `$` variables, and a brace body. Call it through the same dotted name.

```drang
fn .add($a, $b) { $a + $b }

fn .greet($name) {
  return "hi " ~ $name
}

say(.add(2, 3))
say(.greet("sam"))
```

```
5
hi sam
```

`~` is the string-concatenation operator and `say` prints one line. A bare `fn name` without the dot is a parse error: user functions are always `fn .name`. The dot is not optional punctuation, it is what tells the parser this is a user function and not a builtin.

#### Default parameters

A parameter may carry a default, written `$name = expr`, which makes it optional. Defaulted parameters must follow all required ones. A default is evaluated **at call time**, and only when its argument is actually omitted, so there is no hidden shared value that accumulates across calls. A default may reference an earlier parameter, since parameters bind left to right.

```drang
fn .serve($app, $port = 8080, $host = "localhost") {
  "{}://{}:{}" |> format($app, $host, $port)
}
say(.serve("web"))         # web://localhost:8080
say(.serve("web", 9090))   # web://localhost:9090

fn .range_end($start, $end = $start + 10) { $end }
say(.range_end(5))         # 15
```

```
web://localhost:8080
web://localhost:9090
15
```

Arguments are strictly positional. There are no named or keyword arguments, and there is no variadic `$a...` parameter; when you need a variable number of values, pass an array. The same `$name = expr` default syntax works on lambda parameters.

Calling with the wrong **number** of arguments is a hard abort with a source location, not a value you can recover. It is not catchable by `//` or `?`. The message names the accepted count: a function with defaults reports a range, a fixed-arity function reports a single number.

```drang
fn .serve($app, $port = 8080, $host = "localhost") { $app }
say(.serve("a", "b", "c", "d") // "recovered")
```

```
drang: .serve expects 1 to 3 arguments, got 4
  at ...:2:5
    say(.serve("a", "b", "c", "d") // "recovered")
        ^
```

The `// "recovered"` on that line never runs. Treat argument count as a contract checked before the call, in the same class as a builtin arity error, and distinct from a wrong argument *type* to a builtin (which does produce a catchable error value).

### Implicit and explicit return

A function returns the value of its **last statement**; no `return` is needed. When that last statement is an `if`/`else` (or any block), the value of the branch that runs falls straight out as the function's result.

```drang
fn .classify($n) {
  if $n < 0 { "negative" }
  else { "non-negative" }
}

say(.classify(-3))
say(.classify(7))
```

```
negative
non-negative
```

This works because a tail-position block hands out the value of the statement it ran. Note that `if`/`while`/`for` are statements, not expressions: they yield a value only in tail position as the body's result, not inline in the middle of an expression. You cannot write `$x := if c { 1 } else { 2 }`.

Use explicit `return` for early exits. There is also a postfix `return … if` form for guard clauses. (`.abs` below is *your* function; the builtin `abs` is untouched in its own namespace, and the two never clash.)

```drang
fn .abs($n) {
  return -$n if $n < 0
  $n
}

say(.abs(-4))
say(.abs(9))
```

```
4
9
```

### Lambdas: `|$a, $b| …`

An anonymous function is written as pipe-delimited parameters followed by **either** a single expression **or** a `{ … }` block. A block body also returns its last expression. Zero parameters is `||`. A lambda has no name of its own: bind it to a `$` variable and it becomes ordinary data, then call it through that `$` name. The `.` sigil is reserved for functions declared with `fn .name`, so a lambda is never called through a dot.

```drang
$sq := |$x| $x * $x
say($sq(5))

$f := |$a, $b| { $z := $a + $b; $z * 2 }
say($f(3, 4))

$hi := || "hello"
say($hi())
```

```
25
14
hello
```

A lambda body parses at the lowest precedence, so it greedily absorbs operators and `|>` but stops at a `,`, a `)`, a `]`, or a newline. Because a lambda is conventionally the *last* argument to a higher-order function, its body runs cleanly up to the closing `)` of the call. Note that `||` is the zero-parameter lambda and is unrelated to boolean logic: there is no `||` operator, the keyword is `or`.

### Closures

Both named functions and lambdas are closures. They capture the variables of the scope in which they are defined, so a returned inner function keeps working after the outer call has finished.

```drang
fn .make_adder($n) {
  |$x| $x + $n
}

$add10  := .make_adder(10)
$add100 := .make_adder(100)
say($add10(5))
say($add100(5))
```

```
15
105
```

Capture is **by reference**, not by value. The closure and the enclosing scope share the same binding: mutating a captured variable inside the closure is visible on later calls, and each fresh outer call produces its own independent binding. That combination is exactly what a stateful counter needs.

```drang
fn .counter() {
  $n := 0
  |$k| { $n = $n + $k; $n }
}
$a := .counter()
$b := .counter()
say($a(1))    # 1
say($a(1))    # 2
say($b(10))   # 10  (its own $n, untouched by $a)
```

```
1
2
10
```

Capture-by-reference raises an obvious question about loops: if a loop body builds closures over the loop variable, do they all end up sharing one final value? They do not. **Each iteration of a `for` loop gets its own fresh binding** of the loop variable, so closures built in different iterations capture distinct slots.

```drang
$fns := []
for $i in [1, 2, 3] {
  push($fns, || $i)
}
for $f in $fns {
  say($f())
}
```

```
1
2
3
```

If every iteration shared a single `$i`, this would print `3` three times. It prints `1 2 3` because the three closures captured three separate bindings.

### The pipeline operator `|>`

`x |> f(args)` rewrites to `f(x, args)`: the left operand is threaded in as the **first** argument of the call on the right. Chains therefore read left to right, in the order the data actually flows, which is the natural reading order for glue code.

```drang
fn .double($x) { $x * 2 }
fn .add($a, $b) { $a + $b }

say(5 |> .double())            # .double(5)      -> 10
say(5 |> .add(10))             # .add(5, 10)     -> 15
say(3 |> .double() |> .add(1)) # .add(.double(3), 1) -> 7
```

```
10
15
7
```

The same threading works into builtins, since a builtin is just a function value under a bare name:

```drang
say("hello world" |> upper() |> split())
```

```
[HELLO, WORLD]
```

`|>` is lexed greedily as one two-character token, so it never collides with a lambda's `|`.

To spread a pipeline over several lines, put `|>` at the **end** of each line. A trailing `|>` continues the statement onto the next line; a *leading* `|>` on a fresh line is read as the start of a new statement and is a parse error. The one exception is inside `(` or `[`, where newlines are insignificant, so a leading `|>` is fine when the whole chain is wrapped in parentheses.

```drang
$words := ["apple", "fig", "banana", "kiwi"]

$result := $words |>
  filter(|$w| len($w) > 3) |>
  map(|$w| upper($w)) |>
  reduce("", |$acc, $w| $acc ~ $w ~ " ")
say($result)
```

```
APPLE BANANA KIWI 
```

(The trailing space is real: the fold appends `" "` after every word.)

### Higher-order functions

`map`, `filter`, `reject`, `reduce`, and their companions are built in and **array-first**: the array is the first parameter, precisely so these compose under `|>`. Full coverage of the toolkit lives in the Collections section; here is the shape.

```drang
$xs := [1, 2, 3, 4, 5]
say($xs |> map(|$x| $x * $x))
say($xs |> filter(|$x| $x % 2 == 0))
say($xs |> reduce(0, |$acc, $x| $acc + $x))
```

```
[1, 4, 9, 16, 25]
[2, 4]
15
```

Callbacks are arity-flexible. A one-parameter lambda receives the element; a two-parameter lambda also receives the index. `reduce`'s callback is `(acc, el)` or, if you declare a third parameter, `(acc, el, index)`, and its initial accumulator is a required argument.

### Functions and builtins are first-class values

Both a named user function and a builtin can be passed point-free: a bare name in value position *is* a function value, ready to hand to a higher-order builtin without wrapping it in a lambda.

```drang
fn .shout($s) { upper($s) }
say(["a", "b"] |> map(.shout))                        # [A, B]

say(["/a/b/foo.txt", "/c/d/bar.txt"] |> map(basename)) # [foo.txt, bar.txt]
say(["x", "yy", "zzz"] |> map(len))                    # [1, 2, 3]
say([3, 1, 2] |> reduce(0, max))                       # 3

$f := upper
say($f("hi"))                                          # HI
say(type(len))                                         # function
```

```
[A, B]
[foo.txt, bar.txt]
[1, 2, 3]
3
HI
function
```

You reach for an explicit lambda only when you need to reshape the call: reorder arguments, supply extra ones, or pull in the index. A two-parameter callback receives the index alongside the element:

```drang
$xs := ["a", "b", "c"]
say(map($xs, |$x, $i| format("{}:{}", $i, $x)))
```

```
[0:a, 1:b, 2:c]
```

One caution about the shared namespaces. Binding a `$` variable whose name matches a builtin does not create a separate callable slot; within that scope the name now resolves to your data everywhere, including call position. So after `$len := 99`, the name `len` is the number, and trying to call it aborts.

```drang
$len := 99
say($len)
say(len([1, 2, 3]))
```

```
99
drang: len is not a function (it is a int)
  at ...:3:5
    say(len([1, 2, 3]))
        ^
```

The lesson is practical: if you still need a builtin, do not bind its name to non-function data in the same scope. Choose a different variable name.

## Arrays, maps, and the collection toolkit

drang has two built-in container types: ordered **arrays** written `[..]`, and insertion-ordered **maps** written `{k: v}`. A single higher-order toolkit (`map`, `filter`, `sort`, and friends) drives both, and it is the workhorse for text munging and glue scripts. Learn the toolkit once and it applies everywhere.

### Arrays

An array literal is a comma-separated list in square brackets. Elements may be any value, and they need not share a type:

```drang
say([10, 20, 30])
say([1, "two", [3, 4]])
```

```
[10, 20, 30]
[1, two, [3, 4]]
```

Note the display: strings print without quotes when they appear inside a container, so the array `[1, "two", [3, 4]]` shows as `[1, two, [3, 4]]`.

Indexing is zero-based with `arr[i]`. A negative index counts from the end, so `-1` is the last element:

```drang
say([10, 20, 30][1])     # 20
say([10, 20, 30][-1])    # 30
say([10, 20, 30][-2])    # 20
```

You might expect an out-of-range index to crash the script. It does not. Reading past the end (or before the start) produces a catchable error value, so you can recover from it with `//` or propagate it with `?` rather than guarding every access:

```drang
say([1, 2][5])           # error: index 5 out of range (len 2)
say([10, 20, 30][-4])    # error: index -4 out of range (len 3)
```

Slicing uses a range index, `arr[lo..hi]`, and returns a new array. The range is **inclusive at both ends** (as every drang range is), so `arr[1..3]` includes index 3. Negative bounds count from the end, out-of-range bounds clamp to what exists, and a reversed or empty range yields `[]`. A slice therefore never errors, which is the deliberate contrast with a single index:

```drang
say([10, 20, 30, 40, 50][1..3])    # [20, 30, 40]   (inclusive)
say([10, 20, 30, 40, 50][-2..-1])  # [40, 50]
say([10, 20, 30][1..99])           # [20, 30]        (clamped)
say([10, 20, 30][2..0])            # []              (reversed)
```

`len` returns the element count. The same builtin also measures strings (by rune), maps (by entry), and ranges:

```drang
say(len([1, 2, 3]))      # 3
```

#### Mutating in place: push and pop

`push` and `pop` change the array in place. `push` appends one or more values and returns the same array. `pop` removes and returns the last element, and errors on an empty array:

```drang
$a := [1, 2]
push($a, 3, 4)
say($a)                  # [1, 2, 3, 4]

$a := [1, 2, 3]
say(pop($a))             # 3
say($a)                  # [1, 2]

say(pop([]))             # error: pop from empty array
```

These two are the exception. Nearly every other array function returns a **new** array and leaves its input untouched, which keeps pipelines predictable.

#### Non-mutating helpers: take, drop, uniq, contains

`take(arr, n)` returns the first `n` elements; `drop(arr, n)` returns everything after the first `n`. Both clamp `n` to the array's length rather than erroring, so an over-long count is harmless. `uniq` returns the distinct elements by structural equality, in first-seen order:

```drang
say(take([1, 2, 3, 4, 5], 2))    # [1, 2]
say(take([1, 2], 9))             # [1, 2]   (clamped)
say(drop([1, 2, 3, 4, 5], 2))    # [3, 4, 5]
say(uniq([1, 1, 2, 3, 3, 3, 1])) # [1, 2, 3]
```

`contains(arr, x)` reports membership by structural equality:

```drang
say(contains([1, 2, 3], 2))      # true
say(contains([1, 2, 3], 9))      # false
```

### Maps

A map literal is `{key: value, ...}`. A bareword key is taken as a string; any scalar expression also works as a key. Iteration follows **insertion order**, and the map is never silently re-sorted:

```drang
$m := {name: "ada", age: 36}
say($m)                  # {name: ada, age: 36}
say({z: 1, a: 2, m: 3})  # {z: 1, a: 2, m: 3}   (order preserved, not sorted)
```

Read a value with dot syntax `$m.field` (the field name is used as a string key) or with bracket syntax `$m[key]` (the key is any expression). A map doubles as a lightweight record: `{name: "ada", age: 36}` accessed as `$m.name` and `$m.age` reads exactly like a struct.

Reading a **missing key returns a nil value** rather than erroring:

```drang
$m := {name: "ada"}
say($m.name)             # ada
say($m["name"])          # ada
say($m["missing"])       # nil
say($m.zzz)              # nil
```

One subtlety worth internalizing early: `nil` is what a missing lookup evaluates to and what `say` prints for it, but there is **no `nil` literal you can write**. A bare `nil` in source is an undefined name and aborts. So do not test for a missing key by comparing to `nil`. Recover it with `//`, which treats a nil (or an Err) as the trigger for its fallback:

```drang
$m := {name: "ada"}
say($m.missing // "default")   # default
say($m.name // "default")      # ada
```

Assign into a map (creating or updating the key) with `$m[key] = value`:

```drang
$m := {}
$m["x"] = 9
say($m)                  # {x: 9}
```

The inspection and mutation builtins mirror the array ones. `has` tests membership; `keys`, `values`, and `pairs` extract fresh arrays; `delete` removes a key in place:

```drang
$m := {a: 1, b: 2}
say(has($m, "a"), has($m, "z"))   # true false
say(keys($m))                     # [a, b]
say(values($m))                   # [1, 2]
say(pairs($m))                    # [[a, 1], [b, 2]]
delete($m, "a")
say($m)                           # {b: 2}
```

`keys`, `values`, and `pairs` all return their arrays in insertion order, which makes iteration over a map straightforward. `pairs` gives you `[key, value]` two-element arrays:

```drang
$m := {a: 1, b: 2}
for $p in pairs($m) {
  say(format("{} = {}", $p[0], $p[1]))
}
```

```
a = 1
b = 2
```

Keys must be **hashable scalars**: integers, strings, booleans, floats, and nil. Using an array (or any other container) as a key is a catchable error at index time:

```drang
$m := {1: "one", 2: "two"}
say($m[1])               # one

$m := {a: 1}
say($m[[1, 2]])          # error: unhashable map key: array
```

### The higher-order toolkit

These functions operate on arrays and take a callback written as a closure. The **array is always the first argument**, so the functions compose under the pipe operator `|>`, where `xs |> f(args)` calls `f(xs, args)`. Read a pipeline left to right as a sequence of transforms on the collection.

Callbacks are arity-flexible. A one-parameter closure `|$x|` receives the element; a two-parameter closure `|$x, $i|` also receives the element's zero-based index:

```drang
say(["a", "b", "c"] |> map(|$x, $i| format("{}:{}", $i, $x)))   # [0:a, 1:b, 2:c]
```

**`map`** transforms each element into a new array:

```drang
say([1, 2, 3] |> map(|$x| $x * $x))      # [1, 4, 9]
```

**`filter`** and **`reject`** keep or drop the elements a predicate matches:

```drang
say([1, 2, 3, 4, 5, 6] |> filter(|$x| $x % 2 == 0))   # [2, 4, 6]
say([1, 2, 3, 4, 5, 6] |> reject(|$x| $x % 2 == 0))   # [1, 3, 5]
```

**`find`** returns the first matching element, or a nil value if none match (recover it with `//`):

```drang
say([3, 8, 5, 12, 2] |> find(|$x| $x > 10))   # 12
say([1, 2] |> find(|$x| $x > 10))             # nil
```

**`any`**, **`all`**, and **`count`** are the predicate aggregates. Over an empty array, `any` is `false` and `all` is `true`:

```drang
say([1, 2, 3] |> any(|$x| $x > 2))               # true
say([2, 4, 6] |> all(|$x| $x % 2 == 0))          # true
say([1, 2, 3, 4, 5] |> count(|$x| $x % 2 == 1))  # 3
```

**`each`** runs a callback purely for its side effects and returns the **original** array, so it drops into the middle of a pipeline without breaking the chain:

```drang
$r := [1, 2, 3] |> each(|$x| say("saw", $x))
say($r)
```

```
saw 1
saw 2
saw 3
[1, 2, 3]
```

**`flat_map`** maps each element to an array, then concatenates the results one level deep:

```drang
say([1, 2, 3] |> flat_map(|$x| [$x, $x * 10]))   # [1, 10, 2, 20, 3, 30]
```

**`reduce(arr, init, fn)`** folds from the left with an explicit initial accumulator. The initial value is **required**; there is no two-argument form that seeds from the first element. Because `reduce` takes the accumulator between the array and the function, its natural call shape is the three-argument form, not a pipe:

```drang
say(reduce([1, 2, 3, 4], 0, |$acc, $x| $acc + $x))   # 10
```

#### How callbacks fail

The combinators that take a callback (`map`, `filter`, `reject`, `find`, `any`, `all`, `count`, `flat_map`) are **fail-loud**: if a callback *returns* an Err value, that Err becomes the whole result, which you can then catch. This is the ordinary case where you want the failure surfaced:

```drang
fn .check($x) {
  if $x == 2 { return fail("bad two") }
  return $x
}
$r := [1, 2, 3] |> map(|$x| .check($x))
say(is_err($r), err_msg($r))   # true bad two
```

Distinguish that from a callback that hits a **type error in an operator**. Operators do not produce catchable Err values; they abort. So a callback like `|$x| $x + 1` run over a mixed array stops the program at the `+`, and neither `//` nor `?` can intercept it:

```drang
say([1, "x", 3] |> map(|$x| $x + 1))
```

```
drang: cannot use string and int with '+' (no automatic coercion: convert with int()/float()/str(), or ~ to join strings)
```

The rule is general: a bad *value* handed to a builtin is a recoverable Err, but a bad operand to an operator is a hard abort. Convert types explicitly (`int(...)`, `float(...)`, `str(...)`) before arithmetic if the array might be heterogeneous.

### The ordering family

`sort` returns a new array in natural ascending order: numbers numerically, strings lexicographically. The sort is stable:

```drang
say(sort([3, 1, 2, 10, 5]))                  # [1, 2, 3, 5, 10]
say(sort(["banana", "apple", "cherry"]))     # [apple, banana, cherry]
```

Natural order requires elements of a single orderable type. A mixed array is a catchable error, not a silent coercion:

```drang
say(is_err(sort([1, "a", 2])))               # true
```

For a custom order, pass a **comparator** `|$a, $b| ...` that returns a negative number, zero, or a positive number. The `<=>` **spaceship operator** computes exactly that three-way comparison, so it pairs directly with `sort`:

```drang
say(1 <=> 2, 2 <=> 2, 3 <=> 2)               # -1 0 1
say(sort([3, 1, 2], |$a, $b| $b <=> $a))     # [3, 2, 1]   (descending)
```

`sort_by`, `min_by`, and `max_by` take a **key function** `|$el| ...` instead of a comparator, and order by the computed key. `sort_by` computes each key exactly once. `min_by` and `max_by` return the extreme element itself, or a nil value for an empty array:

```drang
say(sort_by(["ccc", "a", "bb"], |$s| len($s)))   # [a, bb, ccc]
say(min_by(["ccc", "a", "bb"], |$s| len($s)))    # a
say(max_by(["ccc", "a", "bb"], |$s| len($s)))    # ccc
say(min_by([], |$x| $x))                         # nil
```

A key function is the natural way to order records by a field:

```drang
$people := [{n: "ada", age: 36}, {n: "bo", age: 29}]
say(sort_by($people, |$p| $p.age) |> map(|$p| $p.n))   # [bo, ada]
```

Because every collection function returns a value (a new array, or an element, or nil), they chain end to end:

```drang
say([5, 3, 8, 1, 9, 2] |> filter(|$x| $x > 2) |> sort() |> take(3))   # [3, 5, 8]
```

### Prelude: collection helpers written in drang

A layer of everyday helpers ships in the standard library but is written in drang itself, in an embedded *prelude*, rather than in the Go core. They are pure compositions of the builtins above and are available unqualified, exactly like a builtin. Writing part of the standard library in the language keeps the native core small and continually exercises the language on real code.

The transforms below map cleanly onto what you have already seen:

```drang
say(flatten([[1, 2], [3]]))                              # [1, 2, 3]
say(sum_by([1, 2, 3, 4], |$x| $x * $x))                  # 30
say(chunk([1, 2, 3, 4, 5], 2))                           # [[1, 2], [3, 4], [5]]
say(zip([1, 2, 3], ["a", "b"]))                          # [[1, a], [2, b]]
say(enumerate(["a", "b", "c"]))                          # [[0, a], [1, b], [2, c]]
say(uniq_by([1, 2, 3, 4], |$x| $x % 2))                  # [1, 2]
```

`zip` truncates to the shorter input. `uniq_by` keeps the first element for each distinct key. `enumerate` pairs each element with its index, which is handy when a `for` loop needs the position.

The bucketing helpers return maps. `group_by` collects the elements under each key; `partition` is the two-bucket special case (matching, then non-matching); `count_by` tallies instead of collecting. Passing the identity `|$x| $x` to `count_by` gives a plain histogram:

```drang
say(group_by([1, 2, 3, 4, 5, 6], |$x| $x % 2))                       # {1: [1, 3, 5], 0: [2, 4, 6]}
say(partition([1, 2, 3, 4, 5], |$x| $x % 2 == 0))                    # [[2, 4], [1, 3, 5]]
say(count_by(["red", "blue", "red", "red", "blue"], |$x| $x))        # {red: 3, blue: 2}
```

The set helpers dedupe and preserve the first array's order:

```drang
say(intersect([1, 2, 3, 4], [2, 4, 6]))   # [2, 4]
say(union([1, 2, 3], [3, 4, 5]))          # [1, 2, 3, 4, 5]
say(difference([1, 2, 3, 4], [2, 4]))     # [1, 3]
```

The statistics helpers return floats. Remember drang's display rule: a whole-valued float prints without a trailing `.0`, so a mean that lands on an integer looks like an integer. An empty input is a catchable error:

```drang
say(mean([2, 4, 9]))          # 5     (a float; 5.0 prints as 5)
say(median([5, 1, 3]))        # 3
say(median([1, 2, 3, 4]))     # 2.5   (mean of the two middle values)
say(is_err(mean([])))         # true
say(is_err(median([])))       # true
```

Nested-structure and scalar helpers round out the set. `get_in` walks a path of keys and indices into nested maps and arrays, returning nil if any step is missing. `deep_merge` merges two maps recursively into a new map, with the second argument winning on conflicts and nested maps merged rather than replaced:

```drang
say(get_in({a: {b: [10, 20, 30]}}, ["a", "b", 2]))                   # 30
say(get_in({a: 1}, ["a", "zzz"]))                                    # nil
say(deep_merge({a: 1, nest: {x: 1}}, {b: 2, nest: {y: 2}}))          # {a: 1, nest: {x: 1, y: 2}, b: 2}
```

```drang
say(clamp(15, 0, 10), clamp(-3, 0, 10), clamp(5, 0, 10))   # 10 0 5
say(sign(-8), sign(0), sign(8))                            # -1 0 1
say(capitalize("hELLO"))                                   # Hello
say(reverse("abcé"))                                       # écba   (reversed by rune)
say(pad("hi", 6) ~ "|")                                    # hi    |   (right-padded with spaces)
```

`dedent` strips the common leading indentation from every line of a string, which is what you want when embedding a heredoc-style block inside indented code:

```drang
$s := dedent("    line one\n      line two\n    line three")
say($s)
```

```
line one
  line two
line three
```

The full prelude collection surface:

| Helper | Meaning |
|--------|---------|
| `flatten(xss)` | concatenate one level of nesting: `[[1, 2], [3]]` -> `[1, 2, 3]` |
| `sum_by(xs, f)` | sum of `f` over each element |
| `count_by(xs, f)` | count occurrences keyed by `f(x)` -> a map `{key: count}`; a plain histogram is the identity key, `count_by($xs, \|$x\| $x)` |
| `chunk(xs, n)` | split into `n`-sized pieces (`n < 1` is an error) |
| `zip(a, b)` | pair two arrays element-wise, truncating to the shorter |
| `group_by(xs, f)` | bucket elements by `f(x)` -> `{key: [elems]}` |
| `partition(xs, pred)` | split into `[matching, non-matching]` |
| `uniq_by(xs, f)` | keep the first element per distinct `f(x)` (order preserved) |
| `enumerate(xs)` | pair each element with its index: `["a", "b"]` -> `[[0, "a"], [1, "b"]]` |
| `mean(xs)` | arithmetic mean (float); empty list -> catchable Err |
| `median(xs)` | middle of the sorted list (mean of the two middle if even); empty -> Err |
| `intersect(a, b)` | elements in both, deduped (hashable elements; `a`'s order) |
| `union(a, b)` | all distinct elements from both (`a`'s order first) |
| `difference(a, b)` | elements in `a` not in `b`, deduped |
| `pad(s, width)` | left-justify by padding the right with spaces (use `format`'s `{:>n}` for right-justify) |
| `capitalize(s)` | first character upper, the rest lower |
| `reverse(s)` | reverse a string by characters (rune-correct) |
| `dedent(s)` | strip the common leading indentation from every line |
| `clamp(x, lo, hi)` | constrain `x` to the range `[lo, hi]` |
| `sign(x)` | `-1`, `0`, or `1` |
| `get_in(data, path)` | follow a path of keys/indices into nested maps/arrays; nil if any step is missing |
| `deep_merge(a, b)` | recursively merge two maps into a new map (`b` wins; nested maps merged) |
| `retry(n, delay_ms, f)` | call `f()` up to `n` times, returning the first non-error result (waiting `delay_ms` between) |

## Errors as values

In drang an error is a value, not an exception. It carries a tag, the same way an int or a string does, and it flows through your program like any other value. A fallible operation returns either its normal result or an **Err** value holding a message and an integer code. Nothing unwinds on its own. You decide what happens next: inspect the Err, recover from it, or propagate it.

The model has four parts. `fail` creates an Err. `is_err` / `err_code` / `err_msg` read one. The `//` operator recovers from one with a fallback. The `?` postfix operator propagates one to the enclosing function boundary. Learn those four and you have the whole system.

One property shapes everything else: an Err value is **truthy**. A plain condition test does not detect failure.

```drang
if int("x") { say("truthy branch taken") }
```

```
truthy branch taken
```

Because a failed result still enters the true branch, you must test for failure explicitly with `is_err`. Never rely on bare truthiness to catch an error.

### Inspecting errors

`is_err(x)` reports whether `x` is an Err value. `err_code(x)` and `err_msg(x)` extract its code and message. On a value that is not an error they return the neutral results `0` and `""`. That is deliberate: `err_code(run(cmd))` then reads as "the exit code, 0 on success," with no special-casing.

```drang
$r := fail("boom")
say(is_err($r), err_code($r), err_msg($r))
say(err_code(42), err_msg(42) == "")
```

```
true 1 boom
0 true
```

An Err renders through `say` as `error: <msg>`, so a stray error surfaces visibly rather than silently:

```drang
say(int("x"))
```

```
error: cannot parse "x" as int
```

### Creating errors: fail

`fail(msg)` builds an Err with the given message and code `1`. Called with no argument it uses the message `"failed"`.

```drang
$r := fail()
say(is_err($r), err_msg($r), err_code($r))
```

```
true failed 1
```

`fail` honors only a message. It takes no code argument, and any extra arguments are ignored: the code is always `1`. Non-`1` codes do not come from `fail`. They come from operations that carry a code naturally, chiefly the subprocess builtins, which fold a child's exit status into the Err code (covered below).

### Recovering with //

`risky() // fallback` evaluates `risky()`. If the result is an Err **or** `nil`, it evaluates and returns `fallback` instead. Otherwise the original value passes through unchanged. This is the workhorse for "try, but have a default."

```drang
say(int("100") // 0, int("oops") // 0)
```

```
100 0
```

Two points to get right.

First, `//` triggers *only* on Err or nil. Other falsy values (`0`, `""`, `false`, an empty array or map) are legitimate results and pass straight through. Recovery keys on the failure tag, not on emptiness.

```drang
say(0 // 99, "" // "x", false // "y")
```

```
0  false
```

Second, the fallback is evaluated **eagerly**, not lazily. Both sides of `//` run before the operator picks a result. So the right-hand side must be safe to evaluate even when the left-hand side succeeded. If you need a fallback that must not run on the happy path, guard it with an `if`.

### Propagating with ?

The `?` postfix operator is the early-exit half of the model. `expr?` evaluates `expr`. If the result is an Err, `?` propagates it out of the **enclosing function** immediately. If it is not an Err, the value flows through unchanged. This lets you write the happy path without a check after every call.

```drang
fn .parse($s) {
  $n := int($s)?      # bail out of .parse if $s is not an int
  return $n * 2
}
say(.parse("21"))
```

```
42
```

The key rule: `?` propagates only to the **nearest call boundary**. When the propagated error reaches the point where the function was called, it becomes an ordinary Err value again in the caller. It does not keep unwinding through the call stack. So the caller can simply recover it, or inspect it, and carry on.

```drang
fn .parse($s) {
  $n := int($s)?
  return $n * 2
}
$r := .parse("xx")
say(is_err($r), err_msg($r))
say("still running")
```

```
true cannot parse "xx" as int
still running
```

At the **top level** there is no enclosing function, so a `?` that fires there has nowhere to propagate. It aborts the whole program. The process exits with the Err's code (clamped to the range `1..255`) and prints `drang: <msg>` to stderr with the source location:

```drang
fail("nope")?
say("unreached")
```

```
drang: nope
  at prog.dr:1:1
    fail("nope")?
    ^
```

That top-level behavior is what makes `?` useful for scripts. Propagate failures upward, let them reach the top, and the program exits with a meaningful status automatically.

### The builtin convention: arg-count aborts, bad values are catchable

Builtins split wrongness into two categories, and treat them differently.

- A wrong **argument count** is a programmer error. It is a hard abort with a source location. Neither `//` nor `?` can intercept it, because there is nothing sensible to recover: the call itself is malformed.
- A wrong **argument type** or a **runtime failure** is a runtime condition. It returns a catchable Err value you can recover.

```drang
say(is_err(int([1, 2])))   # wrong type -> catchable Err
```

```
true
```

```drang
say(int(1, 2) // 99)       # wrong arg count -> hard abort, // cannot save it
```

```
drang: int expects 1 argument, got 2
  at prog.dr:1:5
    say(int(1, 2) // 99)
        ^
```

So `int("x") // 0` is safe and idiomatic, but `int(1, 2) // 0` is a bug that crashes as it should. The distinction is stable: you can lean on `//` to absorb bad *values* without worrying that it will also mask a mistake in how many arguments you passed.

This "bad type is catchable" rule belongs to **builtins only**. Operators do not follow it: a type mismatch on an operator aborts. The same underlying failure can therefore be catchable or fatal depending on which form you use. Dividing by zero through the `/` operator aborts, while the `div` builtin returns a recoverable Err:

```drang
say(1 / 0)
```

```
drang: division by zero
  at prog.dr:1:5
    say(1 / 0)
        ^
```

```drang
say(div(1, 0) // "recovered")
```

```
recovered
```

When you want a division you can recover from, reach for the builtin, not the operator.

### Recovering a failed command

Subprocess builtins follow the same value-result convention, and they are the main source of non-`1` codes. `run(...)` returns `true` on success or a catchable Err carrying the child's exit code (`127` when the command cannot be started at all). `capture(...)` returns the child's trimmed stdout, or an Err on failure with the child's stderr folded into the message.

```drang
$r := run("cmd", "/c", "exit 3")
say(is_err($r), err_code($r))
```

```
true 3
```

Recover a missing or failing command with `//`:

```drang
say(run("definitely-not-a-real-cmd") // "could not run")
say(capture("cmd", "/c", "exit 1") // "default")
```

```
could not run
default
```

Because the Err carries the real exit code, you can branch on it. This is how you distinguish an exit code that means "no result" from one that means "genuine failure," such as a search tool that exits `1` when it simply found no match:

```drang
$r := capture("cmd", "/c", "exit 1")
if is_err($r) {
  if err_code($r) == 1 { say("no match") } else { say("error:", err_msg($r)) }
} else {
  say("found:", $r)
}
```

```
no match
```

And `?` plumbs a command's exit code straight through to the process when it propagates to the top level. The script's exit status becomes the child's:

```drang
run("cmd", "/c", "exit 3")?
```

```
drang: cmd exited with code 3
  at prog.dr:1:1
    run("cmd", "/c", "exit 3")?
    ^
```

The process exits with status `3`.

### Putting it together

A guard function that returns an Err, which the caller then propagates or recovers by choice:

```drang
fn .checked_div($a, $b) {
  if $b == 0 { return fail("divide by zero") }
  return $a / $b
}
say(.checked_div(10, 2) // "n/a")
say(.checked_div(10, 0) // "n/a")
```

```
5
n/a
```

The shape to internalize: `fail` and failing builtins **make** Err values; `?` **moves** them up to the nearest call boundary (and aborts at the top level with the right exit code); `//` **absorbs** them with a default; and `is_err` / `err_code` / `err_msg` **read** them when you need to branch. Errors never unwind by themselves. They travel exactly as far as you send them.

## Regular expressions

drang's regex engine is Go's [RE2](https://github.com/google/re2/wiki/Syntax). Matching runs in linear time with no catastrophic backtracking. That guarantee has a price: the pattern syntax has no backreferences (no `\1` inside a pattern) and no lookaround. If you reach for either, the compiler rejects the pattern rather than silently accepting a slow one.

Patterns come in two forms. A `qr//` literal is compiled by the lexer into a first-class `regex` value. A plain string is compiled on demand by the matching builtins. Either way the compiled result is an immutable, cached `regex`, safe to share across `pmap` workers without copying or locking.

### `qr//` literals

A `qr//` literal compiles its pattern once, at lex time, into a reusable `regex` value:

```drang
say(qr/\d+/)
```

```
qr/\d+/
```

The body is taken literally. Backslashes pass straight through to RE2, so you write `\d`, not `\\d`. This is the reason to prefer `qr//` over a string pattern: a string body decodes its own escapes first, so it needs the doubled backslash that the literal does not.

#### Flags

Flags follow the closing delimiter: `i` (case-insensitive), `m` (multi-line, so `^` and `$` match at line boundaries), `s` (dotall, so `.` matches a newline), and `U` (ungreedy, which swaps the meaning of greedy and lazy quantifiers). They are baked into the pattern as inline flags, which you can see when you print the value:

```drang
say(qr/foo/i)
say(qr/foo/ims)
```

```
qr/(?i)foo/
qr/(?ims)foo/
```

The flags change matching behavior directly:

```drang
say(matches("a\nb", qr/a.b/s))   # dotall on: . spans the newline
say(matches("a\nb", qr/a.b/))    # off: . won't cross \n
say(match("<a><b>", qr/<.+>/U))  # ungreedy: stops at first >
```

```
true
false
[<a>]
```

An unknown flag letter is a parse error, caught before the program runs rather than at the point of use:

```drang
say(qr/foo/x)
```

```
# parse errors in <-e>
line 1: unexpected ILLEGAL "invalid regex flag after qr//"
line 1: expected end of statement, got IDENT "x"
```

#### Delimiters

Besides `/`, a `qr//` literal may open with `|`, `(`, `[`, or `{`. The same-character delimiters (`/`, `|`) run to the next occurrence of that character. The paired delimiters (`(...)`, `[...]`, `{...}`) nest, so the pattern can contain balanced copies of the delimiter char without ending the literal early. Choose a delimiter your pattern does not need to contain:

```drang
say(matches("a/b", qr|/|))          # pattern contains a slash -> use | delimiter
say(match("ab", qr((a)(b))))        # ( ) nest around the groups
say(match_all("aaa", qr{a{1}}))     # { } nest around the quantifier
```

```
true
[ab, a, b]
[a, a, a]
```

### `re(pattern)`: compile a dynamic pattern

A `qr//` literal is fixed at parse time. When the pattern is built at runtime (interpolated from input, assembled from pieces), compile the string with `re()` into a reusable `regex` value. A value that is already a `regex` passes through unchanged, so `re()` is also the idiomatic "give me a regex, whatever I hand you" coercion:

```drang
$p := "\d+"
$rx := re($p)
say(matches("a9", $rx))
say($rx)
say(re(qr/x/i))   # regex in -> same regex out
```

```
true
qr/\d+/
qr/(?i)x/
```

### The matching builtins

Five builtins do the work. For `matches`, `match`, and `match_all`, the pattern argument is either a string or a compiled `regex`, and the two are interchangeable: a string is compiled as a pattern. Passing a `qr//` or `re()` value reuses the compiled object instead of recompiling on each call.

| Builtin | Returns |
|---|---|
| `matches(s, p)` | bool: does `p` match anywhere in `s` |
| `match(s, p)` | `[full, group1, group2, ...]`, or `nil` if no match |
| `match_all(s, p)` | array of every full match, in order (empty array if none) |
| `replace_first(s, needle, repl)` | `s` with the first match replaced |
| `replace_all(s, needle, repl)` | `s` with every match replaced |

```drang
say(matches("Hello World", qr/world/i))
say(match("2026-06-26", qr/(\d{4})-(\d{2})-(\d{2})/))
say(match("nope", qr/\d+/))
say(match_all("a1 b22 c333", qr/\d+/))
```

```
true
[2026-06-26, 2026, 06, 26]
nil
[1, 22, 333]
```

Note the shape of `match`. It returns a flat array whose first element is the whole match and whose remaining elements are the capture groups, in order. It does not return a map keyed by group name. If you expected named captures to arrive as fields you can look up, they do not: `(?P<name>...)` groups come back by position, in the same array, with no `.field` or map accessor and no separate lookup builtin.

```drang
say(match("john smith", qr/(?P<first>\w+) (?P<last>\w+)/))
```

```
[john smith, john, smith]
```

`match_all` collects full matches only. It discards capture groups and, on no match, returns an empty array rather than `nil`:

```drang
say(match_all("abc", qr/\d+/))
```

```
[]
```

For `matches`, `match`, and `match_all`, a string pattern and the equivalent `qr//` behave identically, except that the string form needs the backslash the literal form omits:

```drang
say(match_all("a1b2", "\d"))
say(match_all("a1b2", qr/\d/))
```

```
[1, 2]
[1, 2]
```

### `replace_first` / `replace_all`

These two are the deliberate exception to the "string is a pattern" rule. Here a plain-string needle is a literal, and only a `qr//` or `re()` value is treated as a pattern. The needle's type is how you say what you mean: a string replaces exact text, a regex replaces matches.

```drang
say(replace_all("a.b", ".", "-"))     # string needle: literal dot
say(replace_all("a.b", qr/./, "-"))   # regex needle: every character
```

```
a-b
---
```

`replace_first` stops after the first match; `replace_all` continues to the end:

```drang
say(replace_first("a1b2", qr/\d/, "#"))
say(replace_all("a1b2", qr/\d/, "#"))
```

```
a#b2
a#b#
```

#### Backreferences in the replacement

RE2 has no pattern backreferences, but the replacement string does support substitution. With a regex needle, `$1`, `$2`, ... in the replacement expand to the corresponding capture groups. This is replacement-side substitution, not a backreference inside the pattern:

```drang
say(replace_all("2026-06-26", qr/(\d{4})-(\d{2})-(\d{2})/, "$3/$2/$1"))
```

```
26/06/2026
```

To refer to a group by name with `${name}`, name it in the pattern with RE2's `(?P<name>...)` syntax. One subtlety matters here: the replacement is a drang string, and drang's own interpolation could consume `${...}` before `replace_all` ever sees it. So write the replacement as a non-interpolating literal, either `"..."` or `q{...}`, which pass `${...}` through untouched. Do not use an interpolating form such as `$"..."` or `$qq{...}` for a replacement with named groups, because drang would expand `${name}` itself and fail.

```drang
say(replace_all("john smith", qr/(?P<first>\w+) (?P<last>\w+)/, q{${last}, ${first}}))
```

```
smith, john
```

### Bad patterns are catchable errors

A malformed string pattern does not crash the program. It surfaces as a first-class `Err` value that you can inspect with `is_err`, recover from with `//`, or propagate with `?`:

```drang
$e := matches("x", "(")
say(is_err($e))
```

```
true
```

The same `Err` flows out of `re()`. Left uncaught, it prints the RE2 diagnostic and the program continues:

```drang
say(re("("))
```

```
error: bad regex "(": error parsing regexp: missing closing ): `(`
```

Because the engine is RE2, a backreference inside a pattern is simply invalid syntax. It compiles to an `Err`, not a match:

```drang
say(re("(a)\1"))
```

```
error: bad regex "(a)\\1": error parsing regexp: invalid escape sequence: `\1`
```

This catchable-error behavior applies to string patterns compiled at runtime. A malformed body inside a `qr//` literal is different: it is a lex or parse error, caught before the program runs, exactly like the unknown-flag case above.

## External commands and concurrency

drang is built to be glue, so launching other programs is a first-class part of the language. Every external command goes through the operating system's process API directly. **There is no shell.** Arguments are passed to the child verbatim: nothing splits them on spaces, nothing expands `*` into filenames, nothing interprets `&&` or `>`. If you type `run("cmd", "/c", "echo", "*.txt")`, the child receives the literal three-character string `*.txt`.

This is a deliberate choice, and it is the safe one. A string you assembled from user input can never accidentally become three commands or delete a directory, because the pieces you pass are the exact pieces the child sees. When you genuinely want shell behavior (globbing, `&&`, redirection), you ask for it explicitly by invoking `cmd /c "..."` yourself as a single stage.

Failures are values, not exceptions. A command that exits non-zero, cannot be found, or is killed returns a catchable `Err` carrying the child's exit code. You propagate it with `?`, recover with `//`, or inspect it with `is_err`/`err_code`/`err_msg`. This is the same error discipline as the rest of the language, applied to processes.

> drang is Windows-only, so every example below shells out to Windows programs (`cmd /c`, `findstr`, `ping`, `sort`, `where`). They are deliberately short and non-destructive.

### `run`: execute with stdio wired through

`run(cmd, args..., {opts}?)` runs a command with the child's stdin, stdout, and stderr connected straight to drang's own. The child prints directly to your terminal. `run` returns `true` on success, so it drops naturally into an `if` or a `//`, and returns an `Err` on a non-zero exit.

```drang
$ok := run("cmd", "/c", "exit 0")
say($"success returns true: $ok")
$bad := run("cmd", "/c", "exit 5")
say($"failure is_err: ${is_err($bad)}  code: ${err_code($bad)}")
```
```
success returns true: true
failure is_err: true  code: 5
```

Array arguments are flattened one level into the argument list. That lets you build an argv list as data and pass it as a single value rather than splatting it by hand.

```drang
$args := ["/c", "echo", "hello"]
$ok := run("cmd", $args)
say($"splat ok: $ok")
```
```
hello
splat ok: true
```

A note on argument coercion: a non-string scalar is stringified before launch, so `run(123)` runs the command named `"123"` (which then fails to start, code 127). Passing a `regex` where a command or argument string is expected is the one structural misuse that aborts the program rather than returning an `Err`.

### `capture`: collect stdout as a string

`capture(...)` buffers the child's stdout and returns it as a trimmed string on success. On failure it returns an `Err` with the child's stderr folded into the message, so the reason a command failed travels with the error.

```drang
$where := capture("where", "cmd")
say($"where cmd -> $where")
```
```
where cmd -> C:\Windows\System32\cmd.exe
```

When the child fails, the stderr text is in the error message and the exit code is in `err_code`:

```drang
$r := capture("cmd", "/c", "echo boom 1>&2& exit 1")
say($"is_err: ${is_err($r)}  code: ${err_code($r)}")
say(err_msg($r))
```
```
is_err: true  code: 1
cmd exited with code 1: boom
```

(If a child floods more than 256 MiB to stdout, `capture` gives up with a catchable `Err` of code 137 rather than exhausting memory.)

### `capture_all`: outcome as data, never an error

Sometimes a non-zero exit is not a failure. `findstr` returns 1 when it finds nothing; a diff tool returns 1 when files differ. For these, use `capture_all`, which always returns a map `{out, err, code, ok}` and treats a non-zero exit as ordinary data. It never returns an `Err` for a normal run.

```drang
$r := capture_all("cmd", "/c", "echo hi& exit 3")
say($"out=${r.out} code=${r.code} ok=${r.ok}")
```
```
out=hi code=3 ok=false
```

Even a timeout or a can't-start shows up as data here: the map's `code` becomes `124` or `127` respectively, and `ok` is `false`. You get the full picture in one value and decide for yourself what counts as failure.

### `pipe`: a real pipeline, still no shell

`pipe([cmd, args...], [cmd, args...], ..., {opts}?)` wires each stage's stdout into the next stage's stdin through real operating-system pipes. The data streams between stages rather than buffering fully at each step, and it returns the last stage's trimmed stdout. Each stage is an array; there is still no shell anywhere in the chain.

```drang
$out := pipe(["cmd", "/c", "echo apple& echo banana& echo cherry"],
             ["findstr", "an"])
say($"pipe -> $out")
```
```
pipe -> banana
```

The exit semantics follow a standard pipeline: `127` if any stage cannot start, `124` on timeout, otherwise the last stage's exit code. A stage that cannot start fails the whole pipe:

```drang
$r := pipe(["cmd", "/c", "echo hi"], ["no_such_filter_xyz"])
say($"is_err: ${is_err($r)}  code: ${err_code($r)}")
```
```
is_err: true  code: 127
```

### Options: a trailing map on every exec form

`run`, `capture`, `capture_all`, `pipe`, `stream_lines`, and `start` all accept a trailing map of per-command options. This one block covers the ones you reach for most: working directory, environment, stdin, and stderr merging.

```drang
$dir := capture("cmd", "/c", "cd", {cwd: "C:\\Windows"})
say($"cwd -> $dir")
$e := capture("cmd", "/c", "echo", "%GREETING%", {env_add: {GREETING: "hi there"}})
say($"env_add -> $e")
$forced := {
  PATH: env("PATH"),
  SystemRoot: env("SystemRoot"),
  GREETING: "only this",
}
$f := capture("cmd", "/c",
  "if defined USERNAME (echo inherited) else (echo forced:%GREETING%)",
  {env_exact: $forced})
say($"env_exact -> $f")
$s := capture("findstr", "world", {stdin: "hello\nworld\nfoo\n"})
say($"stdin -> $s")
```
```
cwd -> C:\Windows
env_add -> hi there
env_exact -> forced:only this
stdin -> world
```

A few things worth knowing before you reach for these.

**`cwd`** is the only way to set a child's working directory. There is no global `cd` in drang, on purpose: a process-wide directory change would race between goroutines running commands in parallel. So the working directory is always per-command. A `cwd` that doesn't exist is a clean, catchable `Err` naming the missing path, not a launcher crash.

**`env_add` versus `env_exact`.** These are two different models, and you almost always want the first.

- `env_add` is an overlay. The child inherits your whole environment, and the keys you give replace or add to it. Windows environment-variable names are matched case-insensitively, so setting `GREETING` is visible as `%Greeting%` in the child.
- `env_exact` sets the child's environment to exactly what you provide and nothing else. Even `PATH` and `SystemRoot` are dropped unless you put them in. That is why the example above copies `PATH` and `SystemRoot` in by hand: a child launched with a bare `env_exact` and no `PATH` cannot resolve a bare command name and often cannot even start. Reach for `env_exact` only when you truly need a hermetic environment; otherwise `env_add` is the right tool.

The case-insensitive matching is real:

```drang
$v := capture("cmd", "/c", "echo %Greeting%", {env_add: {GREETING: "matched"}})
say($"case-insensitive env -> $v")
```
```
case-insensitive env -> matched
```

**Feeding stdin, and merging stderr.** `{stdin: "..."}` feeds a string to the child. `{stdin_file: path}` pipes a file straight in without copying it through drang, which is the right choice for large inputs; it cannot be combined with `stdin`. `{merge_stderr: true}` folds the child's stderr into its stdout, so both streams arrive together:

```drang
say(capture("cmd", "/c", "echo out& echo err 1>&2", {merge_stderr: true}))
```
```
out
err
```

**Die-with-parent.** Every child drang launches runs inside a Windows Job Object configured to kill its contents when the job closes. If drang exits, crashes, or is itself killed, the child and its entire descendant tree are terminated too, and this is enforced by the kernel rather than by best-effort cleanup. A child cannot escape by spawning grandchildren: the whole tree belongs to the job. For the synchronous forms (`run`, `capture`, `capture_all`, `pipe`, `stream_lines`), this is always on, so a blocking call never leaves an orphan behind.

**Resource limits.** A child, and its whole descendant tree, can be capped in memory, CPU time, and process count, all kernel-enforced through the same Job Object. Every option is an optional non-negative integer.

| Option | Unit | Scope |
|---|---|---|
| `max_memory` | bytes | committed memory, per process |
| `max_job_memory` | bytes | committed memory, whole job (child and every descendant) |
| `max_cpu` | milliseconds | user CPU time, per process |
| `max_job_cpu` | milliseconds | user CPU time, whole job |
| `max_job_procs` | count | concurrent processes allowed in the job |

A breach terminates the offending child (for a job-wide cap, the whole tree) with exit code `137`, and the error message names the cap that tripped. A runaway build or a fork-bomb becomes ordinary catchable data instead of a machine-swamping event.

```drang
# a busy loop capped at 100 ms of user CPU; the breach kills it with code 137
$r := capture_all("cmd", "/c", "for /L %i in (1,1,100000000) do @rem", {max_cpu: 100})
say($"code=${r.code} ok=${r.ok}")
```
```
code=137 ok=false
```

### Error codes: 124, 127, 137

drang synthesizes three exit codes so that timeouts, launch failures, and kills are distinguishable from any code the child itself might return.

**`124` — timeout.** `{timeout}` is a wall-clock cap in milliseconds, where `0` means no limit. On a breach the whole process tree is killed, so a `cmd /c` wrapper whose grandchild is holding the pipe open cannot keep the call blocked:

```drang
$r := run("cmd", "/c", "ping -n 5 127.0.0.1 >NUL", {timeout: 300})
say($"is_err: ${is_err($r)}  code: ${err_code($r)}")
```
```
is_err: true  code: 124
```

**`127` — cannot start.** The command was not found or is not executable. The message carries the underlying reason:

```drang
$r := run("no_such_program_xyz")
say($"code: ${err_code($r)}")
say(err_msg($r))
```
```
code: 127
no_such_program_xyz: exec: "no_such_program_xyz": executable file not found in %PATH%
```

**`137` — killed or limit breach.** Either you called `kill`, or the child breached a resource cap. Here a still-running child is killed and its status inspected:

```drang
$p := start("cmd", "/c", "ping -n 30 127.0.0.1 >NUL")
kill($p)
say($"code: ${err_code(await($p))}")
```
```
code: 137
```

Because these are ordinary codes, `err_code` reads them the same way it reads a child's own exit code, and you branch on them like any other value.

### `stream_lines`: process output as it arrives

`stream_lines(cmd, args..., {opts}?, |$line| { ... })` invokes your callback once for each line of stdout, as the line arrives, with the trailing newline stripped. Nothing is buffered up front, which is what you want for a build log or a `tail`-style follow. It returns `true` on success, or an `Err` (a non-zero exit code, or `124` on timeout) once the command finishes.

```drang
$n := 0
stream_lines("cmd", "/c", "echo one& echo two& echo three", |$line| {
  $n = $n + 1
  say($"[$n] $line")
})
say($"total lines: $n")
```
```
[1] one
[2] two
[3] three
total lines: 3
```

The callback closes over its surrounding scope, so an outer counter or accumulator like `$n` is visible and updatable from inside it.

### `start`: a detached process handle

Everything above waits for the child. `start(cmd, args...)` does not: it launches the child, detaches its stdio, and returns immediately with a `process` handle. This is the background-launch form. Because the child is detached, its output no longer flows to your terminal, and if you want to observe what it produced you route it somewhere (a file) yourself.

Several builtins act on the handle. `pid(p)` reads the operating-system process id. `await(p)` blocks until the child exits and returns `true` on a clean exit or an `Err` carrying the code otherwise. `kill(p)` terminates the child and its whole tree.

```drang
$p := start("cmd", "/c", "exit 3")
say($"pid > 0: ${pid($p) > 0}")
$status := await($p)
say($"await is_err: ${is_err($status)}  code: ${err_code($status)}")
```
```
pid > 0: true
await is_err: true  code: 3
```

`kill` works on a running child; the pending `await` then reports a kill (code `137`). `kill` is idempotent on an already-exited child.

```drang
$p := start("cmd", "/c", "ping -n 30 127.0.0.1 >NUL")
kill($p)
say($"after kill, is_err: ${is_err(await($p))}")
```
```
after kill, is_err: true
```

**Poll without blocking.** `status(p)` reports on a child without waiting for it. It always returns the same four keys, `{running, ok, code, pid}`, so you never test for a missing one. While the child is alive, `running` is `true`, `ok` is `false`, and `code` is the sentinel `-1`. Once it exits, `ok` and `code` carry the real outcome (the same shape `capture_all` gives). `pid` is present throughout.

```drang
$p := start("cmd", "/c", "ping -n 30 127.0.0.1 >NUL")
$s := status($p)
say($"running=${s.running} ok=${s.ok} code=${s.code}")
kill($p)
```
```
running=true ok=false code=-1
```

After the child has exited, the outcome fields are populated:

```drang
$p := start("cmd", "/c", "exit 0")
await($p)
$s := status($p)
say($s.running, $s.ok, $s.code)
```
```
false true 0
```

**Drive a live child's stdin.** Launch with `{stdin_pipe: true}`, push input with `send_stdin(p, s)`, and signal end-of-input with `close_stdin(p)`. This feeds a long-running filter incrementally. Below, `sort` emits its result only after its input closes; since a started child's stdout is detached, the example routes `sort` to a file and reads it back so the result is observable.

```drang
$p := start("cmd", "/c", "sort > sorted.txt", {stdin_pipe: true})
send_stdin($p, "banana\n")
send_stdin($p, "apple\n")
send_stdin($p, "cherry\n")
close_stdin($p)
await($p)
say(read_file("sorted.txt"))
```
```
apple
banana
cherry
```

**`supervise`.** A plain `start` outlives drang: the detached child keeps running after your program returns. `{supervise: true}` extends the die-with-parent tie to a detached child, so a supervised background process is guaranteed to go down when drang does, kernel-enforced, whether drang finishes cleanly, crashes, or is killed. A clean exit takes a supervised child down too, and that is exactly the point: use it for a helper that must never be left orphaned.

```drang
$p := start("cmd", "/c", "exit 0", {supervise: true})
say($"supervised start ok: ${pid($p) > 0}")
await($p)
```
```
supervised start ok: true
```

For a background child that should outlive drang, use a plain `start` with no `supervise`.

### Which option belongs to which form

Three options are tied to a specific form, and getting this wrong is treated as a programming mistake rather than a recoverable condition.

**`{stdin_pipe}` and `{supervise}` are `start`-only.** They exist to drive or supervise a detached process. Using either on a synchronous form (`run`, `capture`, `capture_all`, `pipe`, `stream_lines`) aborts the program with a clear message; it is not a catchable `Err`, and `//` will not rescue it. The reasoning is that a synchronous call already blocks until the child exits, so supervising it is meaningless, and it has no live handle through which to push stdin. Feed a synchronous child with `stdin` or `stdin_file` instead.

```drang
$r := capture("cmd", "/c", "echo hi", {supervise: true})
say("reached")
```
```
drang: capture: supervise is only for start() (it ties a detached child's lifetime to drang)
  at prog.dr:1:7
    $r := capture("cmd", "/c", "echo hi", {supervise: true})
          ^
```

**`{timeout}` is rejected on `start`,** because a detached process is meant to run unbounded. Here the rejection is different: it is a catchable `Err`, so you can recover from it, since a timeout is a value-level request that simply doesn't apply to a detached launch.

```drang
$r := start("cmd", "/c", "exit 0", {timeout: 500})
say($"is_err: ${is_err($r)}")
say(err_msg($r))
```
```
is_err: true
start does not accept {timeout}: a started process is detached and runs unbounded
```

## In-language concurrency

drang runs work across all your CPU cores in genuine parallel, and it does so without a single lock in your code. That is not luck. It is the payoff of a language deliberately built with almost nothing to share between parallel workers: top-level bindings are frozen constants, scoping is lexical only, strings are immutable, and there is no shared mutable global state. When two workers cannot reach the same mutable object, there is nothing to guard, so there is nothing to lock.

You might expect parallelism here to be cooperative or interleaved on one core. It is not. Spawned tasks and `pmap` workers occupy real OS threads and run at the same instant on different cores. The sections below show that speedup measured, not promised.

The rule that makes it safe is uniform: values cross into a parallel worker by copy, never by reference. A worker gets its own private duplicate of every argument and every element it processes, so one worker's mutations are invisible to every other worker and to the original data. The single exception is the channel, the one value type intentionally designed to be shared, which is how workers talk to each other on purpose.

### `spawn` and `await`: tasks

`spawn(fn, args...)` runs a drang function on its own thread and hands you back a `task` immediately, without waiting. `await(task)` blocks until that task finishes and gives you its result. The arguments are deep-copied into the task as it starts, over a snapshot of the surrounding bindings, so the task cannot observe later changes to the caller's variables and the caller cannot see into the task's private state.

The natural pattern is fan-out then fan-in: launch every task, then collect every result.

```drang
fn .work($n) { $n * 2 }
$tasks := [1, 2, 3, 4] |> map(|$n| spawn(.work, $n))
$results := $tasks |> map(|$t| await($t))
say($"fan-out: $results")
```
```
fan-out: [2, 4, 6, 8]
```

The first `map` starts all four tasks; the second `map` waits on each. Because the tasks run concurrently, the total wall time is roughly that of the slowest one, not the sum of all four.

An error inside a spawned task does not crash the program at the moment it happens. Whether the task returns an `Err`, propagates one with `?`, or panics, the failure is captured and delivered by `await` as an ordinary error value. So the usual recovery vocabulary applies at the join point: `await($t)?` re-propagates the task's failure into the caller, and `await($t) // fallback` recovers from it.

```drang
fn .boom() { fail("worker failed") }
$res := await(spawn(.boom))
say($"is_err: ${is_err($res)}  msg: ${err_msg($res)}")
```
```
is_err: true  msg: worker failed
```

`await` is idempotent: awaiting the same task again returns the same result. It also accepts a `process` handle from `start`, so a single `await` waits on either kind of asynchronous work. For a started process it returns `true` on a clean exit, or an `Err` carrying the exit code otherwise.

### Channels: `chan`, `send`, `recv`, `recv_ok`, `close`, `drain`

Channels are the one place drang lets two workers reach the same object on purpose. A channel is a typed conduit: one side sends values in, the other side receives them out. Passing a channel to a spawned task shares the same channel (a channel's copy is itself), which is exactly what makes it a communication line rather than a per-worker duplicate.

- `chan()` makes an unbuffered channel; `chan(n)` makes one buffered to capacity `n`.
- `send(ch, v)` puts a copy of `v` onto the channel, blocking until there is room or a receiver takes it.
- `recv(ch)` blocks for the next value.
- `recv_ok(ch)` is `recv` plus a flag: it returns `[value, ok]`.
- `close(ch)` marks the channel finished; it is idempotent and safe to call from any thread.
- `drain(ch)` collects every remaining value into an array, blocking until the channel is closed.

Values are copied on `send`, so once a value is on the channel the sender can keep mutating its own copy without disturbing the receiver.

A producer thread feeding a channel that the main thread drains:

```drang
$c := chan(3)
fn .produce($ch) {
  for $i in 1..3 { send($ch, $i * 10) }
  close($ch)
}
$t := spawn(.produce, $c)
$all := drain($c)
await($t)
say($"drained: $all")
```
```
drained: [10, 20, 30]
```

`drain` returns only after `close`, which is why the producer closes the channel when it is done. The `await($t)` afterward is just tidy joining; the drain already guaranteed the producer finished sending.

Receiving one value at a time shows what `recv` and `recv_ok` return, and what happens once the channel is exhausted:

```drang
$c := chan()
fn .worker($ch) {
  send($ch, "first")
  send($ch, "second")
  close($ch)
}
$t := spawn(.worker, $c)
say($"recv: ${recv($c)}")
$pair := recv_ok($c)
say($"recv_ok: $pair")
say($"after close, empty: ${not recv($c)}")
await($t)
```
```
recv: first
recv_ok: [second, true]
after close, empty: true
```

Once a channel is closed and every value has been taken, `recv` stops blocking and yields drang's empty value: it has type `nil`, renders as `nil`, and is falsy, so `not recv($c)` is `true` at exhaustion. This is a signal, not a sentinel you write in your own code. To distinguish a real received `nil` from end-of-channel, use `recv_ok`, whose `ok` flag is `false` only when the channel is closed and drained.

drang refuses to let a channel silently hang your program. A `send` or `recv` that could only ever deadlock, because there is no counterparty and no other task is running to become one, does not freeze: it returns a catchable `Err`. So a lone `send(chan(), x)` on the main thread with nothing to receive it fails as data you can recover, rather than stalling forever:

```drang
$r := send(chan(), "orphan") // "no reader"
say($"recovered: $r")
```
```
recovered: no reader
```

Sending on a closed channel is likewise a catchable `Err`, never a crash. Both failures carry a plain message you can inspect:

```drang
$c := chan(1)
close($c)
$closed := send($c, "y")
say($"is_err: ${is_err($closed)}  msg: ${err_msg($closed)}")
```
```
is_err: true  msg: send on a closed channel
```

### `pmap`: parallel map across CPU cores

`pmap(arr, fn)` is the high-level way to parallelize, and the one you will reach for most. It has the same contract as `map`, so switching a serial `map` to a parallel `pmap` is usually a one-word edit:

- array-first, so `$xs |> pmap(f)` composes in a pipeline;
- the callback takes the element and, optionally, its index;
- results come back in **input order**, regardless of which worker finished first;
- it is **fail-loud**: the first `Err` any callback produces becomes the whole result and stops further work.

What `pmap` adds is a bounded pool of workers, one per CPU as reported by the machine's core count, running the callback in true parallel.

```drang
$squares := [1, 2, 3, 4, 5] |> pmap(|$x| $x * $x)
say($"pmap squares: $squares")
```
```
pmap squares: [1, 4, 9, 16, 25]
```

The optional second callback parameter is the index, and results stay ordered by input even when workers finish out of order:

```drang
$labeled := ["a", "b", "c"] |> pmap(|$x, $i| $"$i:$x")
say($"labeled: $labeled")
```
```
labeled: [0:a, 1:b, 2:c]
```

**The speedup is real, not cooperative.** Here four elements each burn about two seconds of subprocess wall time, run first with `map` and then with `pmap`, timed with `now()` end to end:

```drang
fn .busy($n) { capture("ping", "-n", "3", "127.0.0.1"); $n }

$t0 := now()
$serial := [1, 2, 3, 4] |> map(.busy)
$t1 := now()
$par := [1, 2, 3, 4] |> pmap(.busy)
$t2 := now()

say($"serial   (map):  ${round(($t1 - $t0) * 100) / 100}s")
say($"parallel (pmap): ${round(($t2 - $t1) * 100) / 100}s")
```
```
serial   (map):  8.19s
parallel (pmap): 2.04s
```

Four two-second jobs take eight seconds one after another and two seconds all at once. That factor is the core count at work.

**The purity contract.** A `pmap` callback must be pure. It may read frozen top-level constants and its own parameters, and it must not mutate state shared with other workers. This is not a discipline you have to enforce by hand; the language mostly enforces it for you. Each element is deep-copied to its worker, so mutating the element changes only that worker's private copy and never touches the original array:

```drang
$rows := [[1], [2], [3]]
$out := pmap($rows, |$row| {
  push($row, 99)   # mutates this worker's private copy
  len($row)
})
say($"callback saw lengths: $out")
say($"original rows unchanged: $rows")
```
```
callback saw lengths: [2, 2, 2]
original rows unchanged: [[1], [2], [3]]
```

Each worker saw a two-element array (its copy, with `99` pushed on), while the source `$rows` is untouched. There is deliberately no shared accumulator to reduce into, so the classic racy pattern of many threads writing one collector is largely unwriteable. Collect each callback's return value instead, which `pmap` already does for you in order. Passing a constant container (declared with `::=`, deep-frozen) into a callback is always safe. Mutating a captured mutable container declared with `:=` from inside a parallel callback is documented-undefined; keep callbacks pure and this never arises.

`pmap` inherits `map`'s fail-loud behavior. The first `Err` a callback produces becomes the entire result, and remaining work stops:

```drang
$r := pmap([1, 2, 3], |$x| {
  if $x == 2 { fail("boom on 2") } else { $x }
})
say($"is_err: ${is_err($r)}  msg: ${err_msg($r)}")
```
```
is_err: true  msg: boom on 2
```

Because each worker runs lock-free with its own copies, running many subprocesses in parallel is just `pmap` over commands. Each call carries its own `{timeout}`, `cwd`, or `env_exact`, and a per-element `//` recovers a missing tool without sinking the batch:

```drang
$versions := ["git", "go", "cmd"] |> pmap(|$tool| capture($tool, "--version") // "(missing)")
say($"captured: ${len($versions)}")
```
```
captured: 3
```

## Files and paths

Paths in drang are ordinary strings. There is no path object and no handle type: you pass a string in, and you get a string (or an array, or a bool) back. The builtins split into four groups by how they behave and how they fail.

- **File I/O**: `read_file`, `write_file`, `lines`.
- **Filesystem operations**: `exists`, `is_dir`, `mkdir`, `glob`, `read_dir`, `rename`, `rm`, `copy`, `size`, `tempfile`, `tempdir`.
- **Pure path transforms**: `path_join`, `dirname`, `basename`, `ext`, `stem`, `abs_path`, `to_slash`, `is_abs`, `clean`, `rel`, `is_within`, `path_list_sep`.
- **Freshness gates** for build scripts: `mtime`, `newer`, `stale`.

Here is the whole surface in miniature. It builds a scratch directory under the system temp, writes a file, reads it back, globs for it, and cleans up. Every line ran end to end.

```drang
# A scratch dir under the system temp, cleaned up at the end.
$dir := path_join($ENV["TEMP"], "drang_fs_tour")
rm($dir)              # idempotent: no error if absent
mkdir($dir)           # creates the whole tree, like mkdir -p

$f := path_join($dir, "notes.txt")
write_file($f, "alpha\nbeta\ngamma\n")

say("exists : " ~ exists($f))
say("size   : " ~ size($f))
say("lines  : " ~ len(lines(read_file($f))))

for $m in glob(path_join($dir, "*.txt")) {
  say("glob   : " ~ basename($m))
}

rm($dir)              # tidy up: nothing left behind
say("gone   : " ~ !exists($dir))
```

```
exists : true
size   : 17
lines  : 3
glob   : notes.txt
gone   : true
```

Two conventions recur. `path_join(...)` assembles path segments with the correct native separator, and `~` concatenates strings. Reach for `path_join` rather than gluing strings with `~`: it cleans `.` and `..` segments and collapses stray separators.

### The error model: fallible I/O versus pure transforms

This is the one thing to internalize before using these builtins. drang divides the whole surface by how it reports failure, and the division is deliberate.

**Fallible filesystem operations do not throw.** A missing file, a permission denial, a bad glob pattern: none of these abort your program. The builtin returns a catchable `Err` value, and you decide what happens next. There are three ways to handle it.

- `expr?` propagates: if `expr` is an `Err`, the program aborts with that message and a non-zero exit.
- `expr // fallback` recovers: `fallback` is substituted whenever `expr` is an `Err`.
- Or let the `Err` flow onward as an ordinary value and inspect it later with `is_err`.

Recovery with `//` is the common case for reads that might legitimately be absent:

```drang
$txt := read_file("does_not_exist_xyz.txt") // "DEFAULT"
say("recovered: " ~ $txt)            # recovered: DEFAULT
```

Propagation with `?` is for failures you want to be fatal. The program stops, prints the underlying OS error, and points at the call site:

```drang
read_file("nope_missing.txt")?
say("unreached")
```

```
drang: read_file nope_missing.txt: open nope_missing.txt: The system cannot find the file specified.
  at propagate.dr:1:1
    read_file("nope_missing.txt")?
    ^
```

**Pure path transforms never touch the disk and never fail on a well-typed argument.** `dirname`, `basename`, `ext`, `stem`, `to_slash`, `path_join`, `is_abs`, and `clean` are string math. They cannot report "file not found" because they never look at the filesystem.

**Stat guards always return a plain `bool`.** `exists`, `is_dir`, and `is_within` never yield an `Err`: an unstattable, missing, or uncomparable path is simply `false`. That is what lets them drop directly into `if` and `unless` without any recovery plumbing:

```drang
if exists("no_such_path") {
  say("here")
} else {
  say("missing, handled inline")
}
say(is_dir("no_such_path"))
```

```
missing, handled inline
false
```

Two failure modes cut across all four groups. A wrong argument **type** (passing a number where a path string is expected) is a catchable `Err`, consistent with the rest of the language:

```drang
say(is_err(basename(42)))            # true
say(basename(42) // "recovered")     # recovered
```

A wrong argument **count**, by contrast, is a hard abort that no `?` or `//` can catch. It signals a bug in your script, not a runtime condition to handle. One further caveat: these builtins are shadowed by like-named variables, so binding `$newer` masks the `newer` builtin for the rest of that scope.

### File I/O: read_file, write_file, lines

`read_file(path)` returns the whole file as one string, or an `Err` if it is missing or unreadable. There is a 1 GiB backstop: a file larger than that returns an `Err` rather than exhausting memory.

`write_file(path, content, opts?)` writes `content` to `path`, creating or truncating it, and returns the path. `content` need not be a string. Any value is rendered the way `say` would render it, so a string writes its raw bytes and a number writes its digits:

```drang
$f := path_join(tempdir(), "n.txt")
write_file($f, 42)
say(read_file($f))                   # 42
rm(dirname($f))
```

The optional third argument is a map, and its only permitted key is `append`. With `{append: true}` the file is opened for appending instead of being truncated. Any other key in that map is a catchable `Err`.

```drang
$d := tempdir()
$log := path_join($d, "run.log")
write_file($log, "first\n")
write_file($log, "second\n", {append: true})
for $ln in lines(read_file($log)) { say($ln) }
rm($d)
```

```
first
second
```

`lines(text)` splits a **string** into an array of lines. It is not a file reader. It normalizes CRLF to LF and drops a single trailing newline, which is what you want when the last line ends in `\n`. Pair it with `read_file` to iterate a file's lines: `lines(read_file(path))`.

```drang
say(len(lines("")))        # 0
say(len(lines("a\nb\n")))  # 2   (trailing newline dropped)
say(len(lines("a\nb")))    # 2
```

`tempfile(prefix?)` and `tempdir(prefix?)` create a uniquely-named empty file or directory in the system temp area and return its path. The default prefix is `drang`, and the unique suffix is appended after a `-`. Remove either with `rm` when you are done.

```drang
$f := tempfile()
$d := tempdir("build")
say("file basename : " ~ basename($f))   # e.g. drang-307730866
say("dir  basename : " ~ basename($d))   # e.g. build-1231652904
say("dir  is_dir   : " ~ is_dir($d))     # true
rm($f); rm($d)
```

### Filesystem operations

- `exists(p)` returns a bool: true if the path exists.
- `is_dir(p)` returns a bool: true only if `p` exists and is a directory.
- `mkdir(p)` creates `p` and any missing parent directories, then returns `p`. Creating an existing directory is not an error.
- `glob(pattern)` returns a sorted array of matching paths. No match is an empty array, not an error. It supports `*`, `?`, `[...]`, and a recursive `**` segment that spans directories.
- `read_dir(p)` lists a directory as an array of records, one per entry, sorted by name.
- `rename(src, dst)` moves or renames and returns `dst`.
- `copy(src, dst)` copies a single file, or recursively copies a directory tree, preserving file modes and creating any needed parent directories of `dst`. It returns `dst`.
- `rm(p)` removes a file or an entire directory tree, recursively and idempotently. A path that does not exist is not an error. It is named `rm` because `delete` is reserved for removing a map key.
- `size(p)` returns the file size in bytes as an int, or an `Err` if the path is missing.

Copy, rename, and remove in one pass:

```drang
$dir := path_join($ENV["TEMP"], "drang_fs_moves")
rm($dir)
mkdir($dir)

$src := path_join($dir, "src.txt")
write_file($src, "hello")

copy($src, path_join($dir, "copy.txt"))
rename(path_join($dir, "copy.txt"), path_join($dir, "renamed.txt"))

say("orig    : " ~ exists($src))
say("copy    : " ~ exists(path_join($dir, "copy.txt")))
say("renamed : " ~ exists(path_join($dir, "renamed.txt")))
rm($dir)
```

```
orig    : true
copy    : false
renamed : true
```

`read_dir` returns records with three fields: `name` (the bare entry name), `path` (the full joined path), and `is_dir`. Entries are sorted by name.

```drang
$dir := path_join($ENV["TEMP"], "drang_readdir")
rm($dir)
mkdir($dir)
mkdir(path_join($dir, "sub"))
write_file(path_join($dir, "a.txt"), "")
write_file(path_join($dir, "b.log"), "")

for $e in read_dir($dir) {
  say($e.name ~ "  is_dir=" ~ $e.is_dir)
}
rm($dir)
```

```
a.txt  is_dir=false
b.log  is_dir=false
sub  is_dir=true
```

A `**` glob walks subdirectories. Results stay sorted, and the walk root itself is never yielded. A pattern that matches nothing is an empty array, so you can loop over it without guarding:

```drang
$dir := path_join($ENV["TEMP"], "drang_recglob")
rm($dir)
mkdir(path_join($dir, "sub"))
write_file(path_join($dir, "top.go"), "")
write_file(path_join($dir, "sub", "deep.go"), "")

say("empty : " ~ len(glob(path_join($dir, "*.rs"))))   # no match -> []

for $m in glob(path_join($dir, "**", "*.go")) {
  say(to_slash($m))
}
rm($dir)
```

```
empty : 0
C:/Users/anafa/AppData/Local/Temp/drang_recglob/sub/deep.go
C:/Users/anafa/AppData/Local/Temp/drang_recglob/top.go
```

### Pure path helpers

These are string transforms. They never read the disk, and on a well-typed argument they never fail. A non-string argument is a catchable `Err`, like every other builtin's wrong-type check, and a wrong argument count is still a hard abort. On Windows they return the native separator (`\`) unless the helper's whole job is to change it.

| Builtin | Input | Result |
|---|---|---|
| `dirname(p)` | `C:/Users/anafa/tmp/notes.txt` | `C:\Users\anafa\tmp` (the directory) |
| `basename(p)` | `.../notes.txt` | `notes.txt` |
| `ext(p)` | `.../notes.txt` | `.txt` (last extension, dot included) |
| `stem(p)` | `.../notes.txt` | `notes` (basename minus last extension) |
| `abs_path(p)` | `foo/bar.txt` | absolute path against the CWD (numeric absolute value is `abs`) |
| `to_slash(p)` | `C:\a\b` | `C:/a/b` (forward slashes) |
| `is_abs(p)` | `C:/x` vs `x` | `true` vs `false` |
| `clean(p)` | `a/b/../c/./d.txt` | `a\c\d.txt` (lexical simplify) |

```drang
$f := "C:/Users/anafa/tmp/notes.txt"
say(dirname($f))          # C:\Users\anafa\tmp
say(basename($f))         # notes.txt
say(ext($f))              # .txt
say(stem($f))             # notes
say(to_slash(dirname($f))) # C:/Users/anafa/tmp
say(is_abs($f))           # true
say(clean("a/b/../c/./d.txt"))  # a\c\d.txt
```

`ext` and `stem` split on the last dot only, so a doubled extension is not fully separated:

```drang
say(ext("archive.tar.gz"))    # .gz
say(stem("archive.tar.gz"))   # archive.tar
```

Note that `dirname` and `clean` return the native `\` separator. Reach for `to_slash` whenever you want stable forward-slash output for logging or comparison across machines.

Three more helpers deal with relationships between paths. `rel(base, target)` gives the path from `base` to `target`. `is_within(base, target)` is a stat guard returning bool: true when `target` sits inside or equals `base`, and false for an escaping `../` path or an uncomparable pair (different volumes). `path_list_sep()` returns the separator used in `PATH`-style lists, which is `;` on Windows.

```drang
say(is_within("C:/proj", "C:/proj/src/main.c"))   # true
say(is_within("C:/proj", "C:/other/x"))           # false
say(is_within("C:/proj", "C:/proj/../secret"))    # false (escapes base)
say(to_slash(rel("C:/proj", "C:/proj/src/a.c")))  # src/a.c
say(path_list_sep())                               # ;
```

### Freshness helpers for build scripts

These three power the classic "rebuild only if out of date" pattern.

- `mtime(p)` returns the modification time as float Unix seconds, with sub-second precision (the same unit as `now()`), or an `Err` if the path is missing.
- `newer(a, b)` returns a bool: is `a`'s modification time strictly after `b`'s? Both paths must exist; a missing operand is a catchable `Err`.
- `stale(target, sources)` returns a bool: does `target` need rebuilding? It is true when `target` is missing, or when `target` is older than any source. `sources` may be a single path or an array of paths.

Watch the state flip across a build. The object is missing (stale), gets built (fresh), then the source is edited (stale again). The `sleep` calls only exist to guarantee visibly distinct timestamps in this demo.

```drang
$dir := path_join($ENV["TEMP"], "drang_fresh")
rm($dir)
mkdir($dir)
$src := path_join($dir, "main.c")
$obj := path_join($dir, "main.o")

write_file($src, "int main(){}")
say("obj missing -> stale : " ~ stale($obj, $src))

sleep(0.05)
write_file($obj, "<compiled>")                 # "build" the object
say("just built  -> stale : " ~ stale($obj, $src))

sleep(0.05)
write_file($src, "int main(){ return 0; }")    # edit the source
say("src edited  -> stale : " ~ stale($obj, $src))
say("src newer than obj   : " ~ newer($src, $obj))
rm($dir)
```

```
obj missing -> stale : true
just built  -> stale : false
src edited  -> stale : true
src newer than obj   : true
```

In practice you feed `stale` a glob of sources and gate the compile on it:

```drang
$dir := path_join($ENV["TEMP"], "drang_stalearr")
rm($dir)
mkdir(path_join($dir, "src"))
write_file(path_join($dir, "src", "a.c"), "")
write_file(path_join($dir, "src", "b.c"), "")
$obj := path_join($dir, "app.o")   # not built yet

$srcs := glob(path_join($dir, "src", "**", "*.c"))
say("sources found : " ~ len($srcs))
if stale($obj, $srcs) {
  say("rebuilding " ~ basename($obj))
}
rm($dir)
```

```
sources found : 2
rebuilding app.o
```

One subtlety in `stale` worth knowing, because it affects error handling. `stale` short-circuits: if the target is missing it returns `true` immediately and never looks at the sources. A missing source is therefore only reported as an `Err` when the target actually exists (and so the sources are consulted). When both the target and a source are missing, you get `true`, not an error.

```drang
$dir := path_join($ENV["TEMP"], "drang_staleprobe")
rm($dir)
mkdir($dir)
$obj := path_join($dir, "app.o")
$missing := path_join($dir, "gone.c")

write_file($obj, "built")            # target EXISTS -> sources are consulted
say("target present : " ~ is_err(stale($obj, $missing)))   # true (Err)

rm($obj)                             # target MISSING -> short-circuits
say("target missing : " ~ is_err(stale($obj, $missing)))   # false
say("value          : " ~ stale($obj, $missing))            # true
rm($dir)
```

```
target present : true
target missing : false
value          : true
```

On timestamp precision: `mtime` returns sub-second float seconds, and `newer` and `stale` compare the full timestamps, so files written back to back are still distinguished on NTFS (roughly 100 ns resolution). The ceiling is the filesystem itself; older formats such as FAT resolve only to two seconds, which can make near-simultaneous writes compare equal.

## Persistent storage

Most scripts are amnesiac: they run, do their work, and forget everything. A glue script that runs on a schedule usually wants the opposite — to remember where it left off. A log tailer wants the last offset it read; a sync job wants the files it already copied; a poller wants the last id it saw. drang gives that a first-class form: a **store**, a durable key-value map backed by a single JSON file.

Open a store with `store()`, then read and write it with the `store_` builtins. The value survives between runs:

```drang
$s := store("runs.store")
store_set($s, "n", store_get($s, "n", 0) + 1)
say("run " ~ str(store_get($s, "n")))
```

```
run 1
```

Run that script again and it prints `run 2`, then `run 3`. Keys are strings; values are any JSON-serializable drang value — a number, a string, an array, or a map. A value holding a channel, task, process, or function cannot be persisted and is rejected with a catchable error, the same way `to_json` would reject it.

### Where the file lives

`store()` with no argument puts its file in a `.drang/` subfolder next to the running script: a script at `C:\jobs\backup.dr` gets its store at `C:\jobs\.drang\backup.store`. The location is predictable, it travels with the script, and it never depends on an environment variable or a system-wide settings directory. Pass an explicit path to put the file wherever you want:

```drang
$s := store("data/app.store")   # relative to the working directory; parent dirs are created
```

Because the default is derived from the script's own path, a one-liner (`-e`) or piped stdin — which have no script file — must pass an explicit path.

The file is plain, pretty-printed JSON you can read, edit, or commit to version control, kept alongside a `.bak` copy of the previous snapshot and a `.lock` file:

```
{
  "n": 3
}
```

### Reading and writing

The core operations read like map access. A missing key reads as `nil`, or as a default you supply:

```drang
$s := store("kv.store")
store_set($s, "name", "ada")
say(store_get($s, "name"))         # ada
say(store_get($s, "missing"))      # nil
say(store_get($s, "missing", 0))   # 0
say(store_has($s, "name"))         # true
store_delete($s, "name")
say(store_keys($s))                # []
```

`store_all($s)` returns the whole store as an ordinary map, handy for iterating, and `store_clear($s)` empties it.

### Counters done right: `store_update`

The tempting way to bump a counter — read it, add one, write it back — has a subtle bug when two runs overlap: both read the same value and both write back the same increment, so one is lost. `store_update` closes that gap by doing the read-modify-write as one atomic step:

```drang
$s := store("hits.store")
store_update($s, "hits", 0, |$n| $n + 1)
store_update($s, "hits", 0, |$n| $n + 1)
say(store_get($s, "hits"))
```

```
2
```

The third argument is the starting value the function receives when the key is absent — here `0`, so the very first `store_update` sees `0` rather than a nil it would have to guard against (the argument order mirrors `reduce`). The function returns the new value. Because the whole step happens under the store's lock, counters and accumulators stay correct even when scheduled runs race — and across processes only one run holds a store at a time anyway: a second opener gets a catchable `store busy` error rather than silently clobbering the first.

### Grouping writes: `with_store`

Sometimes several writes must land together or not at all — advance a cursor *and* record its result, never one without the other. `with_store` batches every mutation inside its block into a single all-or-nothing commit:

```drang
$s := store("job.store")
with_store($s, |$s| {
  store_set($s, "cursor", 1000)
  store_set($s, "done", true)
})
say(store_get($s, "cursor"))
```

```
1000
```

If the block returns or propagates an error (or calls `fail`), the whole batch is rolled back and the store is left exactly as it was before it began:

```drang
$s := store("job.store")
fn .attempt($s) {
  store_set($s, "started", true)
  fail("network down")
}
$r := with_store($s, .attempt) // "recovered"
say($r)
say(store_has($s, "started"))
```

```
recovered
false
```

Nothing the failed batch wrote was committed.

### One store, many workers

A store handle is a shared thing, like a channel: hand it to a `spawn`ed task or a `pmap` worker and they all see the same store, with access serialized so nothing races. Mutating one store from many workers is safe but not a speed-up — the writes take turns. Reach for a store to *coordinate* parallel work (a shared tally, a set of seen ids), not to parallelize the writes themselves.

### Scope

A store is a small key-value checkpoint, not a database. There are no queries, no indexes, and no joins — one obvious operation per task. When you need those, reach for a real database through an external tool. When you just need a script to remember something between runs, a store is the whole answer.

## JSON

`from_json` parses a JSON document into drang values; `to_json` renders drang values back to JSON.

The mapping is direct. A JSON object becomes a drang map, and because drang maps preserve insertion order, key order round-trips unchanged. A JSON array becomes a drang array. A JSON number becomes an `int` when it is integral and a `float` otherwise. Strings, `true`/`false`, and `null` become the drang `string`, `bool`, and `nil` values.

```drang
$cfg := from_json("{\"name\": \"zmal\", \"tags\": [\"build\", \"test\"]}")
say($cfg.name)
say($cfg.tags |> len)
```

```
zmal
2
```

To serialize, build a value and pass it to `to_json`. A second argument controls indentation. With no second argument the output is compact. Pass an `int` for that many spaces per level, or pass a whitespace string to use as the literal indent unit (a tab, for instance):

```drang
$out := {}
$out["ok"] = true
$out["items"] = [1, 2]
say(to_json($out))
say(to_json($out, 2))
```

```
{"ok":true,"items":[1,2]}
{
  "ok": true,
  "items": [
    1,
    2
  ]
}
```

### Whole-valued floats stay floats

One display detail catches people out. A value like `3.0` is a `float`, and drang's normal display form prints a whole-valued float without a fractional part, as `3`. But `to_json` is not the display form: it emits a valid JSON number, so a `float` always carries its decimal point. A number that arrived as `3.0` leaves as `3.0`, preserving its type across a round trip.

```drang
$doc := from_json("{\"n\": 3.0}")
say($doc.n)            # display form drops the point
say(to_json($doc))     # JSON keeps it (still a float)
say(type($doc.n))
```

```
3
{"n":3.0}
float
```

If you need an integer instead, convert explicitly with `int` before serializing.

### Errors

Bad input from `from_json` is a catchable error value, not an abort. Malformed JSON, and a non-string argument, both yield an `Err` you can inspect with `is_err` or supply a default for with the `//` operator:

```drang
say(is_err(from_json("{ broken")))
say(from_json("nope") // "fallback")
```

```
true
fallback
```

`to_json` is catchable in the same way for two cases: a value it cannot encode (a function, say), and an indent argument of a disallowed type (a `float` or `bool`):

```drang
$fn := |$x| $x * 2
say(is_err(to_json($fn)))       # functions are not encodable
say(is_err(to_json({}, 2.5)))   # a float indent is rejected
```

```
true
true
```

Two indent mistakes abort instead of returning an `Err`, because they signal a programming error rather than bad data: an `int` indent outside the range 0 to 80, and a string indent containing non-whitespace characters.

```drang
say(to_json({}, 200))
```

```
drang: to_json indent count must be between 0 and 80, got 200
```

---

## CSV

`from_csv` parses RFC 4180 CSV into rows; `to_csv` renders rows back to CSV. The parser handles the awkward parts for you: fields that contain commas, quotes, or newlines, and the doubled-quote escape (`""`).

Every parsed field comes back as a string. There is no type inference, so convert numeric columns yourself with `int($row.age)` or `float($row.price)`.

By default rows are arrays of strings. Pass `{header: true}` and the first row names the columns; every later row becomes a map record keyed by those names:

```drang
say(from_csv("a,b\n1,2"))
$rows := from_csv("name,age\nalice,30\nbob,25", {header: true})
say($rows[0].name)
say(to_csv($rows))
```

```
[[a, b], [1, 2]]
alice
name,age
alice,30
bob,25
```

`to_csv` accepts either shape. An array of arrays writes plain rows. An array of map records writes a header row (taken from the first record's keys) followed by one row per record, with each row's values pulled **by key**. Because values are matched by key and not by position, a record's own key order need not match the header:

```drang
$recs := [{name: "alice", age: 30}, {age: 25, name: "bob"}]
say(to_csv($recs))
```

```
name,age
alice,30
bob,25
```

Scalar cells stringify. A null value (a JSON `null`, or a record field the header names but the record lacks) writes an empty cell. A non-scalar cell, such as a nested array or map, is a catchable error:

```drang
say(is_err(to_csv([["ok", [1, 2]]])))
```

```
true
```

### Strict by default

Both directions are strict, so malformed data fails loudly rather than silently corrupting a dataset. Three conditions are errors: a ragged row (a row whose field count differs from the header), a duplicate header name, and a record whose keys diverge from the header.

```drang
say(is_err(from_csv("a,b\n1,2,3")))              # ragged row
say(is_err(from_csv("a,a\n1,2", {header: true}))) # duplicate header
```

```
true
true
```

Pass `{lenient: true}` to relax all three. Ragged rows are padded or truncated to the header width, a duplicate column keeps the last value, and unknown record keys are dropped while missing ones become empty cells:

```drang
say(from_csv("a,a\n1,2", {header: true, lenient: true}))
say(to_csv([{name: "alice"}, {age: 25}], {lenient: true}))
```

```
[{a: 2}]
name
alice

```

### Options

Both functions take an optional trailing map.

| Option | Where | Meaning |
|--------|-------|---------|
| `sep` | both | field delimiter, exactly one character (default `,`; use `"\t"` for TSV) |
| `header` | both | read: first row is column names, producing records; write: include a header row for record input (default `true`) |
| `lenient` | both | relax the three strict checks (pad/truncate ragged rows, keep the last duplicate column, drop or pad divergent record keys) |
| `comment` | read | skip lines whose first character is this |
| `trim` | read | drop leading whitespace in each field |
| `lazy_quotes` | read | tolerate stray quotes in otherwise malformed input |
| `crlf` | write | line ending: `\r\n` (the RFC 4180 default); set `false` for `\n` |
| `sanitize` | write | neutralize spreadsheet formula cells (default `false`; see below) |

A worked read with three options at once:

```drang
say(from_csv("a\tb\n1\t2", {sep: "\t"}))          # tab-separated
say(from_csv("# note\na,b\n1,2", {comment: "#"}))  # skip comment line
say(from_csv("a, b\n1, 2", {trim: true}))          # trim leading spaces
```

```
[[a, b], [1, 2]]
[[a, b], [1, 2]]
[[a, b], [1, 2]]
```

The default write line ending is `\r\n`, which is why a two-line file below runs to 10 bytes rather than 8:

```drang
$rows := [["a", "b"], ["1", "2"]]
say(len(to_csv($rows)))                     # 3 + CRLF + 3 + CRLF
say(len(to_csv($rows, {crlf: false})))      # 3 + LF + 3 + LF
```

```
10
8
```

### CSV injection

A cell such as `=SUM(A1)` becomes a live formula when the file is opened in a spreadsheet, and a hostile cell can run commands. `to_csv` writes your data faithfully by default: a leading `-` on a negative number is preserved, and nothing is rewritten.

When the output is bound for a spreadsheet, pass `{sanitize: true}`. Any cell that begins with `=`, `+`, `-`, `@`, tab, carriage return, or line feed gets a `'` prefix, so the spreadsheet treats it as text. This is opt-in precisely because it changes the data: a `-5` is rewritten as `'-5`.

```drang
say(to_csv([["=SUM(A1)", "-5", "ok"]], {sanitize: true, header: false}))
```

```
'=SUM(A1),'-5,ok
```

### Errors

Malformed CSV under strict mode, and unencodable rows, are catchable `Err` values; recover them with `//`:

```drang
say(is_err(from_csv("a,b\n1,2,3")))    # ragged row, strict
```

```
true
```

Misusing the call itself aborts, because it is a bug in your program rather than bad data. A non-string first argument, a `sep` that is not exactly one character, and an unknown option key all abort:

```drang
say(from_csv("a,b\n1,2", {sep: "::"}))
```

```
drang: sep option must be exactly one character, got "::"
```

### Inherited quirks

A leading UTF-8 byte-order mark is stripped automatically on read, so a header key never carries an invisible prefix:

```drang
$src := from_hex("efbbbf") ~ "name,age\nalice,30"
say(keys(from_csv($src, {header: true})[0]))
```

```
[name, age]
```

Two normalizations are worth knowing. A `\r\n` inside a quoted field reads back as a plain `\n`. And blank lines are skipped, so a row that is a single empty field does not survive a round trip. Writing `[["a"], [""]]` and reading it straight back yields only `[["a"]]`:

```drang
say(from_csv(to_csv([["a"], [""]])))
```

```
[[a]]
```

Finally, the line-based one-liner modes (`-n` and `-p`) cannot safely stream a CSV that contains quoted newlines, since a single logical row may span several physical lines. Read and parse the whole text with `from_csv` in that case rather than processing it line by line.

---

## Date and time

A point in time is a **number**: seconds since the Unix epoch of 1970-01-01 UTC, carried as a float with sub-second precision. There is no dedicated date type. This is a deliberate choice, and it pays off immediately: time arithmetic and comparison are just number arithmetic and comparison. One hour later is `$t + 3600`. "Before" is `$a < $b`. A duration is a subtraction. You never reach for a date library to answer "which is earlier" or "how many seconds apart".

The five builtins convert between that number and the things humans and logs care about.

- `now()`: the current time as epoch seconds (a float).
- `sleep($secs)`: pause for `$secs` seconds and return nothing. A fractional value is fine (`sleep(0.25)`).
- `format_time($epoch, $fmt, $opts?)`: render an epoch as a string using `%`-codes.
- `parse_time($str, $fmt, $opts?)`: parse such a string back to an epoch, or return an `Err`.
- `date_parts($epoch, $opts?)`: break an epoch into a map of calendar components.

```drang
$t := parse_time("2026-06-27 13:45:09", "%Y-%m-%d %H:%M:%S")
say(format_time($t, "%A, %b %e %Y at %H:%M"))   # Saturday, Jun 27 2026 at 13:45
say(format_time($t + 86400, "%Y-%m-%d"))         # 2026-06-28  (one day later)
say(date_parts($t).weekday)                      # 6
```

Because an epoch is a number, ordinary comparison is chronology:

```drang
$a := parse_time("2026-01-01", "%Y-%m-%d")
$b := parse_time("2026-06-27", "%Y-%m-%d")
say($a < $b)   # true
```

`sleep` returns nothing (its result is `nil`), so call it for its effect. Note that `nil` is a value the language produces, not a literal you can write. There is no `nil` keyword to compare against.

```drang
say(sleep(0.01))   # nil
```

### Format codes

`format_time` and `parse_time` share one `%`-code vocabulary, the usual strftime set:

`%Y %y %m %d %e %H %I %M %S %p %A %a %B %b %j %w %z %Z %%`, plus `%n` (newline) and `%t` (tab).

The two builtins treat an unrecognized code differently, and the difference is intentional. `format_time` leaves an unknown code in the output verbatim, so a stray code degrades to literal text rather than aborting a log line. `parse_time` is strict: a code it cannot satisfy makes the parse fail and return an `Err`.

```drang
$b := parse_time("2026-06-27", "%Y-%m-%d")
say(format_time($b, "%Q left literally, day %d"))   # %Q left literally, day 27
```

A `parse_time` that does not match its format is a catchable `Err`, so `//` can supply a fallback:

```drang
$e := parse_time("not a date", "%Y-%m-%d")
say(is_err($e))              # true
say($e // "unparseable")     # unparseable
```

### Local versus UTC

All three of `format_time`, `parse_time`, and `date_parts` work in **local time by default**. Pass `{utc: true}` as the trailing options map to work in UTC instead. Use UTC whenever a timestamp crosses machines or gets compared against another system's clock. Local time is for display to a person sitting at the machine.

```drang
say(format_time(1000000000, "%Y-%m-%d %H:%M:%S", {utc: true}))   # 2001-09-09 01:46:40
```

`format_time` and `parse_time` are exact inverses under a matching format and the same time zone, so a value survives a round trip:

```drang
$s    := format_time(1000000000, "%Y-%m-%d %H:%M:%S", {utc: true})
$back := parse_time($s, "%Y-%m-%d %H:%M:%S", {utc: true})
say(format_time($back, "%Y-%m-%d %H:%M:%S", {utc: true}))        # 2001-09-09 01:46:40
```

One display detail to keep in mind: a raw epoch is a large float, and large whole floats print in scientific notation. Reach for `format_time` when you want a timestamp a human can read, and do arithmetic on the number directly.

### date_parts

`date_parts` returns a map with these keys: `year month day hour minute second weekday yearday`. `weekday` runs 0 to 6 with Sunday as 0. `yearday` is 1-based (January 1 is 1). The other fields carry their natural calendar values.

```drang
say(date_parts(1000000000, {utc: true}))
# {year: 2001, month: 9, day: 9, hour: 1, minute: 46, second: 40, weekday: 0, yearday: 252}
```

### Errors

These builtins follow the standard convention: a bad-typed or out-of-range argument yields a catchable `Err`, so you can guard with `//` or `is_err`. A wrong argument count is a programming mistake and aborts uncatchably. `now()` and `sleep` expect exactly 0 and 1 arguments; the three formatting builtins take their epoch or string, a format where applicable, and an optional options map.

---

## Hashing, encoding, and randomness

These builtins cover the everyday cryptographic-adjacent chores: content digests, wire encodings, and random values for jitter, sampling, and identifiers. They operate on strings and return strings, and their failures are catchable `Err`s (except a wrong argument count, which aborts).

### Hashing

`sha256`, `sha1`, and `md5` each take a string and return its lowercase hexadecimal digest. The digests are fixed length: 64 hex characters for SHA-256, 40 for SHA-1, 32 for MD5.

```drang
say(sha256("abc"))   # ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
say(sha1("abc"))     # a9993e364706816aba3e25717850c26c9cd0d89d
say(md5("abc"))      # 900150983cd24fb0d6963f7d28e17f72
```

### Encoding

Three encode/decode pairs move data across textual boundaries. The `to_*` side always succeeds; the `from_*` side returns a catchable `Err` when its input is malformed, so `//` gives you a clean fallback.

- `to_base64` / `from_base64`: standard padded base64.
- `to_hex` / `from_hex`: lowercase hex. Decoding rejects a non-hex character or an odd-length string.
- `to_url` / `from_url`: URL query encoding. A space becomes `+`, and reserved characters are percent-escaped. `from_url` is the exact inverse.

```drang
say(to_base64("hi"))                    # aGk=
say(from_base64("aGk="))                # hi
say(to_hex("AB"))                       # 4142
say(from_hex("4142"))                   # AB
say(to_url("a b&c=d"))                  # a+b%26c%3Dd
say(from_url("a+b%26c%3Dd"))            # a b&c=d
```

Malformed input on the decode side is data, not a crash:

```drang
say(from_base64("!!!") // "bad input")   # bad input
say(from_hex("zz") // "bad hex")         # bad hex
```

### Randomness

The random builtins split into two groups by their source, and the distinction matters when security is on the line.

`rand`, `rand_int`, `shuffle`, and `sample` draw from a fast, automatically seeded generator. It is excellent for retry jitter, sampling, test data, and games. It is **not** suitable for anything that must resist prediction: tokens, passwords, session keys, nonces.

`uuid` is the exception. It draws from the cryptographic generator, so its output is unpredictable and safe as an identifier.

- `rand()`: a float in `[0, 1)`.
- `rand_int(n)`: an int in `[0, n)`. With two arguments, `rand_int(lo, hi)` gives an int in `[lo, hi)`.
- `shuffle(arr)`: a **new** randomly permuted array. The input array is left untouched.
- `sample(arr)`: one random element of `arr`.
- `uuid()`: a random v4 UUID string.

```drang
say(rand_int(6) + 1)            # a die roll, 1–6                     e.g. 5
say(rand_int(10, 20))           # in [10, 20)                        e.g. 19
say(shuffle([1, 2, 3, 4]))      # a fresh permutation                e.g. [4, 1, 3, 2]
say(sample(["a", "b", "c"]))    # one element                        e.g. c
say(uuid())                     # 29b6c756-f3d7-4fe4-a48f-823d248102a2
```

`shuffle` returning a copy is worth stating plainly: the array you pass in is never modified, so it is safe to shuffle a value you still need in original order.

```drang
$xs := [1, 2, 3, 4, 5]
$ys := shuffle($xs)
say($xs)                  # [1, 2, 3, 4, 5]  (unchanged)
say(len($ys) == len($xs)) # true
```

The bounds are guarded, and a violation is a catchable `Err`. `rand_int(n)` requires `n` to be positive; `rand_int(lo, hi)` requires `lo < hi`; `sample` requires a non-empty array.

```drang
say(rand_int(0) // "n must be positive")   # n must be positive
say(sample([]) // "empty")                 # empty
```

## HTTP client

A small, robust HTTP client built over Go's `net/http`. The entire surface is one primitive, `http`, plus two conveniences, `http_get` and `http_post`. Any other method (PUT, PATCH, DELETE, HEAD) goes through `http(method, url, opts?)`. The builtin deliberately stops at a single request/response. Higher-level patterns such as retry, cookie jars, auth flows, and pagination are written as drang code, not configured through option flags.

```drang
$r := http_get("https://example.com")
say($r.status, $r.ok, len($r.body))       # 200 true 559
```

### The response is a map

A completed request returns a map with five keys:

| Key | Type | Meaning |
|---|---|---|
| `status`  | int    | The HTTP status code. |
| `ok`      | bool   | `true` for 200–299, `false` otherwise. |
| `body`    | string | The full response body (decompressed). |
| `headers` | map    | Response headers, keys lowercased; multi-value headers joined with `", "`. |
| `url`     | string | The final URL, after any redirects were followed. |

Header keys are always lowercased so you never have to guess the casing a server sent:

```drang
$r := http_get("https://example.com")
say("status:", $r.status)                        # status: 200
say("type:", $r.headers["content-type"])         # type: text/html
say("final url:", $r.url)                         # final url: https://example.com
```

### A status is data; only a broken connection is an error

This is the central design decision, and it will surprise you if you expect any non-2xx status to raise. It does not. A 404 or a 500 is a completed exchange: the server answered, and its answer is a normal response map with `ok: false`.

```drang
$r := http_get("https://example.com/definitely-not-here")
say("is_err:", is_err($r))                        # is_err: false
say("status:", $r.status, "ok:", $r.ok)           # status: 404 ok: false
```

An `Err` is reserved for a *failure to complete* the exchange at all: a DNS lookup that fails, a refused connection, a TLS handshake failure, a malformed URL, a timeout, or a body that overflows the size cap. The reasoning is that a status code is information the caller usually wants to branch on, whereas an unreachable host is the kind of failure that should propagate up and out. This mirrors how the subprocess builtins treat a process that runs and exits non-zero (data) versus a process that never starts (error).

Two consequences follow directly. First, `?` on an HTTP call bubbles only the "couldn't reach the server" case, which is almost always what you want. Second, `//` masks only that same transport failure, letting you supply a stand-in response:

```drang
$r := http_get("https://no-such-host.invalid") // {status: 0, ok: false}
say("status:", $r.status, "ok:", $r.ok)           # status: 0 ok: false
```

A timeout is distinguished from every other transport failure by its error code: it carries `err_code` **124** (the same code a subprocess timeout uses), while every other transport, option, or type failure carries **1**. So you can tell a slow server from an unreachable one:

```drang
$r := http_get("https://example.com", {timeout: 1})   # 1 ms: guaranteed to time out
say("is_err:", is_err($r), "code:", err_code($r))      # is_err: true code: 124
```

Putting the pieces together, the typical dispatch over a result reads:

```drang
$r := http_get($url)
if is_err($r) {
  if err_code($r) == 124 { say("timed out") } else { say("unreachable") }
} else if $r.status == 404 {
  say("not found")                                # an answer, not an error
} else if $r.ok {
  say(from_json($r.body).name)
}
```

### Options

The third argument to `http` (and the second to `http_get` / `http_post`) is a trailing options map, in the same style as the subprocess builtins. Unknown keys are ignored; keys with the wrong type are an error where noted.

| Option | Type | Effect |
|---|---|---|
| `headers`   | map    | `{name: value}`, both strings. Overrides the defaults; a non-string entry is an `Err`. |
| `body`      | string | A raw request body. A non-string value is an `Err`. |
| `json`      | any    | Serialized to JSON and sent with `Content-Type: application/json`. Supplying `body` and `json` together is an `Err`. |
| `timeout`   | int (ms) | Wall-clock cap on the whole request. `0` means unlimited. Default 30000. |
| `redirects` | int    | Redirect cap. `0` means do not follow: the 3xx response is returned as-is. |
| `max_body`  | int (bytes) | Cap on the decompressed body. `0` means unlimited. Exceeding it is an `Err`, never a silent truncation. |
| `insecure`  | bool   | When truthy, skip TLS certificate verification. |

Sending JSON is a one-liner. The `json` option takes any drang value, serializes it, and sets the content type for you:

```drang
$r := http("POST", "https://httpbin.org/post", {json: {name: "ada", n: 3}})?
$echo := from_json($r.body)
say(to_json($echo.json))                          # {"n":3,"name":"ada"}
```

`http_post` sends a *string* body, which is the right tool for form-encoded or other pre-formatted payloads. Set the content type yourself through `headers`:

```drang
$r := http_post("https://httpbin.org/post", "a=1&b=2",
                {headers: {"Content-Type": "application/x-www-form-urlencoded"}})?
$echo := from_json($r.body)
say(to_json($echo.form))                          # {"a":"1","b":"2"}
```

Asking for `body` and `json` at once is a mistake the client catches for you:

```drang
$r := http("POST", "https://httpbin.org/post", {body: "x", json: {a: 1}})
say(err_msg($r))                                  # http: pass body or json, not both
```

Setting `redirects: 0` hands you the raw 3xx instead of following it, so you can read the `Location` header yourself:

```drang
$r := http_get("https://httpbin.org/redirect/1", {redirects: 0})
say("status:", $r.status)                         # status: 302
say("location:", $r.headers["location"])          # location: /get
```

And `max_body` turns an oversized response into a clean error rather than a truncated string:

```drang
$r := http_get("https://httpbin.org/bytes/1000", {max_body: 100})
say(err_msg($r))                                  # http: response body exceeds max_body (100 bytes)
```

### Robust defaults

The bare client the standard library gives you has no timeout, no body cap, and no redirect ceiling. drang's client fills those gaps so a script does not hang or exhaust memory on a hostile or broken server:

- A 30-second timeout on every request.
- Redirects followed up to a depth of 10, and `Authorization` dropped on a hop that changes host, so a credential is never leaked to a redirect target.
- TLS certificate verification on. Turn it off per-request with `{insecure: true}`.
- A 32 MiB response-body cap. Overflow is an error, not a truncation.
- Transparent gzip decompression.
- A default `User-Agent: drang`, which you can override through `headers`.
- One shared, connection-pooled transport, safe to fan out under `pmap`.

Because the transport is shared and safe to use from many workers at once, concurrent fan-out is direct. Combine it with `//` to turn each unreachable host into a placeholder rather than aborting the whole batch:

```drang
$urls := ["https://example.com", "https://httpbin.org/status/404", "https://no-such-host.invalid"]
$statuses := $urls |> pmap(|$u| http_get($u, {timeout: 5000}) // {status: 0}) |> map(|$r| $r.status)
say(to_json($statuses))                           # [200,404,0]
```

Here `200` is a live server, `404` is a completed exchange returned as data, and `0` is the fallback from `//` standing in for the host that could not be reached.

### Arity is checked before the request runs

Calling `http`, `http_get`, or `http_post` with the wrong number of arguments is a programming mistake, not a runtime condition, so it aborts uncatchably rather than returning a catchable `Err`:

```drang
$r := http_get()
# drang: http_get expects 1 or 2 arguments (url, opts?), got 0
```

---

## Task dispatch

`dispatch(tasks)` turns a script into a subcommand-style command-line tool: a small task runner living inside your own program. You hand it a map of `{name: function}`, it reads the task name from `$ARGV[0]`, runs the matching function, and then **exits the process** with a resolved code. It never returns to the code after it.

```drang
fn .build($args) { say("building " ~ to_json($args)) }
fn .clean()      { say("cleaning") }

dispatch({build: .build, clean: .clean})
```

Run with no task name (or with `list`, `-l`, or `--list`), it prints the available tasks and exits 0:

```
$ drang tasks.dr
tasks:
  build
  clean
```

Run with a task name, it invokes that function. Everything after the name becomes the arguments:

```
$ drang tasks.dr build a b
building ["a","b"]
```

Run with a name that is not in the map, it prints the task list to stderr and exits **2**:

```
$ drang tasks.dr nope
drang: unknown task "nope"
tasks:
  build
  clean
```

### Task function shape

A task function takes either **zero parameters** or **one parameter**. With zero, it ignores the command-line arguments. With one, it receives the arguments that followed the task name, as an array of strings. Declaring two or more parameters is caught before anything runs:

```
$ drang tasks.dr two x y
drang: dispatch: task "two" must take 0 or 1 parameter, got 2
```

### Exit codes

The process exit code is resolved from what the task did:

- The task returns normally: exit **0**.
- The task returns or propagates an `Err`: exit with that error's code, clamped to `1..255`.
- The task name is unknown: exit **2**.

To fail a task with a specific code, return or propagate an `Err`. The natural way is `fail("...")?`, which propagates a code-1 error and prints its message with the `drang:` prefix:

```drang
fn .build($args) { say("building " ~ to_json($args)) }
fn .check()      { fail("checks failed")? }

dispatch({build: .build, check: .check})
```

```
$ drang tasks.dr check
drang: checks failed        # (stderr)
$ echo $?
1
```

A non-1 code flows through the same way when it comes from an operation that carries one. A propagated subprocess failure, for instance, sets the exit code to the child's status:

```drang
fn .test() {
  capture("cmd", "/c", "exit 42")?      # the subprocess exits 42
  say("passed")
}

dispatch({test: .test})
```

```
$ drang tasks.dr test
drang: cmd exited with code 42
$ echo $?
42
```

One sharp edge to know. `exit(n)` and `die(...)` do **not** work from inside a dispatched task. They are program-level controls, and calling either within a task function produces `drang: exit outside of a program` on stderr and an exit code of 1, discarding the code you asked for. Inside a task, choose your exit code by returning or propagating an `Err`, as above, and reserve `exit`/`die` for the top level of an ordinary script.

## One-liner mode

`-n` and `-p` turn drang into a stream processor: the program runs once per input line, in the tradition of the classic line-oriented text tools. `-n` just loops over the lines. `-p` loops and, after each line, prints the topic variable `$_` — the filter mode, where you edit `$_` in place and let drang do the writing.

Short flags combine. A trailing `e` takes the program source as its argument, exactly like a standalone `-e`, so `-ne`, `-pe`, and `-ane` are the usual forms:

```drang
drang -pe '$_ = upper($_)' notes.txt              # filter: uppercase each line
drang -ne 'if matches($_, "ERROR") { say($_) }' log.txt   # keep matching lines
drang -ape '$_ = $f[0]' data.tsv                  # print the first column
```

Those three run against a `notes.txt` of two lines, a `log.txt` with one `ERROR` line, and a tab-separated `data.tsv`:

```
THE QUICK BROWN FOX
LAZY DOG
```
```
[07-04] ERROR disk full
```
```
apple
banana
cherry
```

`matches(s, pattern)` returns a bool: true when the pattern matches anywhere in `s`. That is the idiomatic line filter.

### Where the program and the input come from

The flags decide what the first non-flag argument is. With a trailing `e`, the source is the quoted string right after the flags, and every remaining argument is an input file. Without `e`, the first non-flag argument is a program *file*, and the rest are inputs:

```drang
drang -n prog.dr notes.txt        # prog.dr is the script; notes.txt is the input
```

Input comes from the files named after the program. When none are named, drang reads standard input. A bare `-` in the file list also means standard input. The filenames are exposed as the array `$ARGV`, so the program can see its own argument list:

```drang
drang -ne 'if $nr == 1 { say(str($ARGV)) }' notes.txt nums.txt
# [notes.txt, nums.txt]
```

### Per-line variables

Before each line runs, drang sets four variables, all in the `$` data namespace:

| Variable | Meaning |
|----------|---------|
| `$_`    | the current line, with its trailing newline (and a `\r`) stripped |
| `$nr`   | the 1-based line number, counting straight across every input file |
| `$file` | the current input filename (`"<stdin>"` when reading standard input) |
| `$f`    | with `-a`, the line split on whitespace into a 0-indexed array |

`$nr` does not reset between files, and `$file` follows whichever file the current line came from:

```drang
drang -ne 'say($nr ~ "  " ~ $file ~ "  " ~ $_)' notes.txt nums.txt
```
```
1  notes.txt  the quick brown fox
2  notes.txt  lazy dog
3  nums.txt  10
4  nums.txt  20
5  nums.txt  30
```

`$f` exists only under `-a`. Reading it without `-a` is not an empty array; the variable is simply undefined, and touching it aborts with `undefined variable $f`. Reach for `-a` when you want fields, and leave it off otherwise.

### Autosplit with `-a`

`-a` splits each line into `$f` on runs of whitespace, ignoring leading and trailing whitespace, so there are no empty edge fields and any mix of spaces and tabs collapses to one separator:

```drang
printf 'a   b\t\tc\n' | drang -ane 'say(len($f) ~ " " ~ $f[0] ~ "|" ~ $f[1] ~ "|" ~ $f[2])'
# 3 a|b|c
```

`len($f)` is the field count, which makes per-line word counting a one-liner:

```drang
drang -ane 'say($nr ~ ": " ~ len($f) ~ " words")' notes.txt
```
```
1: 4 words
2: 2 words
```

### `BEGIN` and `END`

`BEGIN { ... }` runs once before the loop, `END { ... }` once after. Use them for headers, setup, accumulators, and totals. The per-line body runs in a persistent scope, so a variable declared in `BEGIN` is still there on every line and still there in `END`:

```drang
drang -ane 'BEGIN{ $sum := 0 } $sum = $sum + int($f[0]); END{ say($sum) }' nums.txt
# 60
```

The same shape totals across files, because the scope outlives every line and every input:

```drang
drang -ane 'BEGIN{ $w := 0 } $w = $w + len($f); END{ say("total words: " ~ $w) }' notes.txt
# total words: 6
```

`BEGIN` and `END` are contextual keywords, recognized only as a statement-leading `BEGIN {` or `END {`. Everywhere else they are ordinary identifiers, so nothing stops you from naming a variable or field `begin`.

Under `-p`, the auto-print of `$_` happens *after* the body, so a body that also prints will interleave. Editing `$_` is how you change the output; setting it to the empty string yields a blank line:

```drang
printf 'one\ntwo\n' | drang -pe 'say("> " ~ $_)'
```
```
> one
one
> two
two
```

### Notes and limits

- Separate statements on one line with `;`. A block's closing `}` also ends a statement, so `BEGIN{ ... } stmt` needs no `;`, but `stmt; END{ ... }` does.
- Use `:=` or `=` in the per-line body, not `::=`. A constant declaration re-runs on every line, and re-declaring the same constant twice aborts with `cannot redeclare constant`.
- `-p` ends each line with `\n`. Carriage-return line endings on input are normalized to a plain newline, and a missing final newline is supplied. Feeding `a\r\nb` (no trailing newline) through a `-p` filter emits `a...\nb...\n`, so the output is always newline-terminated.
- Runtime errors in stream mode report the message together with a source position and a caret pointing into the one-liner, the same as any other program, even when the failing line is deep in the input:

  ```drang
  printf '5\n0\n' | drang -ne 'say(100 / int($_))'
  ```
  ```
  20
  drang: division by zero
    at <-e>:1:9
      say(100 / int($_))
              ^
  ```

- In-place file editing is not yet available.

## Modules: `use`

A program can be split across files. Any `.dr` file is a **module**. Its top-level named functions (`fn .foo`) and constants (`$CONST ::= …`) are its **exports**, and nothing else is. A mutable top-level variable in a module is rejected at import time, so a module's public surface is always functions and constants, never mutable state.

There is exactly one keyword, `use`, and it has two modes. Which one you get is decided by a single thing: **whether you capture the result**. Used bare as a statement, `use` merges. Used as a call whose value you bind, it isolates. There is no `import`/`from`/`as` vocabulary to learn beyond this.

### Flat merge: `use "./util"`

As a statement, `use` merges the module's exports into the current scope, as if the source had been pasted in: the module's `.foo` functions join your `.`-space, its `$CONST`s join your `$`-space. You then call them with no prefix.

Given this module:

```drang
# util.dr
fn .shout($s) { upper($s) ~ "!" }
$GREETING ::= "hi"
```

a flat merge pulls both names straight into scope:

```drang
use "./util"
say(.shout("hey"))   # HEY!
say($GREETING)       # hi
```

```
HEY!
hi
```

### Isolated: `$u := use("./util")`

Bind the result of `use(...)` and you get the module's **export record** instead. It merges nothing into your namespaces; you reach each export through the binding as `$u.name`. Bind to any `$`-name you like — the binding name is the module's alias, which is why there is no separate `as` keyword.

```drang
$u := use("./util")
say($u.shout("hey"))   # HEY!
say($u.GREETING)        # hi
```

```
HEY!
hi
```

Note the two spellings that select the mode. `use "./util"` (a statement, no parentheses) merges. `$u := use("./util")` (a captured call) isolates. They are the same keyword doing two jobs.

### Path resolution

Paths are ordinary **strings**, so a path containing a space needs no special handling. The `.dr` extension is **optional**: `use "./greet"` and `use "./greet.dr"` name the same file.

A relative path resolves against **the importing file's own directory**, not against whoever imported it. A module's `use "./sibling"` therefore always means the sibling next to *that* module, and a module can be relocated with its dependencies without rewriting its imports.

```drang
$g := use("./my mods/greet")   # spaces fine, extension omitted
say($g.hi())
```

```
hi from spaced dir
```

Entry points that have no source file of their own — `-e`, standard input, the REPL, and a `drang build` standalone — resolve relative paths against the **current working directory** instead, since there is no containing file to anchor them.

### Loading rules

**Load once.** A module's top level is evaluated exactly once per process and cached by canonical path (case-folded, matching how Windows treats file names). If several modules import the same dependency, its top level still runs a single time. In a diamond, where a left and a right module both import a shared module, the shared module loads once:

```drang
# count.dr
say("loading count")
fn .n() { 1 }
```

```drang
# left.dr and right.dr each contain:  use "./count"
$l := use("./left")
$r := use("./right")
say($l.left())
say($r.right())
```

```
loading count
1
1
```

`loading count` appears once, proving the shared module was evaluated a single time even though two importers pulled it in.

**Only successful loads are cached.** If a load fails, nothing is cached and the next `use` of that path runs the module again from the top. This lets a captured import be retried after a transient failure rather than being permanently poisoned:

```drang
# aborts.dr
say("aborts top-level runs")
.does_not_exist()
```

```drang
$a := use("./aborts") // fail("first failed")
$b := use("./aborts") // fail("second failed")
say(is_err($a))
say(is_err($b))
```

```
aborts top-level runs
aborts top-level runs
true
true
```

The module's top level ran both times.

**Cycles are an error, not a hang.** If imports form a cycle, the load fails with a message containing `import cycle through …` rather than looping forever:

```
drang: use "./cyc_a": … use "./cyc_b": … use "./cyc_a": import cycle through …\cyc_a.dr
```

**Flat merge is not transitive.** If module `b` does `use "./d"`, importing `b` gives you `b`'s own exports only; `d`'s names are not re-exported through `b`. A merge injects names into the importing module's scope and stops there. Reaching for a name that was merged one level down is an unknown-name error:

```
drang: unknown function .fromD
```

### Errors

The two modes differ in how a *failed* import behaves, and the difference is deliberate.

A **captured** import that fails produces a catchable [error value](#errors-as-values). This is the recoverable form: pair it with `//` to fall back when a module is missing or broken.

```drang
$cfg := use("./nonexistent") // {}
say(type($cfg))
say(len($cfg))
```

```
map
0
```

A **flat-merge statement** that fails **aborts** the program with the import error. There is no value to catch, and a broken merge would leave your scope in a half-populated state, so the load is fatal:

```
drang: use "./nonexistent": cannot read …\nonexistent.dr: … The system cannot find the file specified.
```

**Collisions abort, in both directions, and are never silent.** Merging a name that is already bound in the current scope fails, and defining a name that a prior `use` already merged fails too. A merge can never quietly overwrite one of your definitions, nor you one of its:

```drang
fn .shout($s) { $s }
use "./util"          # util also exports .shout
```

```
drang: use "./util": .shout is already defined here
```

```drang
use "./util"
fn .shout($s) { $s }  # redefining a merged name
```

```
drang: cannot redefine .shout (it is already defined, e.g. imported by use)
```

**`exit` and `die` always propagate, even through a captured import.** If a module calls `exit()` or `die()` while loading, the whole program ends. This is not downgraded to a catchable error, and the `// {}` fallback does not stop it, because these builtins mean "end the process now," not "this import failed":

```drang
# exiter.dr contains:  exit(7)
$m := use("./exiter") // {}
say("this should not print")
```

The program exits with code `7` and prints nothing; the `say` never runs. A module that calls `die("...")` likewise ends the program with its stderr message and exit code `1`, past any `//`.

### Limits and notes

**A module exports only functions and constants.** A mutable top-level `:=` variable is not exportable and makes the whole module fail at import:

```drang
# badmod.dr
$counter := 0
fn .bump() { $counter = $counter + 1 }
```

```
drang: use "./badmod": …\badmod.dr: a module may export only functions and constants,
but $counter is a mutable top-level variable
```

This rule has a consequence worth internalizing. To capture a nested import *inside* a module, bind it to a **constant**, not a variable: `$DEP ::= use("./dep")` is a valid export-compatible binding, whereas `$dep := use("./dep")` would make the enclosing module itself unexportable.

**Exports are deeply immutable.** A module's export record and every container reachable through it (each array and map inside) are frozen. Since exports are shared across the import cache, this guarantees one importer cannot mutate a value out from under another. An attempted write fails loudly at the point of mutation:

```drang
# frozen.dr contains:  $CONF ::= {host: "local", port: 80}
$c := use("./frozen")
say($c.CONF.host)
$c.CONF.host = "other"
```

```
local
drang: cannot modify a frozen map
```

**Every top-level `.foo` is exported.** There is no module-private helper mechanism yet; a top-level function is always public.

**A bare parenthesized `use(...)` as a statement loads the module but imports nothing.** Because it is a call whose result is discarded, it neither merges (that is the no-parentheses statement form) nor binds. It is almost always a mistake:

```drang
use("./util")         # loads util, imports nothing
say(.shout("x"))
```

```
drang: unknown function .shout
```

Write `use "./util"` to merge, or `$u := use("./util")` to capture.

## Testing

drang has a built-in test runner. You do not install a framework, register cases, or import an assertion library. You write `example` statements next to your code and run `drang test`.

### `example`: assertions that double as documentation

An `example` is a single assertion. It reads as a claim about your code that is both checked by the runner and legible to a reader. There are three forms.

```drang
example .add(2, 3) == 5       # equality: the two sides must be equal
example .is_valid("ok")       # truthy: the expression must be a truthy value
example .parse("bad") fails   # error: the expression must fail
```

The truthy form accepts any value, not just a bool. It passes when the value is truthy under drang's standard rule (everything except `nil`, `false`, `0`, `0.0`, `""`, and empty containers), so `example 42` passes and `example ""` fails.

The `fails` form is the way you assert that something is *supposed* to go wrong. It succeeds whether the expression returns an Err value (for instance from `fail(...)`) or triggers an uncatchable runtime abort (for instance calling a function that does not exist). Both count as "it failed," which is usually what you mean when you write a negative test.

Put these in a file with the code they describe:

```drang
fn .add($a, $b) { $a + $b }
fn .is_valid($s) { len($s) > 0 }
fn .parse_num($s) { int($s) }

example .add(2, 3) == 5
example .is_valid("ok")
example .parse_num("bad") fails
```

Run it with the `test` subcommand:

```
$ drang test mathutil.dr
mathutil.dr: 3 passed, 0 failed
```

### How a run works

`drang test` first runs the whole file top to bottom, so every function definition and every piece of top-level setup executes. Then it evaluates each `example` as an assertion against that finished program state.

Because the file runs in full before any assertion is checked, order does not matter. An `example` may sit *above* the function it exercises. This is deliberate: it lets you lead a definition with the examples that document it, and the runner will still find the definition. There is no scanning-in-order restriction to work around.

In a normal run (`drang file.dr`, or `-e`), `example` statements are skipped entirely. They never execute, cost nothing, and cannot interfere with the program. The same source file is both your program and your test suite, with no build step to separate them:

```drang
say("hello")
example 1 == 2    # never runs outside `drang test`
say("world")
```

```
$ drang file.dr
hello
world
```

### Failure output

When an assertion fails, the runner prints a block naming the file and line, the example as the runner parsed it, and the expected-versus-actual values. At the end of each file it prints a `N passed, M failed` summary. The process exits non-zero if anything failed, so a test run is a usable gate in a script or CI job.

Given this file:

```drang
fn .add($a, $b) { $a + $b }

example .add(2, 3) == 6
example .add(2, 3) == 5
example .is_ok("x")
```

```
$ drang test mathfail.dr
  FAIL mathfail.dr:3  (example (call .add 2 3) == 6)
        expected 6, got 5
  FAIL mathfail.dr:5  (example (call .is_ok "x"))
        unexpected error: unknown function .is_ok
mathfail.dr: 1 passed, 2 failed
```

Two details worth noting. The parenthesized rendering (`(call .add 2 3)`) is the runner echoing the assertion in a normalized form, not a syntax you type. And an example that was *not* declared with `fails` but hits an error is reported as an `unexpected error`, which usually means the assertion itself is broken (here, a typo in the function name) rather than the code under test.

Numbers compare by value across the int and float divide. A whole-valued float equals the integer it represents, and it also *displays* without a trailing `.0`:

```drang
example 6 / 2 == 3    # passes: 3.0 equals 3
example 7 / 2 == 3    # fails
```

```
$ drang test flt.dr
  FAIL flt.dr:2  (example (/ 7 2) == 3)
        expected 3, got 3.5
flt.dr: 1 passed, 1 failed
```

### Testing several files

Pass more than one file and the runner reports each on its own line, then prints a combined `total`:

```
$ drang test mathutil.dr errval.dr
mathutil.dr: 4 passed, 0 failed
errval.dr: 1 passed, 0 failed
total: 5 passed, 0 failed
```

### Golden-output tests

An `example` checks a value. A golden test checks a script's entire **stdout** against a saved snapshot. This is the right tool for scripts whose job is to *produce text*: reports, formatters, code generators, CLI tools.

The convention is a sibling file: `report.dr` pairs with `report.golden`. When a `.golden` file exists next to the script, `drang test` runs the script, captures its stdout, and diffs the capture against the golden. A script with no sibling `.golden` is simply an `example` test, and its stdout flows to the terminal as usual.

Create or re-bless a golden from the script's current output with `--update` (short: `-u`):

```drang
for $i in 1..3 {
  say("row " ~ str($i))
}
example 1 + 1 == 2
```

```
$ drang test --update report.dr
  updated report.golden
report.dr: 1 passed, 0 failed
```

The written `report.golden` now holds exactly what the script printed:

```
row 1
row 2
row 3
```

On the next run the golden is checked alongside any `example` assertions, and the counts combine. Here the golden is one check and the single `example` is another, for two total:

```
$ drang test report.dr
report.dr: 2 passed, 0 failed
```

When the captured stdout no longer matches the golden, the runner prints a header identifying the two files, then the first point of divergence with `-` lines for the expected (golden) content and `+` lines for what the script actually printed, and exits non-zero:

```
  FAIL report.dr — stdout differs from report.golden
        @@ first difference at line 1 @@
        - row 1
        - row 2
        - row 3
        + line 1
        + line 2
        + line 3
report.dr: 0 passed, 1 failed
```

To accept the new output as correct, re-run with `--update`.

A golden test assumes the script's output is deterministic and fully flushed by the time the top level finishes. Two consequences follow from drang's concurrency. `await` every `spawn`ed task before the program ends, since a still-running detached task's output may not land in the capture. And avoid output whose line order depends on `pmap` scheduling, because parallel workers do not emit in a fixed order and the snapshot would flap between runs.

---

## Formatting

`drang fmt` rewrites drang source into one canonical style. It is opinionated and has no configuration. It preserves comments and never changes what a program does, only how it reads.

```
$ drang fmt script.dr            # print formatted source to stdout
$ drang fmt -w script.dr         # rewrite the file in place
$ drang fmt -w src/              # rewrite every *.dr under a directory
$ drang fmt --check src/         # exit non-zero if anything is unformatted (a CI gate)
$ cat script.dr | drang fmt      # filter stdin to stdout
```

With no paths, `fmt` reads stdin and writes formatted source to stdout, so it drops into a pipeline as a filter. Given paths, files are formatted directly and directories are searched for `*.dr` files (dot-directories such as `.git` are skipped).

| flag | effect |
|---|---|
| `-w`, `--write` | rewrite each changed file in place, atomically; requires paths |
| `-c`, `--check` | list unformatted files to stderr and exit non-zero (for CI) |
| `-l`, `--list`  | list files that would change, to stdout |
| `-d`, `--diff`  | print a diff of the changes |
| `--fix`         | also apply migration rewrites (see below) |

Exit status is `0` when everything is already formatted, `1` when a file would change (under `--check`, `-l`, or `-d`) or a file fails to parse, and `2` for a usage error (for example, `-w` with no paths, since stdin cannot be rewritten in place).

### What it does

Give `fmt` this deliberately messy file:

```drang
fn .add($a,$b){$a+$b}
$xs=[1,2,3]
$r  =  reduce($xs,0,|$a,$b|$a+$b)
say(  str( $r )  )
```

and it produces the canonical form:

```
$ drang fmt messy.dr
fn .add($a, $b) {
	$a + $b
}

$xs = [1, 2, 3]
$r = reduce($xs, 0, |$a, $b| $a + $b)
say(str($r))
```

The rules behind that transformation:

- **Indentation** is tabs, one per nesting level. A braced block is always multi-line, even a one-line function body.
- **Spacing** is normalized: one space around binary, assignment, and `|>` operators; tight `..` ranges and prefix `-`/`!`; `, ` between elements; `key: value` in maps.
- **Blank lines** are collapsed, with one blank line kept around each top-level function.
- **Parentheses** are reduced to what precedence requires. `(1 + 2) * 3` keeps its parens because they change the result; `1 + (2 * 3)` becomes `1 + 2 * 3` because `*` already binds tighter and the parens are redundant.
- **Long lines wrap** at roughly 100 columns. A `|>` pipeline breaks at every stage; a long call, array, or map goes to one element per line.

The wrapped pipeline form is worth seeing, since it is the shape most drang code takes:

```drang
$total := [1, 2, 3, 4, 5, 6] |> filter(|$x| $x % 2 == 0) |> map(|$x| $x * $x) |> reduce(0, |$a, $b| $a + $b)
```

```
$ drang fmt pipe.dr
$total := [1, 2, 3, 4, 5, 6] |>
	filter(|$x| $x % 2 == 0) |>
	map(|$x| $x * $x) |>
	reduce(0, |$a, $b| $a + $b)
```

One principle bounds all of this: **your surface is kept faithful**. String quoting and the `qq`/`q`/heredoc forms, `qw{...}` word lists, regex literals, numeric spelling, and postfix modifiers (`say($x) if $c`) are reprinted as written, not rewritten into some equivalent the formatter happens to prefer. `fmt` fixes whitespace and layout; it does not rephrase your code.

Comments are preserved and re-attached by position (leading, same-line trailing, and floating between statements):

```drang
# leading comment
$x=1  # trailing
say($x)
```

```
$ drang fmt cmt.dr
# leading comment
$x = 1  # trailing
say($x)
```

`fmt` re-verifies its own output before writing. If reformatting would drop a comment or produce source that no longer parses, it aborts that file untouched and exits non-zero rather than risk corrupting your code. A file that already fails to parse is reported and skipped:

```
$ drang fmt broken.dr
drang fmt: broken.dr: parse error: line 2: unexpected EOF ""
```

Formatting is idempotent: running `fmt` on already-formatted source is a no-op and leaves the bytes unchanged.

### `--fix`: migrations

drang has no version pragma in source. A language revision that renames or reshapes a construct instead ships a mechanical source rewrite, applied with `drang fmt --fix`. This keeps upgrades out of your files: you never annotate code with the revision it targets, you run one command to move it forward.

Today there are no such rewrites, so `--fix` behaves exactly like plain `fmt`:

```drang
say(1+2)
```

```
$ drang fmt --fix fix.dr
say(1 + 2)
```

When a future revision introduces a breaking rename, `drang fmt --fix -w src/` will migrate an entire codebase in a single pass, alongside the ordinary formatting.

## Not yet: known gaps and surprises

drang is a daily-driver under active construction, not a finished language. This section is the honest inventory of what is missing or behaves unexpectedly, so you don't burn time reaching for something that isn't there. Every claim and every output below was captured from the binary.

### Math is sized for glue scripts, not for science

The math family covers everyday arithmetic and nothing heavier. You get `abs`, `sum`, `min`, `max`, `floor`, `ceil`, `round`, `sqrt`, `pow`, `log`, `exp`, and `div`; the full trig set (`sin`, `cos`, `tan`, `asin`, `acos`, `atan`, `atan2`, all in radians); and the constant `pi()`. The prelude adds `mean` and `median`.

```drang
say([abs(-3), max([2, 9, 4]), floor(3.7), div(17, 5), pow(2, 10)])
# [3, 9, 3, 3, 1024]
say(log(1000, 10))
# 2.9999999999999996
say([mean([1, 2, 3, 4]), median([1, 2, 3, 4])])
# [2.5, 2.5]
```

That is the whole toolbox. There is no arbitrary-precision arithmetic (`int` is a fixed 64-bit integer, and overflow aborts loudly rather than growing the number; see below), no complex numbers, and no matrix, linear-algebra, or statistics library beyond `mean` and `median`. For anything heavier, shell out to a real tool.

Note two arity quirks worth internalizing: `round` takes exactly one argument (there is no digits parameter, so round manually with `pow` if you need decimal places), and `log`'s base is an optional second argument (`log(x)` is natural log, `log(x, 2)` and `log(x, 10)` give other bases).

Everything else a glue script normally reaches for is present and covered in its own section: HTTP (`http_get`, `http_post`, `http`), date and time, hashing, encodings, and randomness. There is no bare `fetch` builtin; use `http_get`.

```drang
say(fetch("http://example.com"))
# drang: undefined: fetch
```

### Operators that don't exist

Several operators an experienced programmer reaches for by reflex are simply not in the grammar.

**No integer-division operator.** `/` is *always* float division, even for two integer operands. Use the `div()` builtin (truncating toward zero) or wrap the result in `int()`.

```drang
say(10 / 4)        # 2.5
say(div(10, 4))    # 2
say(int(10 / 4))   # 2
```

A subtlety that trips people: a whole-valued float prints without a trailing `.0`, so the result *looks* like an integer even though its type is float.

```drang
say(6 / 2)         # 3
say(type(6 / 2))   # float
```

**No exponent operator.** `2 ** 8` does not parse. Use the `pow()` builtin.

```drang
say(2 ** 8)
# line 1: unexpected STAR "*"
# line 1: expected end of statement, got INT "8"
```

**No ternary.** `1 > 0 ? 1 : 2` does not parse, and there is no inline conditional of any kind, because `if` is a statement, not an expression. Assign inside an `if`/`else` body instead.

Do not fall back on the `cond and a or b` idiom as a substitute. Because `and`/`or` return one of their operands, that pattern returns `b` whenever the true-branch value `a` is *itself* falsy (`0`, `""`, `[]`, `false`). So it silently produces the wrong answer for exactly the values you most need to preserve.

```drang
$a := 0
say((true and $a) or 99)   # 99   (you wanted 0)
```

**No bitwise operators.** `&`, `|`-as-or, `<<`, and `>>` are all absent. `&` lexes as an illegal character, and `<<` is read as the start of a heredoc, so both produce parse errors. (`|` is reserved as the lambda delimiter, not bitwise-or.)

```drang
say(6 & 3)
# line 1: expected ')' to close call, got ILLEGAL "&"
```

**No increment or decrement.** `$x++` does not parse. Use compound assignment: `$x += 1`.

### Designed but not yet built

These features are specified in the design notes but do not work in the binary. Don't reach for them.

**Structs.** `struct Foo { ... }` is a parse error. Use a map as a record in the meantime; map keys make perfectly good field names.

```drang
$s := {reqs: 0, by_ip: {}}
$s["reqs"] += 1
say($s)
# {reqs: 1, by_ip: {}}
```

**Named arguments.** `f(port: 9090)` does not parse. Arguments are positional. (Default parameter values *are* supported, and are the idiomatic way to make an argument optional; see the functions section.)

**Variadic parameters.** `$a...` in a parameter list is deliberately out of scope. When you need a variable number of inputs, pass a single array.

**Automatic string coercion.** `"5" + 3` is not `8`. drang never coerces a string to a number for you. Convert explicitly.

```drang
say(int("5") + 3)   # 8
```

A bare `"5" + 3` does not merely return an error value: an arithmetic *operator* on a bad type pair aborts the program on the spot. The abort is uncatchable (this is the operator policy; see the surprises below).

```drang
say("5" + 3)
# drang: cannot use string and int with '+' (no automatic coercion: convert with int()/float()/str(), or ~ to join strings)
#   at prog.dr:1:5
#     say("5" + 3)
#         ^
```

The message names the fix: convert with `int()`/`float()`/`str()`, or join strings with the `~` concatenation operator.

Modules (`use`) and the one-liner `BEGIN`/`END` blocks *are* shipped and documented in their own sections; both were once listed here as missing.

### Behaviors that may surprise you

**Arithmetic overflow and divide-by-zero abort. They are not recoverable.** This is the single most important surprise, and it is easy to get wrong. drang draws a hard line between operators and builtins:

- An arithmetic *operator* (`+`, `-`, `*`, `/`, `%`) that overflows 64-bit range, hits a bad operand type, or divides by zero *aborts the program*. The abort is uncatchable: `//` and `?` cannot recover it, because the failure happens below the level where error values exist.
- A *builtin* that hits the same trouble returns an ordinary, catchable Err value.

So overflow and `1 / 0` abort, but `div(1, 0)` hands you a recoverable error.

```drang
say(9223372036854775807 + 1)
# drang: integer overflow: 9223372036854775807 + 1
#   at prog.dr:1:5
#     say(9223372036854775807 + 1)
#         ^
```

```drang
$safe := div(1, 0) // -1
say($safe)          # -1
```

The design intent: operator-level failures are programming mistakes that should stop the program at the exact source line, while builtins model runtime conditions you can reasonably handle. When you want arithmetic you can recover from, route it through the math builtins.

**`format()` uses `{}` and `{:spec}` placeholders, not `%`-style verbs.** Width, precision, alignment, sign, and base all live inside the spec (`{:>8}`, `{:.2f}`, `{:08x}`), not in `%d`/`%s` codes. A `%`-style template therefore contains *no* placeholders, so passing arguments to it is a (catchable) arity mismatch.

```drang
say(format("{:>3}: {:.2f}", "pi", 3.14159))   #  pi: 3.14
say(format("%d", 5))
# error: format: template has 0 placeholder(s) but got 1 argument(s). format uses {} / {:spec} placeholders, not %-style verbs (example: format("{} {:.2f}", name, x))
```

There is no `sprintf`; `format` is the only string-formatting builtin.

```drang
say(sprintf("%d", 5))
# drang: unknown function sprintf
```

**Runaway recursion becomes a catchable error, not a crash.** Call depth is bounded at 4000. Past the limit, the call returns an ordinary Err value that `//` recovers and `is_err` detects, rather than overflowing the native stack and killing the process. Ordinary deep recursion well within the limit is unaffected.

```drang
fn .f($n) { .f($n + 1) }        # never terminates
say(.f(0) // "stopped safely")  # stopped safely
say(err_msg(.f(0)))             # call depth exceeded 4000 (infinite recursion?)
```

**Running a `.bat` or `.cmd` is safe from argument injection.** The process builtins (`run`, `capture`, `pipe`, `stream_lines`, `start`) launch batch files through `cmd.exe` with defensive quoting, so an argument containing `"`, `&`, `|`, `<`, `>`, or `%VAR%` is delivered as inert data and never interpreted as a command. This closes the CVE-2024-24576 ("BatBadBut") class of hole. The nasty argument below arrives intact and is *not* executed:

```drang
$out := capture(".\\greet.bat", "a & echo PWNED")
say($out)
# GOT:["a & echo PWNED"]
```

The one restriction: a batch argument may not contain a NUL or a raw newline. Those are rejected with a catchable Err rather than risking a broken command line.

```drang
$r := capture(".\\greet.bat", "line1\nline2")
say(is_err($r))     # true
say(err_msg($r))    # .\greet.bat: winjob: batch argument contains a newline: "line1\nline2"
```

### Also absent

A few more design-notes items are not shipped: the `sh()` shell escape (`unknown function sh`), and the cross-machine and distribution growth paths.

String ranges are absent too, and the shape of that absence is worth stating precisely. drang has no character type, and range bounds must be integers. `'a'..'z'` is therefore not a range of one-character strings; it is an error.

```drang
say('a'..'e')
# error: range bounds must be ints, got string..string
```

To iterate over letters, build the array you want (for example with `chars()` over a literal) and loop that instead.

Finally, note that first-class builtin values *do* work now, so `map($xs, basename)` and similar point-free forms are fine; that was once listed here as a gap. These remaining items are tracked in the design and roadmap notes as deferred or planned, not shipped.
