package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Compile-time interface compliance assertions.
var (
	_ Manager              = (*K8sManager)(nil)
	_ CredentialedManager  = (*K8sManager)(nil)
	_ Sandbox              = (*K8sSandbox)(nil)
)

// K8sSandbox is a sandbox backed by a Kubernetes pod.
type K8sSandbox struct {
	podName   string
	namespace string
	config    Config
	execFn    ExecFunc
	destroyFn func(ctx context.Context) error
	createdAt time.Time
	lastUsed  time.Time
	mu        sync.Mutex
}

// Exec runs a command inside the K8s pod.
func (s *K8sSandbox) Exec(ctx context.Context, command []string, workDir string, opts ...ExecOption) (*ExecResult, error) {
	s.mu.Lock()
	s.lastUsed = time.Now()
	s.mu.Unlock()

	timeout := time.Duration(s.config.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	o := ApplyExecOpts(opts)
	if len(o.Env) > 0 {
		return nil, fmt.Errorf("K8s sandbox does not support per-exec env injection; use credentialed pod")
	}

	cmd := command
	if workDir != "" {
		cmd = append([]string{"sh", "-c", fmt.Sprintf("cd %q && exec \"$@\"", workDir), "--"}, command...)
	}

	stdout, stderr, exitCode, err := s.execFn(execCtx, nil, cmd)
	if err != nil {
		return nil, fmt.Errorf("k8s exec: %w", err)
	}
	return &ExecResult{ExitCode: exitCode, Stdout: stdout, Stderr: stderr}, nil
}

// Destroy removes the K8s pod.
func (s *K8sSandbox) Destroy(ctx context.Context) error { return s.destroyFn(ctx) }

// ID returns the pod name.
func (s *K8sSandbox) ID() string { return s.podName }

// K8sManager manages K8s sandbox pods based on scope.
type K8sManager struct {
	clientset  kubernetes.Interface
	restConfig *rest.Config
	config     Config
	sandboxes  map[string]*K8sSandbox
	credPods   map[string]*credPodEntry // sessionKey -> cred-pod entry
	mu         sync.RWMutex
	sf         singleflight.Group
	stopCh     chan struct{}
	ownerRef   *metav1.OwnerReference
	gatewayID  string
}

// NewK8sManager creates a manager for K8s sandboxes.
func NewK8sManager(clientset kubernetes.Interface, restConfig *rest.Config, cfg Config, ownerRef *metav1.OwnerReference) *K8sManager {
	gatewayID := os.Getenv("HOSTNAME")
	if gatewayID == "" {
		gatewayID = "unknown"
	}
	m := &K8sManager{
		clientset:  clientset,
		restConfig: restConfig,
		config:     cfg,
		sandboxes:  make(map[string]*K8sSandbox),
		credPods:   make(map[string]*credPodEntry),
		stopCh:     make(chan struct{}),
		ownerRef:   ownerRef,
		gatewayID:  gatewayID,
	}
	m.startPruning()
	return m
}

// Get returns an existing sandbox or creates a new one for the given key.
func (m *K8sManager) Get(ctx context.Context, key string, workspace string, cfgOverride *Config) (Sandbox, error) {
	cfg := m.config
	if cfgOverride != nil {
		cfg = *cfgOverride
	}
	if cfg.Mode == ModeOff {
		return nil, ErrSandboxDisabled
	}

	m.mu.RLock()
	if sb, ok := m.sandboxes[key]; ok {
		m.mu.RUnlock()
		return sb, nil
	}
	m.mu.RUnlock()

	v, err, _ := m.sf.Do(key, func() (any, error) {
		m.mu.RLock()
		if sb, ok := m.sandboxes[key]; ok {
			m.mu.RUnlock()
			return sb, nil
		}
		m.mu.RUnlock()

		sb, err := m.createSandbox(ctx, key, cfg)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.sandboxes[key] = sb
		m.mu.Unlock()
		return sb, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(Sandbox), nil
}

// Release destroys a sandbox by key.
func (m *K8sManager) Release(ctx context.Context, key string) error {
	m.mu.Lock()
	sb, ok := m.sandboxes[key]
	if ok {
		delete(m.sandboxes, key)
	}
	m.mu.Unlock()
	if ok {
		return sb.Destroy(ctx)
	}
	return nil
}

// ReleaseAll destroys all active sandboxes and cred-pods.
func (m *K8sManager) ReleaseAll(ctx context.Context) error {
	m.mu.Lock()
	sbs := make(map[string]*K8sSandbox, len(m.sandboxes))
	maps.Copy(sbs, m.sandboxes)
	m.sandboxes = make(map[string]*K8sSandbox)

	credEntries := make(map[string]*credPodEntry, len(m.credPods))
	maps.Copy(credEntries, m.credPods)
	m.credPods = make(map[string]*credPodEntry)
	m.mu.Unlock()

	for key, sb := range sbs {
		if err := sb.Destroy(ctx); err != nil {
			slog.Warn("failed to release k8s sandbox", "key", key, "error", err)
		}
	}
	ns := ""
	if m.config.K8s != nil {
		ns = m.config.K8s.Namespace
	}
	for key, entry := range credEntries {
		if err := entry.sandbox.Destroy(ctx); err != nil {
			slog.Warn("failed to release k8s cred-pod", "key", key, "error", err)
		}
		if ns != "" {
			_ = m.clientset.CoreV1().Secrets(ns).Delete(ctx, entry.secretName, metav1.DeleteOptions{})
		}
	}
	return nil
}

// Stop signals the pruning goroutine to stop.
func (m *K8sManager) Stop() {
	select {
	case <-m.stopCh:
	default:
		close(m.stopCh)
	}
}

// Stats returns information about active sandboxes.
func (m *K8sManager) Stats() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	pods := make(map[string]string, len(m.sandboxes))
	for key, sb := range m.sandboxes {
		pods[key] = sb.podName
	}
	return map[string]any{
		"mode": m.config.Mode, "backend": "k8s",
		"image": m.config.Image, "active": len(m.sandboxes), "pods": pods,
	}
}

// RecoverOrphans deletes orphaned pods from a previous gateway instance.
func (m *K8sManager) RecoverOrphans(ctx context.Context) error {
	if m.config.K8s == nil {
		return nil
	}
	ns := m.config.K8s.Namespace
	labelSel := fmt.Sprintf("app=goclaw-sandbox,goclaw.io/gateway=%s", m.gatewayID)
	pods, err := m.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
	if err != nil {
		return fmt.Errorf("k8s orphan recovery list: %w", err)
	}
	deleted := 0
	for i := range pods.Items {
		pod := &pods.Items[i]
		if err := m.clientset.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
			slog.Warn("k8s orphan recovery: failed to delete", "pod", pod.Name, "error", err)
		} else {
			slog.Info("k8s orphan recovery: deleted", "pod", pod.Name, "phase", pod.Status.Phase)
			deleted++
		}
	}
	if deleted > 0 {
		slog.Info("k8s orphan recovery completed", "deleted", deleted)
	}
	return nil
}

// createSandbox builds a pod spec, creates it, and waits for Ready.
func (m *K8sManager) createSandbox(ctx context.Context, key string, cfg Config) (*K8sSandbox, error) {
	if cfg.K8s == nil {
		return nil, fmt.Errorf("k8s sandbox: k8s config is nil")
	}
	ns := cfg.K8s.Namespace
	podName := buildPodName(key)
	pod := m.buildPodSpec(podName, key, cfg)

	createCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	created, err := m.clientset.CoreV1().Pods(ns).Create(createCtx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s pod create: %w", err)
	}
	slog.Info("k8s sandbox pod created", "pod", created.Name, "namespace", ns)

	if err := m.waitForPodReady(createCtx, ns, podName); err != nil {
		_ = m.clientset.CoreV1().Pods(ns).Delete(ctx, podName, metav1.DeleteOptions{})
		return nil, fmt.Errorf("k8s pod not ready: %w", err)
	}

	containerName := "sandbox"
	execFn := NewK8sExecFunc(m.restConfig, m.clientset, ns, podName, containerName, cfg.MaxOutputBytes)
	now := time.Now()
	return &K8sSandbox{
		podName: podName, namespace: ns, config: cfg, execFn: execFn,
		createdAt: now, lastUsed: now,
		destroyFn: func(dCtx context.Context) error {
			if err := m.clientset.CoreV1().Pods(ns).Delete(dCtx, podName, metav1.DeleteOptions{}); err != nil {
				slog.Warn("k8s pod destroy failed", "pod", podName, "error", err)
				return err
			}
			slog.Info("k8s sandbox pod destroyed", "pod", podName)
			return nil
		},
	}, nil
}

// waitForPodReady polls until the pod is Running with all containers ready.
func (m *K8sManager) waitForPodReady(ctx context.Context, namespace, podName string) error {
	return wait.PollUntilContextCancel(ctx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		pod, err := m.clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return false, nil
		}
		if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
			return false, fmt.Errorf("pod %s entered terminal phase %s", podName, pod.Status.Phase)
		}
		if pod.Status.Phase != corev1.PodRunning {
			return false, nil
		}
		for _, cs := range pod.Status.ContainerStatuses {
			if !cs.Ready {
				return false, nil
			}
		}
		return true, nil
	})
}

// startPruning launches a background goroutine that prunes idle and terminal pods.
func (m *K8sManager) startPruning() {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.prune(context.Background())
			}
		}
	}()
	slog.Debug("k8s sandbox pruning started", "interval", "1m")
}

// prune removes idle pods and terminal-state pods.
func (m *K8sManager) prune(ctx context.Context) {
	idleTimeout := time.Duration(m.config.IdleTimeoutMin) * time.Minute
	if idleTimeout <= 0 {
		idleTimeout = 20 * time.Minute
	}
	threshold := time.Now().Add(-idleTimeout)

	m.mu.RLock()
	var toRemove []string
	for key, sb := range m.sandboxes {
		sb.mu.Lock()
		lastUsed := sb.lastUsed
		sb.mu.Unlock()
		if lastUsed.Before(threshold) {
			toRemove = append(toRemove, key)
		}
	}
	m.mu.RUnlock()

	for _, key := range toRemove {
		m.mu.Lock()
		sb, ok := m.sandboxes[key]
		if ok {
			delete(m.sandboxes, key)
		}
		m.mu.Unlock()
		if ok {
			if err := sb.Destroy(ctx); err != nil {
				slog.Warn("k8s prune: failed to destroy idle pod", "key", key, "error", err)
			} else {
				slog.Info("k8s pruned idle sandbox pod", "key", key, "pod", sb.podName)
			}
		}
	}

	// Prune idle cred-pods
	m.mu.RLock()
	var credToRemove []string
	for key, entry := range m.credPods {
		entry.sandbox.mu.Lock()
		lastUsed := entry.sandbox.lastUsed
		entry.sandbox.mu.Unlock()
		if lastUsed.Before(threshold) {
			credToRemove = append(credToRemove, key)
		}
	}
	m.mu.RUnlock()
	for _, key := range credToRemove {
		_ = m.ReleaseCredentialed(ctx, key)
		slog.Info("k8s pruned idle cred-pod", "key", key)
	}

	// Delete terminal pods (Failed/Succeeded from activeDeadlineSeconds)
	if m.config.K8s != nil {
		ns := m.config.K8s.Namespace
		labelSel := fmt.Sprintf("app=goclaw-sandbox,goclaw.io/gateway=%s", m.gatewayID)
		pods, err := m.clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: labelSel})
		if err != nil {
			slog.Warn("k8s prune: list failed", "error", err)
			return
		}
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
				if err := m.clientset.CoreV1().Pods(ns).Delete(ctx, pod.Name, metav1.DeleteOptions{}); err != nil {
					slog.Warn("k8s prune: failed to delete terminal pod", "pod", pod.Name, "error", err)
				} else {
					slog.Info("k8s pruned terminal pod", "pod", pod.Name, "phase", pod.Status.Phase)
				}
			}
		}
	}
}
