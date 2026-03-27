package sandbox

import (
	"context"
	"fmt"
	"testing"
)

// mockExecCall records a single call to a mock ExecFunc.
type mockExecCall struct {
	stdin   []byte
	command []string
}

// newMockExecFunc returns an ExecFunc that records calls and returns configurable responses.
func newMockExecFunc(responses map[string]struct {
	stdout   string
	stderr   string
	exitCode int
	err      error
}) (ExecFunc, *[]mockExecCall) {
	var calls []mockExecCall
	fn := func(ctx context.Context, stdin []byte, command []string) (string, string, int, error) {
		calls = append(calls, mockExecCall{stdin: stdin, command: command})
		if len(command) > 0 {
			if resp, ok := responses[command[0]]; ok {
				return resp.stdout, resp.stderr, resp.exitCode, resp.err
			}
		}
		return "", "", 0, nil
	}
	return fn, &calls
}

func TestFsBridgeReadFile(t *testing.T) {
	execFn, calls := newMockExecFunc(map[string]struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}{
		"cat": {stdout: "file contents", exitCode: 0},
	})

	fb := NewFsBridgeWithExec(execFn, "/workspace")
	content, err := fb.ReadFile(context.Background(), "test.txt")

	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if content != "file contents" {
		t.Errorf("ReadFile() = %q, want %q", content, "file contents")
	}
	if len(*calls) < 1 {
		t.Fatal("expected at least 1 exec call")
	}
	cmd := (*calls)[0].command
	if len(cmd) != 3 || cmd[0] != "cat" || cmd[1] != "--" || cmd[2] != "/workspace/test.txt" {
		t.Errorf("unexpected command: %v", cmd)
	}
}

func TestFsBridgeReadFileError(t *testing.T) {
	execFn, _ := newMockExecFunc(map[string]struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}{
		"cat": {stderr: "No such file", exitCode: 1},
	})

	fb := NewFsBridgeWithExec(execFn, "/workspace")
	_, err := fb.ReadFile(context.Background(), "missing.txt")

	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFsBridgeWriteFile(t *testing.T) {
	execFn, calls := newMockExecFunc(map[string]struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}{
		"mkdir": {exitCode: 0},
		"sh":    {exitCode: 0},
	})

	fb := NewFsBridgeWithExec(execFn, "/workspace")
	err := fb.WriteFile(context.Background(), "out.txt", "hello world")

	if err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// Should have 2 calls: mkdir -p + sh -c cat > ...
	if len(*calls) < 2 {
		t.Fatalf("expected 2 exec calls, got %d", len(*calls))
	}
	// Second call should have stdin
	writeCall := (*calls)[1]
	if string(writeCall.stdin) != "hello world" {
		t.Errorf("WriteFile stdin = %q, want %q", string(writeCall.stdin), "hello world")
	}
}

func TestFsBridgeListDir(t *testing.T) {
	execFn, calls := newMockExecFunc(map[string]struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}{
		"ls": {stdout: "file1.txt\nfile2.txt", exitCode: 0},
	})

	fb := NewFsBridgeWithExec(execFn, "/workspace")
	listing, err := fb.ListDir(context.Background(), ".")

	if err != nil {
		t.Fatalf("ListDir() error = %v", err)
	}
	if listing != "file1.txt\nfile2.txt" {
		t.Errorf("ListDir() = %q, want listing", listing)
	}
	if len(*calls) < 1 {
		t.Fatal("expected at least 1 exec call")
	}
	if (*calls)[0].command[0] != "ls" {
		t.Errorf("expected ls command, got %v", (*calls)[0].command)
	}
}

func TestFsBridgePathResolutionStaysInWorkdir(t *testing.T) {
	execFn, calls := newMockExecFunc(map[string]struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}{
		"cat": {stdout: "", exitCode: 0},
	})

	fb := NewFsBridgeWithExec(execFn, "/workspace")

	// Absolute path outside workdir should fallback to workdir
	fb.ReadFile(context.Background(), "/etc/passwd")
	if len(*calls) < 1 {
		t.Fatal("expected exec call")
	}
	cmd := (*calls)[0].command
	// Path should resolve to /workspace (fallback), not /etc/passwd
	if cmd[2] != "/workspace" {
		t.Errorf("path escape: resolved to %q, want /workspace", cmd[2])
	}
}

func TestFsBridgeAbsolutePathEscapeBlocked(t *testing.T) {
	execFn, calls := newMockExecFunc(map[string]struct {
		stdout   string
		stderr   string
		exitCode int
		err      error
	}{
		"cat": {stdout: "", exitCode: 0},
	})

	fb := NewFsBridgeWithExec(execFn, "/workspace")

	// Absolute path outside workdir should fallback to workdir (defense in depth).
	// Relative traversal (../../) is handled by container isolation,
	// not by resolvePath — this is existing Docker sandbox behavior.
	fb.ReadFile(context.Background(), "/etc/shadow")
	if len(*calls) < 1 {
		t.Fatal("expected exec call")
	}
	cmd := (*calls)[0].command
	resolved := cmd[2]
	if resolved == "/etc/shadow" {
		t.Errorf("absolute path escape not blocked: resolved to %q, want /workspace", resolved)
	}
}

func TestFsBridgeExecFuncTransportError(t *testing.T) {
	execFn := func(ctx context.Context, stdin []byte, command []string) (string, string, int, error) {
		return "", "", -1, fmt.Errorf("connection refused")
	}

	fb := NewFsBridgeWithExec(execFn, "/workspace")
	_, err := fb.ReadFile(context.Background(), "test.txt")

	if err == nil {
		t.Fatal("expected error for transport failure")
	}
}

func TestNewFsBridgeBackwardCompat(t *testing.T) {
	// NewFsBridge should create a valid FsBridge with dockerExecFunc.
	// We can't run Docker in unit tests, but we can verify the struct is constructed.
	fb := NewFsBridge("test-container-id", "/workspace")
	if fb == nil {
		t.Fatal("NewFsBridge returned nil")
	}
	if fb.execFn == nil {
		t.Fatal("NewFsBridge execFn is nil")
	}
	if fb.workdir != "/workspace" {
		t.Errorf("workdir = %q, want /workspace", fb.workdir)
	}
}

func TestNewFsBridgeDefaultWorkdir(t *testing.T) {
	fb := NewFsBridge("test-id", "")
	if fb.workdir != "/workspace" {
		t.Errorf("default workdir = %q, want /workspace", fb.workdir)
	}

	fb2 := NewFsBridgeWithExec(func(ctx context.Context, stdin []byte, command []string) (string, string, int, error) {
		return "", "", 0, nil
	}, "")
	if fb2.workdir != "/workspace" {
		t.Errorf("default workdir = %q, want /workspace", fb2.workdir)
	}
}
