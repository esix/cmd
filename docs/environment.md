# Environment: the variable store and scopes

The `env` package (`env/env.go`) implements the shell's variable store. A single
`*env.Env` value is created at startup and threaded through the executor for the
lifetime of the process. It owns three things:

1. The variable name/value table (case-insensitive, original-case preserving).
2. A handful of process-level flags the executor reads and writes
   (`ExitCode`, `FileMode`, `Echo`, `DelayedExpansion`).
3. The `SETLOCAL`/`ENDLOCAL` scope stack.

This document is exhaustive about the cmd.exe quirks the code deliberately
reproduces — those are the load-bearing details. See
[Expansion](expansion.md) for how `%VAR%` / `!VAR!` references are resolved into
calls to `Get`, and [Architecture](architecture.md) for how the executor wires
`Push`/`Pop` to the `SETLOCAL`/`ENDLOCAL` AST nodes.

## The `Env` struct

```go
type Env struct {
    vars             map[string]string
    names            map[string]string // UPPER key -> original-case name
    stack            []scope           // for SETLOCAL / ENDLOCAL
    ExitCode         int
    FileMode         bool // true when executing a .bat file (affects %% vs % in FOR)
    Echo             bool // ECHO ON / ECHO OFF
    DelayedExpansion bool // SETLOCAL EnableDelayedExpansion
}
```
(`env/env.go:18`)

- `vars` is the actual store. Every key is uppercased before use, so lookups
  and assignments are case-insensitive (BAT variables are case-insensitive).
- `names` is a parallel map keyed by the same uppercase key, whose value is the
  original-case spelling used when the variable was first created. This is *only*
  for display; it has no effect on lookup. See
  [Case-insensitivity and original-case preservation](#case-insensitivity-and-original-case-preservation).
- `stack` is the `SETLOCAL` scope stack (a slice used as a LIFO stack). See
  [The SETLOCAL/ENDLOCAL scope stack](#the-setlocalendlocal-scope-stack).

The four exported flags are mutated directly by the executor rather than through
methods:

- `ExitCode` — the last command's exit status. Surfaced as the dynamic
  `%ERRORLEVEL%` (see below). It is a plain `int`, not stored in `vars`.
- `FileMode` — `true` while running a `.bat` script, `false` at an interactive
  prompt. It governs the `%%var` vs `%var` distinction in `FOR` loops (a parser
  concern; `env` only holds the flag).
- `Echo` — the `ECHO ON`/`ECHO OFF` state. Defaults to `true` (see `New`).
- `DelayedExpansion` — whether `!VAR!` delayed expansion is active. Toggled by
  `SETLOCAL EnableDelayedExpansion` / `DisableDelayedExpansion`, and — critically —
  saved and restored by the scope stack.

## Case-insensitivity and original-case preservation

Every public entry point uppercases the name before touching `vars`:

```go
func (e *Env) Get(name string) string {
    upper := strings.ToUpper(name)
    ...
}
```
(`env/env.go:66`)

So `%Path%`, `%PATH%`, and `%path%` all resolve to the same slot.

cmd.exe, however, remembers the *spelling* used the first time a variable is
created, and reproduces it in `SET` listings. The `names` map provides this. On
the first `Set` of a name, the original-case spelling is captured; subsequent
re-assignments keep the original spelling:

```go
func (e *Env) Set(name, value string) {
    upper := strings.ToUpper(name)
    if _, exists := e.vars[upper]; !exists {
        e.names[upper] = name
    }
    e.vars[upper] = value
}
```
(`env/env.go:109`)

`DisplayName` reads it back, falling back to the passed-in name if the key is
unknown:

```go
func (e *Env) DisplayName(name string) string {
    if orig, ok := e.names[strings.ToUpper(name)]; ok {
        return orig
    }
    return name
}
```
(`env/env.go:126`)

`SET` with no arguments (the "list all" path) iterates the uppercase keys from
`All()`, sorts them (so listings are alphabetical by *uppercase* key), and prints
`DisplayName(k)=value` (`executor/builtins/set.go:16`). Worked example:

```bat
set MyVar=hello
set MYVAR=world
set
```
The listing shows `MyVar=world` — the value from the second assignment, but the
spelling from the first creation. (Re-assignment does not refresh `names`, since
the key already exists in `vars`.)

### Quirk: the names/vars maps can desync if you bypass `Set`

`Set`/`Unset` keep `vars` and `names` consistent. But `New` and the `TEMP`/`TMP`
fallbacks below write into `vars` directly for the synthesised entries, so a key
can exist in `vars` without an entry in `names`. `DisplayName` handles this by
falling back to the queried name, so listing such a variable shows whatever case
the caller passed (in `SET`'s case, the uppercase key).

## `New`: pre-population and Windows-to-Unix mapping

```go
func New() *Env {
    e := &Env{
        vars:  make(map[string]string),
        names: make(map[string]string),
        Echo:  true,
    }
    for _, kv := range os.Environ() {
        parts := strings.SplitN(kv, "=", 2)
        if len(parts) == 2 {
            upper := strings.ToUpper(parts[0])
            e.vars[upper] = parts[1]
            e.names[upper] = parts[0]
        }
    }
    ...
}
```
(`env/env.go:36`)

Every process environment variable is imported. `Echo` starts `true` (cmd.exe
starts with echo on). Note: this is the one path that *does* populate `names`
alongside `vars` directly (not via `Set`), so imported vars list with their OS
spelling.

After import, two Windows-isms are synthesised when absent so that scripts which
read `%TEMP%`/`%TMP%` work on Unix:

```go
if e.vars["TEMP"] == "" {
    if tmpdir := os.Getenv("TMPDIR"); tmpdir != "" {
        e.vars["TEMP"] = strings.TrimSuffix(tmpdir, "/")
    } else {
        e.vars["TEMP"] = "/tmp"
    }
}
if e.vars["TMP"] == "" {
    e.vars["TMP"] = e.vars["TEMP"]
}
```
(`env/env.go:51`)

Details and edge cases:

- The macOS / Unix `TMPDIR` (e.g. `/var/folders/.../T/`) seeds `TEMP`, with its
  trailing slash trimmed (cmd.exe's `%TEMP%` has no trailing separator).
- If `TMPDIR` is unset, `TEMP` falls back to `/tmp`.
- `TMP` then mirrors `TEMP`.
- The `== ""` guard treats an empty value the same as missing, so a `TEMP=` in
  the OS environment is overwritten. A non-empty pre-existing `TEMP` (Windows
  parity or an explicit override) is left untouched.
- These synthetic entries are written straight into `vars` and never into
  `names` (see the desync quirk above).

## `Get` and the dynamic pseudo-variables

`Get` is the single read path. Besides a plain `vars` lookup it special-cases the
cmd.exe *dynamic* (a.k.a. magic) variables. Each is computed fresh on read, and
each is **shadowable**: if the user has explicitly assigned a variable of that
name, the stored value wins, exactly as in cmd.exe.

```go
func (e *Env) Get(name string) string {
    upper := strings.ToUpper(name)
    if upper == "ERRORLEVEL" {
        return fmt.Sprint(e.ExitCode)
    }
    if upper == "RANDOM" {
        if v, ok := e.vars["RANDOM"]; ok {
            return v
        }
        return strconv.Itoa(rand.Intn(32768))
    }
    if upper == "DATE" {
        if v, ok := e.vars["DATE"]; ok {
            return v
        }
        return now().Format("Mon 01/02/2006")
    }
    if upper == "TIME" {
        if v, ok := e.vars["TIME"]; ok {
            return v
        }
        t := now()
        cs := t.Nanosecond() / 10_000_000 // centiseconds
        return fmt.Sprintf("%2d:%02d:%02d.%02d", t.Hour(), t.Minute(), t.Second(), cs)
    }
    return e.vars[upper]
}
```
(`env/env.go:66`)

A missing variable returns `""` (Go's zero value for an absent map key) — there
is no "not defined" sentinel.

### `%ERRORLEVEL%`

Returns `fmt.Sprint(e.ExitCode)` — the decimal exit code of the last command,
not a value in `vars`. Note `ERRORLEVEL` is checked **before** the shadow check,
so... actually it is the one dynamic var that is *not* shadowable here: there is
no `if v, ok := e.vars["ERRORLEVEL"]` branch. `%ERRORLEVEL%` always reflects
`ExitCode`. (In real cmd.exe an explicit `set ERRORLEVEL=foo` does shadow it;
this port does not reproduce that particular shadow.)

### `%RANDOM%`

A fresh pseudo-random integer in `[0, 32767]` on each read — `rand.Intn(32768)`
yields `0..32767` inclusive, matching cmd.exe's 0–32767 range. The global
`math/rand` source is auto-seeded (Go ≥ 1.20), so the sequence differs between
runs without explicit seeding. An explicit `set RANDOM=...` shadows the dynamic
value (then `%RANDOM%` is a constant until unset).

### `%DATE%`

`now().Format("Mon 01/02/2006")` → e.g. `Mon 06/24/2026`: three-letter weekday,
a space, then `MM/DD/YYYY`. This is cmd.exe's default US locale format. Real
cmd.exe's format is locale-dependent; this port hard-codes US.

### `%TIME%`

24-hour `HH:MM:SS.CC`, e.g. `21:17:09.42`:

- Hour is `%2d` — **space-padded**, not zero-padded. So 9 a.m. renders as
  `" 9:05:…"` with a leading space, matching cmd.exe.
- Minutes and seconds are `%02d` (zero-padded).
- The fractional part is centiseconds (hundredths): `Nanosecond() / 10_000_000`,
  printed `%02d`. This is integer truncation, so `.999...` seconds shows as
  `.99`, never rounded up.

### Dynamic vars are absent from `SET` listings unless assigned

The dynamic vars live in `Get`, not in `vars`. `All()` copies only `vars`, so a
bare `SET` listing does **not** show `ERRORLEVEL`/`RANDOM`/`DATE`/`TIME` — again
matching cmd.exe — until the user explicitly assigns one, at which point it
becomes a normal stored variable and appears in the listing (and, for RANDOM /
DATE / TIME, simultaneously shadows the dynamic value).

### The `now` indirection

```go
var now = time.Now
```
(`env/env.go:105`)

`%DATE%` and `%TIME%` read through the package-level `now` variable rather than
calling `time.Now` directly, so a test can pin the clock by reassigning `now`.

## `Set`, `Unset`, `All`

- `Set(name, value)` (`env/env.go:109`) uppercases the key, captures the
  original-case spelling on first creation, and stores the value. Empty values
  are allowed via `Set`, but the `SET` builtin routes `SET VAR=` (empty value)
  to `Unset` instead — see below.
- `Unset(name)` (`env/env.go:118`) deletes from both `vars` and `names`. This is
  what `SET VAR=` (assignment to empty) maps to: in BAT, `SET VAR=` deletes the
  variable rather than storing an empty string. The `SET` builtin enforces this
  (`executor/builtins/set.go:123`): a parsed value of `""` calls `Unset`,
  otherwise `Set`.
- `All()` (`env/env.go:151`) returns a shallow copy of `vars` (uppercase keys →
  values). The copy means callers can iterate/sort without holding a reference
  to the live map. Used by `SET` for listing and prefix-matching.

## The SETLOCAL/ENDLOCAL scope stack

`SETLOCAL` saves the whole environment; `ENDLOCAL` (or the implicit end of a
called script) restores it. The model is a snapshot stack.

```go
type scope struct {
    vars             map[string]string
    names            map[string]string
    delayedExpansion bool
}
```
(`env/env.go:29`)

`Push` deep-copies `vars` and `names` into a new `scope`, captures the current
`DelayedExpansion` flag, and pushes it:

```go
func (e *Env) Push() {
    snapshot := make(map[string]string, len(e.vars))
    for k, v := range e.vars { snapshot[k] = v }
    nameSnap := make(map[string]string, len(e.names))
    for k, v := range e.names { nameSnap[k] = v }
    e.stack = append(e.stack, scope{
        vars: snapshot, names: nameSnap, delayedExpansion: e.DelayedExpansion,
    })
}
```
(`env/env.go:134`)

`Pop` swaps the live maps for the saved snapshots and restores the flag, returning
`false` if the stack is empty:

```go
func (e *Env) Pop() bool {
    if len(e.stack) == 0 {
        return false
    }
    top := e.stack[len(e.stack)-1]
    e.stack = e.stack[:len(e.stack)-1]
    e.vars = top.vars
    e.names = top.names
    e.DelayedExpansion = top.delayedExpansion
    return true
}
```
(`env/env.go:161`)

Important design points:

- **Snapshot-and-restore, not copy-on-write.** Mutations after `SETLOCAL`
  (`Set`/`Unset`) operate on the live maps. `ENDLOCAL` discards them wholesale by
  rebinding `e.vars`/`e.names` to the saved snapshots. There is no diffing.
- **`Push` snapshots by full copy**, so changes made inside the scope cannot leak
  into the saved maps. `Pop` does **not** copy — it adopts the snapshot maps
  directly as the new live maps and shrinks the stack slice. That snapshot map is
  no longer referenced by the stack, so it is safe to mutate after restore.
- **`DelayedExpansion` is part of the scope.** This is the subtle one: the
  delayed-expansion *state* is saved by `Push` and restored by `Pop`, so a
  `SETLOCAL EnableDelayedExpansion` is automatically undone by the matching
  `ENDLOCAL`.
- **Nesting.** Because `stack` is a LIFO slice, nested `SETLOCAL`s push multiple
  snapshots and each `ENDLOCAL` peels one off, restoring to the most recent
  enclosing state.
- **Unbalanced `ENDLOCAL`.** A `Pop` with an empty stack returns `false` and is a
  no-op. The executor turns that into a diagnostic
  (`ENDLOCAL without matching SETLOCAL`, `executor/executor.go:1548`) rather than
  crashing.

### Executor wiring

`SETLOCAL` always calls `Push` first, then optionally mutates
`DelayedExpansion` *after* the snapshot is taken — which is exactly why the prior
state is the thing that gets restored:

```go
func (ex *Executor) execSetlocal(s *parser.SetlocalStatement) int {
    ex.env.Push()
    if s.EnableDelayedExpansion  { ex.env.DelayedExpansion = true }
    if s.DisableDelayedExpansion { ex.env.DelayedExpansion = false }
    return 0
}
```
(`executor/executor.go:1537`)

Even a bare `SETLOCAL` (no delayed-expansion clause) pushes a scope, so it still
snapshots and will restore variables on `ENDLOCAL` while leaving the
delayed-expansion flag unchanged through the scope.

### Worked example: variable + delayed-expansion restoration

```bat
@echo off
set FOO=outer
echo before: FOO=%FOO% delayed=off

setlocal EnableDelayedExpansion
set FOO=inner
set BAR=created-inside
set X=1
set X=2
echo inside: FOO=!FOO! BAR=!BAR! X=!X!

endlocal
echo after: FOO=%FOO% BAR=%BAR%
```

Output:

```
before: FOO=outer delayed=off
inside: FOO=inner BAR=created-inside X=2
after: FOO=outer BAR=
```

What happened, step by step:

1. `set FOO=outer` stores `FOO` in the live `vars`.
2. `setlocal EnableDelayedExpansion` calls `Push`, snapshotting `vars`/`names`
   (with `FOO=outer`, no `BAR`) and the current `DelayedExpansion` (`false`). Then
   it sets `DelayedExpansion = true`, so the `!VAR!` syntax on the next lines is
   live.
3. Inside the scope, `FOO` is reassigned, `BAR` is created, and `X` is set twice;
   all mutate the live maps. `!FOO!`/`!BAR!`/`!X!` reflect the in-scope values.
4. `endlocal` calls `Pop`: the live `vars`/`names` are replaced by the snapshot
   (`FOO=outer`, no `BAR`, no `X`) and `DelayedExpansion` reverts to `false`.
5. After the scope, `%FOO%` is `outer` again and `%BAR%` expands to empty (the
   variable no longer exists — `Get` returns `""`). Delayed expansion is off, so
   `!BAR!` would now be literal text rather than an expansion.

The same restoration would occur for a plain `setlocal` (no delayed-expansion
clause): variable changes are rolled back, and the delayed-expansion flag is
preserved at whatever it was when the scope opened.
