# Architecture

`cmd` is a Go port of the Windows `cmd.exe`/BAT shell that runs on Unix. This
document is the entry-point overview: it describes the four-stage processing
pipeline, where each stage lives, the difference between file mode and
interactive mode, the statement dispatcher, and how a single line flows through
the system. Per-subsystem details live in the sibling docs linked throughout
([Lexer](lexer.md), [Parser](parser.md), [Expansion](expansion.md),
[Environment](environment.md), [Builtins](builtins.md)).

## Entry point

`main()` (`main.go:19`) creates one shared `*env.Env` and chooses a mode from
`os.Args`:

- **File mode** — `cmd script.bat [args...]`. Builds an `Executor` and calls
  `ex.RunFile(path, args)` (`main.go:27`), then exits with its return code.
- **Interactive mode** — no script argument. Runs `~/autoexec.bat` if present
  (each via a throwaway `Executor`, `main.go:38`), then hands the same `Env` to
  `repl.Run(e)` (`main.go:43`).

The `Env` is the single piece of state shared across everything: variables,
`%ERRORLEVEL%`, the `SETLOCAL` scope stack, and the `Echo`/`DelayedExpansion`/
`FileMode` flags (`env/env.go`). An `Executor` (`executor/executor.go:33`) is
cheap and is frequently created fresh for nested work — `CALL`, pipeline
stages, and `FOR /F` capture all spin up a child `Executor` that shares the
parent's `*env.Env` but gets its own program counter and statement/line slices.

## The four-stage pipeline

Every logical line is run through four stages, **in this fixed order**:

1. **Early percent-expansion** — `expander.ExpandPercent` rewrites `%VAR%`,
   `%N`, and `%~mods N` in the *raw line text*, before any tokenizing.
2. **Lex / tokenize** — `lexer.TokenizeWithOpts` splits the expanded line into
   `WORD` / `REDIRECTION` / `PIPE` tokens.
3. **Parse to AST** — `parser.Parse` turns the tokens into a `[]Statement`.
4. **Execute** — `Executor.execute` switches over the AST node types and runs
   each statement.

Stages 2 and 3 are bundled in `parser.ParseLineWithOpts(line, delayed)`
(`parser/parser.go`), which tokenizes then parses; the executor calls that
single helper. So in the code the pipeline reads as two calls per line:

```go
expanded := expander.ExpandPercent(sl.raw, ex.env, ex.positional) // stage 1
stmts, err := parser.ParseLineWithOpts(expanded, ex.env.DelayedExpansion) // stages 2+3
// ... stage 4:
for _, stmt := range stmts { code = ex.execute(stmt) }
```

(`executor/executor.go:231`)

### Why percent-expansion happens before tokenizing

This is a deliberate `cmd.exe` fidelity choice, documented at
`expander/early.go:11`. Real `cmd.exe` substitutes `%VAR%` into the command
text first and then re-parses the result, which means an expanded value can
change the *token structure* of the line — variables can contain operators,
redirections, quotes, or whole command fragments. Expanding before the lexer
runs reproduces that:

```bat
set CMD=echo hello ^& echo world
%CMD%
```

After stage 1 the line is literally `echo hello & echo world`, so the lexer
sees two commands joined by `&`. If we tokenized first, `%CMD%` would be one
opaque word and the `&` inside it would never become an operator.

The two expansion kinds are split across stages on purpose:

- **`%VAR%` (immediate)** is resolved at stage 1, before lexing — its value is
  baked into the line that gets parsed.
- **`!VAR!` (delayed)** is *not* touched by `ExpandPercent` (see the comment at
  `expander/early.go:13`). It survives into the AST as literal text/`DelayedVarPart`
  and is expanded at stage 4, per-iteration, when `SETLOCAL EnableDelayedExpansion`
  is on. That is the whole point of delayed expansion: re-evaluating inside a
  `FOR`/`IF` body after the variable has changed.

`%%X` FOR-variable references are likewise preserved verbatim through stage 1
(`expander/early.go:25`) because the loop value isn't known until the loop runs;
they are resolved during `FOR` execution. See [Expansion](expansion.md) for the
full ruleset (`%%~nf` tilde modifiers, `%%` collapse, `SET /A` modulo `%%`,
etc.).

## File mode vs. interactive mode

The two modes diverge in **when parsing happens** and **how flow control is
unwound**, but converge on the same `execute` dispatcher.

### File mode: `RunFile` → `runLines` (lazy per-line parse)

`RunFile` (`executor/executor.go:123`) reads the whole `.bat` file once and
preprocesses it into a `[]scriptLine` plus a label index, then delegates to
`runLines`. Preprocessing is purely structural — it does **not** parse:

- Windows `\` → `/` and a drive-letter prefix is stripped from the path
  (`C:/foo` → `/foo`, `executor/executor.go:125`).
- Caret line-continuations are joined first
  (`joinCaretContinuations`, `executor/executor.go:1705`): a line ending in an
  *odd* number of `^` is glued to the next physical line.
- Each line is classified: blank lines, `#!` shebangs, `::` comments, and
  `REM`/`REM ...` lines are dropped; `:LABEL` lines become `scriptLine{label}`
  (only the first word is the label, the rest is comment); everything else is a
  `scriptLine{raw}` (`executor/executor.go:152`).
- Leading `@` (echo suppression) is stripped here.
- Multi-line parenthesized blocks are folded into one logical line by
  `joinBlocks` (`executor/executor.go:1734`), which counts unquoted parens
  (`echo(` is *not* a block opener, `executor/executor.go:1688`) and joins
  continuation lines with a `\x01` boundary marker the parser understands as a
  statement separator that must not be swallowed into an `IF`/`FOR` body.

The result is cached per absolute path in `fileCache` (`executor/executor.go:119`)
so repeated `CALL`s of the same file skip re-reading and re-preprocessing.

The key design point is in `runLines` (`executor/executor.go:209`): each line
is `ExpandPercent`-ed and `ParseLineWithOpts`-ed **lazily, one at a time, in the
loop body** (`executor/executor.go:231`). Parsing is *not* done up front for the
whole file. This is what makes `SETLOCAL EnableDelayedExpansion` (and `ECHO`
state) take effect for *subsequent* lines: line N can change
`ex.env.DelayedExpansion`, and line N+1 is then tokenized/parsed with the new
setting because its parse hasn't happened yet:

```bat
@echo off
set x=before
setlocal EnableDelayedExpansion
set x=after
echo !x!        REM parsed with delayed expansion ON -> prints "after"
```

`runLines` drives a program counter `ex.pc` over the lines. After each statement
it updates `%ERRORLEVEL%` (but only if `updatesErrorlevel(stmt)` says the
statement type actually touches it — builtins like `ECHO`/`SET`/`IF`/`FOR` do
not in real `cmd.exe`, `executor/executor.go:65`) and then checks the three
flow-control flags (`executor/executor.go:242`):

- `abortPending` — a missing `CALL`/`GOTO` label or a malformed `IF` aborts the
  whole batch file; `pc` jumps past the end and the file returns errorlevel 1.
- `gotoPending` — `GOTO` already moved `pc` via the label index; break out of
  the statement loop so the main loop resumes at the new line.
- `exitPending` — `EXIT /B`; `pc` jumps past the end.

### Interactive mode: `RunLine`

`repl.Run` (`repl/repl.go`) reads lines with `readline` and calls
`ex.RunLine(line)` once per line (`repl/repl.go:50`). `RunLine`
(`executor/executor.go:81`) is the *eager* path: it strips a leading `@`,
special-cases `ECHO.`/`ECHO.text` (handled before tokenizing,
`executor/executor.go:93`), runs stage 1 + stages 2/3 immediately, then calls
`RunStmts` on the resulting slice. There is no line index and no lazy parse —
there is only the current line.

`RunLine` clears `abortPending` at the top (`executor/executor.go:84`): a bad
`GOTO` at the prompt aborts that *command*, but the REPL itself never
terminates.

### `RunStmts`: the shared statement driver

Both modes can land in `RunStmts` (`executor/executor.go:276`), which runs a
pre-parsed `[]Statement` with `GOTO` support over `ex.stmts`. It is used for
interactive lines and, recursively, for `IF`/`FOR` bodies. Two subtle
behaviours it preserves:

- **`positional` sharing.** When called with `positional == nil` (the
  `IF`/`FOR`-body case) it shares the parent's positional args, so a `SHIFT`
  inside the block persists to the enclosing scope — matching `cmd.exe`. A fresh
  slice (e.g. from `CALL`) overrides them and is restored on return
  (`executor/executor.go:283`).
- **`pc` restore.** It restores the saved `pc` only when no `GOTO`/`EXIT /B` is
  pending (`executor/executor.go:305`).

## Statement dispatch: `execute()`

`execute` (`executor/executor.go:316`) is a type switch over the parser's AST
node types — the central jump table of the interpreter. Each case delegates to
an `exec*` method:

| AST node | Handler | Notes |
|---|---|---|
| `*EchoStatement` | `execEcho` | `ECHO ON/OFF`, blank-line `ECHO.`, redirections, `!VAR!`/`%%X` re-expansion |
| `*SetStatement` | `execSet` | plain/`/A` arithmetic/`/P` prompt; bare `set` lists vars |
| `*IfStatement` | `execIf` → `evalCondition` | string/numeric/`DEFINED`/`EXIST`/`ERRORLEVEL` |
| `*GotoStatement` | `execGoto` | `:EOF`, label-index lookup, missing-label abort |
| `*CallStatement` | `execCall` | `:label` subroutine, `call <builtin>`, `CALL script.bat` |
| `*ForStatement` | `execFor` | dispatches on `ForKind`: in-list / `/L` range / `/F` tokens / `/D` dirs / `/R` recursive |
| `*ExitStatement` | `execExit` | `EXIT` (`os.Exit`) vs `EXIT /B` (`exitPending`) |
| `*ShiftStatement` | `execShift` | drops `positional[0]` |
| `*PipeStatement` | `execPipe` | sequential buffered stages on child executors |
| `*ChainStatement` | `execChain` | `&`, `&&`, `\|\|` |
| `*BlockStatement` | `execBlock` | `( a & b & c )` |
| `*SetlocalStatement` | `execSetlocal` | `env.Push()`, toggles `DelayedExpansion` |
| `*EndlocalStatement` | `execEndlocal` | `env.Pop()` |
| `*LabelStatement` | (inline) | no-op, returns 0 |
| `*SimpleCommand` | `execSimple` | builtin → `.bat` resolution → external command |
| *default* | (inline) | prints `unknown statement type: %T`, returns 1 |

A few dispatch behaviours worth noting for maintainers:

- **Pipelines and `FOR /F` capture run on child executors.** `execPipe`
  (`executor/executor.go:356`) runs each stage through `runPipeStage`
  (`executor/executor.go:432`), which swaps `os.Stdin`/`os.Stdout` to OS pipes
  and runs the stage on a `sub := &Executor{env: ex.env, ...}`. This makes
  builtins (`TYPE`, `FINDSTR`, `ECHO`, `SET`, …) usable in pipelines and stops
  flow control (`GOTO`/`EXIT`/abort) from leaking out of a stage — `cmd.exe`
  runs pipe segments in child interpreters. `FOR /F ('cmd')` uses the same
  capture trick via `runInternalCapture` for its `internalForFSources`
  (`executor/executor.go:381`), because `sh`'s `set`/`echo` would behave
  differently.
- **`SimpleCommand` resolution order** (`execSimple`, `executor/executor.go:1575`):
  builtin in `builtins.Registry` → `.bat`/`.cmd` resolved via `resolveBat`
  (CWD then `PATH`, accepting both `;` and `:` separators,
  `executor/executor.go:1784`) → external process via `runExternal`
  (`executor/executor.go:1617`). Backslashes in args are converted to `/`
  *except* for `FINDSTR`, whose patterns use `\` as a regex escape.
- **`CALL`'s double-expansion idiom.** `execCall` (`executor/executor.go:837`)
  dequotes `%~0`-style, splits positional args respecting quotes, and for
  `call <builtin>` runs one extra expansion round (`expandCallText`,
  `executor/executor.go:1035`) so `call set "y=%%x%%"` collapses `%%`→`%`,
  applies delayed `!VAR!`, then a second `%VAR%` pass — the classic batch
  trick. `call set <verb> ...` with multiple plain words is deliberately *not*
  routed to the builtin (`executor/executor.go:1006`) so it can find a
  `set.bat` on `PATH`.

## Package map

| Path | Responsibility | Key types / functions |
|---|---|---|
| `main.go` | Entry point; mode selection | `main` |
| `repl/` | Interactive Read-Eval-Print loop (readline, history, completer) | `Run`, `newCompleter` |
| `executor/` | Runs parsed statements; the four-stage driver and dispatcher | `Executor`, `RunFile`, `runLines`, `RunLine`, `RunStmts`, `execute`, `scriptLine`/`scriptFile` |
| `executor/builtins/` | Internal commands (`ECHO`, `SET`, `DIR`, `TYPE`, `FINDSTR`, `COPY`, …) | `Registry map[string]Func`, `Func` |
| `expander/` | Variable resolution (`%VAR%`, `%N`, `%~mods`, `!VAR!`, `%%X`) | `ExpandPercent` (early), `ExpandWord`, `ExpandArgs`, `ExpandName`, `ExpandForVars`, `ExpandBangs` |
| `lexer/` | Tokenizes one logical line | `Tokenize`/`TokenizeWithOpts`, `Token`, `Kind` (`WORD`/`REDIRECTION`/`PIPE`) |
| `parser/` | Token stream → AST | `Parse`, `ParseLine`/`ParseLineWithOpts`, `Statement` & `Condition` & `WordPart` node types, `ForKind` |
| `env/` | Variable store + `SETLOCAL` scope stack + dynamic vars | `Env`, `New`, `Get`/`Set`/`Unset`, `Push`/`Pop`, `All`, `DisplayName` |
| `internal/util/` | Windows↔Unix path conversion | `ToUnix` |

Notes for maintainers:

- **Case-insensitivity** lives in `env`: keys are stored uppercase, with a
  parallel `names` map preserving the original-case name used at creation
  (`env/env.go`, `DisplayName`).
- **`SETLOCAL`/`ENDLOCAL`** is a scope stack: `Push` snapshots the full
  environment, `Pop` restores it (`env/env.go:134`).
- **Builtins are pure functions** of the form `func(args []string, e *env.Env) int`
  (`executor/builtins/builtins.go`); the executor applies redirections around
  them via `applyRedirects` (`executor/executor.go:1850`), which temporarily
  swaps the `os.Std*` files and returns a cleanup closure.

## Data flow of one line

The diagram below traces a single logical line in **file mode** (interactive
mode is the same minus the line index / lazy loop — `RunLine` does stages 1–4
eagerly on one line). Stage numbers match the four-stage pipeline above.

```
                 .bat file bytes
                        |
                        v
   +--------------------------------------------------+
   | RunFile (executor.go:123)                        |
   |  - path normalize (C:\ -> /, \ -> /)             |
   |  - joinCaretContinuations  (^ at EOL)            |   PREPROCESS
   |  - classify: drop blank/REM/::/#!, index :LABELs |   (once, cached)
   |  - joinBlocks  ( ... ) -> one line via \x01      |
   +--------------------------------------------------+
                        |
                        v   []scriptLine + labelIdx
   +--------------------------------------------------+
   | runLines loop (executor.go:222)                  |
   |   for each line at pc:                            |
   +--------------------------------------------------+
                        | sl.raw
                        v
   [1] expander.ExpandPercent (early.go:14)
        %VAR% / %N / %~mods  ->  substituted into text
        ( !VAR! and %%X preserved verbatim )
                        |
                        v   expanded line string
   [2] lexer.TokenizeWithOpts (lexer.go)
        text  ->  [WORD, REDIRECTION, PIPE, ...]
                        |
                        v   []Token
   [3] parser.Parse (parser.go)
        tokens  ->  []Statement (AST)
                        |
                        v   []parser.Statement
   [4] executor.execute (executor.go:316)   <-- type switch
        |          |           |            |
        v          v           v            v
   exec* methods (execEcho / execSet / execIf / execFor / ...)
        |                                   |
        |  builtins.Registry  <-- SimpleCommand: builtin?
        |  resolveBat (.bat/.cmd)           |
        |  runExternal (sh / exec.Command)  |
        |                                   |
        v                                   v
   delayed !VAR! / %%X expansion        os.Stdout / files
   (ExpandWord, ExpandName, ExpandBangs)    (via applyRedirects)
                        |
                        v
   update %ERRORLEVEL% (if updatesErrorlevel)
   check abortPending / gotoPending / exitPending
                        |
                        v
                advance pc (or jump)
```

The shared `*env.Env` threads through every stage: `ExpandPercent` reads it for
`%VAR%`, the parser/lexer read `DelayedExpansion` to decide how to treat `!`,
and `execute` both reads it (conditions, expansion) and mutates it (`SET`,
`SETLOCAL`, `%ERRORLEVEL%`). See [Expansion](expansion.md) for the expansion
rules, [Environment](environment.md) for the variable store and scope stack, and
[Builtins](builtins.md) for the internal command set.
