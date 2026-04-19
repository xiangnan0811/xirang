import "@testing-library/jest-dom/vitest"
import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, it, expect, vi } from "vitest"
import { SilencesPanel } from "./settings-page.silences"

vi.mock("@/context/auth-context", () => ({
  useAuth: () => ({ token: "test-token" }),
}))

vi.mock("@/lib/api/silences", () => ({
  listSilences: vi.fn().mockResolvedValue([
    {
      id: 1,
      name: "maint-A",
      match_node_id: 1,
      match_category: "",
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

describe("SilencesPanel", () => {
  it("renders the existing silence row", async () => {
    render(<SilencesPanel />)
    await waitFor(() => expect(screen.getByText("maint-A")).toBeInTheDocument())
  })

  it("opens create dialog", async () => {
    render(<SilencesPanel />)
    await userEvent.click(screen.getByRole("button", { name: /新建静默规则/ }))
    expect(screen.getByLabelText(/名称/)).toBeInTheDocument()
  })
})
