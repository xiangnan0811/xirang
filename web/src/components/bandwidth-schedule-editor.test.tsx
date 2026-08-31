import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { BandwidthScheduleEditor } from "./bandwidth-schedule-editor";

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
  }),
}));

describe("BandwidthScheduleEditor", () => {
  it("reads snake_case wire JSON and emits snake_case while editing camelCase domain fields", () => {
    const onChange = vi.fn();
    render(
      <BandwidthScheduleEditor
        value='[{"start":"22:00","end":"06:00","limit_mbps":80}]'
        onChange={onChange}
      />,
    );

    const limit = screen.getByLabelText("bandwidthEditor.limitMbps") as HTMLInputElement;
    expect(limit.value).toBe("80");

    fireEvent.change(limit, { target: { value: "40" } });
    expect(onChange).toHaveBeenCalledWith(
      JSON.stringify([{ start: "22:00", end: "06:00", limit_mbps: 40 }]),
    );
  });
});
