package builtins

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/esix/cmd/env"
)

// Certutil implements a minimal certutil -encodehex / -decodehex.
// Only supports the hex encoding mode used by gw-batsic.
func Certutil(args []string, _ *env.Env) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: certutil -encodehex infile outfile [type]")
		return 1
	}

	mode := strings.ToLower(args[0])

	switch mode {
	case "-encodehex":
		return certutilEncodeHex(args[1:])
	case "-decodehex":
		return certutilDecodeHex(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "certutil: unsupported mode %q\n", mode)
		return 1
	}
}

// certutil -encodehex infile outfile [type]
// type 12 = continuous hex string (no spaces, no line breaks)
// type 4  = hex with spaces
// default = hex dump format
func certutilEncodeHex(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "certutil -encodehex: need infile and outfile")
		return 1
	}
	inFile := args[0]
	outFile := args[1]
	encType := 0
	if len(args) >= 3 {
		fmt.Sscanf(args[2], "%d", &encType)
	}

	data, err := os.ReadFile(inFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "certutil: cannot read %s: %v\n", inFile, err)
		return 1
	}

	var output string
	switch encType {
	case 12:
		// Continuous hex string, no spaces
		output = hex.EncodeToString(data)
	case 4:
		// Hex with spaces between bytes
		var sb strings.Builder
		for i, b := range data {
			if i > 0 {
				sb.WriteByte(' ')
			}
			fmt.Fprintf(&sb, "%02x", b)
		}
		output = sb.String()
	default:
		// Default: hex dump with offsets (simplified)
		output = hex.EncodeToString(data)
	}

	if err := os.WriteFile(outFile, []byte(output+"\n"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "certutil: cannot write %s: %v\n", outFile, err)
		return 1
	}
	return 0
}

// certutil -decodehex infile outfile [type]
func certutilDecodeHex(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "certutil -decodehex: need infile and outfile")
		return 1
	}
	inFile := args[0]
	outFile := args[1]

	data, err := os.ReadFile(inFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "certutil: cannot read %s: %v\n", inFile, err)
		return 1
	}

	// Strip whitespace and newlines
	hexStr := strings.TrimSpace(string(data))
	hexStr = strings.ReplaceAll(hexStr, " ", "")
	hexStr = strings.ReplaceAll(hexStr, "\n", "")
	hexStr = strings.ReplaceAll(hexStr, "\r", "")

	decoded, err := hex.DecodeString(hexStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "certutil: invalid hex: %v\n", err)
		return 1
	}

	if err := os.WriteFile(outFile, decoded, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "certutil: cannot write %s: %v\n", outFile, err)
		return 1
	}
	return 0
}
