package sandbox

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// credPodEntry tracks a credentialed pod and its associated K8s Secret.
type credPodEntry struct {
	sandbox    *K8sSandbox
	secretName string
}

// GetCredentialedPod returns (or creates) a cred-pod with env vars from a K8s Secret.
func (m *K8sManager) GetCredentialedPod(ctx context.Context, sessionKey, tenantID string, envMap map[string]string) (Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if entry, ok := m.credPods[sessionKey]; ok {
		return entry.sandbox, nil
	}

	if m.config.K8s == nil {
		return nil, fmt.Errorf("k8s sandbox: k8s config is nil")
	}
	ns := m.config.K8s.Namespace
	hash8 := shortHash(sessionKey)
	tenantShort := shortTenant(tenantID)
	secretName := fmt.Sprintf("sbx-cred-%s-%s", tenantShort, hash8)
	podName := fmt.Sprintf("goclaw-cred-%s-%s", tenantShort, hash8)

	if err := m.createCredSecret(ctx, secretName, ns, envMap); err != nil {
		return nil, fmt.Errorf("create cred secret: %w", err)
	}

	sb, err := m.createCredPod(ctx, podName, ns, secretName, sessionKey)
	if err != nil {
		_ = m.clientset.CoreV1().Secrets(ns).Delete(ctx, secretName, metav1.DeleteOptions{})
		return nil, fmt.Errorf("create cred pod: %w", err)
	}

	m.credPods[sessionKey] = &credPodEntry{sandbox: sb, secretName: secretName}
	return sb, nil
}

// ReleaseCredentialed deletes a cred-pod and its Secret.
func (m *K8sManager) ReleaseCredentialed(ctx context.Context, sessionKey string) error {
	m.mu.Lock()
	entry, ok := m.credPods[sessionKey]
	if ok {
		delete(m.credPods, sessionKey)
	}
	m.mu.Unlock()

	if !ok {
		return nil
	}
	if m.config.K8s == nil {
		return nil
	}

	ns := m.config.K8s.Namespace
	if err := entry.sandbox.Destroy(ctx); err != nil {
		slog.Warn("failed to destroy cred pod", "session", sessionKey, "error", err)
	}
	if err := m.clientset.CoreV1().Secrets(ns).Delete(ctx, entry.secretName, metav1.DeleteOptions{}); err != nil {
		slog.Warn("failed to delete cred secret", "session", sessionKey, "secret", entry.secretName, "error", err)
	}
	return nil
}

func (m *K8sManager) createCredSecret(ctx context.Context, name, namespace string, envMap map[string]string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"goclaw.io/retention": "session",
				"goclaw.io/gateway":   m.gatewayID,
			},
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: envMap,
	}
	if m.ownerRef != nil {
		secret.OwnerReferences = []metav1.OwnerReference{*m.ownerRef}
	}
	_, err := m.clientset.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{})
	return err
}

func (m *K8sManager) createCredPod(ctx context.Context, podName, namespace, secretName, sessionKey string) (*K8sSandbox, error) {
	cfg := m.config
	k := cfg.K8s

	pod := m.buildPodSpec(podName, sessionKey, cfg)
	pod.Labels["role"] = "sandbox-cred"
	pod.Labels["goclaw.io/retention"] = "session"
	pod.Labels["goclaw.io/session"] = sanitizeLabel(sessionKey)

	// Add envFrom: secretRef to inject credentials as env vars
	pod.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{
		SecretRef: &corev1.SecretEnvSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
		},
	}}

	createCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	created, err := m.clientset.CoreV1().Pods(namespace).Create(createCtx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("k8s cred-pod create: %w", err)
	}
	slog.Info("k8s cred-pod created", "pod", created.Name, "namespace", namespace)

	if err := m.waitForPodReady(createCtx, namespace, podName); err != nil {
		_ = m.clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
		return nil, fmt.Errorf("k8s cred-pod not ready: %w", err)
	}

	containerName := "sandbox"
	execFn := NewK8sExecFunc(m.restConfig, m.clientset, namespace, podName, containerName, cfg.MaxOutputBytes)

	now := time.Now()
	return &K8sSandbox{
		podName:   podName,
		namespace: k.Namespace,
		config:    cfg,
		execFn:    execFn,
		createdAt: now,
		lastUsed:  now,
		destroyFn: func(dCtx context.Context) error {
			return m.clientset.CoreV1().Pods(namespace).Delete(dCtx, podName, metav1.DeleteOptions{})
		},
	}, nil
}

func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", h[:4])
}

func shortTenant(tenantID string) string {
	if len(tenantID) > 8 {
		return tenantID[:8]
	}
	return tenantID
}
