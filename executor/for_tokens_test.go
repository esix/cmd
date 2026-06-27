package executor

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/esix/cmd/env"
	"github.com/esix/cmd/parser"
)

func TestForFBackquotedSortCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("uses Windows sort command")
	}

	dir := t.TempDir()
	input := filepath.Join(dir, "program.dat")
	if err := os.WriteFile(input, []byte("00020 two words\r\n00010 one word\r\n"), 0644); err != nil {
		t.Fatal(err)
	}
	line := `for /f "usebackq tokens=1,* delims= " %%a in (` + "`sort \"" + input + "\"`" + `) do echo %%a %%b`
	stmts, err := parser.ParseLine(line)
	if err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		ex := New(env.New())
		ex.RunStmts(stmts, nil)
	})
	if !strings.Contains(out, "00010 one word") || !strings.Contains(out, "00020 two words") {
		t.Fatalf("FOR /F output = %q", out)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	saved := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	os.Stdout = saved
	w.Close()
	data, _ := io.ReadAll(r)
	r.Close()
	return string(data)
}
