# Research: Kubernetes Sandbox (like Docker Sandbox)

## 1. Current Docker Sandbox Architecture

The existing sandbox system (`internal/sandbox/`) provides isolated code execution via Docker containers:

### Interfaces
```go
// Sandbox — single execution environment
type Sandbox interface {
    Exec(ctx, command, workDir, ...ExecOption) (*ExecResult, error)
    Destroy(ctx) error
    ID() string
}

// Manager — lifecycle management
type Manager interface {
    Get(ctx, key, workspace, *Config) (Sandbox, error)
    Release(ctx, key) error
    ReleaseAll(ctx) error
    Stop()
    Stats() map[string]any
}
```

### How It Works
1. **Container creation**: `docker run -d --name {prefix}{key} --read-only --cap-drop ALL --network none --memory 512m --cpus 1.0 {image} sleep infinity`
2. **Execution**: `docker exec [-e ENV] [-w dir] {containerID} {command}`
3. **File ops**: `FsBridge` routes read/write/list through `docker exec` inside the container
4. **Workspace sharing**: Host dir bind-mounted to `/workspace` (ro or rw)
5. **Scope-based reuse**: session (one per session), agent (one per agent), shared (one for all)
6. **Background pruning**: Goroutine prunes idle (>24h) or old (>7d) containers
7. **DooD support**: When GoClaw runs inside Docker, resolves host paths via `docker inspect`

### Security Model
- Read-only root filesystem (`--read-only`)
- All capabilities dropped (`--cap-drop ALL`)
- No new privileges (`--security-opt no-new-privileges`)
- Network disabled by default (`--network none`)
- Resource limits (memory, CPU, PIDs)
- Output truncation (1MB default, prevents OOM)
- Tmpfs for writable temp dirs
- Fail-closed: if Docker unavailable, refuse to execute (no host fallback)

### Integration Points
- `tools/shell.go` — Routes exec through sandbox when sandboxKey present in context
- `tools/filesystem.go`, `filesystem_write.go`, `filesystem_list.go`, `edit.go` — File ops via FsBridge
- `tools/sandbox_hints.go` — LLM-friendly error hints for sandbox-specific failures
- `agent/loop_context.go` — Injects sandbox config into agent context
- `tools/registry.go` — Injects sandboxKey from sessionKey

---

## 2. Why Kubernetes Sandbox?

### Limitations of Docker Sandbox
| Limitation | Impact |
|---|---|
| Requires Docker daemon on host | Cannot run in managed K8s clusters without DinD/DooD |
| Single-node only | Sandbox containers compete with GoClaw for host resources |
| Manual scaling | No auto-scaling of sandbox capacity |
| No GPU scheduling | Cannot leverage K8s GPU scheduling for ML workloads |
| No native multi-tenancy | Container isolation weaker than Pod security + NetworkPolicy |
| DooD complexity | `docker_resolve.go` complexity for nested containers |

### Benefits of K8s Sandbox
- **Cloud-native deployment**: Works naturally when GoClaw runs in K8s
- **Multi-node scheduling**: Sandboxes spread across cluster nodes
- **Resource quotas**: K8s ResourceQuotas + LimitRanges for tenant isolation
- **Network policies**: Fine-grained network isolation via NetworkPolicy
- **GPU/special hardware**: K8s scheduler can assign GPU pods for ML tools
- **Pod Security Standards**: Restricted/Baseline profiles for hardening
- **Auto-cleanup**: K8s TTL controllers, Job TTL-after-finished
- **Observability**: Native integration with Prometheus, logging stacks
- **gVisor/Kata**: Can use runtime classes for stronger isolation (sandboxed kernels)

---

## 3. K8s Sandbox Design

### 3.1 Architecture Overview

```
                  GoClaw Pod
                  ┌─────────────────┐
                  │  Agent Loop      │
                  │       │          │
                  │  Tool Registry   │
                  │       │          │
                  │  K8sManager      │──── K8s API ────▶  Sandbox Pod 1
                  │  (implements     │                    Sandbox Pod 2
                  │   Manager)       │                    Sandbox Pod N
                  └─────────────────┘
```

The K8s sandbox creates ephemeral **Pods** instead of Docker containers. Each Pod runs the same sandbox image with the same security constraints, but orchestrated via the Kubernetes API.

### 3.2 Implementation: `k8s.go`

```go
// KubeSandbox is a sandbox backed by a Kubernetes Pod.
type KubeSandbox struct {
    podName     string
    namespace   string
    containerName string  // container within the pod
    config      Config
    workspace   string
    createdAt   time.Time
    lastUsed    time.Time
    mu          sync.Mutex
}
```

**Key differences from Docker sandbox:**

| Aspect | Docker | K8s |
|---|---|---|
| Create | `docker run -d` | Create Pod spec + `client.CoreV1().Pods().Create()` |
| Exec | `docker exec` | K8s exec API via SPDY/WebSocket (`remotecommand.NewSPDYExecutor`) |
| Destroy | `docker rm -f` | `client.CoreV1().Pods().Delete()` |
| File I/O | `docker exec cat/sh -c cat >` | K8s exec same approach, or ephemeral volume + copy |
| Workspace | `-v host:container:rw` | PVC mount, emptyDir, or ConfigMap |
| Network | `--network none` | NetworkPolicy (deny-all egress) |
| Resources | `--memory`, `--cpus` | Pod resource requests/limits |
| Security | `--cap-drop ALL`, `--read-only` | SecurityContext, PodSecurityStandard |

### 3.3 Pod Spec Template

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: goclaw-sbx-{sanitized-key}
  namespace: {sandbox-namespace}
  labels:
    app.kubernetes.io/managed-by: goclaw
    goclaw.sandbox: "true"
    goclaw.sandbox-scope: session|agent|shared
  annotations:
    goclaw.sandbox/created-at: "2024-01-01T00:00:00Z"
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  enableServiceLinks: false
  securityContext:
    runAsNonRoot: true
    runAsUser: 65534     # nobody
    runAsGroup: 65534
    fsGroup: 65534
    seccompProfile:
      type: RuntimeDefault
  containers:
  - name: sandbox
    image: goclaw-sandbox:bookworm-slim
    command: ["sleep", "infinity"]
    securityContext:
      allowPrivilegeEscalation: false
      readOnlyRootFilesystem: true
      capabilities:
        drop: ["ALL"]
    resources:
      requests:
        memory: "256Mi"
        cpu: "250m"
      limits:
        memory: "512Mi"
        cpu: "1000m"
    volumeMounts:
    - name: workspace
      mountPath: /workspace
    - name: tmp
      mountPath: /tmp
    - name: var-tmp
      mountPath: /var/tmp
    - name: run
      mountPath: /run
  volumes:
  - name: workspace
    persistentVolumeClaim:
      claimName: goclaw-workspace-{key}  # or emptyDir for ephemeral
  - name: tmp
    emptyDir:
      sizeLimit: 64Mi
  - name: var-tmp
    emptyDir:
      sizeLimit: 64Mi
  - name: run
    emptyDir:
      sizeLimit: 16Mi
  # Optional: activeDeadlineSeconds for auto-cleanup
  activeDeadlineSeconds: 86400  # 24h max lifetime
```

### 3.4 Network Isolation

```yaml
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: goclaw-sandbox-deny-all
  namespace: {sandbox-namespace}
spec:
  podSelector:
    matchLabels:
      goclaw.sandbox: "true"
  policyTypes:
  - Ingress
  - Egress
  # Empty ingress/egress = deny all (equivalent to --network none)
```

When `NetworkEnabled: true`, create a permissive policy or skip the deny-all. For `RestrictedDomains`, use an egress NetworkPolicy with CIDR rules (requires DNS resolution at policy creation time or an external DNS-based policy engine like Cilium).

### 3.5 Workspace Strategies

| Strategy | Use Case | Mechanism |
|---|---|---|
| **EmptyDir** | Ephemeral, no persistence | `emptyDir: {}` on pod |
| **PVC (ReadWriteOnce)** | Session-scoped, persistent | Dynamically provisioned PVC per session |
| **PVC (ReadWriteMany)** | Shared workspace | Single PVC mounted by all sandbox pods (needs RWX storage class like NFS/EFS) |
| **Init container copy** | Read-only workspace from host | Init container copies from shared PVC → emptyDir |
| **CSI ephemeral** | Cloud-native temp storage | CSI driver provisions ephemeral volume per pod |

**Recommended default**: EmptyDir for ephemeral sandboxes, PVC for persistent workspace access.

For the common case where the agent workspace lives on the GoClaw pod's filesystem:
1. GoClaw pod mounts a PVC at `/data/workspaces`
2. Sandbox pods mount the same PVC (requires ReadWriteMany)
3. Each sandbox sees only its subdirectory via `subPath`

### 3.6 Exec via K8s API

```go
func (s *KubeSandbox) Exec(ctx context.Context, command []string, workDir string, opts ...ExecOption) (*ExecResult, error) {
    s.mu.Lock()
    s.lastUsed = time.Now()
    s.mu.Unlock()

    o := ApplyExecOpts(opts)

    // Build the command: wrap in sh -c with cd + env
    shellCmd := buildShellCommand(command, workDir, o.Env)

    req := s.client.CoreV1().RESTClient().Post().
        Resource("pods").
        Name(s.podName).
        Namespace(s.namespace).
        SubResource("exec").
        VersionedParams(&corev1.PodExecOptions{
            Container: s.containerName,
            Command:   shellCmd,
            Stdin:     false,
            Stdout:    true,
            Stderr:    true,
        }, scheme.ParameterCodec)

    executor, err := remotecommand.NewSPDYExecutor(s.restConfig, "POST", req.URL())
    if err != nil {
        return nil, fmt.Errorf("k8s exec setup: %w", err)
    }

    timeout := time.Duration(s.config.TimeoutSec) * time.Second
    execCtx, cancel := context.WithTimeout(ctx, timeout)
    defer cancel()

    stdout := &limitedBuffer{max: s.config.MaxOutputBytes}
    stderr := &limitedBuffer{max: s.config.MaxOutputBytes}

    err = executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
        Stdout: stdout,
        Stderr: stderr,
    })

    exitCode := 0
    if err != nil {
        // Extract exit code from exec error
        if execErr, ok := err.(utilexec.ExitError); ok {
            exitCode = execErr.ExitStatus()
            err = nil
        } else {
            return nil, fmt.Errorf("k8s exec: %w", err)
        }
    }

    return &ExecResult{
        ExitCode: exitCode,
        Stdout:   stdout.String(),
        Stderr:   stderr.String(),
    }, nil
}
```

### 3.7 FsBridge for K8s

The `FsBridge` approach translates directly — instead of `docker exec cat`, use K8s exec. The bridge wraps the same `Exec` call:

```go
type K8sFsBridge struct {
    sandbox *KubeSandbox
    workdir string
}

func (b *K8sFsBridge) ReadFile(ctx context.Context, path string) (string, error) {
    resolved := b.resolvePath(path)
    result, err := b.sandbox.Exec(ctx, []string{"cat", "--", resolved}, "")
    if err != nil {
        return "", err
    }
    if result.ExitCode != 0 {
        return "", fmt.Errorf("read failed: %s", result.Stderr)
    }
    return result.Stdout, nil
}
```

This reuses the existing `resolvePath` logic and `limitedBuffer` from Docker FsBridge.

### 3.8 K8s Manager

```go
type KubeManager struct {
    client    kubernetes.Interface
    restConfig *rest.Config
    config    Config
    namespace string
    sandboxes map[string]*KubeSandbox
    mu        sync.RWMutex
    stopCh    chan struct{}
}
```

The manager mirrors `DockerManager` behavior:
- `Get()`: Check cache → Create Pod → Wait for Running → Return `KubeSandbox`
- `Release()`: Delete Pod
- `ReleaseAll()`: List pods by label → Delete all
- `Prune()`: List pods by label → Check annotations for age/idle → Delete stale ones
- Pruning can also leverage K8s `activeDeadlineSeconds` as a hard ceiling

### 3.9 Config Extensions

```go
type Config struct {
    // ... existing fields ...

    // K8s-specific
    Runtime          string `json:"runtime"`           // "docker" or "k8s"
    Namespace        string `json:"namespace"`         // K8s namespace for sandbox pods
    ServiceAccount   string `json:"service_account"`   // SA for sandbox pods (default: none/default)
    StorageClass     string `json:"storage_class"`     // For PVC-based workspaces
    RuntimeClassName string `json:"runtime_class_name"` // e.g. "gvisor" for extra isolation
    NodeSelector     map[string]string `json:"node_selector,omitempty"`
    Tolerations      []string `json:"tolerations,omitempty"`
    ImagePullSecret  string `json:"image_pull_secret,omitempty"`

    // Workspace strategy: "emptydir", "pvc", "pvc-shared"
    WorkspaceStrategy string `json:"workspace_strategy"`
}
```

### 3.10 Manager Factory

```go
func NewManager(cfg Config) (Manager, error) {
    switch cfg.Runtime {
    case "k8s", "kubernetes":
        restConfig, err := rest.InClusterConfig()
        if err != nil {
            return nil, fmt.Errorf("k8s sandbox requires in-cluster config: %w", err)
        }
        client, err := kubernetes.NewForConfig(restConfig)
        if err != nil {
            return nil, fmt.Errorf("k8s client: %w", err)
        }
        return NewKubeManager(cfg, client, restConfig), nil
    default:
        return NewDockerManager(cfg), nil
    }
}
```

---

## 4. Comparison: Docker vs K8s Sandbox

| Feature | Docker Sandbox | K8s Sandbox |
|---|---|---|
| **Dependencies** | Docker daemon on host | K8s cluster + API access |
| **Deployment** | Bare metal, VMs, Docker-in-Docker | Any K8s cluster (EKS, GKE, AKS, self-hosted) |
| **Create latency** | ~200-500ms | ~1-5s (pod scheduling + image pull) |
| **Exec latency** | ~10-50ms (local docker exec) | ~50-200ms (API server → kubelet → CRI) |
| **Network isolation** | `--network none` | NetworkPolicy (CNI-dependent) |
| **Filesystem isolation** | `--read-only` + bind mount | `readOnlyRootFilesystem` + volumes |
| **Resource limits** | cgroups v1/v2 | K8s requests/limits (cgroups) |
| **Stronger isolation** | User namespaces (limited) | gVisor, Kata Containers (RuntimeClass) |
| **Multi-node** | No | Yes (K8s scheduler) |
| **Auto-scaling** | No | Pod autoscaling, cluster autoscaler |
| **Observability** | Docker logs | K8s metrics, logs, events |
| **Cleanup** | Background goroutine | TTL controller + background goroutine |
| **Complexity** | Low | Medium-high |
| **RBAC** | Docker socket access | K8s RBAC (pods/exec in sandbox namespace) |

---

## 5. Implementation Plan

### Phase 1: Core K8s Sandbox (MVP)
1. **`k8s.go`** — `KubeSandbox` implementing `Sandbox` interface
   - Pod creation with security context
   - Exec via SPDY/WebSocket
   - Destroy via pod deletion
2. **`k8s_manager.go`** — `KubeManager` implementing `Manager` interface
   - Pod lifecycle management
   - Label-based pod discovery
   - Background pruning
3. **`k8s_fsbridge.go`** — `K8sFsBridge` (wraps KubeSandbox.Exec)
4. **Config extension** — Add `Runtime` field, namespace, etc.
5. **Factory function** — `NewManager()` dispatches to Docker or K8s

### Phase 2: Workspace & Storage
6. **EmptyDir workspaces** — Default for ephemeral sandboxes
7. **PVC workspaces** — Dynamic PVC creation for persistent sessions
8. **Shared PVC** — ReadWriteMany for agent-scope shared workspace

### Phase 3: Network & Security
9. **NetworkPolicy** — Auto-create deny-all policy for sandbox namespace
10. **RuntimeClass support** — gVisor/Kata for high-security tenants
11. **Pod Security Admission** — Enforce Restricted PSS on sandbox namespace

### Phase 4: Production Hardening
12. **Pod readiness probing** — Wait for container ready before first exec
13. **Exec timeout & retry** — Handle transient API server errors
14. **Resource quotas** — Per-tenant ResourceQuota in sandbox namespace
15. **Image pull caching** — DaemonSet to pre-pull sandbox images
16. **Metrics** — Sandbox creation/exec/error counters

### Dependencies
- `k8s.io/client-go` — K8s API client
- `k8s.io/apimachinery` — K8s types
- Build-tag gated: `//go:build k8s` to keep K8s deps optional

### Estimated Scope
- Phase 1: ~600-800 lines of Go (core sandbox + manager + factory)
- Phase 2: ~200-300 lines (workspace strategies)
- Phase 3: ~150-200 lines (network policy + runtime class)
- Phase 4: ~200-300 lines (hardening)

---

## 6. Key Design Decisions

### 6.1 Build-tag gating
K8s client-go is a heavy dependency (~30MB). Gate with `//go:build k8s` so users who only need Docker sandbox don't pay the binary size cost.

### 6.2 Pod startup latency
K8s pods take 1-5s to start (vs 200-500ms for Docker). Mitigations:
- **Pre-warm pool**: Maintain N warm pods ready for assignment
- **Image pre-pull DaemonSet**: Ensure image is cached on all nodes
- **Scope = agent/shared**: Reuse pods across sessions to amortize startup

### 6.3 Exec path
Two options:
1. **K8s exec API** (SPDY/WebSocket) — Standard, works everywhere, ~50-200ms per call
2. **Direct kubelet exec** — Lower latency but requires kubelet network access, less portable

Recommend option 1 for portability.

### 6.4 Workspace sharing
The Docker sandbox uses bind mounts (`-v host:container`). In K8s:
- GoClaw and sandbox pods must share storage
- Simplest: Both mount the same PVC (requires ReadWriteMany storage class)
- Alternative: GoClaw copies files to sandbox pod via tar pipe (slower but works with any storage)

### 6.5 Namespace isolation
Dedicate a namespace (e.g., `goclaw-sandbox`) for all sandbox pods. Apply:
- `ResourceQuota` — Limit total pods, CPU, memory
- `LimitRange` — Default per-pod limits
- `NetworkPolicy` — Deny all by default
- `PodSecurity` — Restricted level

### 6.6 Cleanup guarantees
Docker sandbox cleanup relies on the GoClaw process running the prune goroutine. If GoClaw crashes, containers linger. K8s offers:
- `activeDeadlineSeconds` on Pod — Hard lifetime limit
- K8s TTL controller — Auto-delete completed Jobs
- Label-based reconciliation — On startup, list+delete orphaned sandbox pods
- CronJob — External cleanup job that prunes old sandbox pods

---

## 7. Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Pod scheduling latency | Slow first exec | Pre-warm pool, image pre-pull |
| API server unavailability | Cannot create/exec sandboxes | Retry with backoff, fall-closed |
| PVC provisioning delay | Slow workspace setup | EmptyDir default, async PVC |
| NetworkPolicy CNI dependency | `--network none` equivalent may not work | Require Calico/Cilium, document requirement |
| client-go version conflicts | Dependency hell | Build-tag gate, separate go.mod |
| RBAC misconfiguration | Sandbox pods can't be created | Clear docs, helm chart with proper roles |
| Pod OOM kill | Sandbox killed by K8s | Set memory limits, handle OOMKilled status |
| Exec WebSocket instability | Commands fail mid-execution | Retry idempotent ops, timeout handling |

---

## 8. Conclusion

A Kubernetes sandbox implementation is **feasible and valuable** for cloud-native deployments. The existing `Sandbox` and `Manager` interfaces are well-designed for this — a K8s implementation slots in cleanly via the factory pattern.

**Recommended approach**: Start with Phase 1 (core K8s sandbox with emptyDir), build-tag gated, and iterate. The existing Docker sandbox remains the default for simplicity; K8s is opt-in via `"runtime": "k8s"` in config.

The main challenges are:
1. **Latency** — Pod startup is 5-10x slower than Docker; mitigate with pre-warming
2. **Dependencies** — client-go is heavy; mitigate with build tags
3. **Storage** — Workspace sharing needs ReadWriteMany; mitigate with emptyDir default + tar copy fallback
