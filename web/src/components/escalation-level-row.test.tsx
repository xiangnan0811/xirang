import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { EscalationLevelRow } from "./escalation-level-row";
import type { EscalationLevel, IntegrationChannel } from "@/types/domain";

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string, opts?: Record<string, unknown>) => {
      const map: Record<string, string> = {
        "escalation.levels.level": `第 ${(opts?.n as number) ?? "?"} 级`,
        "escalation.levels.delaySeconds": "延迟 (秒)",
        "escalation.levels.delayHint": "首级固定为 0",
        "escalation.levels.integrations": "通知通道",
        "escalation.levels.severityOverride": "严重度调整",
        "escalation.levels.tags": "打标签",
        "escalation.levels.tagsHint": "每级最多 10 个",
        "escalation.levels.removeLevel": "删除此级",
        "escalation.severityOverride.empty": "保持原严重度",
        "escalation.severity.info": "info",
        "escalation.severity.warning": "warning",
        "escalation.severity.critical": "critical",
      };
      return map[key] ?? key;
    },
  }),
}));

const makeLevel = (overrides?: Partial<EscalationLevel>): EscalationLevel => ({
  delay_seconds: 0,
  integration_ids: [],
  severity_override: "",
  tags: [],
  ...overrides,
});

const integrations: IntegrationChannel[] = [
  { id: "int-1", type: "email", name: "Email", endpoint: "", hasSecret: false, enabled: true, failThreshold: 3, cooldownMinutes: 5, proxyUrl: "" },
];

describe("EscalationLevelRow", () => {
  it("changing delay calls onChange with new delay_seconds", () => {
    const onChange = vi.fn();
    render(
      <EscalationLevelRow
        level={makeLevel({ delay_seconds: 60 })}
        index={1}
        isFirst={false}
        integrations={integrations}
        onChange={onChange}
      />
    );
    const input = screen.getByLabelText("延迟 (秒)");
    fireEvent.change(input, { target: { value: "120" } });
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({ delay_seconds: 120 })
    );
  });

  it("first level delay input is disabled when isFirst=true", () => {
    render(
      <EscalationLevelRow
        level={makeLevel({ delay_seconds: 0 })}
        index={0}
        isFirst={true}
        integrations={integrations}
        onChange={vi.fn()}
      />
    );
    const input = screen.getByLabelText("延迟 (秒)") as HTMLInputElement;
    expect(input.disabled).toBe(true);
  });

  it("remove button only shown when onRemove prop is defined", () => {
    const { rerender } = render(
      <EscalationLevelRow
        level={makeLevel()}
        index={0}
        isFirst={false}
        integrations={integrations}
        onChange={vi.fn()}
      />
    );
    expect(screen.queryByText("删除此级")).toBeNull();

    rerender(
      <EscalationLevelRow
        level={makeLevel()}
        index={0}
        isFirst={false}
        integrations={integrations}
        onChange={vi.fn()}
        onRemove={vi.fn()}
      />
    );
    expect(screen.getByText("删除此级")).toBeTruthy();
  });
});
