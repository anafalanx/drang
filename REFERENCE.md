# drang — Formal Reference (v0.9)

A terse, complete specification of drang for tools and agents: grammar, semantics,
and every builtin. No tutorial prose — for a worked, example-driven guide see
[MANUAL.md](MANUAL.md).

Every signature and error mode here was verified against the `drang` binary. Where
the binary and the older design notes disagree, this reference follows the binary.

## Conventions

- **Types:** `int` `float` `string` `bool` `nil` `array` `map` `range` `function` `error` `any`.
- **Signatures:** `f(a: T, opt?: T, rest...) -> R` — `?` marks an optional argument, `...` a variadic tail.
- **Error convention:** a wrong argument *count* aborts the program (uncatchable). A wrong argument *type* or a runtime failure in a **builtin** returns a first-class `error` value, catchable with `//` (recover on Err-or-nil) or `?` (propagate); a type mismatch on an **operator** aborts. Each builtin is marked `err:` with its failure mode.
- **Sigils / namespaces:** `$name` is a variable, `.name` a user-defined function, a bare `name` a builtin or keyword — three disjoint namespaces.

### Surprises

The rules a first reader is most likely to get wrong (each is verified against the binary, not a bug):

- **Operators abort; builtins return an Err.** A type mismatch on an operator (`"5" + 1`), integer overflow, or divide-by-zero **aborts the program uncatchably** — `//` and `?` cannot recover it, because the failure is below the level where error values exist. A builtin handed a bad-type argument instead returns a catchable Err (`int("x")` is recoverable). A wrong argument *count* — to a builtin *or* a user function — also aborts. Rule of thumb: an operator failing is a *bug* (fix the types); a builtin failing is a *condition* (handle it with `//` / `?`).
- **There is no `nil` literal.** You cannot write `nil` in source. Nil arises only as a value — an absent map key, a `store_get` miss, `recv` on a drained channel. Test presence with `has`, not `== nil`.
- **String interpolation is opt-in.** `"..."` and `'...'` are literal; only `$"..."` interpolates, and it interpolates a bare `$name` only (no `${expr}`). Join strings with `~`, never `+` (`"a" + "b"` aborts — see the first rule).
- **`/` is always float division**, even on two ints (`10 / 4` is `2.5`); use `div()` for truncating integer division. A whole-valued float prints with no trailing `.0`, so `6 / 2` prints `3` although its type is `float`.
- **A user function is always `.name`.** Define with `fn .f(...)`, call as `.f(...)`; a bare `f` is a builtin/keyword, `$f` is a variable holding a value. The three never collide.

---

## Grammar

EBNF notation used below: `=` defines a rule; `|` alternation; `( )` grouping; `[ ]` optional (0 or 1); `{ }` repetition (0 or more); `"..."` literal terminal (source text); UPPERCASE names are lexer token classes (terminals); lowercase names are nonterminals. Whitespace between tokens is insignificant except where it disambiguates a quote operator (see Lexical grammar). `NEWLINE` is a synthesized terminator token, not a literal `\n`.

### Lexical grammar

The lexer emits tokens and, Go-style, **synthesizes a `NEWLINE` terminator** at a source newline only when (a) the previous token can end a statement, and (b) the innermost open bracket is `{` or none. Inside `(` or `[`, newlines are insignificant (long expressions and `|>` pipelines wrap freely). A `;` is always a terminator.

Tokens that can precede a synthesized `NEWLINE` (statement-ending): any literal (`INT FLOAT STRING RAWSTR ISTRING QW QR`), `IDENT`, `VAR`, `true`, `false`, `return`, `break`, `next`, and the closers `)` `}` `]` `?`.

```ebnf
(* trivia — discarded, never emitted as tokens *)
whitespace   = " " | "\t" | "\r" ;
comment      = "#" { any-byte-except-newline } ;        (* line comment; runs to newline/EOF *)

(* --- identifiers and the three sigils --- *)
letter       = "_" | "a".."z" | "A".."Z" ;
digit        = "0".."9" ;
ident-char   = letter | digit ;
IDENT        = letter { ident-char } ;                  (* bare word: builtins & keywords *)
VAR          = "$" ident ;                              (* $-sigil variable; Lit = name w/o '$' *)
dotname      = "." ident ;                              (* user-function reference; see prefix rules *)
```

The language has exactly **three name forms**: `$name` (VAR — variables/parameters, the only sigil'd form), a **bare** `IDENT` (builtins and, in statement/call position, contextual words), and a leading-dot `.name` (user-defined functions, kept as an `Ident` whose name literally begins with `.`, disjoint from bare builtins). A **keyword** is any `IDENT` matching: `fn return if else unless for in while until break next true false or and not`. A bare `IDENT` cannot be assigned to and is not an lvalue.

```ebnf
(* --- numeric literals --- *)
INT          = digit { digit } ;                        (* decimal only; no 0x/0o/0b, no underscores, no sign *)
FLOAT        = digit { digit } "." digit { digit } ;    (* requires a digit after '.'; no exponent; '.' with a
                                                            non-digit after is the DOT / DOTDOT operator instead *)

(* --- string / quote-like literals ---
   Emitted token class determines interpolation & escaping:
     STRING  = escaped, NO interpolation      "..."  qq DELIM  <<TAG  <<"TAG"
     RAWSTR  = literal, no escapes/interp      '...'  q DELIM   <<'TAG'
     ISTRING = escaped + interpolation        $"..." $qq DELIM  <<$TAG   (parser splices)
     QW      = whitespace-split word list      qw DELIM         (parser builds an array)
     QR      = compiled-regex pattern          qr DELIM [flags]                                *)

dq-string    = '"' { "\\" any-byte | any-byte-except-'"' } '"' ;      (* -> STRING *)
sq-string    = "'" { any-byte-except-quote } "'" ;                    (* -> RAWSTR; no escapes, spans newlines *)
i-dq-string  = "$" dq-string ;                                        (* -> ISTRING *)

quote-op     = ( "q" | "qq" | "qw" | "qr" ) delim-open ;             (* keyword must be IMMEDIATELY followed by a
                                                                        delimiter, no space, else it is a plain IDENT *)
i-quote-op   = "$" "qq" delim-open ;                                 (* only $qq interpolates; $q/$qw/$qr/$' rejected *)
delim-open   = "(" | "[" | "{" | "/" | "|" ;                        (* paired ([{  nest; /  |  run to next occurrence.
                                                                        Body is literal: no backslash-escaping of the
                                                                        delimiter. Close = ) ] } for paired, else same *)
regex-flags  = { "i" | "m" | "s" | "U" } ;                          (* baked into QR pattern as Go "(?flags)" prefix *)

(* --- heredocs ---
   Opener must be the LAST token on its line. Body = following lines up to a line
   equal to TAG. Each body line carries a trailing "\n"; an empty body is "". *)
heredoc      = "<<" [ "~" ] heredoc-tag ;                            (* leading ~ strips common leading indentation *)
heredoc-tag  = ident                    (* <<TAG   -> STRING  *)
             | '"' ident '"'            (* <<"TAG" -> STRING  *)
             | "'" ident "'"            (* <<'TAG' -> RAWSTR  *)
             | "$" ident ;              (* <<$TAG  -> ISTRING *)
```

**Escapes** (decoded in STRING and ISTRING bodies; RAWSTR is verbatim): `\n \t \r \\ \" \$` decode to their char (`\$` → literal `$`, suppressing interpolation). Any **other** `\X` is kept verbatim as `\X` (lenient: regex `\d`, paths `C:\dir`). **Interpolation** (ISTRING only): `$name` splices a variable; `${ expr }` splices any expression (brace-matched, string-aware). A lone `$` not before an identifier or `{` is a literal `$`.

```ebnf
(* --- operators & punctuation (maximal-munch) --- *)
":=" "::=" "="  "+=" "-=" "*=" "/=" "%=" "~=" "//="
"+" "-" "*" "/" "%" "~" "?" "!" "." ".." "," ":" ";"
"|>" "|" "//" "<=>" "==" "!=" "<" "<=" ">" ">=" "("")" "{""}" "[""]"
```
Note: `<<` (when a heredoc tag follows) is scanned as a heredoc, not two `<`. `//` is defined-or, never a comment. A stray `$` (not before `"`, an identifier, `qq`, or a heredoc tag) is `ILLEGAL`; so is `$'`, `$q…`, `$qw…`, `$qr…`.

### Syntactic grammar

The parser is a Pratt (precedence-climbing) expression parser over the token stream.

```ebnf
program      = { terminator } { statement { terminator } } EOF ;
terminator   = NEWLINE | ";" ;
block        = "{" program-body "}" ;                  (* introduces a scope *)
program-body = { terminator } { statement { terminator } } ;   (* until "}" *)
```

A **block-form** statement (`if`/`while`/`for`/`fn`/`BEGIN`/`END`) ends at its closing `}`, which also terminates the statement — a following statement on the same line needs no `;`/NEWLINE. A map literal or lambda body that merely ends in `}` still requires a separator.

```ebnf
statement =
      special-block                     (* BEGIN {…} / END {…}  — contextual, one-liner stream mode *)
    | use-stmt                          (* use "path"           — contextual: 'use' + string literal *)
    | example-stmt                      (* example EXPR …       — contextual: top level only *)
    | fn-decl
    | if-stmt
    | while-stmt
    | for-stmt
    | ( "break" | "next" ) [ postfix-modifier ]
    | simple-stmt [ postfix-modifier ] ;

simple-stmt =
      "return" [ expr ]                 (* bare return allowed before terminator / "}" / EOF *)
    | VAR ":="  expr                    (* mutable declaration *)
    | VAR "::=" expr                    (* constant declaration *)
    | expr [ assign-op expr ] ;         (* assignment (if lvalue) or bare expression statement *)

assign-op   = "=" | "+=" | "-=" | "*=" | "/=" | "%=" | "~=" | "//=" ;
(* the LHS of an assign-op must be an lvalue: VAR, index, or field access — else a parse error *)

postfix-modifier =
      "if"     expr                     (* stmt runs when expr is truthy       *)
    | "unless" expr                     (* stmt runs when expr is falsy        *)
    | "while"  expr                     (* stmt repeats while expr is truthy   *)
    | "until"  expr                     (* stmt repeats while expr is falsy    *)
    | "for"    expr ;                   (* stmt runs per element; loop var is $_ *)

fn-decl     = "fn" dotname "(" [ params ] ")" block ;   (* user code REQUIRES the leading dot; bare 'fn name'
                                                            is a parse error (reserved for builtins/prelude) *)
params      = param { "," param } [ "," ] ;             (* trailing comma tolerated *)
param       = VAR [ "=" expr ] ;                        (* defaulted params must not precede required ones *)

if-stmt     = "if" expr block [ [NEWLINE] "else" ( if-stmt | block ) ] ;   (* else-if chains; 'else' may follow
                                                                              '}' on the same line or one newline *)
while-stmt  = "while" expr block ;
for-stmt    = "for" VAR [ "," VAR ] "in" expr block ;   (* one var = element; two = index/key, value *)

special-block = ( "BEGIN" | "END" ) block ;
use-stmt      = "use" ( STRING | RAWSTR ) ;             (* the '$u := use("path")' captured form is an ordinary call *)
example-stmt  = "example" expr [ "==" expr | "fails" ] ;
```

**Expressions** — binding powers, lowest to highest. `//` is looser than `|>` (so `$x // $y |> f()` parses as `$x // ($y |> f())`). All binary operators are **left-associative** (verified: `10-3-2` → `(- (- 10 3) 2)`; `$a//$b//$c` → `(// (// $a $b) $c)`; `1<2<3` → `(< (< 1 2) 3)`; `1..2..3` → `(range (range 1 2) 3)`).

| Level | Operators | Node / meaning | Assoc |
|---|---|---|---|
| 1 (loosest) | `or` | logical OR (short-circuit) | left |
| 2 | `and` | logical AND (short-circuit) | left |
| 3 | `//` | defined-or (nil/error fallback) | left |
| 4 | `\|>` | pipeline | left |
| 5 | `==` `!=` `<=>` | equality / three-way compare | left |
| 6 | `<` `<=` `>` `>=` | relational | left |
| 7 | `..` | range literal | left |
| 8 | `+` `-` `~` | add, subtract, string concat | left |
| 9 | `*` `/` `%` | multiply, divide, modulo | left |
| 10 | *prefix* `-` `!` `not` | unary negate / logical not | — |
| 11 (tightest) | `(…)` call · `[…]` index · `.name` field · `?` | postfix | left |

`not` is a **prefix** operator at level 10, an exact alias of `!` (NOT a low-precedence Perl `not`): `not a == b` parses as `(== (! a) b)`.

```ebnf
expr        = prefix-expr { infix-op-or-postfix } ;    (* Pratt: climb while next op binds tighter than context *)

prefix-expr =
      INT | FLOAT | STRING | RAWSTR | ISTRING | QR      (* literals; ISTRING may expand to an interpolation node *)
    | QW                                                (* qw{…} -> array of string literals *)
    | "true" | "false"
    | VAR                                               (* $name *)
    | IDENT                                             (* bare builtin / word (callee or value) *)
    | dotname                                           (* .name — user-function reference (a value) *)
    | "(" expr ")"                                      (* grouping *)
    | array-lit
    | map-lit
    | lambda
    | ( "-" | "!" | "not" ) prefix-expr ;               (* unary; 'not' folds to '!' *)

infix-op-or-postfix =
      binary-op expr                                    (* + - * / % ~ == != < <= > >= <=> .. *)
    | "or"  expr | "and" expr                           (* Logical node *)
    | "//"  expr                                        (* DefOr node *)
    | "|>"  pipe-rhs                                    (* Pipe node; see below *)
    | "(" [ args ] ")"                                  (* call *)
    | "[" expr "]"                                      (* index *)
    | "." IDENT                                         (* field access *)
    | "?" ;                                             (* postfix error-propagate *)

args        = expr { "," expr } ;                       (* no trailing comma; calls REQUIRE parentheses *)

pipe-rhs    = call-expr | callable ;                    (* RHS of |> must resolve to a call or bare callable.
                                                            A bare callable (IDENT, field, VAR, index, lambda) is
                                                            wrapped in an arg-less call; lhs is prepended as arg 0.
                                                            'x |> f()?' binds ? to the pipe result: (x |> f())?.
                                                            Any other RHS (operator/literal/paren) is a parse error. *)

array-lit   = "[" [ expr { "," expr } [ "," ] ] "]" ;   (* newlines inside [] are insignificant *)
map-lit     = "{" [ mapentry { "," mapentry } [ "," ] ] "}" ;
mapentry    = expr ":" expr ;                           (* key may be any expr (bare word -> Ident, string, etc.) *)
range-lit   = expr ".." expr ;                          (* via the '..' infix operator *)

lambda      = "|" [ params ] "|" ( block | expr ) ;     (* |a,b| … ; a '{' after the params is a BLOCK body, never
                                                            a map — to return a map wrap it: |$x| ({k: $x}) *)
```

Assignability: only `VAR`, index (`x[i]`), and field (`x.name`) are valid assignment targets; anything else on the left of an `assign-op` is a parse error. `break`/`next` are a parse error outside a loop in the current function (loop nesting resets at `fn`/lambda boundaries).

## Semantics

Formal rules for drang 0.9. Every rule below was verified against the interpreter. Terminology: *aborts* = terminates the program with a source location, uncatchable by `//` or `?`; *catchable Err* = returns a first-class `error` value recoverable with `//` and propagable with `?`.

### Value model

Ten value tags: `nil`, `bool`, `int` (int64), `float` (float64), `string`, `error`, `array`, `map`, `range`, `function`, plus `regex` and the concurrency handles `chan`, `task`, `process` (from `chan()`, `spawn()`, `start()`). `type(x)` reports the tag name.

There is **no `nil` literal** (nor a `null`): a nil value only *arises* — from a map/array miss, a bare `return`, an unfilled slot — it cannot be written directly (`say(nil)` is `undefined: nil`). Likewise there is no array/map "spread" or `undefined`.

- **Immutable / value-copied**: `nil`, `bool`, `int`, `float`, `string`, `range`, `regex`, `error`. Strings are immutable (indexing reads only; `s[i] = …` is not assignment). Numbers are unboxed.
- **Mutable / reference-semantic**: `array`, `map`. A binding holds a reference; aliasing is visible (`$b := $a; push($b, 1)` mutates the object `$a` also names). `function` values are reference/identity values (closures capturing by reference; see Functions). `chan` is intentionally shared (its deep-copy returns itself); `task`/`process` are handles.
- **Freezing** (see Declarations): an `array`/`map` carries a `frozen` flag. Freezing is deep, idempotent, and cycle-safe, and *follows the object* — freezing a reference freezes the object every alias sees.
- **Equality** (`==` / `!=`): structural and deep for `array`, `map`, `range` (element-by-element, insertion-order for maps). Scalars compare by value. `function`, `chan`, `task`, `process` compare by **identity**. `int` and `float` are equal when numerically equal (`1 == 1.0` is true), and collide as map keys — `1` and `1.0` are one key (the first-stored key value is kept on overwrite). Cross-container-kind comparison is `false`, not an error.
- **Ordering** (`< <= > >= <=>`): numbers numerically, strings lexicographically (byte/UTF-8 order). `<=>` (spaceship) yields `-1`/`0`/`1` over numbers *and* strings. Comparing **incompatible types aborts** (e.g. `1 < "a"` → `cannot compare int and string`); it is not a catchable Err. There is no `cmp`.
- **Display** (`str(x)`, `say`): an `int` and a whole-valued `float` render identically — `3.0` prints as `3` — so a `float` result can look like an `int`; distinguish with `type(x)`. `nil` → `nil`, `true`/`false`, an `error` → `error: <msg>`, an `array` → `[a, b, c]`, a `map` → `{k: v}`.

### Truthiness

Falsy is exactly: `nil`, `false`, `0` (int), `0.0` (float), `""`, and an **empty** `array`/`map`/`range`. Everything else is truthy — non-empty containers, non-empty strings including `"0"`, functions, regexes, channel/task/process handles, and **error values**.

- An `error` is **truthy**: `if risky() { … }` takes the true branch even on failure. Test failure with `is_err(x)`, never with bare truthiness. Every higher-order builtin `is_err`-checks a callback's result *before* testing truthiness, for this reason.
- A reversed/empty range (`5..1`) is falsy; a non-empty range is truthy.

### Declarations & scope

Three mutually collision-immune namespaces, keyed by sigil at *every* occurrence:

| Sigil | Namespace | Bind / use |
|---|---|---|
| `$name` | data — variables and constants | `$x := 1`, `$x` |
| `.name` | user-defined functions | `fn .f()`, `.f()`, value `.f` |
| bare `name` | builtins / stdlib | `split`, `map`, `run` |

Because the spaces are separate, `.split` (yours) and `split` (builtin) coexist; adding a builtin can never break user code. A `$`-binding of a builtin's name shadows it (`$len := 99` makes `$len` the number).

Binding forms:

- **`$x := v`** — declares a **mutable** lexical binding in the current block scope.
- **`$x ::= v`** — declares a **frozen constant**. Deep-freeze: if `v` is a container, the container and everything reachable is frozen (see Value model). Freeze follows the object, so `$C ::= $existing` freezes the object `$existing` still names; bind a literal or a copy to keep the original mutable.
- **`$x = v`** — reassigns the **nearest** existing binding of `$x` (lexically). Assigning an undeclared name aborts.

Scope rules:

- Every `{ }` is a block scope. Top-level `:=` bindings are ordinary mutable bindings; the *code/constant image* is what is frozen and shared across goroutines, so top-level mutable state exists but is not safely shareable into parallel callbacks.
- Shadowing is allowed: an inner `:=` may shadow an outer binding (including a constant) in a nested scope.
- Each `for`-loop iteration gets a **fresh** child scope, so closures made in different iterations capture distinct bindings (they do not share one mutable slot).

Frozen-binding errors:

- Reassigning a constant aborts: `$k = 2` on `$k ::= 1` → `cannot assign to constant $k`.
- Redeclaring a constant in the same scope aborts: `$k ::= 2` after `$k ::= 1` → `cannot redeclare constant $k`.
- Mutating a frozen container splits by mechanism: **index/field assignment** (`$T["k"] = v`, `$e[0] = v`) is a statement and **aborts** (`cannot modify a frozen map`/`array`); **`push`/`pop`/`delete`** on a frozen container return a **catchable Err** (`is_err` true, recoverable with `//`).

### Evaluation

- **Operand order is left-to-right**: in `f(a) + f(b)` and every operator/argument list, subexpressions evaluate in source order.
- **Implicit return**: a function/lambda body's value is its **last statement's** value; a tail-position `if`/`else` (or any block) hands out its taken branch. `return` is available for early exit (with a postfix `return … if` form). Control-flow constructs (`if`/`while`/`for`) are statements, not expressions — they yield no value inline.
- **Integer overflow aborts.** `int64` `+`/`-`/`*` that overflows aborts (`integer overflow: …`), uncatchable even inside `//`. It does not wrap or auto-promote. Opt into float math with `float(...)`.
- **Division `/` always yields `float`** — `type(6/2)` is `float`, `6/4` is `1.5`; for two `int` operands too. There is no integer-division *operator*; use the `div(a, b)` builtin or wrap with `int(a/b)`. (A whole-valued `float` prints without a trailing `.0`, so `say(6/2)` shows `3` while its type is `float` — see Value model.)
- **`%` and `div` truncate toward zero**: the remainder's sign follows the dividend (`-7 % 3` → `-1`, `7 % -3` → `1`); `div(-7, 2)` → `-3`, returns `int`.
- **Operator type policy** (distinct from the builtin convention below): arithmetic operators do **not** coerce. `int op int` stays `int` for `+ - * %`; a mixed or non-numeric operand **aborts** (`"a" + "b"`, `[1] + 2`, `7.5 % 2` all abort uncatchably). `~` is the only string-concatenation operator. Division/modulo by zero **aborts** (`division by zero` / `modulo by zero`) — but `div(1, 0)` is a **catchable Err**.
- **Logical operators** `and`/`or` short-circuit and are **value-returning** (Lua/Python-style): `0 or 5` → `5`, `7 and 9` → `9`. `not`/`!` yield a `bool`. Precedence low→high: `or` < `and` < `//` < `|>` < comparisons < `~` < additive < multiplicative.

### Error model

An `error` (Err) is an ordinary value: a message plus an integer code. Nothing unwinds on its own.

- **`fail(msg)`** builds an Err with the given message and code **1** (default message `"failed"`; `fail` takes no code argument — non-1 codes come only from operations that carry one, chiefly subprocess exit status).
- **`is_err(x)` / `err_code(x)` / `err_msg(x)`** inspect. On a non-error, `err_code` → `0` and `err_msg` → `""`.
- **`expr //  fallback`** (defined-or) recovers on Err **or `nil` only**. Other falsy values (`0`, `""`, `false`, `[]`) are real results and pass through. RHS is evaluated eagerly.
- **`expr?`** (propagate) short-circuits an Err out to the **nearest enclosing function boundary**, where it becomes an ordinary Err value in the caller (it does not keep unwinding). A `?` that fires at **top level** aborts the program, exiting with the Err's code (clamped to 1..255) and printing `drang: <msg>` to stderr.
- **The builtin convention.** A wrong **argument count** to a builtin is a programmer error: a hard **abort** with a source location, uncatchable by `//`/`?` (e.g. `int(1, 2)` → `int expects 1 argument, got 2`). A wrong **argument type** or **runtime failure** in a builtin returns a **catchable Err** (e.g. `int([1,2])` → is_err true). A builtin panic is internally recovered into a catchable Err, so ordinary script input cannot crash the interpreter. *This "bad-type-is-catchable" rule is a property of builtins only — operator type mismatches abort (see Evaluation).*
- **`exit(n)`** unwinds **everything** uncatchably (not intercepted by `//`/`?`), setting the process exit code (`n<0` clamps to 1, `n>255` to 255; `exit(0)` is deliberate success). `die(msg)` similarly aborts.
- **Locked exit codes** (additive-only, mirrored by `err_code`): `0` success · `1` generic caught Err / `die` · `2` unknown-command dispatch · `124` timeout · `127` command could not start · `137` resource-cap breach / `kill()`.

### Functions

- Named: `fn .name($p, …) { … }`; lambda: `|$p, …| expr-or-block` (zero params `||`). Both are first-class `function` values; a named function's value is written `.name`, a builtin's value is its bare name (`map($xs, upper)`), both usable point-free.
- **Positional args only** — no named/keyword args, no variadic parameter (pass an array for a variable count).
- **Default parameters**: `$name = expr`, after all required params. Evaluated **at call time**, only when the argument is omitted, **left-to-right**, and may reference earlier params (`fn .f($a, $b = $a + 1) …`; `.f(10)` → `$b == 11`). No shared-mutable-default surprise. Wrong arg count is a catchable Err naming the accepted range (e.g. `.f expects 1 to 3 arguments, got 4`).
- **Closures**: both named functions and lambdas capture their defining scope **by reference** — a captured mutable `$var` mutated inside the closure is visible outside, and vice-versa.
- **Recursion depth guard**: exceeding depth **4000** returns a **catchable Err** (code 1, message `call depth exceeded 4000 (infinite recursion?)`), not an abort.
- **Higher-order callbacks** are arity-flex: a 1-param lambda gets the element; a 2-param lambda also gets the index; `reduce`'s callback is `(acc, el)` or `(acc, el, index)`.

### Concurrency

Safe *by subtraction*: frozen top-level constants + lexical-only scope + immutable strings + no shared mutable globals ⇒ lock-free parallelism.

- **`pmap(arr, fn)`** — parallel map over a bounded `NumCPU` worker pool. Same contract as `map`: array-first, arity-flex callback, results in **input order**, **fail-loud** (the first Err a callback produces becomes the whole result and stops further work). Each element is **deep-copied** to its worker, so mutating the element affects only that worker's private copy.
- **`spawn(fn, args…)` → `task`**; **`await(task)`** blocks for the result (deep-copied out; idempotent). `spawn` deep-copies args in (copy-on-send) over a *snapshot* of the captured env. A task's error (returned, `?`-propagated, or panicked) is captured and surfaced by `await`, so `await(t)?` propagates and `await(t) // x` recovers. `await` also accepts a `process` handle from `start`.
- **Channels**: `chan()` (unbuffered) / `chan(n)` (buffered); `send(ch, v)` blocks until received (copy-on-send; on a closed channel a **catchable Err**, never a crash); `recv(ch)` blocks, yielding `nil` once closed and drained; `recv_ok(ch)` → `[value, ok]`; `close(ch)` idempotent; `drain(ch)` collects all remaining values into an array, blocking until closed. A `send`/`recv` that could only deadlock (no counterparty, no other task) is a **catchable Err**, not an abort.
- **Freeze-for-safety rule**: a parallel callback must be **pure** — it may read frozen top-level constants and its own (copied) params, but must not mutate shared captured state. Passing a `::=` constant container into `pmap`/`spawn` is safe (deep-frozen). Mutating a *captured mutable `:=` container* from a parallel callback is documented-undefined; collect each callback's return value instead. The language offers no shared accumulator, so the canonical racy form is largely unwriteable.

### Modules

Any `.dr` file is a module; its top-level `fn .foo` functions and `$CONST ::=` constants are its **exports** (a mutable top-level `:=` var is rejected at import). One keyword, `use`; **whether the result is captured** chooses the mode:

- **Flat merge** — `use "./util"` (statement): merges the module's `.foo` into your `.`-space and `$CONST` into your `$`-space, as if pasted.
- **Isolated** — `$u := use("./util")` (captured call): binds the export **record**; reach exports via `$u.foo()` / `$u.CONST`. Injects nothing. This is the aliased-import form (no `as`).

Resolution & rules:

- Paths are **strings**; the `.dr` extension is optional. Relative paths resolve against **the importing file's directory**; for source-less entry points (`-e`, stdin, REPL, a `drang build` standalone) they resolve against the **current working directory**.
- **Load once** per process, cached by canonical path (Windows-case-folded), so a diamond loads a shared dependency exactly once. Only successful loads are cached; a failed load re-runs.
- **Cycles error** (`import cycle through …`), not loop.
- **Flat-merge is non-transitive**: a name a module itself merged is not re-exported.
- **Collisions error**: merging a name already bound in the current scope (or defining one a `use` already merged) aborts, never silently overwrites.
- **`exit`/`die` propagate** through loading, even through the captured `$u := use(...)` form (not downgraded to a catchable Err).
- A **failed captured** import is a **catchable Err** (`use("./x") // {}`); a **failed flat-merge** statement **aborts** with the import error.
- **Exports are deeply immutable** (record and every container within frozen), safe to share across the import cache. Every top-level `.foo` is exported — no module-private helpers yet.

## Builtins

Every builtin, grouped by area. Callback-taking forms note their arity; the `err:` tag gives each builtin’s failure mode under the error convention above.

### Output & errors

Shared conventions: `say`/`warn`/`die` join their arguments with a single space and (for `say`/`warn`) terminate with exactly one `\n`. `say`/`warn` return `nil`. An Err value carries a message (string) and an integer code; construct one with `fail`, inspect with `is_err`/`err_code`/`err_msg`. Per the drang error convention, wrong argument *count* aborts the program uncatchably; a wrong argument *type* or runtime failure yields a first-class Err value (recoverable via `//` or `?`) — except `exit`, whose non-int argument aborts (see entry).

| Builtin | Signature | Behavior | err |
|---|---|---|---|
| `say` | `say(x...) -> nil` | Print all args to **stdout**, space-separated, then one newline; `say()` prints an empty line. | never |
| `warn` | `warn(x...) -> nil` | Like `say` but writes to **stderr**; for diagnostics that must not pollute stdout. | never |
| `fail` | `fail(msg?: any) -> error` | Build an Err value with message `msg` (coerced to string; default `"failed"`) and code `1`. Extra args are ignored (does not abort; does not set code). | never |
| `is_err` | `is_err(x: any) -> bool` | `true` iff `x` is an Err value; `false` for every other type (incl. `nil`). | aborts on wrong arity (needs exactly 1) |
| `err_code` | `err_code(x: any) -> int` | The Err's integer code; `0` for a non-Err (reads as a success/exit code). | aborts on wrong arity (needs exactly 1) |
| `err_msg` | `err_msg(x: any) -> string` | The Err's message string; `""` for a non-Err. | aborts on wrong arity (needs exactly 1) |
| `exit` | `exit(code?: int) -> ⊥` | Terminate the process with `code` (default `0`), unwinding past functions, loops, `?`, and `//` (uncatchable). Code is clamped to `0..255`: `0`→0, `>255`→255, **negatives→1**. | non-int `code` **aborts** ("exit code must be an int"); aborts on wrong arity (0 or 1 arg) |
| `die` | `die(x...) -> ⊥` | Print the args to **stderr** (space-separated, one newline, **no** `drang:` prefix), then exit with code `1`; the fatal-error convention for a tool. | never (always exits 1) |

Notes:
- `fail(123)` → Err whose `err_msg` is the string `"123"` (message is string-coerced), code `1`. `fail` never sets a non-default code and never aborts.
- `exit` and `die` propagate out of module loads: a `use`d module calling either ends the whole program.
- In `drang test`/`dispatch`/task mode the process exit code follows: success→`0`, a returned/propagated Err→its code (clamped `1..255`), `exit(n)`/`die`→that code.

### Conversions

All five conversion builtins are **strictly unary**: any other argument count is a hard, uncatchable abort (`<name> expects 1 argument, got N`) that `//`/`?` cannot intercept. Where a conversion can fail on a value, it returns a catchable **Err** value (recoverable with `//` or `?`). Two distinct Err messages recur: `cannot parse "X" as <t>` (a `string` argument that is not valid syntax for the target) and `cannot convert <type> to <t>` (an argument whose type is not convertible at all).

| Builtin | Signature | Behavior | err |
|---|---|---|---|
| `int` | `int(x: int\|float\|string) -> int` | Identity on `int`; truncates a `float` toward zero (`int(2.7)==2`, `int(-2.7)==-2`); parses a `string` as a base-10 integer with surrounding whitespace trimmed and an optional leading `+`/`-`. | Unparseable/empty/hex/float-syntax/overflowing string -> `Err` (`cannot parse "X" as int`); `bool`, `nil`, `array`, `map`, etc. -> `Err` (`cannot convert <type> to int`). Aborts on wrong arity. |
| `str` | `str(x: any) -> string` | Renders **any** value as its display string: numbers (integral floats drop the fraction — `str(3.0)=="3"`), `bool`, `nil` (`"nil"`), collections (bracketed, `", "`-separated, e.g. `[1, 2, 3]`, `{a: 1}`, `1..3`), an `error` as `"error: <msg>"`, a `function` as `"<fn>"`. | **never** (total — accepts every type incl. function/channel; aborts only on wrong arity). |
| `float` | `float(x: int\|float\|string) -> float` | Identity on `float`; widens an `int`; parses a `string` as a decimal or scientific (`float("1e3")==1000`) literal, whitespace trimmed. | Unparseable string (incl. `"inf"`/`"nan"`) -> `Err` (`cannot parse "X" as float`); `bool`, `nil`, `array`, etc. -> `Err` (`cannot convert <type> to float`). Aborts on wrong arity. |
| `bool` | `bool(x: any) -> bool` | Coerces by truthiness: `nil`, `false`, `0`, `0.0`, `""`, and empty containers (`[]`, `{}`, empty range) -> `false`; everything else (incl. non-empty containers, any `function`, and any `error` value) -> `true`. | **never** (total; aborts only on wrong arity). |
| `type` | `type(x: any) -> string` | Returns the argument's type name as a `string`: one of `int`, `float`, `string`, `bool`, `nil`, `array`, `map`, `range`, `function`, `error` (also `regex` for a compiled pattern, plus the concurrency handle names). | **never** (total; aborts only on wrong arity). |

Notes:
- `int` does **not** coerce `bool` or accept float-formatted strings (`int("3.9")` is an Err, not `3`); go through `float` first if needed (`int(float("3.9"))`). It is the idiomatic truncating cast for the always-float `/` operator: `int(7 / 2) == 3`.
- Because bad *values* are catchable, `int(s) // 0` / `float(s) // 0.0` are the standard "parse with default" idioms; a wrong *arity* like `int(1, 2) // 0` still aborts.
- `str`, `bool`, and `type` are total functions — they only ever fail on wrong arity, never on the value.

### Numeric

Daily-driver math plus a trig/exp line; no bignum, complex, or matrix support. **Type preservation:** `abs`/`sum`/`min`/`max` preserve int vs float; `floor`/`ceil`/`round`/`div` always return `int`; `sqrt`/`log`/`exp`/`sin`/`cos`/`tan`/`asin`/`acos`/`atan`/`atan2`/`pi` always return `float`; `pow` returns `int` when both operands are `int` and `exp >= 0` (else `float`). **Shared failure convention:** a non-number operand returns a catchable Err; a wrong argument *count* aborts the program (`drang: <name> expects N arguments, got M`) and is uncatchable. Trig arguments/results are in **radians**.

`abs(n: int|float) -> int|float` — absolute value, preserving numeric type (the path builtin is `abs_path`). **err:** non-number -> Err; wrong arity aborts.
`sum(arr: array) -> int|float` / `sum(a: int|float, rest...) -> int|float` — adds an array or a variadic list of numbers; empty/zero-arg -> `0`; preserves int vs float. **err:** non-number element/arg or overflow -> Err; never aborts (0–N args accepted).
`min(arr: array) -> int|float` / `min(a: int|float, rest...) -> int|float` — smallest value from an array or variadic list; single scalar returns itself. **err:** empty array or zero args -> Err; non-number -> Err; never aborts (0–N args accepted).
`max(arr: array) -> int|float` / `max(a: int|float, rest...) -> int|float` — largest value from an array or variadic list; single scalar returns itself. **err:** empty array or zero args -> Err; non-number -> Err; never aborts (0–N args accepted).
`floor(n: int|float) -> int` — rounds down to an `int`. **err:** non-number, or NaN/Inf/out-of-int-range -> Err; wrong arity aborts.
`ceil(n: int|float) -> int` — rounds up to an `int`. **err:** non-number, or NaN/Inf/out-of-int-range -> Err; wrong arity aborts.
`round(n: int|float) -> int` — rounds to nearest `int`, half away from zero (`round(2.5)`→3, `round(-2.5)`→-3). **err:** non-number, or NaN/Inf/out-of-int-range -> Err; wrong arity aborts.
`sqrt(n: int|float) -> float` — square root. **err:** non-number or negative -> Err; wrong arity aborts.
`pow(base: int|float, exp: int|float) -> int|float` — `base**exp`; `int` when both args are `int` and `exp >= 0`, else `float`. **err:** non-number or int-result overflow -> Err; wrong arity aborts.
`log(x: int|float, base?: int|float) -> float` — natural log of `x`, or log base `base` when given (`log(x, 2)`, `log(x, 10)`). **err:** non-number, non-positive `x`, or bad `base` -> Err; wrong arity (not 1 or 2 args) aborts.
`exp(x: int|float) -> float` — `e**x` (`exp(1)` is e, `exp(0)` is 1). **err:** non-number -> Err; wrong arity aborts.
`div(a: int|float, b: int|float) -> int` — truncating integer division toward zero (matches `%` sign convention; `/` is always float division). **err:** non-number or divide-by-zero -> Err; wrong arity aborts.
`sin(r: int|float) -> float` — sine of `r` radians. **err:** non-number -> Err; wrong arity aborts.
`cos(r: int|float) -> float` — cosine of `r` radians. **err:** non-number -> Err; wrong arity aborts.
`tan(r: int|float) -> float` — tangent of `r` radians. **err:** non-number -> Err; wrong arity aborts.
`asin(x: int|float) -> float` — inverse sine (radians). **err:** non-number or `x` outside `[-1, 1]` -> Err; wrong arity aborts.
`acos(x: int|float) -> float` — inverse cosine (radians). **err:** non-number or `x` outside `[-1, 1]` -> Err; wrong arity aborts.
`atan(x: int|float) -> float` — inverse tangent (radians). **err:** non-number -> Err; wrong arity aborts.
`atan2(y: int|float, x: int|float) -> float` — angle in radians of point `(x, y)`, quadrant-correct (`atan2(1,1)` is π/4). **err:** non-number -> Err; wrong arity (not exactly 2) aborts.
`pi() -> float` — the constant π (`3.141592653589793`) as a zero-arg builtin; bind a constant with `$PI ::= pi()`. **err:** never; passing any argument aborts (`pi expects no arguments`).

### Strings

Error convention: a wrong ARGUMENT COUNT aborts the program (uncatchable; nonzero exit). A wrong argument TYPE or runtime failure returns a first-class `error` value, catchable with `//` or `?`. All indices below are **rune** (Unicode code point) offsets, not byte offsets. `len` and `contains` are polymorphic (also operate on collections); listed here for their string behavior.

`split(s: string, sep?: string) -> array` — splits `s`: no `sep` → split on runs of whitespace with leading/trailing whitespace stripped (`"  a  b  "` → `[a, b]`); `sep == ""` → split into single-rune strings; else split on literal `sep`. Empty input → `[]`. **err:** non-string `s` or `sep` → Err; aborts on arity ≠ 1..2.

`join(array: array, sep?: string) -> string` — renders each element (via its display form) and concatenates with `sep` (default `""`). Array-only; for path segments use `path_join`. `sep` is rendered like an element, so a non-string `sep` (e.g. int) is stringified rather than rejected. **err:** first arg not an array → Err (message points at `path_join`); aborts on arity ≠ 1..2.

`replace_first(s: string, needle: string|regex, repl: string) -> string` — returns `s` with the first occurrence of `needle` replaced by `repl`; a **string** `needle` is a LITERAL, a `qr//`/`re()` regex matches as a pattern (with `$1`/`${name}` backrefs in `repl`). **err:** non-string `s` → Err; aborts on arity ≠ 3.

`replace_all(s: string, needle: string|regex, repl: string) -> string` — as `replace_first` but replaces every occurrence/match. **err:** non-string `s` → Err; aborts on arity ≠ 3.

`trim(s: string, cutset?: string) -> string` — trims from both ends: whitespace by default, or any character contained in `cutset` (`trim("abcxabc","abc")` → `"x"`). **err:** non-string `s` → Err; aborts on arity ≠ 1..2.

`upper(s: string) -> string` — Unicode uppercase fold (`"héy"` → `"HÉY"`). **err:** non-string `s` → Err; aborts on arity ≠ 1.

`lower(s: string) -> string` — Unicode lowercase fold. **err:** non-string `s` → Err; aborts on arity ≠ 1.

`starts_with(s: string, prefix: string) -> bool` — true if `s` begins with `prefix`. **err:** non-string arg 1 or 2 → Err; aborts on arity ≠ 2.

`ends_with(s: string, suffix: string) -> bool` — true if `s` ends with `suffix`. **err:** non-string arg 1 or 2 → Err; aborts on arity ≠ 2.

`format(template: string, args...) -> string` — substitutes each `{}` / `{:spec}` placeholder with the next arg; `{{` and `}}` emit literal braces; specs follow a Python/Rust subset (width/align/fill/precision/sign/base/`%`, e.g. `{:.2f}`, `{:>8}`, `{:#x}`, `{:+d}`, `{:.1%}`). **err:** placeholder count ≠ arg count → Err; non-string `template` → Err; aborts if called with zero arguments.

`lines(s: string) -> array` — splits `s` into lines, normalizing CRLF to LF and dropping one trailing newline (`"a\nb\n"` → `[a, b]`); `""` → `[]`. Operates on a string, not a file (pair with `read_file`). **err:** non-string `s` → Err; aborts on arity ≠ 1.

`repeat(s: string, n: int) -> string` — concatenates `n` copies of `s`; `n == 0` → `""`. **err:** non-int `n` (incl. float) → Err; negative `n` → Err (`repeat: negative count`); oversized result → Err (`result too large`); aborts on arity ≠ 2.

`chars(s: string) -> array` — array of single-rune strings, one per Unicode code point (`"héy"` → `[h, é, y]`); `""` → `[]`. **err:** non-string `s` → Err; aborts on arity ≠ 1.

`find_index(s: string, needle: string) -> int` — rune index of the first occurrence of `needle` in `s`, or `-1` if absent; empty needle → `0`. **err:** non-string arg 1 or 2 → Err; aborts on arity ≠ 2.

`len(s: string) -> int` — rune count of `s` (`len("héy")` → 3). Polymorphic: also length of array/map/range. **err:** unsupported type → Err (`cannot take len of <type>`); aborts on arity ≠ 1.

`contains(s: string, needle: string) -> bool` — substring test. Polymorphic: `contains(arr, x)` is membership by structural `==`. **err:** string form with a non-string needle → Err (`contains on a string needs a string needle`); non-string/non-array first arg → Err; aborts on arity ≠ 2.

### Collections & higher-order

All builtins here are **array-first** (the collection is the first argument) so they compose under `|>` (`$xs |> f(args)` calls `f($xs, args)`). Callbacks are arity-flexible: a **1-param** lambda `|$x|` receives the element; a **2-param** lambda `|$x, $i|` also receives the 0-based index. The sole exception is `reduce`, whose folding function is `|$acc, $el|` or `|$acc, $el, $i|` (accumulator, element, optional index). Comparators (`sort`'s optional 2nd arg) are `|$a, $b| -> int` (negative / `0` / positive; the `<=>` operator computes this). Key functions (`sort_by`/`min_by`/`max_by`) are `|$el| -> key`. Wrong **argument count** aborts the program uncatchably; a wrong argument **type**, a bad value, or an Err returned by a callback yields a catchable Err value (recover with `//`, propagate with `?`). Callback combinators (`map`, `filter`, `reject`, `find`, `any`, `all`, `count`, `flat_map`, `pmap`) are **fail-loud**: the first Err a callback produces becomes the whole result.

| Signature | Behavior | err |
|---|---|---|
| `len(x: array) -> int` | Element count. Also accepts string (rune count), map (entry count), range (length). | non-collection type -> Err; aborts on wrong arity (≠1) |
| `push(arr: array, x: any, rest...: any) -> array` | Appends one or more values **in place**; returns the same array. | non-array 1st arg, or frozen (constant) array -> Err; aborts on arity (<2) |
| `pop(arr: array) -> any` | Removes and returns the last element (mutates in place). | empty array, non-array, or frozen array -> Err; aborts on arity (≠1) |
| `take(arr: array, n: int) -> array` | New array of the first `n` elements; `n` clamps to `0..len` (negative -> `[]`). Never mutates. | non-array, or non-int `n` -> Err; aborts on arity (≠2) |
| `drop(arr: array, n: int) -> array` | New array with the first `n` elements removed; `n` clamps to `0..len` (negative -> unchanged copy). Never mutates. | non-array, or non-int `n` -> Err; aborts on arity (≠2) |
| `uniq(arr: array) -> array` | New array of distinct elements by structural `==`, first-seen order. | non-array -> Err; aborts on arity (≠1) |
| `contains(arr: array, x: any) -> bool` | True if `x` is in `arr` by structural `==`. Also accepts a string 1st arg (substring test). | non-array/non-string 1st arg (e.g. a map) -> Err; aborts on arity (≠2) |
| `map(arr: array, fn: function) -> array` | Applies `fn` to each element -> new array. | non-array, non-function `fn`, or callback Err (fail-loud) -> Err; aborts on arity (≠2) |
| `filter(arr: array, fn: function) -> array` | Keeps elements where `fn` is truthy. | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `reject(arr: array, fn: function) -> array` | Drops elements where `fn` is truthy (inverse of `filter`). | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `each(arr: array, fn: function) -> array` | Runs `fn` for side effects over each element; returns the **original** array (for `|>` chaining). | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `find(arr: array, fn: function) -> any` | First element where `fn` is truthy, else `nil` (composes with `//`). | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `any(arr: array, fn: function) -> bool` | True if `fn` is truthy for at least one element; `false` over an empty array. | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `all(arr: array, fn: function) -> bool` | True if `fn` is truthy for every element; `true` over an empty array. | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `count(arr: array, fn: function) -> int` | Number of elements for which `fn` is truthy. | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `reduce(arr: array, init: any, fn: function) -> any` | Left fold: `fn(acc, el)` (or `fn(acc, el, i)`) starting from `init`. `init` is **required** — there is no 2-arg form. | non-array, non-function, or callback Err -> Err; aborts on arity (≠3) |
| `flat_map(arr: array, fn: function) -> array` | Maps then flattens **one** level: array results are spliced in, scalars appended. | non-array, non-function, or callback Err -> Err; aborts on arity (≠2) |
| `pmap(arr: array, fn: function) -> array` | Parallel `map` over a `NumCPU`-bounded worker pool; results in **input order**. Each element is deep-copied to its worker; callback must be pure (read frozen constants/params only, no shared mutation). Same callback contract as `map`. | non-array, non-function, or first callback Err (fail-loud, stops further work) -> Err; aborts on arity (≠2) |
| `sort(arr: array, cmp?: function) -> array` | New array in natural ascending order (numbers numerically, strings lexicographically), **stable**. Optional comparator `\|$a,$b\| -> int` for custom order. | non-array, non-function `cmp`, non-orderable/mixed-type elements, or comparator Err -> Err; aborts on arity (not 1 or 2) |
| `sort_by(arr: array, keyFn: function) -> array` | New array sorted ascending by `keyFn(el)` (each key computed **once**). | non-array, non-function, non-orderable keys, or keyFn Err -> Err; aborts on arity (≠2) |
| `min_by(arr: array, keyFn: function) -> any` | Element with the smallest `keyFn(el)`, or `nil` for an empty array. | non-array, non-function, or keyFn Err -> Err; aborts on arity (≠2) |
| `max_by(arr: array, keyFn: function) -> any` | Element with the largest `keyFn(el)`, or `nil` for an empty array. | non-array, non-function, or keyFn Err -> Err; aborts on arity (≠2) |

### Maps

Maps preserve **insertion order**; all iteration-producing builtins reflect it. Keys must be hashable scalars (`int`, `string`, `bool`, `float`, `nil`); using a container as a key is a catchable Err at map-index time (`has` treats an unhashable key as simply absent). Convention: a **wrong argument count aborts** the program (uncatchable, exit 1); a **wrong argument type** returns a first-class Err value (catchable with `//` or `?`).

`keys(m: map) -> array` — fresh array of the map's keys in insertion order. **err:** first arg not a map -> Err | aborts on wrong arity.

`values(m: map) -> array` — fresh array of the map's values in insertion order. **err:** first arg not a map -> Err | aborts on wrong arity.

`pairs(m: map) -> array` — fresh array of `[key, value]` two-element arrays in insertion order. **err:** first arg not a map -> Err | aborts on wrong arity.

`has(m: map, key: any) -> bool` — `true` if `m` contains `key`, else `false`; an unhashable (container) `key` is reported as `false`, not an error. **err:** first arg not a map -> Err | aborts on wrong arity.

`delete(m: map, key: any) -> map` — removes `key` from `m` in place and returns the same map; deleting an absent key is a no-op. **err:** first arg not a map -> Err | aborts on wrong arity.

`len(x: string|array|map|range) -> int` — entry count of a map (also rune count of a string, element count of an array, length of a range). **err:** arg is none of string/array/map/range -> Err | aborts on wrong arity.

### Regex

Engine: Go **RE2** (`https://github.com/google/re2/wiki/Syntax`). Matching is linear-time; the pattern syntax has **no backreferences** (`\1` inside a pattern) and **no lookaround**. A `qr//` value has type `regex`; it is immutable, compiled once, cached, and safe to share across `pmap` workers.

**Shared conventions.** For `matches`/`match`/`match_all` the pattern arg is a `string` OR a compiled `regex` — interchangeable (a `string` is compiled as a *pattern*). The `string` form needs the RE2 backslash that the `qr//` literal form does not (e.g. `"\d"` vs `qr/\d/`). `replace_first`/`replace_all` are the **exception**: a plain-`string` needle is a **literal**, and only a `qr//` (or `re(...)`) value matches as a pattern (Ruby-`gsub` convention). Wrong **arity** aborts the program (uncatchable). A wrong argument **type**, or a malformed **string** pattern, returns a first-class `Err` value (catchable with `//` or `?`). A malformed pattern in a `qr//` **literal** is a lex/parse error (aborts). `match`/`match_all` ignore capture groups in their result list (only full matches); capture groups are returned only by `match` (as the tail of its array). Named captures `(?P<name>…)` are returned **positionally** in `match`'s array (there is no `.field`/map accessor and no `match_map` builtin in this build); named-group access is available only replacement-side via `${name}`.

`qr/…/[flags]` — regex literal; body is taken **literally** (backslashes pass straight to RE2, do not double them). Flags after the closing delimiter: `i` (case-insensitive), `m` (multi-line `^`/`$`), `s` (dotall, `.` spans `\n`), `U` (ungreedy, swaps greedy/lazy). Flags are baked as Go inline flags and shown when printed (`qr/foo/i` → `qr/(?i)foo/`). Delimiters: `/`, `|` run to the next same char; paired `(…)`, `[…]`, `{…}` **nest**. **err:** unknown flag letter or malformed body → parse error (aborts).

`re(pattern: string|regex) -> regex` — compiles a `string` pattern into a reusable `regex`; a `regex` passes through unchanged. **err:** bad pattern → Err; wrong arity aborts.

`matches(s: string, pattern: string|regex) -> bool` — true if `pattern` matches anywhere in `s`. **err:** non-string `s`, non-string/regex `pattern`, or bad string pattern → Err; wrong arity aborts.

`match(s: string, pattern: string|regex) -> array | nil` — first match as `[full, group1, group2, …]`, or `nil` if no match. **err:** non-string `s`, bad pattern, or wrong type → Err; wrong arity aborts.

`match_all(s: string, pattern: string|regex) -> array` — every **full** match in order (capture groups omitted); no match → `[]`. **err:** non-string `s`, bad pattern, or wrong type → Err; wrong arity aborts.

`replace_first(s: string, needle: string|regex, repl: string) -> string` — replace the **first** occurrence; string `needle` is a literal, `qr//`/`re(...)` matches as a pattern. Regex-needle `repl` uses Go substitution: `$1`/`${name}` (name via RE2 `(?P<name>…)`); use a non-interpolating replacement literal (`"…"` or `q{…}`) so `${…}` reaches the builtin. No-match/needle-absent → `s` unchanged. **err:** non-string `s`/`repl`, or bad regex needle → Err; wrong arity aborts.

`replace_all(s: string, needle: string|regex, repl: string) -> string` — as `replace_first` but replaces **every** occurrence. **err:** non-string `s`/`repl`, or bad regex needle → Err; wrong arity aborts.

Bad patterns are catchable: `re("(")` and `matches("x","(")` yield an `Err` (`is_err(...)` → `true`; uncaught, prints e.g. `error: bad regex "(": error parsing regexp: missing closing ): `(``). A pattern backreference such as `re("(a)\1")` is invalid RE2 and yields an `Err`. Note: `nil` is a reserved literal, so passing `nil` as a pattern fails at name resolution before the builtin runs (aborts), not as an Err.

### JSON & CSV

Every JSON/CSV builtin's arity is fixed; calling with the wrong number of arguments **aborts** the program (uncatchable). Type/runtime failures are per-builtin, noted below. Recover any `Err` value with `//` or `?`, or inspect it with `is_err`. Round-trip notes: JSON objects ↔ insertion-ordered `map` (key order preserved); JSON numbers decode to `int` when integral, else `float`; CSV fields are **always** strings on read (no type inference — convert with `int(...)` yourself); a leading UTF-8 BOM is stripped on read; `\r\n` inside a quoted CSV field reads back as `\n`; blank lines are skipped on read (a lone empty field does not survive a round trip).

| Builtin | Signature | Behavior |
|---|---|---|
| `from_json` | `from_json(s: string) -> any` | Parses a JSON document into drang values: object→`map`, array→`array`, integral number→`int`, other number→`float`, plus `string`/`bool`/`nil`. |
| `to_json` | `to_json(v: any, indent?: int\|string) -> string` | Renders `v` as JSON; no `indent` → compact; `indent` as `int` = that many spaces per level; `indent` as `string` = that literal indent unit (spaces/tabs only). |
| `from_csv` | `from_csv(s: string, opts?: map) -> array` | Parses RFC 4180 CSV; default → `array` of `array`-of-`string` rows; with `{header: true}` the first row names columns and each later row becomes a `map` record; strict by default. |
| `to_csv` | `to_csv(rows: array, opts?: map) -> string` | Renders `rows` to CSV; an array of `array`s writes plain rows; an array of `map` records writes a header (from the first record's keys) then one row per record, values pulled by key; scalar cells stringify (`nil`→empty), minimal quoting, `\r\n` line endings by default. |

**Failure modes** (verified on `drang.exe`):

- `from_json` — **err:** malformed JSON → `Err`; non-`string` argument → `Err` (catchable); wrong arity → aborts.
- `to_json` — **err:** non-encodable value (e.g. a `function`) → `Err`; `indent` of a disallowed type (`bool`, `float`) → `Err`; but an out-of-range `int` indent (must be `0`–`80`) **aborts**, and a non-whitespace `string` indent **aborts**; wrong arity → aborts.
- `from_csv` — **err:** malformed CSV under strict mode → `Err` (ragged row = differing field count; duplicate header name; a record whose keys diverge from the header); but a non-`string` first argument **aborts**, and an unknown/ill-typed option key (bad `sep`, unknown key, wrong opt type) **aborts**; wrong arity → aborts.
- `to_csv` — **err:** a non-scalar cell (e.g. a nested `array`/`map`) → `Err`; a `rows` argument that is not an array of rows **aborts**; bad option **aborts**; wrong arity → aborts.

**`from_csv` options** (trailing `map`): `sep` (1-char delimiter, default `,`); `header` (bool, first row = column names → records); `lenient` (relax all three strict checks: pad/truncate ragged rows, keep last of duplicate columns, drop unknown record keys); `comment` (skip lines whose first char is this); `trim` (drop leading whitespace per field); `lazy_quotes` (tolerate stray quotes).

**`to_csv` options** (trailing `map`): `header` (bool, default `true`; write a header row for record input — `false` suppresses it); `sep` (1-char delimiter); `crlf` (bool, default `true` = `\r\n` per RFC 4180; `false` = `\n`); `sanitize` (bool, default `false`; when `true`, prefix a `'` on any cell beginning with `=`, `+`, `-`, `@`, tab, CR, or LF, neutralizing spreadsheet formula/CSV-injection cells — opt-in because it mutates data, e.g. `-5`→`'-5`); `lenient` (relax strict record-key checks).

### Filesystem & paths

Shared conventions:
- **Pure path transforms** (`path_join`, `dirname`, `basename`, `ext`, `stem`, `to_slash`, `is_abs`, `clean`) never touch disk and never fail on a well-typed argument; a non-string argument is a catchable Err.
- **Stat guards** (`is_within`, `exists`, `is_dir`) always return a `bool`, never an Err — a missing/unstattable/uncomparable path is simply `false`.
- **Fallible I/O ops** return the operated path/value on success and a catchable Err (`error`, code 1) on real failure.
- Paths are returned with the OS-native separator (`\` on Windows); use `to_slash` to normalize. `path_list_sep()` is `;` on Windows.
- Every builtin below **aborts the program (uncatchable) on wrong argument count**; a wrong argument **type** is a catchable Err. Note builtins are shadowed by like-named variables (e.g. binding `$newer` masks `newer`).

| Builtin | Signature | Behavior | err |
|---|---|---|---|
| `path_join` | `path_join(seg: string, rest...: string) -> string` | Join segments into one OS-native path via `filepath.Join` (cleans `.`/`..`, collapses separators); zero args → `""`. | non-string arg → Err; aborts on wrong arity (n/a — variadic) |
| `dirname` | `dirname(p: string) -> string` | Directory portion (`filepath.Dir`); e.g. `.../a/b.txt` → `.../a`. | non-string → Err; aborts on wrong arity |
| `basename` | `basename(p: string) -> string` | Final path element (`filepath.Base`). | non-string → Err; aborts on wrong arity |
| `ext` | `ext(p: string) -> string` | Extension including the dot (`.txt`), or `""` if none (`filepath.Ext`). | non-string → Err; aborts on wrong arity |
| `stem` | `stem(p: string) -> string` | Basename with its extension trimmed (`b.txt` → `b`). | non-string → Err; aborts on wrong arity |
| `abs_path` | `abs_path(p: string) -> string` | Resolve to an absolute path against the CWD (`filepath.Abs`). (Numeric absolute value is `abs`.) | resolution failure → Err; non-string → Err; aborts on wrong arity |
| `to_slash` | `to_slash(p: string) -> string` | Convert `\` separators to `/` (`filepath.ToSlash`). | non-string → Err; aborts on wrong arity |
| `is_abs` | `is_abs(p: string) -> bool` | True if `p` is absolute (`filepath.IsAbs`). | non-string → Err; aborts on wrong arity |
| `clean` | `clean(p: string) -> string` | Lexically simplify (resolve `.`/`..`, no disk access; `filepath.Clean`). | non-string → Err; aborts on wrong arity |
| `rel` | `rel(base: string, target: string) -> string` | Path from `base` to `target` (`filepath.Rel`). | uncomparable paths (e.g. different volumes) → Err; non-string → Err; aborts on wrong arity |
| `is_within` | `is_within(base: string, target: string) -> bool` | True if `target` is inside or equal to `base`; an escaping `../` path or an uncomparable pair → `false`. | never (always bool); non-string → Err; aborts on wrong arity |
| `path_list_sep` | `path_list_sep() -> string` | OS PATH-list separator (`;` Windows, `:` Unix). | never; aborts on wrong arity (takes 0 args) |
| `exists` | `exists(p: string) -> bool` | True if `os.Stat(p)` succeeds. | never (always bool); non-string → Err; aborts on wrong arity |
| `is_dir` | `is_dir(p: string) -> bool` | True if `p` exists and is a directory. | never (always bool); non-string → Err; aborts on wrong arity |
| `is_file` | `is_file(p: string) -> bool` | True if `p` exists and is a regular file (not a dir/device). | never (always bool); non-string → Err; aborts on wrong arity |
| `is_symlink` | `is_symlink(p: string) -> bool` | True if `p` itself is a symlink (uses `Lstat`, so it does not follow the link, unlike exists/is_dir/is_file). | never (always bool); non-string → Err; aborts on wrong arity |
| `glob` | `glob(pattern: string) -> array` | Sorted array of matching paths; supports `**` (spans directories); no match → `[]`. | malformed pattern → Err; non-string → Err; aborts on wrong arity |
| `read_dir` | `read_dir(p: string) -> array` | List a directory (one level) as `[{name, path, is_dir, is_symlink}]` records, sorted by name; `path` is the joined full path. | missing/unreadable dir → Err; non-string → Err; aborts on wrong arity |
| `walk` | `walk(dir: string) -> array` | Recursively list everything under `dir` (root excluded) as `[{name, path, is_dir, is_symlink, size, mtime}]` records, depth-first in lexical order. Symlinks are reported but not followed (no cycles); unreadable entries are skipped. | non-directory / unreadable root → Err; non-string → Err; aborts on wrong arity |
| `readlink` | `readlink(p: string) -> string` | The target a symlink points to (`os.Readlink`), without following it. | non-symlink / missing → Err; non-string → Err; aborts on wrong arity |
| `mkdir` | `mkdir(p: string) -> string` | Create the directory tree (like `mkdir -p`, `os.MkdirAll` 0755); returns `p`. | create failure → Err; non-string → Err; aborts on wrong arity |
| `mtime` | `mtime(p: string) -> float` | Modification time as float Unix seconds, sub-second precision (same unit as `now()`). | missing/unstattable → Err; non-string → Err; aborts on wrong arity |
| `newer` | `newer(a: string, b: string) -> bool` | True if `a`'s mtime is strictly after `b`'s. | either path missing → Err; non-string → Err; aborts on wrong arity |
| `stale` | `stale(target: string, sources: string \| array) -> bool` | True if `target` is missing or older than any source; `sources` is one path or an array of paths. | any listed source missing → Err; non-string target / non-string array element → Err; aborts on wrong arity |
| `read_file` | `read_file(p: string) -> string` | Read the whole file into a string. | unreadable/missing → Err; a file exceeding the 1 GiB memory backstop → Err (not OOM); non-string → Err; aborts on wrong arity |
| `write_file` | `write_file(p: string, content: any, opts?: map) -> string` | Write `content` (rendered as `say` would — a string writes raw bytes) to `p` (0644); with `{append: true}` opens O_APPEND\|O_CREATE. Returns `p`. Only key `append` is allowed. | write failure → Err; opts not a map / unknown opt key → Err; non-string path → Err; aborts on wrong arity (2–3 args) |
| `tempfile` | `tempfile(prefix?: string) -> string` | Create a fresh uniquely-named empty file in the system temp dir (`<prefix>-*`, default prefix `drang`); returns its path (remove with `rm`). | create failure → Err; non-string prefix → Err; aborts on wrong arity (0–1 args) |
| `tempdir` | `tempdir(prefix?: string) -> string` | Create a fresh uniquely-named directory in the system temp dir; returns its path (remove with `rm`). | create failure → Err; non-string prefix → Err; aborts on wrong arity (0–1 args) |
| `rename` | `rename(src: string, dst: string) -> string` | Rename/move `src` to `dst` (`os.Rename`); returns `dst`. | rename failure → Err; non-string → Err; aborts on wrong arity |
| `rm` | `rm(p: string) -> string` | Remove a file or directory tree, recursively and idempotently (`os.RemoveAll` — a nonexistent path is not an error); returns `p`. (Named `rm` because `delete` removes map keys.) | removal failure → Err; non-string → Err; aborts on wrong arity |
| `copy` | `copy(src: string, dst: string) -> string` | Copy a file, or recursively copy a directory tree, preserving file modes; creates parent dirs of `dst`; returns `dst`. | copy failure (e.g. missing `src`) → Err; non-string → Err; aborts on wrong arity |
| `size` | `size(p: string) -> int` | File size in bytes (`os.Stat().Size()`). | missing/unstattable → Err; non-string → Err; aborts on wrong arity |

### Persistent store

A `store` is a durable JSON key-value map backed by a single file. `store(path?)` opens (or creates) one and returns a `store` handle; the operations below read and mutate it. Keys are strings; values are any **JSON-serializable** drang value (scalar, array, or map) — a value carrying a `channel`/`task`/`process`/`function`/`regex` is rejected with a catchable Err, exactly as `to_json` would reject it. `int` round-trips as an exact 64-bit integer.

Durability & concurrency:
- **Atomic snapshot per write.** Every mutation rewrites the whole file (temp + fsync + atomic rename), keeping the previous copy as `<path>.bak`; the file is never observed torn.
- **One writer.** A store holds a process-exclusive advisory lock on `<path>.lock` for its lifetime; a second process opening the same store gets a catchable `store busy` Err. The data file itself is never locked, so other tools can read it.
- The handle is a **shared reference** (like a channel): `DeepCopy` returns itself and a mutex guards all access, so it is safe to hand to `spawn`/`pmap` workers (access is serialized, not raced). Mutating one store from parallel workers is well-defined but serialized — not a parallelism win.
- Opening the same absolute path twice in one process returns the **same** handle.

Location: `store()` with no path defaults to `.drang/<script>.store` in the running script's directory — a predictable, environment-variable-free location that travels with the script. `-e`/stdin have no script file, so they must pass an explicit path. `store("path")` resolves like every other file builtin (relative to the working directory).

`store`, `store_update`, and `with_store` are **evaluator special forms** (they need the running environment or take a lambda); the rest are ordinary builtins. Error mode as elsewhere: wrong **argument count** aborts; a wrong **type**, a busy lock, a non-serializable value, an I/O failure, or exceeding the 64 MiB size cap is a catchable Err (code 1).

| Builtin | Signature | Behavior | err |
|---|---|---|---|
| `store` | `store(path?: string) -> store` | Open/create a store at `path`, or at `.drang/<script>.store` when omitted. Idempotent per absolute path within a process. | busy lock / unreadable-or-corrupt file (no valid `.bak`) / no script for the default path / non-string path → Err; aborts on wrong arity (0–1) |
| `store_get` | `store_get(s: store, key: string, default?: any) -> any` | Value for `key`; the `default` (or `nil`) when absent. Returns an isolated copy. | non-store / non-string key → Err; aborts on wrong arity (2–3) |
| `store_set` | `store_set(s: store, key: string, value: any) -> true` | Store an isolated copy of `value`, durably. | non-serializable value / non-store / non-string key / I/O / size-cap → Err; aborts on wrong arity |
| `store_has` | `store_has(s: store, key: string) -> bool` | Whether `key` is present. | non-store / non-string key → Err; aborts on wrong arity |
| `store_delete` | `store_delete(s: store, key: string) -> true` | Remove `key` (idempotent). | non-store / non-string key / I/O → Err; aborts on wrong arity |
| `store_keys` | `store_keys(s: store) -> array` | Keys in insertion order. | non-store → Err; aborts on wrong arity |
| `store_all` | `store_all(s: store) -> map` | The whole store as an isolated map copy (for iteration/inspection). | non-store → Err; aborts on wrong arity |
| `store_clear` | `store_clear(s: store) -> true` | Remove every key. | non-store / I/O → Err; aborts on wrong arity |
| `store_update` | `store_update(s: store, key: string, default: any, fn: function) -> any` | **Atomic read-modify-write:** call `fn(current)`, or `fn(default)` if `key` is absent, under the store lock, then store and return the result. Argument order mirrors `reduce`. `fn` must be a pure transform and must not touch this store (it would deadlock). A `?`/`fail` inside `fn` leaves the store unchanged and surfaces the Err. | non-store / non-string key / non-function / non-serializable result / I/O → Err; aborts on wrong arity (4) |
| `with_store` | `with_store(s: store, fn: function) -> any` | **All-or-nothing batch:** run `fn(s)`; mutations inside commit together in one atomic write on success, or roll the store back to its pre-batch state if `fn` returns/propagates an Err (or exits). Returns `fn`'s value. Not for concurrent batching of one store. | non-store / non-function / commit I/O → Err; aborts on wrong arity |
| `store_path` | `store_path(s: store) -> string` | The backing file's absolute path. | non-store → Err; aborts on wrong arity |
| `store_close` | `store_close(s: store) -> nil` | Release the lock and forget the handle (also released on process exit). | non-store → Err; aborts on wrong arity |

### Process & concurrency

External commands go through Go `os/exec` directly — **no shell**, args passed verbatim (no word-splitting, no glob expansion). Handle value types: `process` (from `start`), `task` (from `spawn`), `channel` (from `chan`). Array arguments in an argv list are **flattened one level** (`run("git", ["log","--oneline"])`). A trailing map literal is the options map (see the option table). `spawn`, `stream_lines`, and `dispatch` are **evaluator special forms**, not ordinary builtins (they take a function/lambda and are handled before normal builtin dispatch).

Error-mode convention (as elsewhere): wrong **argument count** aborts the program (uncatchable). A wrong argument **type** or a runtime failure is a first-class Err value (catchable with `//` or `?`) — with two documented exceptions where a structurally-invalid argument aborts instead: passing a `regex` where a command/arg string is expected, and a non-string for a string-valued option (`cwd`/`stdin`/`arg0`), both abort. Note a non-error scalar arg is stringified (`run(123)` execs `"123"` → 127 Err, not an abort).

#### Synchronous exec

`run(cmd: string, args...: any, opts?: map) -> bool` — runs the command with the child's stdin/stdout/stderr wired straight to drang's; returns `true` on success. **err:** non-zero exit → Err (code = child exit); can't-start → Err 127; timeout → Err 124; killed/limit → Err 137. Aborts on 0 args.

`capture(cmd: string, args...: any, opts?: map) -> string` — buffers the child's stdout and returns it **trimmed**. **err:** failure → Err with the child's stderr folded into the message (code = exit / 124 / 127 / 137); output past 256 MiB → catchable Err 137 (not an OOM). Aborts on 0 args.

`capture_all(cmd: string, args...: any, opts?: map) -> map` — runs and returns `{out, err, code, ok}`; a **non-zero exit is data, not an Err** (`ok=false`, `code=`child exit). **err:** timeout → still returns the map? no — timeout → `code` 124 / can't-start → `code` 127 as data in the map; the map is always returned (never an Err on ordinary run). Aborts on 0 args.

`pipe(stage: array, stages...: array, opts?: map) -> string` — wires each `[cmd, args...]` stage's stdout to the next stage's stdin through real OS pipes (streamed); returns the **last stage's trimmed stdout**. Bash pipeline semantics: 127 if any stage can't start, 124 on timeout, else the **last** stage's exit code. **err:** per those codes → Err. Aborts on 0 args.

`stream_lines(cmd: string, args...: any, opts?: map, cb: fn(line)) -> bool` — *special form*; invokes `cb` once per output line (newline stripped), streaming. Returns `true` on success. Callback arity: `|$line|`. **err:** non-zero exit / 124 timeout → Err. Aborts on wrong arity.

#### Detached process handle

`start(cmd: string, args...: any, opts?: map) -> process` — launches a child **without waiting** (async), returns a `process` handle. Rejects `{timeout}` (a detached process runs unbounded) and `{supervise}` requires this form. **err:** can't-start → Err 127; `{timeout}` present → catchable Err. Aborts on 0 args.

`pid(p: process) -> int` — OS process id of a started child. **err:** aborts on wrong arity.

`kill(p: process) -> bool` — terminates the process **and its whole tree** (kernel-enforced job kill); returns `true`. A pending `await` on it then yields Err 137. Idempotent on an already-exited child. **err:** aborts on wrong arity (expects exactly 1 process).

`status(p: process) -> map` — polls **without blocking**; always returns the same four keys `{running, ok, code, pid}`. While alive: `running=true`, `ok=false`, `code=-1` (sentinel); after exit: `ok`/`code` carry the outcome. `pid` is present throughout. **err:** aborts on wrong arity.

`send_stdin(p: process, s: string) -> bool` — pushes `s` to a child launched with `{stdin_pipe: true}`; returns `true`. **err:** returns Err if the child has no stdin pipe / already closed; aborts on wrong arity.

`close_stdin(p: process) -> bool` — signals end-of-input on the child's stdin pipe; returns `true`. Pairs with `{stdin_pipe: true}` + `send_stdin`. **err:** aborts on wrong arity.

#### Tasks

`spawn(fn: function, args...: any) -> task` — *special form*; runs a drang function on its own goroutine (args deep-copied in, copy-on-send), returns a `task` immediately. **err:** aborts on wrong arity (needs a function). An error raised inside the task (returned, `?`-propagated, or panicked) is captured and surfaced by `await`.

`await(h: task | process) -> any` — blocks for a task's result (deep-copied out) **or** a started process's exit status. For a process: `true` on clean exit, else an Err carrying the code (child exit / 124 / 127 / 137). **err:** for a process, non-zero/killed → Err; a spawned task's captured error is returned as an Err. Aborts on wrong arity.

#### Channels

`channel` is the one intentionally *shared* value type; values are copied on `send` and on `await`. A `send`/`recv` that could only ever deadlock (no counterparty, no other task running) is a **catchable Err**, not a process abort.

`chan(n?: int) -> channel` — makes an unbuffered channel, or a buffered one of capacity `n`. **err:** aborts on wrong arity.

`send(c: channel, v: any) -> nil` — sends a **copy** of `v`, blocking until received. **err:** send on a closed channel → Err; would-deadlock → Err. Aborts on wrong arity.

`recv(c: channel) -> any` — blocks for the next value; yields `undef` (nil) once the channel is **closed and drained**. **err:** would-deadlock → Err. Aborts on wrong arity.

`recv_ok(c: channel) -> array` — like `recv` but returns `[value, ok]`; `ok=false` when the channel is closed and drained. **err:** would-deadlock → Err. Aborts on wrong arity.

`close(c: channel) -> nil` — closes the channel; **idempotent** and safe from any goroutine. **err:** aborts on wrong arity.

`drain(c: channel) -> array` — collects every remaining value into an array, **blocking until the channel is closed**. **err:** aborts on wrong arity.

#### Task dispatch

`dispatch(tasks: map) -> (never returns)` — *special form*; treats the map `{name: function}` as a subcommand CLI, looks up the task named by `$ARGV[0]`, runs it, and **exits the process** with a resolved code. No arg / `list` / `-l` / `--list` prints task names (exit 0); unknown task prints the list to stderr and exits 2. A task fn takes **0 params** (ignores argv) or **1 param** (receives post-name argv as a string array); 2+ params is an error. Exit code: success → 0; returned/propagated Err → its code clamped to `1..255`; `exit(n)`/`die` → that code; unknown task → 2. **err:** aborts on wrong arity of a task fn (>1 param).

#### Exec options (trailing map on `run` / `capture` / `capture_all` / `pipe` / `stream_lines` / `start`)

| Option | Type | Effect |
|---|---|---|
| `cwd` | string | Child working directory. There is no global `cd`; this is the only way to change it. A non-existent dir → catchable Err. |
| `env_exact` | map | Sets the child's **exact** environment; nothing is inherited (even `PATH`/`SystemRoot` dropped unless added). Bare command names resolve against this env's `PATH`. |
| `env_add` | map | **Overlay**: starts from the inherited env and replaces/adds the given keys (Windows names matched case-insensitively). |
| `stdin` | string | Feeds this string as the child's stdin. Mutually exclusive with `stdin_file`. |
| `stdin_file` | string (path) | Pipes a file straight into stdin (no copy through drang; good for large inputs). Cannot combine with `stdin`. |
| `stdin_pipe` | bool | **`start`-only**: opens a live stdin pipe driven by `send_stdin`/`close_stdin`. Rejected on synchronous forms. |
| `merge_stderr` | bool | Folds the child's stderr into its stdout (like shell `2>&1`). |
| `arg0` | string | Presents a different argv[0] than the launched executable. Rejected for a `.bat`/`.cmd` target (launched via `cmd.exe`, which owns argv[0]) → catchable Err. |
| `timeout` | int (ms) | Wall-clock cap; `0` = no limit. On breach the whole process **tree** is killed → Err 124. **Rejected on `start`** (detached, unbounded) → catchable Err. |
| `supervise` | bool | **`start`-only**: ties a detached child's lifetime to drang's job (dies with drang, kernel-enforced, even on clean exit). **Rejected** on the synchronous forms (they always die-with-parent while waiting) → catchable Err. |
| `max_memory` | int (bytes) | Committed-memory cap, **per process**. Breach → child killed, Err 137. |
| `max_job_memory` | int (bytes) | Committed-memory cap for the **whole job** (child + every descendant). Breach → whole tree killed, Err 137. |
| `max_cpu` | int (ms) | User-CPU cap, **per process**. Breach → Err 137. |
| `max_job_cpu` | int (ms) | User-CPU cap for the **whole job**. Breach → Err 137. |
| `max_job_procs` | int (count) | Max concurrent processes allowed in the job. Breach → Err 137. |

Resource limits are Job-Object, kernel-enforced; a breach's Err message names the cap that tripped. All limit options are optional non-negative integers.

**Die-with-parent.** Every child runs inside a Windows Job Object with `KILL_ON_JOB_CLOSE`: if drang exits/crashes/is killed, the child and its whole tree are terminated too (a child cannot escape by forking). For the synchronous forms (`run`/`capture`/`capture_all`/`pipe`/`stream_lines`) this is always on. For a detached `start`, add `{supervise: true}` to extend the same tie; a plain `start` outlives drang.

#### Synthesized error codes

| Code | Meaning | When |
|---|---|---|
| **124** | timeout | `{timeout}` breached; the whole process tree is killed (matches GNU `timeout`). |
| **127** | cannot start | command not found / not executable (matches shell convention). |
| **137** | killed / limit breach | `kill(p)`, or a resource-limit (`max_*`) breach terminated the child/tree. |

These fold into the Err code, readable via `err_code(...)`.

### HTTP

A small HTTP client over Go's `net/http`. The whole surface is `http` plus the `http_get`/`http_post` sugar; PUT/PATCH/DELETE go through `http(method, url, opts?)`.

**Error model.** A *completed* exchange is data: any HTTP status — including 4xx/5xx — returns a response map, never an Err. Only a *failure to complete* the exchange (DNS failure, connection refused, timeout, TLS failure, malformed URL, or a body exceeding `max_body`) returns a catchable `Err`. A timeout carries `err_code` **124** (matching the subprocess convention); every other transport/opts/type failure carries `err_code` **1**. Thus `?` bubbles "couldn't reach the server", and `// {status: 0}` masks only a transport failure.

**Response map.** A successful call returns `{status: int, ok: bool (status 200–299), body: string, headers: map (lowercased keys; multi-value headers joined with `", "`), url: string (final URL after redirects)}`.

**`opts`** (trailing map; keys absent or wrong-typed are ignored, except as noted): `headers` (`{name: value}` string→string map; a non-map or non-string entry → Err), `body` (string; non-string → Err), `json` (any value; serialized and sent as `Content-Type: application/json`), `timeout` (int/float ms, `0` = unlimited; negative → Err), `redirects` (int cap, `0` = don't follow and return the 3xx; exceeding the cap → Err), `max_body` (int bytes, `0` = unlimited; counts *decompressed* bytes; exceeding → Err), `insecure` (truthy skips TLS verification). Supplying both `body` and `json` → Err.

**Defaults:** 30 s timeout, follow ≤10 redirects (dropping `Authorization` on a cross-host hop), TLS verification on, 32 MiB body cap, transparent gzip, `User-Agent: drang` (overridable via `headers`), one shared connection-pooled transport (safe under `pmap`).

| Builtin | Signature | Behavior |
|---|---|---|
| `http` | `http(method: string, url: string, opts?: map) -> map \| error` | Request with any HTTP `method` (case-insensitive; the primitive under `http_get`/`http_post`). **err:** aborts on wrong arity (not 2–3 args); non-string `method`/`url`, non-map `opts`, bad URL, `body`+`json` conflict, oversized body, or transport failure → Err (code 1); timeout → Err (code 124). Never on a 4xx/5xx status. |
| `http_get` | `http_get(url: string, opts?: map) -> map \| error` | GET `url`. **err:** aborts on wrong arity (not 1–2 args); non-string `url`, non-map `opts`, bad URL, or transport failure → Err (code 1); timeout → Err (code 124). Never on a 4xx/5xx status. |
| `http_post` | `http_post(url: string, body: string, opts?: map) -> map \| error` | POST a **string** `body` to `url` (merged into `opts` as `body`; use `http("POST", url, {json: x})` to send JSON). **err:** aborts on wrong arity (not 2–3 args); non-string `body`, non-string `url`, non-map `opts`, bad URL, or transport failure → Err (code 1); timeout → Err (code 124). Never on a 4xx/5xx status. |

### System

Zero-arg builtins (`cwd`, `os`, `arch`, `home`, `exe`, `drang_pid`) abort on any argument. Error-mode is **not** uniform in this category: `env` and `parse_args` **abort** on a wrong-type argument (uncatchable), whereas `is_terminal` and `drang_gc` return a **catchable Err** on wrong-type. Each entry states its mode.

| Builtin | Signature | Returns |
|---|---|---|
| `cwd` | `cwd() -> string` | Current working directory as a native (backslash) path. |
| `env` | `env(name: string, default?: any) -> string \| any \| nil` | Process env var via case-insensitive `os.LookupEnv`; unset → `default` if given, else `nil`. Distinct from the exact-case `$ENV` map. |
| `os` | `os() -> string` | Operating-system name; always `"windows"` (drang is Windows-only). |
| `arch` | `arch() -> string` | CPU architecture (`"amd64"`, `"arm64"`, …), from Go's `runtime.GOARCH`. |
| `home` | `home() -> string` | Current user's home directory (`os.UserHomeDir`). |
| `exe` | `exe() -> string` | Path of the running drang executable (`os.Executable`); may be an unresolved symlink. |
| `is_terminal` | `is_terminal(stream?: string) -> bool` | Whether `stream` is a console vs a pipe/file; `stream` ∈ `stdin` (default), `stdout`, `stderr`. Uses the real Windows console check (`GetConsoleMode` + MSYS2/Cygwin pty heuristic), so it reports `true` under mintty/Git Bash. |
| `parse_args` | `parse_args(argv: array, value_opts?: array) -> map` | Parse an argv (all elements strings) into a flat map (see rules below). |
| `drang_gc` | `drang_gc(mode: string \| int) -> int` | Set the GC target and return the PREVIOUS GOGC percent. |
| `drang_pid` | `drang_pid() -> int` | Process id of the running drang interpreter itself (`os.Getpid`). Distinct from `pid(proc)`, which reports a spawned child's id. |

**Per-builtin error modes:**

`cwd() -> string` — **err:** `os.Getwd()` failure → catchable Err; else aborts on any arg.
`env(name, default?) -> …` — **err:** non-string `name` → **aborts** (uncatchable); aborts on arity ≠ 1–2. Never Err.
`os() -> string` — **err:** aborts on any arg; never Err.
`arch() -> string` — **err:** aborts on any arg; never Err.
`home() -> string` — **err:** lookup failure → catchable Err; else aborts on any arg.
`exe() -> string` — **err:** lookup failure → catchable Err; else aborts on any arg.
`is_terminal(stream?) -> bool` — **err:** non-string arg OR unknown stream name → catchable Err; aborts on arity > 1.
`parse_args(argv, value_opts?) -> map` — **err:** non-array `argv`/`value_opts`, or any non-string element → **aborts** (uncatchable); aborts on arity ≠ 1–2. Never Err.
`drang_gc(mode) -> int` — **err:** unknown mode word OR non-int/non-string arg → catchable Err; aborts on arity ≠ 1.

**`parse_args` rules** (permissive; unknown options are not errors, duplicates keep the last value):
- Leading dashes are fully stripped, so both `--flag` and `-f` → `{flag: true}` / `{f: true}`.
- `--key=val` → `{key: "val"}` (string), regardless of `value_opts`.
- `--key val` → `{key: "val"}` only when `key` ∈ `value_opts`; otherwise `--key` → `{key: true}` and `val` becomes a positional.
- A `value_opts` option with no following token, or whose next token is the `--` terminator, gets `""`.
- Positionals (bare tokens, `""`, a lone `-`, and everything after a `--` terminator) collect into the array under the reserved `"_"` key; `"_"` is always present (empty array if none).
- `--_` never overwrites the positionals array — it is kept as a raw positional token.

**`drang_gc` modes** (return value is the prior GOGC percent, so a phase can save/restore): `off` → -1 (GC disabled), `lean` → 20, `normal` → 100, `relaxed` → 400; or an explicit int sets GOGC directly (negative disables GC). The process default is `400` (`relaxed`). After `drang_gc("off")` the reported previous percent is `-1`.

### Date & time

A point in time is epoch seconds as a `float` (Unix epoch, sub-second precision); do time math/comparison with ordinary numeric operators (`$t + 3600`, `$a < $b`). `format_time`/`parse_time`/`date_parts` take an optional trailing opts `map`; the only honored key is `utc: true` (work in UTC instead of local time). The opts argument must be a `map` when present — a non-map, or (harmlessly) an unknown key, yields a catchable `Err`, never an abort. Strftime `%`-codes: `%Y %y %m %d %e %H %I %M %S %p %A %a %B %b %j %w %z %Z %n %t %%`. `format_time` leaves an unknown code literal; `parse_time` returns an `Err` on a code/string it cannot parse.

- `now() -> float` — current time as epoch seconds. **err:** aborts on wrong arity (expects 0 args).
- `sleep(secs: float) -> nil` — pause `secs` seconds (fractional accepted); returns `nil`. **err:** non-numeric `secs` -> Err; aborts on wrong arity (expects 1).
- `format_time(epoch: float, fmt: string, opts?: map) -> string` — format `epoch` via strftime `%`-codes; local time, or UTC when `opts` is `{utc: true}`; unknown codes pass through literally. **err:** wrong-type `epoch`/`fmt`, or non-map/invalid `opts` -> Err; aborts on wrong arity (expects 2 or 3).
- `parse_time(str: string, fmt: string, opts?: map) -> float` — parse `str` per `fmt` `%`-codes back to epoch seconds; interprets `str` as local time, or UTC when `opts` is `{utc: true}`. **err:** unparseable `str`, wrong-type args, or non-map/invalid `opts` -> Err; aborts on wrong arity (expects 2 or 3).
- `date_parts(epoch: float, opts?: map) -> map` — decompose `epoch` into `{year, month, day, hour, minute, second, weekday, yearday}` (`weekday` 0–6, Sunday = 0; `yearday` 1-based; `month`/`day`/`hour`/`minute`/`second` natural values); local, or UTC when `opts` is `{utc: true}`. **err:** wrong-type `epoch` or non-map/invalid `opts` -> Err; aborts on wrong arity (expects 1 or 2).

### Hashing, encoding, randomness

Thin bindings over Go's standard library. Convention: a wrong argument **count** aborts the program (uncatchable); a wrong argument **type** or a runtime failure returns a first-class `Err` value (catchable with `//` or `?`). Digest and encode functions require a `string`; passing another type returns `Err` (`"<fn> expects a string, got <type>"`). All `from_*` decoders return `Err` on malformed input. `rand`/`rand_int`/`shuffle`/`sample` draw from a fast, auto-seeded (non-cryptographic) generator; `uuid` draws from the cryptographic generator.

| Signature | Behavior | err |
|---|---|---|
| `sha256(s: string) -> string` | Lowercase hex SHA-256 digest of `s` (64 chars). | non-string -> Err; wrong arity aborts |
| `sha1(s: string) -> string` | Lowercase hex SHA-1 digest of `s` (40 chars). | non-string -> Err; wrong arity aborts |
| `md5(s: string) -> string` | Lowercase hex MD5 digest of `s` (32 chars). | non-string -> Err; wrong arity aborts |
| `to_base64(s: string) -> string` | Standard (RFC 4648, padded) base64 encoding of `s`. | non-string -> Err; wrong arity aborts |
| `from_base64(s: string) -> string` | Decodes standard base64 `s` back to a string. | malformed base64 or non-string -> Err; wrong arity aborts |
| `to_hex(s: string) -> string` | Lowercase hex encoding of the bytes of `s`. | non-string -> Err; wrong arity aborts |
| `from_hex(s: string) -> string` | Decodes a hex string back to a string. | malformed hex (non-hex char / odd length) or non-string -> Err; wrong arity aborts |
| `to_url(s: string) -> string` | URL query-encodes `s`: space -> `+`, reserved chars percent-escaped. | non-string -> Err; wrong arity aborts |
| `from_url(s: string) -> string` | Inverse of `to_url`: decodes `+`-as-space and `%XX` escapes. | malformed escape (e.g. `%zz`) or non-string -> Err; wrong arity aborts |
| `rand() -> float` | Random float in `[0, 1)`. | takes no args; wrong arity aborts |
| `rand_int(n: int) -> int` | Random int in `[0, n)`. | `n <= 0` -> Err (`"n must be positive"`); non-int -> Err; wrong arity aborts |
| `rand_int(lo: int, hi: int) -> int` | Random int in `[lo, hi)`. | `lo >= hi` -> Err; non-int -> Err; wrong arity aborts |
| `shuffle(arr: array) -> array` | Returns a new randomly permuted array; input unchanged. | non-array -> Err; wrong arity aborts |
| `sample(arr: array) -> any` | Returns a random element of `arr`. | empty array -> Err; non-array -> Err; wrong arity aborts |
| `uuid() -> string` | Random v4 UUID string (cryptographic). | takes no args; wrong arity aborts |

`rand_int` accepts 1 or 2 arguments; any other count aborts.

## Command-line interface

```
drang [flags] program.dr [args...]
drang -e '<source>' [args...]
drang fmt [--fix] <files...>       # format
drang test <files...>              # run tests
drang build [-o out] ./cmd/...     # compile a static binary
```

- `-e <src>` — run source from the argument instead of a file.
- `--run` (default), `--ast` (print the AST), `--tokens` (print the token stream), `--version`/`-V`, `--help`/`-h`.
- Leading flags are consumed up to the first non-flag token (the program); everything after becomes script arguments, exposed as the array `$ARGV`. The process environment is the map `$ENV`. `parse_args($ARGV, [named...])` folds flags into a map (`--flag` -> `true`, `--key=val` or `--key val` -> string, positionals under `"_"`).

### One-liner / stream mode

`-n` and `-p` run the program once per input line (awk/perl style); `-p` also prints the topic variable after each line. Short flags combine (`-ne`, `-pe`, `-ane`); a trailing `e` takes the source as its argument. Input is the files after the program, or stdin (`-` also means stdin).

**`-i` — edit files in place.** With `-i`, the `-p` output for each input file is written back to that file (atomically) instead of to stdout; `-i<suffix>` (e.g. `-i.bak`) first saves the original to `<file><suffix>`. It requires `-p` and one or more real input files (not stdin), and clusters as `-pi` / `-pi.bak`. Note the file is rewritten line-by-line, so CRLF line endings are normalized to LF.

| Var | Meaning |
|---|---|
| `$_` | current line, trailing newline/`` stripped |
| `$nr` | 1-based line number across all inputs |
| `$file` | current input filename (`"<stdin>"` for stdin) |
| `$f` | with `-a`, the line split on whitespace (0-indexed array) |

`BEGIN { ... }` / `END { ... }` blocks run once before / after the loop; the per-line body runs in a persistent scope. Use `:=`/`=` in the body, not `::=`.

### Exit codes

`0` success | `1` uncaught error, `die`, or top-level `?` propagation | `124` subprocess timeout | `127` subprocess could not start | `137` subprocess killed or resource-limit breach | `exit(code)` sets an explicit code (clamped 0-255).
