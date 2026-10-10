import "@testing-library/jest-dom/vitest"
import { act, fireEvent, render as rtlRender, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { StrictMode, useState, type ReactElement } from "react"
import { MemoryRouter } from "react-router-dom"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { CreateSilenceDialog } from "@/components/create-silence-dialog"
import { useAuth } from "@/context/auth-context.hooks"
import type { QuickSilenceMatch } from "@/lib/alert-silence-match"
import { apiClient } from "@/lib/api/client"
import {
  ApiError,
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  isAuthTransitionActive,
  releaseAuthTransitionBarrier,
  rememberAuthIdentity,
} from "@/lib/api/core"
import type { NodeRecord, Silence } from "@/types/domain"
import { SilencesPanel } from "./settings-page.silences"

function render(ui: ReactElement) {
  return rtlRender(ui, { wrapper: MemoryRouter })
}

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

const { toastErrorMock, toastSuccessMock, authRef } = vi.hoisted(() => ({
  toastErrorMock: vi.fn(),
  toastSuccessMock: vi.fn(),
  authRef: {
    current: {
      token: "test-token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
    },
  },
}))

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: {
    error: toastErrorMock,
    success: toastSuccessMock,
  },
}))

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    token: authRef.current.token,
    role: authRef.current.role,
  }),
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
    getNodes: vi.fn(),
    getNode: vi.fn(),
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

function matchedNode(id: number, name: string): NodeRecord {
  return {
    id,
    name,
    host: "10.0.0.8",
    address: "10.0.0.8",
    ip: "10.0.0.8",
    port: 22,
    username: "backup",
    authType: "key",
    status: "online",
    tags: [],
    lastSeenAt: "",
    lastBackupAt: "",
  }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function setAuth(next: { token: string | null; role: "admin" | "operator" | "viewer" | null }) {
  authRef.current = next
  rememberAuthIdentity(next.token, next.role)
}

beforeEach(() => {
  if (isAuthTransitionActive()) clearAuthTransitionBarrier()
  setAuth({ token: "test-token", role: "admin" })
  toastErrorMock.mockReset()
  toastSuccessMock.mockReset()
  vi.mocked(apiClient.createSilence).mockReset()
  vi.mocked(apiClient.createSilence).mockResolvedValue(createdSilence)
  vi.mocked(apiClient.getNodes).mockReset()
  vi.mocked(apiClient.getNodes).mockResolvedValue([
    matchedNode(1, "node-1"),
    matchedNode(2, "node-2"),
  ])
  vi.mocked(apiClient.getNode).mockReset()
  // Resolves even when the caller aborts. Generation has to drop the stale result.
  vi.mocked(apiClient.getNode).mockImplementation(async (_token, nodeId) => matchedNode(nodeId, `node-${nodeId}`))
  vi.mocked(apiClient.listSilences).mockReset()
  vi.mocked(apiClient.listSilences).mockResolvedValue([
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
  ])
  vi.mocked(apiClient.deleteSilence).mockReset()
  vi.mocked(apiClient.deleteSilence).mockResolvedValue(undefined)
})

afterEach(() => {
  if (isAuthTransitionActive()) clearAuthTransitionBarrier()
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

const QUICK_EXEC: QuickSilenceMatch = { nodeId: 42, category: "XR-EXEC" }

function SilenceDialogHarness({ match }: { match?: QuickSilenceMatch }) {
  const { token, role } = useAuth()
  const [open, setOpen] = useState(false)
  const [created, setCreated] = useState(0)
  const sessionToken = token ?? ""
  return (
    <div>
      <button type="button" onClick={() => setOpen(true)}>open-silence</button>
      <span data-testid="created-count">{created}</span>
      {open && role === "admin" && sessionToken ? (
        <CreateSilenceDialog
          key={match ? `${match.nodeId}:${match.category}` : "settings"}
          open={open}
          onOpenChange={setOpen}
          onCreated={() => setCreated((count) => count + 1)}
          token={sessionToken}
          initialMatch={match}
        />
      ) : null}
    </div>
  )
}

function renderQuick(match: QuickSilenceMatch = QUICK_EXEC, onOpenChange = vi.fn(), onCreated = vi.fn()) {
  return render(
    <CreateSilenceDialog
      open
      onOpenChange={onOpenChange}
      onCreated={onCreated}
      token="test-token"
      initialMatch={match}
    />,
  )
}

describe("quick silence range", () => {
  it("does not mount a create dialog without an admin role", async () => {
    setAuth({ token: "test-token", role: "operator" })
    const onOpenChange = vi.fn()
    render(
      <CreateSilenceDialog
        open
        onOpenChange={onOpenChange}
        onCreated={vi.fn()}
        token="test-token"
        initialMatch={QUICK_EXEC}
      />,
    )
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(apiClient.getNode).not.toHaveBeenCalled()
    expect(apiClient.createSilence).not.toHaveBeenCalled()
  })

  it("does not expose the settings create dialog for a viewer", () => {
    setAuth({ token: "test-token", role: "viewer" })
    render(<SilencesPanel />)
    expect(screen.queryByRole("button", { name: /silences.new/ })).not.toBeInTheDocument()
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
    expect(apiClient.getNode).not.toHaveBeenCalled()
    expect(apiClient.getNodes).not.toHaveBeenCalled()
    expect(apiClient.createSilence).not.toHaveBeenCalled()
  })

  it("posts only the checked node and category, with no tag wildcard", async () => {
    const onCreated = vi.fn()
    renderQuick(QUICK_EXEC, vi.fn(), onCreated)
    expect(await screen.findByText("node-42")).toBeInTheDocument()
    expect(screen.getByText("#42")).toBeInTheDocument()
    expect(screen.getByText("XR-EXEC")).toBeInTheDocument()
    expect(screen.getByText("silences.quick.effectExplanation")).toBeInTheDocument()
    expect(screen.getByText("silences.quick.windowLabel")).toBeInTheDocument()
    expect(screen.getByRole("dialog")).toHaveAccessibleDescription("silences.quick.rangeExplanation")
    expect(screen.getByRole("link", { name: "silences.quick.rulesLink" })).toHaveAttribute(
      "href",
      "/app/settings?tab=silences",
    )
    expect(screen.queryByPlaceholderText("silences.tagsHint")).not.toBeInTheDocument()
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    expect(apiClient.getNodes).not.toHaveBeenCalled()
    expect(apiClient.getNode).toHaveBeenCalledWith("test-token", 42, expect.objectContaining({
      signal: expect.any(AbortSignal),
    }))

    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(apiClient.createSilence).not.toHaveBeenCalled()

    await userEvent.type(screen.getByLabelText(/silences.name/), "quiet-exec")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(apiClient.createSilence).toHaveBeenCalledTimes(1)
    const payload = vi.mocked(apiClient.createSilence).mock.calls[0]?.[1]
    expect(payload).toEqual(expect.objectContaining({
      name: "quiet-exec",
      matchNodeId: 42,
      matchCategory: "XR-EXEC",
      matchTags: [],
    }))
    expect(payload?.matchNodeId).not.toBeNull()
    expect(Date.parse(String(payload?.endsAt)) - Date.parse(String(payload?.startsAt))).toBe(3_600_000)
    expect(onCreated).toHaveBeenCalledTimes(1)
  })

  it("drops the previous draft when another quick range is opened", async () => {
    const view = renderQuick()
    expect(await screen.findByText("node-42")).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText(/silences.name/), "old-draft")
    view.rerender(
      <CreateSilenceDialog
        open
        onOpenChange={vi.fn()}
        onCreated={vi.fn()}
        token="test-token"
        initialMatch={{ nodeId: 7, category: "XR-VRFY" }}
      />,
    )
    expect(screen.queryByDisplayValue("old-draft")).not.toBeInTheDocument()
    expect(screen.queryByText("node-42")).not.toBeInTheDocument()
    expect(await screen.findByLabelText(/silences.name/)).toHaveValue("")
    expect(await screen.findByText("node-7")).toBeInTheDocument()
    expect(screen.getByText("#7")).toBeInTheDocument()
    expect(screen.getByText("XR-VRFY")).toBeInTheDocument()
    expect(apiClient.createSilence).not.toHaveBeenCalled()
  })

  it("keeps the requested node when the check returns a different id", async () => {
    vi.mocked(apiClient.getNode)
      .mockResolvedValueOnce(matchedNode(99, "other-node"))
      .mockResolvedValueOnce(matchedNode(42, "node-42"))
    renderQuick()
    expect(await screen.findByRole("alert")).toHaveTextContent("silences.quick.nodeMismatch")
    expect(screen.getByText("#42")).toBeInTheDocument()
    expect(screen.queryByText("other-node")).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/silences.name/)).not.toBeInTheDocument()
    fireEvent.submit(screen.getByRole("dialog").querySelector("form")!)
    expect(apiClient.createSilence).not.toHaveBeenCalled()

    await userEvent.click(screen.getByRole("button", { name: "silences.quick.nodeRetry" }))
    expect(await screen.findByText("node-42")).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText(/silences.name/), "after-retry")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(apiClient.createSilence).toHaveBeenCalledWith("test-token", expect.objectContaining({
      name: "after-retry",
      matchNodeId: 42,
      matchCategory: "XR-EXEC",
      matchTags: [],
    }))
  })

  it("shows a retry after a failed node check and still refuses a null node", async () => {
    vi.mocked(apiClient.getNode).mockRejectedValueOnce(new Error("network down"))
    renderQuick()
    expect(await screen.findByRole("alert")).toHaveTextContent("silences.quick.nodeFailed")
    expect(screen.getByText("#42")).toBeInTheDocument()
    expect(screen.queryByLabelText(/silences.name/)).not.toBeInTheDocument()
    fireEvent.submit(screen.getByRole("dialog").querySelector("form")!)
    expect(apiClient.createSilence).not.toHaveBeenCalled()

    await userEvent.click(screen.getByRole("button", { name: "silences.quick.nodeRetry" }))
    expect(await screen.findByText("node-42")).toBeInTheDocument()
    expect(apiClient.getNode).toHaveBeenCalledTimes(2)
  })

  it("explains a missing node without creating a rule", async () => {
    vi.mocked(apiClient.getNode).mockRejectedValueOnce(new ApiError(404, "missing"))
    renderQuick()
    expect(await screen.findByRole("alert")).toHaveTextContent("silences.quick.nodeMissing")
    expect(screen.getByText("#42")).toBeInTheDocument()
    expect(apiClient.createSilence).not.toHaveBeenCalled()
    expect(apiClient.getNodes).not.toHaveBeenCalled()
  })

  it("rejects a category outside the quick allowlist before any node read", async () => {
    renderQuick({ nodeId: 42, category: "XR-NODE" })
    expect(await screen.findByRole("alert")).toHaveTextContent("silences.quick.rangeInvalid")
    expect(screen.getByText("#42")).toBeInTheDocument()
    expect(screen.getByText("XR-NODE")).toBeInTheDocument()
    expect(screen.queryByRole("button", { name: "silences.quick.nodeRetry" })).not.toBeInTheDocument()
    expect(apiClient.getNode).not.toHaveBeenCalled()
    expect(apiClient.createSilence).not.toHaveBeenCalled()
  })

  it("keeps the quick draft when create fails and retries the same range", async () => {
    const create = vi.mocked(apiClient.createSilence)
    create.mockRejectedValueOnce(new Error("save failed"))
    renderQuick()
    expect(await screen.findByText("node-42")).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText(/silences.name/), "kept")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(toastErrorMock).toHaveBeenCalledWith("save failed")
    expect(screen.getByRole("dialog")).toBeInTheDocument()
    expect(screen.getByLabelText(/silences.name/)).toHaveValue("kept")
    expect(screen.getByText("#42")).toBeInTheDocument()

    create.mockResolvedValueOnce(createdSilence)
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    expect(create).toHaveBeenLastCalledWith("test-token", expect.objectContaining({
      name: "kept",
      matchNodeId: 42,
      matchCategory: "XR-EXEC",
      matchTags: [],
    }))
  })
})

describe("silence create lifetime", () => {
  it("sends one create when the submit control is activated twice", async () => {
    const pending = deferred<Silence>()
    vi.mocked(apiClient.createSilence).mockReturnValue(pending.promise)
    render(<CreateSilenceDialog open onOpenChange={vi.fn()} onCreated={vi.fn()} token="test-token" />)
    await userEvent.type(screen.getByLabelText(/silences.name/), "once")
    const submit = screen.getByRole("button", { name: "silences.create" })
    fireEvent.click(submit)
    fireEvent.click(submit)
    expect(apiClient.createSilence).toHaveBeenCalledTimes(1)
    await act(async () => {
      pending.resolve(createdSilence)
    })
    expect(toastSuccessMock).toHaveBeenCalledTimes(1)
  })

  it("describes the captured window when the form changes after submit", async () => {
    const pending = deferred<Silence>()
    vi.mocked(apiClient.createSilence).mockReturnValue(pending.promise)
    render(<CreateSilenceDialog open onOpenChange={vi.fn()} onCreated={vi.fn()} token="test-token" />)
    await userEvent.type(screen.getByLabelText(/silences.name/), "captured")
    const end = screen.getByLabelText(/silences.endsAt/)
    const endValue = (end as HTMLInputElement).value
    fireEvent.click(screen.getByRole("button", { name: "silences.create" }))
    fireEvent.change(end, { target: { value: "2030-01-02T05:00" } })
    await act(async () => {
      pending.resolve(createdSilence)
    })
    const summary = String(toastSuccessMock.mock.calls[0]?.[0])
    expect(summary).toContain(endValue)
    expect(summary).not.toContain("2030-01-02T05:00")
    const payload = vi.mocked(apiClient.createSilence).mock.calls[0]?.[1]
    expect(String(payload?.endsAt)).not.toContain("2030-01-02T05:00")
  })

  it("does not toast, close, or refresh when create resolves after the dialog closes", async () => {
    const pending = deferred<Silence>()
    vi.mocked(apiClient.createSilence).mockReturnValue(pending.promise)
    render(<SilencesPanel />)
    await screen.findByText("maint-A")
    const lists = vi.mocked(apiClient.listSilences).mock.calls.length
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    await userEvent.type(screen.getByLabelText(/silences.name/), "pending-name")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    await userEvent.keyboard("{Escape}")
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    await act(async () => {
      pending.resolve(createdSilence)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.listSilences).mock.calls.length).toBe(lists)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    expect(screen.getByLabelText(/silences.name/)).toHaveValue("")
  })

  it.each(["success", "error"] as const)("drops late create %s and finally after the auth generation changes", async (outcome) => {
    const pending = deferred<Silence>()
    const onOpenChange = vi.fn()
    const onCreated = vi.fn()
    vi.mocked(apiClient.createSilence).mockReturnValueOnce(pending.promise)
    render(<CreateSilenceDialog open onOpenChange={onOpenChange} onCreated={onCreated} token="test-token" />)
    await userEvent.type(screen.getByLabelText(/silences.name/), "kept-name")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    bumpAuthSessionGeneration()
    await act(async () => {
      if (outcome === "success") pending.resolve(createdSilence)
      else pending.reject(new Error("late failure"))
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(onOpenChange).not.toHaveBeenCalled()
    expect(onCreated).not.toHaveBeenCalled()
    expect(screen.getByLabelText(/silences.name/)).toHaveValue("kept-name")
    expect(screen.getByRole("button", { name: "silences.creating" })).toBeDisabled()
    expect(apiClient.createSilence).toHaveBeenCalledTimes(1)
  })

  it("does not apply a node list from an earlier auth generation", async () => {
    const pending = deferred<NodeRecord[]>()
    vi.mocked(apiClient.getNodes).mockReturnValue(pending.promise)
    render(<CreateSilenceDialog open onOpenChange={vi.fn()} onCreated={vi.fn()} token="test-token" />)
    await waitFor(() => expect(apiClient.getNodes).toHaveBeenCalled())
    const signal = vi.mocked(apiClient.getNodes).mock.calls.at(-1)?.[1]?.signal
    expect(signal?.aborted).toBe(false)
    bumpAuthSessionGeneration()
    await act(async () => {
      pending.resolve([matchedNode(9, "late-list")])
    })
    expect(screen.queryByRole("option", { name: "late-list" })).not.toBeInTheDocument()
    expect(signal?.aborted).toBe(false)
  })

  it("does not apply a node check that ignores abort after the auth generation changes", async () => {
    const pending = deferred<NodeRecord>()
    let calls = 0
    vi.mocked(apiClient.getNode).mockImplementation((_token, nodeId) => {
      calls += 1
      if (calls === 1) return pending.promise
      return Promise.resolve(matchedNode(nodeId, "node-42"))
    })
    renderQuick()
    await waitFor(() => expect(calls).toBe(1))
    const signal = vi.mocked(apiClient.getNode).mock.calls[0]?.[2]?.signal
    expect(signal?.aborted).toBe(false)
    bumpAuthSessionGeneration()
    await act(async () => {
      pending.resolve(matchedNode(42, "late-node"))
    })
    expect(screen.queryByText("late-node")).not.toBeInTheDocument()
    expect(signal?.aborted).toBe(false)
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.getByRole("status")).toHaveTextContent("silences.quick.nodeLoading")
    expect(screen.queryByLabelText("silences.name")).not.toBeInTheDocument()
    expect(apiClient.createSilence).not.toHaveBeenCalled()
  })

  it("discards a strict-mode node check that resolves after its attempt", async () => {
    const inflight: Array<ReturnType<typeof deferred<NodeRecord>>> = []
    vi.mocked(apiClient.getNode).mockImplementation(() => {
      const pending = deferred<NodeRecord>()
      inflight.push(pending)
      return pending.promise
    })
    render(
      <StrictMode>
        <CreateSilenceDialog
          open
          onOpenChange={vi.fn()}
          onCreated={vi.fn()}
          token="test-token"
          initialMatch={QUICK_EXEC}
        />
      </StrictMode>,
    )
    await waitFor(() => expect(inflight.length).toBeGreaterThanOrEqual(2))
    await act(async () => {
      inflight.slice(0, -1).forEach((item) => item.resolve(matchedNode(42, "stale-node")))
    })
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument()
    const current = inflight.at(-1)
    if (!current) throw new Error("missing current node check")
    await act(async () => {
      current.resolve(matchedNode(42, "fresh-node"))
    })
    expect(await screen.findByText("fresh-node")).toBeInTheDocument()
    expect(screen.queryByText("stale-node")).not.toBeInTheDocument()
  })

  it("drops the create result when the admin role is removed", async () => {
    const pending = deferred<Silence>()
    const onOpenChange = vi.fn()
    const onCreated = vi.fn()
    vi.mocked(apiClient.createSilence).mockReturnValue(pending.promise)
    const view = render(
      <CreateSilenceDialog open onOpenChange={onOpenChange} onCreated={onCreated} token="test-token" />,
    )
    await userEvent.type(screen.getByLabelText(/silences.name/), "downgrade")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    setAuth({ token: "test-token", role: "operator" })
    bumpAuthSessionGeneration()
    view.rerender(
      <CreateSilenceDialog open onOpenChange={onOpenChange} onCreated={onCreated} token="test-token" />,
    )
    await act(async () => {
      pending.resolve(createdSilence)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(onCreated).not.toHaveBeenCalled()
    expect(onOpenChange).toHaveBeenCalledWith(false)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })

  it("does not restore a sealed draft after an auth transition and A to B to A", async () => {
    const pending = deferred<Silence>()
    vi.mocked(apiClient.createSilence).mockReturnValue(pending.promise)
    render(<SilenceDialogHarness match={QUICK_EXEC} />)
    await userEvent.click(screen.getByRole("button", { name: "open-silence" }))
    expect(await screen.findByText("node-42")).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText(/silences.name/), "aba-draft")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    let barrier = 0
    await act(async () => {
      barrier = beginAuthTransitionBarrier()
    })
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    setAuth({ token: "admin-b", role: "admin" })
    bumpAuthSessionGeneration()
    setAuth({ token: "test-token", role: "admin" })
    bumpAuthSessionGeneration()
    act(() => {
      releaseAuthTransitionBarrier(barrier)
    })
    await act(async () => {
      pending.resolve(createdSilence)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(screen.getByTestId("created-count")).toHaveTextContent("0")
    await userEvent.click(screen.getByRole("button", { name: "open-silence" }))
    expect(await screen.findByLabelText(/silences.name/)).toHaveValue("")
    expect(screen.queryByDisplayValue("aba-draft")).not.toBeInTheDocument()
  })

  it("ignores a create that resolves after unmount", async () => {
    const pending = deferred<Silence>()
    const onCreated = vi.fn()
    vi.mocked(apiClient.createSilence).mockReturnValue(pending.promise)
    const view = render(
      <CreateSilenceDialog open onOpenChange={vi.fn()} onCreated={onCreated} token="test-token" />,
    )
    await userEvent.type(screen.getByLabelText(/silences.name/), "unmounted")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    view.unmount()
    await act(async () => {
      pending.resolve(createdSilence)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(onCreated).not.toHaveBeenCalled()
  })
})

describe("silence panel ownership", () => {
  function silenceNamed(id: number, name: string): Silence {
    return {
      id,
      name,
      matchNodeId: 1,
      matchCategory: "XR-NODE",
      matchTags: ["prod"],
      startsAt: "2026-04-19T00:00:00Z",
      endsAt: "2026-04-19T02:00:00Z",
      createdBy: 1,
      note: "",
      createdAt: "",
      updatedAt: "",
    }
  }

  function listMock() {
    return vi.mocked(apiClient.listSilences)
  }

  function deleteMock() {
    return vi.mocked(apiClient.deleteSilence)
  }

  function installListQueue() {
    const queue: Array<ReturnType<typeof deferred<Silence[]>>> = []
    // Resolves even when the caller aborts. Attempt and owner checks have to drop it.
    listMock().mockImplementation(() => {
      const pending = deferred<Silence[]>()
      queue.push(pending)
      return pending.promise
    })
    return queue
  }

  function switchAuth(
    view: ReturnType<typeof render>,
    next: { token: string | null; role: "admin" | "operator" | "viewer" | null },
  ) {
    setAuth(next)
    bumpAuthSessionGeneration()
    view.rerender(<SilencesPanel />)
  }

  function revokeButton(name: string) {
    return screen.getByRole("button", { name: `删除静默规则 ${name}` })
  }

  it("shows no rules only after the same admin read succeeds", async () => {
    listMock().mockResolvedValue([])
    render(<SilencesPanel />)
    expect(await screen.findByText("silences.empty")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(listMock()).toHaveBeenCalledWith("test-token", false, expect.objectContaining({
      signal: expect.any(AbortSignal),
    }))
  })

  it("keeps a failed first read retryable and does not claim the list is empty", async () => {
    listMock().mockRejectedValueOnce(new Error("list failed"))
    render(<SilencesPanel />)
    expect(await screen.findByRole("alert")).toHaveTextContent("silences.loadFailed")
    expect(screen.getByRole("alert")).toHaveTextContent("list failed")
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(screen.queryByText("maint-A")).not.toBeInTheDocument()
    expect(toastErrorMock).toHaveBeenCalledWith("list failed")

    await userEvent.click(screen.getByRole("button", { name: "silences.loadRetry" }))
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
  })

  it("keeps the last successful list when a later read fails", async () => {
    render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    listMock().mockRejectedValueOnce(new Error("list failed"))
    await userEvent.click(revokeButton("maint-A"))
    await waitFor(() => expect(toastErrorMock).toHaveBeenCalledWith("list failed"))
    expect(toastSuccessMock).toHaveBeenCalledWith("silences.revoke")
    expect(screen.getByText("maint-A")).toBeInTheDocument()
    expect(screen.getByRole("alert")).toHaveTextContent("silences.loadFailed")
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
  })

  it.each(["success", "error"] as const)("drops abort-ignoring list %s and finally after the auth generation changes", async (outcome) => {
    const queue = installListQueue()
    render(<SilencesPanel />)
    await waitFor(() => expect(queue).toHaveLength(1))
    const signal = listMock().mock.calls[0]?.[2]?.signal
    expect(signal?.aborted).toBe(false)
    bumpAuthSessionGeneration()
    await act(async () => {
      if (outcome === "success") queue[0]?.resolve([silenceNamed(9, "late-list")])
      else queue[0]?.reject(new Error("late list"))
    })
    expect(screen.queryByText("late-list")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(screen.getByText("common.loading")).toBeInTheDocument()
    expect(listMock()).toHaveBeenCalledTimes(1)
  })

  it("drops a late list success and error after the admin role is removed", async () => {
    const queue = installListQueue()
    const view = render(<SilencesPanel />)
    await waitFor(() => expect(queue).toHaveLength(1))
    switchAuth(view, { token: "test-token", role: "operator" })
    const lists = listMock().mock.calls.length
    await act(async () => {
      queue[0]?.resolve([silenceNamed(9, "late-success")])
    })
    expect(screen.queryByText("late-success")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(listMock().mock.calls.length).toBe(lists)

    switchAuth(view, { token: "test-token", role: "admin" })
    await waitFor(() => expect(queue.length).toBeGreaterThan(lists))
    const stale = queue.at(-1)
    if (!stale) throw new Error("missing admin list")
    switchAuth(view, { token: "test-token", role: "operator" })
    await act(async () => {
      stale.reject(new Error("late list"))
    })
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(screen.queryByRole("alert")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(deleteMock()).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.createSilence)).not.toHaveBeenCalled()
  })

  it("drops abort-ignoring list results from token A after A to B to A", async () => {
    const queue = installListQueue()
    const view = render(<SilencesPanel />)
    await waitFor(() => expect(queue.length).toBeGreaterThanOrEqual(1))
    const firstSignal = listMock().mock.calls[0]?.[2]?.signal
    const firstCount = queue.length
    switchAuth(view, { token: "admin-b", role: "admin" })
    await waitFor(() => expect(queue.length).toBeGreaterThan(firstCount))
    const midCount = queue.length
    switchAuth(view, { token: "test-token", role: "admin" })
    await waitFor(() => expect(queue.length).toBeGreaterThan(midCount))
    expect(firstSignal?.aborted).toBe(true)
    expect(listMock().mock.calls.at(-1)?.[2]?.signal?.aborted).toBe(false)
    await act(async () => {
      queue.slice(0, -1).forEach((item, index) => {
        if (index === 0) item.reject(new Error("stale list"))
        else item.resolve([silenceNamed(8, "stale-a")])
      })
    })
    expect(screen.queryByText("stale-a")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(toastErrorMock).not.toHaveBeenCalled()
    const current = queue.at(-1)
    if (!current) throw new Error("missing current list")
    await act(async () => {
      current.resolve([silenceNamed(9, "fresh-a")])
    })
    expect(await screen.findByText("fresh-a")).toBeInTheDocument()
    expect(screen.queryByText("stale-a")).not.toBeInTheDocument()
  })

  it("drops an in-flight list across an auth transition and reloads the current admin", async () => {
    const queue = installListQueue()
    render(<SilencesPanel />)
    await waitFor(() => expect(queue.length).toBeGreaterThanOrEqual(1))
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    await userEvent.type(screen.getByLabelText(/silences.name/), "transition-draft")
    const before = queue.length
    let barrier = 0
    await act(async () => {
      barrier = beginAuthTransitionBarrier()
    })
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument())
    expect(queue.length).toBe(before)
    await act(async () => {
      releaseAuthTransitionBarrier(barrier)
    })
    await waitFor(() => expect(queue.length).toBeGreaterThan(before))
    await act(async () => {
      queue.slice(0, before).forEach((item) => item.resolve([silenceNamed(4, "stale-transition")]))
    })
    expect(screen.queryByText("stale-transition")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    const current = queue.at(-1)
    if (!current) throw new Error("missing list after transition")
    await act(async () => {
      current.resolve([silenceNamed(5, "after-transition")])
    })
    expect(await screen.findByText("after-transition")).toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    expect(screen.getByLabelText(/silences.name/)).toHaveValue("")
    expect(screen.queryByDisplayValue("transition-draft")).not.toBeInTheDocument()
  })

  it("does not list or revoke after a transition when the role is no longer admin", async () => {
    const view = render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    const lists = listMock().mock.calls.length
    let barrier = 0
    await act(async () => {
      barrier = beginAuthTransitionBarrier()
    })
    switchAuth(view, { token: "test-token", role: "viewer" })
    await act(async () => {
      releaseAuthTransitionBarrier(barrier)
    })
    await act(async () => {
      await Promise.resolve()
    })
    expect(listMock().mock.calls.length).toBe(lists)
    expect(deleteMock()).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.createSilence)).not.toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: /silences.new/ })).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(screen.queryByText("maint-A")).not.toBeInTheDocument()
  })

  it("ignores a list success and error after unmount", async () => {
    const queue = installListQueue()
    const view = render(<SilencesPanel />)
    await waitFor(() => expect(queue).toHaveLength(1))
    const signal = listMock().mock.calls[0]?.[2]?.signal
    view.unmount()
    expect(signal?.aborted).toBe(true)
    await act(async () => {
      queue[0]?.resolve([silenceNamed(7, "after-unmount")])
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()

    const again = installListQueue()
    const second = render(<SilencesPanel />)
    await waitFor(() => expect(again).toHaveLength(1))
    second.unmount()
    await act(async () => {
      again[0]?.reject(new Error("unmounted list"))
    })
    expect(toastErrorMock).not.toHaveBeenCalled()
  })

  it("discards a strict-mode list that resolves after its attempt", async () => {
    const queue = installListQueue()
    render(
      <StrictMode>
        <SilencesPanel />
      </StrictMode>,
    )
    await waitFor(() => expect(queue.length).toBeGreaterThanOrEqual(2))
    await act(async () => {
      queue.slice(0, -1).forEach((item, index) => {
        if (index === 0) item.reject(new Error("stale strict"))
        else item.resolve([silenceNamed(1, "stale-strict")])
      })
    })
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(screen.queryByText("stale-strict")).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    const current = queue.at(-1)
    if (!current) throw new Error("missing current strict list")
    await act(async () => {
      current.resolve([silenceNamed(2, "fresh-strict")])
    })
    expect(await screen.findByText("fresh-strict")).toBeInTheDocument()
    expect(screen.queryByText("stale-strict")).not.toBeInTheDocument()
  })

  it.each([
    { token: "test-token" as string | null, role: "operator" as const },
    { token: "test-token" as string | null, role: "viewer" as const },
    { token: null, role: "admin" as const },
  ])("does not list, create, or revoke for $role with token $token", async ({ token, role }) => {
    setAuth({ token, role })
    render(<SilencesPanel />)
    await act(async () => {
      await Promise.resolve()
    })
    expect(listMock()).not.toHaveBeenCalled()
    expect(deleteMock()).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.createSilence)).not.toHaveBeenCalled()
    expect(vi.mocked(apiClient.getNodes)).not.toHaveBeenCalled()
    expect(screen.queryByRole("button", { name: /silences.new/ })).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(screen.queryByRole("button", { name: /删除静默规则/ })).not.toBeInTheDocument()
  })

  it("keeps the loaded row when revoke fails", async () => {
    render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    const lists = listMock().mock.calls.length
    deleteMock().mockRejectedValueOnce(new Error("revoke failed"))
    await userEvent.click(revokeButton("maint-A"))
    await waitFor(() => expect(toastErrorMock).toHaveBeenCalledWith("revoke failed"))
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(deleteMock()).toHaveBeenCalledWith("test-token", 1)
    expect(listMock().mock.calls.length).toBe(lists)
    expect(screen.getByText("maint-A")).toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(revokeButton("maint-A")).toBeEnabled()
    expect(revokeButton("maint-A")).toHaveTextContent("silences.revoke")
  })

  it.each(["success", "failure"] as const)("does not dispatch a second revoke until the first %s settles", async (outcome) => {
    listMock().mockResolvedValue([silenceNamed(1, "one"), silenceNamed(2, "two")])
    const pending = deferred<void>()
    let reentered = false
    deleteMock().mockImplementation(() => {
      if (!reentered) {
        reentered = true
        // Still inside the first click, before the disabled state commits.
        fireEvent.click(revokeButton("one"))
        fireEvent.click(revokeButton("two"))
      }
      return pending.promise
    })
    render(<SilencesPanel />)
    expect(await screen.findByText("one")).toBeInTheDocument()
    expect(screen.getByText("two")).toBeInTheDocument()

    await userEvent.click(revokeButton("one"))
    expect(deleteMock()).toHaveBeenCalledTimes(1)
    expect(deleteMock()).toHaveBeenCalledWith("test-token", 1)
    expect(revokeButton("one")).toBeDisabled()
    expect(revokeButton("one")).toHaveTextContent("common.loading")
    expect(revokeButton("two")).toBeDisabled()
    expect(revokeButton("two")).toHaveTextContent("silences.revoke")
    fireEvent.click(revokeButton("one"))
    fireEvent.click(revokeButton("two"))
    expect(deleteMock()).toHaveBeenCalledTimes(1)

    const listsWhilePending = listMock().mock.calls.length
    await act(async () => {
      if (outcome === "success") pending.resolve(undefined)
      else pending.reject(new Error("revoke one failed"))
    })
    if (outcome === "success") {
      await waitFor(() => expect(toastSuccessMock).toHaveBeenCalledTimes(1))
      expect(toastSuccessMock).toHaveBeenCalledWith("silences.revoke")
      expect(toastErrorMock).not.toHaveBeenCalled()
      expect(listMock().mock.calls.length).toBeGreaterThan(listsWhilePending)
    } else {
      await waitFor(() => expect(toastErrorMock).toHaveBeenCalledWith("revoke one failed"))
      expect(toastSuccessMock).not.toHaveBeenCalled()
      expect(listMock().mock.calls.length).toBe(listsWhilePending)
    }
    expect(screen.getByText("one")).toBeInTheDocument()
    expect(screen.getByText("two")).toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(revokeButton("one")).toBeEnabled()
    expect(revokeButton("two")).toBeEnabled()
    expect(revokeButton("one")).toHaveTextContent("silences.revoke")
    expect(revokeButton("two")).toHaveTextContent("silences.revoke")

    const next = deferred<void>()
    reentered = false
    deleteMock().mockImplementation(() => {
      if (!reentered) {
        reentered = true
        fireEvent.click(revokeButton("one"))
        fireEvent.click(revokeButton("two"))
      }
      return next.promise
    })
    const listsBeforeNext = listMock().mock.calls.length
    await userEvent.click(revokeButton("two"))
    expect(deleteMock()).toHaveBeenCalledTimes(2)
    expect(deleteMock()).toHaveBeenLastCalledWith("test-token", 2)
    expect(revokeButton("two")).toHaveTextContent("common.loading")
    expect(revokeButton("one")).toBeDisabled()
    expect(revokeButton("two")).toBeDisabled()
    fireEvent.click(revokeButton("one"))
    fireEvent.click(revokeButton("two"))
    expect(deleteMock()).toHaveBeenCalledTimes(2)

    await act(async () => {
      if (outcome === "success") next.reject(new Error("revoke two failed"))
      else next.resolve(undefined)
    })
    if (outcome === "success") {
      await waitFor(() => expect(toastErrorMock).toHaveBeenCalledWith("revoke two failed"))
      expect(toastSuccessMock).toHaveBeenCalledTimes(1)
      expect(listMock().mock.calls.length).toBe(listsBeforeNext)
    } else {
      await waitFor(() => expect(toastSuccessMock).toHaveBeenCalledTimes(1))
      expect(toastSuccessMock).toHaveBeenCalledWith("silences.revoke")
      expect(toastErrorMock).toHaveBeenCalledTimes(1)
      expect(toastErrorMock).toHaveBeenCalledWith("revoke one failed")
      expect(listMock().mock.calls.length).toBeGreaterThan(listsBeforeNext)
    }
    expect(deleteMock().mock.calls.map((call) => call[1])).toEqual([1, 2])
    expect(screen.getByText("one")).toBeInTheDocument()
    expect(screen.getByText("two")).toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
    expect(revokeButton("one")).toBeEnabled()
    expect(revokeButton("two")).toBeEnabled()
    expect(revokeButton("two")).toHaveTextContent("silences.revoke")
  })

  it("drops a revoke success after downgrade and token A to B to A", async () => {
    const pending = deferred<void>()
    deleteMock().mockReturnValue(pending.promise)
    const view = render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    await userEvent.click(revokeButton("maint-A"))
    const deletes = deleteMock().mock.calls.length
    switchAuth(view, { token: "admin-b", role: "admin" })
    switchAuth(view, { token: "test-token", role: "admin" })
    await act(async () => {
      pending.resolve(undefined)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(deleteMock().mock.calls.length).toBe(deletes)
    await screen.findByText("maint-A")
    expect(revokeButton("maint-A")).toHaveTextContent("silences.revoke")
    expect(revokeButton("maint-A")).toBeEnabled()

    const late = deferred<void>()
    deleteMock().mockReturnValue(late.promise)
    await userEvent.click(revokeButton("maint-A"))
    switchAuth(view, { token: "test-token", role: "operator" })
    const listsAfterDowngrade = listMock().mock.calls.length
    await act(async () => {
      late.resolve(undefined)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(listMock().mock.calls.length).toBe(listsAfterDowngrade)
    expect(screen.queryByRole("button", { name: /删除静默规则/ })).not.toBeInTheDocument()
    expect(screen.queryByText("silences.empty")).not.toBeInTheDocument()
  })

  it("does not let a pre-transition revoke clear a revoke started after release", async () => {
    const first = deferred<void>()
    const second = deferred<void>()
    deleteMock()
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(second.promise)
    render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    await userEvent.click(revokeButton("maint-A"))
    let barrier = 0
    await act(async () => {
      barrier = beginAuthTransitionBarrier()
    })
    expect(revokeButton("maint-A")).toHaveTextContent("silences.revoke")
    await act(async () => {
      releaseAuthTransitionBarrier(barrier)
    })
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    await userEvent.click(revokeButton("maint-A"))
    expect(revokeButton("maint-A")).toHaveTextContent("common.loading")
    await act(async () => {
      first.reject(new Error("late revoke"))
    })
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(revokeButton("maint-A")).toHaveTextContent("common.loading")
    expect(revokeButton("maint-A")).toBeDisabled()
    await act(async () => {
      second.resolve(undefined)
    })
    await waitFor(() => expect(toastSuccessMock).toHaveBeenCalledTimes(1))
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(revokeButton("maint-A")).toBeEnabled()
  })

  it("ignores a revoke that resolves after unmount", async () => {
    const pending = deferred<void>()
    deleteMock().mockReturnValue(pending.promise)
    const view = render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    await userEvent.click(revokeButton("maint-A"))
    const lists = listMock().mock.calls.length
    view.unmount()
    await act(async () => {
      pending.resolve(undefined)
    })
    expect(toastSuccessMock).not.toHaveBeenCalled()
    expect(toastErrorMock).not.toHaveBeenCalled()
    expect(listMock().mock.calls.length).toBe(lists)
  })

  it("reloads rules after the shared dialog reports creation", async () => {
    render(<SilencesPanel />)
    expect(await screen.findByText("maint-A")).toBeInTheDocument()
    const lists = listMock().mock.calls.length
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    await userEvent.type(screen.getByLabelText(/silences.name/), "created-rule")
    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))
    await waitFor(() => expect(listMock().mock.calls.length).toBeGreaterThan(lists))
    expect(toastSuccessMock).toHaveBeenCalled()
    expect(vi.mocked(apiClient.createSilence)).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument()
  })
})
