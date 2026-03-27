import { useTranslation } from "react-i18next";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { SandboxConfig } from "@/types/agent";
import { ConfigSection, InfoLabel, numOrUndef } from "./config-section";

interface SandboxSectionProps {
  enabled: boolean;
  value: SandboxConfig;
  onToggle: (v: boolean) => void;
  onChange: (v: SandboxConfig) => void;
}

export function SandboxSection({ enabled, value, onToggle, onChange }: SandboxSectionProps) {
  const { t } = useTranslation("agents");
  const s = "configSections.sandbox";
  const isK8s = (value.backend ?? "docker") === "k8s";
  return (
    <ConfigSection
      title={t(`${s}.title`)}
      description={t(`${s}.description`)}
      enabled={enabled}
      onToggle={onToggle}
    >
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <InfoLabel tip="'off' disables sandboxing, 'non-main' sandboxes only sub-agents, 'all' sandboxes every execution including the main agent.">{t(`${s}.mode`)}</InfoLabel>
          <Select
            value={value.mode ?? ""}
            onValueChange={(v) => onChange({ ...value, mode: v as SandboxConfig["mode"] })}
          >
            <SelectTrigger><SelectValue placeholder="off" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="off">off</SelectItem>
              <SelectItem value="non-main">non-main</SelectItem>
              <SelectItem value="all">all</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <InfoLabel tip={t(`${s}.backendTip`)}>{t(`${s}.backend`)}</InfoLabel>
          <Select
            value={value.backend ?? "docker"}
            onValueChange={(v) => onChange({ ...value, backend: v as SandboxConfig["backend"] })}
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="docker">Docker</SelectItem>
              <SelectItem value="k8s">Kubernetes (K8s)</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <InfoLabel tip="How the sandbox accesses the host workspace. 'none' = isolated, 'ro' = read-only mount, 'rw' = full read-write access.">{t(`${s}.workspaceAccess`)}</InfoLabel>
          <Select
            value={value.workspace_access ?? ""}
            onValueChange={(v) =>
              onChange({ ...value, workspace_access: v as SandboxConfig["workspace_access"] })
            }
          >
            <SelectTrigger><SelectValue placeholder="rw" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="none">none</SelectItem>
              <SelectItem value="ro">ro (read-only)</SelectItem>
              <SelectItem value="rw">rw (read-write)</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <InfoLabel tip={t(`${s}.idleTimeoutMinTip`)}>{t(`${s}.idleTimeoutMin`)}</InfoLabel>
          <Input
            type="number"
            className="text-base md:text-sm"
            placeholder="20"
            value={value.idle_timeout_min ?? ""}
            onChange={(e) => onChange({ ...value, idle_timeout_min: numOrUndef(e.target.value) })}
          />
        </div>
      </div>
      <div className="space-y-2">
        <InfoLabel tip="Docker image used for the sandbox container. Must be pre-built and available locally.">{t(`${s}.image`)}</InfoLabel>
        <Input
          className="text-base md:text-sm"
          placeholder="goclaw-sandbox:bookworm-slim"
          value={value.image ?? ""}
          onChange={(e) => onChange({ ...value, image: e.target.value || undefined })}
        />
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <InfoLabel tip="Container lifecycle scope. 'session' = one container per chat session, 'agent' = shared across sessions, 'shared' = shared across all agents.">{t(`${s}.scope`)}</InfoLabel>
          <Select
            value={value.scope ?? ""}
            onValueChange={(v) => onChange({ ...value, scope: v as SandboxConfig["scope"] })}
          >
            <SelectTrigger><SelectValue placeholder="session" /></SelectTrigger>
            <SelectContent>
              <SelectItem value="session">session</SelectItem>
              <SelectItem value="agent">agent</SelectItem>
              <SelectItem value="shared">shared</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <InfoLabel tip="Maximum execution time in seconds for each command run inside the sandbox.">{t(`${s}.timeout`)}</InfoLabel>
          <Input
            type="number"
            className="text-base md:text-sm"
            placeholder="300"
            value={value.timeout_sec ?? ""}
            onChange={(e) => onChange({ ...value, timeout_sec: numOrUndef(e.target.value) })}
          />
        </div>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <div className="space-y-2">
          <InfoLabel tip="Maximum memory allocation for the sandbox container in megabytes.">{t(`${s}.memoryMb`)}</InfoLabel>
          <Input
            type="number"
            className="text-base md:text-sm"
            placeholder="512"
            value={value.memory_mb ?? ""}
            onChange={(e) => onChange({ ...value, memory_mb: numOrUndef(e.target.value) })}
          />
        </div>
        <div className="space-y-2">
          <InfoLabel tip="CPU allocation for the sandbox container. Fractional values allowed (e.g. 0.5 = half a core).">{t(`${s}.cpus`)}</InfoLabel>
          <Input
            type="number"
            step="0.5"
            className="text-base md:text-sm"
            placeholder="1.0"
            value={value.cpus ?? ""}
            onChange={(e) => onChange({ ...value, cpus: numOrUndef(e.target.value) })}
          />
        </div>
      </div>
      <div className="flex items-center gap-2">
        <Switch
          checked={value.network_enabled ?? false}
          onCheckedChange={(v) => onChange({ ...value, network_enabled: v })}
        />
        <InfoLabel tip="Allow the sandbox container to access the network. Disable for fully isolated execution.">{t(`${s}.networkEnabled`)}</InfoLabel>
      </div>
      {isK8s && (
        <div className="rounded-md border px-3 py-3 space-y-3 mt-1">
          <p className="text-xs font-medium text-muted-foreground uppercase tracking-wide">{t(`${s}.k8s.title`)}</p>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <InfoLabel tip={t(`${s}.k8s.namespaceTip`)}>{t(`${s}.k8s.namespace`)}</InfoLabel>
              <Input
                className="text-base md:text-sm"
                placeholder="default"
                value={value.k8s?.namespace ?? ""}
                onChange={(e) => onChange({ ...value, k8s: { ...value.k8s, namespace: e.target.value || undefined } })}
              />
            </div>
            <div className="space-y-2">
              <InfoLabel tip={t(`${s}.k8s.serviceAccountTip`)}>{t(`${s}.k8s.serviceAccount`)}</InfoLabel>
              <Input
                className="text-base md:text-sm"
                placeholder="default"
                value={value.k8s?.service_account ?? ""}
                onChange={(e) => onChange({ ...value, k8s: { ...value.k8s, service_account: e.target.value || undefined } })}
              />
            </div>
            <div className="space-y-2">
              <InfoLabel tip={t(`${s}.k8s.pvcTemplateTip`)}>{t(`${s}.k8s.pvcTemplate`)}</InfoLabel>
              <Input
                className="text-base md:text-sm"
                placeholder="sandbox-{tenant_id}"
                value={value.k8s?.pvc_template ?? ""}
                onChange={(e) => onChange({ ...value, k8s: { ...value.k8s, pvc_template: e.target.value || undefined } })}
              />
            </div>
            <div className="space-y-2">
              <InfoLabel tip={t(`${s}.k8s.maxPodLifetimeSecTip`)}>{t(`${s}.k8s.maxPodLifetimeSec`)}</InfoLabel>
              <Input
                type="number"
                className="text-base md:text-sm"
                placeholder="3600"
                value={value.k8s?.max_pod_lifetime_sec ?? ""}
                onChange={(e) => onChange({ ...value, k8s: { ...value.k8s, max_pod_lifetime_sec: numOrUndef(e.target.value) } })}
              />
            </div>
          </div>
        </div>
      )}
    </ConfigSection>
  );
}
