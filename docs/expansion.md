# Variable Expansion

This shell reproduces cmd.exe's **two-phase** expansion model. Understanding the
split between the two phases — and exactly which syntax each phase owns — is the
key to understanding every "why doesn't this expand?" question in the codebase.

The two phases are:

1. **Early `%`-expansion** ([`expander/early.go`](../expander/early.go)) runs on
   the **raw, un-tokenized line** before the parser sees it. It resolves
   `%VAR%`, positional `%0`–`%9`, and `%~[mods]N`, and it carefully *preserves*
   `%%`, `%%X`, and `%%~modsX` so the parser can turn them into FOR-variable
   nodes.
2. **Delayed `!`-expansion** ([`expander/expander.go`](../expander/expander.go))
   runs at **statement execution time**, after parsing, and only when delayed
   expansion is enabled. It resolves `!VAR!` (with all the same `:~` / `:old=new`
   sub-modifiers as `%VAR%`).

A third concern lives in `expander.go` but is not really a "phase": the runtime
resolution of the parsed AST word parts (`ExpandWord`), which handles the tilde
modifiers (`%~dp0`, `%%~nf`), substrings parsed into `SubstringVarPart`, and so
on. This document covers all three.

See also: the parser produces the `WordPart` nodes consumed here
(`parser/ast.go:201`).

---

## Phase 1 — `ExpandPercent` on the raw line

```go
func ExpandPercent(line string, e *env.Env, positional []string) string
```

`ExpandPercent` ([`early.go:14`](../expander/early.go)) is called by the
executor on the raw source line *before* `parser.ParseLineWithOpts`. The two
primary call sites are:

- The top of `runLine` — `executor.go:102` — the normal one-line-at-a-time path.
- The block-execution loop — `executor.go:231` and `executor.go:883` — where
  each stored raw sub-line is re-expanded before being parsed (so that a `%VAR%`
  written by an earlier statement in the same block is visible to a later one).

It is also used as the *final* step of CALL's extra double-expansion round
(`expandCallText`, `executor.go:1064`), after bangs are expanded, so
`%`-references that a `!...!` substitution introduced still get resolved
(this is what makes `call set "y=%%x%%"` work).

Because this runs on the raw byte stream, it is a hand-written scanner, not a
regex. Walking the cases in order:

### `%VAR%` — plain variable

```bat
set FOO=bar
echo %FOO%        & rem -> bar
echo %MISSING%    & rem -> (empty; env.Get returns "" for unset vars)
```

Resolution is `e.Get(name)` (`early.go:134`), which is **case-insensitive** and
returns `""` for unset names (`env/env.go:66`). `Get` also serves the dynamic
pseudo-variables `%ERRORLEVEL%`, `%RANDOM%`, `%DATE%`, `%TIME%` unless the user
has explicitly assigned them.

A `%` with no matching close `%` is emitted literally and scanning continues
(`early.go:94`). An empty `%%`-style `%...%` with nothing between the percents is
handled by the `%%` branch below; a `%` immediately followed by a non-special
char but never closed simply passes through.

### `%0`–`%9` — positional parameters

```bat
rem  called as:  script.bat alpha beta
echo %0   & rem -> script.bat   (positional[0])
echo %1   & rem -> alpha
echo %1   & rem -> (empty if no such argument)
```

A `%` followed by a single digit is replaced by `positional[digit]`, or by
nothing if the index is out of range (`early.go:84`). `positional` is threaded
in from the executor (`ex.positional`).

### `%~[mods]N` — tilde-modified positional

```bat
rem  called as:  build.bat "C:\src\app.bat"
echo %~1    & rem -> C:\src\app.bat   (quotes stripped)
echo %~n1   & rem -> app              (name, no extension)
echo %~x1   & rem -> .bat
echo %~nx1  & rem -> app.bat
echo %~dp0  & rem -> /abs/dir/of/script/   (drive+path of arg 0)
```

The scanner reads `~`, then a run of ASCII letters (the modifiers), then a single
digit (`early.go:62`). The modifiers are lower-cased and handed to
`tildeExpand` (`early.go:140`), which strips quotes and applies the path
decomposition. **Note:** `early.go` contains its own `tildeExpand` used at this
phase; the richer `applyTildeMods` in `expander.go` is the one used for the
parsed-AST tilde forms (see below). They share `stripQuotes` and the same path
logic, but `applyTildeMods` adds the `s`/`a`/`t`/`z` modifiers and the
"recognized" rule. The early `tildeExpand` covers the common `d p n x f`
subset; `%~1` with no modifiers just strips quotes.

If `%~` is not followed by a valid `letters+digit` sequence, the `%` is emitted
literally (`early.go:78`).

### `%%`, `%%X`, `%%~modsX` — preserved for the parser

This is the subtle part. FOR-loop variables (`%%i`, `%%~nf`) are *not* known at
line-expansion time, so `ExpandPercent` must hand them through untouched for the
parser to recognize. The `%%` branch (`early.go:30`) distinguishes three shapes:

| Input          | Action                                          | Reason |
|----------------|-------------------------------------------------|--------|
| `%%~<letters>` | emit the whole `%%~modsX` token verbatim        | tilde-modified FOR var, e.g. `%%~nf` (`early.go:32`) |
| `%%X` (single letter, **not** followed by alnum/`_`) | emit `%%X` verbatim | FOR variable reference (`early.go:46`) |
| `%%` (anything else)  | emit `%%` verbatim                       | literal `%` for the parser / `SET /A` modulo (`early.go:55`) |

```bat
for %%i in (a b c) do echo %%i      & rem  %%i survives ExpandPercent intact
for %%f in (*.txt) do echo %%~nf    & rem  %%~nf survives intact
set /a x = 7 %% 3                    & rem  %% kept; SET /A sees the modulo op
echo 50%% done                       & rem  %% kept; collapses to "%" later
```

The "not followed by alnum" rule (`nextIsAlnum`, `early.go:46`) prevents
`%%abc` from being treated as a FOR variable `%%a` followed by `bc`. The full
`isAlphaNum` includes `_` (`early.go:236`).

### `%VAR:~N,M%` — substring (early form)

When the name between the percents contains `:~`, the text after it is a
substring spec handled by `substringExpand` (`early.go:110`). See
[Substrings](#substrings-vartilden-and-vartildenm) for the spec semantics.

```bat
set S=abcdef
echo %S:~2%      & rem -> cdef
echo %S:~2,3%    & rem -> cde
echo %S:~-2%     & rem -> ef
echo %S:~1,-1%   & rem -> bcde
```

### `%VAR:old=new%` — string replacement (early form)

When the name contains a `:` (but not `:~`) followed by `=`, it is a string
replacement (`early.go:120`), implemented as `strings.ReplaceAll`.

```bat
set P=a.b.c
echo %P:.=-%     & rem -> a-b-c
echo %P:b=%      & rem -> a..c   (delete all "b")
```

---

## Phase 2 — delayed `!VAR!` expansion at run time

When `SETLOCAL EnableDelayedExpansion` is active (`env.DelayedExpansion`,
`env/env.go:25`), `!...!` references are resolved **after** parsing, at the
moment a statement runs. This is what lets a variable assigned earlier *in the
same block / FOR iteration* be re-read with its current value, instead of the
value frozen at parse time.

The runtime entry points (all in `executor.go`) are:

- `ExpandBangs` over a whole string — e.g. FOR `/F` source (`executor.go:1409`),
  FOR `/D` items (`executor.go:1099`), FOR `/L` range bounds (`executor.go:1159`),
  FOR in-list items (`executor.go:1184`), redirection targets
  (`executor.go:1838`), `SET /A` operands (`executor.go:612`).
- `expandBangs` invoked *inside* `ExpandWord` for every `LiteralPart` when
  delayed expansion is on (`expander.go:21`) — so a literal token that contains
  `!FOO!` expands when the word is evaluated.
- `DelayedVarPart` nodes the parser emitted go straight to `expandDelayedRef`
  (`expander.go:33`).

```go
func ExpandBangs(s string, e *env.Env) string   // expander.go:340
func expandBangs(s string, e *env.Env) string    // expander.go:344
```

`expandBangs` ([`expander.go:344`](../expander/expander.go)) scans for a pair of
`!`. An unmatched `!` is emitted literally and scanning continues
(`expander.go:355`); an empty `!!` emits a single `!` (`expander.go:361`). The
inner name goes to `resolveDelayedRef`.

```bat
setlocal enabledelayedexpansion
set X=start
set X=changed & echo !X!     & rem -> changed  (NOT "start"; read at run time)
```

Contrast with `%X%`, which in the same line would resolve to `start` because it
was frozen during phase 1 before the second `set` ran.

### `resolveDelayedRef` — the shared inner resolver

```go
func resolveDelayedRef(name string, e *env.Env) string   // expander.go:234
```

This is the heart of delayed expansion (`expander.go:234`) and is also reused by
the early-phase paths indirectly. The order of operations matters:

1. **Nested `%`/`%%` first** — `name = expandNestedPercents(name, e)`
   (`expander.go:235`). This resolves `%%i` (FOR var) and `%VAR%` *inside* the
   `!...!` name, which is essential for the FOR-body idiom where the substring
   index is itself a loop variable:

   ```bat
   setlocal enabledelayedexpansion
   set _s=ABCDEF
   for /l %%i in (0,1,5) do echo !_s:~%%i,1!
   rem  %%i is substituted into the name BEFORE the substring is taken:
   rem  -> A B C D E F  (one per line)
   ```

2. **`VAR:~N,M`** → substring via `substringExpand` (`expander.go:236`).
3. **`VAR:old=new`** → `strings.ReplaceAll` (`expander.go:241`).
4. **`VAR=`** → the `VAR-equals` quirk (see below).
5. Otherwise → plain `e.Get(name)`.

### The `VAR-equals` quirk (`!VAR=!`)

This is a deliberately-reproduced cmd.exe oddity (`expander.go:252`):

> A delayed reference whose name **ends in `=`** and contains **no `:`** yields
> `VAR`'s value with **one leading `=` stripped if present, otherwise the empty
> string**.

```go
if len(name) > 1 && name[len(name)-1] == '=' && !strings.Contains(name, ":") {
    val := e.Get(name[:len(name)-1])
    if strings.HasPrefix(val, "=") {
        return val[1:]
    }
    return ""
}
```

```bat
setlocal enabledelayedexpansion
set "EQ==X"          & rem  value literally begins with "="
echo !EQ=!           & rem -> X      (leading "=" stripped)
set "NE=Y"
echo !NE=!           & rem -> (empty; value did not start with "=")
```

**Why it matters — gw-batsics char→hex table.** In cmd.exe you cannot store a
value that begins with `=` through ordinary `set name=value` syntax (the first
`=` is treated as the name/value separator), yet such values *can* exist and the
`!VAR=!` form is the canonical way to read them back minus the leading `=`. The
`gw-batsic`-style character-to-hex lookup tables encode the troublesome `=`
character by relying precisely on this strip-one-leading-equals behavior; if we
did not reproduce it, those tables would silently mis-encode `=`. Hence the
exact "strip iff leading, else empty" semantics are load-bearing, not incidental.

---

## Runtime AST resolution — `ExpandWord`

```go
func ExpandWord(parts []parser.WordPart, e *env.Env, positional []string) string
```

`ExpandWord` ([`expander.go:15`](../expander/expander.go)) collapses a parsed
word (a `[]parser.WordPart`) into one string. It is reached through the
executor's `expandParts` helper (`executor.go:1906`) and `ExpandArgs`
(`expander.go:50`). It dispatches on the concrete `WordPart` type
(`parser/ast.go:201`):

| WordPart            | Handling                                                              | Source |
|---------------------|----------------------------------------------------------------------|--------|
| `*LiteralPart`      | text as-is; if delayed expansion on, run `expandBangs` on it          | `expander.go:19` |
| `*VarPart`          | `Positional>=0` → positional arg, else `e.Get(Name)`                  | `expander.go:25` |
| `*DelayedVarPart`   | `expandDelayedRef(Name)`                                              | `expander.go:33` |
| `*TildeVarPart`     | FOR-var form (`Name!=""`) → `applyTildeMods(e.Get(Name), …)`; else `expandTilde` over positional | `expander.go:35` |
| `*SubstringVarPart` | `expandSubstring`                                                     | `expander.go:42` |

Note that the literal `expandBangs` pass means delayed expansion is applied even
to tokens the parser classified as literal text — `!FOO!` embedded mid-word
still expands at evaluation time.

---

## Tilde modifiers — `applyTildeMods`

```go
func applyTildeMods(val, modifiers string) string   // expander.go:71
```

`applyTildeMods` ([`expander.go:71`](../expander/expander.go)) is the full
implementation shared by both tilde forms:

- **Call-argument form** `%~dp1` → `expandTilde` (`expander.go:59`) pulls the
  value from `positional[pt.Positional]`.
- **FOR-variable form** `%%~nf` → `ExpandWord` calls `applyTildeMods(e.Get(pt.Name), …)`
  directly (`expander.go:38`); the FOR variable's value lives in the env under
  that single-letter name.

With **no modifiers** it merely strips one layer of surrounding double quotes
(`stripQuotes`, `expander.go:372`).

### Backslash normalization

```go
path := strings.ReplaceAll(stripQuotes(val), "\\", "/")
```

Before any path work, backslashes are converted to forward slashes
(`expander.go:80`). On Unix `\` is not a path separator, so without this a value
like `..\..\x` would never have its `..` segments resolved by `filepath.Abs` /
`filepath.Clean`. With normalization, `..` correctly collapses:

```bat
rem  arg1 = C:\a\b\..\c.txt   →  normalized to /…/a/b/../c.txt  →  cleaned
echo %~f1    & rem -> /abs/a/c.txt   (".." resolved away)
```

The value is then made absolute with `filepath.Abs` (`expander.go:84`) so that
`d`/`p`/`f` produce fully-qualified results, matching cmd.exe where `%~dp0` and
`%~f1` are always absolute.

### Path modifiers `d p n x f s`

Applied piecewise and concatenated (so combinations like `dp`, `nx`, `dpnx`
compose), in this fixed order regardless of how the user wrote them:

| Mod | Meaning                          | Implementation |
|-----|----------------------------------|----------------|
| `f` | full absolute path (**overrides** the piecewise build; returns immediately) | `expander.go:107` |
| `d` | drive — on Unix, a leading `/` if the path is absolute | `expander.go:114` |
| `p` | directory part (`filepath.Dir`), trailing `/` ensured; de-dupes the leading `/` when combined with `d` | `expander.go:121` |
| `n` | base name without extension | `expander.go:134` |
| `x` | extension only (`filepath.Ext`, includes the dot) | `expander.go:141` |
| `s` | short 8.3 name — no Unix equivalent; **alone** yields the full path | `expander.go:146` |

```bat
rem  arg1 = "/home/u/proj/main.bat"
echo %~d1     & rem -> /
echo %~p1     & rem -> /home/u/proj/
echo %~dp1    & rem -> /home/u/proj/      (d's "/" + p, not "//")
echo %~n1     & rem -> main
echo %~x1     & rem -> .bat
echo %~nx1    & rem -> main.bat
echo %~dpnx1  & rem -> /home/u/proj/main.bat
echo %~f1     & rem -> /home/u/proj/main.bat
echo %~s1     & rem -> /home/u/proj/main.bat   (no 8.3; full path)
```

The `d`+`p` de-duplication (`expander.go:125`) strips the leading `/` that `d`
already emitted from the front of `filepath.Dir`'s result, so `%~dp1` of
`/home/u/proj/main.bat` is `/home/u/proj/` and never `//home/...`.

### The recognized-vs-empty-result rule

```go
recognized := false
// ...each handled modifier sets recognized = true...
if !recognized {
    return stripQuotes(val)   // unknown modifier: fall back to the value
}
return result                  // recognized: empty result is legitimate
```

This rule (`expander.go:112`, `156`) is essential for the
**extension-of-extensionless-file** case. If we just checked `result == ""` and
fell back to the value, `%~x1` of a file with no extension would wrongly return
the whole filename. Instead, because `x` *was recognized*, the empty extension is
returned as-is:

```bat
rem  arg1 = "/home/u/README"
echo %~x1     & rem -> (empty; recognized, so empty is correct)
echo %~q1     & rem -> /home/u/README   (q is not a modifier; fall back to value)
```

(The early-phase `tildeExpand` in `early.go:190` uses the older
`result == ""` fallback and therefore does *not* implement this rule; the
recognized-flag behavior lives in the AST-driven `applyTildeMods`.)

### File-info modifiers `z t a` and `fileAttrString`

These are `os.Stat` queries handled before the path build (`expander.go:90`). If
the stat fails, the result is `""`. Only one of `z`/`t`/`a` is honored per
reference (first match in the switch wins):

| Mod | Meaning                              | Implementation |
|-----|--------------------------------------|----------------|
| `z` | file size in bytes                   | `expander.go:96` |
| `t` | mod time, formatted `01/02/2006 03:04 PM` | `expander.go:98` |
| `a` | 9-char attribute string              | `expander.go:100` |

```bat
echo %~z1     & rem -> 1024              (bytes)
echo %~t1     & rem -> 06/24/2026 09:17 PM
echo %~a1     & rem -> dr-------         (dir + read-only flags)
```

`fileAttrString` (`expander.go:164`) builds the cmd.exe-style 9-character string.
Unix has no DOS attribute bits, so only two are approximated: position 0 is `d`
for a directory, and position 1 is `r` when the owner-write bit is clear
(`Mode().Perm()&0o200 == 0`). All other positions stay `-`.

---

## Substrings — `%VAR:~N%` and `%VAR:~N,M%`

There are two near-identical implementations:

- `substringExpand(val, spec string)` (`early.go:196`) parses the spec text
  itself (used by the phase-1 `%VAR:~…%` path and by `resolveDelayedRef`).
- `expandSubstring(pt *SubstringVarPart, e)` (`expander.go:176`) reads the
  already-parsed `Start`/`Length`/`HasLength` fields from the AST node.

Both implement the same arithmetic:

- **Negative start** counts from the end: `start = n + start`, clamped to `0`
  (`early.go:210`, `expander.go:186`).
- A `start > n` yields `""`.
- **No length** → from `start` to end of string.
- **Negative length** is an *end offset from the right*: `end = n + length`; if
  `end <= start` the result is empty (`early.go:222`, `expander.go:201`).
- **Positive length** → `val[start:start+length]`, with `end` clamped to `n`.

```bat
set S=0123456789
echo %S:~3%      & rem -> 3456789
echo %S:~3,4%    & rem -> 3456
echo %S:~-3%     & rem -> 789       (3 from the end)
echo %S:~0,-3%   & rem -> 0123456   (drop last 3)
echo %S:~2,-2%   & rem -> 234567    (from 2, stop 2 before end)
echo %S:~99%     & rem -> (empty; start past end)
```

One difference to be aware of: `expandSubstring` short-circuits to `""` when the
variable is unset/empty (`expander.go:178`), whereas `substringExpand` will run
the math on an empty string and also return `""` — the observable result is the
same.

---

## String replacement — `%VAR:old=new%`

Implemented as `strings.ReplaceAll(val, old, new)` in both phases
(`early.go:127`, `expander.go:247`). The split point is the **first `=` after the
`:`**; everything before it is `old`, everything after is `new` (which may be
empty to delete).

```bat
set PATHX=a;b;c
echo %PATHX:;=,%    & rem -> a,b,c
echo %PATHX:b=%     & rem -> a;;c     (delete "b")
echo %PATHX:x=y%    & rem -> a;b;c    (no match; unchanged)
```

Note this implementation does **not** support cmd.exe's `%VAR:*old=new%`
leading-anchor variant — the parser/resolver split on the first `=` and treat the
literal text (including a leading `*`) as `old`.

---

## Nested name resolution — `expandNestedPercents`, `ExpandName`, `ExpandForVars`

```go
func ExpandName(s string, e *env.Env) string       // expander.go:264
func ExpandForVars(s string, e *env.Env) string     // expander.go:274
func expandNestedPercents(s string, e *env.Env) string  // expander.go:279
```

`expandNestedPercents` ([`expander.go:279`](../expander/expander.go)) is the
mini-scanner that resolves `%%X` FOR-variable references and `%VAR%` references
that appear *inside another name* — most importantly inside a delayed-ref name
(called first thing in `resolveDelayedRef`, `expander.go:235`).

### How `%%X` FOR variables resolve

The crucial detail: a FOR variable `%%X` resolves to **the environment variable
whose name is that single letter** (`expander.go:307`):

```go
ch := s[i+2]
if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
    sb.WriteString(e.Get(string(ch)))   // env var named e.g. "i"
    i += 3
}
```

That is, FOR loop variables are stored as ordinary single-character environment
entries; `%%i` is literally "the value of env var `i`". A `%%~modsX` form
(`expander.go:291`) peels the trailing letter off as the variable and feeds the
preceding letters to `applyTildeMods(e.Get(letter), mods)`:

```bat
rem  inside a FOR body, %%f holds "/path/to/file.txt"
echo %%~nf      & rem -> file   (env var "f" → applyTildeMods(…, "n"))
echo %%~dpf     & rem -> /path/to/
```

A plain `%%` not followed by a letter is emitted as `%%` unchanged
(`expander.go:314`).

### `ExpandName`

`ExpandName` (`expander.go:264`) = optional `expandBangs` (if delayed expansion
is on) followed by `expandNestedPercents`. Its two call sites:

- **ECHO with raw text** (`executor.go:569`): the raw echoed text already went
  through phase-1 `%`-expansion, so `ExpandName` finishes the job by resolving
  surviving `!VAR!` and `%%X`/`%VAR%` references before printing.
- **SET name resolution** (`executor.go:601`): the *target name* of a `set` may
  itself be computed, e.g. `set "%%a=%%b"` inside a FOR body — the name `%%a`
  must resolve to the loop variable's value before the assignment.

```bat
setlocal enabledelayedexpansion
for %%a in (DEST) do set "%%a=hello"
rem  the SET target name "%%a" resolves to "DEST" via ExpandName,
rem  so this assigns  DEST=hello
echo !DEST!     & rem -> hello
```

### `ExpandForVars`

`ExpandForVars` (`expander.go:274`) is just `expandNestedPercents` under a
descriptive name. It is used for FOR `/IN` list items where the list itself
references the outer loop variable (`executor.go:1185`), e.g.:

```bat
for %%j in (a b) do for %%p in (%%j) do echo %%p
rem  the inner list "(%%j)" needs %%j substituted before iterating
```

---

## Putting the phases together — worked example

```bat
@echo off
setlocal enabledelayedexpansion
set NAME=world
for %%g in (1 2) do (
    set MSG=hi %NAME% iter %%g
    echo !MSG!
)
```

Per raw line, phase 1 (`ExpandPercent`) runs first:

- `%NAME%` is frozen to `world` *at parse time* — but here it is inside the FOR
  body, which is re-expanded per stored sub-line (`executor.go:883`), so by the
  time the `set` runs, `%NAME%` → `world`.
- `%%g` is **preserved** verbatim by the `%%X` rule (`early.go:46`) so the parser
  turns it into a FOR variable; at run time `expandNestedPercents` resolves it to
  env var `g` (`expander.go:307`), giving `1` then `2`.
- `!MSG!` is left for phase 2; `expandBangs` (`expander.go:344`) reads `MSG`'s
  *current* value each iteration, printing `hi world iter 1` then
  `hi world iter 2`.

Had `echo %MSG%` been used instead of `echo !MSG!`, phase-1 expansion of `%MSG%`
would have happened before the `set MSG=` on the same logical pass, demonstrating
exactly why delayed `!...!` exists.

---

## Related docs

- Parser word-part nodes consumed here: see `parser/ast.go:201`.
- The `env.Get` lookup and dynamic pseudo-variables (`%RANDOM%`, `%DATE%`,
  `%TIME%`, `%ERRORLEVEL%`): see `env/env.go:66`.
