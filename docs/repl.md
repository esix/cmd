# Interactive Shell and Startup

This document covers the interactive REPL — how `cmd` launches with no arguments,
runs `~/autoexec.bat`, and drives a [readline](https://github.com/chzyer/readline)
prompt where each typed line is handed to the executor. It also documents tab
completion (`completer.go`) and the (subtle) ways interactive execution differs
from running a `.bat` file.

The two relevant source files are tiny — [`repl/repl.go`](../repl/repl.go) (the
Run loop) and [`repl/completer.go`](../repl/completer.go) (tab completion) — plus
the startup glue in [`main.go`](../main.go). The interesting behavior actually
lives one layer down in the executor, so this doc cross-references
[`executor.RunLine`](../executor/executor.go) heavily.

## Startup and dispatch (`main.go`)

`main()` decides between *file mode* and *interactive mode* purely by argument
count (`main.go:22`):

```go
e := env.New()

if len(os.Args) >= 2 {
    // File mode: cmd script.bat [args...]
    path := os.Args[1]
    args := os.Args[2:]
    ex := executor.New(e)
    code := ex.RunFile(path, args)
    os.Exit(code)
}
```

So `cmd script.bat a b c` runs `RunFile` with `path="script.bat"`,
`args=["a","b","c"]` and exits with that script's exit code. Any first argument
selects file mode — there is no flag parsing, no `/c` / `/k` switch handling.

With **zero** extra arguments, `main` prints a banner and enters interactive
mode (`main.go:31`):

```go
fmt.Println("cmd — BAT shell for Unix. Type EXIT to quit.")
```

A fresh `*env.Env` is shared across autoexec and the REPL, so variables set in
`autoexec.bat` (and any `SET` it performs, `ECHO OFF`, etc.) persist into the
interactive session. `env.New()` (`env/env.go:36`) seeds the environment from
the host process's `os.Environ()` and maps `TEMP`/`TMP` to `$TMPDIR` (or
`/tmp`), so those are visible at the prompt.

### `~/autoexec.bat`

Before the REPL starts, `main` runs `~/autoexec.bat` *if it exists*
(`main.go:34`):

```go
if home, err := os.UserHomeDir(); err == nil {
    autoexec := filepath.Join(home, "autoexec.bat")
    if _, err := os.Stat(autoexec); err == nil {
        ex := executor.New(e)
        ex.RunFile(autoexec, nil)
    }
}
```

Notes and edge cases:

- The path is **always** `$HOME/autoexec.bat` (via `os.UserHomeDir`). It is not
  taken from any environment variable, the cwd, or a config flag.
- It is run with `RunFile(..., nil)` — i.e. as a `.bat` file in *file mode* with
  **no positional arguments**, so `%0` is the autoexec path and `%1`.. are empty.
- Its exit code is discarded (`RunFile`'s return is ignored). An autoexec that
  ends in `EXIT /B` returns control and the REPL still starts, but a plain
  `EXIT` (with or without a code, e.g. `EXIT 0`) calls `os.Exit` and terminates
  `cmd` before the prompt appears — see [EXIT](#exit-os-exit-vs-exit-b) below.
- A *separate* `executor.New(e)` is created for autoexec, distinct from the one
  the REPL builds. They share the same `*env.Env`, so environment state carries
  over but per-executor state (program counter, FOR-var stack) does not — which
  is fine since autoexec has fully returned before the REPL executor is created.
- If `$HOME` is unset or `os.Stat` fails (file absent), startup silently skips
  autoexec. There is no error if the file is missing.

Example `~/autoexec.bat`:

```bat
@ECHO OFF
REM Runs once when `cmd` starts interactively.
SET PROMPT_NAME=dev
SET PATH=%PATH%;/usr/local/bin
ECHO Welcome back, %USERNAME%.
```

Because the REPL shares the env, after this autoexec the prompt session sees
`%PROMPT_NAME%` set to `dev` and the augmented `%PATH%`.

## The Run loop (`repl/repl.go`)

`repl.Run(e *env.Env)` (`repl/repl.go:17`) owns the readline session and the
read-eval loop.

### readline configuration

```go
histFile := ""
if home, err := os.UserHomeDir(); err == nil && home != "" {
    histFile = filepath.Join(home, ".cmd_history")
}

rl, err := readline.NewEx(&readline.Config{
    Prompt:          "C:\\> ",
    HistoryFile:     histFile,
    AutoComplete:    newCompleter(),
    InterruptPrompt: "^C",
    EOFPrompt:       "exit",
})
```

- **Prompt** is the fixed literal `C:\> ` (`Prompt: "C:\\> "`). It is *not*
  derived from `%PROMPT%`, the current directory, or any env var — it never
  changes during the session.
- **History file** is `.cmd_history` in `os.UserHomeDir()` (`repl/repl.go:18`).
  readline loads prior history from it on startup and appends accepted lines,
  persisting command history across `cmd` invocations. If the home directory
  cannot be resolved, no history file is configured.
- **AutoComplete** is the prefix completer from `newCompleter()` —
  see [Tab completion](#tab-completion-completergo).
- **InterruptPrompt** / **EOFPrompt** are readline's display strings for Ctrl-C
  and Ctrl-D; they are cosmetic (what readline echoes), not control flow.

If `readline.NewEx` fails, `Run` prints `readline init error: …` to stderr and
calls `os.Exit(1)` (`repl/repl.go:28`). On success `defer rl.Close()` restores
the terminal on return.

The REPL executor is built once, after readline init:
`ex := executor.New(e)` (`repl/repl.go:33`).

### The loop body

```go
for {
    line, err := rl.Readline()
    if err == readline.ErrInterrupt {
        continue
    }
    if err == io.EOF {
        fmt.Println()
        break
    }

    line = strings.TrimSpace(line)
    if line == "" {
        continue
    }

    code := ex.RunLine(line)
    e.ExitCode = code
}
```

Behavior, line by line:

- **Ctrl-C (`readline.ErrInterrupt`)** discards the current input line and loops
  back to a fresh prompt (`continue`). It does **not** exit the shell and does
  not abort a running command — it only cancels line editing. Once a line has
  been accepted and `RunLine` is executing, Ctrl-C is not intercepted here.
- **Ctrl-D / EOF (`io.EOF`)** prints a newline and breaks the loop, returning
  from `Run` (and thus from `main`, exiting the process). This is the graceful
  end-of-input exit. Note that `EOFPrompt: "exit"` only controls the text
  readline shows; the actual termination is this `break`.
- The accepted line is **`strings.TrimSpace`d**, and **empty lines are skipped**
  (`continue`) without invoking the executor. Leading/trailing whitespace is
  stripped before the line reaches `RunLine` — so an indented command at the
  prompt is dedented first.
- Every non-empty line is executed by **`ex.RunLine(line)`**, and its return
  value is stored into `e.ExitCode` so the next line can branch on
  `%ERRORLEVEL%` / `IF ERRORLEVEL n`. (`env.Get("ERRORLEVEL")` returns
  `e.ExitCode`, see `env/env.go:69`.) Because the same executor and env persist
  across iterations, `SET`, `SETLOCAL`, `CD`, `ECHO OFF`, delayed-expansion
  toggles, etc. all carry over from one prompt line to the next.

There is no other error handling in the loop: `RunLine` reports its own parse
errors to stderr and returns a code; `Run` just records it.

## How a line is executed: `RunLine`

Each prompt line goes through `executor.RunLine` (`executor/executor.go:81`),
which is the interactive counterpart to `RunFile`'s per-line processing. The
order of operations is important and matches real cmd.exe's
"expand `%` *before* tokenizing" model:

1. **Clear any pending abort** (`executor/executor.go:84`):

   ```go
   ex.abortPending = false
   ```

   This is the key interactive-vs-file difference: a prior command may have set
   `abortPending` (e.g. a missing `GOTO` label, a malformed `IF`). In a script
   that flag terminates the whole file; at the prompt it must be reset every
   line so **the REPL itself never terminates** on a bad command. See
   [Interactive vs file mode](#interactive-vs-file-mode).

2. **Strip a leading `@`** (echo suppression marker). At the prompt this is
   largely vestigial since the REPL never echoes the command back regardless,
   but `@` is consumed so it does not reach the tokenizer.

3. **Special-case `ECHO.`** — the blank-line form is handled before tokenizing
   (`executor/executor.go:93`), emitting CRLF.

4. **Early `%`-expansion**: `line = expander.ExpandPercent(line, ex.env,
   ex.positional)` (`executor/executor.go:102`). This resolves `%VAR%`, `%N`,
   `%~mods N`, substring/replace forms — *before* the parser runs. `%%X` is
   preserved verbatim for the parser (FOR variables); see
   [`%%I` vs `%I`](#i-vs-i-for-variables-at-the-prompt).

5. **Parse**: `parser.ParseLineWithOpts(line, ex.env.DelayedExpansion)`. Parse
   errors print `Parse error: …` to stderr and return `1` (the REPL keeps
   going).

6. **Execute**: `return ex.RunStmts(stmts, nil)` (`executor/executor.go:113`).
   Passing `nil` positional means the line shares the executor's existing
   positional args rather than overriding them.

At the prompt `ex.positional` is whatever the REPL executor was last left with
(initially `nil`, since `repl.Run` builds a bare `executor.New(e)`), so `%1`..
`%9` and `%~..N` generally expand to empty interactively unless something set
them — these are normally script-argument constructs.

## Interactive vs file mode

The executor distinguishes the two paths in a handful of deliberate ways. Note
that `env.FileMode` (`env/env.go:23`) is set to `true` by `runLines`
(`executor/executor.go:210`) but is **not read anywhere else** in the codebase —
the `%%` vs `%` distinction is enforced structurally in `ExpandPercent`/the
parser identically in both modes, not by branching on `FileMode`. The real
observable differences are:

### Abort / terminate semantics at the prompt

In a `.bat` file, a missing `CALL`/`GOTO` label or a malformed `IF` sets
`abortPending`, and `runLines` reacts by jumping the program counter past the
end of the file and returning errorlevel `1` (`executor/executor.go:242`):

```go
if ex.abortPending {
    // cmd.exe terminates the current batch file on a missing
    // CALL/GOTO label or a malformed IF; control returns to the
    // caller with errorlevel 1.
    ex.abortPending = false
    ex.pc = len(ex.lines)
    code = 1
    ex.env.ExitCode = 1
    break
}
```

At the prompt there is no "rest of the file" to abort. `RunLine` clears
`abortPending` at the *start* of every line (`executor/executor.go:84`), so the
same error condition simply ends that one line's execution (via `shouldStop()`
in `RunStmts`, `executor/executor.go:299`) and the loop reads the next command.
A typo'd `GOTO :nowhere` aborts the line but leaves you at a live prompt; the
shell does not die.

### EXIT: `os.Exit` vs `EXIT /B`

`execExit` (`executor/executor.go:1557`) branches on whether `/B` was given:

```go
if s.SubOnly { // EXIT /B
    ex.exitPending = true
    return code
}
os.Exit(code)
```

- **`EXIT`** (no `/B`) calls `os.Exit(code)` — it terminates the whole `cmd`
  process immediately, from the prompt or from a script. This is the documented
  "Type EXIT to quit" path from the banner.
- **`EXIT /B [code]`** sets `exitPending`, which unwinds the current
  `RunStmts`/`runLines` frame. At the prompt that just ends the current line.

So at the interactive prompt, `EXIT` quits the shell, while `EXIT /B 3` merely
sets `%ERRORLEVEL%` to 3 for the next prompt line and otherwise does nothing
visible.

### Line echoing

`RunFile` honors `ECHO ON` by echoing each script line as it runs; `RunLine`
deliberately does **not** echo the typed command (the user already sees it as
they typed it). The dead `if ex.env.Echo && echoLine …` block in `RunLine`
(`executor/executor.go:109`) is a no-op placeholder documenting this.

### `%%I` vs `%I` (FOR variables at the prompt)

Real cmd.exe requires doubled `%%` for FOR variables inside `.bat` files but
single `%` when typed at the interactive prompt. **This port does not implement
that distinction** — it expects doubled `%%I` in *both* modes. The mechanism:

- In `ExpandPercent` (`expander/early.go:30`), a `%%` followed by a single
  letter not followed by an alphanumeric is preserved verbatim (`%%X` stays
  `%%X`), and `%%~mods<letter>` is likewise emitted whole. These survive to the
  parser, which turns them into FOR-variable references (`parser/parser.go:1137`,
  `parser/parser.go:1260`; the FOR header strips all `%` via
  `strings.Trim(raw, "%")` at `parser/parser.go:802`).
- A *single* `%I` is treated as the start of a `%VAR%` reference: `ExpandPercent`
  looks for a closing `%` (`expander/early.go:94`) and, finding none on a typical
  `FOR %I IN (...)` line, emits a literal `%` and moves on — so `%I` does **not**
  become a FOR variable. The loop variable would be empty.

Therefore, at this shell's prompt you must write the script form:

```bat
FOR %%I IN (a b c) DO ECHO %%I
```

Typing the cmd.exe-interactive single-percent form `FOR %I IN (a b c) DO ECHO %I`
will not bind the loop variable. This is a known divergence, not cmd.exe-faithful
behavior, and is the most likely surprise for someone used to the real prompt.

## Tab completion (`completer.go`)

`newCompleter()` (`repl/completer.go:19`) returns a `readline.PrefixCompleter`
built by `buildItems()`. Two ingredients: a fixed list of builtin command names,
and dynamic filesystem completion.

### Builtin command names

```go
var batCommands = []string{
    "ECHO", "SET", "IF", "FOR", "GOTO", "CALL", "EXIT",
    "CD", "CHDIR", "DIR", "CLS", "PAUSE", "REM",
    "SETLOCAL", "ENDLOCAL",
}
```

`buildItems()` (`repl/completer.go:25`) registers each command **twice** — the
uppercase spelling and its `strings.ToLower` variant — so both `EC<Tab>` and
`ec<Tab>` complete. Each command node attaches `PcItemDynamic(fileCompleter)` as
its child, so after the command name the completer offers file/dir arguments
(e.g. `TYPE <Tab>` lists files). Finally a top-level `PcItemDynamic(fileCompleter)`
is appended (`repl/completer.go:38`) so bare filename completion works for the
first word too (useful for invoking external programs that are not in
`batCommands`).

Note this list is a curated subset of the actually-implemented builtins — it is
just the completion menu, not the dispatch table, so completing `DIR` does not
imply `DIR` is the only directory command. Adding a new builtin to the shell
does not automatically add it here; this slice must be edited to surface it in
tab completion.

### File completion and path presentation (`fileCompleter`)

`fileCompleter(prefix string) []string` (`repl/completer.go:44`) is where the
Windows-vs-Unix path translation happens. It is the most quirk-laden part of the
file. Walkthrough:

```go
// Convert backslashes to forward slashes for OS filesystem calls
osPrefix := strings.ReplaceAll(prefix, "\\", "/")
```

1. **Input normalization**: the user is expected to type Windows-style
   backslash paths (consistent with the `C:\>` prompt and the rest of the
   shell). The completer first rewrites `\` → `/` to make real
   `os.ReadDir` calls against the Unix filesystem.

2. **Split into dir + base** (`repl/completer.go:49`): the substring after the
   last `/` is the partial filename (`base`); the part before is the directory
   to scan (`dir`). With no `/`, `dir = "."`. A leading `/` (root) yields
   `dir = "/"`.

3. **Scan and prefix-match** (`repl/completer.go:59`): `os.ReadDir(dir)` is
   listed; entries whose name (lowercased) starts with `base` (lowercased) match.
   Matching is therefore **case-insensitive**, in keeping with cmd.exe semantics
   even on a case-sensitive Unix FS. A `os.ReadDir` error (e.g. nonexistent dir)
   returns `nil` — no completions, no error.

4. **Re-present as backslash paths** (`repl/completer.go:70`): each match is
   rebuilt as `dir + "/" + name` (or just `name` when `dir == "."`), then the
   whole thing is converted back with `strings.ReplaceAll(full, "/", "\\")`.
   So although the filesystem is Unix and uses `/`, completions are shown to the
   user with `\`, matching the prompt's Windows aesthetic.

5. **Directory marker** (`repl/completer.go:79`): directory entries get a
   trailing `\` appended (`full += "\\"`), so completing into a directory leaves
   the cursor ready to keep typing the next path segment.

Consequences and edge cases to be aware of:

- A path typed with forward slashes still works for the `os.ReadDir` call (the
  `\`→`/` rewrite is a no-op on it), but the *returned* completions are always
  backslash-style, so continuing to tab-complete mixes separators unless the
  user adopts `\`.
- Hidden files (dotfiles) are not filtered — they appear if they match `base`.
- There is no globbing or `*`/`?` expansion here; this is plain prefix matching.
- Because matching is purely prefix-based on the *current directory segment*,
  this completer does not understand `..`, `~`, or environment variables — those
  are passed straight to `os.ReadDir` as literal directory names.

## Example session

Assuming `~/autoexec.bat` from above and a directory containing `notes.txt` and
a `src\` subdirectory:

```text
$ cmd
cmd — BAT shell for Unix. Type EXIT to quit.
Welcome back, alice.
C:\> ECHO %PROMPT_NAME%
dev
C:\> SET COUNT=3
C:\> IF %COUNT%==3 ECHO three
three
C:\> FOR %%I IN (a b c) DO ECHO item %%I
item a
item b
item c
C:\> TYPE no<Tab>
C:\> TYPE notes.txt          (completion filled in; directories would gain a trailing \)
C:\> GOTO :nowhere
The system cannot find the batch label specified - nowhere   (line aborts; prompt returns — the shell does NOT exit)
C:\> EXIT /B 7
C:\> IF ERRORLEVEL 7 ECHO at least seven
at least seven
C:\> EXIT
$
```

Key things this session demonstrates:

- Autoexec's `SET`/`ECHO OFF`/banner ran once at startup and its variables
  persist.
- `%ERRORLEVEL%` flows from one line to the next via `e.ExitCode = code`.
- `FOR` requires the doubled-percent script form even at the prompt.
- A failed `GOTO` aborts only that line, not the session.
- `EXIT /B` sets the exit code but stays in the shell; bare `EXIT` quits.
- Ctrl-D at an empty prompt would also quit (printing a newline first); Ctrl-C
  just clears the current input line.

## Related docs

- [Variable expansion and `%`/`!` handling](expansion.md) — the `ExpandPercent`
  pre-tokenization pass `RunLine` invokes.
- [The executor and statement execution](executor.md) — `RunLine`, `RunStmts`,
  `RunFile`, and abort/EXIT/GOTO flow control.
- [The parser](parser.md) — how FOR-variable `%%X` tokens are recognized.
