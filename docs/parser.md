# Parser: AST and Recursive-Descent Parsing

The `parser` package turns a token stream (produced by the `lexer` package) into
a slice of `Statement` AST nodes. It is a hand-written recursive-descent parser
with no separate grammar file. Two files make up the package:

- `parser/ast.go` — the node type definitions.
- `parser/parser.go` — the parser itself plus the raw-string-to-`WordPart`
  helpers.

The parser operates on one *logical* line at a time. Multi-line constructs
(blocks spanning physical lines, `IF`/`FOR` bodies in parentheses) are joined
into a single logical line *before* tokenizing, with a `\x01` byte injected at
each original line boundary. That marker is central to several parser decisions
(see [The `\x01` block-line marker](#the-x01-block-line-marker)).

This document is the parser-internals reference. For how the lexer produces the
tokens consumed here, see [Lexer](lexer.md). For how the resulting AST is
evaluated and how `WordPart`s are turned into strings, see
[Expansion](expansion.md) and [Execution](execution.md).

## Entry points

```go
func Parse(tokens []lexer.Token) ([]Statement, error)
func ParseLine(line string) ([]Statement, error)
func ParseLineWithOpts(line string, delayedExpansion bool) ([]Statement, error)
```

- `Parse` (parser.go:12) is the lowest-level entry: it wraps an existing token
  slice in a `parser` and runs `parseStatements`. Note that a `parser` built
  this way has an **empty `raw` field**, which disables every verbatim-slicing
  feature (`ECHO` `RawText`, `CALL` `RawText`, `SET` trailing-whitespace
  capture). Prefer `ParseLineWithOpts` when those features matter.
- `ParseLine` (parser.go:18) is the convenience wrapper used by most callers; it
  delegates to `ParseLineWithOpts(line, false)`.
- `ParseLineWithOpts` (parser.go:23) tokenizes with
  `lexer.TokenizeWithOpts(line, delayedExpansion)` and, crucially, stores the
  original `line` in `parser.raw`. The `delayedExpansion` flag changes how the
  lexer treats `!...!` (whether `!VAR!` becomes a `BANG_VAR` token), so the
  parser's behavior for delayed-expansion references is decided one level down
  in the lexer.

All three return `([]Statement, error)`. In practice the parser is extremely
tolerant — it almost never returns an error and instead degrades to literal text
or `SimpleCommand` fallbacks, mirroring cmd.exe's "do something rather than
fail" behavior.

## The parser struct

```go
type parser struct {
	tokens     []lexer.Token
	pos        int
	raw        string // original line text (for tokens that need exact slicing)
	blockDepth int
}
```

(parser.go:29)

- `tokens` / `pos` — the token slice and the cursor into it. `peek()`
  (parser.go:39) returns a synthetic `EOF` token when `pos` is past the end;
  `consume()` (parser.go:46) returns the current token and advances.
- `raw` — the original, untokenized line. Used by `parseEcho`, `parseCall`, and
  `parseSet` to re-slice the original text by byte offset (`lexer.Token.Pos`),
  recovering spacing and verbatim content the token stream has normalized away.
- `blockDepth` — counts how many `( ... )` blocks enclose the current position.
  It is incremented/decremented in `parseBlock` (parser.go:267-268). Its sole
  purpose is to decide whether a `)` is a block closer or literal text: outside
  any block (`blockDepth == 0`) a `)` in an `ECHO` body is printed literally
  (see [`collectEchoWordGroups`](#echo-word-collection-and-the-)-quirk)).

## The descent chain

Parsing proceeds top-down through precedence levels, lowest-binding first:

```
parseStatements
  └─ parseChain        (&&, ||, &)        ← lowest precedence
       └─ parsePipe    (|)
            └─ parseOne (keyword dispatch / redirects / blocks)
```

### `parseStatements`

`parseStatements` (parser.go:52) is the top-level loop. It repeatedly:

1. Skips bare `AMPERSAND` separators between statements.
2. Calls `parseChain` and appends any non-nil result.
3. **Guarantees forward progress**: if `parseChain` consumed nothing
   (`p.pos == posBefore`), it force-consumes one token (parser.go:70-72). This
   prevents an infinite loop on a stray token the chain parser refuses to eat —
   most importantly an unmatched `)` at top level.

### `parseChain` and `parseIfChain`

`parseChain` (parser.go:97) parses a `parsePipe` left operand, then folds any
run of `&&` / `||` / `&` operators into left-associated `ChainStatement` nodes.
If the right operand parses to `nil` (e.g. trailing operator), the loop breaks
and the left side is returned as-is.

`parseIfChain` (parser.go:125) is identical **except** that it treats an
`AMPERSAND` token whose value is `\x01` as a terminator rather than a chain
operator (parser.go:133). This is what stops an unparenthesized `IF`/`ELSE`/`FOR`
body from swallowing statements that belong to the *next* physical line of a
block. See [The `\x01` block-line marker](#the-x01-block-line-marker).

### `parsePipe`

`parsePipe` (parser.go:152) parses a `parseOne` left operand. If the next token
is not `PIPE`, it returns that operand directly (no wrapper node). Otherwise it
collects all pipe-separated `parseOne` results into a single
`PipeStatement{Commands: [...]}`. `nil` results from `parseOne` are dropped
rather than added.

### `parseOne`

`parseOne` (parser.go:174) is the dispatcher. In order:

1. Returns `nil, nil` immediately if the next token is `EOF`, `AMPERSAND`,
   `AND`, or `OR` (an empty operand).
2. **Leading redirects** (parser.go:183): a run of `REDIRECTION` tokens at the
   start of a command (`> file echo text`) is collected, then `parseOne` recurses
   to parse the actual command, and `attachRedirects` prepends the collected
   redirects to it. See [Leading redirects](#leading-redirects).
3. `LPAREN` → `parseBlock`.
4. **`ECHO.`** prefix (parser.go:212): a token literally starting with `ECHO.`
   (e.g. `echo.`, `echo.hello`) becomes `EchoStatement{Newline: true}` plus any
   trailing redirects. Note this matches `HasPrefix`, so `echo.hello` is treated
   as the blank-line form here — the dot form's argument text is *not*
   preserved by this branch.
5. **`ECHO(`** prefix (parser.go:219): the safe-echo idiom. See
   [`ECHO(` and `ECHO.`](#echo-and-echo).
6. A keyword `switch` (parser.go:235) on the uppercased command word dispatching
   to `parseEcho`, `parseSet`, `parseIf`, `parseGoto`, `parseCall`, `parseFor`,
   `parseExit`, `SHIFT` (`ShiftStatement` inline), `parseSetlocal`, `ENDLOCAL`
   (`EndlocalStatement` inline).
7. Default → `parseSimpleCommand`.

There is no separate `LabelStatement` production in the parser — `LabelStatement`
(ast.go:195) exists in the AST but labels (`:foo`) are recognized upstream
(line classification before parsing), not by this dispatcher.

## Statement AST nodes

Every node implements the marker interface `Statement` (ast.go:5) via an empty
`statementNode()` method.

### `SimpleCommand`

```go
type SimpleCommand struct {
	Args      []WordPart
	Redirects []Redirect
}
```

(ast.go:11) The fallback for any command that is not a recognized keyword.
Produced by `parseSimpleCommand` (parser.go:893). Each `WORD` token becomes a
single `LiteralPart` (parser.go:924) — note it is *not* run through
`parseWordParts` here; `%%`/`!!` expansion of literal words is deferred to
runtime `ExpandWord`. `PERCENT_VAR`/`BANG_VAR` tokens become the appropriate
var part. Redirects can appear anywhere in the argument list and are pulled out
into `Redirects`. `Args` is therefore *only* the actual argv, never redirect
targets.

### `Redirect`

```go
type Redirect struct {
	Op   string // ">", ">>", "<", "2>", "2>&1", etc.
	File string
}
```

(ast.go:20) `Op` is the verbatim operator token from the lexer; `File` is the
following `WORD` if present (and `""` if the redirect operator is at end of
input, as for `2>&1` which has no file).

### `PipeStatement` / `ChainStatement` / `BlockStatement`

```go
type PipeStatement  struct { Commands []Statement }                 // ast.go:27
type ChainStatement struct { Left Statement; Op string; Right Statement } // ast.go:35
type BlockStatement struct { Stmts []Statement }                    // ast.go:45
```

`ChainStatement.Op` is one of `"&&"`, `"||"`, `"&"`. Chains are left-associated:
`a & b & c` parses as `((a & b) & c)`. `BlockStatement` holds the statements
parsed inside a `( ... )`.

### `IfStatement` and `Condition` variants

```go
type IfStatement struct {
	Not       bool
	Condition Condition
	Then      []Statement
	Else      []Statement // nil if no ELSE
}
```

(ast.go:53) `Condition` (ast.go:63) is a sealed interface with these
implementations:

- `StringCompare{Left, Op, Right []WordPart; CaseInsensitive bool}` (ast.go:65) —
  `Op` is always `"=="` in BAT; `CaseInsensitive` is set when `/I` was present.
- `NumericCompare{Left, Op, Right []WordPart}` (ast.go:75) — `Op` is one of
  `EQU NEQ LSS LEQ GTR GEQ`.
- `ExistCondition{Path []WordPart}` (ast.go:83).
- `DefinedCondition{Name []WordPart}` (ast.go:87) — the name is a `WordPart`
  slice, not a string, so dynamic names like
  `if defined table.!_lhs!.%%a` expand at evaluation time.
- `ErrorlevelCondition{N int}` (ast.go:91).

See [`parseIf`](#if-i-not-and-empty-operands) for how each is recognized.

### `GotoStatement`

```go
type GotoStatement struct {
	Label      string     // static label (if known)
	LabelParts []WordPart // dynamic label (expanded at execution time)
}
```

(ast.go:97) `parseGoto` (parser.go:725) always populates `LabelParts` (never
`Label`), because the target may contain variable references resolved only at
run time. The static `Label` field exists for callers/consumers that can resolve
a label statically.

### `CallStatement`

```go
type CallStatement struct {
	Args      [][]WordPart // each element is one argument; Args[0] is the script/label
	Redirects []Redirect
	RawText   string // verbatim args text (incl. command word)
}
```

(ast.go:106) See [`parseCall`](#call-rawtext-and-redirects). `RawText` lets the
executor re-expand a `CALL` of a builtin from the original text.

### `SetStatement`

```go
type SetStatement struct {
	Name       string
	Value      [][]WordPart // word groups, joined with " "
	Redirects  []Redirect   // for SET /P with < file
	HasEquals  bool
	Arithmetic bool         // SET /A
	Prompt     bool         // SET /P
}
```

(ast.go:116) See [`parseSet`](#set-a-p-and-raw-value-capture).

### `EchoStatement`

```go
type EchoStatement struct {
	Args      [][]WordPart
	Redirects []Redirect
	TurnOn    *bool  // nil = not a toggle; true = ECHO ON, false = ECHO OFF
	Newline   bool   // ECHO. prints a blank line
	RawText   string // verbatim text after "echo "
	HasRaw    bool   // true when RawText should be used instead of Args
}
```

(ast.go:129) See [`parseEcho`](#echo-toggles-rawtext-and-the-echo-echo-forms).

### `ForStatement`

```go
type ForKind int
const (
	ForInList ForKind = iota // FOR %%I IN (a b c) DO
	ForInFiles               // FOR %%I IN (*.txt) DO
	ForRange                 // FOR /L %%I IN (start,step,end) DO
	ForTokens                // FOR /F "tokens=..." %%I IN (...) DO
	ForDirs                  // FOR /D %%I IN (pattern) DO
	ForRecursive             // FOR /R [path] %%I IN (pattern) DO
)

type ForStatement struct {
	Variable string
	Kind     ForKind
	InList   []string
	Options  string
	RootPath string // for /R: root directory to walk ("" = current dir)
	Body     []Statement
}
```

(ast.go:142-160) See [`parseFor`](#for-l-f-d-r-and-the-options-string). Note
`ForInFiles` is a kind the parser **never assigns** — `parseFor` only produces
`ForInList`, `ForRange`, `ForTokens`, `ForDirs`, or `ForRecursive`. Distinguishing
a literal list from a glob is left to the executor inspecting `InList`.

### `ExitStatement`

```go
type ExitStatement struct {
	Code      int
	CodeParts []WordPart // dynamic code, e.g. EXIT /B !ERRORLEVEL!
	SubOnly   bool       // EXIT /B
}
```

(ast.go:166) See [`parseExit`](#exit-b-and-dynamic-codes).

### `ShiftStatement`, `SetlocalStatement`, `EndlocalStatement`, `LabelStatement`

```go
type ShiftStatement   struct{}                                       // ast.go:176
type SetlocalStatement struct {                                      // ast.go:182
	EnableDelayedExpansion  bool
	DisableDelayedExpansion bool
}
type EndlocalStatement struct{}                                      // ast.go:189
type LabelStatement    struct{ Name string }                         // ast.go:195
```

`SHIFT` and `ENDLOCAL` are parsed inline in `parseOne` (a bare `consume()` plus
the empty struct). `SETLOCAL` is handled by `parseSetlocal`.

## Per-keyword parsers

### `IF`: `/I`, `NOT`, and empty operands

`parseIf` (parser.go:539):

1. After consuming `IF`, it loops over leading `WORD` tokens collecting **both**
   `/I` (case-insensitive) and `NOT`, in any order and any number (parser.go:545).
   `NOT` order-independence is deliberate — cmd.exe accepts `IF NOT /I` and
   `IF /I NOT` equally.
2. `parseCondition` builds the `Condition`.
3. If `/I` was set and the condition is a `StringCompare`, its
   `CaseInsensitive` flag is set (parser.go:565-569). `/I` has no effect on the
   other condition types.
4. `parseIfBody` parses the THEN (and optional ELSE).

`parseCondition` (parser.go:584) recognizes, in order:

- `EXIST <path>` → `ExistCondition`; the path is `collectOneWordParts` (one
  whitespace-bounded word).
- `DEFINED <name>` → `DefinedCondition`; the name is also `collectOneWordParts`,
  so it may contain `!var!`/`%%a` parts.
- `ERRORLEVEL <n>` → `ErrorlevelCondition`; the number is the next token via
  `strconv.Atoi` (errors ignored → `0`).
- **`==` glued inside one token** (parser.go:615): `"val1"=="val2"` arrives as a
  single token; it is split at the first `==`. If the right side is empty
  (`"x"== "y"` with a space), the remaining word parts are collected
  (parser.go:621-623), so the empty-right-operand case still produces a usable
  comparison.
- Otherwise the left operand is `collectOneWordParts`, then:
  - a numeric operator (`numericOps`, parser.go:579) → `NumericCompare`.
  - `==` as a **separate token or `==`-prefixed token** (parser.go:642): the
    suffix after `==` becomes the right operand; if empty, `collectOneWordParts`
    supplies it.
  - **Fallback** (parser.go:653): collect up to an `==` (`collectUntilOp`),
    append to the left operand, and take whatever follows as the right operand.
    This handles `==` split awkwardly across tokens.

`collectOneWordParts` (parser.go:1012) refuses to merge a token that begins with
`==` into the operand (parser.go:1024) so the comparison operator is never eaten
into an operand.

```bat
if /i "%a%"=="YES" echo matched
if not exist "%file%" goto :missing
if errorlevel 1 echo failed
if %n% gtr 10 echo big
```

### `parseIfBody`: bodies that don't swallow following statements

`parseIfBody` (parser.go:659):

- If the THEN starts with `LPAREN`, it is a `parseBlock` and the block's
  `Stmts` become `Then`.
- Otherwise the THEN is a single `parseIfChain` (parser.go:671) — *not*
  `parseChain*. Using `parseIfChain` means the unparenthesized body extends to
  end of line through `&`/`&&`/`||` **but stops at the `\x01` marker**, so an
  `IF` on one block line does not absorb the next block line.
- An optional `ELSE` follows the same block-vs-`parseIfChain` rule
  (parser.go:681-697).

```bat
if "%x%"=="1" (
    echo one
) else (
    echo other
)

if "%x%"=="1" echo yes & echo still-part-of-then
```

### `FOR`: `/L` `/F` `/D` `/R` and the options string

`parseFor` (parser.go:768):

1. Reads an optional flag (parser.go:775):
   - `/L` → `ForRange`.
   - `/F` → `ForTokens`; **if** the next token is a `WORD` it becomes
     `Options` (e.g. `"tokens=1,2 delims=,"` arrives quoted as one token, since
     the lexer keeps quotes). No further parsing of the option string happens
     here — interpreting `tokens=`/`delims=`/`skip=`/`eol=`/`usebackq` is the
     executor's job.
   - `/D` → `ForDirs`.
   - `/R` → `ForRecursive`; if the next token is a `WORD` that does **not**
     start with `%` it becomes `RootPath` (parser.go:793). `%`-prefixed means we
     are already at the loop variable, so there is no explicit root.
2. The loop variable is the next `PERCENT_VAR`, with the surrounding `%` trimmed
   (parser.go:800-803). So `%%I` yields `Variable == "I"` and `%I` yields
   `"I"` too (interactive single-`%` form).
3. An optional literal `IN` is consumed.
4. The `( ... )` in-list is read token by token (parser.go:810-827). For each
   `WORD`/`PERCENT_VAR`/`BANG_VAR` token, the value is **split on commas**, each
   piece trimmed, and non-empty pieces appended to `InList`. The comma split is
   what makes `FOR /L %%I IN (1,1,5)` yield three items `1 1 5`. Other token
   kinds inside the parens are skipped.
5. An optional literal `DO` is consumed.
6. The DO body is a `parseBlock` (if it starts with `(`) or a single `parseOne`
   otherwise (parser.go:835-849).

```bat
for %%f in (*.txt) do echo %%f
for /l %%i in (1,1,5) do echo %%i
for /f "tokens=1,2 delims=:" %%a in (data.txt) do echo %%a %%b
for /d %%d in (*) do echo %%d
for /r C:\src %%f in (*.go) do echo %%f
```

Note the body is parsed with plain `parseOne`, not `parseIfChain`; the `\x01`
handling for FOR bodies relies on the body being a parenthesized block when it
spans lines.

### `SET`: `/A`, `/P`, and raw-value capture

`parseSet` (parser.go:384) is the most intricate parser because it must preserve
exact value text — including trailing whitespace and adjacent
`%`/`!` references — that the tokenizer would otherwise normalize.

1. Consume `SET`. With nothing after it → empty `SetStatement`.
2. Optional `/A` (`Arithmetic`) or `/P` (`Prompt`) flag (parser.go:393).
3. Consume the first value token and remember its byte position. Detect whether
   it was quoted (`SET "name=value"` / `SET /A "expr"`), then strip one layer of
   quotes via `stripQuotes` (parser.go:1301).
4. **Unquoted `name=value` raw-value capture** (parser.go:420): when the token
   was unquoted and contains `=`, the parser re-slices `p.raw` starting just
   after the `=` to recover the *true* value. It scans forward, honoring quotes,
   stopping at an unquoted `&`, `|`, `>`, `<`, or `\x01`. Two cmd.exe quirks are
   reproduced here:
   - If a separator was hit, trailing whitespace before it is trimmed
     (visual spacing). 
   - If end-of-line was reached, trailing whitespace is **preserved**, so
     `set X= ` assigns a single space. (parser.go:444-446)
   After re-slicing, it consumes the now-redundant tokens up to the next
   separator (parser.go:451-458).
5. **`SET /A` with a spaced expression** (parser.go:463): when `/A` is set and
   no `=` was found in the first token (e.g. `set /a ii = 2 * i`, where `=` is
   its own token), all remaining tokens up to a redirect are concatenated and
   re-split on the first `=`. Name and value are each `TrimSpace`d. If there is
   still no `=`, the whole expression becomes `Name` with `HasEquals == false`
   (a bare `set /a expr` that evaluates without assigning).
6. Otherwise (`eqIdx >= 0`): `Name` is the text before `=`, and the value text
   after `=` is split into word groups by `parseWordParts` + `collectWordGroups`
   (parser.go:495-514). A special case (parser.go:500) merges an immediately
   adjacent following token (no space, not a redirect) into the initial value
   group, so `SET acc=VAR_!x!` keeps `VAR_` and the `!x!` reference in the same
   group.
7. Trailing redirects are collected (parser.go:518) — this is how
   `SET /P var= < file` captures its `<` and `SET /A "d=1" 2>nul` captures its
   `2>`.

```bat
set name=value
set "name=value with spaces"
set /a result = 2 * (x + 1)
set /a counter+=1
set /p answer=Continue? 
set X= 
```

Because raw-value capture depends on `p.raw`, **`SET` parsed via `Parse` (empty
`raw`) loses trailing-whitespace fidelity**; only the `ParseLine*` paths get it.

### `ECHO`: toggles, `RawText`, and the `ECHO(`/`ECHO.` forms

`parseEcho` (parser.go:314):

1. Consume `ECHO`. Nothing after → empty `EchoStatement` (cmd.exe prints the
   current echo state, handled downstream).
2. If the next word is `ON`/`OFF` (case-insensitive), set `TurnOn`
   (parser.go:322-334).
3. Collect the body with `collectEchoWordGroups` (see the quirk below).
4. Compute `argsEndPos` from the position of the first token after the body
   (parser.go:340-343) — this bounds verbatim slicing so a block's closing `)`
   is not captured.
5. Collect trailing redirects.
6. **Verbatim `RawText` capture** (parser.go:358-378): only when there are no
   redirects and `p.raw` is non-empty. The content starts right after the
   `ECHO` token, skipping **exactly one** separator space/tab (cmd.exe rule: the
   first space after `ECHO` is the separator, everything after is literal). The
   slice is right-trimmed of spaces/tabs. **If the text contains a `^`**, the
   verbatim path is abandoned (`HasRaw` stays false) and `Args` are used
   instead, because carets are escapes the token path already resolved
   (parser.go:373).

```bat
echo on
echo off
echo Hello,    World      ← internal spacing preserved via RawText
echo a ^& b               ← caret present: falls back to Args
echo message > out.txt    ← redirect present: RawText not used
```

#### `ECHO(` and `ECHO.`

These are handled in `parseOne` *before* the keyword switch, because they are
glued tokens (`echo(text`, `echo.`):

- **`ECHO.`** (parser.go:212): any token starting with `ECHO.` →
  `EchoStatement{Newline: true}` + trailing redirects. The classic blank-line
  idiom `echo.`. Note: because this is a plain prefix match, `echo.hello` also
  takes this branch and the `hello` is dropped — only the newline is emitted.
- **`ECHO(`** (parser.go:219): the safe-echo idiom. `echo(text` is equivalent to
  `echo text`; `echo(` alone prints a blank line. The text glued onto the
  `echo(` token (`tok.Value[len("echo("):]`) is parsed with `parseWordParts`,
  then any further space-separated args via `collectWordGroups`. Empty → blank
  line; otherwise an `EchoStatement{Args: groups}`.

```bat
echo.
echo(this is the safe form
echo(%var_that_might_be_empty%
```

The `ECHO(` form is preferred in real scripts because it cannot be derailed by a
value that looks like `/?` or `off`.

### `CALL`: `RawText` and redirects

`parseCall` (parser.go:733):

1. Consume `CALL`, record `rawStart` (byte position of the first arg token).
2. `Args` = `collectWordGroups` — each whitespace-separated token is one
   argument; parts of a single token (e.g. a quoted `"!_path!"`) stay grouped so
   a quoted delayed-expansion arg isn't split into multiple positional params.
3. `rawEnd` = position of the token after the args (redirect/`&`/`|`/`)`/EOF),
   clamped to `len(p.raw)`.
4. `RawText` = `strings.TrimSpace(p.raw[rawStart:rawEnd])` — the verbatim args
   text **including the command word**. This is what lets the executor
   re-expand a `CALL` of a builtin (e.g. `call set x=%y%`) from original text.
5. Trailing redirects collected (parser.go:755).

```bat
call :subroutine arg1 "arg with spaces"
call other.bat %*
call set "var=%base%\sub"     ← RawText preserves the inner SET for re-expansion
call foo > out.txt 2>&1
```

### `GOTO`

`parseGoto` (parser.go:725): consume `GOTO`, then `collectWordParts` for the
label, stored in `LabelParts`. Dynamic labels expand at run time.

```bat
goto :eof
goto label_%errorlevel%
```

### `EXIT`: `/B` and dynamic codes

`parseExit` (parser.go:863):

1. Optional `/B` → `SubOnly` (exit the batch/subroutine, not the process).
2. If the next token is a `WORD` that parses as an integer, it becomes `Code`.
   A non-numeric word is **left unconsumed** (no error).
3. If the next token is a `BANG_VAR` or `PERCENT_VAR`, it becomes `CodeParts`
   (via `tokenToParts`) and the function returns immediately — this is the
   dynamic-code path (`EXIT /B !ERRORLEVEL!`).

```bat
exit /b 0
exit /b %errorlevel%
exit /b !rc!
exit 1
```

### `SETLOCAL`

`parseSetlocal` (parser.go:705): consume `SETLOCAL`, then loop over `WORD`
arguments setting `EnableDelayedExpansion` / `DisableDelayedExpansion` for the
matching (uppercased) keywords; any other word stops the loop. Both flags can in
principle be set, though normally only one appears.

```bat
setlocal enabledelayedexpansion
setlocal disabledelayedexpansion
```

## Block parsing

`parseBlock` (parser.go:265):

1. Consume `(`, increment `blockDepth` (decremented by deferred call on return).
2. Loop until `RPAREN` or `EOF`:
   - skip bare `AMPERSAND` separators;
   - parse each statement with **`parseIfChain`** (parser.go:279), not
     `parseChain`. Inside a block, each physical line is its own statement and
     both `&` and `\x01` act as separators, so chaining through `&` within the
     block is intentionally avoided.
   - force-consume on no-progress, same anti-spin guard as `parseStatements`.
3. Consume the closing `)` if present (a missing `)` is tolerated).

The result is a `BlockStatement`. `parseIf`, `parseFor`, and the `ELSE` branch
all reuse `parseBlock` and lift `block.(*BlockStatement).Stmts` into their body
slices.

### The `\x01` block-line marker

When physical lines are joined into one logical line for a multi-line block, a
`\x01` byte is inserted at each boundary. The lexer turns it into an
`AMPERSAND` token whose `Value` is `"\x01"` (lexer.go:63-65). The parser uses it
in two ways:

- Inside a block, `parseIfChain` stops at it, so each original line is parsed as
  a distinct statement (it behaves like a statement separator there).
- For an unparenthesized `IF`/`ELSE` body, `parseIfChain` stops at it so the
  body does not "leak" into the following block line.

In effect: a real `&` chains commands on the same source line; a `\x01` (an
end-of-physical-line) does not. `parseChain` (used at top level) treats `\x01`
like any `&` because at top level there is no enclosing block to delimit.

```bat
(
    echo first
    if "%x%"=="1" echo cond
    echo third          ← NOT swallowed by the IF above, thanks to \x01
)
```

## Redirects

### `collectRedirects`

`collectRedirects` (parser.go:301) consumes a run of trailing `REDIRECTION`
tokens, each followed by an optional `WORD` file target, returning
`[]Redirect`. Several parsers inline an equivalent loop rather than calling it
(`parseEcho`, `parseSet`, `parseCall`, `parseSimpleCommand`), but the behavior is
identical: operator token → `Op`, following `WORD` → `File` (empty if absent,
as with `2>&1`).

### Leading redirects

`parseOne` handles redirects that appear **before** the command word
(parser.go:183), e.g. `> out.txt echo hi`. It collects them, recurses into
`parseOne` for the command, and calls `attachRedirects`.

`attachRedirects` (parser.go:78) **prepends** the leading redirects to the
statement's existing redirect list, and only for statement types that carry
redirects: `EchoStatement`, `SimpleCommand`, `SetStatement`, `CallStatement`
(parser.go:82-93). Other statement kinds silently ignore leading redirects. The
comment notes that because leading redirects were consumed *before* `parseEcho`
ran, the echo's verbatim `RawText` slicing is unaffected.

```bat
> out.txt echo hello        ← redirect attached to the EchoStatement
2>> err.log call foo.bat
```

## From raw text to WordParts

A `WordPart` (ast.go:201) is one piece of a word. Words mix literals and
variable references; the parser turns token strings into `[]WordPart`.

### `WordPart` types

- **`LiteralPart{Text string}`** (ast.go:203) — plain text.
- **`VarPart{Name string; Positional int}`** (ast.go:207) — a `%VAR%` reference
  (`Positional == -1`) or a positional parameter `%0`..`%9` (`Name == ""`,
  `Positional` set). FOR variables (`%%I`) are also `VarPart{Name: "I",
  Positional: -1}`.
- **`DelayedVarPart{Name string}`** (ast.go:215) — a `!VAR!` delayed-expansion
  reference.
- **`TildeVarPart{Positional int; Modifiers string; Name string}`**
  (ast.go:224) — a tilde-modified reference. Two forms share this node:
  - **Call/positional form** `%~1`, `%~dp0`: `Name == ""`, value comes from
    `Positional`, `Modifiers` are the letters between `~` and the digit.
  - **FOR-variable form** `%%~nf`, `%%~dpf`: `Name` is the trailing letter (the
    FOR variable, e.g. `"f"`), `Modifiers` are the preceding letters (`"n"`,
    `"dp"`), and `Positional == -1`. The value is read from the FOR variable's
    current binding rather than a positional parameter.
  `Modifiers` letters are `d p n x f s a t z` (drive, path, name, extension,
  full, short, attrs, time, size); empty modifiers means "strip surrounding
  quotes."
- **`SubstringVarPart{Name string; Start int; Length int; HasLength bool}`**
  (ast.go:233) — `%VAR:~N%` or `%VAR:~N,M%`. `HasLength` distinguishes the
  one-argument form (`:~N`, take to end) from the two-argument form (`:~N,M`).

### `tokenToParts`

`tokenToParts` (parser.go:1054) maps a single token to `[]WordPart`:

- `PERCENT_VAR` → `varPartFromToken(tok.Value)`.
- `BANG_VAR` → a single `DelayedVarPart` (trim the `!` delimiters).
- everything else → `parseWordParts(tok.Value)`.

### `varPartFromToken`

`varPartFromToken` (parser.go:1235) classifies a *whole-token* `PERCENT_VAR`
value, in order:

1. `%%~<mods><letter>` → `forTildePart` (FOR-variable tilde form).
2. `%~<mods>N` → `TildeVarPart{Positional}` (positional tilde form). If the last
   character is not a digit (e.g. a malformed `%~`), it degrades to a
   `LiteralPart` of the raw text.
3. `%%I` → `VarPart{Name: "I"}` (FOR variable).
4. `%N` (exactly two chars, digit) → positional `VarPart`.
5. `%VAR%` or `%VAR:~N,M%` → `SubstringVarPart` (if `:~` present) else
   `VarPart{Name}`.

### `parseWordParts`

`parseWordParts` (parser.go:1068) is the general scanner used for *embedded*
references inside an arbitrary string (e.g. a SET value, an IF operand, an
`echo(` tail). It walks the string finding the next `%` or `!` (whichever is
first) and emits a `LiteralPart` for the text before it, then classifies the
reference:

- **`!VAR!`** (parser.go:1103): finds the closing `!`. If there is no close, or
  the name is empty, a literal `"!"` is emitted and scanning continues.
  Otherwise a `DelayedVarPart`. (This branch only fires when the lexer left
  `!...!` *inside* a word string rather than tokenizing it as `BANG_VAR`.)
- **`%%~<mods><letter>`** (parser.go:1124): a FOR-variable tilde reference (at
  least one trailing letter). Uses `forTildePart`.
- **`%%X`** (parser.go:1138): a single-letter FOR variable, but **only** when
  the following character is *not* alphanumeric/underscore (otherwise it is part
  of a longer name). Emits `VarPart{Name: "X"}`.
- **`%~[mods]N`** (parser.go:1152): tilde positional reference; the letters run
  up to the digit. If no digit follows, the `%` is emitted literally.
- **`%N`** (parser.go:1171): positional parameter.
- **`%NAME%`** (parser.go:1178): finds the closing `%`. No close, or empty name
  → literal `"%"`. With `:~` in the name → `SubstringVarPart`; otherwise
  `VarPart{Name}`.

The substring spec parsing (parser.go:1192-1210, mirrored in `varPartFromToken`)
splits on the first comma; `strconv.Atoi` errors are ignored, leaving `0`. A
single-argument `:~N` sets `HasLength == false`.

Unterminated `%` and `!` are deliberately *not* errors — they become literal
text, matching cmd.exe's lenient handling of stray percent/bang characters.

### `forTildePart`

`forTildePart` (parser.go:1220) parses a `%%~<modifiers><letter>` string: the
**last** letter is the FOR variable (`Name`), the preceding letters are
`Modifiers`, and `Positional == -1`. Returns `nil` if the input doesn't start
with `%%~`, is empty after the prefix, or doesn't end in a letter — so callers
can fall through to other interpretations.

## Word-collection helpers

These drain runs of tokens into word parts/groups, differing in their stop
conditions and grouping:

- **`collectWordGroups`** (parser.go:942) → `[][]WordPart`. Stops at
  `atEnd()`-class tokens plus `PIPE`/`REDIRECTION`. **Merges adjacent tokens**
  (`!SpaceBefore`) into the same group, so `VAR_!x!` (no space) stays one
  argument. Used by `parseCall`, `parseSet`, the `ECHO(` tail, and `parseEcho`'s
  body via the variant below.
- **`collectEchoWordGroups`** (parser.go:965) → `[][]WordPart`. Like
  `collectWordGroups` but does **not** treat `)` as a terminator at top level:
  when `blockDepth == 0` an `RPAREN` is consumed and emitted as the literal text
  `)`. This reproduces `echo === LL(1) Parse Table ===` printing in full at top
  level. Inside a block (`blockDepth > 0`) a `)` still terminates. It also does
  not use `atEnd()` (which treats `)` as a terminator).
- **`collectWordParts`** (parser.go:998) → flat `[]WordPart`. Stops at
  `atEnd()` plus `PIPE`/`REDIRECTION`. No grouping. Used for `GOTO` labels and
  some IF operands.
- **`collectOneWordParts`** (parser.go:1012) → flat `[]WordPart` for a **single**
  word: stops at the first `SpaceBefore` token (after the first), and refuses to
  merge a leading-`==` token. Used for IF operands, `EXIST`, `DEFINED`.
- **`collectUntilOp`** (parser.go:1035) → flat `[]WordPart`, consuming up to and
  including a token ending with the given operator (`"=="`). Used by the
  `parseCondition` fallback.

`atEnd` (parser.go:934) returns true at `EOF`, `AMPERSAND`, `AND`, `OR`, or
`RPAREN` — the tokens that end a statement.

## Edge cases and cmd.exe quirks reproduced

A consolidated list of the deliberate quirks, each grounded in code:

- **Stray `)` at top level is literal text** in `ECHO` bodies
  (`collectEchoWordGroups` + `blockDepth`), so `echo LL(1)` prints correctly
  (parser.go:983-989).
- **`set X= ` preserves a trailing space**; `set X= &` trims it. End-of-line
  whitespace is value; pre-separator whitespace is layout (parser.go:444-446).
- **`echo` with a `^` falls back to token-based `Args`** instead of verbatim
  `RawText`, because the lexer already resolved the caret escapes
  (parser.go:373).
- **Exactly one space after `ECHO`** is the separator; subsequent spaces are
  literal (`RawText`), reproducing cmd.exe's spacing rules (parser.go:362).
- **`IF NOT /I` and `IF /I NOT`** are both accepted (order-independent flag loop,
  parser.go:545).
- **`IF`/`ELSE` bodies stop at `\x01`** so they don't swallow the next line of a
  block (`parseIfChain`, parser.go:125).
- **`FOR /L` in-list commas** are split into separate items (parser.go:816), so
  `(1,1,5)` becomes `1 1 5`.
- **`FOR /R` root path** is taken only when the next word isn't `%`-prefixed
  (parser.go:793).
- **Dynamic `GOTO`/`EXIT`/`DEFINED` targets** keep `WordPart` slices for run-time
  expansion rather than resolving statically.
- **Unterminated `%...%` / `!...!`** become literal `%` / `!`, never errors
  (parser.go:1105, 1179, etc.).
- **Anti-spin guards** in `parseStatements` and `parseBlock` force progress on
  tokens no production will consume (parser.go:70, 287), guaranteeing
  termination on malformed input.
- **`Parse` (no `raw`) silently disables verbatim capture** — only
  `ParseLine*` get `ECHO`/`CALL` `RawText` and `SET` whitespace fidelity
  (parser.go:13 vs 25).
