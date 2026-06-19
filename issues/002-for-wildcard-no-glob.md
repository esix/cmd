# `for %%f in (wildcard)` does not expand wildcards

## Summary

A plain `for` loop over a filename pattern — `for %%f in (*.bat)` or
`for %%f in ("dir\*.bat")` — matches nothing. The set element is treated
as a literal string (or simply skipped) rather than expanded against the
filesystem. Real cmd.exe globs the pattern and iterates the matching
files in the current directory.

## Reproduction

```bat
@echo off
setlocal EnableDelayedExpansion
set "_n=0"
for %%f in ("src/rtl/*.bat") do set /a "_n+=1"
echo count=!_n!
```

Observed (current build), run from a dir where `src/rtl/*.bat` matches 100+ files:

```
count=0
```

Expected (real cmd.exe): `count=` the number of matching files.

Also fails for an unquoted bare pattern `for %%f in (src\rtl\*.bat)` and
for a CWD-relative `for %%f in (*.bat)`.

## Notes / scope

- `for /f` over a captured `dir /b` command *does* iterate files — only
  the direct-glob `for %%f in (pattern)` form is unimplemented. (So the
  standard workaround is `for /f "delims=" %%f in ('dir /b "pat"') do` —
  but see issue 001: that currently yields full paths.)
- cmd.exe only globs in `for %%f in (...)` when the element contains
  wildcard characters (`*` or `?`); literal sets like
  `for %%f in (a b c)` must keep their current literal behavior. The fix
  should glob an element only when it contains `*`/`?`, and fall back to
  the literal token when the glob matches nothing (cmd.exe yields the
  literal pattern once in that case — verify against real cmd.exe).

## Where

`executor/executor.go` — the `for %%x in (set)` handler (the
`execForRange`/`execForInList` family; the in-list iteration path that
splits the parenthesized set into elements). It currently emits each
element verbatim with no filesystem expansion.

## Impact

Scripts that enumerate files the idiomatic way (`for %%f in (*.ext)`)
find nothing. Worked around in my harness by switching to
`for /f ('dir /b ...')`, but that is exactly the path with issue 001.
