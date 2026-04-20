import "@testing-library/jest-dom/vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, it, expect, vi, beforeEach } from "vitest"
import { SilencesPanel } from "./settings-page.silences"

const { toastErrorMock, toastSuccessMock } = vi.hoisted(() => ({
  toastErrorMock: vi.fn(),
  toastSuccessMock: vi.fn(),
}))

vi.mock("@/components/ui/toast", () => ({
  toast: {
    error: toastErrorMock,
    success: toastSuccessMock,
  },
}))

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}))

vi.mock("react-i18next", () => ({
  useTranslation: () => ({
    t: (key: string) => key,
    i18n: { language: "zh", changeLanguage: vi.fn() },
  }),
  initReactI18next: { type: "3rdParty", init: vi.fn() },
}))

vi.mock("@/lib/api/silences", () => ({
  listSilences: vi.fn().mockResolvedValue([
    {
      id: 1,
      name: "maint-A",
      match_node_id: 1,
      match_category: "XR-NODE",
      match_tags: ["prod"],
      starts_at: "2026-04-19T00:00:00Z",
      ends_at: "2026-04-19T02:00:00Z",
      created_by: 1,
      note: "",
      created_at: "",
      updated_at: "",
    },
  ]),
  createSilence: vi.fn(),
  deleteSilence: vi.fn(),
  parseSilenceTags: (s: { match_tags: string | string[] | null }) =>
    Array.isArray(s.match_tags) ? s.match_tags : [],
}))

vi.mock("@/lib/api/client", () => ({
  apiClient: {
    getNodes: vi.fn().mockResolvedValue([
      { id: 1, name: "node-1" },
      { id: 2, name: "node-2" },
    ]),
  },
}))

describe("SilencesPanel", () => {
  beforeEach(() => {
    toastErrorMock.mockReset()
    toastSuccessMock.mockReset()
  })

  it("renders the existing silence row", async () => {
    render(<SilencesPanel />)
    await waitFor(() => expect(screen.getByText("maint-A")).toBeInTheDocument())
  })

  it("opens create dialog", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))
    expect(screen.getByLabelText(/silences.name/)).toBeInTheDocument()
  })

  it("rejects invalid time window", async () => {
    const { createSilence } = await import("@/lib/api/silences")
    const createSilenceMock = vi.mocked(createSilence)
    createSilenceMock.mockReset()

    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /silences.new/ }))

    // Fill in a name
    await userEvent.type(screen.getByLabelText(/silences.name/), "test-silence")

    // Set endsAt before startsAt via the datetime-local inputs
    const startsInput = screen.getByLabelText(/silences.startsAt/)
    const endsInput = screen.getByLabelText(/silences.endsAt/)
    await userEvent.clear(startsInput)
    await userEvent.type(startsInput, "2026-04-20T10:00")
    await userEvent.clear(endsInput)
    await userEvent.type(endsInput, "2026-04-20T09:00")

    await userEvent.click(screen.getByRole("button", { name: "silences.create" }))

    expect(toastErrorMock).toHaveBeenCalledWith("silences.validationWindowInvalid")
    expect(createSilenceMock).not.toHaveBeenCalled()
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
