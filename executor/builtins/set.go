package builtins

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/esix/cmd/env"
)

// Set implements the SET command.
// Called only for the "display all vars" case; SET NAME=VALUE is handled
// directly by the executor from the SetStatement AST node.
func Set(args []string, e *env.Env) int {
	if len(args) == 0 {
		// Print all variables sorted
		vars := e.All()
		keys := make([]string, 0, len(vars))
		for k := range vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Printf("%s=%s\n", k, vars[k])
		}
		return 0
	}

	// SET /A arithmetic
	if strings.ToUpper(args[0]) == "/A" {
		raw := strings.Join(args[1:], "")
		eqIdx := strings.IndexByte(raw, '=')
		if eqIdx == -1 {
			// No assignment, just evaluate and print
			result, err := evalArith(raw, e)
			if err != nil {
				fmt.Fprintf(os.Stderr, "SET /A: %v\n", err)
				return 1
			}
			fmt.Println(result)
			return 0
		}
		name := strings.TrimSpace(raw[:eqIdx])
		expr := strings.TrimSpace(raw[eqIdx+1:])

		// Check for compound operators: +=, -=, *=, /=, %=
		compoundOp := byte(0)
		if len(name) > 0 {
			last := name[len(name)-1]
			if last == '+' || last == '-' || last == '*' || last == '/' || last == '%' {
				compoundOp = last
				name = strings.TrimSpace(name[:len(name)-1])
			}
		}

		result, err := evalArith(expr, e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SET /A: %v\n", err)
			return 1
		}

		if compoundOp != 0 {
			current, _ := strconv.Atoi(e.Get(name))
			switch compoundOp {
			case '+':
				result = current + result
			case '-':
				result = current - result
			case '*':
				result = current * result
			case '/':
				if result != 0 {
					result = current / result
				}
			case '%':
				if result != 0 {
					result = current % result
				}
			}
		}

		e.Set(name, strconv.Itoa(result))
		return 0
	}

	// SET NAME=VALUE or SET NAME (display)
	raw := strings.Join(args, " ")
	eqIdx := strings.IndexByte(raw, '=')
	if eqIdx == -1 {
		// Display variables matching the prefix
		prefix := strings.ToUpper(raw)
		vars := e.All()
		found := false
		for k, v := range vars {
			if strings.HasPrefix(k, prefix) {
				fmt.Printf("%s=%s\n", k, v)
				found = true
			}
		}
		if !found {
			fmt.Printf("Environment variable %s not defined\n", raw)
			return 1
		}
		return 0
	}

	name := raw[:eqIdx]
	value := raw[eqIdx+1:]
	if value == "" {
		e.Unset(name)
	} else {
		e.Set(name, value)
	}
	return 0
}

// evalArith evaluates a simple integer arithmetic expression for SET /A.
// Supports +, -, *, /, % operators.
func evalArith(expr string, e *env.Env) (int, error) {
	expr = strings.TrimSpace(expr)

	// Replace %VAR% references with their values (already done by early expansion,
	// but handle any remaining ones)
	for {
		start := strings.IndexByte(expr, '%')
		if start == -1 {
			break
		}
		end := strings.IndexByte(expr[start+1:], '%')
		if end == -1 {
			break
		}
		name := expr[start+1 : start+1+end]
		val := e.Get(name)
		expr = expr[:start] + val + expr[start+1+end+1:]
	}

	// %% is the modulo operator in SET /A (double % survives early expansion)
	expr = strings.ReplaceAll(expr, "%%", "%")

	// Set env for variable resolution in expressions (e.g. "d/4096")
	arithEnv = e
	return evalExpr(expr)
}

// evalExpr: recursive descent with correct CMD SET /A precedence.
// From lowest to highest: | ^ & << >> + - * / %
func evalExpr(expr string) (int, error) {
	expr = strings.TrimSpace(expr)
	val, rest, err := parseBitOr(expr)
	if err != nil {
		return 0, err
	}
	rest = strings.TrimSpace(rest)
	if rest != "" && rest[0] != ')' {
		return val, nil // ignore trailing text
	}
	return val, nil
}

func parseBitOr(expr string) (int, string, error) {
	left, rest, err := parseBitXor(expr)
	if err != nil {
		return 0, expr, err
	}
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != '|' {
			break
		}
		right, newRest, err := parseBitXor(rest[1:])
		if err != nil {
			return 0, rest, err
		}
		left = left | right
		rest = newRest
	}
	return left, rest, nil
}

func parseBitXor(expr string) (int, string, error) {
	left, rest, err := parseBitAnd(expr)
	if err != nil {
		return 0, expr, err
	}
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != '^' {
			break
		}
		right, newRest, err := parseBitAnd(rest[1:])
		if err != nil {
			return 0, rest, err
		}
		left = left ^ right
		rest = newRest
	}
	return left, rest, nil
}

func parseBitAnd(expr string) (int, string, error) {
	left, rest, err := parseShift(expr)
	if err != nil {
		return 0, expr, err
	}
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" || rest[0] != '&' {
			break
		}
		right, newRest, err := parseShift(rest[1:])
		if err != nil {
			return 0, rest, err
		}
		left = left & right
		rest = newRest
	}
	return left, rest, nil
}

func parseShift(expr string) (int, string, error) {
	left, rest, err := parseAddSub(expr)
	if err != nil {
		return 0, expr, err
	}
	for {
		rest = strings.TrimSpace(rest)
		if len(rest) < 2 {
			break
		}
		if rest[0] == '<' && rest[1] == '<' {
			right, newRest, err := parseAddSub(rest[2:])
			if err != nil {
				return 0, rest, err
			}
			left = left << uint(right)
			rest = newRest
		} else if rest[0] == '>' && rest[1] == '>' {
			right, newRest, err := parseAddSub(rest[2:])
			if err != nil {
				return 0, rest, err
			}
			left = left >> uint(right)
			rest = newRest
		} else {
			break
		}
	}
	return left, rest, nil
}

func parseAddSub(expr string) (int, string, error) {
	left, rest, err := parseMulDiv(expr)
	if err != nil {
		return 0, "", err
	}
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		op := rest[0]
		if op != '+' && op != '-' {
			break
		}
		right, newRest, err := parseMulDiv(rest[1:])
		if err != nil {
			return 0, rest, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
		rest = newRest
	}
	return left, rest, nil
}

func parseMulDiv(expr string) (int, string, error) {
	expr = strings.TrimSpace(expr)
	left, rest, err := parseAtom(expr)
	if err != nil {
		return 0, expr, err
	}
	for {
		rest = strings.TrimSpace(rest)
		if rest == "" {
			break
		}
		op := rest[0]
		if op != '*' && op != '/' && op != '%' {
			break
		}
		right, newRest, err := parseAtom(rest[1:])
		if err != nil {
			return 0, rest, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				return 0, "", fmt.Errorf("division by zero")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, "", fmt.Errorf("modulo by zero")
			}
			left %= right
		}
		rest = newRest
	}
	return left, rest, nil
}

// arithEnv is set before evaluating so parseAtom can resolve variable names.
var arithEnv *env.Env

func parseAtom(expr string) (int, string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, "", fmt.Errorf("unexpected end of expression")
	}

	// Parenthesized sub-expression
	if expr[0] == '(' {
		inner := expr[1:]
		val, rest, err := parseAddSubRest(inner)
		if err != nil {
			return 0, expr, err
		}
		rest = strings.TrimSpace(rest)
		if len(rest) > 0 && rest[0] == ')' {
			rest = rest[1:]
		}
		return val, rest, nil
	}

	// Unary minus
	if expr[0] == '-' {
		val, rest, err := parseAtom(expr[1:])
		return -val, rest, err
	}

	// Unary plus
	if expr[0] == '+' {
		return parseAtom(expr[1:])
	}

	// Hex literal: 0x...
	if len(expr) >= 2 && expr[0] == '0' && (expr[1] == 'x' || expr[1] == 'X') {
		i := 2
		for i < len(expr) && isHexDigit(expr[i]) {
			i++
		}
		n, _ := strconv.ParseInt(expr[2:i], 16, 64)
		return int(n), expr[i:], nil
	}

	// Numeric literal
	if expr[0] >= '0' && expr[0] <= '9' {
		i := 0
		for i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
			i++
		}
		n, _ := strconv.Atoi(expr[:i])
		return n, expr[i:], nil
	}

	// Variable name: letters, digits, underscore (resolve to its numeric value)
	if isVarStart(expr[0]) {
		i := 0
		for i < len(expr) && isVarChar(expr[i]) {
			i++
		}
		name := expr[:i]
		val := 0
		if arithEnv != nil {
			val, _ = strconv.Atoi(arithEnv.Get(name))
		}
		return val, expr[i:], nil
	}

	return 0, expr, fmt.Errorf("expected number, got %q", expr)
}

// parseAddSubRest is used by parenthesized sub-expressions.
func parseAddSubRest(expr string) (int, string, error) {
	return parseBitOr(expr)
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func isVarStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

func isVarChar(c byte) bool {
	return isVarStart(c) || (c >= '0' && c <= '9')
}
