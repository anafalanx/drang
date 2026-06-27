# drang — Language Manual

*A small, Perl-inspired, parallel scripting language for text processing, system glue, and orchestration — implemented in Go.*

> Every code example in this manual was executed against the interpreter; the shown output is real.

## Contents

- [Introduction](#introduction)
- [Lexical Structure, Declarations, Types, and Operators](#lexical-structure-declarations-types-and-operators)
- [Strings](#strings)
- [Control flow](#control-flow)
- [Functions, Lambdas, Closures, and Pipelines](#functions-lambdas-closures-and-pipelines)
- [Arrays, Maps, and the Collection Toolkit](#arrays-maps-and-the-collection-toolkit)
- [Errors as Values](#errors-as-values)
- [Regular expressions](#regular-expressions)
- [External Commands & Concurrency](#external-commands-concurrency)
- [In-language concurrency](#in-language-concurrency)
- [Files and Paths](#files-and-paths)
- [JSON](#json)
- [CSV](#csv)
- [One-liner mode](#one-liner-mode)
- [Quick reference: builtins](#quick-reference-builtins)
- [Not Yet — Known Gaps and Surprises](#not-yet-known-gaps-and-surprises)

## Introduction

drang is a small, Perl-inspired scripting language for **text processing and system glue** — the niche awk, sed, and Perl one-liners have always owned — implemented in Go. Its tagline is *"reads like Ruby, thinks like Perl, runs like Go."* It is a personal daily-driver: the language you reach for to wrangle text, shell out to other programs, and orchestrate small jobs.

Four ideas define it:

- **Perl's soul, not its warts.** First-class regex, terse one-liners, a single `$` sigil on every variable, and string interpolation — but without scalar/list context, typeglobs, `bless`, or the punctuation-variable zoo. One sigil covers all data: `$x` whether it holds a number, string, array, or hash. (Names carry their kind: `$` for data, `.` for your own functions, bare for builtins — see Functions.)
- **Effortless parallelism.** Real multi-core execution with no GIL, made safe *by subtraction* — top-level bindings are frozen constants and there are no mutable globals, so data-parallel combinators like `pmap` run lock-free.
- **First-class errors.** Failures are ordinary values you can inspect (`is_err`, `err_msg`, `err_code`) or propagate with a trailing `?`. There is no `$@` global and no exceptions-by-default; a dropped failure is a deliberate choice, not an accident.
- **Complete via Go.** The standard library is a curated binding over Go's — strings, files, `os/exec`, regex (RE2) — not a from-scratch reimplementation.

Under the hood drang runs on a tree-walking interpreter alongside a register bytecode VM kept byte-for-byte in lockstep with it, but that is an implementation detail — the language behaves identically either way.

### Running programs

drang reads a program from one of four places.

**A file** (`.dr` extension):

```
drang app.dr
```

**Inline**, with `-e`:

```
drang -e 'say("hello, world")'
```

```
hello, world
```

**Piped stdin** — when stdin is not a terminal, drang runs it as the program, so `cat foo.dr | drang` works:

```
echo 'say("from stdin")' | drang
```

```
from stdin
```

**The REPL** — run `drang` with no program on an interactive terminal (this is also what double-clicking the executable does), or force it with `drang --repl`. State persists across submissions:

```
drang> $x := 21
21
drang> $x * 2
42
drang> exit
```

Finally, **a standalone executable**: `drang build app.dr -o app` compiles a script into a single self-contained binary — the drang runtime with your program embedded — that needs no separate interpreter. Running it executes the embedded program, with arguments exposed as `$ARGV`. The build validates that the script parses and refuses to overwrite the source or the running interpreter; Windows and Linux are supported natively, and on macOS it best-effort ad-hoc re-signs the result.

### Flags

Leading flags are consumed up to the first non-flag token (the program); everything after the program becomes script arguments.

| Flag | Effect |
| --- | --- |
| `--run` | Run the program (the default; rarely written explicitly). |
| `--ast` | Print the parsed AST instead of running. |
| `--tokens` | Print the token stream instead of running. |
| `--version`, `-V` | Print the version and exit. |
| `--help`, `-h` | Print usage and exit. |

`--tokens` and `--ast` are debugging windows onto the front end:

```
drang --ast -e 'say(1+2)'
```

```
# ast of <-e>
(call say (+ 1 2))
```

### Script arguments and the environment

Arguments after the program are exposed as the array `$ARGV`; the process environment is the hash `$ENV`.

```
drang -e 'say($ARGV[0], $ARGV[1])' foo bar
```

```
foo bar
```

```
drang -e 'say($ENV["FOO"])'    # with FOO=bar in the environment
```

```
bar
```

For real command-line tools, `parse_args` turns `$ARGV` into a flat map — `--flag` becomes `true`, `--key=val` (or `--key val` when `key` is named in the optional second argument) becomes a string, and the leftover positionals collect under `"_"`:

```
drang -e '$o := parse_args($ARGV, ["out"]); say($o.out); say($o["_"])' --out=build x.dr y.dr
```

```
build
[x.dr, y.dr]
```

### A taste

Variables are declared with `:=` (a lexical) or `::=` (a frozen top-level constant); plain `=` reassigns. Builtins are called with parentheses, strings interpolate bare `$var` (or `${ expr }` for anything complex), and data nests transparently with `.` and `[]`:

```drang
$d := {users: [{name: "ada"}, {name: "alan"}]}
say($d.users[1].name)
say("count: ${len($d.users)}")
```

```
alan
count: 2
```

Subroutines use `fn` and carry a leading-dot sigil (`fn .name`, called `.name` — more on the three name sigils later), are first-class values, and pair with the higher-order combinators (`map`, `filter`, `reduce`, …) using `|args| body` lambdas. Loops are `for`-in over ranges, with postfix modifiers for one-liners:

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

And the headline trick — counting words across files **in parallel**, propagating any read failure with `?`, with no locks and no threads to manage:

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

## Lexical Structure, Declarations, Types, and Operators

This section covers the surface syntax: how a program is broken into statements, how you bind names, the value types, what counts as true, and the operator set. Variables always carry a `$` sigil.

### Comments

A `#` begins a comment that runs to the end of the line. There is no block-comment form.

```drang
# a full-line comment
$x := 10   # a trailing comment
```

### Statement termination

A newline ends a statement whenever the line *could* end there — that is, when the previous token is something that can finish an expression (a literal, a `$var`, an identifier, a closing `)` `}` `]`, or a `?`). Inside `(` or `[`, newlines are insignificant, so long calls and pipelines wrap freely; inside `{` blocks and at top level they terminate. A `;` also separates statements, letting you put several on one line:

```drang
$a := 1; $b := 2; say($a + $b)
```

```
3
```

### Declarations and assignment

Introduce a name with `:=` (mutable) or `::=` (constant). Once declared, a plain `=` reassigns it.

```drang
$count := 0      # mutable binding
$count = $count + 1
$pi ::= 3.14     # constant binding
```

In practice every binding is a `$`-name:

```drang
$count := 0
$count = $count + 1
$pi ::= 3.14
say($count, $pi)
```

```
1 3.14
```

Reassigning a constant is an error:

```drang
$k ::= 1
$k = 2
```

```
drang: cannot assign to constant $k
  at <-e>:2:6
    $k = 2
         ^
```

### Value types at a glance

| Type | Example literal / how you get one |
|------|-----------------------------------|
| `nil` | the absent/empty value (e.g. a missing map key); **no `nil` literal keyword** |
| `bool` | `true`, `false` |
| `int` | `42` (64-bit signed) |
| `float` | `3.5` (64-bit) |
| `string` | `"hello"` |
| `error` | from `fail("...")` and fallible builtins |
| `array` | `[1, 2, 3]` |
| `map` | `{"a": 1, "b": 2}` (insertion-ordered) |
| `range` | `1..5` (inclusive) |
| `function` | a lambda `|$x| $x * 2`, or `fn .name` declared and referenced as `.name` |
| `regex` | `re("[0-9]+")` |

(The concurrency section adds channel, task, and process handles.) `nil` is a real runtime value but has no source literal — writing `nil` yields `undefined: nil`. You obtain it from, e.g., an absent map key:

```drang
$m := {}
say($m["absent"])
```

```
nil
```

```drang
say(42)
say(3.5)
say([1, 2, 3])
say({"a": 1, "b": 2})
say(1..3)
```

```
42
3.5
[1, 2, 3]
{a: 1, b: 2}
1..3
```

### Truthiness

Falsy: `nil`, `false`, `0`, `0.0`, `""`, and **empty** containers (`[]`, `{}`, and an empty range). Everything else is truthy — including non-empty containers, functions, and *error values*.

```drang
fn .t($v) { if $v { say("truthy") } else { say("falsy") } }
$m := {}
.t($m["missing"]); .t(false); .t(0); .t(0.0); .t(""); .t([]); .t({})
.t(true); .t(1); .t(3.14); .t("x"); .t([1]); .t({"a": 1})
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
truthy
truthy
```

An error is truthy, so an `if` on it takes the true branch — use `is_err` to test for errors rather than truthiness:

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

**Arithmetic** `+ - * / %`. With two ints, `+ - * %` stay int. The big gotcha: `/` between two ints yields a **float** — there is **no integer-division operator**. For a truncated integer quotient, wrap with `int(...)`.

```drang
say(7 + 2, 7 - 2, 7 * 2, 7 % 2)   # int in, int out
say(7 / 2)                        # / always produces a float
say(int(7 / 2))                   # truncate back to int
```

```
9 5 14 1
3.5
3
```

`%` requires integer operands. Division or modulo by zero is a runtime error (`division by zero` / `modulo by zero`). Arithmetic operators do not coerce strings — `"a" + "b"` errors; use `~` to concatenate.

**String concat** `~`:

```drang
say("foo" ~ "bar" ~ "!")
```

```
foobar!
```

**Comparisons** `== != < <= > >=` (numbers compare numerically, strings lexicographically) and the **spaceship** `<=>`, which returns `-1`, `0`, or `1`:

```drang
say(1 < 2, 2 <= 2, "a" < "b")
say(1 <=> 2, 2 <=> 2, 3 <=> 2)
```

```
true true true
-1 0 1
```

**Logical** `and` / `or` / `not` (and `!` as a prefix synonym for `not`). `and`/`or` short-circuit — the right side is not evaluated when the left already decides the result:

```drang
fn .boom() { say("boom ran"); true }
say(false and .boom())
say(true or .boom())
say(!true, not false)
```

```
false
true
false true
```

(`boom ran` never prints — both calls are short-circuited.)

**Compound assignment** `+= -= *= /=`. Note that `/=` follows `/`'s float rule:

```drang
$n := 10
$n += 5    # 15
$n -= 2    # 13
$n *= 3    # 39
$n /= 2    # -> float
say($n)
```

```
19.5
```

**Ranges** `lo..hi` are inclusive of both ends:

```drang
say(len(1..5))
```

```
5
```

### What is *not* in the language

These are deliberate omissions — each is a parse error, not a missing feature you can polyfill with syntax:

- **No ternary** `?: ` — use `if/else`.
- **No exponent** `**` — there is no power operator.
- **No bitwise** operators (`&`, `|`, `^`, `<<`, `>>`).
- **No increment/decrement** `++` / `--` — use `+= 1` / `-= 1`.

```drang
say(2 ** 3)
```

```
# parse errors in <-e>
line 1: unexpected STAR "*"
line 1: expected end of statement, got INT "3"
```

```drang
$x := 1
$x++
```

```
# parse errors in <-e>
line 2: unexpected PLUS "+"
```

---

## Strings

drang strings are UTF-8 text. The most common form is a double-quoted literal, which both processes escapes and interpolates `$` expressions. Several quote operators and heredocs give you raw, interpolated, and word-list variants.

### String literals and the lenient escape policy

Inside `"..."`, exactly five escapes are decoded: `\n`, `\t`, `\r`, `\\`, and `\"`. Any other backslash escape is **left intact** — the backslash and the following character are kept verbatim. This "lenient" policy is deliberate: it makes regex classes and Windows paths far less painful, since you don't have to double every backslash.

```drang
say("a\tb\nc")
say("\d+")
```

```
a	b
c
\d+
```

The unknown escape `\d` survives as `\d`, ready to hand to a regex builtin.

Watch out for the one trap this creates with Windows paths: `\n`, `\t`, and `\r` are still real escapes, so a path segment that begins with `n`, `t`, or `r` gets mangled:

```drang
say("C:\dir\new")
```

```
C:\dir
ew
```

Here `\d` stayed literal but `\new` became `\` + a newline + `ew`. For paths use a raw quote operator (`q{...}`, below) or build the path with `join`.

### Interpolation

A `$name` splices a variable's value; `${expr}` splices any expression. Escape a literal dollar with `\$`.

```drang
$x := 42
say("x is $x")
say("sum=${$x + 4}")
say("\$x stays literal, $x splices")
```

```
x is 42
sum=46
$x stays literal, 42 splices
```

`${...}` can hold arithmetic, calls, and indexing:

```drang
$a := [10, 20, 30]
say("second is ${$a[1]}")
```

```
second is 20
```

One limitation: a `${...}` body cannot itself contain a double-quoted string while inside a `"..."` literal — the nested `"` confuses brace matching and you get an `unterminated ${...}` parse error. Reach for `qq{...}` (a different delimiter) when the interpolated expression needs a string literal:

```drang
say(qq{up is ${upper("hi")}})
```

```
up is HI
```

### Quote operators

Three quote operators avoid escaping gymnastics. The delimiter follows the operator with **no space**; allowed delimiters are `( [ { / |`. The paired ones (`()`, `[]`, `{}`) **nest**; `/` and `|` simply run to the next matching delimiter.

- **`q{...}`** — raw. No interpolation, no escape processing at all.
- **`qq{...}`** — interpolated, exactly like `"..."`.
- **`qw{...}`** — whitespace-split word list, producing an **array**.

```drang
$x := 9
say(q{no $x interp, \n stays literal})
say(qq{x=$x and a \t tab})
say(qw{red green blue})
```

```
no $x interp, \n stays literal
x=9 and a 	 tab
[red, green, blue]
```

`q{...}` is the clean way to write a Windows path or a regex with backslashes:

```drang
say(q(C:\Users\new\tmp))
```

```
C:\Users\new\tmp
```

Nesting and alternate delimiters:

```drang
say(q{outer {inner} done})
say(qq|x is ${3 + 4}|)
```

```
outer {inner} done
x is 7
```

`qw{...}` is a real array — splits on runs of whitespace and works with the usual array tools:

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

Note: the quote body is taken literally — there is no backslash escaping of the delimiter itself, so pick a delimiter the content avoids (or a nesting paired one).

### Heredocs

A heredoc starts with `<<TAG` and runs on the following lines until a line equal to `TAG`. The opener **must be the last thing on its line**. Forms:

- **`<<END`** and **`<<"END"`** — interpolate, like `qq`/`"..."`.
- **`<<'END'`** — raw, like `q`.
- **`<<~END`** — strips the common leading indentation of the body (the terminator may be indented too).

```drang
$name := "world"
$msg := <<END
Hello, $name!
Sum is ${2 + 3}.
END
say($msg)

$raw := <<'END'
Literal $name and \n here.
END
say($raw)
```

```
Hello, world!
Sum is 5.

Literal $name and \n here.
```

(A non-empty body keeps a trailing newline, which is why a blank line follows each block above.) The dedenting form `<<~END` removes the smallest shared indent; extra indentation is preserved relative to it:

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

### String builtins

| Builtin | Signature | Notes |
|---|---|---|
| `upper` / `lower` | `(s)` | ASCII/Unicode case fold |
| `trim` | `(s, cutset?)` | trims whitespace, or the given cutset of chars |
| `split` | `(s, sep?)` | no `sep` → split on whitespace runs; `""` → split into runes; else split on `sep` |
| `join` | `(array, sep?)` | renders each element and joins with `sep` (default `""`) |
| `replace` | `(s, old, new)` | replaces all occurrences |
| `contains` | `(s, needle)` | substring test (also works on arrays) |
| `starts_with` / `ends_with` | `(s, prefix/suffix)` | boolean |
| `repeat` | `(s, n)` | `n` copies; `n` must be an int |
| `chars` | `(s)` | array of single-rune strings |
| `lines` | `(s)` | CRLF-normalized; drops one trailing newline; `""` → `[]` |
| `format` | `(template, args...)` | `{}` placeholders; counts must match |

```drang
say(upper("Hi"))
say("[" ~ trim("  hi  ") ~ "]")
say(trim("xxhix", "x"))
say(split("a b  c"))
say(split("a,b,c", ","))
say(join(["a", "b", "c"], "-"))
say(replace("a.b.c", ".", "-"))
say(contains("hello", "ell"))
say(starts_with("foobar", "foo"))
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
true
true
ababab
[h, é, y]
[a, b, c]
```

Note `join` is polymorphic: `join(array, sep)` is the string join shown above, but `join` called on plain string arguments instead joins them as **path** components — see the filesystem section.

### `format` and its placeholders

`format` substitutes each `{}` placeholder with the next argument. Use `{{` and `}}` for literal braces.

```drang
say(format("{} + {} = {}", 2, 3, 5))
say(format("set {{x}} to {}", 9))
```

```
2 + 3 = 5
set {x} to 9
```

The number of `{}` placeholders must equal the number of arguments — otherwise `format` returns an **error value** rather than silently dropping or emitting literal braces. This deliberately catches the printf habit (`%s` has no `{}`):

```drang
say(format("{} and {}", 1))
say(format("%s", 5))
```

```
error: format: template has 2 "{}" placeholder(s) but got 1 argument(s)
error: format: template has 0 "{}" placeholder(s) but got 1 argument(s)
```

The result is a regular error value (the program does not crash); it propagates through `?` like any other drang error — see the error-handling section.

---

## Control flow

Control flow in drang is built from *statements*, not expressions. `if`, `while`, and `for` produce no value, so you cannot bind one to a variable:

```drang
$x := if 1 { 2 } else { 3 }
```

```
# parse errors in <-e>
line 1: unexpected IF "if"
line 1: expected end of statement, got INT "1"
```

Use a plain assignment inside the branches instead.

### if / else

`if cond { ... }` runs its block when the condition is truthy. An optional `else` block, or an `else if` chain, handles the rest. The condition is bare (no parentheses) and the braces are mandatory.

```drang
$g := 75
if $g >= 90 { say("A") } else if $g >= 70 { say("B") } else { say("C") }
```

```
B
```

The `else` may sit on the same line as the closing `}` or on the next line.

### unless

`unless` exists only as a *postfix modifier* (see below) — there is no block `unless` form. Writing `unless cond { ... }` is a parse error; use `if !cond { ... }` for a negated block.

### while and until

`while cond { ... }` loops while the condition stays truthy:

```drang
$i := 0
while $i < 3 { say($i); $i += 1 }
```

```
0
1
2
```

Like `unless`, `until` has no block form — it is postfix-only. For a negated block loop use `while !cond { ... }`.

### for-in

`for $x in iter { ... }` iterates a collection. With one loop variable you get each element; with two (`for $a, $b in iter`) the first is an index/key and the second the value. The iterable is snapshotted before the loop, so mutating it in the body does not disturb the iteration.

**Over an array** — one variable is the element, two are index + element:

```drang
for $i, $x in ["a", "b"] { say($i ~ ":" ~ $x) }
```

```
0:a
1:b
```

(`~` is the string-concat operator; `+` does not concatenate.)

**Over a map** — one variable iterates *values*, two iterate *key then value*:

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

**Over an integer range** `lo..hi` — inclusive of both ends; two variables give index + value:

```drang
for $n in 1..4 { say($n) }
```

```
1
2
3
4
```

A descending range such as `5..1` yields no iterations.

**Over a string** — character by character (by rune, so multibyte characters stay intact):

```drang
for $c in "héy" { say($c) }
```

```
h
é
y
```

### break and next

`break` exits the innermost enclosing loop; `next` skips to its next iteration. They bind to the *innermost* loop only.

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

These are checked at parse time: `break` or `next` outside any loop is a parse error, not a runtime one.

```drang
break
```

```
# parse errors in <-e>
line 1: 'break' outside a loop
```

Crucially, the loop nesting count resets at every function and lambda boundary. So `break`/`next` inside a lambda or `fn` — even one that is itself nested inside a loop — cannot escape to the outer loop, and is likewise a parse error:

```drang
for $n in 1..3 {
  each([10, 20], |$x| { break })
}
```

```
# parse errors in <-e>
line 2: 'break' outside a loop
```

### Postfix modifiers

Any simple statement may take a single trailing modifier: `if`, `unless`, `while`, `until`, or `for`. This is the only form `unless` and `until` come in.

```drang
$x := 5
say("yes") if $x > 3
say("ok") unless 0
```

```
yes
ok
```

`while` / `until` re-run the statement until the condition flips:

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

Postfix `for` iterates a collection, binding each element to the implicit variable `$_`:

```drang
say($_) for [10, 20, 30]
```

```
10
20
30
```

---

## Functions, Lambdas, Closures, and Pipelines

### Three name kinds, three sigils

drang carries a name's kind in a sigil at *every* use, so you always know what a name refers to:

- **`$name`** — data: variables and constants alike (`$count`, `$pi`).
- **`.name`** — a **user-defined function**: declared `fn .name()`, called `.name()`, and passed as a value as `.name`.
- **a bare `name`** — a **builtin or standard-library function**, the language's own verbs (`say`, `map`, `split`, …).

The leading `.` is the user-namespace sigil — read `.foo` as "`foo`, a member of the implicit user namespace." It is the *same* `.` as field access: `.foo` is a member of the implicit user namespace, just as `$m.foo` is a member of the map `$m`. Because your functions live in that `.` namespace, they can never collide with builtins or the stdlib — your `.split` and the builtin `split` coexist, so adding a new builtin can never break your code.

### Named functions: `fn .name`

Define a named function with `fn`, a **dotted** name, a parameter list of sigil variables, and a brace body. Call it through the same dot:

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

(`~` is the string-concatenation operator; `say` prints a line.) A bare `fn name` (no dot) is an error — user functions must be `fn .name`.

### Implicit and explicit return

A function returns the value of its **last expression** — no `return` needed. Because `if`/`else` is itself an expression, the branch value falls straight out:

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

Use explicit `return` for early exits. There is also a postfix `return … if` form. (Note `.abs` here is *your* function; the builtin `abs` is untouched in the `.` namespace and the two never clash:)

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

An anonymous function is written with pipe-delimited parameters followed by **either** a single expression **or** a `{ … }` block (the block also returns its last expression). Zero parameters is `||`. A lambda has no name of its own; bind it to a `$` variable and it is plain data, called through that `$` name (`$sq(5)`) — the `.` sigil is only for functions declared with `fn .name`:

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

The body parses at the lowest precedence, so a lambda absorbs operators and `|>` but stops at `,`, `)`, `]`, or a newline. Since a lambda is always the *last* argument to a higher-order function, its body runs cleanly to the closing `)`. (`||` is the zero-param lambda; there is no `||` boolean operator — use the `or` keyword.)

### Closures

Both named functions and lambdas are closures: they capture the variables of the scope where they are defined.

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

Crucially, **each iteration of a `for` loop captures its own binding** of the loop variable — closures made in different iterations do not share one mutable slot:

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

(If iterations shared a single `$i`, this would print `3` three times.)

### The pipeline operator `|>`

`x |> f(args)` desugars to `f(x, args)` — the left side is threaded in as the **first** argument. Chains read left-to-right, which is the natural reading order for glue code:

```drang
fn .double($x) { $x * 2 }
fn .add($a, $b) { $a + $b }

say(5 |> .double())          # .double(5)
say(5 |> .add(10))           # .add(5, 10)
say(3 |> .double() |> .add(1))   # .add(.double(3), 1)
```

```
10
15
7
```

`|>` is lexed greedily as a single two-character token, so it never collides with the lambda `|`.

To spread a pipeline across lines, put `|>` at the **end** of each line (a trailing `|>` continues the statement; a *leading* `|>` on a fresh line is read as a new statement and fails). Inside `(` or `[`, newlines are suppressed, so a leading `|>` is also fine when the whole chain is parenthesized:

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

### Higher-order functions (brief)

`map`, `filter`, `reject`, `reduce`, and friends are built in and **array-first**, precisely so they compose under `|>`. (Full coverage of the toolkit lives in the Collections section.)

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

Callbacks are arity-flexible: a one-parameter lambda receives the element; a two-parameter lambda also receives the index (and `reduce`'s lambda is `(acc, el)` or `(acc, el, index)`).

### Limitation: builtins are not first-class values yet

A **named user function** can be passed point-free:

```drang
fn .shout($s) { upper($s) }
say(["a", "b"] |> map(.shout))
```

```
[A, B]
```

A **builtin**, however, is not yet a first-class value, so passing its bare name fails:

```drang
say(["A.txt", "B.txt"] |> map(basename))   # drang: undefined: basename
```

Wrap the builtin in a lambda instead:

```drang
$paths := ["/a/b/foo.txt", "/c/d/bar.txt"]
say($paths |> map(|$p| basename($p)))
```

```
[foo.txt, bar.txt]
```

---

## Arrays, Maps, and the Collection Toolkit

drang has two built-in container types: ordered **arrays** (`[..]`) and insertion-ordered **maps** (`{k: v}`). Both work directly with the same higher-order toolkit (`map`, `filter`, `sort`, ...), which is the workhorse for text and glue scripts.

### Arrays

An array literal is a comma-separated list in square brackets. Elements may be any value and may be mixed:

```drang
say([10, 20, 30])
say([1, "two", [3, 4]])
```

```
[10, 20, 30]
[1, two, [3, 4]]
```

**Indexing** is zero-based with `arr[i]`. **Negative indices** count from the end (`-1` is the last element):

```drang
say([10, 20, 30][1])     # 20
say([10, 20, 30][-1])    # 30
say([10, 20, 30][-2])    # 20
```

**Out-of-bounds** access is a catchable error value, not a crash, and the same applies to a negative index that reaches before the start:

```drang
say([1, 2][5])           # error: index 5 out of range (len 2)
say([10, 20, 30][-4])    # error: index -4 out of range (len 3)
```

`len` returns the element count (and works on maps, ranges, and strings too):

```drang
say(len([1, 2, 3]))      # 3
```

**`push` and `pop`** mutate the array in place. `push` appends one or more values and returns the same array; `pop` removes and returns the last element, erroring on an empty array:

```drang
$a := [1, 2]
push($a, 3, 4)
say($a)                  # [1, 2, 3, 4]

$a := [1, 2, 3]
say(pop($a))             # 3
say($a)                  # [1, 2]

say(pop([]))             # error: pop from empty array
```

**`take` / `drop` / `uniq`** return *new* arrays and never mutate. `take(arr, n)` keeps the first `n`; `drop(arr, n)` skips the first `n`; both clamp `n` to the array's length. `uniq` removes duplicates (by structural equality), preserving first-seen order:

```drang
say(take([1, 2, 3, 4, 5], 2))    # [1, 2]
say(take([1, 2], 9))             # [1, 2]   (clamped)
say(drop([1, 2, 3, 4, 5], 2))    # [3, 4, 5]
say(uniq([1, 1, 2, 3, 3, 3, 1])) # [1, 2, 3]
```

### Maps

A map literal is `{key: value, ...}`. Keys may be barewords (treated as strings) or any scalar expression; iteration follows **insertion order**:

```drang
$m := {name: "ada", age: 36}
say($m)                  # {name: ada, age: 36}
say({z: 1, a: 2, m: 3})  # {z: 1, a: 2, m: 3}   (order preserved, not sorted)
```

Access a value with **dot syntax** `$m.field` (field name as a string key) or **bracket syntax** `$m[key]` (any key expression). A **missing key reads as `nil`**, not an error:

```drang
$m := {name: "ada"}
say($m.name)             # ada
say($m["name"])          # ada
say($m["missing"])       # nil
say($m.zzz)              # nil
```

Assign into a map (creating or updating the key) with `$m[key] = value`:

```drang
$m := {}
$m["x"] = 9
say($m)                  # {x: 9}
```

**Inspection and mutation builtins:**

```drang
$m := {a: 1, b: 2}
say(has($m, "a"), has($m, "z"))   # true false
say(keys($m))                     # [a, b]
say(values($m))                   # [1, 2]
say(pairs($m))                    # [[a, 1], [b, 2]]
delete($m, "a")
say($m)                           # {b: 2}
```

`keys`, `values`, and `pairs` all return fresh arrays in insertion order, which makes iteration straightforward:

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

**Only scalar keys are hashable.** Integers and strings are fine; an array (or other container) used as a key is a catchable error:

```drang
$m := {1: "one", 2: "two"}
say($m[1])               # one

$m := {a: 1}
say($m[[1, 2]])          # error: unhashable map key: array
```

### The higher-order toolkit

These functions operate on arrays and take a callback written as a closure `|$x| ...` (or `|$x, $i|` to also receive the element's index). They compose cleanly with the pipe operator `|>`, where `xs |> f(args)` calls `f(xs, args)`.

**`map`** — transform each element into a new array:

```drang
say([1, 2, 3] |> map(|$x| $x * $x))      # [1, 4, 9]
```

**`filter`** / **`reject`** — keep / drop elements matching a predicate:

```drang
say([1, 2, 3, 4, 5, 6] |> filter(|$x| $x % 2 == 0))   # [2, 4, 6]
say([1, 2, 3, 4, 5, 6] |> reject(|$x| $x % 2 == 0))   # [1, 3, 5]
```

**`find`** — the first matching element, or `nil` if none match:

```drang
say([3, 8, 5, 12, 2] |> find(|$x| $x > 10))   # 12
say([1, 2] |> find(|$x| $x > 10))             # nil
```

**`any`** / **`all`** / **`count`** — predicate aggregates:

```drang
say([1, 2, 3] |> any(|$x| $x > 2))            # true
say([2, 4, 6] |> all(|$x| $x % 2 == 0))       # true
say([1, 2, 3, 4, 5] |> count(|$x| $x % 2 == 1))  # 3
```

**`flat_map`** — map then concatenate the resulting arrays one level deep:

```drang
say([1, 2, 3] |> flat_map(|$x| [$x, $x * 10]))   # [1, 10, 2, 20, 3, 30]
```

**`reduce(arr, init, fn)`** — fold left with an explicit initial accumulator (note the 3-argument call form, not a pipe target's natural shape):

```drang
say(reduce([1, 2, 3, 4], 0, |$acc, $x| $acc + $x))   # 10
```

### The ordering family

`sort` returns a new array in natural ascending order (numbers numerically, strings lexicographically):

```drang
say(sort([3, 1, 2, 10, 5]))                  # [1, 2, 3, 5, 10]
say(sort(["banana", "apple", "cherry"]))     # [apple, banana, cherry]
```

For a custom order, pass a **comparator** `|$a, $b| ...` that returns a negative number, `0`, or a positive number. The **`<=>` (spaceship) operator** computes exactly that three-way comparison, so it pairs naturally with `sort`:

```drang
say(1 <=> 2, 2 <=> 2, 3 <=> 2)               # -1 0 1
say(sort([3, 1, 2], |$a, $b| $b <=> $a))     # [3, 2, 1]   (descending)
```

`sort_by`, `min_by`, and `max_by` take a **key function** instead of a comparator and order by the computed key (`sort_by` computes each key once). `min_by`/`max_by` return the extreme element, or `nil` for an empty array:

```drang
say(sort_by(["ccc", "a", "bb"], |$s| len($s)))   # [a, bb, ccc]
say(min_by(["ccc", "a", "bb"], |$s| len($s)))    # a
say(max_by(["ccc", "a", "bb"], |$s| len($s)))    # ccc
say(min_by([], |$x| $x))                         # nil
```

Because every collection function returns a value (a new array, or `nil`), they chain end to end:

```drang
say([5, 3, 8, 1, 9, 2] |> filter(|$x| $x > 2) |> sort() |> take(3))   # [3, 5, 8]
```

### Prelude: collection helpers written in drang

A handful of everyday helpers are part of the standard library but written in drang
itself — an embedded *prelude* — rather than in Go. They are pure compositions of the
builtins above, available unqualified like any builtin:

| Helper | Meaning |
|--------|---------|
| `flatten(xss)` | concatenate one level of nesting: `[[1, 2], [3]]` → `[1, 2, 3]` |
| `sum_by(xs, f)` | sum of `f` over each element |
| `tally(xs)` | count occurrences → a map `{value: count}` |
| `count_by(xs, f)` | like `tally`, but keyed by `f(x)` |
| `chunk(xs, n)` | split into `n`-sized pieces (`n < 1` is an error) |
| `zip(a, b)` | pair two arrays element-wise, truncating to the shorter |

Writing part of the stdlib in drang keeps the Go core small and pressure-tests the
language; the rule for what goes in Go vs drang is recorded in DESIGN.

---

## Errors as Values

In drang an error is not an exception — it is an ordinary value with a tag, like an int or a string. A fallible operation returns either its normal result or an **Err** value carrying a message and an integer code. Nothing unwinds on its own; you decide what to do with the Err: inspect it, recover from it, or propagate it.

Three pieces make up the model: the `?` postfix operator (propagate), the `//` operator (recover with a fallback), and the inspectors `is_err` / `err_code` / `err_msg`. You create errors with `fail`.

### Inspecting errors

`is_err(x)` reports whether `x` is an Err value. `err_code(x)` and `err_msg(x)` pull out its code and message. On a *non*-error they return the neutral values `0` and `""`, so `err_code(run(cmd))` reads naturally as "the exit code, 0 on success."

```drang
$r := fail("boom")
say(is_err($r), err_code($r), err_msg($r))
say(err_code(42), err_msg(42) == "")
```

```
true 1 boom
0 true
```

An Err value prints through `say` as `error: <msg>`:

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

Note: `fail` only honors a message — it does not take a second code argument, and the Err code is always `1`. Custom, non-1 codes come from operations that carry one naturally — most importantly subprocess builtins, which fold the child's exit status into the Err code (see below).

### Recovering with //

`risky() // fallback` evaluates `risky()`; if the result is an Err value **or** `nil`, it evaluates and returns `fallback` instead. Otherwise the original value passes through. This is the workhorse for "try, but have a default."

```drang
say(int("100") // 0, int("oops") // 0)
```

```
100 0
```

`//` triggers *only* on Err or nil — other falsy values (`0`, `""`, `false`) are real results and pass straight through:

```drang
say(0 // 99, "" // "x", false // "y")
```

```
0  false
```

### Propagating with ?

The `?` postfix operator is the early-exit half of the model. `expr?` evaluates `expr`; if it is an Err, `?` propagates that error out of the **enclosing function**. If it is not an Err, the value flows through unchanged. This lets you write the happy path without per-call checks:

```drang
fn .parse($s) {
  $n := int($s)?      # bail out of .parse() if $s isn't an int
  return $n * 2
}
say(.parse("21"))
```

```
42
```

The key rule: `?` propagates only to the nearest call boundary. When the propagated error reaches the point where the function was called, it turns back into an ordinary Err **value** — it does not keep unwinding. So a caller can simply recover it:

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

At the **top level** there is no enclosing function, so a `?` that fires there aborts the whole program. The process exits with the Err's code (clamped to `1..255`), printing `drang: <msg>` to stderr:

```drang
fail("nope")?
say("unreached")
```

```
drang: nope
```

```
exit status 1
```

This top-level behavior is what makes `?` useful for scripts: propagate failures up to `main`, and the program exits with a meaningful status automatically.

### The builtin convention: arg-count aborts, bad values are catchable

Builtins distinguish two kinds of wrongness:

- A wrong **argument count** is a programmer error — a hard abort that `?`/`//` cannot intercept. It stops the program with a source location.
- A wrong **type** or **bad value** is a runtime condition — a catchable Err value you can recover.

```drang
say(is_err(int([1, 2])))   # wrong type -> catchable Err
```

```
true
```

```drang
say(int(1, 2) // 99)       # wrong arg count -> hard abort, // can't save it
```

```
drang: int expects 1 argument, got 2
  at <-e>:1:5
    say(int(1, 2) // 99)
        ^
```

So `int("x") // 0` is safe and idiomatic, but `int() // 0` (or `int(1,2) // 0`) is a bug that will rightly crash.

### Recovering a failed command

Subprocess builtins follow the same value-result convention, and they are where non-1 codes appear. `run(...)` returns `true` on success or a catchable Err carrying the child's exit code (`127` if the command could not be started). `capture(...)` returns the child's trimmed stdout, or an Err on failure.

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

Because the Err carries the real exit code, you can branch on it — e.g. treating `grep`'s exit 1 ("no match") differently from a genuine error:

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

And `?` plumbs a command's exit code straight through to the process when it propagates to the top level:

```drang
run("cmd", "/c", "exit 3")?
```

```
drang: cmd exited with code 3
```

```
exit status 3
```

### Putting it together

A guard that returns an Err, propagated or recovered by the caller's choice:

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

The shape to internalize: `fail` and failing builtins *make* Err values; `?` *moves* them up to the call boundary (aborting at the top level with the right exit code); `//` *absorbs* them with a default; and `is_err`/`err_code`/`err_msg` *read* them when you need to branch.

---

## Regular expressions

drang's regex engine is Go's [RE2](https://github.com/google/re2/wiki/Syntax): matching is **linear-time** with no catastrophic backtracking, but in exchange the pattern syntax has **no backreferences** (`\1` inside a pattern) and no lookaround. Patterns come in two forms — a `qr//` literal that the lexer turns into a compiled, first-class `regex` value, or a plain string that the regex builtins compile on demand. Compiled regexes are immutable, cached, and safe to share across `pmap` workers.

### `qr//` literals

A `qr` literal compiles a pattern at lex time into a reusable `regex` value:

```drang
say(qr/\d+/)
```

```
qr/\d+/
```

The body is taken **literally** — backslashes are passed straight through to RE2, so you do not double them the way you would in a `"..."` string.

**Flags** follow the closing delimiter: `i` (case-insensitive), `m` (multi-line `^`/`$`), `s` (dotall — `.` matches newline), `U` (ungreedy — swaps greedy/lazy). They are baked into the pattern as Go inline flags, which is visible when you print the value:

```drang
say(qr/foo/i)
say(qr/foo/ims)
```

```
qr/(?i)foo/
qr/(?ims)foo/
```

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

An unknown flag letter is a **parse error**, caught before the program runs:

```drang
say(qr/foo/x)
```

```
# parse errors in <-e>
line 1: unexpected ILLEGAL "invalid regex flag after qr//"
line 1: expected end of statement, got IDENT "x"
```

**Delimiters.** Besides `/`, you may open a `qr` literal with `|`, `(`, `[`, or `{`. Same-char delimiters (`/`, `|`) run to the next occurrence; paired delimiters (`(...)`, `[...]`, `{...}`) **nest**, which lets the pattern contain unbalanced copies of the delimiter char. Pick a delimiter your pattern avoids:

```drang
say(matches("a/b", qr|/|))          # pattern contains a slash → use | delimiter
say(match("ab", qr((a)(b))))        # ( ) nest around the groups
say(find_all("aaa", qr{a{1}}))      # { } nest around the quantifier
```

```
true
[ab, a, b]
[a, a, a]
```

### `re(pattern)` — compile a dynamic pattern

`qr//` is a literal; when the pattern is built at runtime (e.g. interpolated), use `re()` to compile a string into a reusable `regex` value. An already-compiled regex passes straight through:

```drang
$p := "\d+"
$rx := re($p)
say(matches("a9", $rx))
say($rx)
say(re(qr/x/i))   # regex in → same regex out
```

```
true
qr/\d+/
qr/(?i)x/
```

### The matching builtins

Every regex builtin takes the pattern as **either a string or a compiled `regex` value** — they are interchangeable. Using a `qr//` value (or one from `re()`) reuses the compiled object instead of recompiling.

| Builtin | Returns |
|---|---|
| `matches(s, p)` | bool — does `p` match anywhere in `s` |
| `match(s, p)` | `[full, group1, group2, ...]`, or `nil` if no match |
| `find_all(s, p)` | array of every (full) match, in order |
| `gsub(s, p, repl)` | `s` with every match replaced by `repl` |

```drang
say(matches("Hello World", qr/world/i))
say(match("2026-06-26", qr/(\d{4})-(\d{2})-(\d{2})/))
say(match("nope", qr/\d+/))
say(find_all("a1 b22 c333", qr/\d+/))
```

```
true
[2026-06-26, 2026, 06, 26]
nil
[1, 22, 333]
```

String and `qr//` pattern arguments are equivalent — note the string form needs the backslash that the literal form does not:

```drang
say(find_all("a1b2", "\d"))
say(find_all("a1b2", qr/\d/))
```

```
[1, 2]
[1, 2]
```

### `gsub` and backreferences in the replacement

In `gsub`, the **replacement** string uses Go's `$1` / `${name}` substitution (this is replacement-side substitution, not a pattern backreference — RE2 has none of those):

```drang
say(gsub("2026-06-26", qr/(\d{4})-(\d{2})-(\d{2})/, "$3/$2/$1"))
```

```
26/06/2026
```

For `${name}` references, name the groups with RE2's `(?P<name>...)` syntax. Beware: a `"..."` double-quoted replacement is interpolated by drang first, so use a non-interpolating literal (`q{...}`) to keep the `${...}` intact for `gsub`:

```drang
say(gsub("john smith", qr/(?P<first>\w+) (?P<last>\w+)/, q{${last}, ${first}}))
```

```
smith, john
```

### Bad patterns are catchable errors

A malformed **string** pattern is not a crash — it surfaces as a first-class `Err` value the program can inspect with `is_err`:

```drang
$e := matches("x", "(")
say(is_err($e))
```

```
true
```

The same `Err` flows out of `re()`, and uncaught it prints the RE2 diagnostic:

```drang
say(re("("))
```

```
error: bad regex "(": error parsing regexp: missing closing ): `(`
```

Because the engine is RE2, a backreference inside the **pattern** is simply not valid syntax and produces such an `Err`:

```drang
say(re("(a)\1"))
```

```
error: bad regex "(a)\\1": error parsing regexp: invalid escape sequence: `\1`
```

---

## External Commands & Concurrency

drang is a glue language, so running other programs and doing work in parallel are
first-class. External commands go through `os/exec` directly — **no shell is
involved** and arguments are passed verbatim (no word-splitting, no glob
expansion). Failures are values: a failed command returns a catchable `Err`
carrying the child's exit code, which you propagate with `?`, recover with `//`,
or inspect with `is_err`/`err_code`/`err_msg`.

> All examples below were run on Windows, so they shell out to `cmd /c`,
> `findstr`, `ping`, etc. The drang surface is identical on any platform — only
> the command names differ. Examples deliberately use short, non-destructive
> commands.

### `run` — execute and stream stdio

`run(cmd, args..., {opts}?)` runs a command with the child's stdin/stdout/stderr
wired straight through to drang's. It returns `true` on success (so it composes
with `if` and `//`) or an `Err` on failure.

```drang
$ok := run("cmd", "/c", "exit 0")
say("success returns true: $ok")
$bad := run("cmd", "/c", "exit 5")
say("failure is_err: ${is_err($bad)}  code: ${err_code($bad)}")
```
```
success returns true: true
failure is_err: true  code: 5
```

Array arguments are **flattened one level**, so you can build an argv list and
splat it: `run("git", ["log", "--oneline"])`.

### `capture` — collect stdout

`capture(...)` buffers the child's stdout and returns it as a **trimmed string**
on success, or an `Err` (with the child's stderr folded into the message) on
failure.

```drang
$ver := capture("cmd", "/c", "ver")
say($ver)
$where := capture("where", "cmd")
say("where cmd -> $where")
```
```
Microsoft Windows [Version 10.0.26200.6899]
where cmd -> C:\Windows\System32\cmd.exe
```

### `pipe` — a pipeline, no shell

`pipe([cmd, args...], [cmd, args...], ..., {opts}?)` wires each stage's stdout to
the next stage's stdin through real OS pipes (streamed, not buffered between
stages). Each stage is an array. It returns the **last stage's trimmed stdout**.
This is native `os/exec` wiring — there is still no shell.

```drang
$out := pipe(["cmd", "/c", "echo apple& echo banana& echo cherry"],
             ["findstr", "an"])
say("pipe -> $out")
```
```
pipe -> banana
```

(For genuine shell features — globbing, `&&`, redirection — invoke `cmd /c "..."`
yourself as a single stage.)

### Options: `{cwd, env, stdin, timeout}`

A trailing map sets per-command options on `run`, `capture`, `pipe`, and
`each_line`. `env` is **overlaid** onto the inherited environment (matched
case-insensitively, per Windows); `timeout` is in **milliseconds** and `0` means
no limit.

```drang
$dir := capture("cmd", "/c", "cd", {cwd: "C:\\Windows"})
say("cwd -> $dir")
$e := capture("cmd", "/c", "echo", "%GREETING%", {env: {GREETING: "hi there"}})
say("env -> $e")
$s := capture("findstr", "world", {stdin: "hello\nworld\nfoo\n"})
say("stdin -> $s")
```
```
cwd -> C:\Windows
env -> hi there
stdin -> world
```

There is no global `cd`; per-command `{cwd}` is the only way to change the working
directory (a process-wide chdir would race across goroutines).

### Error codes: 124 (timeout) and 127 (cannot start)

Two exit codes are synthesized, matching GNU `timeout`/shell conventions. On
**timeout** the whole process *tree* is killed (not just the direct child, so a
`cmd /c <spawner>` whose grandchild holds the pipe can't keep the call blocked):

```drang
$r := run("cmd", "/c", "ping -n 5 127.0.0.1 >NUL", {timeout: 300})
say("is_err: ${is_err($r)}  code: ${err_code($r)}")
```
```
is_err: true  code: 124
```

When a command **cannot be started** (not found, not executable), the code is
`127`:

```drang
$r := run("no_such_program_xyz")
say("code: ${err_code($r)}  msg: ${err_msg($r)}")
```
```
code: 127  msg: no_such_program_xyz: exec: "no_such_program_xyz": executable file not found in %PATH%
```

`pipe` follows bash's pipeline semantics: `127` if a stage can't start, `124` on
timeout, otherwise the **last** stage's exit code.

### `each_line` — stream output line by line

`each_line(cmd, args..., {opts}?, |$line| { ... })` invokes the callback for each
line of stdout **as it arrives** (not buffered), ideal for build logs or tails.
It returns `true` on success or an `Err` (exit code / `124` timeout) once the
command finishes.

```drang
$n := 0
each_line("cmd", "/c", "echo one& echo two& echo three", |$line| {
  $n = $n + 1
  say("[$n] $line")
})
say("total lines: $n")
```
```
[1] one
[2] two
[3] three
total lines: 3
```

### `start` — a detached process handle

`start(cmd, args...)` launches a child **without waiting** (the equivalent of
`cmd &`), with stdio detached, and returns a process handle. Three builtins act
on it: `pid(p)` reads the PID, `await(p)` blocks for its exit status (`true`, or
an `Err` with the code), and `kill(p)` terminates the whole tree.

```drang
$p := start("cmd", "/c", "exit 3")
say("pid > 0: ${pid($p) > 0}")
$status := await($p)
say("await -> is_err: ${is_err($status)}  code: ${err_code($status)}")
```
```
pid > 0: true
await -> is_err: true  code: 3
```

`kill` works on a still-running process; its pending `await` then yields an error:

```drang
$p := start("cmd", "/c", "ping -n 30 127.0.0.1 >NUL")
kill($p)
say("after kill, is_err: ${is_err(await($p))}")
```
```
after kill, is_err: true
```

---

## In-language concurrency

drang has **real multi-core parallelism with no GIL** — goroutine-backed, made
safe by *subtraction*: top-level bindings are frozen constants, scoping is
lexical-only, strings are immutable, and there is no shared mutable global state.
With almost nothing shared, parallel execution needs no locks.

### `spawn` / `await` — tasks

`spawn(fn, args...)` runs a drang function on its own goroutine (args are
deep-copied in — copy-on-send) and returns a `Task`. `await(task)` blocks for the
result. (`await` accepts a `Task` *or* a process handle from `start` — one "await
any async handle".)

```drang
fn .work($n) { $n * 2 }
$tasks := [1, 2, 3, 4] |> map(|$n| spawn(.work, $n))
$results := $tasks |> map(|$t| await($t))
say("fan-out: $results")
```
```
fan-out: [2, 4, 6, 8]
```

An error inside a spawned task (returned, `?`-propagated, or panicked) is captured
and surfaced by `await`, so `await($t)?` propagates and `await($t) // x` recovers:

```drang
fn .boom() { fail("worker failed") }
$res := await(spawn(.boom))
say("is_err: ${is_err($res)}  msg: ${err_msg($res)}")
```
```
is_err: true  msg: worker failed
```

### Channels: `chan` / `send` / `recv` / `recv_ok` / `close` / `drain`

`chan()` makes an unbuffered channel; `chan(n)` a buffered one. A channel is the
one intentionally *shared* value type. `send` blocks until received (and copies
the value — copy-on-send); `recv` blocks for the next value (and yields `undef`
once the channel is closed and drained); `recv_ok` returns `[value, ok]`; `close`
is idempotent; `drain` collects every remaining value into an array, blocking
until the channel is closed.

```drang
$c := chan(3)
fn .produce($ch) {
  for $i in 1..3 { send($ch, $i * 10) }
  close($ch)
}
$t := spawn(.produce, $c)
$all := drain($c)
await($t)
say("drained: $all")
```
```
drained: [10, 20, 30]
```

```drang
$c := chan()
fn .worker($ch) {
  send($ch, "first")
  send($ch, "second")
  close($ch)
}
$t := spawn(.worker, $c)
say("recv: ${recv($c)}")
$pair := recv_ok($c)
say("recv_ok: $pair")
say("after close, undef: ${not recv($c)}")
await($t)
```
```
recv: first
recv_ok: [second, true]
after close, undef: true
```

`send` on a closed channel is a catchable `Err`, never a crash.

### `pmap` — parallel map across CPU cores

`pmap(arr, fn)` is the high-level workhorse: the same contract as `map`
(array-first so `$xs |> pmap(f)` composes; element + optional index callback;
results in **input order**; fail-loud on the first `Err`), but fanned across a
bounded `NumCPU` worker pool for **true parallelism**.

```drang
$squares := [1, 2, 3, 4, 5] |> pmap(|$x| $x * $x)
say("pmap squares: $squares")
```
```
pmap squares: [1, 4, 9, 16, 25]
```

The win is real, not cooperative. Four `ping -n 3` calls (each ~2s of wall time),
`map` vs `pmap`, measured end-to-end:

```
serial (map):    8.14s
parallel (pmap): 2.05s
```

**The purity contract.** A `pmap` callback must be **pure**: it may read frozen
top-level constants and its own parameters, but it must not mutate shared captured
state. Each element is **deep-copied to its worker**, so mutating the element only
affects that worker's private copy:

```drang
$rows := [[1], [2], [3]]
$out := pmap($rows, |$row| {
  push($row, 99)   # mutates the worker's COPY
  len($row)
})
say("callback saw lengths: $out")
say("original rows unchanged: $rows")
```
```
callback saw lengths: [2, 2, 2]
original rows unchanged: [[1], [2], [3]]
```

The language deliberately offers no shared accumulator to reduce into, so the
canonical racy form is largely unwriteable. Mutating a *captured mutable lexical
container* from a parallel callback is documented-undefined — keep callbacks pure.

Like `map`, `pmap` is **fail-loud**: the first `Err` a callback produces becomes
the whole result and stops further work.

```drang
$r := pmap([1, 2, 3], |$x| {
  if $x == 2 { fail("boom on 2") } else { $x }
})
say("is_err: ${is_err($r)}  msg: ${err_msg($r)}")
```
```
is_err: true  msg: boom on 2
```

Parallel subprocesses are just `pmap` over commands — each call gets its own
`{timeout}`/`cwd`/`env` and runs lock-free:

```drang
$versions := ["git", "go", "node"] |> pmap(|$tool| capture($tool, "--version") // "(missing)")
```

---

## Files and Paths

drang treats paths as plain strings and leans on Go's `os`/`filepath` underneath.
The builtins fall into four groups: **file I/O** (`read_file`, `write_file`,
`lines`), **filesystem ops** (`exists`, `is_dir`, `mkdir`, `glob`, `rename`, `rm`,
`copy`, `size`), **pure path transforms** (`dirname`, `basename`, `ext`, `stem`,
`abs`, `slash`), and **freshness gates** for build scripts (`mtime`, `newer`,
`stale`).

A guided tour — everything below was run end to end. It builds a scratch
directory under the system temp, writes a file, reads it back, globs it, and
cleans up:

```drang
# A scratch dir under the system temp, cleaned up at the end.
$dir := join($ENV["TEMP"], "drang_fs_tour")
rm($dir)              # idempotent: no error if absent
mkdir($dir)          # mkdir -p semantics

$f := join($dir, "notes.txt")
write_file($f, "alpha\nbeta\ngamma\n")

say("exists : " ~ exists($f))
say("size   : " ~ size($f))
say("lines  : " ~ len(lines(read_file($f))))

for $m in glob(join($dir, "*.txt")) {
  say("glob   : " ~ basename($m))
}

rm($dir)             # tidy up — nothing left behind
say("gone   : " ~ !exists($dir))
```

```
exists : true
size   : 17
lines  : 3
glob   : notes.txt
gone   : true
```

Two conventions show up throughout: `join(...)` assembles path segments
OS-correctly, and `~` concatenates strings. Use `:=` to declare a variable.

### Error model

Fallible filesystem builtins do **not** throw on failure — they return a
catchable `Err` value (exit code 1). You handle it three ways:

- `expr?` — propagate: if `expr` is an `Err`, abort the program with that message.
- `expr // fallback` — recover: substitute `fallback` when `expr` is an `Err`.
- let the `Err` flow as an ordinary value.

```drang
$txt := read_file("does_not_exist_xyz.txt") // "DEFAULT"
say("recovered: " ~ $txt)            # recovered: DEFAULT
```

The `?` form aborts with the underlying OS error and a non-zero exit:

```drang
read_file("nope_missing.txt")?
say("unreached")
```

```
drang: read_file nope_missing.txt: open nope_missing.txt: The system cannot find the file specified.
```

`exists` and `is_dir` are the exception: they always return a plain `bool`, so
they drop straight into `if`/`unless` without recovery plumbing.

### File I/O: read_file, write_file, lines

- `read_file(path)` → the whole file as a string, or `Err` if unreadable.
- `write_file(path, content)` → writes `content` (any value, rendered like
  `say`) to `path`, creating or truncating it; returns the path, or `Err`.
- `lines(text)` → splits a **string** into an array of lines. It is *not* a file
  reader — pair it with `read_file`: `lines(read_file(path))`.

`lines` normalizes CRLF to LF and drops a single trailing newline, so
`"a\nb\n"` yields two elements and `""` yields an empty array:

```drang
say(len(lines("")))        # 0
say(len(lines("a\nb\n")))  # 2   (trailing newline dropped)
say(len(lines("a\nb")))    # 2
```

`write_file` accepts non-strings, rendering them the way `say` would —
`write_file(f, 42)` stores the text `42`.

### Filesystem ops

- `exists(p)` → bool, true if the path exists.
- `is_dir(p)` → bool, true only if `p` exists *and* is a directory.
- `mkdir(p)` → creates `p` and any missing parents (`mkdir -p`); returns `p`.
- `glob(pattern)` → sorted array of matching paths; **no match is an empty
  array, not an error**. Supports `*`, `?`, `[...]`, and a recursive `**`
  segment that spans directories.
- `rename(src, dst)` → moves/renames; returns `dst`.
- `copy(src, dst)` → copies a file, or recursively copies a directory tree;
  returns `dst`.
- `rm(p)` → removes a file or directory tree, recursively and idempotently (no
  error if absent). It is named `rm` because `delete` is the map-key remover.
- `size(p)` → file size in bytes as an int, or `Err` if the path is missing.

```drang
$dir := join($ENV["TEMP"], "drang_fs_demo2")
rm($dir)
mkdir($dir)

$src := join($dir, "src.txt")
write_file($src, "hello")

copy($src, join($dir, "copy.txt"))
rename(join($dir, "copy.txt"), join($dir, "renamed.txt"))

say("orig  : " ~ exists($src))                       # orig  : true
say("copy  : " ~ exists(join($dir, "copy.txt")))     # copy  : false  (renamed away)
say("moved : " ~ exists(join($dir, "renamed.txt")))  # moved : true
rm($dir)
```

`glob` with `**` walks subdirectories (results are sorted; the walk root itself
is never yielded):

```drang
$all := glob(join($dir, "**", "*.go"))
for $m in $all { say(slash($m)) }
```

```
C:/Users/anafa/AppData/Local/Temp/drang_fs_demo4/sub/deep.go
C:/Users/anafa/AppData/Local/Temp/drang_fs_demo4/top.go
```

### Pure path helpers

These are string transforms — they never touch the disk and never return an
`Err` (a non-string argument is a hard error). On Windows they use the native
separator unless noted.

| Builtin | Input | Result |
|---|---|---|
| `dirname(p)` | `.../notes.txt` | `...` (the directory) |
| `basename(p)` | `.../notes.txt` | `notes.txt` |
| `ext(p)` | `.../notes.txt` | `.txt` (leading dot) |
| `stem(p)` | `.../notes.txt` | `notes` (basename minus ext) |
| `abspath(p)` | `foo/bar.txt` | absolute path against the CWD (numeric absolute value is `abs`) |
| `slash(p)` | `C:\a\b` | `C:/a/b` (forward slashes) |

```drang
$f := "C:/Users/anafa/AppData/Local/Temp/drang_fs_demo/notes.txt"
say(dirname($f))   # C:\Users\anafa\AppData\Local\Temp\drang_fs_demo
say(basename($f))  # notes.txt
say(ext($f))       # .txt
say(stem($f))      # notes
say(slash($f))     # C:/Users/anafa/AppData/Local/Temp/drang_fs_demo/notes.txt
```

Note `dirname` returns the path with the platform separator; reach for `slash`
when you want stable forward-slash output (e.g. for logging or comparison).

### Freshness helpers for build scripts

These power the classic "rebuild only if stale" pattern.

- `mtime(p)` → modification time as a Unix-seconds int, or `Err` if missing.
- `newer(a, b)` → bool: is `a` strictly newer than `b`? Both must exist (a
  missing operand is an `Err`).
- `stale(target, sources)` → bool: does `target` need rebuilding? True if
  `target` is missing **or** older than any source. `sources` may be a single
  path or an array of paths. A *missing source* is a real `Err`.

```drang
$dir := join($ENV["TEMP"], "drang_fresh")
mkdir($dir)
$src := join($dir, "main.c")
$obj := join($dir, "main.o")
write_file($src, "int main(){}")

say(stale($obj, $src))   # true  — target missing, build it
```

After building the object and later editing the source, `stale` flips back to
true and `newer` agrees:

```drang
say(stale($obj, $src))    # true   — source edited after obj built
say(newer($src, $obj))    # true
```

```drang
$obj := "build/app.o"
$srcs := glob("src/**/*.c")
if stale($obj, $srcs) {
  say("rebuilding " ~ basename($obj))
  # ... run the compiler ...
}
```

**Timestamp granularity caveat:** `mtime` resolves to whole seconds, so two
files written in the same instant compare equal — `newer` returns `false` in
both directions for them. Freshness checks are reliable across a real time gap
(an actual edit between builds), not for files created back-to-back in one
script run.

---

## JSON

`from_json` parses a JSON document into drang values; `to_json` renders them back. Objects become drang's insertion-ordered maps (so key order round-trips), arrays become arrays, and numbers become `int` when integral or `float` otherwise.

```drang
$cfg := from_json("{\"name\": \"zmal\", \"tags\": [\"build\", \"test\"]}")
say($cfg.name)
say($cfg.tags |> len)
```

```
zmal
2
```

Build a value and serialize it. A second argument to `to_json` switches on indentation — an int is that many spaces; without it the output is compact:

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

Malformed input is a catchable error value, not an abort — recover it with `//` or inspect it with `is_err`:

```drang
say(is_err(from_json("{ broken")))
say(from_json("nope") // "fallback")
```

```
true
fallback
```

---

## CSV

`from_csv` parses RFC 4180 CSV into rows; `to_csv` renders rows back. Both are built
on a battle-tested parser, so the awkward parts are handled: fields containing
commas, quotes, or newlines, and the doubled-quote escape (`""`). **Cells are always
strings** — convert explicitly (`int($row.age)`); there is no type inference.

By default rows are arrays of strings. With `{header: true}` the first row names the
columns and every later row becomes a record keyed by those names:

```drang
from_csv("a,b\n1,2")                       # [["a", "b"], ["1", "2"]]
$rows := from_csv("name,age\nalice,30\nbob,25", {header: true})
say($rows[0].name)                         # alice
say(to_csv($rows))                         # name,age / alice,30 / bob,25 (header auto-written)
```

`to_csv` accepts either shape: an array of arrays writes plain rows; an array of
records writes a header (from the first record's keys) plus one row per record, with
values pulled *by key* (so a record's key order need not match). Scalars stringify
(`nil` is an empty cell); a non-scalar cell is an error.

Both are **strict by default**, to catch malformed data loudly — ragged rows (a
differing field count), duplicate header names, and records whose keys differ from
the header are errors. Pass `{lenient: true}` to relax all three (pad/truncate, keep
the last duplicate column, drop unknown keys).

Options (an optional trailing map):

| Option | Where | Meaning |
|--------|-------|---------|
| `sep` | both | field delimiter, one character (default `,`; e.g. `"\t"` for TSV) |
| `header` | both | read: first row is column names → records; write: include a header row (default true) |
| `lenient` | both | relax strictness (ragged rows, duplicate / divergent keys) |
| `comment` | read | skip lines whose first character is this |
| `trim` | read | drop leading whitespace in each field |
| `lazy_quotes` | read | tolerate stray quotes in malformed input |
| `crlf` | write | end lines with `\r\n` (strict RFC) instead of the default `\n` |

As with JSON, option misuse (a bad type, a multi-character or invalid `sep`, an
unknown option key) aborts; malformed CSV and unencodable rows are catchable `Err`
values:

```drang
say(is_err(from_csv("a,b\n1,2,3")))                          # true — ragged row (strict)
$rows := from_csv(read_file("data.csv"), {header: true}) // []   # [] on a read error
```

A leading UTF-8 BOM (which Excel writes) is stripped automatically. Two inherited
quirks worth knowing: a `\r\n` *inside* a quoted field reads back as `\n`, and blank
lines are skipped — so a row that is a single empty field won't survive a round trip.
And because one-liner `-n`/`-p` is line-based, it can't safely stream a CSV with
quoted newlines — run `from_csv` on the whole text instead.

---

## One-liner mode

`-n` and `-p` turn drang into a stream processor in the awk/perl/sed tradition:
the program runs once per input line. `-n` just loops; `-p` also prints the topic
variable after each line (the filter/sed mode). Short flags combine — `-ne`,
`-pe`, `-ane` — and a trailing `e` takes the program source as its argument, like
a plain `-e`.

```
drang -pe '$_ = upper($_)' < notes.txt               # filter: uppercase each line
drang -ne 'if matches($_, "ERROR") { say($_) }' log  # grep-like (matches(s, pat))
drang -ane '$_ = $f[0]' data.tsv                      # print the first column
```

Per-line variables (all in the `$` data namespace):

| Variable | Meaning |
|----------|---------|
| `$_`    | the current line, with its trailing newline (and `\r`) stripped |
| `$nr`   | the 1-based line number, counting across every input file |
| `$file` | the current input filename (`"<stdin>"` when reading stdin) |
| `$f`    | with `-a`, the line split on whitespace into a 0-indexed array |

Input comes from the files named after the program, or from stdin when none are
given; `-` in the file list also means stdin. The filenames are exposed as `$ARGV`.

`BEGIN { ... }` and `END { ... }` blocks run once, before and after the loop — for
setup, headers, accumulators, and totals. The per-line body runs in a persistent
scope, so a variable declared in `BEGIN` survives every line:

```
drang -ane 'BEGIN{ $sum := 0 } $sum = $sum + int($f[0]); END{ say($sum) }' nums.txt
```

(`BEGIN`/`END` are contextual keywords — recognized only as a statement-leading
`BEGIN {` / `END {` — so they stay ordinary identifiers everywhere else.)

Notes and limits (v1):

- Separate statements on one line with `;`. A block's closing `}` also ends a
  statement, so `BEGIN{ ... } stmt` needs no `;`, but `stmt; END{ ... }` does.
- Use `:=` / `=` in the per-line body, not `::=` — re-declaring a constant on every
  line is an error.
- `-p` ends each line with `\n` (CRLF input is normalized to LF, and a missing final
  newline is added).
- Runtime errors in stream mode report the message but not a source position.
- In-place file editing (`-i`) is not yet supported.

---

## Quick reference: builtins

Every builtin in drang, grouped by area. Signatures use `?` for an optional
argument and `...` for variadic. Builtins follow one error convention: a wrong
argument **count** aborts the program (an uncatchable Go error), while a wrong
argument **type** or a runtime failure becomes a first-class **Err** value you
can recover with `//` or propagate with `?`. "→ Err" below means the failure mode
is a catchable Err value.

The list is derived from the `builtins` map in `internal/eval/eval.go` and the
higher-order forms in `internal/eval/hof.go`. (`spawn` and `each_line` are
evaluator special forms, not map builtins, and are documented elsewhere; `pmap`,
`sort`, and the `*_by` forms are higher-order and appear under Collections.)

### Output & errors

| Builtin | Signature | Description |
|---|---|---|
| `say` | `say(x...)` | Print all arguments space-separated, then a newline; returns nil. |
| `warn` | `warn(x...)` | Like `say`, but to stderr — for diagnostics that shouldn't pollute stdout. |
| `fail` | `fail(msg?)` | Make an Err value with message `msg` (default `"failed"`) and code 1. |
| `is_err` | `is_err(x)` | True if `x` is an Err value. |
| `err_code` | `err_code(x)` | The Err's code; 0 for a non-Err (so it reads as an exit code). |
| `err_msg` | `err_msg(x)` | The Err's message; `""` for a non-Err. |
| `exit` | `exit(code?)` | End the program with `code` (default 0, clamped 0–255), unwinding past functions, loops, `?`, and `//`. |
| `die` | `die(x...)` | Print the message to stderr and exit with code 1 — the fatal-error convention for a tool. |

### Conversions

| Builtin | Signature | Description |
|---|---|---|
| `int` | `int(x)` | Convert int/float/string to an int; unparseable → Err. |

### Numeric

Minimal daily-driver math (not a math/trig kitchen sink). `abs`/`sum`/`min`/`max` preserve int vs float; `floor`/`ceil`/`round` return an int. A non-number operand is a catchable Err.

| Builtin | Signature | Description |
|---|---|---|
| `abs` | `abs(n)` | Numeric absolute value (the path builtin is `abspath`). |
| `sum` | `sum(arr)` / `sum(a, ...)` | Add numbers (array or variadic); empty → 0; overflow → Err. |
| `min` | `min(arr)` / `min(a, ...)` | Smallest value; empty → Err. |
| `max` | `max(arr)` / `max(a, ...)` | Largest value; empty → Err. |
| `floor` | `floor(n)` | Round down to an int; NaN/Inf/out-of-range → Err. |
| `ceil` | `ceil(n)` | Round up to an int. |
| `round` | `round(n)` | Round to the nearest int (half away from zero). |

### Strings

| Builtin | Signature | Description |
|---|---|---|
| `split` | `split(s, sep?)` | Split `s`; no `sep` splits on whitespace runs, `""` splits into runes. |
| `replace` | `replace(s, old, new)` | Replace every literal `old` with `new`. |
| `trim` | `trim(s, cutset?)` | Trim whitespace, or the given `cutset` characters, from both ends. |
| `upper` | `upper(s)` | Uppercase. |
| `lower` | `lower(s)` | Lowercase. |
| `starts_with` | `starts_with(s, prefix)` | True if `s` begins with `prefix`. |
| `ends_with` | `ends_with(s, suffix)` | True if `s` ends with `suffix`. |
| `format` | `format(tmpl, x...)` | Fill each `{}` in `tmpl` with the next arg (`{{`/`}}` are literal); arity-checked → Err. |
| `lines` | `lines(s)` | Split into lines (CRLF-normalized), dropping one trailing newline; `""` → `[]`. |
| `repeat` | `repeat(s, n)` | Concatenate `n` copies of `s`; negative or oversized `n` → Err. |
| `chars` | `chars(s)` | Array of single-rune strings. |
| `len` | `len(x)` | Rune count of a string (also entry count of an array/map/range). |
| `contains` | `contains(s, needle)` | Substring test for a string (also membership for an array). |

### JSON

| Builtin | Signature | Description |
|---|---|---|
| `from_json` | `from_json(s)` | Parse JSON into drang values (object→map, array→array, number→int/float); malformed input → Err. |
| `to_json` | `to_json(v, indent?)` | Render a value as JSON; `indent` (int spaces or whitespace string) pretty-prints, else compact. Non-encodable values → Err. |
| `from_csv` | `from_csv(s, opts?)` | Parse RFC 4180 CSV into rows (arrays, or records with `{header: true}`); strict by default. Malformed input → Err. |
| `to_csv` | `to_csv(rows, opts?)` | Render rows (arrays or records) as CSV; minimal quoting, `\n` lines (`{crlf: true}` for `\r\n`). Bad rows → Err. |

### Collections & higher-order

| Builtin | Signature | Description |
|---|---|---|
| `len` | `len(arr)` | Element count (also string runes, map entries, range length). |
| `push` | `push(arr, x...)` | Append values in place; returns the same array. |
| `pop` | `pop(arr)` | Remove and return the last element; empty → Err. |
| `take` | `take(arr, n)` | New array of the first `n` elements (clamped). |
| `drop` | `drop(arr, n)` | New array with the first `n` elements removed (clamped). |
| `uniq` | `uniq(arr)` | Distinct elements (structural ==), in order. |
| `contains` | `contains(arr, x)` | True if `x` is in `arr` by structural `==` (also string substring). |
| `map` | `map(arr, fn)` | Apply `fn` to each element → new array; fail-loud on first Err. |
| `filter` | `filter(arr, fn)` | Keep elements where `fn` is truthy. |
| `reject` | `reject(arr, fn)` | Drop elements where `fn` is truthy. |
| `each` | `each(arr, fn)` | Run `fn` for side effects; returns the original array (for `\|>`). |
| `find` | `find(arr, fn)` | First element where `fn` is truthy, else undef (composes with `//`). |
| `any` | `any(arr, fn)` | True if `fn` is truthy for any element (false over empty). |
| `all` | `all(arr, fn)` | True if `fn` is truthy for every element (true over empty). |
| `count` | `count(arr, fn)` | How many elements satisfy `fn`. |
| `reduce` | `reduce(arr, init, fn)` | Fold left with `fn(acc, el)` (or `fn(acc, el, i)`) starting at `init`. |
| `flat_map` | `flat_map(arr, fn)` | Map then flatten one level (array results spliced, scalars appended). |
| `pmap` | `pmap(arr, fn)` | Parallel `map` over a CPU-bounded worker pool; result in input order. |
| `sort` | `sort(arr, cmp?)` | New array sorted ascending (stable); optional comparator `fn(a,b)`→int. |
| `sort_by` | `sort_by(arr, keyFn)` | New array sorted by `keyFn(el)` (key computed once per element). |
| `min_by` | `min_by(arr, keyFn)` | Element with the smallest `keyFn(el)`; empty → undef. |
| `max_by` | `max_by(arr, keyFn)` | Element with the largest `keyFn(el)`; empty → undef. |

Callbacks take one parameter, or two to also receive the 0-based index
(`reduce` takes 2 or 3: `acc`, `el`, optional `i`).

### Maps

| Builtin | Signature | Description |
|---|---|---|
| `keys` | `keys(m)` | Fresh array of keys, in insertion order. |
| `values` | `values(m)` | Fresh array of values, in insertion order. |
| `pairs` | `pairs(m)` | Array of `[key, value]` arrays, in insertion order. |
| `has` | `has(m, key)` | True if `m` contains `key`. |
| `delete` | `delete(m, key)` | Remove `key` in place; returns the same map. |
| `len` | `len(m)` | Entry count. |

### Regex

Patterns use Go's RE2 syntax. A pattern argument may be a string (compiled and
cached) or a compiled regex value (a `qr/.../` literal or `re(...)`).

| Builtin | Signature | Description |
|---|---|---|
| `re` | `re(pattern)` | Compile a string pattern into a reusable regex value; bad pattern → Err. |
| `matches` | `matches(s, pattern)` | True if `pattern` matches anywhere in `s`. |
| `match` | `match(s, pattern)` | First match as `[full, group1, ...]`, or undef if no match. |
| `find_all` | `find_all(s, pattern)` | Array of every (full) match, in order. |
| `gsub` | `gsub(s, pattern, repl)` | Replace every match with `repl` (`$1`/`${name}` backrefs). |

### Filesystem & paths

Path helpers are pure string transforms (never an Err); stat guards always return
a bool; the rest signal real I/O failures as Err.

| Builtin | Signature | Description |
|---|---|---|
| `join` | `join(seg, ...)` | Join path segments (OS-native). Also `join(arr, sep?)` to render+join an array. |
| `dirname` | `dirname(p)` | Directory portion of a path. |
| `basename` | `basename(p)` | Final path element. |
| `ext` | `ext(p)` | Extension including the dot (`.txt`), or `""`. |
| `stem` | `stem(p)` | Basename without its extension. |
| `abspath` | `abspath(p)` | Absolute path against the CWD; failure → Err. (Numeric absolute value is `abs`.) |
| `slash` | `slash(p)` | Convert separators to forward slashes. |
| `is_abs` | `is_abs(p)` | True if `p` is an absolute path. |
| `clean` | `clean(p)` | Lexically simplify a path (resolve `.`/`..`). |
| `rel` | `rel(base, p)` | Relative path from `base` to `p`; uncomparable → Err. |
| `within` | `within(base, p)` | True if `p` is inside (or equal to) `base`. |
| `path_list_sep` | `path_list_sep()` | OS PATH-list separator (`;` Windows / `:` Unix). |
| `exists` | `exists(p)` | True if the path exists. |
| `is_dir` | `is_dir(p)` | True if the path exists and is a directory. |
| `glob` | `glob(pattern)` | Sorted matches (supports `**`); no match is `[]`, bad pattern → Err. |
| `read_dir` | `read_dir(p)` | List a dir as `[{name, path, is_dir}]` (sorted by name); missing → Err. |
| `mkdir` | `mkdir(p)` | Create the directory tree (like `mkdir -p`); returns `p`, failure → Err. |
| `mtime` | `mtime(p)` | Modification time as a Unix timestamp; missing → Err. |
| `newer` | `newer(a, b)` | True if `a` is newer than `b`; a missing path → Err. |
| `stale` | `stale(target, sources)` | True if `target` is missing or older than any source; missing source → Err. |
| `read_file` | `read_file(p)` | Read the whole file as a string; failure → Err. |
| `write_file` | `write_file(p, content)` | Write `content` to `p`; returns `p`, failure → Err. |
| `rename` | `rename(src, dst)` | Rename/move; returns `dst`, failure → Err. |
| `rm` | `rm(p)` | Remove a file or tree, recursively and idempotently; returns `p`. |
| `copy` | `copy(src, dst)` | Copy a file or directory tree; returns `dst`, failure → Err. |
| `size` | `size(p)` | File size in bytes; missing → Err. |

### Process & concurrency

Process builtins take command words (arrays splice, scalars stringify) and an
optional trailing options map `{cwd, env, stdin, timeout, arg0}` (`timeout` in ms;
`arg0` presents a different argv[0] than the launched executable).
No shell is involved; args are passed verbatim. Channels and tasks are shared
reference types; values are deep-copied on send and on `await`.

| Builtin | Signature | Description |
|---|---|---|
| `run` | `run(cmd, args..., opts?)` | Run with inherited stdio; true on success, non-zero exit → Err (code = exit). |
| `capture` | `capture(cmd, args..., opts?)` | Run and return trimmed stdout; failure → Err (stderr folded in). |
| `capture_all` | `capture_all(cmd, args..., opts?)` | Run and return `{out, err, code, ok}`; non-zero exit is data, not an Err (124 timeout / 127 can't-start). |
| `pipe` | `pipe([cmd,args..], ..., opts?)` | Stream a pipeline of `[cmd, args...]` stages; returns last stage's trimmed stdout. |
| `start` | `start(cmd, args..., opts?)` | Launch detached (no wait); returns a process handle, can't-start → Err (127). |
| `pid` | `pid(proc)` | PID of a started process. |
| `kill` | `kill(proc)` | Terminate a started process (and its tree); returns true. |
| `await` | `await(t)` | Block for a task's result or a process's exit status (deep-copied out). |
| `chan` | `chan(n?)` | Make a channel, unbuffered or with buffer size `n`. |
| `send` | `send(c, v)` | Send a copy of `v` (blocking); send on a closed channel → Err. |
| `recv` | `recv(c)` | Block for the next value; closed-and-drained yields undef. |
| `recv_ok` | `recv_ok(c)` | Like `recv` but returns `[value, ok]` (ok=false when closed). |
| `close` | `close(c)` | Close a channel (safe from any goroutine); returns nil. |
| `drain` | `drain(c)` | Collect all remaining values into an array, blocking until closed. |

### System

| Builtin | Signature | Description |
|---|---|---|
| `sys_gc` | `sys_gc(mode)` | Tune the GC (`off`/`lean`/`normal`/`relaxed`, or a GOGC int); returns the previous percent. |
| `cwd` | `cwd()` | Current working directory as a native path. |
| `env` | `env(name, default?)` | Process env var (case-insensitive on Windows); `default` or nil if unset. |
| `parse_args` | `parse_args(argv, value_opts?)` | Parse an argv array into a flat map: `--flag`→`true`, `--key=val`/`--key val` (if `key` is in `value_opts`)→string, positionals under `"_"`. |

---

## Not Yet — Known Gaps and Surprises

drang is a personal daily-driver under active construction, not a finished language. This section is the honest inventory of what is missing or behaves unexpectedly, so you don't waste time reaching for something that isn't there. Everything below was confirmed against the binary.

### Whole capability areas with no builtins

There is no HTTP, date/time, randomness, hashing, or text-encoding support, and only minimal math (`abs`/`sum`/`min`/`max`/`floor`/`ceil`/`round` — no `sqrt`/trig/etc.). The functions you might expect simply don't exist, and calling one is an `unknown function` error:

```
drang -e 'say(sqrt(9))'
# drang: unknown function sqrt

drang -e 'say(now())'
# drang: unknown function now

drang -e 'say(rand())'
# drang: unknown function rand

drang -e 'say(base64("x"))'
# drang: unknown function base64
```

The same goes for `fetch`/`http`, `sha256`/`hash`, `hex`, `uuid`, and the math family (`sin`, `cos`, `floor`, `ceil`, `abs` as a number op — note `abs` *does* exist but is the path-absolutize builtin, not a numeric absolute value). These are all planned as thin bindings over Go's stdlib, but none have landed.

### Missing operators

- **No integer-division operator.** `/` is always float division. Use `int()` to truncate:

  ```drang
  say(10 / 4)        # 2.5
  say(int(10 / 4))   # 2
  ```

- **No `**` exponent operator** — `2 ** 8` is a parse error (`unexpected STAR`).
- **No ternary** — `1 > 0 ? 1 : 2` does not parse. `if` is a statement, not an expression, so there is no inline conditional. Use `and`/`or` short-circuit value-returning logic (`$cond and $a or $b`) as the workaround.
- **No bitwise operators** — `&`, `|` (as bitwise), `<<`, `>>` all fail to parse (`&` lexes as `ILLEGAL`; `<<` is read as a heredoc start). `|` is the lambda delimiter, not bitwise-or.
- **No `++` / `--`** — `$x++` is a parse error. Use compound assignment: `$x += 1`.

### No types, modules, or coercion you might assume

- **Structs are designed but not implemented.** The `struct` keyword from DESIGN.md does not parse yet (`struct Foo { ... }` → parse error). Use maps as records in the meantime: `$s := {reqs: 0, by_ip: {}}`.
- **No module / import system.** `import "x"` and `use "x"` are both parse errors. Everything lives in one file; there is no way to split or load code.
- **No automatic stringy coercion.** Despite being on the roadmap, `"5" + 3` is currently an error, not `8`. Convert explicitly with `int()`:

  ```drang
  say("5" + 3)        # error: cannot use string and int with '+'
  say(int("5") + 3)   # 8
  ```

### Behaviors that may surprise you

- **`int` is 64-bit; `+`/`-`/`*` overflow is an error**, not a silent wrap (and there is no auto-promotion to float) — it fails loudly, like division by zero:

  ```drang
  say(9223372036854775807 + 1)
  # drang: integer overflow: 9223372036854775807 + 1
  ```

- **`format()` uses `{}` placeholders, not `%`-style verbs.** Each `{}` consumes one argument; passing more arguments than placeholders is an error, so a `printf`-style template fails:

  ```drang
  say(format("{} and {}", 1, 2))   # 1 and 2
  say(format("%d", 5))             # error: format: template has 0 "{}" placeholder(s) but got 1 argument(s)
  ```

  There is no `sprintf` (`unknown function`); `format` is the only string-formatting builtin.

### Also absent (from DESIGN.md, not yet built)

first-class builtin values (you must wrap a builtin in a lambda to pass it: `map($xs, |$f| f($f))`), `sh()` shell escape, `BEGIN`/`END` autoloop blocks, char ranges (`'a'..'z'`), and the cross-machine/distribution growth paths. These are tracked in DESIGN.md as deferred or planned, not shipped.
