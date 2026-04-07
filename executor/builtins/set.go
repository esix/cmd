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
		result, err := evalArith(expr, e)
		if err != nil {
			fmt.Fprintf(os.Stderr, "SET /A: %v\n", err)
			return 1
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

	// Replace variable references %VAR% with their numeric values
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

	return evalExpr(expr)
}

// evalExpr is a minimal recursive-descent evaluator for +, -, *, /, %.
func evalExpr(expr string) (int, error) {
	expr = strings.TrimSpace(expr)
	return parseAddSub(expr)
}

func parseAddSub(expr string) (int, error) {
	left, rest, err := parseMulDiv(expr)
	if err != nil {
		return 0, err
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
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
		rest = newRest
	}
	return left, nil
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

func parseAtom(expr string) (int, string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return 0, "", fmt.Errorf("unexpected end of expression")
	}

	// Unary minus
	if expr[0] == '-' {
		val, rest, err := parseAtom(expr[1:])
		return -val, rest, err
	}

	// Read digits
	i := 0
	for i < len(expr) && expr[i] >= '0' && expr[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, expr, fmt.Errorf("expected number, got %q", expr)
	}
	n, _ := strconv.Atoi(expr[:i])
	return n, expr[i:], nil
}
