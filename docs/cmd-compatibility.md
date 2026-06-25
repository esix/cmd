# cmd.exe Compatibility

This document is the authoritative reference for how faithfully this port
reproduces Windows `cmd.exe`, which quirks it reproduces *on purpose*, and
where it necessarily diverges because the host is Unix. It is the detailed
companion to the "Differences from Windows CMD" summary in the top-level
`README.md`.

Every claim here is grounded in the source. See also [Expansion](expansion.md),
[Builtins](builtins.md), [Lexer](lexer.md), and [Architecture](architecture.md).

The compatibility work is visible in the git history; the load-bearing commits
are referenced inline (e.g. `6679e13`, `be4afe5`, `121862c`, `8182f08`).

---

## 1. Supported feature matrix

### Builtin commands

Registered in `executor/builtins/builtins.go:12`. Dispatch is case-insensitive
(name uppercased in `executor.go:1588`).

| Command | Aliases | Notes |
|---|---|---|
| `ECHO` | | text, `ECHO.`/`ECHO(` blank line, `ECHO ON/OFF`, `ECHO` query |
| `SET` | | assign, unset (empty value), `/A` arithmetic, `/P` prompt, list, prefix query |
| `CD` | `CHDIR` | `/D` switch accepted and ignored (drive change is meaningless on Unix), `misc.go:15` |
| `CLS` | | emits `\033[H\033[2J`, `misc.go:39` |
| `PAUSE` | | reads one byte from stdin |
| `DIR` | | `/B`, `/S`, `/A-D` subset (`misc.go:90`); full listing otherwise |
| `REM` | | no-op |
| `DATE` / `TIME` | | non-interactive, never prompt (`misc.go:60`, `misc.go:68`) |
| `TYPE` | | print file(s) |
| `SHIFT` | | (registry no-op; the real work is `execShift`, `executor.go:523`) |
| `PUSHD` / `POPD` | | directory stack |
| `DEL` / `ERASE`, `COPY`, `MOVE`, `MKDIR`/`MD`, `RMDIR`/`RD`, `REN`/`RENAME` | | file ops, `fileops.go` |
| `CERTUTIL` | | `-encodehex` / `-decodehex` subset |
| `CMD` | | nested interpreter |
| `FINDSTR` | | regex/literal line filter, `findstr.go` |
| `CHCP`, `VER`, `TITLE`, `COLOR` | | cosmetic Windows console commands → no-op (`builtins.go:41`) |

Anything not a builtin and not a resolvable `.bat`/`.cmd` is run as an external
process (`executor.go:1617`).

### Control flow

- `IF` with string compare (`==`), `IF NOT`, numeric `EQU/NEQ/LSS/LEQ/GTR/GEQ`,
  `IF EXIST`, `IF DEFINED`, `IF ERRORLEVEL n`, and `ELSE`
  (`execIf`/`evalCondition`, `executor.go:659`). Numeric comparison falls back to
  string comparison when either side is non-numeric (`executor.go:708`).
- `GOTO label` and `GOTO :EOF`; O(1) label lookup via a prebuilt index
  (`labelIdx`, `executor.go:198`), with a linear-scan fallback.
- `CALL :label [args]`, `CALL script.bat [args]`, and `CALL <builtin>`
  (`execCall`, `executor.go:837`).
- `EXIT [/B] [code]` — `/B` returns from the batch/subroutine, bare `EXIT`
  terminates the process (`executor.go:1557`).
- `SHIFT` shifts the positional parameters in place; a `SHIFT` inside an `IF`/`FOR`
  block persists to the enclosing scope (`RunStmts` deliberately shares
  `positional` unless `CALL` supplied a fresh set; `executor.go:283`).
- `SETLOCAL` / `ENDLOCAL` modelled as an environment scope stack with `Push`/`Pop`
  (`env.go:134`/`env.go:161`); `SETLOCAL EnableDelayedExpansion` /
  `DisableDelayedExpansion` toggle the delayed-expansion flag, which `ENDLOCAL`
  also restores.

### FOR variants

Dispatched in `execFor` (`executor.go:1069`):

| Variant | Handler | Behavior |
|---|---|---|
| `FOR %%v IN (list) DO` | `execForInList`, `executor.go:1179` | literal items, space-split after expansion, wildcard globbing |
| `FOR /L %%v IN (start,step,end) DO` | `execForRange`, `executor.go:1150` | numeric range; bounds may be `!VAR!` |
| `FOR /F ... IN (...) DO` | `execForTokens`, `executor.go:1399` | parse string / file / `'command'` / `` `command` `` output with `tokens=`, `delims=`, `eol=`, `usebackq` |
| `FOR /D %%v IN (pattern) DO` | `execForDirs`, `executor.go:1095` | directories matching glob |
| `FOR /R [path] %%v IN (pattern) DO` | `execForRecursive`, `executor.go:1120` | walk tree, glob in each dir |

`FOR /F` source kinds (`executor.go:1404`):
- `"string"` — parsed directly (or, with `usebackq`, treated as a filename).
- `'command'` / `` `command` `` — executed and stdout captured. Internal commands
  (`SET`, `ECHO`, `TYPE`, `FINDSTR`, `VER`, `CALL`, `IF`, `FOR`, and Windows `dir`)
  run through the port's own engine via a child executor capturing `os.Stdout`
  (`runInternalCapture`, `executor.go:390`; `runDirCommand`, `executor.go:1387`);
  everything else is handed to `sh -c`.
- bare token — read as a filename.

The `tokens=`, `delims=`, `eol=`, and `usebackq` options are honored
(`parseForFOpts`, `executor.go:1246`). `FOR /F` skips blank lines and `eol`
comment lines automatically (`executor.go:1493`, `executor.go:1498`). Note:
`skip=` is recognized only as an option-boundary keyword in the parser
(`executor.go:1295`) but is **not** actually applied — there is no `skip` field
in `forFOpts` (`executor.go:1239`), so leading lines are not dropped. This is a
genuine gap, not a faithful quirk.

### Redirection

Recognized for builtins (`applyRedirects`, `executor.go:1850`), `ECHO`
(`execEcho`, `executor.go:538`), and external commands (`runExternal`,
`executor.go:1617`):

`>`, `1>`, `>>`, `1>>`, `2>`, `2>>`, `<`, `2>&1`, `1>&2` / `>&2`. The target
`nul` maps to `/dev/null` (`cleanRedirectFile`, `executor.go:1842`). Leading
redirects (`> file echo text`) are collected before the command word
(`parser.go:181`).

### Pipes and chains

- **Pipes are executed** (the README's claim that they are merely "recognized"
  is stale). `execPipe` (`executor.go:356`) runs each stage sequentially on a
  *child* `Executor`, buffering each stage's stdout and feeding it to the next
  stage's stdin. Running stages in child interpreters means builtins work inside
  pipelines and flow control (`GOTO`/`EXIT`/abort) never leaks out — mirroring
  `cmd.exe`'s child-interpreter-per-segment model (commit `121862c`).
- **`&&`, `||`, `&` are executed** (`execChain`, `executor.go:483`): `&` always
  runs the right side, `&&` only on exit code 0, `||` only on nonzero.
- `( a & b & c )` blocks execute sequentially (`execBlock`, `executor.go:507`).

### Expansion features

Early `%`-expansion runs on the raw line *before* tokenizing, exactly as
`cmd.exe` does (`ExpandPercent`, `early.go:14`). Supported:

- `%VAR%`, `%0`–`%9` positional, `%~[mods]N` tilde modifiers on positionals.
- `%VAR:~N%` and `%VAR:~N,M%` substrings, with negative offsets/lengths
  (`substringExpand`, `early.go:196`).
- `%VAR:old=new%` substring replacement.
- `%%X` FOR variables and `%%~<mods><ltr>` tilde-modified FOR variables, which
  are preserved verbatim through early expansion (the loop value isn't known
  yet) and resolved later (`early.go:30`, `ExpandWord`, `expander.go:15`).

See [Expansion](expansion.md) for the full grammar.

### Delayed expansion

`!VAR!` is resolved at run time only when `DelayedExpansion` is on
(`ExpandBangs`/`expandBangs`, `expander.go:340`). Inside `!...!` the name may
itself contain `%VAR%`/`%%X` (resolved first via `expandNestedPercents`) and
supports `:~N,M` substrings and `:old=new` replacement
(`resolveDelayedRef`, `expander.go:234`).

---

## 2. Faithfully reproduced cmd.exe quirks

These behaviors are non-obvious and were implemented deliberately. Each is a
bug-for-bug match with `cmd.exe`; removing any of them would break real batch
scripts.

### CRLF on every output line

Every line the interpreter prints terminates with `\r\n`, not `\n`:
`ECHO` (`executor.go:561`, `executor.go:570`, `executor.go:586`), the `Echo`
builtin (`echo.go:17`, `echo.go:23`), `DATE`/`TIME` (`misc.go:61`,
`misc.go:77`), and `DIR /B` (`misc.go:240`).
*Rationale:* `cmd.exe` emits DOS line endings; scripts that capture output via
`FOR /F` and re-process it (and tools that compare against captured fixtures)
expect CRLF. The `FOR /F` capture path strips the trailing `\r` from each line
so the quirk round-trips cleanly (`executor.go:1439`).

### `!VAR=!` leading-equals strip

`resolveDelayedRef` (`expander.go:252`) handles a delayed ref whose name ends in
`=` with no colon: it returns the variable's value with one leading `=` removed
*iff* the value starts with `=`, else the empty string.

```bat
SETLOCAL EnableDelayedExpansion
SET "X==Q"
ECHO !X=!      REM prints  Q   (leading '=' stripped)
SET "Y=plain"
ECHO !Y=!      REM prints nothing (no leading '=')
```

*Rationale:* `gw-batsic`'s character→hex encoding table relies on this exact
behavior to encode the `=` character, which cannot otherwise survive a `SET`
assignment (the documented comment at `expander.go:230`).

### Verbatim ECHO spacing preservation

`ECHO` prefers a captured *verbatim* `RawText` over re-joined word tokens
(`parser.go:355`, used at `executor.go:566`). Exactly one space/tab after the
`ECHO` keyword is consumed as the separator; everything after it is literal,
including runs of internal spaces.

```bat
ECHO a     b      REM prints "a     b" with the internal spaces intact
```

The verbatim path is skipped when the line contains a caret (the token path
already resolved those escapes) or when redirects are present
(`parser.go:359`, `parser.go:373`).
*Rationale:* `cmd.exe` treats only the first space after `ECHO` as a delimiter
and echoes the remainder byte-for-byte; tokenize-then-rejoin would collapse
internal whitespace.

### SET /P always prints the leading-stripped prompt, including the `<nul` idiom

`SET /P` (`executor.go:617`) prints the prompt with its leading whitespace
stripped (`strings.TrimLeft(prompt, " \t")`, `executor.go:625`) and *always*
prints it — even when stdin is redirected. An empty variable name is the
print-without-newline idiom and assigns nothing:

```bat
<nul set /p "=no newline here"
```

The bare-`set`-lists-everything path is explicitly suppressed when the name is
empty but `/P` is set (`executor.go:596`), so `<nul set /p "=text"` prints the
text rather than dumping the environment.
*Rationale:* batch authors use `<nul set /p "=..."` precisely because `cmd.exe`
prints the (leading-stripped) prompt and reads nothing from the closed stdin —
the canonical "echo without trailing newline" trick.

### Echo-dot and echo-paren: blank line, honoring redirection

`ECHO.` and `ECHO(` (with nothing after) print a single blank `\r\n` line
(`parser.go:211`/`parser.go:219`, executed at `executor.go:560`). Crucially the
blank line still honors a redirect, so `echo.>> file` appends a CRLF
(`executor.go:558`). `ECHO(text` is equivalent to `echo text`; the
`(` is glued to the keyword and is *not* a block delimiter
(`countUnquotedParens` skips an `(` immediately after `ECHO`, `executor.go:1687`).
The interactive `RunLine` path has its own early `ECHO.` shortcut
(`executor.go:93`).
*Rationale:* `echo.` / `echo(` are the idiomatic ways to print a blank line, and
`echo(` additionally sidesteps `cmd.exe`'s "ECHO is on/off" ambiguity for
content that looks like a toggle.

### `%%X` FOR variables and `%%~mods` tilde modifiers

In a `.bat` file the loop variable is written `%%X`; the doubled percent
survives early expansion verbatim (`early.go:30`) because the loop value is not
known until the loop runs, then resolves from the env where the loop stores it
(`expandNestedPercents`, `expander.go:289`). The tilde-modified FOR form
`%%~<mods><ltr>` (e.g. `%%~nxf`) is likewise preserved and applied via
`applyTildeMods` (`expander.go:300`, `expander.go:71`). A single letter not
followed by another alphanumeric is treated as a FOR var; otherwise the `%%` is
left intact (so `SET /A` modulo `%%` and literal `%%` pass through,
`early.go:46`).
*Rationale:* `cmd.exe` requires `%%` in scripts (and `%` interactively) precisely
because `%`-expansion runs first; reproducing the two-pass model is what makes
nested loops like `for %%p in (%%j)` see the outer variable
(`ExpandForVars`, `expander.go:274`).

### Dynamic RANDOM / DATE / TIME with user-assignment shadowing

`%RANDOM%`, `%DATE%`, and `%TIME%` are computed on each read in `Env.Get`
(`env.go:75`, `env.go:86`, `env.go:92`), but *only* if the user has not
explicitly assigned the variable — an explicit `SET` shadows the dynamic value
(checked via `e.vars[...]` before computing). `%RANDOM%` yields `0..32767`
(`rand.Intn(32768)`); `%DATE%` is `Mon 01/02/2006`; `%TIME%` is
`%2d:%02d:%02d.%02d` (24-hour, space-padded hour, centiseconds). `%ERRORLEVEL%`
is also dynamic (returns `ExitCode`, `env.go:69`).

```bat
ECHO %RANDOM%          REM e.g. 17239
SET RANDOM=4
ECHO %RANDOM%          REM 4  (explicit assignment shadows the dynamic var)
```

*Rationale:* `cmd.exe` exposes these as live pseudo-variables, yet a real `SET
RANDOM=...` permanently overrides them; both halves are reproduced (commits
`d1dab5d`, `be4afe5`).

### `DIR /B` bare names, `DIR /S` full paths, case-insensitive ordering

`DirList` (`misc.go:90`) implements the script-consumed subset of `dir`. Without
`/S` it emits bare `filepath.Base` names sorted case-insensitively
(`sortNamesCI`, `misc.go:188`); with `/S` it emits absolute paths in `cmd.exe`
`dir /s /b` order: each directory's matching entries (sorted) first, then a
depth-first recursion into its (sorted) subdirectories (`dirWalkBare`,
`misc.go:152`). `/A-D` restricts to files (`misc.go:101`).
*Rationale:* `cmd.exe`'s `dir /b` outputs bare names and `dir /s /b` outputs
fully-qualified paths in exactly this recursion order; matching it lets
`FOR /F` loops over `dir` output behave identically (commits `6679e13`,
`8c4d891`). The standalone `DIR` builtin and the `FOR /F` `dir` capture share
this one implementation so they cannot drift (`runDirCommand`, `executor.go:1387`).

### FOR wildcard globbing: files only, zero iterations on no-match

In `execForInList` (`executor.go:1207`) an element containing `*` or `?` is
globbed against the filesystem; directories are filtered out (plain `FOR`
matches files only — `FOR /D` matches directories), and a wildcard that matches
nothing produces *zero* iterations rather than passing the literal pattern
through (`executor.go:1214`). A non-wildcard literal keeps its surrounding
quotes on the loop variable (`executor.go:1224`).

```bat
FOR %%F IN (*.nonexistent) DO ECHO %%F   REM body never runs
```

*Rationale:* `cmd.exe` globs only wildcard tokens, lists files (not directories)
for plain `FOR`, and silently skips a no-match wildcard — a literal-fallback
would corrupt scripts that probe for files (commit `6679e13`).

### `tokens=M-N` ranges

`parseTokenSpec` (`executor.go:1327`) parses `tokens=` specs of single numbers,
comma lists, `M-N` ranges, and a trailing `*` wildcard (rest-of-line), e.g.
`tokens=1,3,5`, `tokens=2-4`, `tokens=1,2*`. Malformed or out-of-range parts are
skipped, and a guard cap (`maxToken = 128`) prevents a huge range from
allocating unboundedly.
*Rationale:* before this, a range like `tokens=1-4` caused an index-out-of-range
panic; `cmd.exe` accepts ranges and this matches it (commit `fc61b65`).

### CALL builtin re-dispatch: double expansion

`CALL <builtin>` (e.g. `call set`, `call echo`) restarts the command with one
extra round of `%`-expansion (`callBuiltin`/`expandCallText`, `executor.go:975`,
`executor.go:1035`): `%%` collapses to `%`, an enclosing FOR variable's `%%X`
substitutes its value, delayed `!VAR!` refs expand, then a *second* `%VAR%` pass
runs. This is the classic batch double-expansion idiom:

```bat
SET X=hello
CALL SET "Y=%%X%%"     REM Y becomes "hello"
```

The FOR-variable disambiguation relies on `activeForVars` (pushed in `execFor`,
`executor.go:1072`) so `%%X` inside a loop body is read as the loop value rather
than a literal `%`. A deliberate exception: `call set <plain words>` with no `=`
and no switch is *not* dispatched to the SET builtin — it's treated as a script
named `set.bat` on `PATH`, because real-world suites (gw-batsic's `stl` module)
do exactly that (`executor.go:997`, `executor.go:1006`).
*Rationale:* `cmd.exe`'s `CALL` of an internal command re-parses the line with an
extra expansion pass; `%%x%%` reaching `SET` as the *value* of `x` is the whole
point of the idiom (commit `121862c`).

### Missing label and malformed IF abort the script

A `GOTO`/`CALL` to a label that does not exist prints
`The system cannot find the batch label specified - <name>` and sets
`abortPending` (`missingLabel`, `executor.go:829`), which unwinds the *entire*
batch file (and propagates out of subroutines, `executor.go:900`) returning
errorlevel 1 (`executor.go:242`). A malformed numeric `IF` — an *unquoted*
operand that expands to nothing — prints `<token> was unexpected at this time.`
and aborts the same way (`evalCondition`, `executor.go:694`). A quoted empty
operand `""` stays a valid token and falls through to string comparison.

```bat
IF %UNDEFINED% EQU 1 ECHO never   REM "EQU was unexpected at this time." + abort
GOTO nowhere                      REM aborts the file, errorlevel 1
```

*Rationale:* `cmd.exe` terminates the running batch file on both conditions;
silently continuing would mask bugs and diverge from real behavior (commit
`121862c`, `8182f08`). At the interactive prompt the abort is cleared on each
line (`executor.go:84`) since the REPL never terminates.

### ERRORLEVEL not clobbered by ECHO / SET / etc.

`updatesErrorlevel` (`executor.go:65`) returns `false` for `ECHO`, `SET`, `IF`,
`FOR`, `SETLOCAL`, `ENDLOCAL`, label, `SHIFT`, and `GOTO` statements, so their
exit codes do *not* overwrite `%ERRORLEVEL%`. Only external commands, `CALL`,
and `EXIT` update it (applied in `runLines`, `executor.go:239`; `RunStmts`,
`executor.go:296`; and `execBlock`, `executor.go:511`).

```bat
some_external_cmd_that_fails
ECHO checking...
IF ERRORLEVEL 1 ECHO it still sees the failure   REM ECHO didn't reset it
```

*Rationale:* in real `cmd.exe` these builtins leave `errorlevel` untouched;
scripts routinely run an `ECHO`/`SET` between a failing command and an
`IF ERRORLEVEL` check and expect the code to survive (commit `8182f08`).

### Caret line continuation

A physical line ending in an odd number of carets `^` continues onto the next
physical line; an even count is escaped carets (`^^`). `joinCaretContinuations`
(`executor.go:1705`) and `endsWithContinuationCaret` (`executor.go:1726`) merge
them before parsing.

```bat
ECHO this is one ^
logical line
```

*Rationale:* `^` is `cmd.exe`'s line-continuation (and escape) character;
multi-line commands in scripts depend on it.

---

## 3. Differences and limitations on Unix

These divergences are inherent to running on a Unix host; they are *not* bugs.

### Drive letters stripped (`C:\` → `/`)

A drive-letter prefix is removed wherever paths enter the OS layer:
`RunFile` (`executor.go:128`), `resolveBat` (`executor.go:1788`), `IF EXIST`
(`executor.go:754`), and `toUnixPath` for builtins (`misc.go:294`).
`C:\foo` → `/foo`, `C:foo` → `foo`, and a bare `C:` → `/`. The `CD /D` switch is
accepted and discarded (`misc.go:18`) because there is no separate drive to
change. There is no per-drive current directory.

### Backslash / forward-slash normalization

Backslashes are converted to forward slashes before any filesystem call, in
essentially every path-handling site: `RunFile` (`executor.go:125`), the `FOR`
glob (`executor.go:1210`), `applyTildeMods` (`expander.go:80`), redirect targets
(`executor.go:1841`), `IF EXIST` (`executor.go:752`), external command argv
(`executor.go:1619`), and `toUnixPath` (`misc.go:303`). One deliberate
exception: `FINDSTR` is *not* normalized in `execSimple` because its patterns
use `\` as a regex escape (`\<`, `\>`); it converts its own file arguments
internally (`executor.go:1592`). FOR `/F` `'command'` sources also normalize `\`
to `/` before handing to `sh`, so `sh` doesn't read them as escapes
(`executor.go:1421`).

### Case-sensitive filesystem

Variable *names* are case-insensitive (stored uppercase, `env.go`), and command
names are uppercased for dispatch — but the underlying filesystem is
case-sensitive. `dir`/`FOR` ordering is sorted case-insensitively to mimic
`cmd.exe` (`sortNamesCI`, `misc.go:188`), yet a script that opens `FILE.TXT`
when only `file.txt` exists will fail where it would have succeeded on Windows.

### No 8.3 short names — the `s` modifier approximates to full path

Unix has no 8.3 short-name table, so the `~s` tilde modifier cannot produce a
short name. `applyTildeMods` (`expander.go:146`) returns the *full* (absolute)
path when `s` is used alone, and the comment at `expander.go:106`/`early.go`
documents this. The other path modifiers map naturally: `d` → `/` for an
absolute path, `p` → directory, `n` → name without extension, `x` → extension,
`f` → absolute path. `..` segments are resolved because the value is normalized
to forward slashes and run through `filepath.Abs` first (`expander.go:80`).

### US-locale-fixed DATE/TIME format

`%DATE%`, `%TIME%`, the `DATE`/`TIME` builtins, and the `~t` modifier all use
hardcoded US formats, regardless of host locale:
`%DATE%` = `Mon 01/02/2006` (`env.go:90`), `%TIME%` = 24-hour with centiseconds
(`env.go:98`), `DATE` builtin = `Mon 01/02/2006` (`misc.go:61`), `TIME /t` =
`03:04 PM` (`misc.go:72`), and `~t` = `01/02/2006 03:04 PM` (`expander.go:99`).
There is no locale/regional-settings support. The clock is read via a
`now = time.Now` indirection (`env.go:105`) so tests can pin it.

### The `a` / `t` / `z` modifier approximations

The stat-query tilde modifiers (`applyTildeMods`, `expander.go:90`) approximate
DOS file metadata using Unix `os.Stat`:
- `~z` → file size in bytes (exact).
- `~t` → modification time in US format (above); no creation/access distinction.
- `~a` → a 9-char attribute string (`fileAttrString`, `expander.go:164`) that can
  only set the directory bit (`d`) and a read-only bit (derived from the absence
  of the owner-write permission, `0o200`). The other seven DOS attributes
  (archive, hidden, system, etc.) have no Unix equivalent and are always `-`.

### Other approximations and unsupported features

- **`SET /A` is integer-only and uses Go `int`.** Operators `| ^ & << >> + - * /
  %` with correct `cmd.exe` precedence, hex `0x` literals, unary `+`/`-`,
  parentheses, and comma-separated assignment lists with compound operators
  (`+=` etc.) are supported (`set.go:34`, `set.go:160`). It is *not* the exact
  32-bit signed wraparound `cmd.exe` uses; division/modulo by zero returns an
  error rather than `cmd.exe`'s runtime message.
- **`CHCP`, `VER`, `TITLE`, `COLOR` are no-ops** (`builtins.go:41`) — console
  cosmetics with no Unix meaning. `VER` prints nothing, so a `FOR /F` over `ver`
  captures an empty line.
- **`DATE`/`TIME` never prompt.** Real `cmd.exe` `date`/`time` (without `/t`)
  prompt for a new value; this port prints and returns, behaving like `/t` so it
  can't block a non-interactive script (`misc.go:59`).
- **External commands run via the host**, with the argv[0] backslash-normalized
  (`executor.go:1619`). Windows-specific console behavior, `.com`/`.exe`
  resolution semantics, and the Windows `PATHEXT` search order are not
  reproduced; `.bat`/`.cmd` resolution probes CWD then `PATH`
  (`resolveBat`, `executor.go:1784`), accepting both `;` and `:` as `PATH`
  separators (`executor.go:1819`).
- **`TEMP`/`TMP`** are seeded from `TMPDIR` (or `/tmp`) when unset (`env.go:51`).
- **`FOR /F skip=N` is not applied** (parsed as a keyword only; see §1). Scripts
  relying on `skip=` to drop header lines will see those lines.
- **No `%CD%` pseudo-variable.** (`%CD%` is not handled in `Env.Get`; use the
  `CD` builtin with no args, which prints the working directory, `misc.go:25`.)
  `%DATE%`/`%TIME%`/`%RANDOM%`/`%ERRORLEVEL%` *are* implemented as dynamic
  pseudo-vars (see §2).
