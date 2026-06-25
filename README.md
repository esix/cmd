# cmd

A Windows BAT/CMD shell for Unix and Linux. Run `.bat` files natively, or use the interactive shell with BAT syntax.

Built for fun. Not a full CMD.EXE replacement — just enough to be useful.

## Install

Requires Go 1.22+.

```bash
git clone https://github.com/esix/cmd
cd cmd
go build -o cmd .
```

Optionally install system-wide:

```bash
sudo cp cmd /usr/local/bin/cmd
```

## Startup

When started interactively, cmd runs `~/autoexec.bat` automatically if it exists — just like `AUTOEXEC.BAT` on DOS/Windows. Use it to set environment variables, print a greeting, or configure your environment.

Example `~/autoexec.bat`:
```bat
@ECHO OFF
SET EDITOR=vim
ECHO Ready.
ECHO.
```

## Usage

**Interactive shell:**
```bash
cmd
```

**Run a BAT file:**
```bash
cmd script.bat
cmd script.bat arg1 arg2
```

**As a shebang interpreter** — add to the first line of your `.bat` file:
```bat
#!/usr/local/bin/cmd
ECHO Hello from Unix!
```
Then make it executable:
```bash
chmod +x script.bat
./script.bat
```

## Supported Syntax

### Variables

```bat
SET NAME=World
ECHO Hello %NAME%!

REM Unset a variable
SET NAME=

REM Arithmetic
SET /A RESULT=10*5+2
ECHO %RESULT%
```

### IF

```bat
IF "%NAME%"=="Alice" ECHO Hi Alice!
IF NOT "%OS%"=="Windows" ECHO Running on Unix

IF EXIST /etc/hosts ECHO hosts file found

IF ERRORLEVEL 1 ECHO Something went wrong
```

### GOTO and labels

```bat
GOTO start

:init
ECHO Initializing...
GOTO :EOF

:start
CALL :init
ECHO Done.
```

### FOR loops

```bat
REM Numeric range (start, step, end)
FOR /L %%I IN (1,1,10) DO ECHO %%I

REM List of items
FOR %%F IN (foo bar baz) DO ECHO %%F

REM File glob
FOR %%F IN (*.txt) DO ECHO %%F
```

### Subroutines

```bat
CALL :greet Alice
CALL :greet Bob
GOTO :EOF

:greet
ECHO Hello %1!
EXIT /B 0
```

### Redirection

```bat
ECHO hello > out.txt
ECHO world >> out.txt
SET /P NAME=< input.txt
cmd /c somecmd 2>&1
```

### Pipes and chaining

```bat
TYPE log.txt | FINDSTR error      REM pipe builtins or external commands
build && ECHO ok || ECHO failed   REM run on success / on failure
mkdir tmp & cd tmp                REM unconditional sequence
```

### Delayed expansion

```bat
SETLOCAL EnableDelayedExpansion
SET RESULT=start
FOR /L %%I IN (1,1,5) DO SET RESULT=!RESULT!-%%I
ECHO %RESULT%     REM shows "start" (expanded at parse time)
ECHO !RESULT!     REM shows "start-1-2-3-4-5" (expanded at run time)
ENDLOCAL
```

### Echo control

```bat
@ECHO OFF          REM suppress command echo, @ suppresses this line too
ECHO ON            REM re-enable
ECHO.              REM print blank line
```

## Built-in Commands

| Command | Description |
|---|---|
| `ECHO [text]` | Print text. `ECHO.` / `ECHO(` print a blank line. `ECHO ON/OFF` toggles echo. |
| `SET [name=value]` | Set variable. No args lists all. Empty value unsets. |
| `SET /A name=expr` | Integer arithmetic (`+ - * / %`, bitwise `& \| ^`, shifts `<< >>`, comma-separated assignments) |
| `SET /P name=prompt` | Prompt and read a line from stdin (or a `<` redirect) |
| `IF` | Conditional: string compare, `/I`, `EXIST`, `DEFINED`, `ERRORLEVEL`, `NOT`, numeric `EQU`/`NEQ`/`LSS`/… |
| `FOR /L %%I IN (s,step,e) DO` | Numeric loop |
| `FOR %%I IN (list) DO` | List / wildcard-glob loop |
| `FOR /F ["opts"] %%I IN (...) DO` | Tokenize files / strings / command output (`tokens=`, `delims=`, `eol=`, `usebackq`) |
| `FOR /D` / `FOR /R` | Directory loop / recursive walk |
| `GOTO label` | Jump to `:label` (`GOTO :EOF` ends the script/subroutine) |
| `CALL :label [args]` | Call subroutine |
| `CALL script.bat [args]` | Run another BAT file |
| `CALL set/echo …` | Call a builtin with an extra round of expansion |
| `EXIT [/B] [code]` | Exit shell or subroutine (`/B`) with optional code |
| `CD [/D] [path]` | Change directory |
| `DIR [/B] [/S] [/A-D] [path]` | List directory contents / bare names |
| `PUSHD` / `POPD` | Push/pop the directory stack |
| `TYPE file [file2…]` | Print file contents |
| `DEL`/`ERASE`, `COPY`, `MOVE`, `REN`/`RENAME` | File operations |
| `MKDIR`/`MD`, `RMDIR`/`RD` | Directory operations |
| `FINDSTR [/R /L /I /V /N /C:…] pattern [files]` | Search text (findstr regex dialect) |
| `CERTUTIL -encodehex/-decodehex` | Hex encode/decode a file |
| `CMD /C [/V:ON] command` | Run a command in a sub-shell |
| `DATE [/T]` / `TIME [/T]` | Print current date/time |
| `CLS`, `PAUSE`, `REM` | Clear screen, wait for keypress, comment |
| `SHIFT` | Shift positional parameters |
| `SETLOCAL [EnableDelayedExpansion]` | Push environment scope. `!VAR!` resolves at run time. |
| `ENDLOCAL` | Pop environment scope (restores variables and delayed expansion state) |

`CHCP`, `VER`, `TITLE`, and `COLOR` are accepted as no-ops. Any command not
matched as a builtin or `.bat` file is executed as a system command. See
[docs/builtins.md](docs/builtins.md) for the full reference.

## Differences from Windows CMD

- **No drive letters.** `C:\foo\bar` becomes `/foo/bar`; a leading drive letter is stripped. Backslashes and forward slashes are normalized.
- **Case-sensitive filenames.** The underlying Linux filesystem is case-sensitive even though BAT commands and variable names are not.
- **`%%I` vs `%I` in FOR.** Use `%%I` in `.bat` files (same as Windows). The REPL accepts both.
- **`%DATE%`, `%TIME%`, `%RANDOM%`, `%ERRORLEVEL%`** are dynamic and work. `%CD%` is not yet a magic variable — use `cd` with no args instead.
- **No 8.3 short names.** The `~s` path modifier returns the full path; `~a`/`~t`/`~z` are approximated.

For the full, authoritative compatibility reference — the supported-feature
matrix, the cmd.exe quirks deliberately reproduced, and every Unix divergence —
see **[docs/cmd-compatibility.md](docs/cmd-compatibility.md)**.

## Documentation

Detailed implementation docs live in **[docs/](docs/README.md)**: the processing
pipeline ([lexer](docs/lexer.md), [parser](docs/parser.md),
[expansion](docs/expansion.md), [executor](docs/executor.md)), the
[built-in commands](docs/builtins.md), the [environment model](docs/environment.md),
the [interactive shell](docs/repl.md), [cmd.exe compatibility](docs/cmd-compatibility.md),
and the [libraries used](docs/libraries.md). Start with
[docs/architecture.md](docs/architecture.md).

## Project Structure

```
cmd/
├── main.go                  entry point
├── lexer/                   tokenizer
├── parser/                  AST + recursive descent parser
├── expander/                %VAR% / !VAR! / %%VAR / tilde resolution
├── executor/                statement execution, control flow, pipes, redirection
│   └── builtins/            ECHO, SET, DIR, TYPE, FINDSTR, CERTUTIL, ...
├── env/                     variable store + SETLOCAL scope stack
├── repl/                    interactive loop, readline, tab completion
├── internal/util/           Windows→Unix path helpers
└── docs/                    implementation documentation
```

## License

MIT
