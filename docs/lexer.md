# Lexer

The `lexer` package turns a single logical BAT line into a flat slice of `Token`
values. It is the first stage of the pipeline: the [executor](executor.md)
splits the script into logical lines and joins parenthesized blocks, the lexer
tokenizes each line, and the [parser](parser.md) turns those tokens into an AST.

The lexer is deliberately *line-oriented* and *stateless across lines*. It knows
nothing about labels, control flow, or block structure. Everything it does is a
single forward pass over one string with one cursor (`pos`). The two source
files are small and worth reading directly:

- `lexer/token.go` — the `Token` struct and the `Kind` enumeration.
- `lexer/lexer.go` — the tokenizer itself.

## The `Token` struct

`lexer/token.go:22`:

```go
type Token struct {
	Kind        Kind
	Value       string // raw text of the token
	Pos         int    // byte offset in the original line
	SpaceBefore bool   // true if whitespace preceded this token
}
```

- **`Kind`** — the lexical category (see below).
- **`Value`** — the *raw* text of the token. Crucially the lexer does **not**
  perform variable expansion: a `PERCENT_VAR` token carries its delimiters
  (`%VAR%`, `%~f1`, `%%I`) verbatim, and a quoted `WORD` keeps its surrounding
  double quotes. Caret (`^`) escapes are the one exception — they are resolved
  inside `readWord` so the escape characters never reach `Value`.
- **`Pos`** — byte offset of the token's first character in the original input
  line. Used for diagnostics; the lexer itself never reads it back.
- **`SpaceBefore`** — `true` when whitespace (space or tab) preceded the token.
  This is load-bearing downstream: command vs. argument boundaries and several
  cmd.exe quirks depend on whether tokens were space-separated. The first token
  on a line is always reported with `SpaceBefore = true` (see the
  `l.pos == 0` clause in `lexer/lexer.go:36`).

## The `Kind` enumeration

`lexer/token.go:6` defines every token kind:

| Kind          | Meaning                                              |
|---------------|------------------------------------------------------|
| `WORD`        | A literal word or a quoted string.                   |
| `PERCENT_VAR` | `%VAR%`, `%0`–`%9`, `%~mods N`, `%VAR:~N,M%`, `%%I`. |
| `BANG_VAR`    | `!VAR!` delayed-expansion reference.                 |
| `REDIRECTION` | `>`, `>>`, `<`, `2>`, `2>>`, `2>&1`, etc.            |
| `PIPE`        | `\|`                                                 |
| `AMPERSAND`   | `&` command separator (and the block marker).        |
| `AND`         | `&&`                                                  |
| `OR`          | `\|\|`                                               |
| `LPAREN`      | `(`                                                  |
| `RPAREN`      | `)`                                                  |
| `NEWLINE`     | End of a logical line.                                |
| `EOF`         | End of input.                                         |

`NEWLINE` is declared but never produced by this lexer — each call to
`Tokenize` handles exactly one logical line, so the loop emits `EOF` and stops.
`NEWLINE` exists for the parser's token model.

## Entry points

`lexer/lexer.go:15`:

```go
func Tokenize(line string) []Token
func TokenizeWithOpts(line string, delayedExpansion bool) []Token
```

`Tokenize` is a thin wrapper that calls `TokenizeWithOpts(line, false)`. The
`delayedExpansion` flag corresponds to `SETLOCAL EnableDelayedExpansion` and is
the *only* piece of external state the lexer accepts. When it is `false`, the
`!` character has no special meaning and is consumed as ordinary `WORD` text;
when `true`, `!...!` is recognized as a `BANG_VAR`. The parser threads the flag
through at `parser/parser.go:24`. The doc comment also notes that **the caller
is responsible for stripping a leading `@`** before calling — the lexer does not
treat `@` specially.

The lexer state is just three fields (`lexer/lexer.go:25`):

```go
type lexer struct {
	input            string
	pos              int
	delayedExpansion bool
}
```

## The main tokenize loop

`tokenize` (`lexer/lexer.go:31`) loops until the cursor reaches end of input:

1. Record `posBefore`, then `skipSpaces()` (spaces and tabs).
2. Compute `hadSpace = l.pos > posBefore || l.pos == 0`. This is stored as
   `SpaceBefore` on whatever token is produced this iteration. The
   `l.pos == 0` term forces the very first token to count as space-preceded.
3. If at end of input, append `Token{Kind: EOF, Pos: l.pos}` and break.
4. Otherwise dispatch on the current byte `ch` through a `switch`.

The dispatch order matters because some prefixes overlap (`&` vs `&&`, `|` vs
`||`, a digit that starts `2>` vs a digit that starts a word). The `switch`
cases, in order (`lexer/lexer.go:48`):

```text
ch == '|' && peek(1) == '|'   -> OR        "||"
ch == '&' && peek(1) == '&'   -> AND       "&&"
ch == '&'                     -> AMPERSAND  "&"
ch == '\x01'                  -> AMPERSAND  "\x01"   (block marker)
ch == '|'                     -> PIPE       "|"
ch == '('                     -> LPAREN     "("
ch == ')'                     -> RPAREN     ")"
ch == '>' || ch == '<' || isRedirectStart  -> readRedirect()
ch == '%'                     -> readPercentVar()
ch == '!' && delayedExpansion -> readBangVar()
default                       -> readWord()
```

The two-character operators (`||`, `&&`) are tested before their single
character forms so the longer match wins. Each branch advances `pos` by the
exact number of bytes consumed and attaches `SpaceBefore` via the local
`addToken` closure.

`peek(offset)` (`lexer/lexer.go:97`) returns the byte at `pos+offset`, or `0`
when out of bounds, so the two-character lookaheads are safe at end of line.

### The `0x01` block-line boundary marker

`joinBlocks` in the executor merges a multi-line parenthesized block into a
single physical string before lexing. Instead of concatenating block lines with
a space or a literal `&`, it inserts a `\x01` byte between them
(`executor/executor.go:1762`):

```go
// Use \x01 as a line-boundary marker; the parser treats it as a
// statement separator that does NOT get consumed into IF/FOR body.
accum += "\x01" + strings.TrimSpace(line)
```

The lexer turns `\x01` into an `AMPERSAND` token **but keeps `Value` as
`"\x01"`** rather than `"&"` (`lexer/lexer.go:61`):

```go
case ch == '\x01':
	addToken(Token{Kind: AMPERSAND, Value: "\x01", Pos: l.pos})
	l.pos++
```

So `\x01` behaves like `&` as a statement separator, but the distinct `Value`
lets the parser tell a *real* `&` apart from a line boundary. This is exactly
how the parser uses it: at `parser/parser.go:133` an IF body keeps consuming
across `&` (`tok.Kind == AMPERSAND && tok.Value != "\x01"`) but stops at the
block-line marker, so a statement on a later line of a block is not swallowed
into a single-line `IF` on a previous line. `readWord` also treats `\x01` as a
hard stop (`lexer/lexer.go:259`), so it can never be absorbed into a word.

## `readWord` — words, quotes, and caret escapes

`readWord` (`lexer/lexer.go:240`) accumulates characters until it hits a
delimiter, returning a single `WORD` token. Stop characters (each of which
*ends* the current word without being consumed):

- space, tab, `|` — whitespace and pipe.
- `)` — always a delimiter.
- `(` — a delimiter **only when the word is still empty** (`sb.Len() == 0`).
- `&` and `\x01` — separators / block marker.
- `>`, `<`, or a redirect start detected by `isRedirectStart` (a digit
  immediately followed by `>`).
- `%` — handed back to the main loop so it becomes a variable token.
- `!` — but only when `delayedExpansion` is enabled.

Three behaviors inside the loop deserve special attention.

### Caret (`^`) escape handling

`^` is the BAT escape character. When `readWord` sees `^` it advances past it
and writes the *next* byte literally, then continues (`lexer/lexer.go:276`):

```go
if ch == '^' {
	l.pos++
	if l.pos < len(l.input) {
		sb.WriteByte(l.input[l.pos])
		l.pos++
	}
	continue
}
```

Consequences:

- `^&` produces a word containing a literal `&` — the `&` never reaches the
  separator dispatch, so it does not split the command.
- `^^` produces a single literal `^`.
- `^%` writes a literal `%` into the word, which means an escaped percent does
  *not* start a `PERCENT_VAR` (it is consumed before the `%`-stop check fires).
- A trailing `^` at end of line escapes nothing and is silently dropped (the
  `if l.pos < len(l.input)` guard skips the write).

Note that `^` is only honored *inside* `readWord`. It is **not** processed by
the operator dispatch, `readPercentVar`, or `readBangVar`. So `^` cannot escape
the leading character of a token — e.g. a line beginning with `^&` still enters
`readWord` (the default case) and is handled correctly, but a `%` or `!` that
reaches the main loop is treated as a variable introducer regardless of any
preceding caret that was already consumed.

### Quoted-string consumption

A double quote starts an inline quoted run. `readWord` writes the opening quote,
copies every byte up to the next `"`, then writes the closing quote
(`lexer/lexer.go:286`):

```go
if ch == '"' {
	sb.WriteByte(ch)
	l.pos++
	for l.pos < len(l.input) && l.input[l.pos] != '"' {
		sb.WriteByte(l.input[l.pos])
		l.pos++
	}
	if l.pos < len(l.input) {
		sb.WriteByte(l.input[l.pos]) // closing "
		l.pos++
	}
	continue
}
```

Key points:

- **The quotes are preserved** in `Value`; the lexer does not strip them.
- **Everything inside quotes is literal** — spaces, `&`, `|`, `>`, `(`, even
  `^` and `%` are copied verbatim because the inner loop only watches for `"`.
  This matches cmd.exe: redirection and separators are inert inside quotes.
- A quoted run is part of the *current* word, not a token by itself. So
  `set"x"=y` and `abc"d e"f` each tokenize as one `WORD`, with the quotes
  embedded mid-word.
- An unterminated quote (no closing `"`) consumes to end of line and the word
  ends there; no error is raised.

### The mid-word open-paren rule (the `echo(` idiom)

`(` is a delimiter only at the *start* of a word (`lexer/lexer.go:256`):

```go
// ( is a delimiter only at start of a token. Mid-word ( is literal
// (allows echo(text pattern used in BAT for safe echo)
if ch == '(' && sb.Len() == 0 {
	break
}
```

This reproduces the well-known `echo(` trick. In real BAT files, `echo(` is a
robust way to echo text (or a blank line) that avoids the pitfalls of
`echo.`/`echo:` and the "ECHO is on/off" message. Because the `(` appears
mid-word (after `echo`), it is taken literally and the whole thing tokenizes as
one `WORD` `echo(text` rather than `echo` `LPAREN` `text`:

```bat
echo(Hello World
echo(
```

A leading `(` (start of a word, `sb.Len() == 0`) still becomes an `LPAREN` so
genuine blocks like `( ... )` parse normally.

## `readPercentVar` — every `%` form

`readPercentVar` (`lexer/lexer.go:167`) is entered whenever the dispatcher sees
`%`. It consumes the opening `%` and then branches by what follows. All forms
keep their delimiters in `Value`; expansion happens later (see
[Expansion](expansion.md)).

1. **`%~[modifiers]N` — tilde parameter modifiers** (`lexer/lexer.go:172`).
   After `%~`, it greedily reads alphanumerics, `_`, and `$` (the `$` supports
   `%~$PATH:1`). Emits `PERCENT_VAR` with value like `%~f1`, `%~dp0`,
   `%~$PATH:1`. There is no closing `%`.

2. **Positional `%0`–`%9`** (`lexer/lexer.go:185`). A `%` immediately followed
   by a single digit yields a two-byte token `%0` … `%9`. Only one digit is
   consumed, so `%12` tokenizes as the positional `%1` followed by a literal
   `2` (handled by the subsequent `readWord`). No closing `%` is needed.

3. **`%%` forms — FOR-loop variables and literal percent**
   (`lexer/lexer.go:193`). After consuming the second `%`:
   - **`%%~[modifiers]X`** — a tilde-modified FOR variable. Reads `~` then the
     same alphanumeric/`$` run as form 1, producing e.g. `%%~nxI`
     (`lexer/lexer.go:196`).
   - **`%%X`** — when the next byte is alphanumeric, emits `%%X` as a single
     `PERCENT_VAR` (the FOR loop variable, e.g. `%%I`, `%%i`, `%%1`)
     (`lexer/lexer.go:207`).
   - Otherwise (`%%` not followed by a usable char), emits a `WORD` containing
     a single literal `%` (`lexer/lexer.go:212`). Note: only one `%` is written
     even though two were consumed — this collapses `%%` to a literal `%`.

4. **`%VAR%`, `%VAR:~N,M%`, `%VAR:old=new%`** (`lexer/lexer.go:216`). For
   anything else, it searches for the next `%` with `strings.IndexByte`. The
   entire span up to and including the closing `%` becomes the token value
   (`%VAR%`). Because it just looks for the next `%`, all substring/substitution
   syntaxes (`%VAR:~0,3%`, `%PATH:C:=D:%`) are captured as one opaque token and
   decoded during expansion, not here.

5. **Lone-percent fallbacks.** If no closing `%` is found in form 4, the token
   is a `WORD` whose value is just `"%"` (`lexer/lexer.go:218`). Likewise the
   bare-`%%` case in form 3 falls back to a `"%"` word. So a stray `%` that is
   not part of any recognizable reference degrades to a literal percent rather
   than erroring.

### `%%` requires `%%` together

The FOR-variable forms (`%%I`, `%%~nxI`) are only recognized when the lexer sees
two consecutive `%`. A FOR loop body in a real BAT file uses `%%I`; on the
command line it would be `%I`, but this lexer's positional/named forms cover the
single-`%` cases. The double-percent is the in-file convention and is what the
parser/executor expect for [FOR](executor.md) iteration variables.

## `readBangVar` — `!VAR!` delayed expansion

`readBangVar` (`lexer/lexer.go:226`) is only reached when `delayedExpansion` is
`true` (guarded in the main switch at `lexer/lexer.go:86`). It mirrors form 4 of
`readPercentVar`: consume the opening `!`, find the next `!`, and emit a
`BANG_VAR` whose value is `!...!` including both delimiters. If there is no
closing `!`, it falls back to a `WORD` containing the single character `"!"`
(`lexer/lexer.go:232`).

When `delayedExpansion` is `false`, none of this runs — `!` is an ordinary
character. In `readWord` the `!`-stop is likewise gated on `delayedExpansion`
(`lexer/lexer.go:271`), so with the flag off a `!` is simply written into the
surrounding word.

## `readRedirect` — redirection operators

Two helpers cooperate:

- `isRedirectStart(s, pos)` (`lexer/lexer.go:116`) returns `true` when the byte
  at `pos` is a digit and the next byte is `>`. This is what lets a leading
  stream number (`2>`) be recognized as redirection rather than a word.
- `readRedirect` (`lexer/lexer.go:130`) assembles the operator from up to four
  parts, in order:
  1. An optional single leading digit (the stream number, e.g. `2`).
  2. The `>` or `<` character.
  3. An optional second `>` (turning `>` into `>>` append).
  4. An optional `&` followed by an optional single digit — the
     duplicate-handle form `&1`.

The resulting `Value` is the literal operator string. Forms the code produces:

```text
>     >>     <
2>    2>>    1>    0<
2>&1  1>&2   >&2
```

Notes and edge cases:

- Only **one** leading digit and **one** trailing digit are consumed. `12>` is
  *not* a stream-12 redirect — `isRedirectStart` only checks one digit, so this
  enters `readWord` and the `1` is part of a word, then `2>` is the redirect.
- The `&N` tail is only collected when the redirect already contains a `>` or
  `<` from step 2. The bare `&` separator is handled earlier in the main switch,
  so `&1` only attaches here as part of an in-progress redirect like `2>&1`.
- The digit after `&` is optional in the code: `2>&` (no digit) is accepted and
  produces `Value == "2>&"`. cmd.exe would treat `&1` as required, but the lexer
  is lenient and leaves validation to later stages.
- `<` redirects support the same digit-prefix machinery even though input
  redirection from a numbered handle is unusual.

## Worked example

Take a line that exercises most of the machinery:

```bat
echo(%~nx1 "a & b" > out.txt 2>&1 ^& done
```

Tokenizing with `delayedExpansion = false` produces:

| #  | Kind          | Value          | SpaceBefore | Notes |
|----|---------------|----------------|-------------|-------|
| 1  | `WORD`        | `echo(`        | true        | mid-word `(` is literal (the `echo(` idiom); stops at `%` |
| 2  | `PERCENT_VAR` | `%~nx1`        | false       | tilde modifiers `nx` + positional `1`, no closing `%` |
| 3  | `WORD`        | `"a & b"`      | true        | quoted run; inner `&` and spaces are literal |
| 4  | `REDIRECTION` | `>`            | true        | plain stdout redirect |
| 5  | `WORD`        | `out.txt`      | true        | ordinary word |
| 6  | `REDIRECTION` | `2>&1`         | true        | stream 2 duplicated onto handle 1 |
| 7  | `WORD`        | `&`            | true        | `^&` — caret-escaped `&` becomes a literal `&` word, not an `AMPERSAND` |
| 8  | `WORD`        | `done`         | true        | |
| 9  | `EOF`         | `` (empty)     | true        | `Pos` = end of line |

Things worth re-reading off this example:

- Token 1 ends at `%` (not at `(`), because `(` is mid-word and `%` is a hard
  stop that the main loop turns into the next token.
- Token 2 has no surrounding `%`/closing delimiter — the tilde form is
  self-delimiting.
- Token 3 keeps its quotes and absorbs the `&` that would otherwise split the
  line.
- Token 7 demonstrates that `^&` collapses to a literal `&` *word*; contrast
  with an unescaped `&`, which would have been an `AMPERSAND` separator.

## Summary of cmd.exe quirks the lexer reproduces

- **`echo(` idiom**: `(` is literal mid-word, a delimiter only at word start.
- **Caret escapes** inside words: `^x` → literal `x`; trailing `^` is dropped.
- **Quotes are opaque and preserved**: separators/redirects are inert inside
  `"..."`; quotes are kept in `Value`.
- **One-digit positional and stream numbers**: `%12` = `%1` + `2`; `12>` is not
  a stream-12 redirect.
- **`%%` collapses to `%`** when not a FOR variable; `%%I` is the FOR variable
  form.
- **Lenient fallbacks**: an unmatched `%`, unmatched `!`, or `%%` with no usable
  follow-on degrade to a literal `WORD`, never an error.
- **`\x01` block marker** behaves as `&` for separation but stays distinct in
  `Value` so the parser does not fold later block lines into an earlier IF/FOR
  body.
- **`!` is inert unless delayed expansion is enabled.**
