import "@testing-library/jest-dom/vitest";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setLanguage } from "@/i18n";
import { ApiError } from "@/lib/api/core";
import { runAxe } from "@/test/a11y-helpers";
import type {
  BackupFileSourceComparisonItem,
  BackupFileSourceComparisonPage,
  BackupFileSourceComparisonSide,
  BackupFileSourceVersion,
  CatalogProjection,
} from "@/types/domain";

import { BackupFileSourceCompare, type BackupFileSourceCompareProps } from "./backup-file-source-compare";

const mocks = vi.hoisted(() => ({ compare: vi.fn() }));
vi.mock("@/lib/api/client", () => ({ apiClient: { compareBackupFileSourceVersions: mocks.compare } }));

const setId = "a".repeat(32);
const cursor = "eyJvZmZzZXQiOjEwfQ.signature_1";

function version(char: string, capturedAt: string, overrides: Partial<BackupFileSourceVersion> = {}): BackupFileSourceVersion {
  return {
    recoveryPointId: char.repeat(32), repositoryId: "c".repeat(32), producingTaskId: 9, capturedAt, committedAt: null,
    createdAt: capturedAt, lifecycleState: "committed", catalogCoverage: "complete", browseState: "browsable",
    unavailableReason: null, comparisonState: "eligible", contentAvailability: { available: true, reason: null },
    entryCount: 1, logicalBytes: 1, permissions: { list: true, preview: false, download: false },
    ...overrides,
  };
}
const newest = version("b", "2026-08-29T00:00:00.000Z", {
  browseState: "unavailable",
  unavailableReason: { code: "repository_offline", params: {} },
  contentAvailability: { available: false, reason: { code: "repository_offline", params: {} } },
});
const middle = version("d", "2026-08-28T00:00:00.000Z");
const indexing = version("e", "2026-08-27T12:00:00.000Z", { comparisonState: "indexing", catalogCoverage: "building" });
const oldest = version("f", "2026-08-27T00:00:00.000Z");
const versions = [newest, middle, indexing, oldest];

function side(point: string, entryChar: string, name: string, crumbs: string[] = ["docs", "2026"]): BackupFileSourceComparisonSide {
  return {
    ref: { recoveryPointId: point, entryId: entryChar.repeat(64) }, name, entryType: "file", size: 1, modifiedAt: null,
    fingerprintStrength: "strong",
    breadcrumb: crumbs.map((crumb, index) => ({ ref: { recoveryPointId: point, entryId: String(index + 1).repeat(64) }, name: crumb })),
    omittedAncestorNames: [],
  };
}
const modifiedItem: BackupFileSourceComparisonItem = {
  kind: "modified",
  base: side(oldest.recoveryPointId, "a", "report.txt"),
  compare: side(newest.recoveryPointId, "b", "report.txt"),
  changedFields: ["size", "content"],
  contentEquality: "different",
};
const addedItem: BackupFileSourceComparisonItem = {
  kind: "added", base: null, compare: side(newest.recoveryPointId, "c", "new.txt", []), changedFields: [], contentEquality: "unknown",
};
function page(items: BackupFileSourceComparisonItem[], nextCursor: string | null): CatalogProjection<BackupFileSourceComparisonPage> {
  return {
    status: "available",
    value: {
      baseRecoveryPointId: oldest.recoveryPointId,
      compareRecoveryPointId: newest.recoveryPointId,
      baseContentAvailability: { available: true, reason: null },
      compareContentAvailability: { available: false, reason: { code: "repository_offline", params: {} } },
      items,
      nextCursor,
    },
  };
}
// The ES2022 lib configured for web/ has no Promise.withResolvers.
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

function props(overrides: Partial<BackupFileSourceCompareProps> = {}): BackupFileSourceCompareProps {
  return {
    token: "token",
    role: "operator",
    backupSetId: setId,
    versions,
    selectedRecoveryPointId: undefined,
    hasMoreVersions: false,
    loadingMoreVersions: false,
    versionsLoading: false,
    onLoadMoreVersions: vi.fn(),
    onRefreshVersions: vi.fn(),
    versionsFailure: null,
    onRetryVersions: vi.fn(),
    ...overrides,
  };
}

async function openAndSelect(user: UserEvent) {
  await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
  const dialog = await screen.findByRole("dialog", { name: "Compare recovery points" });
  await user.selectOptions(within(dialog).getByLabelText("Base recovery point"), oldest.recoveryPointId);
  await user.selectOptions(within(dialog).getByLabelText("Comparison recovery point"), newest.recoveryPointId);
  return dialog;
}

beforeEach(async () => {
  mocks.compare.mockReset();
  await act(async () => { await setLanguage("en"); });
});

afterEach(async () => {
  await act(async () => { await setLanguage("zh"); });
});

describe("BackupFileSourceCompare", () => {
  it("is unavailable until a backup set is resolved", () => {
    render(<BackupFileSourceCompare {...props({ backupSetId: null })} />);
    expect(screen.getByRole("button", { name: "Compare recovery points" })).toBeDisabled();
  });

  it("opens with focus on a base prefilled from an eligible selected version and offers only eligible points", async () => {
    const user = userEvent.setup();
    render(<BackupFileSourceCompare {...props({ selectedRecoveryPointId: middle.recoveryPointId })} />);

    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const dialog = await screen.findByRole("dialog", { name: "Compare recovery points" });
    const baseSelect = within(dialog).getByLabelText("Base recovery point");
    expect(baseSelect).toHaveFocus();
    expect(baseSelect).toHaveValue(middle.recoveryPointId);
    expect(within(dialog).getByLabelText("Comparison recovery point")).toHaveValue("");

    const indexingOption = within(baseSelect).getByRole("option", { name: /Indexing/ });
    expect(indexingOption).toBeDisabled();
    // Eligible options omit the redundant state, use a short time and keep offline for selection.
    const shortTime = new Intl.DateTimeFormat("en", { dateStyle: "short", timeStyle: "medium" });
    const offlineOption = within(baseSelect).getByRole("option", { name: `${shortTime.format(new Date("2026-08-29T00:00:00.000Z"))} · Content offline` });
    expect(offlineOption).toBeEnabled();
    expect(within(baseSelect).getByRole("option", { name: shortTime.format(new Date("2026-08-28T00:00:00.000Z")) })).toBeEnabled();
    expect(baseSelect).not.toHaveTextContent("Comparable");
    expect(within(dialog).getByRole("button", { name: "Run comparison" })).toHaveAttribute("aria-disabled", "true");
    expect(mocks.compare).not.toHaveBeenCalled();
  });

  it("does not prefill an ineligible selected version and warns about the same point", async () => {
    const user = userEvent.setup();
    render(<BackupFileSourceCompare {...props({ selectedRecoveryPointId: indexing.recoveryPointId })} />);
    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByLabelText("Base recovery point")).toHaveValue("");

    await user.selectOptions(within(dialog).getByLabelText("Base recovery point"), oldest.recoveryPointId);
    await user.selectOptions(within(dialog).getByLabelText("Comparison recovery point"), oldest.recoveryPointId);
    expect(within(dialog).getByText("Choose two different recovery points.")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    expect(mocks.compare).not.toHaveBeenCalled();
  });

  it("loads more candidate versions through the existing version pagination", async () => {
    const user = userEvent.setup();
    const onLoadMoreVersions = vi.fn();
    const { rerender } = render(<BackupFileSourceCompare {...props({ hasMoreVersions: true, onLoadMoreVersions })} />);
    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const dialog = await screen.findByRole("dialog");

    await user.click(within(dialog).getByRole("button", { name: "Load more versions" }));
    expect(onLoadMoreVersions).toHaveBeenCalledTimes(1);

    rerender(<BackupFileSourceCompare {...props({ hasMoreVersions: true, loadingMoreVersions: true, onLoadMoreVersions })} />);
    await user.click(within(dialog).getByRole("button", { name: "Loading more..." }));
    expect(onLoadMoreVersions).toHaveBeenCalledTimes(1);
  });

  it("shows ordered results with side breadcrumbs, offline side, certainty and explicit continuation", async () => {
    const user = userEvent.setup();
    mocks.compare
      .mockResolvedValueOnce(page([modifiedItem], cursor))
      .mockResolvedValueOnce(page([addedItem], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);

    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    expect(mocks.compare).toHaveBeenCalledWith("token", setId, expect.objectContaining({
      baseRecoveryPointId: oldest.recoveryPointId,
      compareRecoveryPointId: newest.recoveryPointId,
      limit: 100,
    }));
    const results = within(dialog).getByRole("region", { name: "Comparison results" });
    expect(await within(results).findByRole("status")).toHaveTextContent("1 change loaded · More changes are available.");
    const list = within(results).getByRole("list", { name: "Changed files" });
    const [row] = within(list).getAllByRole("listitem");
    expect(row).toHaveTextContent("Modified");
    expect(row).toHaveTextContent("docs / 2026 / report.txt");
    expect(row).toHaveTextContent("Changed: Size, Content");
    expect(row).toHaveTextContent("Content differs");
    expect(within(results).getAllByText("Content offline")).toHaveLength(1);
    expect(within(results).getByText("Comparison").parentElement).toHaveTextContent("Content offline");
    expect(within(results).getByText("Base").parentElement).not.toHaveTextContent("Content offline");

    await user.click(within(results).getByRole("button", { name: "Load more changes" }));
    expect(mocks.compare).toHaveBeenLastCalledWith("token", setId, expect.objectContaining({ cursor }));
    await waitFor(() => expect(within(list).getAllByRole("listitem")).toHaveLength(2));
    const added = within(list).getAllByRole("listitem")[1];
    expect(added).toHaveTextContent("Added");
    expect(added).toHaveTextContent("Comparison: new.txt");
    expect(added).not.toHaveTextContent("Content equality unknown");
    expect(within(results).getByRole("status")).toHaveTextContent("2 changes loaded");
    expect(within(results).queryByRole("button", { name: "Load more changes" })).not.toBeInTheDocument();
    await waitFor(() => expect(within(results).getByRole("status")).toHaveFocus());
  });

  it("shows no empty state until a complete empty result arrives", async () => {
    const user = userEvent.setup();
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockReturnValueOnce(pending.promise);
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);

    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    const status = within(dialog).getByRole("status");
    expect(status).toHaveTextContent("Comparing recovery points...");
    expect(within(dialog).queryByText(/No differences/)).not.toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Comparing..." })).toHaveAttribute("aria-disabled", "true");

    await act(async () => { pending.resolve(page([], null)); });
    expect(status).toHaveTextContent("No differences were reported between these recovery points.");
  });

  it("keeps loaded rows with an incomplete notice and manual retry after a transient continuation failure", async () => {
    const user = userEvent.setup();
    mocks.compare
      .mockResolvedValueOnce(page([modifiedItem], cursor))
      .mockRejectedValueOnce(new ApiError(503, "private/internal/path"))
      .mockResolvedValueOnce(page([addedItem], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    await user.click(await within(dialog).findByRole("button", { name: "Load more changes" }));

    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent("Could not load more changes. The results shown are incomplete.");
    expect(dialog).not.toHaveTextContent("private/internal/path");
    expect(within(dialog).getAllByRole("listitem")).toHaveLength(1);
    expect(within(dialog).getByRole("status")).toHaveTextContent("1 change loaded · Results are incomplete.");
    const retry = within(alert).getByRole("button", { name: "Retry loading changes" });
    await waitFor(() => expect(retry).toHaveFocus());
    expect(mocks.compare).toHaveBeenCalledTimes(2);

    await user.click(retry);
    await waitFor(() => expect(within(dialog).getAllByRole("listitem")).toHaveLength(2));
    expect(mocks.compare).toHaveBeenCalledTimes(3);
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
  });

  it("clears rows and guides reselection when a continuation is stale", async () => {
    const user = userEvent.setup();
    const onRefreshVersions = vi.fn();
    mocks.compare
      .mockResolvedValueOnce(page([modifiedItem], cursor))
      .mockRejectedValueOnce(new ApiError(409, "stale"));
    render(<BackupFileSourceCompare {...props({ onRefreshVersions })} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    await user.click(await within(dialog).findByRole("button", { name: "Load more changes" }));

    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent("These recovery points changed after the comparison started.");
    expect(within(dialog).queryByRole("list", { name: "Changed files" })).not.toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /Retry/ })).not.toBeInTheDocument();
    const reselect = within(alert).getByRole("button", { name: "Select recovery points again" });
    await waitFor(() => expect(reselect).toHaveFocus());

    await user.click(reselect);
    expect(onRefreshVersions).toHaveBeenCalledTimes(1);
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByLabelText("Base recovery point")).toHaveFocus());
  });

  it("offers a manual retry, never an automatic one, after a transient initial failure", async () => {
    const user = userEvent.setup();
    mocks.compare.mockRejectedValueOnce(new ApiError(503, "busy")).mockResolvedValueOnce(page([], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Comparison is temporarily unavailable.");
    expect(mocks.compare).toHaveBeenCalledTimes(1);
    await user.click(within(dialog).getByRole("button", { name: "Retry comparison" }));
    expect(await within(dialog).findByText(/No differences/)).toBeInTheDocument();
    expect(mocks.compare).toHaveBeenCalledTimes(2);
  });

  it("aborts and clears the comparison when the pair changes", async () => {
    const user = userEvent.setup();
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockReturnValueOnce(pending.promise);
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    const signal = mocks.compare.mock.calls[0][2].signal as AbortSignal;

    await user.selectOptions(within(dialog).getByLabelText("Comparison recovery point"), middle.recoveryPointId);
    expect(signal.aborted).toBe(true);
    await act(async () => { pending.resolve(page([modifiedItem], null)); });
    expect(within(dialog).queryByRole("list", { name: "Changed files" })).not.toBeInTheDocument();
    expect(within(dialog).getByRole("status")).toBeEmptyDOMElement();
  });

  it("aborts on close, returns focus to the entry and reopens with cleared state", async () => {
    const user = userEvent.setup();
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockReturnValueOnce(pending.promise);
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    const signal = mocks.compare.mock.calls[0][2].signal as AbortSignal;

    await user.keyboard("{Escape}");
    expect(signal.aborted).toBe(true);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Compare recovery points" })).toHaveFocus();
    await act(async () => { pending.resolve(page([modifiedItem], null)); });

    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const reopened = await screen.findByRole("dialog");
    expect(within(reopened).getByLabelText("Comparison recovery point")).toHaveValue("");
    expect(within(reopened).queryByRole("list")).not.toBeInTheDocument();

    await user.click(within(reopened).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Compare recovery points" })).toHaveFocus();
  });

  it("closes and aborts when the backup set or identity changes", async () => {
    const user = userEvent.setup();
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockReturnValueOnce(pending.promise);
    const { rerender } = render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    const signal = mocks.compare.mock.calls[0][2].signal as AbortSignal;

    rerender(<BackupFileSourceCompare {...props({ role: "admin" })} />);
    expect(signal.aborted).toBe(true);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    await screen.findByRole("dialog");
    rerender(<BackupFileSourceCompare {...props({ role: "admin", backupSetId: "e".repeat(32) })} />);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("renders Chinese labels and passes axe with results", async () => {
    await act(async () => { await setLanguage("zh"); });
    const user = userEvent.setup();
    mocks.compare.mockResolvedValueOnce(page([modifiedItem], cursor));
    render(<BackupFileSourceCompare {...props()} />);

    await user.click(screen.getByRole("button", { name: "比较恢复点" }));
    const dialog = await screen.findByRole("dialog", { name: "比较恢复点" });
    await user.selectOptions(within(dialog).getByLabelText("基准恢复点"), oldest.recoveryPointId);
    await user.selectOptions(within(dialog).getByLabelText("对照恢复点"), newest.recoveryPointId);
    await user.click(within(dialog).getByRole("button", { name: "运行比较" }));

    expect(await within(dialog).findByRole("list", { name: "变更的文件" })).toBeInTheDocument();
    expect(within(dialog).getByRole("status")).toHaveTextContent("已加载 1 项变更");
    expect(within(dialog).getByText("内容离线")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "加载更多变更" })).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("leaves focus where the user moved while a continuation was loading", async () => {
    const user = userEvent.setup();
    const pending = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockResolvedValueOnce(page([modifiedItem], cursor)).mockReturnValueOnce(pending.promise);
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    await user.click(await within(dialog).findByRole("button", { name: "Load more changes" }));
    expect(within(dialog).getByRole("button", { name: "Loading more changes..." })).toHaveFocus();

    await user.tab();
    const moved = document.activeElement;
    expect(moved).not.toBeNull();
    expect(moved).not.toBe(document.body);
    expect(within(dialog).getByRole("button", { name: "Loading more changes..." })).not.toHaveFocus();

    await act(async () => { pending.resolve(page([addedItem], null)); });
    await waitFor(() => expect(within(dialog).getAllByRole("listitem")).toHaveLength(2));
    expect(within(dialog).getByRole("status")).not.toHaveFocus();
    expect(document.activeElement).toBe(moved);
  });

  it("does not pull focus to the continuation retry after the user moved away", async () => {
    const user = userEvent.setup();
    const failing = deferred<CatalogProjection<BackupFileSourceComparisonPage>>();
    mocks.compare.mockResolvedValueOnce(page([modifiedItem], cursor)).mockReturnValueOnce(failing.promise);
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    await user.click(await within(dialog).findByRole("button", { name: "Load more changes" }));
    await user.tab();
    const moved = document.activeElement;

    await act(async () => { failing.reject(new ApiError(503, "busy")); });
    const retry = await within(dialog).findByRole("button", { name: "Retry loading changes" });
    expect(retry).not.toHaveFocus();
    expect(document.activeElement).toBe(moved);
  });

  it("labels a mixed-strength change without a strength field and leaves content unknown", async () => {
    const user = userEvent.setup();
    const mixed: BackupFileSourceComparisonItem = {
      kind: "modified",
      base: { ...side(oldest.recoveryPointId, "a", "report.txt"), fingerprintStrength: "none" },
      compare: { ...side(newest.recoveryPointId, "b", "report.txt"), size: 2 },
      changedFields: ["size", "owner"],
      contentEquality: "unknown",
    };
    mocks.compare.mockResolvedValueOnce(page([mixed], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));

    const [row] = within(await within(dialog).findByRole("list", { name: "Changed files" })).getAllByRole("listitem");
    expect(row).toHaveTextContent("Changed: Size, Owner");
    expect(row).toHaveTextContent("Content equality unknown");
    expect(row).not.toHaveTextContent(/fingerprint/i);
  });

  it("marks a truncated ancestor path with a leading ellipsis and keeps whitespace-only names visible", async () => {
    const user = userEvent.setup();
    const deep: BackupFileSourceComparisonItem = {
      kind: "added",
      base: null,
      compare: {
        ...side(newest.recoveryPointId, "c", "deep.txt", Array.from({ length: 256 }, (_, index) => `d${index}`)),
        omittedAncestorNames: ["srv"],
      },
      changedFields: [],
      contentEquality: "unknown",
    };
    const blank: BackupFileSourceComparisonItem = {
      kind: "added", base: null, compare: side(newest.recoveryPointId, "e", "   ", [" "]), changedFields: [], contentEquality: "unknown",
    };
    mocks.compare.mockResolvedValueOnce(page([deep, blank], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));

    const [deepRow, blankRow] = within(await within(dialog).findByRole("list", { name: "Changed files" })).getAllByRole("listitem");
    expect(within(deepRow).getByText("…")).toHaveAttribute("title", "Earlier folders omitted");
    expect(deepRow).toHaveTextContent("Comparison: …Earlier folders omitted / d0 / d1 /");
    expect(deepRow).toHaveTextContent("d255 / deep.txt");
    expect(within(deepRow).getByText("srv /")).not.toBeVisible();
    expect(blankRow.textContent).toContain("“   ”");
    expect(blankRow.textContent).toContain("“ ” / “   ”");
    expect(within(blankRow).queryByText(/earlier folder/)).not.toBeInTheDocument();
  });

  it("reveals each deep path's own omitted folders so identical tails and basenames stay distinguishable", async () => {
    const user = userEvent.setup();
    const tail = Array.from({ length: 256 }, (_, index) => `d${index}`);
    const deep = (point: string, entryChar: string, omitted: string[]): BackupFileSourceComparisonSide => ({
      ...side(point, entryChar, "report.txt", tail), omittedAncestorNames: omitted,
    });
    const alpha: BackupFileSourceComparisonItem = {
      kind: "added", base: null, compare: deep(newest.recoveryPointId, "c", ["srv", "alpha"]), changedFields: [], contentEquality: "unknown",
    };
    const beta: BackupFileSourceComparisonItem = { ...alpha, compare: deep(newest.recoveryPointId, "e", ["srv", "beta"]) };
    // Matched sides whose locations differ only above the shown tail are listed separately.
    const moved: BackupFileSourceComparisonItem = {
      kind: "modified",
      base: deep(oldest.recoveryPointId, "6", ["srv", "gamma"]),
      compare: deep(newest.recoveryPointId, "7", ["srv", "delta"]),
      changedFields: ["size"],
      contentEquality: "unknown",
    };
    mocks.compare.mockResolvedValueOnce(page([alpha, beta, moved], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));

    const [alphaRow, betaRow, movedRow] = within(await within(dialog).findByRole("list", { name: "Changed files" })).getAllByRole("listitem");
    expect(alphaRow).toHaveTextContent("d254 / d255 / report.txt");
    expect(betaRow).toHaveTextContent("d254 / d255 / report.txt");
    const alphaPrefix = within(alphaRow).getByText("srv / alpha /");
    const betaPrefix = within(betaRow).getByText("srv / beta /");
    expect(alphaPrefix).not.toBeVisible();
    expect(betaPrefix).not.toBeVisible();

    const alphaToggle = within(alphaRow).getByText("2 earlier folders");
    expect(alphaToggle.tagName).toBe("SUMMARY");
    await user.click(alphaToggle);
    expect(alphaPrefix).toBeVisible();
    expect(betaPrefix).not.toBeVisible();
    await user.click(alphaToggle);
    expect(alphaPrefix).not.toBeVisible();
    await user.click(within(betaRow).getByText("2 earlier folders"));
    expect(betaPrefix).toBeVisible();

    expect(movedRow).toHaveTextContent("Base: …");
    expect(movedRow).toHaveTextContent("Comparison: …");
    expect(within(movedRow).getAllByText("2 earlier folders")).toHaveLength(2);
    expect(within(movedRow).getByText("srv / gamma /")).toBeInTheDocument();
    expect(within(movedRow).getByText("srv / delta /")).toBeInTheDocument();
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("shows a selected offline point on its own described line until results report availability", async () => {
    const user = userEvent.setup();
    mocks.compare.mockResolvedValueOnce(page([modifiedItem], null));
    render(<BackupFileSourceCompare {...props()} />);
    const dialog = await openAndSelect(user);
    const compareSelect = within(dialog).getByLabelText("Comparison recovery point");
    expect(compareSelect).toHaveAccessibleDescription("Content offline");
    expect(within(dialog).getByLabelText("Base recovery point")).not.toHaveAccessibleDescription();
    expect(within(dialog).getByText("Content offline")).toBeVisible();

    await user.click(within(dialog).getByRole("button", { name: "Run comparison" }));
    const results = within(dialog).getByRole("region", { name: "Comparison results" });
    await within(results).findByRole("list", { name: "Changed files" });
    expect(compareSelect).not.toHaveAccessibleDescription();
    expect(within(dialog).getAllByText("Content offline")).toHaveLength(1);
    expect(within(results).getByText("Content offline")).toBeInTheDocument();
  });

  it("shows a failed candidate page inside the dialog with a targeted retry that keeps focus on pagination", async () => {
    const user = userEvent.setup();
    const onRetryVersions = vi.fn();
    const { rerender } = render(<BackupFileSourceCompare {...props({ hasMoreVersions: true, versionsFailure: "page", onRetryVersions })} />);
    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const dialog = await screen.findByRole("dialog");

    const alert = within(dialog).getByRole("alert");
    expect(alert).toHaveTextContent("Could not load more recovery points. The list shown is incomplete.");
    await user.click(within(alert).getByRole("button", { name: "Retry loading recovery points" }));
    expect(onRetryVersions).toHaveBeenCalledTimes(1);

    rerender(<BackupFileSourceCompare {...props({ hasMoreVersions: true, loadingMoreVersions: true, onRetryVersions })} />);
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Loading more..." })).toHaveFocus());

    // The last page arrives and removes the focused pagination control: focus returns to the base picker.
    rerender(<BackupFileSourceCompare {...props({ hasMoreVersions: false, onRetryVersions })} />);
    await waitFor(() => expect(within(dialog).getByLabelText("Base recovery point")).toHaveFocus());
  });

  it("shows a failed candidate refresh instead of an empty list and retries only the list", async () => {
    const user = userEvent.setup();
    const onRetryVersions = vi.fn();
    render(<BackupFileSourceCompare {...props({ versions: [], versionsFailure: "refresh", onRetryVersions })} />);
    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByRole("alert")).toHaveTextContent("Could not load the recovery points for this backup set.");
    expect(within(dialog).queryByText("No versions are loaded for this backup set yet.")).not.toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: "Retry loading recovery points" }));
    expect(onRetryVersions).toHaveBeenCalledTimes(1);
    expect(mocks.compare).not.toHaveBeenCalled();
  });

  it("shows candidate permission loss inside the dialog without a retry", async () => {
    const user = userEvent.setup();
    render(<BackupFileSourceCompare {...props({ versions: [], versionsFailure: "permission_denied" })} />);
    await user.click(screen.getByRole("button", { name: "Compare recovery points" }));
    const dialog = await screen.findByRole("dialog");

    expect(within(dialog).getByRole("alert")).toHaveTextContent("You do not have permission to list these recovery points.");
    expect(within(dialog).queryByRole("button", { name: "Retry loading recovery points" })).not.toBeInTheDocument();
    expect(within(dialog).queryByText("No versions are loaded for this backup set yet.")).not.toBeInTheDocument();
  });
});
