import {
  buildFingerprint,
  createIntegrationId,
  createKeyId,
  describeCron,
  parseTags
} from "@/hooks/use-console-data.utils";
import type {
  IntegrationChannel,
  NewIntegrationInput,
  NewNodeInput,
  NewPolicyInput,
  NewSSHKeyInput,
  NewTaskInput,
  NodeRecord,
  PolicyRecord,
  SSHKeyRecord,
  TaskRecord
} from "@/types/domain";

export function buildDemoSSHKey(input: NewSSHKeyInput): SSHKeyRecord {
  return {
    id: createKeyId(input.name || input.username || "ssh-key"),
    name: input.name,
    username: input.username,
    keyType: input.keyType,
    privateKey: input.privateKey,
    fingerprint: buildFingerprint(input.privateKey),
    createdAt: new Date().toLocaleString("zh-CN", { hour12: false })
  };
}

export function buildDemoNode(input: NewNodeInput, nodes: NodeRecord[], keyId: string | null): NodeRecord {
  const maxNodeID = nodes.length > 0 ? Math.max(...nodes.map((node) => node.id)) : 0;
  return {
    id: maxNodeID + 1,
    name: input.name,
    host: input.host,
    address: input.host,
    ip: input.host,
    port: input.port || 22,
    username: input.username,
    authType: input.authType,
    keyId,
    basePath: input.basePath || "/",
    status: "warning",
    tags: parseTags(input.tags),
    lastSeenAt: "尚未探测",
    lastBackupAt: "尚未执行",
    diskFreePercent: 100,
    diskUsedGb: 0,
    diskTotalGb: 800
  };
}

export function buildDemoTask(
  input: NewTaskInput,
  nodes: NodeRecord[],
  policies: PolicyRecord[],
  tasks: TaskRecord[]
): TaskRecord {
  const node = nodes.find((item) => item.id === input.nodeId);
  const policy = input.policyId ? policies.find((item) => item.id === input.policyId) : undefined;
  const nextTaskID = tasks.length > 0 ? Math.max(...tasks.map((task) => task.id)) + 1 : 3001;
  return {
    id: nextTaskID,
    name: input.name,
    policyName: policy?.name ?? input.name,
    policyId: policy?.id ?? input.policyId ?? null,
    nodeName: node?.name ?? `节点-${input.nodeId}`,
    nodeId: input.nodeId,
    status: "pending",
    progress: 0,
    startedAt: "-",
    rsyncSource: input.rsyncSource ?? policy?.sourcePath,
    rsyncTarget: input.rsyncTarget ?? policy?.targetPath,
    executorType: input.executorType ?? "rsync",
    cronSpec: input.cronSpec ?? policy?.cron,
    speedMbps: 0
  };
}

export function buildDemoPolicy(input: NewPolicyInput, policies: PolicyRecord[]): PolicyRecord {
  const nextID = policies.length > 0 ? Math.max(...policies.map((p) => p.id)) + 1 : 1;
  return {
    id: nextID,
    name: input.name,
    sourcePath: input.sourcePath,
    targetPath: input.targetPath,
    cron: input.cron,
    naturalLanguage: describeCron(input.cron),
    enabled: input.enabled,
    criticalThreshold: Math.max(1, input.criticalThreshold),
    nodeIds: input.nodeIds ?? [],
    verifyEnabled: input.verifyEnabled ?? false,
    verifySampleRate: input.verifySampleRate ?? 0,
  };
}

export function buildDemoIntegration(input: NewIntegrationInput): IntegrationChannel {
  return {
    id: createIntegrationId(input.name || input.type),
    type: input.type,
    name: input.name,
    endpoint: input.endpoint,
    hasSecret: Boolean(input.secret),
    enabled: input.enabled,
    failThreshold: Math.max(1, input.failThreshold),
    cooldownMinutes: Math.max(1, input.cooldownMinutes)
  };
}

export function buildDemoBackupTask(
  node: NodeRecord,
  tasks: TaskRecord[],
  policies: PolicyRecord[]
): TaskRecord {
  const nextTaskID = tasks.length > 0 ? Math.max(...tasks.map((task) => task.id)) + 1 : 5000;
  const targetPolicy = policies.find((policy) => policy.enabled) ?? policies[0];
  return {
    id: nextTaskID,
    policyName: targetPolicy?.name ?? "手动备份",
    nodeName: node.name,
    nodeId: node.id,
    status: "running",
    progress: 6,
    startedAt: new Date().toLocaleString("zh-CN", { hour12: false }),
    speedMbps: 96
  };
}

export function simulateDemoConnection(nodeID: number): {
  result: { ok: boolean; message: string };
  nodeUpdate: (node: NodeRecord) => NodeRecord;
} {
  const now = new Date();
  const seed = (now.getSeconds() + nodeID * 17) % 10;
  const ok = seed >= 2;
  const probeTime = now.toLocaleString("zh-CN", { hour12: false });

  if (!ok) {
    return {
      result: { ok: false, message: "连接失败：SSH 握手超时或认证失败。" },
      nodeUpdate: (node) => ({
        ...node,
        status: "offline",
        lastSeenAt: probeTime,
        diskProbeAt: probeTime,
        connectionLatencyMs: undefined
      })
    };
  }

  return {
    result: { ok: true, message: "SSH 握手成功，已更新磁盘探测信息。" },
    nodeUpdate: (node) => {
      const total = node.diskTotalGb || 800;
      const used = Math.max(10, Math.min(total - 5, node.diskUsedGb + ((seed % 3) - 1) * 8));
      return {
        ...node,
        status: used / total >= 0.9 ? "warning" : "online",
        lastSeenAt: probeTime,
        diskProbeAt: probeTime,
        connectionLatencyMs: 18 + (seed * 11) % 120,
        diskUsedGb: used,
        diskFreePercent: Math.max(1, Math.round(((total - used) / total) * 100))
      };
    }
  };
}
