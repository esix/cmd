# Built-in Commands

Built-ins are the commands the shell implements internally rather than executing
as external programs. They live in [`executor/builtins/`](../executor/builtins)
and are exposed through a single map, [`Registry`](../executor/builtins/builtins.go).
Each entry is a `Func` that receives the already-expanded argument vector
(`argv[1:]`) and the live environment, and returns an exit code.

This document describes every registered name and the behaviour of each
built-in, with emphasis on the cmd.exe quirks the port deliberately reproduces.
Variable substitution, delayed expansion, and the `%%X` / `%VAR%` / `!VAR!`
forms referenced throughout are covered in [Expansion](expansion.md); the
statement-level handling of `ECHO`, `SET`, and command dispatch lives in the
[Executor](executor.md).

## The `Func` signature and the `Registry`

`builtins.go:9`:

```go
// Func is the signature every builtin must implement.
// args are the already-expanded arguments (argv[1:]).
// Returns the exit code.
type Func func(args []string, e *env.Env) int
```

`Registry` (`builtins.go:12`) maps an **uppercased** command name to its
implementation. The dispatcher in the executor uppercases the command word
before the lookup (`executor.go:1599`), so matching is case-insensitive. The
following names are registered:

| Name(s) | Implementation | Notes |
|---|---|---|
| `ECHO` | `Echo` | Note: blank-line/`ON`/`OFF` cases are intercepted by the executor (see below). |
| `SET` | `Set` | The display path; assignment is handled by the executor's `SetStatement`. |
| `CD`, `CHDIR` | `Cd` | `CHDIR` is an alias. |
| `CLS` | `Cls` | |
| `PAUSE` | `Pause` | |
| `DIR` | `Dir` | Shares `DirList` with the `for /f` capture path. |
| `REM` | `Rem` | No-op comment. |
| `DATE` | `Date` | |
| `TIME` | `Time` | |
| `TYPE` | `Type` | |
| `PUSHD` | `Pushd` | |
| `POPD` | `Popd` | |
| `DEL`, `ERASE` | `Del` | `ERASE` is an alias. |
| `COPY` | `Copy` | |
| `MOVE` | `Move` | |
| `MKDIR`, `MD` | `Mkdir` | `MD` is an alias. |
| `RMDIR`, `RD` | `Rmdir` | `RD` is an alias. |
| `REN`, `RENAME` | `Ren` | `RENAME` is an alias. |
| `CERTUTIL` | `Certutil` | |
| `CMD` | `Cmd` | |
| `FINDSTR` | `Findstr` | |
| `SHIFT` | inline no-op | `SHIFT` of `%1..%9` is handled by the FOR/CALL argument machinery; the registry entry just returns 0. |
| `CHCP` | inline no-op | Console code page — meaningless on Unix. |
| `VER` | inline no-op | Cosmetic Windows console command. |
| `TITLE` | inline no-op | Cosmetic Windows console command. |
| `COLOR` | inline no-op | Cosmetic Windows console command. |

The four inline no-ops `CHCP`, `VER`, `TITLE`, `COLOR` (and `SHIFT`) are
literally `func(_ []string, _ *env.Env) int { return 0 }` (`builtins.go:24`,
`builtins.go:41-45`). They consume their arguments and succeed silently so that
scripts containing them keep running.

### Dispatch and fall-through to external execution

The dispatcher (`executor.go:1599`) looks the command name up in `Registry`. On
a hit it applies any redirections, calls the `Func`, then cleans up. On a miss
it tries to resolve the word as a `.bat` file (`executor.go:1608`), and failing
that runs it as an external program via `runExternal` (`executor.go:1617`).
**Anything not in the `Registry` falls through to external execution** — there
is no error for "unknown built-in". An external command that cannot be launched
prints `'<name>' is not recognized as an internal or external command.` and
returns 1 (`executor.go:1667`).

One path-handling subtlety affects built-ins: before dispatch the executor
converts backslashes to forward slashes in every argument **except for
`FINDSTR`** (`executor.go:1592`), because findstr uses `\` as a regex escape.
`Findstr` therefore normalizes the slashes in its own *file* arguments
internally.

## ECHO

`Echo` (`echo.go:14`) handles only the "print text" case; the more delicate
states are intercepted earlier by the executor's `execEcho`
(`executor.go:532`), so the registry function is the fall-back for plain
`ECHO some text`.

- **Text:** joins the args with single spaces and appends a CRLF
  (`echo.go:23`). All ECHO output uses `\r\n`, matching cmd.exe console output.
- **No args / state query:** `ECHO` with nothing else prints `ECHO is on.` or
  `ECHO is off.` depending on `e.Echo` (`echo.go:16-21`). The executor's
  `execEcho` produces the same strings (`executor.go:579-582`).
- **`ECHO ON` / `ECHO OFF`:** toggles command echoing. This is recognized at the
  statement level (`executor.go:534`, setting `ex.env.Echo`), not in the
  registry function.
- **Blank line (`ECHO.`):** `echo.` (and `echo(`) with no following text print a
  single empty CRLF line. This is detected before tokenizing
  (`executor.go:92-94`, `executor.go:558`) because the `.`/`(` glues directly to
  the word with no separating space. Redirection is still honoured, so
  `echo.>> file` appends a CRLF to the file.

```bat
@echo off
echo Hello, world      REM prints: Hello, world
echo.                  REM prints a blank line
echo                   REM prints: ECHO is off.
echo on
```

## SET

`SET` is split across two places. Assignment (`SET NAME=VALUE`), `SET /P`, and
`SET /A` originate from the parser's `SetStatement` and are executed by
`execSet` (`executor.go:592`); the `Set` built-in (`set.go:16`) handles the
display and listing cases plus the arithmetic evaluator. The name is resolved
through `expander.ExpandName` first so that `set "%%a=%%b"` inside a FOR body
works (`executor.go:601`).

Variables are stored uppercased but cmd.exe **preserves the original-case
spelling** used when a name is first created; `DisplayName` (`env.go:126`)
returns that spelling, so listings echo back `Path` rather than `PATH` if that
is how it was set.

### Display and listing

- **`SET` alone** lists every variable as `NAME=value`, sorted case-sensitively
  by the uppercased key, using `DisplayName` for the printed name
  (`set.go:17-28`). Listing output uses `\n`, not CRLF.
- **`SET PREFIX`** (a bare name, no `=`) lists every variable whose uppercased
  key starts with `PREFIX` (`set.go:99-118`). If none match it prints
  `Environment variable <PREFIX> not defined` and returns 1.

### Assignment and unset

`SET NAME=VALUE` assigns; **`SET NAME=` (empty value) deletes** the variable
(`executor.go:649-653`, `env.go:118`). This is the cmd.exe idiom for clearing a
variable.

```bat
set FOO=bar
set FOO=          REM deletes FOO
set F             REM lists FOO... if any survive; else "not defined"
```

### SET /P — prompt and read a line

`SET /P` (`executor.go:617-643`) reads one line from stdin into a variable. It
reproduces several quirks:

- The **prompt is always printed**, even when stdin is redirected from a file.
- **Leading whitespace of the prompt is stripped** before printing
  (`strings.TrimLeft(prompt, " \t")`, `executor.go:625`). An empty prompt prints
  nothing.
- The line read has its trailing `\r`/`\n` removed (`executor.go:631`).
- On EOF with no data the variable is **unset**; otherwise it is set to the line
  (`executor.go:637-641`).
- **Empty name (the no-assign idiom):** `set /p "=text"` has an intentionally
  empty variable name. A bare `set` would list variables, but `execSet` checks
  for the prompt flag specifically so the empty name does *not* trigger a
  listing (`executor.go:596`). With an empty name it prints the prompt and
  returns without assigning (`executor.go:634`). Combined with `<nul` this is
  the classic **print-without-newline** trick:

```bat
<nul set /p "=No newline here"
echo  done
```

  Redirects are applied around the prompt print (`executor.go:624`), so the text
  can be captured: `> out.txt <nul set /p "=text"` writes `text` (no CRLF) to
  `out.txt`.

### SET /A — integer arithmetic

`SET /A` evaluates integer expressions. The statement layer joins the value
words and, if delayed expansion is on, expands `!VAR!` first
(`executor.go:609-614`); it then calls `Set` with `/A` and `name+"="+value`.
Inside `Set` (`set.go:31-94`):

- The expression is a **comma-separated list** evaluated left to right; later
  parts see assignments made by earlier ones (`set.go:36`):

```bat
set /a "a=1, b=a+2, c=b*10"   REM a=1, b=3, c=30
```

- A part **without `=`** is evaluated and its result printed
  (`set.go:42-50`). A part **with `=`** assigns the result to the named
  variable.
- **Compound assignment** operators `+=`, `-=`, `*=`, `/=`, `%=` are recognized
  by inspecting the last byte of the name (`set.go:56-63`); the current value is
  combined with the evaluated right side (`set.go:71-89`). For compound `/=` and
  `%=` by zero the division/modulo step is skipped, leaving the result at the
  already-evaluated right-hand value of `0`; the variable is then assigned
  unconditionally (`set.go:91`), so e.g. `set /a "x/=0"` sets `x` to `0`.

The expression evaluator is a recursive-descent parser (`set.go:160-401`) with
cmd.exe's operator precedence, from lowest to highest:

```
|   ^   &   << >>   + -   * / %
```

implemented as `parseBitOr → parseBitXor → parseBitAnd → parseShift →
parseAddSub → parseMulDiv → parseAtom`. Atoms (`set.go:335`) are:
parenthesized sub-expressions, unary `-`/`+`, hex literals (`0x...`), decimal
literals, and **variable names that resolve to their numeric value**
(`strconv.Atoi(env.Get(name))`, so a missing/empty/non-numeric variable reads as
0, `set.go:392-397`). Division and modulo by zero in the evaluator return an
error (`set.go:317-325`); the error is reported to stderr as `SET /A: <err>` and
the command returns 1 (`set.go:46`, `set.go:67`).

#### The double-percent modulo quirk

In a batch file the `%` of the modulo operator must be written `%%`, because a
single `%` is consumed by early percent-expansion. `evalArith` (`set.go:133`)
first re-resolves any surviving `%VAR%` pairs (`set.go:138-150`) and then
collapses `%%` back into a single `%` before evaluation (`set.go:153`):

```bat
set /a "rem=17 %% 5"      REM rem=2  (the %% is the modulo operator)
```

## CD / CHDIR

`Cd` (`misc.go:15`):

- A leading **`/D` switch is accepted and dropped** (`misc.go:18-24`); on
  Windows it changes drive *and* directory, but the drive half is meaningless on
  Unix.
- With **no path argument** it prints the current working directory
  (`misc.go:25-29`) — cmd.exe's `cd` with no args reports the current path.
- With a path it runs `toUnixPath` (strips a drive letter, converts `\` to `/`,
  `misc.go:294`) then `os.Chdir`. On failure it prints `The system cannot find
  the path specified: <arg>` and returns 1 (`misc.go:31-34`).

## DIR and DirList

The `dir` listing exists in two flavours that intentionally agree with each
other: the human-readable `Dir` built-in (`misc.go:232`) and `DirList`
(`misc.go:90`), the programmatic form scripts capture with `for /f` and which
the executor's `for /f` path also calls (`executor.go:1392`).

### DirList — bare / recursive / files-only

`DirList` recognizes three flag forms and ignores the rest:

- **`/B`** — bare mode, sets the `bare` return flag.
- **`/S`** — recurse.
- **`/A-D`** — any `/A...` switch containing `-D` sets *files only*
  (`misc.go:101-104`); directories are excluded from output but still traversed.

Other switches (`/O`, `/T`, `/W`, ...) are matched by `isDirFlag` and ignored
(`misc.go:105-106`). The first non-flag argument is the pattern (`misc.go:108`);
backslashes are normalized to `/` and an empty pattern becomes `.`
(`misc.go:111-114`).

The pattern is split into a base directory and a glob: if it names an existing
directory, the contents are listed (`dir, glob = pattern, "*"`); otherwise it is
`filepath.Dir`/`filepath.Base` of the pattern (`misc.go:119-127`).

- **Without `/S`:** glob the directory, emit **bare names** (`filepath.Base`),
  sorted case-insensitively (`misc.go:131-144`).
- **With `/S`:** `dirWalkBare` (`misc.go:152`) emits **absolute paths** in
  cmd.exe `dir /s /b` order — within each directory all matching entries
  (sorted) come first, then a depth-first recursion into the sorted
  subdirectories.

The case-insensitive sort `sortNamesCI` (`misc.go:188`) lower-cases for
comparison and uses raw byte order as a stable tiebreaker, matching cmd.exe's
ordering.

### The `isDirFlag` heuristic

`isDirFlag` (`misc.go:204`) must tell a cmd switch like `/B`, `/A-D`, or `/O:N`
apart from a **Unix absolute path** such as `/tmp/iss`, since both start with
`/`. A switch is `/` plus a single option letter/digit, optionally followed by a
`:` or `-` value separator. So:

- `/B` (length 2) → a flag.
- `/A-D`, `/O:N` (third char is `-` or `:`) → a flag.
- `/tmp/iss` (third char is a letter, no separator) → **not** a flag, treated as
  a path.

This heuristic only works because dir switches are never multi-letter.

### Dir — the human-readable listing

`Dir` (`misc.go:232`):

- If any arg is `/B`, it delegates to `DirList` and prints each line followed by
  CRLF, with **no header** (`misc.go:234-243`) — the form `for /f` consumes.
- Otherwise it lists `firstNonFlag(args)` (or `.`) with a Windows-style header
  `Directory of <abs>`, **directories first** then files, both sorted
  case-insensitively (`misc.go:260-265`). Each line shows the mod time formatted
  `01/02/2006  03:04 PM`, `<DIR>` or the size, and the name. A `File(s)`/`Dir(s)`
  footer is printed. A missing path prints `File Not Found: <path>` and returns 1.

## TYPE

`Type` (`type.go:12`) prints one or more files' contents to stdout:

- **Multiple files** are concatenated in order (`type.go:18`).
- **`nul`** (case-insensitively) is skipped, producing no output — this supports
  the `type nul > file` idiom for truncating/creating an empty file
  (`type.go:21-23`).
- The file content is written **verbatim with no trailing newline added**
  (`type.go:34`); cmd.exe's TYPE does the same, and batch code relies on it
  (e.g. emitting padding spaces without a line break).
- A file that cannot be read prints `The system cannot find the file specified:
  <path>` to stderr and sets the exit code to 1, but processing of the remaining
  files continues (`type.go:26-30`).

## File operations: DEL/ERASE, COPY, MOVE, MKDIR/MD, RMDIR/RD, REN/RENAME

These live in [`fileops.go`](../executor/builtins/fileops.go). Several share a
flag heuristic and the `toUnixPath` normalization.

### The `isBatFlag` heuristic

`isBatFlag` (`fileops.go:14`) treats a 2-3 character argument starting with `/`
as a switch (e.g. `/Q`, `/F`, `/S`, `/Y`). Such arguments are stripped from the
file list in `DEL`, `COPY`, and `MOVE`, and inspected in `RMDIR`. Because the
rule is purely length-based, longer `/`-prefixed tokens are treated as paths.

### DEL / ERASE

`Del` (`fileops.go:19`) deletes each non-flag argument after `toUnixPath` and
glob expansion (`fileops.go:25-42`). A pattern with **no matches** prints `Could
Not Find <pattern>` and sets the code to 1, but other patterns still process. A
removal failure prints `Access is denied: <file>` and sets the code to 1. With
no args it prints `The syntax of the command is incorrect.` and returns 1.

### COPY

`Copy` (`fileops.go:47`) strips flags, then takes the first two remaining paths
as source and destination (`fileops.go:48-60`). If the destination is an
existing directory, the source's base name is appended (`fileops.go:69-72`). On
success it prints `        1 file(s) copied. (<n> bytes)`. Missing source →
`The system cannot find the file specified: <src>`; uncreatable destination →
`Cannot create file: <dst>`; fewer than two paths → the syntax error. All
failures return 1.

### MOVE

`Move` (`fileops.go:87`) mirrors `Copy` but uses `os.Rename`. The
directory-destination rule is the same (`fileops.go:101-103`). On success it
prints `        1 file(s) moved.`; failure prints `Cannot move: <err>` and
returns 1.

### MKDIR / MD

`Mkdir` (`fileops.go:114`) creates each argument with `os.MkdirAll` (mode 0755),
so **intermediate directories are created** as cmd.exe's MKDIR does
(`fileops.go:121`). No args → syntax error; a creation failure prints `Cannot
create directory: <dir>` and returns 1.

### RMDIR / RD

`Rmdir` (`fileops.go:130`) removes each directory. **`/S` enables recursive
removal** (`os.RemoveAll`); without it, `os.Remove` only deletes an empty
directory (`fileops.go:131-148`). A failure prints `Cannot remove directory:
<dir>` and returns 1.

### REN / RENAME

`Ren` (`fileops.go:158`) renames the first argument to the second. The
destination is **forced into the source's directory** via
`filepath.Join(filepath.Dir(src), dst)` (`fileops.go:166`), faithfully
reproducing cmd.exe's rule that REN cannot move a file across directories. Fewer
than two args → syntax error; an `os.Rename` failure prints `Cannot rename:
<err>` and returns 1.

## PUSHD / POPD

The directory stack is the package-level slice `dirStack` (`pushd.go:10`).

- **`Pushd`** (`pushd.go:13`): with no arg it prints the current directory
  (cmd.exe behaviour) and returns 0 without touching the stack. With a path it
  `os.Chdir`s, and **only on success** pushes the previous working directory
  onto the stack (`pushd.go:19-25`). A failed `chdir` prints `The system cannot
  find the path specified: <arg>` and returns 1, leaving the stack untouched.
- **`Popd`** (`pushd.go:29`): pops the top entry and `os.Chdir`s back to it. An
  empty stack returns 1 with no output (`pushd.go:30-32`).

```bat
pushd C:\work     REM cd to C:\work, remember the old dir
... do work ...
popd              REM cd back
```

## PAUSE, CLS, REM

- **`Pause`** (`misc.go:45`) prints `Press any key to continue . . . `, reads a
  single byte from stdin, then prints a newline.
- **`Cls`** (`misc.go:39`) writes the ANSI clear sequence `\033[H\033[2J`.
- **`Rem`** (`misc.go:54`) is a no-op returning 0.

## DATE / TIME

Both commands are **non-interactive** — they print the value and return, never
prompting for a new value the way bare `date`/`time` would on Windows (which
would block a script).

- **`Date`** (`misc.go:60`) always prints the current date as `Mon 01/02/2006`
  (e.g. `Mon 06/24/2026`), i.e. cmd.exe's default US format, equivalent to `date
  /t`.
- **`Time`** (`misc.go:68`): with **`/T`** it prints the 12-hour clock `03:04 PM`
  (`misc.go:71-74`); bare `time` prints the full 24-hour value with
  space-padded hour and centiseconds, `%2d:%02d:%02d.%02d` (e.g. `21:17:09.42`),
  matching `%TIME%` (`misc.go:76-78`).

These match the dynamic `%DATE%` / `%TIME%` pseudo-variables in
[`env.go:86-99`](../env/env.go).

## CERTUTIL

`Certutil` (`certutil.go:14`) implements the minimal hex subset used by the
projects this port was tested against — `-encodehex` and `-decodehex` only. Any
other mode prints `certutil: unsupported mode "<mode>"` and returns 1.

### -encodehex infile outfile [type]

`certutilEncodeHex` (`certutil.go:37`) reads `infile` and writes hex to
`outfile`, with output depending on the optional numeric `type`:

- **type 12** — continuous hex string, no spaces (`hex.EncodeToString`).
- **type 4** — hex with a single space between bytes.
- **default / any other** — same continuous hex string as type 12 (the "hex
  dump with offsets" form is simplified to the continuous string).

A trailing `\n` is appended to the output file (`certutil.go:74`).

### -decodehex infile outfile [type]

`certutilDecodeHex` (`certutil.go:82`) reads `infile`, **strips all spaces,
`\n`, and `\r`** plus surrounding whitespace (`certutil.go:97-100`), then
`hex.DecodeString`s the result and writes the raw bytes to `outfile`. Because it
strips both spaces and newlines, it round-trips output produced by either type
12 or type 4. Invalid hex prints `certutil: invalid hex: <err>` and returns 1.

## CMD

`Cmd` (`cmd.go:13`) is a deliberately tiny `cmd /C` shim — it does **not**
re-enter the full interpreter. It scans for flags:

- **`/V:ON`** or **`/V`** enables delayed expansion of the command string via
  `expander.ExpandBangs` (`cmd.go:20-23`, `cmd.go:37-39`).
- **`/C`** marks where the command begins; everything after it is rejoined with
  spaces into `cmdStr` (`cmd.go:24-34`). Without `/C` it prints `cmd: /C flag
  required` and returns 1.

Only two commands are recognized in the joined string:

- **`echo(<text>`** — prints the text after the `(`, trimming a leading-space
  artifact left by arg-joining (`cmd.go:42-50`).
- **`echo <text>`** — prints the text after `echo ` (`cmd.go:52-55`).

Anything else prints `cmd /C: unsupported command: <cmdStr>` and returns 1. This
is enough to support the common `cmd /v:on /c "echo !VAR!"` idiom for forcing
delayed expansion of a single value.

```bat
cmd /v:on /c "echo !COUNTER!"   REM expands !COUNTER! and prints it
```

## FINDSTR

`Findstr` (`findstr.go:29`) implements the commonly used subset of Windows
`findstr.exe`, including a translation from findstr's idiosyncratic regex
dialect to Go's `regexp`.

### Flags

`findstr.go:38-72` recognizes these switches; a switch is `/X` or `/X:...` (a
single letter after the slash, optionally followed by `:`), which is how it
distinguishes flags from Unix paths like `/var/folders/x/file`:

| Flag | Effect |
|---|---|
| `/R` | search strings are regular expressions (findstr dialect) |
| `/L` | search strings are literals |
| `/C:"string"` | a single search string that may contain spaces; **repeatable**, accumulated into `patterns` |
| `/I` | case-insensitive (`(?i)` prefix) |
| `/V` | invert — print lines that do **not** match |
| `/N` | prefix matching lines with `<lineno>:` |
| `/B` | anchor match at beginning of line (`^`) |
| `/E` | anchor match at end of line (`$`) |
| `/X` | match the whole line exactly (`^(?:...)$`) |
| `/M` | print only the names of files that contain a match |

Unsupported switches (`/G:`, `/F:`, `/D:`, `/A:`, `/P`, `/S`, `/O`, ...) are
silently ignored (`findstr.go:68-70`).

### The bare-pattern OR-of-words gotcha

If no `/C:` pattern was given, the **first non-switch argument is the search
string list, split on spaces into independent alternatives** — any of which may
match (`findstr.go:73-82`). Empty words from consecutive spaces are dropped
(they would otherwise match every line). This reproduces the classic findstr
surprise:

```bat
findstr "foo bar" file.txt
REM matches any line containing "foo" OR "bar", NOT the phrase "foo bar"

findstr /c:"foo bar" file.txt
REM matches the literal phrase "foo bar"
```

Subsequent non-switch arguments are the **files** to search (`findstr.go:84`).

### Regex vs literal selection

`useRegex` (`findstr.go:94`) is `regex || (!literal && !patternsAreC)`. That is:
bare patterns are regex by default; `/C:` patterns are literal unless `/R` is
also given; `/L` forces literal. Literal patterns are escaped with
`regexp.QuoteMeta` (`findstr.go:102`).

Each pattern becomes one matcher; a line matches if **any** matcher matches,
then `/V` inverts the result (`findstr.go:125-132`). Anchoring is applied by
wrapping the expression: `/X` → `^(?:expr)$`; `/B` → `^(?:expr)`; `/E` →
`(?:expr)$` (`findstr.go:104-113`). `/I` prepends `(?i)`. A pattern that fails
to compile prints `FINDSTR: bad search string "<p>"` and returns 2.

### The findstr regex dialect translation

`findstrRegexToGo` (`findstr.go:204`) converts findstr's limited regex syntax to
Go's. findstr supports only `.` `*` `^` `$` `[class]` and the word-boundary
escapes `\<` / `\>`, treating everything else as a literal. The translation:

- `\<` and `\>` both become `\b` (Go word boundary).
- Any other `\x` escape becomes the literal `x` (`regexp.QuoteMeta`).
- `.` `*` `^` `$` pass through as regex metacharacters.
- `[...]` character classes are copied verbatim through the closing `]`, with the
  cmd-faithful rule that a `]` immediately after `[` or `[^` is a literal class
  member (`findstr.go:224-241`); an unterminated class degrades to a literal
  `\[`.
- **Every other character** — crucially Go metacharacters `+ ? ( ) { } |` — is
  escaped with `regexp.QuoteMeta` so it matches literally, because findstr does
  not treat them as special.

### File vs stdin modes; output and exit codes

- **No file arguments → stdin (pipe) mode** (`findstr.go:162-170`): reads all of
  stdin (with a 16 MiB scanner buffer) and filters it; the per-line name prefix
  is empty.
- **With files** (`findstr.go:171-189`): each file is read whole, trailing
  newlines trimmed, and split into lines. A file that cannot be opened prints
  `FINDSTR: Cannot open <file>` and sets the code to 2.

Output (`findstr.go:137-160`): the file-name prefix `name:` is added only when
**more than one file** is searched (`showName`, `findstr.go:134`); `/N` adds the
`<lineno>:` prefix; matching lines are printed with CRLF. Under **`/M`** only
the file name is printed once per matching file, and scanning of that file stops
at the first hit (`findstr.go:145-147`, `findstr.go:157-159`).

Exit codes follow findstr: **0** if any line matched, **1** if none matched
(`findstr.go:195-198`), and **2** for a usage/compile/open error
(`findstr.go:89-91`, `findstr.go:119`, `findstr.go:176`). Note the open-error
code 2 is only returned if *no* match was found anywhere
(`findstr.go:190-192`).
