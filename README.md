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
cmd /c somecmd 2>&1
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
| `ECHO [text]` | Print text. `ECHO.` prints a blank line. `ECHO ON/OFF` toggles echo. |
| `SET [name=value]` | Set variable. No args lists all. Empty value unsets. |
| `SET /A name=expr` | Integer arithmetic (`+` `-` `*` `/` `%`) |
| `IF` | Conditional: string compare, `EXIST`, `ERRORLEVEL`, `NOT` |
| `FOR /L %%I IN (s,step,e) DO` | Numeric loop |
| `FOR %%I IN (list) DO` | List/glob loop |
| `GOTO label` | Jump to `:label` |
| `CALL :label [args]` | Call subroutine |
| `CALL script.bat [args]` | Run another BAT file |
| `EXIT [/B] [code]` | Exit shell or subroutine (`/B`) with optional code |
| `CD [path]` | Change directory |
| `DIR [path]` | List directory contents |
| `CLS` | Clear screen |
| `PAUSE` | Wait for keypress |
| `TYPE file [file2...]` | Print file contents to screen |
| `SETLOCAL [EnableDelayedExpansion]` | Push environment scope. `!VAR!` resolves at run time. |
| `ENDLOCAL` | Pop environment scope (restores variables and delayed expansion state) |
| `REM` | Comment |

Any command not matched as a builtin or `.bat` file is executed as a system command.

## Differences from Windows CMD

- **No drive letters.** `C:\foo\bar` becomes `/foo/bar`. Relative paths work normally.
- **Case-sensitive filenames.** The underlying Linux filesystem is case-sensitive even though BAT commands are not.
- **`%%I` vs `%I` in FOR.** Use `%%I` in `.bat` files (same as Windows). The REPL accepts both.
- **No `%CD%`, `%DATE%`, `%TIME%` magic variables** yet — use `pwd`, `date` etc. as external commands.
- **Pipes (`|`) and `&&` / `||`** are recognized by the lexer but not yet executed.

## Project Structure

```
cmd/
├── main.go                  entry point
├── lexer/                   tokenizer
├── parser/                  AST + recursive descent parser
├── expander/                %VAR% resolution
├── executor/                statement execution + GOTO program counter
│   └── builtins/            ECHO, SET, CD, DIR, ...
├── env/                     variable store + SETLOCAL scope stack
├── repl/                    interactive loop, readline, tab completion
└── internal/util/           Windows→Unix path helpers
```

## License

MIT
