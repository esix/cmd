# Libraries and Standard-Library Usage

This document inventories every dependency the port relies on, and maps the Go
standard-library packages that do real work to the exact places they are used.
The design goal is deliberate: **one** external dependency (plus its single
transitive dependency), and the Go standard library for everything else — the
lexer, parser, expander, executor, and all builtins are pure stdlib.

## Go version

The module targets **Go 1.22** ([go.mod:3](../go.mod)):

```
module github.com/esix/cmd

go 1.22

require github.com/chzyer/readline v1.5.1

require golang.org/x/sys v0.0.0-20220310020820-b874c991c1a5 // indirect
```

One language/runtime feature the code relies on is the **auto-seeded global
`math/rand` source** introduced in Go 1.20: `%RANDOM%` calls `rand.Intn(32768)`
directly without ever seeding ([env/env.go:79](../env/env.go), comment at
[env/env.go:74](../env/env.go)). On Go versions before 1.20 every shell start
would produce the same sequence; on the targeted toolchain each process gets a
distinct seed automatically.

## Direct dependency: github.com/chzyer/readline v1.5.1

`readline` is the **only** direct third-party dependency. It provides the
interactive line editor for the REPL:

- **Line editing** — emacs-style key bindings, cursor movement, kill/yank.
- **Persistent history** — backed by a history file on disk.
- **Tab completion** — via a pluggable `AutoCompleter` interface.

It is used in exactly one package, `repl/`, and nowhere else. Non-interactive
execution (running a `.bat` file passed on the command line) goes straight
through `executor` and never touches `readline`.

### Where it is used: the REPL loop

[repl/repl.go:20](../repl/repl.go) constructs the editor and the main loop:

```go
rl, err := readline.NewEx(&readline.Config{
    Prompt:          "C:\\> ",
    HistoryFile:     histFile,
    AutoComplete:    newCompleter(),
    InterruptPrompt: "^C",
    EOFPrompt:       "exit",
})
```

The history file is the user's home-directory `.cmd_history`
([repl/repl.go:18](../repl/repl.go)).
The read loop maps `readline`'s two sentinel errors onto cmd.exe semantics
([repl/repl.go:36](../repl/repl.go)):

- `readline.ErrInterrupt` (Ctrl-C) — discard the current line and re-prompt;
  it does **not** exit the shell.
- `io.EOF` (Ctrl-D) — print a newline and break the loop, ending the session.

Each accepted line is trimmed, empty lines are skipped, and the rest is handed
to `executor.RunLine`, whose return value becomes the process `ERRORLEVEL`
(`e.ExitCode`).

### Where it is used: tab completion

[repl/completer.go](../repl/completer.go) implements the `AutoCompleter`. It is
built from `readline.NewPrefixCompleter` over a static list of BAT builtin
command names ([repl/completer.go:13](../repl/completer.go)) — each registered
in both upper- and lower-case — plus a dynamic file completer
(`readline.PcItemDynamic`) for arguments and the top level
([repl/completer.go:25](../repl/completer.go)).

`fileCompleter` ([repl/completer.go:44](../repl/completer.go)) reproduces
cmd.exe path cosmetics rather than Unix ones: it accepts a backslash-separated
prefix, translates it to forward slashes for `os.ReadDir`, matches
case-insensitively, then renders results **back to backslash separators** with a
trailing `\` appended to directories. This is the one place in completion where
the port deliberately re-creates the Windows look-and-feel.

## Indirect dependency: golang.org/x/sys

`golang.org/x/sys v0.0.0-20220310020820-b874c991c1a5` appears in `go.mod` marked
`// indirect` ([go.mod:7](../go.mod)). It is **not imported anywhere in this
repository** — it is pulled in transitively by `readline`, which uses it for
low-level terminal handling (putting the TTY into raw mode, querying window
size, reading key events) across platforms. It exists in the module graph only
because `readline` needs it; removing `readline` would remove `x/sys` as well.

`go.sum` ([go.sum](../go.sum)) also lists `github.com/chzyer/logex` and
`github.com/chzyer/test`, which are `readline`'s own build/test-time
dependencies; they contribute only `go.mod` hashes (no `h1:` module hash for
`logex`/`test` is required for our build) and are never compiled into the
binary.

## Minimal-dependency design

Everything other than the REPL's line editing is built on the standard library.
There is no argument-parsing framework, no regex engine beyond `regexp`, no
template engine, no third-party path library, and **no vendor directory**. The
lexer (`lexer/`), parser (`parser/`), variable expander (`expander/`),
environment model (`env/`), executor (`executor/`), and every builtin
(`executor/builtins/`) import only stdlib and sibling packages. This keeps the
dependency surface auditable and the build hermetic: a single `go build` with
the standard toolchain produces the shell.

See also: [Expansion](expansion.md), [Builtins](builtins.md),
[Executor](executor.md).

## Standard-library packages that do real work

The table below lists the stdlib packages carrying domain logic and the
principal sites where each is used. (Ubiquitous helpers `fmt`, `io`, `sort`, and
`encoding/hex` are covered in the notes after the table.)

| Package | Role | Key sites |
| --- | --- | --- |
| `os/exec` | External-process execution; running captured `sh -c` subcommands for `FOR /F` | [executor/executor.go:1620](../executor/executor.go), [executor/executor.go:1431](../executor/executor.go) |
| `os` | Filesystem I/O, stdio handles, in-process pipes, redirection files | throughout `executor/`, `builtins/`, `repl/`, `expander/` |
| `path/filepath` | Globbing, PATH search, path decomposition for `%~` tilde modifiers | [expander/expander.go](../expander/expander.go), [executor/executor.go](../executor/executor.go) |
| `regexp` | The `FINDSTR` regular-expression dialect | [executor/builtins/findstr.go](../executor/builtins/findstr.go) |
| `math/rand` | `%RANDOM%` dynamic variable | [env/env.go:79](../env/env.go) |
| `time` | `%DATE%` / `%TIME%` dynamic variables; `DATE`/`TIME` builtins; `%~t` timestamps | [env/env.go:90](../env/env.go), [executor/builtins/misc.go:61](../executor/builtins/misc.go) |
| `strconv` | `SET /A` arithmetic and numeric parsing (`IF` comparisons, `FOR /L`, tilde `%~z`) | [executor/builtins/set.go](../executor/builtins/set.go), [executor/executor.go](../executor/executor.go) |
| `bufio` | `SET /P` prompt input; `FINDSTR` stdin scanning | [executor/executor.go:629](../executor/executor.go), [executor/builtins/findstr.go:165](../executor/builtins/findstr.go) |
| `strings` | Token/argument manipulation everywhere (most-imported package) | every package |

### os/exec — external commands and captured subcommands

`os/exec` is imported in exactly one file, `executor/executor.go`, and used at
two distinct sites.

**1. Running external (non-builtin) commands.** `runExternal`
([executor/executor.go:1617](../executor/executor.go)) builds an
`exec.Command(args[0], args[1:]...)`, wires `Stdin`/`Stdout`/`Stderr` to the
process's own handles (or to redirect files), and runs it. Two cmd.exe-faithful
details:

- The program path has `\` translated to `/` first
  ([executor/executor.go:1619](../executor/executor.go)) so a Windows-style
  invocation resolves on a Unix filesystem.
- A failed exec is reported with the literal cmd.exe wording —
  `'%s' is not recognized as an internal or external command.` — and a non-zero
  exit. When the child *does* run but exits non-zero, the `*exec.ExitError`'s
  `ExitCode()` is propagated to `ERRORLEVEL` ([executor/executor.go:1663](../executor/executor.go)).

**2. Capturing output for `FOR /F ('command')`.** When the loop source is a
backquoted/quoted *command*, the executor tries, in order: an internal `dir`
handler (`runDirCommand`), the port's own internal-command capture
(`runInternalCapture`, for builtins like `set <prefix>` whose semantics differ
from `sh`'s), and only then falls back to `exec.Command("sh", "-c", cmdStr)`
([executor/executor.go:1431](../executor/executor.go)). Before shelling out, the
command string is massaged for Windows-isms: a trailing `2>nul`/`>nul` is
stripped so `sh` does not create a literal `nul` file, and `\` is converted to
`/` so `sh` does not treat separators as escapes
([executor/executor.go:1416](../executor/executor.go)).

This is the "dir/findstr-in-pipe handling" boundary: pipelines themselves do
**not** use `os/exec`. `execPipe`/`runPipeStage`
([executor/executor.go:432](../executor/executor.go)) run each segment in a
**child `Executor`** with `os.Stdin`/`os.Stdout` redirected through `os.Pipe`,
so a builtin like `FINDSTR` (or `dir`) on either side of a `|` reads and writes
the in-process pipe directly — never a `sh` subprocess. Because builtins must
work inside pipelines, a `dir` source for `FOR /F` is also handled internally by
delegating to `builtins.DirList` ([executor/executor.go:1392](../executor/executor.go)),
keeping the standalone `dir` builtin and the captured form in agreement.

### os and path/filepath — filesystem, globbing, path decomposition

`os` provides every filesystem and stdio primitive: `os.Stdin/Stdout/Stderr`,
`os.Create`/`os.OpenFile`/`os.Open` for redirections
([executor/executor.go:1632](../executor/executor.go)), `os.ReadFile` for
`TYPE`/`FINDSTR`/file-sourced `FOR /F`, `os.Pipe` for the in-process pipeline,
`os.ReadDir` for tab completion, and `os.Stat` for the file-info tilde
modifiers.

`path/filepath` carries three responsibilities:

- **Globbing.** Wildcard arguments are expanded with `filepath.Glob`
  ([executor/executor.go:1102](../executor/executor.go),
  [executor/executor.go:1211](../executor/executor.go)); recursive `dir /S`-style
  walking uses `filepath.Walk` with `filepath.SkipDir`
  ([executor/executor.go:1129](../executor/executor.go)).
- **PATH search.** The `PATH` environment value has `;` rewritten to the OS list
  separator before being split with `filepath.SplitList` and joined with
  `filepath.Join` ([executor/executor.go:1819](../executor/executor.go)).
- **Path decomposition for `%~` modifiers.** `applyTildeMods`
  ([expander/expander.go:71](../expander/expander.go)) implements `%~dpnxf`,
  `%~z`, `%~t`, `%~a` and friends using `filepath.Abs`, `filepath.IsAbs`,
  `filepath.Dir`, `filepath.Base`, and `filepath.Ext`. Crucially it first
  converts `\` to `/` ([expander/expander.go:80](../expander/expander.go)) so
  that on Unix — where `\` is **not** a separator — `..\..\x` segments still
  resolve. cmd.exe quirks reproduced here:

  ```bat
  rem %~dp0 always yields a fully-qualified path; the port resolves via filepath.Abs
  echo %~dp0
  rem %~d on Unix has no drive letter, so "d" emits "/" for an absolute path
  rem %~s (short 8.3 name) has no Unix equivalent, so it falls back to the full path
  echo %~zf %~tf
  ```

  `%~z` returns `info.Size()` as a decimal string
  ([expander/expander.go:97](../expander/expander.go)); `%~t` formats the mod
  time as `01/02/2006 03:04 PM` ([expander/expander.go:99](../expander/expander.go)).
  See [Expansion](expansion.md) for the full modifier grammar.

### regexp — the FINDSTR dialect

`regexp` is imported only by `executor/builtins/findstr.go`. FINDSTR's pattern
language is **not** Go's `regexp` syntax, so the builtin translates it:

- Literal/`/L` patterns and `/C:"..."` strings are escaped with
  `regexp.QuoteMeta` ([executor/builtins/findstr.go:102](../executor/builtins/findstr.go))
  so they match verbatim.
- Regex patterns are run through `findstrRegexToGo`, which converts the FINDSTR
  dialect — including the `\<` / `\>` word-boundary anchors — character by
  character, `regexp.QuoteMeta`-escaping anything that is not a recognized
  metacharacter ([executor/builtins/findstr.go:216](../executor/builtins/findstr.go),
  [executor/builtins/findstr.go:243](../executor/builtins/findstr.go)).
- Modifiers map onto Go regex constructs: `/X` (exact) wraps the pattern in
  `^(?:…)$`, `/B`/`/E` add `^`/`$`, and `/I` prepends `(?i)`
  ([executor/builtins/findstr.go:104](../executor/builtins/findstr.go)). Each
  pattern compiles to its own `regexp.Compile`; a compile failure prints
  `FINDSTR: bad search string` and returns exit code 2
  ([executor/builtins/findstr.go:117](../executor/builtins/findstr.go)).

```bat
rem FINDSTR word-boundary anchors are \< \> (not regexp's \b)
echo hello world | findstr "\<world\>"
rem /C: makes the search string a literal even if it contains regex metacharacters
type log.txt | findstr /C:"error (fatal)"
```

See [Builtins](builtins.md) for the complete FINDSTR flag set.

### math/rand — RANDOM

`%RANDOM%` is a *dynamic* pseudo-variable resolved in `env.Get`
([env/env.go:75](../env/env.go)): each read returns a fresh
`strconv.Itoa(rand.Intn(32768))`, i.e. an integer in `0..32767`, matching
cmd.exe's range. The cmd.exe quirk that an explicit `SET RANDOM=…` *shadows* the
dynamic value is honored — an assigned `RANDOM` in the variable map is returned
in preference to a new draw ([env/env.go:76](../env/env.go)).

### time — DATE and TIME

`time` backs both the dynamic `%DATE%`/`%TIME%` pseudo-variables and the
`DATE`/`TIME` builtins, in cmd.exe's default (US) formats:

- `%DATE%` → `now().Format("Mon 01/02/2006")` (weekday + `MM/DD/YYYY`)
  ([env/env.go:90](../env/env.go)).
- `%TIME%` → 24-hour, space-padded hour, centiseconds:
  `%2d:%02d:%02d.%02d` with `cs = t.Nanosecond() / 10_000_000`
  ([env/env.go:96](../env/env.go)).

Like `RANDOM`, an explicit `SET DATE=` / `SET TIME=` shadows the dynamic value.
The clock is read through a package-level `var now = time.Now`
([env/env.go:105](../env/env.go)) so tests can pin it. The `DATE` builtin prints
`time.Now().Format("Mon 01/02/2006")` ([executor/builtins/misc.go:61](../executor/builtins/misc.go)),
and `time` also underlies the `%~t` timestamp modifier above.

### strconv — SET /A and numeric parsing

`strconv` converts between the shell's all-strings model and integers wherever
arithmetic or numeric comparison is needed:

- **`SET /A`.** Results are stored with `strconv.Itoa`
  ([executor/builtins/set.go:91](../executor/builtins/set.go)); compound
  assignment operators (`+=`, `-=`, `*=`, `/=`, `%=`) read the current value via
  `strconv.Atoi` ([executor/builtins/set.go:72](../executor/builtins/set.go)).
  Hex literals are parsed with `strconv.ParseInt(…, 16, 64)`
  ([executor/builtins/set.go:372](../executor/builtins/set.go)). Note the
  cmd.exe-faithful guard: division/modulo by zero is **silently skipped** rather
  than erroring ([executor/builtins/set.go:81](../executor/builtins/set.go)).
- **`IF` numeric comparison.** When both operands parse as integers via
  `strconv.Atoi` they are compared numerically; otherwise the comparison falls
  back to strings ([executor/executor.go:709](../executor/executor.go)).
- **`FOR /L` and token ranges.** Loop bounds and `tokens=` ranges parse with
  `strconv.Atoi` ([executor/executor.go:1337](../executor/executor.go),
  [executor/executor.go:1347](../executor/executor.go)).

### bufio — SET /P and stdin

`bufio` reads user/piped input line-at-a-time:

- **`SET /P`.** A `bufio.NewReader(os.Stdin).ReadString('\n')` collects one line
  after printing the (whitespace-stripped) prompt
  ([executor/executor.go:629](../executor/executor.go)). cmd.exe quirks
  reproduced: the prompt is printed even with redirected stdin; an empty
  variable name (e.g. `<nul set /p "=text"`) is prompt-only and assigns nothing
  ([executor/executor.go:634](../executor/executor.go)); and on an EOF read
  with no input the target variable is **unset**
  ([executor/executor.go:637](../executor/executor.go)) — note real cmd.exe
  leaves the prior value unchanged here, a small divergence.
- **`FINDSTR` stdin (pipe) mode.** With no file arguments, FINDSTR filters
  stdin via a `bufio.NewScanner` whose buffer is grown to 16 MiB so very long
  lines do not error ([executor/builtins/findstr.go:165](../executor/builtins/findstr.go)).

### strings — everywhere

`strings` is the most-imported package (17 files). It does the constant
token/argument work the shell is made of: trimming, splitting, case folding for
the case-insensitive variable model (`strings.ToUpper` in
[env/env.go:67](../env/env.go)), and — pervasively — `strings.ReplaceAll(…, "\\",
"/")` to translate Windows separators before handing paths to the OS.

### Notes on the remaining helpers

- **`fmt`** — all formatted output and the verbatim cmd.exe error strings.
- **`io`** — `io.EOF` detection in the REPL ([repl/repl.go:40](../repl/repl.go))
  and `io.ReadAll` to drain a captured pipe stage
  ([executor/executor.go:462](../executor/executor.go)).
- **`sort`** — stable ordering of `SET` listings
  ([executor/builtins/set.go:24](../executor/builtins/set.go)) and `dir` output
  ([executor/builtins/misc.go:189](../executor/builtins/misc.go)).
- **`encoding/hex`** — the minimal `certutil -encodehex`/`-decodehex`
  implementation (`hex.EncodeToString` / `hex.DecodeString`,
  [executor/builtins/certutil.go:58](../executor/builtins/certutil.go)).
