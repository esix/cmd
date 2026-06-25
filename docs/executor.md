# Executor: statement execution, control flow, and I/O

The `executor` package ([executor/executor.go](../executor/executor.go)) is the
engine that runs parsed BAT statements. It owns the program counter, the GOTO
label index, the FOR / CALL / IF control flow, pipes and chains, redirection,
and the dispatch to [builtins](builtins.md) and external commands. It consumes
the AST produced by the [parser](parser.md), the variable model from the
[env](env.md) package, and the variable-substitution rules from the
[expander](expansion.md).

Two important global invariants this package reproduces from cmd.exe:

- **Abort semantics.** A missing CALL/GOTO label or a malformed `IF` does not
  merely fail the current statement — it *terminates the whole batch file* and
  returns control to the caller with `errorlevel 1`. This is threaded through
  the code as `abortPending`.
- **CRLF output.** Every line the interpreter prints itself (`ECHO`, `ECHO is
  off.`, blank lines) ends with a literal `\r\n`, matching cmd.exe's console
  output, regardless of host OS.

## The `Executor` struct

```go
type Executor struct {
	env          *env.Env
	positional   []string           // %0, %1, ... script arguments
	stmts        []parser.Statement // used by RunStmts (interactive / inline)
	lines        []scriptLine       // used by RunFile (lazy parsing)
	labelIdx     map[string]int     // label name → line index for O(1) GOTO
	pc           int                // current line or statement index
	gotoPending  bool               // GOTO executed in a nested context
	exitPending  bool               // EXIT /B executed in a nested context
	abortPending bool               // current batch file must terminate
	activeForVars []string          // FOR variables currently in scope (innermost last)
}
```

[executor.go:33](../executor/executor.go#L33)

There are two execution modes, distinguished by which field is populated:

- **File mode** drives `lines` + `labelIdx` (set by `RunFile`/`runLines`). GOTO
  uses the O(1) label index, and each line is parsed *lazily* so that a
  `SETLOCAL EnableDelayedExpansion` on one line changes parsing of the next.
- **Statement mode** drives `stmts` (set by `RunStmts`), used for interactive
  input and for nested blocks (the `Then`/`Else` of an `IF`, the body of a
  `FOR`). Here labels are `parser.LabelStatement` nodes scanned linearly.

`pc` is the shared program counter for whichever slice is active. The three
`*Pending` booleans are the unwind signals.

### `shouldStop` — the unwind check

```go
func (ex *Executor) shouldStop() bool {
	return ex.gotoPending || ex.exitPending || ex.abortPending
}
```

[executor.go:48](../executor/executor.go#L48)

`shouldStop` is the universal "stop running statements in this block" predicate.
Every loop that iterates a statement list (`RunStmts`, `execBlock`, the FOR
bodies, `execChain`, `callBuiltin`) checks it after each statement. The three
flags differ only in *who clears them*:

- `gotoPending` is cleared by the loop that owns the target label (the file
  `runLines` loop or the CALL subroutine loop), where `pc` has already been
  repointed.
- `exitPending` (`EXIT /B`) unwinds out of nested blocks until a frame that
  owns the running file/subroutine clears it and stops.
- `abortPending` propagates *all the way up* the file, clearing only at the
  top of `runLines` (and reset at the REPL prompt).

### `activeForVars`

`activeForVars` tracks the loop variables of enclosing FOR loops (innermost
last), pushed in `execFor` and `execForTokens` and popped via `defer`. Its sole
purpose is to let CALL's extra expansion round distinguish a real FOR-variable
reference (`%%i`) from the `%%`→`%` escape in `call set "y=%%x%%"`. See
`expandCallText` below. `isActiveForVar` does the case-insensitive lookup
([executor.go:53](../executor/executor.go#L53)).

## `updatesErrorlevel` — who touches `%ERRORLEVEL%`

```go
func updatesErrorlevel(stmt parser.Statement) bool {
	switch stmt.(type) {
	case *parser.EchoStatement, *parser.SetStatement, *parser.IfStatement,
		*parser.ForStatement, *parser.SetlocalStatement, *parser.EndlocalStatement,
		*parser.LabelStatement, *parser.ShiftStatement, *parser.GotoStatement:
		return false
	}
	return true
}
```

[executor.go:65](../executor/executor.go#L65)

In real cmd.exe, pure flow-control / assignment builtins do **not** reset
`%ERRORLEVEL%`. So `ECHO`, `SET`, `IF`, `FOR`, `SETLOCAL`, `ENDLOCAL`, a label,
`SHIFT`, and `GOTO` are exempt; everything else (external commands, `CALL`,
`EXIT`, `SimpleCommand` builtins like `TYPE`/`DIR`, pipes, chains, blocks) sets
`env.ExitCode` to its return value. This predicate is consulted after every
statement in `runLines`, `RunStmts`, and `execBlock` — note the FOR/IF
*body* still updates errorlevel per inner statement; only the FOR/IF wrapper
itself is exempt.

```bat
@echo off
findstr /c:"x" missing.txt
echo done            & rem ECHO does not clobber the errorlevel from findstr
if errorlevel 1 echo not found
```

## File preprocessing in `RunFile`

`RunFile` ([executor.go:123](../executor/executor.go#L123)) reads and
preprocesses a `.bat` file once, caches the result, and hands it to `runLines`.

### Path normalization and drive-letter stripping

The path has its `\` converted to `/`, then a Windows drive-letter prefix is
stripped so `C:\foo` resolves on Unix:

```go
if len(path) >= 2 && path[1] == ':' &&
	((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) {
	path = path[2:]
	if path == "" { path = "/" }
}
```

[executor.go:128](../executor/executor.go#L128). `C:` alone becomes `/`. The
same stripping logic is duplicated in `resolveBat`
([executor.go:1784](../executor/executor.go#L1784)) and in the `ExistCondition`
branch of `evalCondition`.

### `scriptLine` classification

Each physical line (after caret-continuation joining, see below) is classified
into a `scriptLine{raw, label}` ([executor.go:21](../executor/executor.go#L21)).
The classification loop ([executor.go:152](../executor/executor.go#L152)), in
order:

1. `\r` is trimmed from the right (CRLF tolerance); fully blank lines are
   dropped.
2. **`@` stripping** — a leading `@` (possibly after leading whitespace) is the
   echo-suppression marker; it is removed and the line re-trimmed. Note: in
   file mode `@` is consumed here and `echo off` state is governed solely by
   `env.Echo`; the per-line echo suppression is effectively a no-op because
   `runLines` never echoes the command itself (only the REPL handles echo).
3. **Shebang** — a line starting with `#!` is skipped (lets a `.bat` double as
   a Unix script).
4. **`::` comment** — a line starting with `::` is dropped. (`::` is really a
   label that can never be a valid GOTO target; here it is treated as a pure
   comment.)
5. **`:LABEL`** — a single leading `:` makes a label. Only the *first
   whitespace-delimited word* after the colon is the label name (the rest is a
   comment); it is uppercased and stored in `label`. `:` with nothing after it
   is dropped.
6. **`REM`** — `REM` followed by end-of-line or a space is dropped. (`REMARK`
   is *not* a comment because the 4th char must be a space.)
7. Otherwise the (un-trimmed-left? no — `line` was reassigned to the trimmed
   form during `@` handling) raw text becomes `raw`.

After classification, `joinBlocks` merges multi-line parenthesized blocks
(below), then the **label index** is built in one pass
([executor.go:198](../executor/executor.go#L198)) for O(1) GOTO. The result is
stored in `fileCache` keyed by absolute path.

### `joinCaretContinuations`

```go
func joinCaretContinuations(lines []string) []string
```

[executor.go:1705](../executor/executor.go#L1705). A line ending in an *odd*
number of `^` characters continues onto the next physical line; the trailing
caret is dropped and the next line appended. `endsWithContinuationCaret`
([executor.go:1726](../executor/executor.go#L1726)) counts trailing carets and
returns true only for an odd count, so `^^` (an escaped caret) is **not** a
continuation.

```bat
echo one ^
two ^
three
rem prints: one two three   (the carets vanish, lines concatenate verbatim)
```

This runs over the raw `[]string` *before* classification, so a continuation
can span what would otherwise look like a blank or comment line.

### `joinBlocks` and the `\x01` marker

```go
func joinBlocks(lines []scriptLine) []scriptLine
```

[executor.go:1734](../executor/executor.go#L1734). A `( ... )` block that spans
multiple physical lines (the common `if cond (` / body / `)` form) is collapsed
into one `scriptLine.raw` so the lazy per-line parser sees a complete statement.

It tracks paren `depth` via `countUnquotedParens`
([executor.go:1677](../executor/executor.go#L1677)), which:

- ignores parens inside double quotes, and
- skips a `(` immediately preceded by `ECHO` (the `echo(text` idiom — the paren
  belongs to ECHO, not a block).

While `depth > 0`, subsequent lines are appended to an accumulator joined by the
**`\x01` byte**, not a space or `&`:

```go
accum += "\x01" + strings.TrimSpace(line)
```

[executor.go:1764](../executor/executor.go#L1764). The doc comment notes `&` but
the implementation uses `\x01` as a line-boundary marker — the parser treats it
as a statement separator that is **not** swallowed into the IF/FOR body, which
preserves correct statement boundaries inside a block. A label line encountered
mid-block flushes the accumulator and resets depth (a label can't be inside a
block). Any unterminated accumulator is flushed at the end.

```bat
if exist out.txt (
    echo found
    del out.txt
) else (
    echo missing
)
rem becomes one logical line: if exist out.txt (<0x01>echo found<0x01>del out.txt<0x01>) else (<0x01>echo missing<0x01>)
```

### `fileCache`

```go
var fileCache = map[string]*scriptFile{}
```

[executor.go:119](../executor/executor.go#L119). Keyed by absolute path, it
memoizes the `(lines, labelIdx)` for a file so repeated `CALL script.bat`
avoids re-reading and re-preprocessing. Note the in-source comment claiming the
cache is disabled is stale — the cache is live (`RunFile` both reads it at
[executor.go:138](../executor/executor.go#L138) and writes it at
[executor.go:205](../executor/executor.go#L205)). The cache stores only
preprocessing output; parsing is still done lazily per line on every run, so a
re-`CALL` still honors current delayed-expansion state.

## The program-counter loop: `runLines`

```go
func (ex *Executor) runLines(slines []scriptLine, labelIdx map[string]int, path string, args []string) int
```

[executor.go:209](../executor/executor.go#L209). This is the heart of file
execution. It:

1. Sets `env.FileMode = true`, and saves/restores `positional`, `lines`,
   `labelIdx`, `pc` (so a `CALL`ed file nests cleanly). `positional` is set to
   `[path, args...]` — i.e. `%0` is the script path.
2. Loops `while pc < len(lines)`, fetching `lines[pc]` and pre-incrementing
   `pc`. **Pre-increment matters**: GOTO/CALL set `pc` to `idx+1` so the label
   line itself is skipped and execution resumes at the line *after* the label.
3. A `label != ""` line is a no-op (`continue`).
4. Otherwise the raw line is run through `expander.ExpandPercent` (line-level
   `%VAR%`/`%1` expansion against the current env and positional), then
   `parser.ParseLineWithOpts(expanded, env.DelayedExpansion)`. Parse errors are
   reported to stderr and the line skipped (execution continues — cmd.exe is
   forgiving of per-line parse noise).
5. Each resulting statement is `execute`d. After each: `updatesErrorlevel`
   gates `env.ExitCode`, then the three pending flags are checked in this exact
   order:

```go
if ex.abortPending {
	ex.abortPending = false
	ex.pc = len(ex.lines)
	code = 1
	ex.env.ExitCode = 1
	break
}
if ex.gotoPending {
	ex.gotoPending = false
	break
}
if ex.exitPending {
	ex.exitPending = false
	ex.pc = len(ex.lines)
	break
}
if ex.pc >= len(ex.lines) { break }  // EXIT /B inside a chain moved pc past end
```

[executor.go:242](../executor/executor.go#L242).

- **abort** ends the file with code 1 (`pc` forced to end, `ExitCode` forced
  to 1). This is the cmd.exe "missing label / malformed IF terminates the
  batch" behavior.
- **goto** breaks the inner statement loop; `pc` was already repointed by
  `execGoto`, so the outer `for` resumes there.
- **exit** (`EXIT /B`) forces `pc` to end, terminating this file but *not* the
  process.
- The final guard catches the case where `GOTO :EOF` or `EXIT /B` ran inside a
  chain/block and moved `pc` directly to the end without setting a flag the
  inner loop saw.

## `RunStmts` — statement mode and SHIFT scope

```go
func (ex *Executor) RunStmts(stmts []parser.Statement, positional []string) int
```

[executor.go:276](../executor/executor.go#L276). Runs a pre-parsed statement
slice with GOTO support against `stmts`. Two subtleties:

- **Positional sharing.** When `positional == nil` (the usual call from IF/FOR
  bodies), it *shares* the parent's `positional` rather than overriding it.
  This is deliberate: a `SHIFT` inside an `IF (...)` block must persist to the
  enclosing scope, matching cmd.exe. Only `CALL` passes a fresh non-nil slice,
  and only then is `positional` restored on exit
  ([executor.go:308](../executor/executor.go#L308)).
- **PC restore.** After the loop, `pc` is restored to `savedPC` *unless* a GOTO
  or EXIT is pending — those need the modified `pc` to survive back to the
  owning file loop.

`RunLine` ([executor.go:81](../executor/executor.go#L81)) is the interactive
entry point: it clears `abortPending` first (the REPL never dies), strips a
leading `@`, special-cases `ECHO.`/`ECHO.x` for blank-line output, runs
line-level `%`-expansion, parses, and calls `RunStmts`.

## `execute` dispatch

`execute` ([executor.go:316](../executor/executor.go#L316)) is a type switch
over the statement AST: `Echo`, `Set`, `If`, `Goto`, `Call`, `For`, `Exit`,
`Shift`, `Pipe`, `Chain`, `Block`, `Setlocal`, `Endlocal`, `Label` (no-op),
`SimpleCommand`. An unknown type prints to stderr and returns 1.

## GOTO

```go
func (ex *Executor) execGoto(s *parser.GotoStatement) int
```

[executor.go:777](../executor/executor.go#L777). The label is taken from the
expanded `LabelParts` (so `goto %target%` works) or the literal `Label`, then
`TrimPrefix(":")` and uppercased.

- **`GOTO :EOF`** — sets `pc` to the end of `lines` (file mode) or `stmts`
  (statement mode) and returns 0 *without* setting `gotoPending`. This silently
  exits the current file/subroutine.
- **File mode** — looks up `labelIdx[label]` (O(1)); on hit sets `pc = idx+1`
  and `gotoPending = true`. There is a defensive linear-scan fallback
  ([executor.go:804](../executor/executor.go#L804)) if the index somehow missed.
  On miss, `missingLabel` is called (abort).
- **Statement mode** — linear scan for a `LabelStatement` with the matching
  name; on hit sets `pc = idx+1` and returns 0 (note: **no** `gotoPending` here,
  since `RunStmts` checks `shouldStop` and would break — in statement mode GOTO
  within the same `stmts` works by `pc` repointing and the loop continuing). On
  miss, `missingLabel`.

### `missingLabel`

```go
func (ex *Executor) missingLabel(label string) int {
	fmt.Fprintf(os.Stderr, "The system cannot find the batch label specified - %s\n", label)
	ex.abortPending = true
	return 1
}
```

[executor.go:829](../executor/executor.go#L829). Sets `abortPending`, which
unwinds the entire batch file. Shared by GOTO and CALL.

## CALL

```go
func (ex *Executor) execCall(s *parser.CallStatement) int
```

[executor.go:837](../executor/executor.go#L837). CALL has four targets, checked
in order, plus quirks:

1. **Redirections** for the whole CALL (`CALL foo > out`) are applied up front
   via `applyRedirects` and deferred-cleaned.
2. **Arg handling.** The target (`s.Args[0]`) is expanded and
   `stripOuterQuotes`d (so `call "path with spaces"` opens the file). The
   remaining args are each expanded and then run through `splitArgs` — an
   *unquoted* expansion that yields spaces splits into multiple positional
   params (cmd.exe behavior: `call :x !list!` where `list="2 3"` passes two
   args), while a quoted expansion stays one arg. The assembled `callArgs` has
   `callArgs[0]` = label/script name = `%0` of the callee.

### `CALL :label` — subroutine

If `first` starts with `:`, it's an internal subroutine
([executor.go:867](../executor/executor.go#L867)). It saves `pc`/`positional`,
sets `positional = callArgs`, jumps to `labelIdx[label]+1`, and runs its own
program-counter loop until the file ends or unwinds:

- An `ExitStatement` with `SubOnly` (`EXIT /B`) runs `execExit` and stops the
  subroutine (this is how a subroutine returns).
- `shouldStop` breaks the inner statement loop per line.
- `abortPending` from a missing inner label / malformed IF **breaks out and
  propagates** so the whole file unwinds.
- `gotoPending` is cleared and the loop *continues* — a GOTO inside a
  subroutine jumps within the same file body (the subroutine has no separate
  boundary; it runs until `EXIT /B`, `GOTO :EOF`, or end of file, exactly like
  cmd.exe). `exitPending` is cleared and breaks.

On a missing subroutine label, `missingLabel(first[1:])` aborts. Statement-mode
CALL `:label` ([executor.go:919](../executor/executor.go#L919)) does the
analogous linear scan.

```bat
@echo off
call :greet World
echo back, errorlevel=%errorlevel%
goto :eof

:greet
echo Hello %1
exit /b 7
```

### `CALL` of a builtin — `callBuiltin` and double expansion

```go
func (ex *Executor) callBuiltin(first string, s *parser.CallStatement) (int, bool)
```

[executor.go:975](../executor/executor.go#L975). `call set`, `call echo`, etc.
re-dispatch to the builtin with **one extra round of `%`-expansion** — the
classic batch double-expansion idiom. The mechanism:

- Look up the uppercased first word in `builtins.Registry`; `echo.`/`echo(`
  glue onto `ECHO`. Non-builtin → `handled=false`, fall through to script
  resolution.
- The raw command text (`s.RawText`) is run through `expandCallText` (below).
  If no raw text is available, args are reassembled best-effort.
- **`call set` exception** ([executor.go:997](../executor/executor.go#L997)):
  `call set` is dispatched to the SET builtin only when its remainder looks like
  a SET invocation — empty (list all), contains `=`, starts with `/` (a switch),
  or is a single word (variable query). A remainder of *multiple plain words*
  (`call set verb arg`) is **not** a valid SET, so `handled=false` and CALL
  falls through to resolve a `set.bat` on PATH (real batch suites, e.g.
  gw-batsic's stl module, do this).
- The expanded text is parsed with **delayed expansion off** (bangs already
  resolved by the extra round, so values containing `!` aren't re-expanded) and
  each statement executed, honoring `shouldStop`.

```go
func (ex *Executor) expandCallText(text string) string
```

[executor.go:1035](../executor/executor.go#L1035) performs CALL's extra round:

- A `%%X` where `X` is a single letter that is an *active FOR variable* (and not
  followed by an alphanumeric, to avoid matching `%%var`) substitutes the loop
  value via `env.Get`.
- Every other `%%` pair collapses to a single `%`.
- Then, if delayed expansion is on, `ExpandBangs` runs; finally a second
  `ExpandPercent` pass runs.

This is what makes `call set "y=%%x%%"` work: `%%x%%` → `%x%` → the value of
`x`. See [Expansion](expansion.md) for `ExpandBangs`/`ExpandPercent`/
`ExpandForVars` details.

```bat
@echo off
set x=hello
call set "y=%%x%%"
echo %y%        & rem prints: hello
```

### `CALL script.bat`

Otherwise the target is resolved via `resolveBat` and run on a **child
Executor** sharing the same `env`, with `callArgs[1:]` as its arguments
([executor.go:953](../executor/executor.go#L953)).

## The FOR family

`execFor` ([executor.go:1069](../executor/executor.go#L1069)) pushes
`s.Variable` onto `activeForVars` (popped via defer), then dispatches on
`s.Kind`.

### `FOR /L` — `execForRange`

```go
func (ex *Executor) execForRange(s *parser.ForStatement) int
```

[executor.go:1150](../executor/executor.go#L1150). Iterates `(start,step,end)`.
Bounds are parsed with **delayed bounds**: each is run through `ExpandBangs`
first (so `for /l %%i in (1,1,!n!)` reads the runtime value of `n`; `%VAR%` was
already expanded at line level), then `Sscanf`d to an int (non-numeric → 0). The
loop direction follows the sign of `step`; only `abortPending` breaks the loop
(GOTO/EXIT inside a `FOR /L` body still terminate the iteration via the body's
own `RunStmts`).

### `FOR ... IN (...)` — `execForInList`

```go
func (ex *Executor) execForInList(s *parser.ForStatement) int
```

[executor.go:1179](../executor/executor.go#L1179). For each raw item:

1. `ExpandBangs` then `ExpandForVars` (an inner `for %%p in (%%j)` sees the
   outer loop's value). Empty expansion → no iterations.
2. **Quote/space handling**: a fully double-quoted item is one element even with
   spaces; otherwise items containing whitespace are split with
   `strings.Fields` (so `for %%i in (!list!)` with `list="a b c"` iterates three
   times); a single word is one item.
3. **Glob vs literal**: an item containing `*` or `?` is `stripOuterQuotes`d,
   `\`→`/`, and `filepath.Glob`bed. Crucially, plain `FOR` is **file-only** —
   directory matches are skipped (`if info.IsDir() { continue }`); that
   distinction is what separates plain `FOR` from `FOR /D`. A no-match wildcard
   yields zero iterations (it does **not** fall back to the literal pattern).
4. A literal element keeps its surrounding quotes on the FOR variable
   (`for %%i in ("a b")` → `%%i` is `"a b"`), matching cmd.exe.

`shouldStop` after each body iteration unwinds the loop.

```bat
for %%f in (*.bat) do echo script: %%f      & rem files only, no dirs
for %%i in (a b c) do echo item %%i          & rem three iterations
```

### `FOR /D` — `execForDirs`

```go
func (ex *Executor) execForDirs(s *parser.ForStatement) int
```

[executor.go:1095](../executor/executor.go#L1095). Globs each item and keeps
only entries where `os.Stat(...).IsDir()` — the directory-only counterpart of
plain FOR. `!VAR!`/quote/backslash handling matches the list form.

### `FOR /R` — `execForRecursive`

```go
func (ex *Executor) execForRecursive(s *parser.ForStatement) int
```

[executor.go:1120](../executor/executor.go#L1120). Walks `RootPath` (default
`.`) with `filepath.Walk`; in each *directory* it globs the pattern joined to
that directory and runs the body per match. `shouldStop` returns
`filepath.SkipDir` to stop the walk.

### `FOR /F` — `execForTokens`, `parseForFOpts`, `parseTokenSpec`

`FOR /F` is the most intricate. Options are parsed by `parseForFOpts`
([executor.go:1246](../executor/executor.go#L1246)) into:

```go
type forFOpts struct {
	tokens   []int  // 1-based indices; -1 = wildcard (rest of line)
	delims   string
	eol      byte
	usebackq bool
}
```

Defaults: `tokens=[1]`, `delims=" \t"` (space+tab), `eol=';'`. The option string
is hand-scanned (not a plain split) so that **`delims=` can include a literal
trailing space** — the value of `delims=` extends to end-of-string *unless* a
space is followed by another known keyword (`tokens`/`eol`/`usebackq`/`skip`).
This faithfully handles `"delims= "` (a single space delimiter) and
`"tokens=2 delims=,"`. `usebackq` is a bare flag.

`parseTokenSpec` ([executor.go:1327](../executor/executor.go#L1327)) parses the
`tokens=` value into 1-based indices:

- comma lists: `1,3,5`
- **`M-N` ranges**: `1-4` → `1,2,3,4`; combined `2-3,7`. Capped at `maxToken`
  (128) to guard huge ranges.
- a trailing `*` appends `-1`, the **wildcard** marker (rest of line):
  `1,2*` → `[1,2,-1]`.
- Malformed/0/negative parts are skipped (never produce an out-of-range index);
  an empty result defaults to `[1]`.

`execForTokens` ([executor.go:1399](../executor/executor.go#L1399)) determines
the source lines:

- **`'command'` or `` `command` `` ** — execute and capture. The order is:
  strip a trailing `2>nul`/`>nul` (`stripNulRedirect`, so `sh` doesn't create a
  literal `nul` file), convert `\`→`/`. Then try `runDirCommand` (handles
  Windows `dir` internally via `builtins.DirList` — `sh` has no `dir`); then
  `runInternalCapture` (runs port-internal commands like `set`, `echo`, `type`,
  `findstr`, `ver`, `call`, `if`, `for` on a child executor with stdout
  captured — `sh`'s `set` is a different builtin and would capture nothing);
  finally fall back to `exec.Command("sh", "-c", cmdStr)`. Trailing `\r` is
  stripped from each captured line.
- **`"..."`** — with `usebackq` reads the named file; without it parses the
  literal string as one line.
- **bare filename** — reads the file (`\`→`/`), splitting on `\n` with trailing
  `\r` stripped.

Source delayed-expansion (`ExpandBangs`) is applied to the joined source first.

Per line, `execForTokens`:

1. **Skips blank lines entirely** (cmd.exe behavior).
2. **Skips EOL comment lines** — a line whose first byte equals `opts.eol`.
3. Splits on `delims` via `splitByDelims`
   ([executor.go:1362](../executor/executor.go#L1362), `FieldsFunc`, no empty
   tokens).
4. Assigns tokens to consecutive variables starting at `s.Variable` — `%%a`,
   `%%b`, `%%c` for `tokens=1,2,3`. A wildcard (`-1`) rejoins the remaining
   fields from the position after the previous token, joined by the first delim
   char. Out-of-range token indices set the variable to `""`.

The derived token variables are registered in `activeForVars` (offset by
character from the base, e.g. base `A` → `A`,`B`,`C`) so CALL's extra expansion
recognizes them ([executor.go:1483](../executor/executor.go#L1483)). A missing
loop variable prints `The syntax of the command is incorrect.` and returns 1.

```bat
rem split "alice:1000:/home" on : into three vars
for /f "tokens=1,3 delims=:" %%u in ("alice:1000:/home") do echo %%u lives in %%v
rem %%u=alice  %%v=/home   (tokens=1,3 → %%u,%%v)

rem capture command output
for /f "usebackq tokens=*" %%L in (`ver`) do echo got: %%L
```

### `runDirCommand`

```go
func runDirCommand(cmdStr string) (string, bool)
```

[executor.go:1387](../executor/executor.go#L1387). If the first word is `dir`,
delegates the rest to `builtins.DirList` (so the standalone `dir` builtin and
the FOR /F capture path agree) and returns the newline-joined output, e.g. for
`dir /A-D /S /B pattern`. Returns `ok=false` for anything else so the caller
falls through to `runInternalCapture`/`sh`.

## Pipes — `execPipe` / `runPipeStage`

```go
func (ex *Executor) execPipe(s *parser.PipeStatement) int
```

[executor.go:356](../executor/executor.go#L356). Stages run **sequentially with
buffering**, not as concurrent OS processes. Each stage's stdout is captured to
a `[]byte` and fed as the next stage's stdin. A single-command pipe degenerates
to a plain `execute`.

```go
func (ex *Executor) runPipeStage(stmt parser.Statement, input []byte, last bool) (int, []byte)
```

[executor.go:432](../executor/executor.go#L432). Each stage:

- swaps `os.Stdin`/`os.Stdout` to `os.Pipe()`s; a goroutine writes the previous
  `input` to the stdin pipe and closes it.
- runs the statement on a **CHILD executor** (`&Executor{env, positional}`)
  rather than `ex` itself. This is the key design choice: builtins (`TYPE`,
  `FINDSTR`, `ECHO`, `SET`, ...) work inside pipelines *and* flow control
  (`GOTO`/`EXIT`/abort) does **not leak** out of a pipe segment — matching
  cmd.exe, which runs pipe segments in child interpreters.
- the final (`last`) stage writes to the real stdout; non-final stages capture
  output via a reader goroutine.

`runInternalCapture` ([executor.go:390](../executor/executor.go#L390)) uses the
same child-executor + captured-stdout technique for FOR /F internal sources,
keyed off `internalForFSources` (`SET`/`ECHO`/`TYPE`/`FINDSTR`/`VER`/`CALL`/
`IF`/`FOR`) plus any registered builtin.

```bat
type log.txt | findstr /i error | sort
```

## Chains — `execChain`

```go
func (ex *Executor) execChain(s *parser.ChainStatement) int
```

[executor.go:483](../executor/executor.go#L483). Runs `Left`, and if
`shouldStop` is now set, returns immediately (flow control wins over the
chain). Otherwise on operator:

- `&` — always run `Right`.
- `&&` — run `Right` only if `leftCode == 0`.
- `||` — run `Right` only if `leftCode != 0`.

```bat
build.bat && echo OK || echo FAILED
```

## Blocks — `execBlock`

```go
func (ex *Executor) execBlock(s *parser.BlockStatement) int
```

[executor.go:507](../executor/executor.go#L507). Runs each statement of a
`( ... )` group in sequence, gating `env.ExitCode` per statement with
`updatesErrorlevel`, breaking on `shouldStop`. The return value is the last
statement's code.

## SETLOCAL / ENDLOCAL

```go
func (ex *Executor) execSetlocal(s *parser.SetlocalStatement) int
func (ex *Executor) execEndlocal() int
```

[executor.go:1537](../executor/executor.go#L1537). `SETLOCAL` calls
`env.Push()` (a new environment scope) and toggles `DelayedExpansion`
on/off per `EnableDelayedExpansion`/`DisableDelayedExpansion`. `ENDLOCAL` calls
`env.Pop()`; a pop with no matching push prints `ENDLOCAL without matching
SETLOCAL` to stderr. The scope stack itself lives in the [env](env.md) package;
note these statements are in `updatesErrorlevel`'s exempt set.

## EXIT — `/B` vs full process exit

```go
func (ex *Executor) execExit(s *parser.ExitStatement) int
```

[executor.go:1557](../executor/executor.go#L1557). The code comes from
`s.Code`, or from expanding/`Atoi`ing `s.CodeParts` if present (`exit /b
%errorlevel%`). Then:

- **`EXIT /B`** (`s.SubOnly`) sets `exitPending` and returns the code — unwinds
  the current file/subroutine only.
- **`EXIT`** (no `/B`) calls `os.Exit(code)` — terminates the entire process
  immediately.

## Redirection — `applyRedirects` and `cleanRedirectFile`

```go
func (ex *Executor) applyRedirects(redirects []parser.Redirect) func()
```

[executor.go:1850](../executor/executor.go#L1850). Used for **builtins** and for
the SET /P prompt and whole-CALL redirection: it temporarily reassigns the
process-global `os.Stdout`/`os.Stderr`/`os.Stdin` and returns a cleanup closure
that restores them and closes opened files. Supported ops:

| Op | Effect |
|----|--------|
| `>`, `1>` | truncate-create, stdout |
| `>>`, `1>>` | append-create, stdout |
| `2>` | truncate-create, stderr |
| `1>&2`, `>&2` | stdout → stderr |
| `2>&1` | stderr → stdout |
| `<` | open file as stdin |

(Note `applyRedirects` lacks `2>>` and treats `2>&1`/`1>&2` as simple
assignments. The **external-command** path in `runExternal`
([executor.go:1629](../executor/executor.go#L1629)) is the fuller table —
it additionally handles `2>>` and applies to the `exec.Cmd`'s own
`Stdin`/`Stdout`/`Stderr` fields rather than the globals.)

`cleanRedirectFile` ([executor.go:1834](../executor/executor.go#L1834))
normalizes every redirect target: strips quotes, expands `!VAR!` when delayed
expansion is on, converts `\`→`/` (after expansion), and maps a target named
`nul` (case-insensitively) to **`/dev/null`**.

`execEcho` ([executor.go:532](../executor/executor.go#L532)) does its *own*
redirect resolution (it opens the file and writes through `fmt.Fprint`) so that
`echo x >> file` and `echo.>> file` (a CRLF) append correctly, and `1>&2`
routes a single echo to stderr.

## SimpleCommand dispatch and `runExternal`

```go
func (ex *Executor) execSimple(s *parser.SimpleCommand) int
```

[executor.go:1575](../executor/executor.go#L1575). For a plain command:

1. Expand each arg and `strings.Trim` quotes.
2. Convert `\`→`/` in all args — **except for `FINDSTR`**, whose patterns use
   `\` as a regex escape (`\<`, `\>`); it normalizes its own file args
   internally.
3. **Builtin?** If the uppercased command is in `builtins.Registry`, apply
   redirects (so `>file echo text` works for builtins), call the builtin, clean
   up.
4. **`.bat` typed directly?** `resolveBat` (CWD then `PATH`, probing `.bat`/
   `.cmd`) runs it on a child Executor.
5. Otherwise **external** via `runExternal`.

```go
func (ex *Executor) runExternal(args []string, redirects []parser.Redirect) int
```

[executor.go:1617](../executor/executor.go#L1617). Builds an `exec.Command`,
wires `Stdin`/`Stdout`/`Stderr` to the process's, normalizes redirect files,
applies the fuller redirect table directly on the `exec.Cmd`, and runs it. On
an `*exec.ExitError` it returns the child's exit code; on any other error it
prints `'%s' is not recognized as an internal or external command.` and returns
1.

## Helpers

- `expandParts` ([executor.go:1906](../executor/executor.go#L1906)) → thin
  wrapper over `expander.ExpandWord(parts, env, positional)`.
- `splitArgs` ([executor.go:1912](../executor/executor.go#L1912)) → splits an
  expanded string on unquoted spaces, **preserving** quote characters in the
  output tokens (so downstream `%1` vs `%~1` distinctions survive).
- `stripOuterQuotes` ([executor.go:1940](../executor/executor.go#L1940)) →
  removes a single matched pair of surrounding double quotes.
- `resolveBat` ([executor.go:1784](../executor/executor.go#L1784)) → resolves a
  name to a `.bat`/`.cmd` file: strips drive letter, checks CWD then `PATH`
  (accepting both `;` and `:` separators, since BAT scripts may have set a
  Windows-style `PATH`).

## Edge cases and cmd.exe quirks reproduced (cheat sheet)

- Abort (`abortPending`) on missing CALL/GOTO label or malformed numeric `IF`
  terminates the whole batch file with `errorlevel 1`; it propagates up through
  subroutines.
- A numeric `IF` with an *unquoted* operand that expands to empty prints
  `<token> was unexpected at this time.` and aborts; a *quoted* empty operand
  (`""`) is a valid token and degrades to string comparison
  ([executor.go:687](../executor/executor.go#L687)).
- `IF` numeric comparison tries `Atoi` on both sides; if either is non-numeric
  it falls back to lexical string comparison for `EQU`/`NEQ`/`LSS`/.../`GEQ`.
- `GOTO :EOF` exits without `gotoPending`; a real label sets it.
- `SHIFT` inside an IF/FOR block persists to the enclosing positional scope
  (because `RunStmts(nil)` shares `positional`).
- Plain `FOR` globs files only; `FOR /D` globs dirs only; no-match wildcard =
  zero iterations (no literal fallback).
- `FOR /F "delims= "` preserves a literal trailing-space delimiter; blank and
  EOL-comment lines are skipped; `tokens=M-N` ranges and trailing `*` wildcard
  supported.
- `call set "y=%%x%%"` double-expands; `call set verb arg` (multi plain word)
  resolves a `set.bat` instead of the SET builtin.
- Pipes run buffered on child executors so builtins work and flow control can't
  leak across `|`.
- `nul`/`NUL` redirect target maps to `/dev/null`; Windows `dir`/internal
  commands in FOR /F `'...'` run through the port's engine, not `sh`.
- All interpreter-emitted lines end in `\r\n`.
