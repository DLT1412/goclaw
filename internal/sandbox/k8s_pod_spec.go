package sandbox

import (
	"crypto/sha256"
	"fmt"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// buildPodSpec constructs the full pod specification for a sandbox pod.
func (m *K8sManager) buildPodSpec(podName, scopeKey string, cfg Config) *corev1.Pod {
	k := cfg.K8s

	// Labels
	labels := map[string]string{
		"app":               "goclaw-sandbox",
		"role":              "sandbox",
		"goclaw.io/scope":   sanitizeLabel(scopeKey),
		"goclaw.io/gateway": m.gatewayID,
	}
	maps.Copy(labels, k.PodLabels)

	// Security context
	runAsNonRoot := true
	readOnly := cfg.ReadOnlyRoot
	var uid, gid *int64
	if cfg.User != "" {
		parts := strings.SplitN(cfg.User, ":", 2)
		if v := parseInt64(parts[0]); v > 0 {
			uid = &v
		}
		if len(parts) > 1 {
			if v := parseInt64(parts[1]); v > 0 {
				gid = &v
			}
		}
	}

	// Resource limits (Guaranteed QoS: requests == limits)
	mem := resource.MustParse(fmt.Sprintf("%dMi", cfg.MemoryMB))
	cpu := resource.MustParse(fmt.Sprintf("%dm", int(cfg.CPUs*1000)))
	resources := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceMemory: mem, corev1.ResourceCPU: cpu},
		Limits:   corev1.ResourceList{corev1.ResourceMemory: mem, corev1.ResourceCPU: cpu},
	}

	// Volumes: tmpfs for /tmp and /var/tmp
	volumes := []corev1.Volume{
		{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
		{Name: "var-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory}}},
	}
	mounts := []corev1.VolumeMount{
		{Name: "tmp", MountPath: "/tmp"},
		{Name: "var-tmp", MountPath: "/var/tmp"},
	}

	// Workspace PVC mount
	if k.PVCTemplate != "" && cfg.WorkspaceAccess != AccessNone {
		pvcName := k.PVCTemplate
		readOnlyMount := cfg.WorkspaceAccess == AccessRO
		volumes = append(volumes, corev1.Volume{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcName,
					ReadOnly:  readOnlyMount,
				},
			},
		})
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "workspace",
			MountPath: cfg.ContainerWorkdir(),
			SubPath:   scopeKey, // tenant isolation via subPath
			ReadOnly:  readOnlyMount,
		})
	}

	// activeDeadlineSeconds
	deadline := int64(k.EffectiveMaxPodLifetime())

	// Image pull secrets
	var pullSecrets []corev1.LocalObjectReference
	for _, s := range k.ImagePullSecrets {
		pullSecrets = append(pullSecrets, corev1.LocalObjectReference{Name: s})
	}

	automount := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: k.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			ServiceAccountName:           k.ServiceAccount,
			AutomountServiceAccountToken: &automount,
			RestartPolicy:                corev1.RestartPolicyNever,
			ActiveDeadlineSeconds:        &deadline,
			NodeSelector:                 k.NodeSelector,
			ImagePullSecrets:             pullSecrets,
			Containers: []corev1.Container{{
				Name:         "sandbox",
				Image:        cfg.Image,
				Command:      []string{"sleep", "infinity"},
				WorkingDir:   cfg.ContainerWorkdir(),
				Resources:    resources,
				VolumeMounts: mounts,
				SecurityContext: &corev1.SecurityContext{
					RunAsNonRoot:             &runAsNonRoot,
					RunAsUser:                uid,
					RunAsGroup:               gid,
					ReadOnlyRootFilesystem:   &readOnly,
					AllowPrivilegeEscalation: ptrFalse(),
					Capabilities: &corev1.Capabilities{
						Drop: []corev1.Capability{"ALL"},
					},
					SeccompProfile: &corev1.SeccompProfile{
						Type: corev1.SeccompProfileTypeRuntimeDefault,
					},
				},
			}},
			Volumes: volumes,
		},
	}

	// Owner reference for GC on gateway crash
	if m.ownerRef != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*m.ownerRef}
	}

	return pod
}

// buildPodName generates a deterministic pod name from a scope key.
func buildPodName(key string) string {
	safe := sanitizeKey(key)
	if len(safe) > 30 {
		safe = safe[:30]
	}
	h := sha256.Sum256([]byte(key))
	return fmt.Sprintf("goclaw-sbx-%s-%x", safe, h[:4])
}

// sanitizeLabel makes a value safe for K8s labels (max 63 chars, alphanumeric + dash).
func sanitizeLabel(s string) string {
	r := strings.NewReplacer(":", "-", "/", "-", " ", "-", ".", "-")
	v := r.Replace(s)
	if len(v) > 63 {
		v = v[:63]
	}
	v = strings.TrimRight(v, "-")
	if v == "" {
		v = "default"
	}
	return v
}

func parseInt64(s string) int64 {
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}

func ptrFalse() *bool { v := false; return &v }
