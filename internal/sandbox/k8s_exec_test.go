package sandbox

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	rcutil "k8s.io/apimachinery/pkg/util/remotecommand"
	executil "k8s.io/utils/exec"
)

func TestExtractExitCode(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"nil error", nil, 0},
		{"non-exit error", fmt.Errorf("network timeout"), -1},
		{
			"CodeExitError code 1",
			executil.CodeExitError{Err: fmt.Errorf("exit 1"), Code: 1},
			1,
		},
		{
			"CodeExitError code 127",
			executil.CodeExitError{Err: fmt.Errorf("not found"), Code: 127},
			127,
		},
		{
			"StatusError with NonZeroExitCode reason",
			&apierrors.StatusError{ErrStatus: metav1.Status{
				Reason: rcutil.NonZeroExitCodeReason,
				Details: &metav1.StatusDetails{
					Causes: []metav1.StatusCause{
						{Type: rcutil.ExitCodeCauseType, Message: "42"},
					},
				},
			}},
			42,
		},
		{
			"StatusError without ExitCode cause",
			&apierrors.StatusError{ErrStatus: metav1.Status{
				Reason:  rcutil.NonZeroExitCodeReason,
				Details: &metav1.StatusDetails{Causes: []metav1.StatusCause{}},
			}},
			-1,
		},
		{
			"StatusError with different reason",
			&apierrors.StatusError{ErrStatus: metav1.Status{
				Reason: metav1.StatusReasonNotFound,
			}},
			-1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := extractExitCode(tt.err)
			if code != tt.wantCode {
				t.Errorf("extractExitCode() = %d, want %d", code, tt.wantCode)
			}
		})
	}
}
