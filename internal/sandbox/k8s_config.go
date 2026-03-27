package sandbox

import (
	"fmt"
	"regexp"
	"strings"
)

// K8sConfig holds Kubernetes-specific sandbox configuration.
// Only used when Backend == BackendK8s.
type K8sConfig struct {
	Namespace         string            `json:"namespace"`
	ServiceAccount    string            `json:"service_account,omitempty"`
	PVCTemplate       string            `json:"pvc_template"`
	MaxPodLifetimeSec int               `json:"max_pod_lifetime_sec,omitempty"` // default 3600, hard cap 28800 (8h)
	NodeSelector      map[string]string `json:"node_selector,omitempty"`
	ImagePullSecrets  []string          `json:"image_pull_secrets,omitempty"`
	PodLabels         map[string]string `json:"pod_labels,omitempty"`
}

const (
	// MaxPodLifetimeHardCap is the absolute maximum pod lifetime (8 hours).
	// Non-configurable — enforced regardless of user config.
	MaxPodLifetimeHardCap = 28800

	// DefaultMaxPodLifetime is the default activeDeadlineSeconds for sandbox pods (1 hour).
	DefaultMaxPodLifetime = 3600

	// DefaultIdleTimeoutMin is the default idle timeout before pod deletion.
	DefaultIdleTimeoutMin = 20

	// minPodLifetime is the minimum allowed pod lifetime (1 minute).
	minPodLifetime = 60

	// reservedLabelPrefix is reserved for system labels — custom pod labels must not use it.
	reservedLabelPrefix = "goclaw.io/"
)

// RFC 1123 label: lowercase alphanumeric or '-', must start/end with alphanumeric, max 63 chars.
var rfc1123LabelRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// K8s label value: alphanumeric, '-', '_', '.', max 63 chars, must start/end with alphanumeric (or empty).
var k8sLabelValueRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9._-]{0,61}[a-zA-Z0-9])?)?$`)

// K8s label key: optional prefix (DNS subdomain + '/') + name segment (alphanumeric, '-', '_', '.').
var k8sLabelKeyRegex = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9.-]{0,252}[a-zA-Z0-9])?/)?[a-zA-Z0-9]([a-zA-Z0-9._-]{0,61}[a-zA-Z0-9])?$`)

// DefaultK8sConfig returns sensible defaults for K8s sandbox.
func DefaultK8sConfig() *K8sConfig {
	return &K8sConfig{
		Namespace:         "goclaw-sandbox",
		ServiceAccount:    "sandbox-runner",
		PVCTemplate:       "sandbox-{tenant_id}",
		MaxPodLifetimeSec: DefaultMaxPodLifetime,
	}
}

// Validate checks K8s config for correctness. Called at gateway startup.
func (c *K8sConfig) Validate() error {
	if c.Namespace == "" {
		return fmt.Errorf("k8s sandbox: namespace is required")
	}
	if !rfc1123LabelRegex.MatchString(c.Namespace) {
		return fmt.Errorf("k8s sandbox: namespace %q is not a valid RFC 1123 label", c.Namespace)
	}

	if c.PVCTemplate == "" {
		return fmt.Errorf("k8s sandbox: pvc_template is required")
	}
	if strings.Contains(c.PVCTemplate, "..") || strings.Contains(c.PVCTemplate, "/") {
		return fmt.Errorf("k8s sandbox: pvc_template %q must not contain '..' or '/'", c.PVCTemplate)
	}

	// Enforce pod lifetime bounds.
	if c.MaxPodLifetimeSec > 0 && c.MaxPodLifetimeSec < minPodLifetime {
		return fmt.Errorf("k8s sandbox: max_pod_lifetime_sec must be >= %d (got %d)", minPodLifetime, c.MaxPodLifetimeSec)
	}
	if c.MaxPodLifetimeSec > MaxPodLifetimeHardCap {
		c.MaxPodLifetimeSec = MaxPodLifetimeHardCap
	}

	// Validate custom pod labels.
	for k, v := range c.PodLabels {
		if strings.HasPrefix(k, reservedLabelPrefix) {
			return fmt.Errorf("k8s sandbox: pod_labels key %q uses reserved prefix %q", k, reservedLabelPrefix)
		}
		if !k8sLabelKeyRegex.MatchString(k) {
			return fmt.Errorf("k8s sandbox: pod_labels key %q is not a valid K8s label key", k)
		}
		if !k8sLabelValueRegex.MatchString(v) {
			return fmt.Errorf("k8s sandbox: pod_labels value %q for key %q is not a valid K8s label value", v, k)
		}
	}

	// Validate node selector labels.
	for k, v := range c.NodeSelector {
		if !k8sLabelKeyRegex.MatchString(k) {
			return fmt.Errorf("k8s sandbox: node_selector key %q is not a valid K8s label key", k)
		}
		if !k8sLabelValueRegex.MatchString(v) {
			return fmt.Errorf("k8s sandbox: node_selector value %q for key %q is not a valid K8s label value", v, k)
		}
	}

	// Validate image pull secret names (RFC 1123 DNS subdomain).
	for _, name := range c.ImagePullSecrets {
		if !rfc1123LabelRegex.MatchString(name) {
			return fmt.Errorf("k8s sandbox: image_pull_secrets entry %q is not a valid K8s name", name)
		}
	}

	return nil
}

// EffectiveMaxPodLifetime returns the active deadline in seconds,
// applying defaults and hard cap.
func (c *K8sConfig) EffectiveMaxPodLifetime() int {
	v := c.MaxPodLifetimeSec
	if v <= 0 {
		v = DefaultMaxPodLifetime
	}
	if v > MaxPodLifetimeHardCap {
		v = MaxPodLifetimeHardCap
	}
	return v
}
