import "@testing-library/jest-dom/vitest"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { apiClient } from "@/lib/api/client"
import type { Silence } from "@/types/domain"
import { SilencesPanel } from "./settings-page.silences"

/**
 * Local display and daylight-saving cases depend on the process zone.
 * Do not change TZ inside this process; start one process per zone:
 *   TZ=Asia/Singapore npx vitest run src/pages/settings-page.silences.test.tsx
 *   TZ=UTC npx vitest run src/pages/settings-page.silences.test.tsx
 *   TZ=America/New_York npx vitest run src/pages/settings-page.silences.test.tsx
 * Duration, rounding, validation, and single-capture cases run in every zone.
 */
const PROCESS_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone

const createdSilence: Silence = {
  id: 2,
  name: "created",
  matchNodeId: null,
  matchCategory: "",
  matchTags: [],
  startsAt: "",
  endsAt: "",
  createdBy: 1,
  note: "",
  createdAt: "",
  updatedAt: "",
}

const { toastErrorMock, toastSuccessMock } = vi.hoisted(() => ({
  toastErrorMock: vi.fn(),
  toastSuccessMock: vi.fn(),
}))

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: toastErrorMock,
    success: toastSuccessMock,
  },
}))

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ token: "test-token" }),
}))

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string, options?: { window?: string; offset?: string }) => {
      if (options?.offset) return `${key}|${options.offset}`
      if (options?.window) return `${key}|${options.window}`
      return key
    },
    i18n: { language: "zh", changeLanguage: vi.fn() },
  }),
  initReactI18next: { type: "3rdParty", init: vi.fn() },
}))


vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getNodes: vi.fn().mockResolvedValue([
      { id: 1, name: "node-1" },
      { id: 2, name: "node-2" },
    ]),
    listSilences: vi.fn().mockResolvedValue([
      {
        id: 1,
        name: "maint-A",
        matchNodeId: 1,
        matchCategory: "XR-NODE",
        matchTags: ["prod"],
        startsAt: "2026-04-19T00:00:00Z",
        endsAt: "2026-04-19T02:00:00Z",
        createdBy: 1,
        note: "",
        createdAt: "",
        updatedAt: "",
      },
    ]),
    createSilence: vi.fn(),
    deleteSilence: vi.fn(),
  },
}))

beforeEach(() => {
  toastErrorMock.mockReset()
  toastSuccessMock.mockReset()
  vi.mocked(apiClient.createSilence).mockReset()
  vi.mocked(apiClient.createSilence).mockResolvedValue(createdSilence)
})

describe("SilencesPanel", () => {

  it("renders the existing silence row", async () => {
    render(<SilencesPanel />)
    await waitFor(() => expect(screen.getByText("maint-A")).toBeInTheDocument())
  })

  it("opens create dialog", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    expect(screen.getByLabelText(/silences.name/)).toBeInTheDocument()
    expect(screen.getByRole("dialog")).toHaveAccessibleDescription("silences.dialogDesc")
    expect(screen.getByRole("button", { name: "silences.preset1d" })).toBeInTheDocument()
    expect(screen.getByText("silences.presetDurationHint")).toBeInTheDocument()
    expect(offsetText("silence-starts-offset")).toMatch(/^silences\.utcOffset\|[+-]\d{2}:\d{2}$/)
    expect(offsetText("silence-ends-offset")).toMatch(/^silences\.utcOffset\|[+-]\d{2}:\d{2}$/)
  })

  it("starts with a fresh draft when the dialog is reopened", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    await userEvent.type(screen.getByLabelText(/silences.name/), "discarded draft")
    await userEvent.keyboard("{Escape}")
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    expect(screen.getByLabelText(/silences.name/)).toHaveValue("")
  })

  it("rejects invalid time window", async () => {
    const createSilence = vi.mocked(apiClient.createSilence)

    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    await userEvent.type(screen.getByLabelText(/silences.name/), "test-silence")

    const startsInput = screen.getByLabelText(/silences.startsAt/)
    const endsInput = screen.getByLabelText(/silences.endsAt/)
    fireEvent.change(startsInput, { target: { value: "2026-04-20T10:00" } })
    fireEvent.change(endsInput, { target: { value: "2026-04-20T09:00" } })
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))

    expect(endsInput).toBeInvalid()
    expect(endsInput).toHaveFocus()
    expect(endsInput).toHaveAttribute("aria-describedby", expect.stringContaining("silence-ends-error"))
    expect(document.getElementById("silence-ends-error")).toHaveTextContent("silences.validationWindowInvalid")
    expect(startsInput).not.toBeInvalid()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(createSilence).not.toHaveBeenCalled()
  })

  it("shows node options in dropdown when dialog opens", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    // Wait for nodes to load (apiClient.getNodes is mocked)
    await waitFor(() => {
      expect(screen.getByRole("option", { name: "silences.nodeAll" })).toBeInTheDocument()
    })
    expect(screen.getByRole("option", { name: "node-1" })).toBeInTheDocument()
    expect(screen.getByRole("option", { name: "node-2" })).toBeInTheDocument()
  })

  it("adds and removes tags via chip picker", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))

    const tagInput = screen.getByPlaceholderText(/silences.tagsHint/)
    await userEvent.type(tagInput, "prod")
    await userEvent.keyboard("{Enter}")

    // chip "prod" should appear
    expect(screen.getByText("prod")).toBeInTheDocument()

    // Remove it via ✕ button
    await userEvent.click(screen.getByRole("button", { name: "移除标签 prod" }))
    expect(screen.queryByText("prod")).not.toBeInTheDocument()
  })

  it("category dropdown renders all alert types and stores value on selection", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))

    // The category <select> should contain "silences.categoryAll" option (empty value)
    const categorySelect = screen.getByRole("combobox", { name: /silences.category/ })
    expect(categorySelect).toBeInTheDocument()

    // All 7 known type options must be present
    const expectedKeys = [
      "silences.types.exec",
      "silences.types.vrfy",
      "silences.types.node",
      "silences.types.nodeExpiry",
      "silences.types.retn",
      "silences.types.intg",
      "silences.types.report",
    ]
    for (const key of expectedKeys) {
      expect(screen.getByRole("option", { name: key })).toBeInTheDocument()
    }

    // Selecting "XR-NODE" type option should reflect on the select element value
    await userEvent.selectOptions(categorySelect, "XR-NODE")
    expect((categorySelect as HTMLSelectElement).value).toBe("XR-NODE")
  })
})

type ZoneExpectation = {
  start: string
  end1h: string
  end4h: string
  end24h: string
  /** Signed offset passed to silences.utcOffset, without the UTC label. */
  offset: string
  summary1h: string
}

function offsetLabel(offset: string): string {
  return `silences.utcOffset|${offset}`
}

function windowSummary(start: string, startOffset: string, end: string, endOffset: string): string {
  return `${start} ${offsetLabel(startOffset)} → ${end} ${offsetLabel(endOffset)}`
}

const ZONE_EXPECTATIONS: Record<string, ZoneExpectation> = {
  "Asia/Singapore": {
    start: "2026-10-07T11:00",
    end1h: "2026-10-07T12:00",
    end4h: "2026-10-07T15:00",
    end24h: "2026-10-08T11:00",
    offset: "+08:00",
    summary1h: windowSummary("2026-10-07T11:00", "+08:00", "2026-10-07T12:00", "+08:00"),
  },
  UTC: {
    start: "2026-10-07T03:00",
    end1h: "2026-10-07T04:00",
    end4h: "2026-10-07T07:00",
    end24h: "2026-10-08T03:00",
    offset: "+00:00",
    summary1h: windowSummary("2026-10-07T03:00", "+00:00", "2026-10-07T04:00", "+00:00"),
  },
  "America/New_York": {
    start: "2026-10-06T23:00",
    end1h: "2026-10-07T00:00",
    end4h: "2026-10-07T03:00",
    end24h: "2026-10-07T23:00",
    offset: "-04:00",
    summary1h: windowSummary("2026-10-06T23:00", "-04:00", "2026-10-07T00:00", "-04:00"),
  },
}

function offsetText(id: string): string {
  return document.getElementById(id)?.textContent ?? ""
}

async function openDialog() {
  await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
  return {
    name: screen.getByLabelText(/silences.name/),
    start: screen.getByLabelText(/silences.startsAt/),
    end: screen.getByLabelText(/silences.endsAt/),
  }
}

function createSilenceMock() {
  return vi.mocked(apiClient.createSilence)
}

function setLocalValue(input: HTMLElement, value: string) {
  fireEvent.change(input, { target: { value } })
}

function installClock(startIso: string, stepMs: number) {
  const RealDate = globalThis.Date
  let current = RealDate.parse(startIso)
  const captures: number[] = []

  function SteppingDate(this: Date, ...args: unknown[]) {
    if (!new.target) return new RealDate(current).toString()
    if (args.length === 0) {
      const value = current
      current += stepMs
      captures.push(value)
      return Reflect.construct(RealDate, [value], new.target)
    }
    return Reflect.construct(RealDate, args, new.target)
  }

  SteppingDate.prototype = RealDate.prototype
  Object.assign(SteppingDate, {
    // Real now keeps testing-library timeouts moving. Window capture uses `new Date()`.
    now: RealDate.now.bind(RealDate),
    parse: RealDate.parse,
    UTC: RealDate.UTC,
  })
  vi.stubGlobal("Date", SteppingDate)

  return {
    captures,
    restore() {
      vi.unstubAllGlobals()
    },
  }
}

function flooredIso(ms: number, plusHours = 0): string {
  const floored = Math.floor(ms / 60_000) * 60_000 + plusHours * 3_600_000
  return new Date(floored).toISOString()
}

describe("silence window validation", () => {
  it("associates a blank name with the name field", async () => {
    render(<SilencesPanel />)
    const fields = await openDialog()
    const create = createSilenceMock()

    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))

    expect(fields.name).toBeInvalid()
    expect(fields.name).toHaveFocus()
    expect(fields.name).toHaveAttribute("aria-describedby", "silence-name-error")
    expect(document.getElementById("silence-name-error")).toHaveTextContent("silences.nameRequired")
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(create).not.toHaveBeenCalled()

    await userEvent.type(fields.name, "   ")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(document.getElementById("silence-name-error")).toHaveTextContent("silences.nameRequired")
    expect(create).not.toHaveBeenCalled()
  })

  it("rejects impossible local times without calling toISOString", async () => {
    render(<SilencesPanel />)
    const fields = await openDialog()
    const create = createSilenceMock()
    await userEvent.type(fields.name, "bad-date")

    const invalidValues = [
      "2026-02-29T12:00",
      "2026-04-31T00:00",
      "2026-11-31T12:00",
      "2026-00-10T00:00",
      "2026-13-01T00:00",
      "2026-01-01T24:00",
      "2026-01-01T12:60",
      "2026-1-01T12:00",
      "",
    ]
    for (const value of invalidValues) {
      setLocalValue(fields.end, value)
      const spy = vi.spyOn(Date.prototype, "toISOString")
      try {
        await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
        expect(spy).not.toHaveBeenCalled()
      } finally {
        spy.mockRestore()
      }
      expect(fields.end).toBeInvalid()
      expect(offsetText("silence-ends-offset")).toBe("")
      expect(document.getElementById("silence-ends-error")).toHaveTextContent("silences.validationDateInvalid")
    }

    setLocalValue(fields.start, "2026-04-31T10:00")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(fields.start).toBeInvalid()
    expect(fields.start).toHaveFocus()
    expect(document.getElementById("silence-starts-error")).toHaveTextContent("silences.validationDateInvalid")
    expect(create).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(toastSuccessMock).not.toHaveBeenCalled()
  })

  it("rejects an equal window and accepts a real leap-day minute", async () => {
    render(<SilencesPanel />)
    const fields = await openDialog()
    const create = createSilenceMock()
    await userEvent.type(fields.name, "equal-window")
    setLocalValue(fields.start, "2026-06-15T12:00")
    setLocalValue(fields.end, "2026-06-15T12:00")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(fields.end).toBeInvalid()
    expect(document.getElementById("silence-ends-error")).toHaveTextContent("silences.validationWindowInvalid")
    expect(create).not.toHaveBeenCalled()

    setLocalValue(fields.start, "2024-02-29T00:00")
    setLocalValue(fields.end, "2024-02-29T12:00")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
      name: "equal-window",
      startsAt: new Date(2024, 1, 29, 0, 0, 0, 0).toISOString(),
      endsAt: new Date(2024, 1, 29, 12, 0, 0, 0).toISOString(),
    }))
    expect(String(toastSuccessMock.mock.calls[0]?.[0])).toContain("silences.created|")
  })

  it("leaves the dialog open when create fails", async () => {
    const create = createSilenceMock()
    create.mockRejectedValueOnce(new Error("save failed"))
    render(<SilencesPanel />)
    const fields = await openDialog()
    await userEvent.type(fields.name, "kept")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(toastErrorMock).toHaveBeenCalledWith("save failed")
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(screen.getByRole("dialog")).toBeInTheDocument()
  })
})

describe("silence window capture", () => {
  let restore = () => {}

  beforeEach(() => {
    restore = installClock("2026-10-07T03:00:59.999Z", 0).restore
  })

  afterEach(() => {
    restore()
  })

  it("floors a sub-minute now and posts a one-hour UTC window", async () => {
    render(<SilencesPanel />)
    const fields = await openDialog()
    const known = ZONE_EXPECTATIONS[PROCESS_ZONE]
    if (known) {
      expect(fields.start).toHaveValue(known.start)
      expect(fields.end).toHaveValue(known.end1h)
      expect(offsetText("silence-starts-offset")).toBe(offsetLabel(known.offset))
      expect(offsetText("silence-ends-offset")).toBe(offsetLabel(known.offset))
    }
    await userEvent.type(fields.name, "floor-check")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    const create = createSilenceMock()
    expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
      name: "floor-check",
      startsAt: "2026-10-07T03:00:00.000Z",
      endsAt: "2026-10-07T04:00:00.000Z",
    }))
    if (known) {
      expect(toastSuccessMock).toHaveBeenCalledWith(`silences.created|${known.summary1h}`)
    }
  })

  it("posts exact 1, 4, and 24 hour durations from one floored instant", async () => {
    render(<SilencesPanel />)
    const create = createSilenceMock()
    const known = ZONE_EXPECTATIONS[PROCESS_ZONE]
    const cases = [
      { button: "silences.preset1h", hours: 1, end: "end1h" as const, endsAt: "2026-10-07T04:00:00.000Z" },
      { button: "silences.preset4h", hours: 4, end: "end4h" as const, endsAt: "2026-10-07T07:00:00.000Z" },
      { button: "silences.preset1d", hours: 24, end: "end24h" as const, endsAt: "2026-10-08T03:00:00.000Z" },
    ]
    for (const item of cases) {
      const fields = await openDialog()
      await userEvent.click(screen.getByRole("button", { name: item.button }))
      if (known) {
        expect(fields.start).toHaveValue(known.start)
        expect(fields.end).toHaveValue(known[item.end])
        expect(offsetText("silence-starts-offset")).toBe(offsetLabel(known.offset))
        expect(offsetText("silence-ends-offset")).toBe(offsetLabel(known.offset))
      }
      await userEvent.type(fields.name, `preset-${item.hours}`)
      await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
      expect(create).toHaveBeenLastCalledWith("test-token", expect.objectContaining({
        name: `preset-${item.hours}`,
        startsAt: "2026-10-07T03:00:00.000Z",
        endsAt: item.endsAt,
      }))
      await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    }
  })

  it("drops only the edited end and keeps the other preset instant", async () => {
    render(<SilencesPanel />)
    const fields = await openDialog()
    const endValue = (fields.end as HTMLInputElement).value
    const endOffset = offsetText("silence-ends-offset")
    setLocalValue(fields.start, "2026-10-06T12:00")
    expect(fields.end).toHaveValue(endValue)
    expect(offsetText("silence-ends-offset")).toBe(endOffset)
    await userEvent.type(fields.name, "keep-end")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    const create = createSilenceMock()
    expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
      name: "keep-end",
      startsAt: new Date(2026, 9, 6, 12, 0, 0, 0).toISOString(),
      endsAt: "2026-10-07T04:00:00.000Z",
    }))
  })
})

describe("silence window single capture", () => {
  it("reads now once when the dialog opens and once for each preset", async () => {
    const clock = installClock("2026-10-07T03:00:59.000Z", 90_000)
    try {
      render(<SilencesPanel />)
      expect(clock.captures).toEqual([])
      await openDialog()
      expect(clock.captures).toEqual([Date.parse("2026-10-07T03:00:59.000Z")])

      const create = createSilenceMock()
      const presets = [
        { name: "silences.preset1h", hours: 1 },
        { name: "silences.preset4h", hours: 4 },
        { name: "silences.preset1d", hours: 24 },
      ]
      for (const preset of presets) {
        const beforePreset = clock.captures.length
        await userEvent.click(screen.getByRole("button", { name: preset.name }))
        expect(clock.captures).toHaveLength(beforePreset + 1)
        const sampled = clock.captures[clock.captures.length - 1] ?? 0
        const beforeSubmit = clock.captures.length
        await userEvent.type(screen.getByLabelText(/silences.name/), `once-${preset.hours}`)
        await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
        expect(clock.captures).toHaveLength(beforeSubmit)
        expect(create).toHaveBeenLastCalledWith("test-token", expect.objectContaining({
          name: `once-${preset.hours}`,
          startsAt: flooredIso(sampled),
          endsAt: flooredIso(sampled, preset.hours),
        }))
        await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
        if (preset.hours !== 24) {
          const beforeReopen = clock.captures.length
          await openDialog()
          expect(clock.captures).toHaveLength(beforeReopen + 1)
        }
      }
    } finally {
      clock.restore()
    }
  })
})

function describeControlledClock(zone: string, expected: ZoneExpectation) {
  describe.skipIf(PROCESS_ZONE !== zone)(`${zone} controlled clock`, () => {
    let restore = () => {}

    beforeEach(() => {
      restore = installClock("2026-10-07T03:00:00.000Z", 0).restore
    })

    afterEach(() => {
      restore()
    })

    it("shows the local minute and posts 03:00Z through 04:00Z", async () => {
      expect(Intl.DateTimeFormat().resolvedOptions().timeZone).toBe(zone)
      render(<SilencesPanel />)
      const fields = await openDialog()
      expect(fields.start).toHaveValue(expected.start)
      expect(fields.end).toHaveValue(expected.end1h)
      expect(offsetText("silence-starts-offset")).toBe(offsetLabel(expected.offset))
      expect(offsetText("silence-ends-offset")).toBe(offsetLabel(expected.offset))
      await userEvent.type(fields.name, "zone-1h")
      await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
      const create = createSilenceMock()
      expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
        name: "zone-1h",
        startsAt: "2026-10-07T03:00:00.000Z",
        endsAt: "2026-10-07T04:00:00.000Z",
      }))
      expect(toastSuccessMock).toHaveBeenCalledWith(`silences.created|${expected.summary1h}`)
    })

    it("shows the 4 hour and 24 hour local windows", async () => {
      render(<SilencesPanel />)
      const fields = await openDialog()
      await userEvent.click(screen.getByRole("button", { name: "silences.preset4h" }))
      expect(fields.start).toHaveValue(expected.start)
      expect(fields.end).toHaveValue(expected.end4h)
      expect(offsetText("silence-ends-offset")).toBe(offsetLabel(expected.offset))
      await userEvent.click(screen.getByRole("button", { name: "silences.preset1d" }))
      expect(fields.start).toHaveValue(expected.start)
      expect(fields.end).toHaveValue(expected.end24h)
      expect(offsetText("silence-starts-offset")).toBe(offsetLabel(expected.offset))
      expect(offsetText("silence-ends-offset")).toBe(offsetLabel(expected.offset))
      await userEvent.type(fields.name, "zone-24h")
      await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
      const create = createSilenceMock()
      expect(create).toHaveBeenLastCalledWith("test-token", expect.objectContaining({
        name: "zone-24h",
        startsAt: "2026-10-07T03:00:00.000Z",
        endsAt: "2026-10-08T03:00:00.000Z",
      }))
    })
  })
}

describeControlledClock("Asia/Singapore", ZONE_EXPECTATIONS["Asia/Singapore"]!)
describeControlledClock("UTC", ZONE_EXPECTATIONS.UTC!)
describeControlledClock("America/New_York", ZONE_EXPECTATIONS["America/New_York"]!)

describe.skipIf(PROCESS_ZONE !== "America/New_York")("America/New_York daylight saving", () => {
  let restore = () => {}

  afterEach(() => {
    restore()
    restore = () => {}
  })

  function freeze(iso: string) {
    restore()
    restore = installClock(iso, 0).restore
  }

  it("rejects the spring-forward gap and keeps the real start offset", async () => {
    freeze("2026-03-08T06:00:00.000Z")
    render(<SilencesPanel />)
    const fields = await openDialog()
    await userEvent.type(fields.name, "spring-gap")
    setLocalValue(fields.start, "2026-03-08T01:30")
    setLocalValue(fields.end, "2026-03-08T02:30")
    expect(offsetText("silence-starts-offset")).toBe(offsetLabel("-05:00"))
    expect(offsetText("silence-ends-offset")).toBe("")

    const spy = vi.spyOn(Date.prototype, "toISOString")
    try {
      await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
      expect(spy).not.toHaveBeenCalled()
    } finally {
      spy.mockRestore()
    }

    expect(fields.end).toBeInvalid()
    expect(fields.end).toHaveFocus()
    expect(fields.end).toHaveAttribute("aria-describedby", "silence-ends-error")
    expect(document.getElementById("silence-ends-error")).toHaveTextContent("silences.validationDateInvalid")
    expect(fields.start).not.toBeInvalid()
    const create = createSilenceMock()
    expect(create).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
  })

  it("adds 1 hour and 4 hours in milliseconds across the spring gap", async () => {
    freeze("2026-03-08T06:30:00.000Z")
    render(<SilencesPanel />)
    const create = createSilenceMock()
    let fields = await openDialog()
    expect(fields.start).toHaveValue("2026-03-08T01:30")
    expect(fields.end).toHaveValue("2026-03-08T03:30")
    expect(offsetText("silence-starts-offset")).toBe(offsetLabel("-05:00"))
    expect(offsetText("silence-ends-offset")).toBe(offsetLabel("-04:00"))
    await userEvent.type(fields.name, "spring-1h")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(create).toHaveBeenLastCalledWith("test-token", expect.objectContaining({
      startsAt: "2026-03-08T06:30:00.000Z",
      endsAt: "2026-03-08T07:30:00.000Z",
    }))
    expect(toastSuccessMock).toHaveBeenCalledWith(
      `silences.created|${windowSummary("2026-03-08T01:30", "-05:00", "2026-03-08T03:30", "-04:00")}`,
    )
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())

    fields = await openDialog()
    await userEvent.click(screen.getByRole("button", { name: "silences.preset4h" }))
    expect(fields.start).toHaveValue("2026-03-08T01:30")
    expect(fields.end).toHaveValue("2026-03-08T06:30")
    expect(offsetText("silence-starts-offset")).toBe(offsetLabel("-05:00"))
    expect(offsetText("silence-ends-offset")).toBe(offsetLabel("-04:00"))
    await userEvent.type(fields.name, "spring-4h")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(create).toHaveBeenLastCalledWith("test-token", expect.objectContaining({
      startsAt: "2026-03-08T06:30:00.000Z",
      endsAt: "2026-03-08T10:30:00.000Z",
    }))
  })

  it("keeps the second fall-back hour on a preset and shows both offsets", async () => {
    freeze("2026-11-01T05:30:00.000Z")
    render(<SilencesPanel />)
    const fields = await openDialog()
    expect(fields.start).toHaveValue("2026-11-01T01:30")
    expect(fields.end).toHaveValue("2026-11-01T01:30")
    expect(offsetText("silence-starts-offset")).toBe(offsetLabel("-04:00"))
    expect(offsetText("silence-ends-offset")).toBe(offsetLabel("-05:00"))
    await userEvent.type(fields.name, "fold")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    const create = createSilenceMock()
    expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
      startsAt: "2026-11-01T05:30:00.000Z",
      endsAt: "2026-11-01T06:30:00.000Z",
    }))
    expect(toastSuccessMock).toHaveBeenCalledWith(
      `silences.created|${windowSummary("2026-11-01T01:30", "-04:00", "2026-11-01T01:30", "-05:00")}`,
    )
  })

  it("editing the start keeps the preset end in the second fall-back hour", async () => {
    freeze("2026-11-01T05:30:00.000Z")
    render(<SilencesPanel />)
    const fields = await openDialog()
    setLocalValue(fields.start, "2026-11-01T00:30")
    expect(fields.end).toHaveValue("2026-11-01T01:30")
    expect(offsetText("silence-starts-offset")).toBe(offsetLabel("-04:00"))
    expect(offsetText("silence-ends-offset")).toBe(offsetLabel("-05:00"))
    await userEvent.type(fields.name, "keep-fold")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    const create = createSilenceMock()
    expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
      startsAt: "2026-11-01T04:30:00.000Z",
      endsAt: "2026-11-01T06:30:00.000Z",
    }))
  })

  it("reparses an edited ambiguous minute with the JS local instant", async () => {
    freeze("2026-11-01T05:30:00.000Z")
    render(<SilencesPanel />)
    const fields = await openDialog()
    setLocalValue(fields.start, "2026-11-01T00:30")
    setLocalValue(fields.end, "2026-11-01T01:31")
    expect(offsetText("silence-ends-offset")).toBe(offsetLabel("-04:00"))
    setLocalValue(fields.end, "2026-11-01T01:30")
    expect(fields.end).toHaveValue("2026-11-01T01:30")
    expect(offsetText("silence-ends-offset")).toBe(offsetLabel("-04:00"))
    await userEvent.type(fields.name, "hand-fold")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    const create = createSilenceMock()
    expect(create).toHaveBeenCalledWith("test-token", expect.objectContaining({
      startsAt: "2026-11-01T04:30:00.000Z",
      endsAt: "2026-11-01T05:30:00.000Z",
    }))
    expect(toastSuccessMock).toHaveBeenCalledWith(
      `silences.created|${windowSummary("2026-11-01T00:30", "-04:00", "2026-11-01T01:30", "-04:00")}`,
    )
  })
})
