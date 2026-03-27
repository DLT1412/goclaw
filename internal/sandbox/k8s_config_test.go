package sandbox

import "testing"

func TestK8sConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  K8sConfig
		wantErr bool
	}{
		{
			name:    "valid minimal config",
			config:  K8sConfig{Namespace: "goclaw-sandbox", PVCTemplate: "sandbox-tenant"},
			wantErr: false,
		},
		{
			name:    "empty namespace",
			config:  K8sConfig{Namespace: "", PVCTemplate: "sandbox-test"},
			wantErr: true,
		},
		{
			name:    "invalid namespace uppercase",
			config:  K8sConfig{Namespace: "GoClaw", PVCTemplate: "sandbox-test"},
			wantErr: true,
		},
		{
			name:    "namespace with underscore",
			config:  K8sConfig{Namespace: "goclaw_sandbox", PVCTemplate: "sandbox-test"},
			wantErr: true,
		},
		{
			name:    "pvc template with path traversal",
			config:  K8sConfig{Namespace: "test", PVCTemplate: "sandbox-../etc"},
			wantErr: true,
		},
		{
			name:    "pvc template with slash",
			config:  K8sConfig{Namespace: "test", PVCTemplate: "sandbox/bad"},
			wantErr: true,
		},
		{
			name:    "empty pvc template",
			config:  K8sConfig{Namespace: "test", PVCTemplate: ""},
			wantErr: true,
		},
		{
			name:    "max pod lifetime below minimum",
			config:  K8sConfig{Namespace: "test", PVCTemplate: "sb", MaxPodLifetimeSec: 10},
			wantErr: true,
		},
		{
			name:    "max pod lifetime at minimum",
			config:  K8sConfig{Namespace: "test", PVCTemplate: "sb", MaxPodLifetimeSec: 60},
			wantErr: false,
		},
		{
			name:    "max pod lifetime above hard cap is clamped not error",
			config:  K8sConfig{Namespace: "test", PVCTemplate: "sb", MaxPodLifetimeSec: 50000},
			wantErr: false,
		},
		{
			name: "reserved pod label prefix",
			config: K8sConfig{
				Namespace: "test", PVCTemplate: "sb",
				PodLabels: map[string]string{"goclaw.io/custom": "val"},
			},
			wantErr: true,
		},
		{
			name: "invalid pod label key",
			config: K8sConfig{
				Namespace: "test", PVCTemplate: "sb",
				PodLabels: map[string]string{"!!!invalid": "val"},
			},
			wantErr: true,
		},
		{
			name: "invalid node selector value",
			config: K8sConfig{
				Namespace: "test", PVCTemplate: "sb",
				NodeSelector: map[string]string{"role": "has spaces"},
			},
			wantErr: true,
		},
		{
			name: "invalid image pull secret name",
			config: K8sConfig{
				Namespace: "test", PVCTemplate: "sb",
				ImagePullSecrets: []string{"INVALID_NAME"},
			},
			wantErr: true,
		},
		{
			name: "valid full config",
			config: K8sConfig{
				Namespace: "goclaw-sandbox", PVCTemplate: "sandbox-tenant",
				ServiceAccount: "sandbox-runner", MaxPodLifetimeSec: 3600,
				NodeSelector:     map[string]string{"role": "sandbox"},
				ImagePullSecrets: []string{"registry-creds"},
				PodLabels:        map[string]string{"team": "ai"},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestK8sConfigEffectiveMaxPodLifetime(t *testing.T) {
	tests := []struct {
		name string
		cfg  K8sConfig
		want int
	}{
		{"zero uses default", K8sConfig{MaxPodLifetimeSec: 0}, DefaultMaxPodLifetime},
		{"negative uses default", K8sConfig{MaxPodLifetimeSec: -1}, DefaultMaxPodLifetime},
		{"normal value", K8sConfig{MaxPodLifetimeSec: 1800}, 1800},
		{"above hard cap clamped", K8sConfig{MaxPodLifetimeSec: 99999}, MaxPodLifetimeHardCap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.EffectiveMaxPodLifetime()
			if got != tt.want {
				t.Errorf("EffectiveMaxPodLifetime() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestK8sConfigValidateClampsPodLifetime(t *testing.T) {
	cfg := K8sConfig{Namespace: "test", PVCTemplate: "sb", MaxPodLifetimeSec: 50000}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
	if cfg.MaxPodLifetimeSec != MaxPodLifetimeHardCap {
		t.Errorf("MaxPodLifetimeSec = %d, want %d (clamped)", cfg.MaxPodLifetimeSec, MaxPodLifetimeHardCap)
	}
}
