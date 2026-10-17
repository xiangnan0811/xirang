import type { TFunction } from "i18next";
import type { NodeDoctorCheckResult, NodeDoctorResult } from "@/types/domain";

export function summarizeChecks(checks: readonly NodeDoctorCheckResult[]) {
  return {
    total: checks.length,
    fail: checks.filter((check) => check.status === "fail").length,
    warn: checks.filter((check) => check.status === "warn").length,
    pass: checks.filter((check) => check.status === "pass").length,
    skip: checks.filter((check) => check.status === "skip").length,
  };
}

/**
 * 生成可复制的 Doctor 结果文本。只使用诊断响应中已展示的安全字段（生成时间、
 * 本地化检查名/状态、证据、建议），不接收 NodeRecord，也不序列化整个响应，
 * 因此不会带出节点名称、地址、路径、命令或原始 SSH 输出。
 */
export function formatNodeDoctorCopyText(result: NodeDoctorResult, t: TFunction): string {
  const header = [
    t("nodes.doctorCopyTitle"),
    t("nodes.doctorGeneratedAt", { time: result.generatedAt || "-" }),
    t("nodes.doctorSummary", summarizeChecks(result.checks)),
  ].join("\n");
  const checks = result.checks.map((check) => {
    const lines = [
      `${t(`nodes.doctorStatus_${check.status}`)} · ${t(`nodes.doctorCheck_${check.check}`, { defaultValue: check.check })}`,
      check.evidence || t("nodes.doctorNoEvidence"),
    ];
    if (check.suggestion) {
      lines.push(`${t("nodes.doctorSuggestionPrefix")}${check.suggestion}`);
    }
    return lines.join("\n");
  });
  return [header, ...checks].join("\n\n");
}
