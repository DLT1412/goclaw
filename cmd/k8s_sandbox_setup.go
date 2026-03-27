package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/nextlevelbuilder/goclaw/internal/sandbox"
)

// initK8sClient creates a Kubernetes clientset.
// Tries in-cluster config first (production), falls back to kubeconfig (development).
func initK8sClient() (kubernetes.Interface, *rest.Config, error) {
	restConfig, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, nil, fmt.Errorf("k8s client config: %w", err)
		}
		slog.Info("k8s client using kubeconfig", "path", kubeconfig)
	} else {
		slog.Info("k8s client using in-cluster config")
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s clientset: %w", err)
	}
	return clientset, restConfig, nil
}

// resolveGatewayOwnerRef looks up the gateway's own pod to get its UID for ownerReference.
// Returns nil if lookup fails (running outside K8s in dev) — not fatal.
func resolveGatewayOwnerRef(ctx context.Context, clientset kubernetes.Interface) *metav1.OwnerReference {
	hostname := os.Getenv("HOSTNAME")
	if hostname == "" {
		slog.Warn("sandbox.k8s: HOSTNAME not set, ownerReference disabled")
		return nil
	}

	namespace := os.Getenv("POD_NAMESPACE")
	if namespace == "" {
		nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
		if err != nil {
			slog.Warn("sandbox.k8s: cannot determine namespace, ownerReference disabled", "error", err)
			return nil
		}
		namespace = strings.TrimSpace(string(nsBytes))
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pod, err := clientset.CoreV1().Pods(namespace).Get(lookupCtx, hostname, metav1.GetOptions{})
	if err != nil {
		slog.Warn("sandbox.k8s: self-lookup failed, ownerReference disabled",
			"hostname", hostname, "namespace", namespace, "error", err)
		return nil
	}

	slog.Info("sandbox.k8s: gateway self-lookup succeeded",
		"pod", pod.Name, "uid", pod.UID, "namespace", namespace)

	return &metav1.OwnerReference{
		APIVersion: "v1",
		Kind:       "Pod",
		Name:       pod.Name,
		UID:        pod.UID,
	}
}

// validateK8sSandboxConfig validates the K8s sandbox configuration at startup.
// Returns an error if the config is invalid (fail-closed).
func validateK8sSandboxConfig(cfg sandbox.Config) error {
	if cfg.K8s == nil {
		return fmt.Errorf("sandbox backend is 'k8s' but k8s config is missing")
	}

	if err := cfg.K8s.Validate(); err != nil {
		return fmt.Errorf("sandbox k8s config: %w", err)
	}

	// Fail-closed: K8s + network_enabled + restricted_domains is unsupported
	if cfg.NetworkEnabled && len(cfg.RestrictedDomains) > 0 {
		return fmt.Errorf("sandbox: network_enabled with restricted_domains is not supported in K8s mode; " +
			"use NetworkPolicy for network isolation or set network_enabled: false")
	}

	// Warn: setup_command unsupported in K8s v1
	if cfg.SetupCommand != "" {
		slog.Warn("sandbox.k8s: setup_command is not supported in K8s mode (v1), ignoring",
			"setup_command", cfg.SetupCommand)
	}

	// IdleTimeoutMin validation
	if cfg.IdleTimeoutMin < 1 {
		return fmt.Errorf("sandbox: idle_timeout_min must be >= 1 (got %d); use mode: 'off' to disable sandbox",
			cfg.IdleTimeoutMin)
	}

	return nil
}
