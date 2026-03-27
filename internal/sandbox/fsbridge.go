// Package sandbox — fsbridge.go provides sandboxed file operations via exec.
// Matching TS src/agents/sandbox/fs-bridge.ts.
//
// When sandbox is enabled, file tools (read_file, write_file, list_files)
// route through FsBridge instead of direct host filesystem access.
// Docker backend uses "docker exec"; K8s backend uses remotecommand.
package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// ExecFunc executes a command and returns its output.
// Docker implementation wraps `docker exec`; K8s wraps remotecommand.
// stdin may be nil for commands that don't need input.
type ExecFunc func(ctx context.Context, stdin []byte, command []string) (stdout string, stderr string, exitCode int, err error)

// FsBridge provides sandboxed file operations via a pluggable exec function.
// Matching TS SandboxFsBridge in fs-bridge.ts.
type FsBridge struct {
	execFn  ExecFunc
	workdir string // container-side working directory (e.g. "/workspace")
}

// NewFsBridge creates a bridge using Docker exec (backward compatible).
func NewFsBridge(containerID, workdir string) *FsBridge {
	if workdir == "" {
		workdir = "/workspace"
	}
	return &FsBridge{
		execFn:  dockerExecFunc(containerID),
		workdir: workdir,
	}
}

// NewFsBridgeWithExec creates a bridge using a custom exec function.
// Used by K8s sandbox to inject remotecommand-based exec.
func NewFsBridgeWithExec(execFn ExecFunc, workdir string) *FsBridge {
	if workdir == "" {
		workdir = "/workspace"
	}
	return &FsBridge{
		execFn:  execFn,
		workdir: workdir,
	}
}

// ReadFile reads file contents from inside the container.
// Matching TS FsBridge.readFile().
func (b *FsBridge) ReadFile(ctx context.Context, path string) (string, error) {
	resolved := b.resolvePath(path)

	stdout, stderr, exitCode, err := b.execFn(ctx, nil, []string{"cat", "--", resolved})
	if err != nil {
		return "", fmt.Errorf("fsbridge read: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("read failed: %s", strings.TrimSpace(stderr))
	}

	return stdout, nil
}

// WriteFile writes content to a file inside the container, creating directories as needed.
// Matching TS FsBridge.writeFile().
func (b *FsBridge) WriteFile(ctx context.Context, path, content string) error {
	resolved := b.resolvePath(path)

	// Create parent directory
	dir := resolved[:strings.LastIndex(resolved, "/")]
	if dir != "" && dir != "/" {
		_, _, _, _ = b.execFn(ctx, nil, []string{"mkdir", "-p", dir})
	}

	// Write content via stdin pipe
	_, stderr, exitCode, err := b.execFn(ctx, []byte(content), []string{"sh", "-c", fmt.Sprintf("cat > %q", resolved)})
	if err != nil {
		return fmt.Errorf("fsbridge write: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("write failed: %s", strings.TrimSpace(stderr))
	}

	return nil
}

// ListDir lists files and directories inside the container.
// Matching TS FsBridge.readdir().
func (b *FsBridge) ListDir(ctx context.Context, path string) (string, error) {
	resolved := b.resolvePath(path)

	// Use ls -la for detailed listing
	stdout, stderr, exitCode, err := b.execFn(ctx, nil, []string{"ls", "-la", "--", resolved})
	if err != nil {
		return "", fmt.Errorf("fsbridge list: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("list failed: %s", strings.TrimSpace(stderr))
	}

	return stdout, nil
}

// Stat checks if a path exists and returns basic info.
func (b *FsBridge) Stat(ctx context.Context, path string) (string, error) {
	resolved := b.resolvePath(path)

	stdout, stderr, exitCode, err := b.execFn(ctx, nil, []string{"stat", "--", resolved})
	if err != nil {
		return "", fmt.Errorf("fsbridge stat: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("stat failed: %s", strings.TrimSpace(stderr))
	}

	return stdout, nil
}

// resolvePath resolves a path relative to the container workdir.
// Validates that absolute paths stay within the workdir (defense in depth).
func (b *FsBridge) resolvePath(path string) string {
	if path == "" || path == "." {
		return b.workdir
	}
	if strings.HasPrefix(path, "/") {
		// Validate absolute paths stay within workdir (defense in depth,
		// container is already sandboxed with read-only FS + cap-drop ALL).
		cleaned := filepath.Clean(path)
		if cleaned == b.workdir || strings.HasPrefix(cleaned, b.workdir+"/") {
			return cleaned
		}
		return b.workdir // fallback to workdir for escapes
	}
	// Relative paths: use filepath.Join for proper normalization
	return filepath.Clean(filepath.Join(b.workdir, path))
}

// dockerExecFunc returns an ExecFunc that runs commands via Docker CLI exec.
func dockerExecFunc(containerID string) ExecFunc {
	return func(ctx context.Context, stdin []byte, command []string) (string, string, int, error) {
		dockerArgs := []string{"exec"}
		if stdin != nil {
			dockerArgs = append(dockerArgs, "-i")
		}
		dockerArgs = append(dockerArgs, containerID)
		dockerArgs = append(dockerArgs, command...)

		cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}

		err := cmd.Run()
		exitCode := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
				err = nil // non-zero exit is not an execution error
			} else {
				return "", "", -1, err
			}
		}

		return stdout.String(), stderr.String(), exitCode, nil
	}
}
