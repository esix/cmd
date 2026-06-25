# Implementation Documentation

Developer documentation for **cmd** — a Windows BAT/CMD shell implemented in Go
for Unix and Linux. These documents describe how the interpreter is built: the
processing pipeline, each subsystem, the built-in commands, and the cmd.exe
behaviors (and quirks) it reproduces.

For *using* the shell, see the top-level [README](../README.md). Start here if
you want to understand, maintain, or extend the implementation.

## Start here

- **[Architecture](architecture.md)** — the big picture: the four-stage line
  pipeline (early `%VAR%` expansion → lex → parse → execute), file mode vs.
  interactive mode, the `execute()` dispatch, a package map, and a data-flow
  diagram. Read this first.

## The processing pipeline

A BAT line flows through four stages, each documented in order:

1. **[Expansion](expansion.md)** — the two-phase variable model: early
   `%VAR%` / `%~modsN` expansion on the raw line (before tokenizing), and
   delayed `!VAR!` resolution at run time. Covers tilde modifiers, substrings,
   string replacement, and the `!VAR=!` quirk.
2. **[Lexer](lexer.md)** — tokenizing a single line: token kinds, operators,
   the `0x01` block marker, word/quote/caret rules, every `%`- and `!`-variable
   form, and redirection operators.
3. **[Parser](parser.md)** — the AST node types and the recursive-descent
   parser that turns tokens into `Statement`s, including every per-keyword
   parser and how a block body avoids swallowing following statements.
4. **[Executor](executor.md)** — running the AST: the program-counter loop,
   `GOTO`/`CALL`/`FOR`/`IF` control flow, abort semantics, pipes, chains,
   redirection, and CRLF-faithful I/O.

## Subsystems

- **[Built-in commands](builtins.md)** — reference for every command in the
  registry (`ECHO`, `SET`, `IF`-family helpers, `DIR`, `TYPE`, `FINDSTR`,
  `CERTUTIL`, `CMD`, file ops, …): flags, behavior, exit codes, and quirks.
- **[Environment](environment.md)** — the variable store: case-insensitive
  variables with original-case display, the dynamic pseudo-variables
  (`%ERRORLEVEL%`, `%RANDOM%`, `%DATE%`, `%TIME%`), and the `SETLOCAL`/`ENDLOCAL`
  snapshot stack.
- **[Interactive shell](repl.md)** — the REPL: `~/autoexec.bat` startup,
  readline configuration, tab completion, and how interactive execution differs
  from `.bat` file mode.

## Reference

- **[cmd.exe compatibility](cmd-compatibility.md)** — the authoritative
  fidelity reference: a supported-feature matrix, the cmd.exe quirks the port
  deliberately reproduces (each with a rationale), and the Unix-specific
  divergences and limitations.
- **[Libraries](libraries.md)** — external dependencies (`chzyer/readline` and
  the indirect `golang.org/x/sys`) and where each standard-library package does
  real work. The design is otherwise standard-library only.

## Source layout

| Path | Doc | Responsibility |
|------|-----|----------------|
| `main.go` | [Architecture](architecture.md) | Entry point; file vs. interactive mode; `autoexec.bat` |
| `lexer/` | [Lexer](lexer.md) | Tokenizer (`token.go`, `lexer.go`) |
| `parser/` | [Parser](parser.md) | AST (`ast.go`) + recursive-descent parser (`parser.go`) |
| `expander/` | [Expansion](expansion.md) | `%VAR%`/`!VAR!`/`%%`/tilde resolution (`early.go`, `expander.go`) |
| `executor/` | [Executor](executor.md) | Statement execution, control flow, pipes, redirection |
| `executor/builtins/` | [Builtins](builtins.md) | `ECHO`, `SET`, `DIR`, `FINDSTR`, … |
| `env/` | [Environment](environment.md) | Variable store + `SETLOCAL` scope stack |
| `repl/` | [REPL](repl.md) | Interactive loop, readline, tab completion |
| `internal/util/` | [Architecture](architecture.md) | Windows→Unix path helpers |
