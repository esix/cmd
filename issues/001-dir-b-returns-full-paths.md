# `dir /b` returns absolute paths instead of bare filenames

## Summary

`dir /b <pattern>` (bare format, **non-recursive**) emits the absolute
path of every match instead of just the filename. Real cmd.exe `dir /b`
prints bare names only (no directory, no header). The full-path form is
correct *only* when `/S` (recurse) is also given — that is the one case
where cmd.exe's `/B` includes the path.

This is the highest-impact of the three issues here: any script that does
`for /f %%f in ('dir /b "somedir\*.ext"') do ...` and then builds a name
from `%%f` (very common) silently gets paths, not names.

## Reproduction

```bat
@echo off
setlocal EnableDelayedExpansion
set "DIR=/Users/esix/pro/gw-batsic/src/rtl"
for /f "delims=" %%f in ('dir /b "%DIR%/*.bat"') do set "_last=%%f"
echo last=[!_last!]
```

Observed (current build):

```
last=[/Users/esix/pro/gw-batsic/src/rtl/_cmp.bat]
```

Expected (real cmd.exe):

```
last=[_cmp.bat]
```

The breakage cascade in real-world code: a script that strips the
extension and uses the result as a variable-name suffix —

```bat
for /f "delims=" %%f in ('dir /b "%DIR%/*.bat"') do (
  set "_bn=%%f"
  set "_bn=!_bn:.bat=!"      &:: intends "PEND"; actually ".../rtl/PEND"
  set "_impl_!_bn!=1"        &:: sets _impl_/Users/.../rtl/PEND (unusable)
)
echo [!_impl_PEND!]          &:: empty — lookup by bare name never matches
```

prints `[]` instead of `[1]`. (This is exactly what tripped up a harness
I was building; I first mis-diagnosed it as a dynamic-`set` bug. It is
not — `set "_x_!v!=1"` inside a `for /f` body works fine. The sole cause
is `dir /b` returning the path.)

## Root cause

`executor/executor.go`, `runDirCommand` (~line 1348). Both the recursive
(`/S`) and non-recursive branches build `matches` with `filepath.Abs(...)`
unconditionally:

- non-recursive branch (~line 1417): `abs, _ := filepath.Abs(m); matches = append(matches, abs)`
- recursive `/S` branch (~line 1405): same, `filepath.Abs(path)`

The doc-comment notes the function was written for `dir /A-D /S /B pattern`
(recursive bare listing), where absolute paths *are* the cmd.exe result —
so the abs-path output is right for that case but wrong for plain `/B`.

## Suggested fix

In the non-recursive branch (and only there), emit `filepath.Base(m)`
instead of the absolute path. Keep `filepath.Abs` for the `/S` recursive
branch. Roughly:

```go
name := m
if !recursive {
    name = filepath.Base(m)
} else {
    name, _ = filepath.Abs(m)
}
matches = append(matches, name)
```

(cmd.exe actually prints `/S /B` paths relative to the searched dir with a
leading absolute root; matching `filepath.Abs` for `/S` is close enough
for the scripts that use it.)

## Secondary, related: standalone `dir /b` fails

`dir /b <pattern>` as a top-level statement (not inside `for /f`) prints
`File Not Found: /b` — `runDirCommand` is only invoked from the `for /f`
command-capture path, so a bare `dir` statement falls through to the
generic command path and treats `/b` as a missing file. Lower priority
(scripts almost always use `dir` via `for /f`), but worth wiring the same
handler into the statement path.
