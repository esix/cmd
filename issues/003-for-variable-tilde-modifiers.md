# FOR-variable tilde modifiers (`%%~nf`, `%%~xf`, `%%~ff`, …) are not expanded

## Summary

Tilde modifiers on a FOR loop variable — `%%~nf` (name without
extension), `%%~xf` (extension), `%%~ff` (full path), `%%~dpf`, etc. — are
emitted literally instead of being applied. They are the standard way to
decompose a path inside a `for` body, so any script that iterates files
and extracts the basename/extension is affected.

Note: the analogous modifiers on **call arguments** (`%~nx1`, `%~n1`,
`%~dp1`) *do* work in this port. The gap is specifically the FOR-variable
form `%%~xf`.

## Reproduction

```bat
@echo off
for %%f in (/tmp/example.bat) do (
  echo name=[%%~nf]
  echo ext=[%%~xf]
  echo full=[%%~ff]
)
```

Observed (current build):

```
name=[%%~nf]
ext=[%%~xf]
full=[%%~ff]
```

Expected (real cmd.exe):

```
name=[example]
ext=[.bat]
full=[/tmp/example.bat]
```

(Run as a .bat file, so `%%f` is the correct doubled form. The literal
`%%~nf` in the output confirms the modifier text passes through
untouched.)

## Where

`executor/executor.go` — the FOR-variable substitution that replaces
`%%x` occurrences in the loop body with the current element. It handles
the plain variable but not the `~`-modifier syntax
(`%%~[dpnxsfat...]<var>`). The expansion logic that already exists for
call-arg modifiers (`%~n1` etc.) can likely be reused.

## Modifiers to support (cmd.exe set)

`~f` full path · `~d` drive · `~p` path · `~n` name · `~x` extension ·
`~s` short name · `~a` attributes · `~t` timestamp · `~z` size · and
combinations like `~dp`, `~nx`, `~dpnx`. At minimum `~n`, `~x`, `~f`,
`~dp`, `~nx` cover the common cases.

## Impact

Forces scripts to fall back to string-substitution hacks
(`set "n=%%f" & set "n=!n:.bat=!"`) to strip extensions — which then
interact badly with issue 001 (`dir /b` full paths). Implementing `%%~nf`
would let the natural idiom work.
