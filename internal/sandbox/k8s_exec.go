package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	rcutil "k8s.io/apimachinery/pkg/util/remotecommand"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	executil "k8s.io/utils/exec"
)

// NewK8sExecFunc returns an ExecFunc that runs commands in a K8s pod via remotecommand.
// Uses WebSocket as primary executor with SPDY fallback.
func NewK8sExecFunc(restConfig *rest.Config, clientset kubernetes.Interface,
	namespace, podName, containerName string, maxOutputBytes int) ExecFunc {

	if maxOutputBytes <= 0 {
		maxOutputBytes = 1 << 20 // 1MB default
	}

	return func(ctx context.Context, stdin []byte, command []string) (string, string, int, error) {
		execURL := buildExecURL(clientset, namespace, podName, containerName, command, stdin != nil)

		executor, err := newFallbackExecutor(restConfig, "POST", execURL)
		if err != nil {
			return "", "", -1, fmt.Errorf("k8s exec setup: %w", err)
		}

		stdout := &limitedBuffer{max: maxOutputBytes}
		stderr := &limitedBuffer{max: maxOutputBytes}

		opts := remotecommand.StreamOptions{
			Stdout: stdout,
			Stderr: stderr,
			Tty:    false,
		}
		if stdin != nil {
			opts.Stdin = bytes.NewReader(stdin)
		}

		err = executor.StreamWithContext(ctx, opts)

		// Extract exit code from error
		exitCode := 0
		if err != nil {
			exitCode = extractExitCode(err)
			if exitCode >= 0 {
				err = nil // non-zero exit is not a transport error
			}
		}

		outStr := stdout.String()
		errStr := stderr.String()
		if stdout.truncated {
			outStr += "\n...[output truncated]"
		}
		if stderr.truncated {
			errStr += "\n...[output truncated]"
		}

		return outStr, errStr, exitCode, err
	}
}

// buildExecURL constructs the exec URL for a K8s pod.
func buildExecURL(clientset kubernetes.Interface, namespace, podName, containerName string,
	command []string, hasStdin bool) *url.URL {

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(namespace).
		SubResource("exec")

	req.VersionedParams(&corev1.PodExecOptions{
		Container: containerName,
		Command:   command,
		Stdin:     hasStdin,
		Stdout:    true,
		Stderr:    true,
		TTY:       false,
	}, scheme.ParameterCodec)

	return req.URL()
}

// newFallbackExecutor creates a WebSocket executor with SPDY fallback.
func newFallbackExecutor(config *rest.Config, method string, execURL *url.URL) (remotecommand.Executor, error) {
	wsExec, err := remotecommand.NewWebSocketExecutor(config, method, execURL.String())
	if err != nil {
		return nil, fmt.Errorf("websocket executor: %w", err)
	}

	spdyExec, err := remotecommand.NewSPDYExecutor(config, method, execURL)
	if err != nil {
		return nil, fmt.Errorf("spdy executor: %w", err)
	}

	return remotecommand.NewFallbackExecutor(wsExec, spdyExec, func(err error) bool {
		return true // fallback to SPDY on any WebSocket error
	})
}

// extractExitCode extracts the process exit code from a remotecommand error.
// Returns -1 if the error is not an exit code error (transport failure).
func extractExitCode(err error) int {
	if err == nil {
		return 0
	}

	// Check for exec.CodeExitError (wraps the exit code directly)
	var codeErr executil.CodeExitError
	if errors.As(err, &codeErr) {
		return codeErr.Code
	}

	// Check for StatusError with non-zero exit code in status details
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		status := statusErr.Status()
		if status.Reason == rcutil.NonZeroExitCodeReason && status.Details != nil {
			for _, cause := range status.Details.Causes {
				if cause.Type == rcutil.ExitCodeCauseType {
					code, parseErr := strconv.Atoi(cause.Message)
					if parseErr == nil {
						return code
					}
				}
			}
		}
	}

	return -1 // transport error, not an exit code
}
