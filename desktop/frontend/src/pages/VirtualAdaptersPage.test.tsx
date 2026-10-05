// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  HyperVAdapterStatus,
  HyperVRemoveResult,
  HyperVSwitch,
} from "../platform/services";
import { VirtualAdaptersPage } from "./VirtualAdaptersPage";

const mocks = vi.hoisted(() => ({
  state: { pageActive: true, desktop: true, locale: "zh" },
  list: vi.fn(),
  switches: vi.fn(),
  create: vi.fn(),
  remove: vi.fn(),
  removeAdapters: vi.fn(),
  notify: vi.fn(),
}));

// The real module pulls in generated Wails bindings; the page only needs the
// five calls plus the timeout budgets the wiring freeze exported.
vi.mock("../platform/services", () => ({
  appServices: {
    virtualAdapters: {
      list: mocks.list,
      switches: mocks.switches,
      create: mocks.create,
      remove: mocks.remove,
      removeAdapters: mocks.removeAdapters,
    },
  },
  withServiceTimeout: <T,>(request: Promise<T>) => request,
  HYPERV_ADAPTER_READ_TIMEOUT_MS: 10_000,
  HYPERV_ADAPTER_WRITE_TIMEOUT_MS: 60_000,
  HYPERV_ADAPTER_CREATE_TIMEOUT_MS: 180_000,
  HYPERV_ADAPTER_BATCH_TIMEOUT_MS: 180_000,
}));

vi.mock("../platform/runtime", () => ({
  isDesktopRuntime: () => mocks.state.desktop,
}));

// `t` stays a key echo so the frozen-contract keys stay assertable, EXCEPT for two
// families: the quota strings and the batch-removal strings moved into real
// language-pack entries, so the assertions below pin the real prose (and both
// languages, because `locale` is live). Scoping the lookup to those prefixes
// keeps every other key-echo assertion in this file byte-identical.
vi.mock("../i18n/i18n", async () => {
  const messages = (await import("../i18n/legacy.messages.json")).default as Record<
    string,
    Record<string, string>
  >;
  const resolvedPrefixes = ["virtual_adapters_quota_"];
  // Only the batch keys whose {count}/{name}/{reason} placeholders the tests
  // read back; the rest stay key echoes so no existing assertion moves.
  const resolvedKeys = [
    "virtual_adapters_delete",
    "virtual_adapters_select_all",
    "virtual_adapters_remove_selected",
    "virtual_adapters_remove_selected_ok",
    "virtual_adapters_remove_selected_partial",
    "virtual_adapters_remove_selected_warning",
    "virtual_adapters_remove_failure_line",
  ];
  return {
    useI18n: () => ({
      locale: mocks.state.locale,
      t: (key: string, values?: Record<string, string | number>) => {
        const resolved =
          resolvedPrefixes.some((prefix) => key.startsWith(prefix)) || resolvedKeys.includes(key);
        if (!resolved) return key;
        const template = messages[mocks.state.locale]?.[key] ?? messages.zh?.[key] ?? key;
        if (!values) return template;
        return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
          Object.prototype.hasOwnProperty.call(values, name) ? String(values[name]) : whole,
        );
      },
    }),
  };
});

vi.mock("../components/notifications/AppNotifications", () => ({
  useAppNotifications: () => ({ notify: mocks.notify }),
}));

vi.mock("../components/shell/PageActivity", () => ({
  usePageActive: () => mocks.state.pageActive,
}));

beforeAll(() => {
  (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

const adapter = (patch: Partial<HyperVAdapterStatus> = {}): HyperVAdapterStatus => ({
  name: "HypoMux vNIC 1",
  interfaceName: "vEthernet (HypoMux vNIC 1)",
  adapterId: "adapter-1",
  macAddress: "00-15-5D-01-02-03",
  switchName: "外网交换机",
  state: "ready",
  address: "172.24.8.11",
  prefixLength: 24,
  gateway: "172.24.8.1",
  managed: true,
  inPool: false,
  batchId: "batch-1",
  createdAt: "2026-01-01T00:00:00Z",
  lastError: "",
  ...patch,
});

const hypervSwitch = (patch: Partial<HyperVSwitch> = {}): HyperVSwitch => ({
  name: "外网交换机",
  type: "External",
  allowManagementOs: true,
  uplink: "以太网",
  netAdapterName: "以太网",
  ...patch,
});

const readyRow = adapter();

/** Flush microtasks (and any due timers) inside act(). */
const tick = async (ms = 0) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

const renderPage = () => render(<VirtualAdaptersPage />);

const toolbar = () => document.querySelector(".virtual-adapters-toolbar") as HTMLElement;

beforeEach(() => {
  // Only the timer APIs matter here; queueMicrotask/nextTick must stay real so
  // React's scheduler keeps flushing.
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval", "Date"] });
  mocks.state.pageActive = true;
  mocks.state.desktop = true;
  mocks.state.locale = "zh";
  mocks.list.mockReset().mockResolvedValue([readyRow]);
  mocks.switches.mockReset().mockResolvedValue([hypervSwitch()]);
  mocks.create.mockReset().mockResolvedValue([readyRow]);
  mocks.remove.mockReset().mockResolvedValue(undefined);
  mocks.removeAdapters.mockReset().mockResolvedValue([]);
  mocks.notify.mockReset();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("VirtualAdaptersPage polling gate", () => {
  it("does not poll at all while the page is inactive", async () => {
    mocks.state.pageActive = false;
    renderPage();
    await tick(12_000);
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.switches).not.toHaveBeenCalled();
  });

  it("starts polling once the page becomes active and stops when it goes away", async () => {
    mocks.state.pageActive = false;
    const view = renderPage();
    await tick();
    expect(mocks.list).not.toHaveBeenCalled();

    mocks.state.pageActive = true;
    view.rerender(<VirtualAdaptersPage />);
    await tick();
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await tick(3_000);
    expect(mocks.list).toHaveBeenCalledTimes(2);

    view.unmount();
    await tick(12_000);
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("polls list() and switches() every 3s", async () => {
    renderPage();
    await tick();
    expect(mocks.list).toHaveBeenCalledTimes(1);
    await tick(3_000);
    expect(mocks.list).toHaveBeenCalledTimes(2);
    expect(mocks.switches).toHaveBeenCalledTimes(2);
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();
  });
});

describe("VirtualAdaptersPage failure handling", () => {
  it("surfaces a list failure with a retry button and keeps the page alive", async () => {
    mocks.list.mockRejectedValue(new Error("wsl.exe exit 1"));
    renderPage();
    await tick();
    expect(screen.getByText("virtual_adapters_state_unknown")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_unavailable")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_retry" }));
    await tick();
    expect(mocks.list).toHaveBeenCalledTimes(2);
  });

  it("raises exactly one toast per mount no matter how often list fails", async () => {
    mocks.list.mockRejectedValue(new Error("boom"));
    renderPage();
    await tick();
    await tick(3_000);
    await tick(3_000);
    expect(mocks.list.mock.calls.length).toBeGreaterThan(1);
    expect(mocks.notify).toHaveBeenCalledTimes(1);
    expect(mocks.notify.mock.calls[0][0]).toMatchObject({
      dedupeKey: "virtual-adapters:error:list",
      intent: "error",
    });
  });

  it("still renders adapters when only switches() fails, and explains why create is unavailable", async () => {
    mocks.switches.mockRejectedValue(new Error("adapter not found"));
    renderPage();
    await tick();
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();
    // A failed read must not masquerade as "this host has no external switch".
    expect(screen.queryByText("virtual_adapters_no_switch")).toBeNull();
    // M4: a greyed-out Create with nothing explaining it is a dead end.
    expect(screen.getByText("virtual_adapters_switch_failed")).toBeTruthy();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_retry" }));
    await tick();
    expect(mocks.switches).toHaveBeenCalledTimes(2);
  });

  it("shows the switches() cause next to the generic hint, and notifies with it", async () => {
    // The generic "check Hyper-V is installed / are you elevated" hint cannot
    // distinguish "the host has no Hyper-V" from "the read was denied" from
    // "the query exited 1", so the service message has to reach the user too.
    const cause = "无法读取 Hyper-V 虚拟交换机：PowerShell 以退出码 1 结束";
    mocks.switches.mockRejectedValue(new Error(cause));
    renderPage();
    await tick();

    // Both lines are present — the detail never replaces the hint.
    expect(screen.getByText("virtual_adapters_switch_failed")).toBeTruthy();
    expect(screen.getByText(cause)).toBeTruthy();

    // …and the same cause reaches the notification centre.
    expect(mocks.notify).toHaveBeenCalledWith(expect.objectContaining({
      dedupeKey: "virtual-adapters:error:switches",
      intent: "error",
      message: cause,
    }));
    // Still no "no external switch" claim, and create stays disabled.
    expect(screen.queryByText("virtual_adapters_no_switch")).toBeNull();
    expect(screen.queryByText("virtual_adapters_switch_external_only")).toBeNull();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
  });

  it("drops the switches() cause again once the read recovers", async () => {
    mocks.switches
      .mockRejectedValueOnce(new Error("adapter not found"))
      .mockResolvedValueOnce([hypervSwitch()]);
    renderPage();
    await tick();
    expect(screen.getByText("adapter not found")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_retry" }));
    await tick();
    expect(screen.queryByText("adapter not found")).toBeNull();
    expect(screen.queryByText("virtual_adapters_switch_failed")).toBeNull();
  });

  it("warns and disables create when the host has no external switch", async () => {
    mocks.switches.mockResolvedValue([]);
    renderPage();
    await tick();
    expect(screen.getByText("virtual_adapters_no_switch")).toBeTruthy();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
  });

  it("treats a null switch list as empty instead of crashing", async () => {
    mocks.switches.mockResolvedValue(null);
    renderPage();
    await tick();
    expect(screen.getByText("virtual_adapters_no_switch")).toBeTruthy();
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();
  });
});

describe("VirtualAdaptersPage create flow", () => {
  const openCreateDialog = async () => {
    fireEvent.click(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    return screen.getByRole("dialog");
  };

  it("sends the manually typed count and shows the banner until no row is creating", async () => {
    const creatingRow = adapter({ adapterId: "adapter-new", state: "creating", address: "" });
    mocks.create.mockResolvedValue([creatingRow]);
    renderPage();
    await tick();

    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), { target: { value: "3" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();

    expect(mocks.create).toHaveBeenCalledWith("外网交换机", 3);
    expect(screen.getByText("virtual_adapters_create_banner")).toBeTruthy();

    // Contract §3.5: the batch is still settling, so the banner stays up.
    mocks.list.mockResolvedValue([creatingRow]);
    await tick(3_000);
    expect(screen.getByText("virtual_adapters_create_banner")).toBeTruthy();

    mocks.list.mockResolvedValue([{ ...creatingRow, state: "ready", address: "172.24.8.21" }]);
    await tick(3_000);
    expect(screen.queryByText("virtual_adapters_create_banner")).toBeNull();
  });

  it("keeps polling while the elevated create call is still running", async () => {
    mocks.create.mockReturnValue(new Promise<HyperVAdapterStatus[]>(() => {}));
    renderPage();
    await tick();

    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), { target: { value: "1" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    expect(mocks.create).toHaveBeenCalledTimes(1);

    await tick(3_000);
    await tick(3_000);
    expect(mocks.list.mock.calls.length).toBeGreaterThanOrEqual(3);
  });

  it("rejects a count the dialog could never produce", async () => {
    renderPage();
    await tick();
    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), { target: { value: "17" } });
    expect(within(dialog).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
    expect(mocks.create).not.toHaveBeenCalled();
  });

  // M1: Go returns only the rows of the batch it just created
  // (hyperv_adapter.go:1088), so assigning that slice wholesale empties the
  // table of everything already on screen until the next poll.
  it("merges the created batch instead of replacing the table", async () => {
    const existing = adapter({ name: "HypoMux vNIC 1", adapterId: "adapter-1", inPool: true });
    const batchRow = adapter({ name: "HypoMux vNIC 5", adapterId: "adapter-5", state: "creating", address: "" });
    mocks.list.mockResolvedValue([existing]);
    mocks.create.mockResolvedValue([batchRow]);
    renderPage();
    await tick();
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();

    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    expect(mocks.create).toHaveBeenCalled();

    // Both rows survive, so neither the pre-existing card nor its pool badge
    // blinks out for up to one poll interval.
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();
    expect(screen.getByText("HypoMux vNIC 5")).toBeTruthy();
    expect(screen.getAllByText("virtual_adapters_pool")).toHaveLength(1);
  });

  // §3.6: the outbound pool is written in a background goroutine *after*
  // Create() resolves, so the prompt used to be asserted unconditionally on every
  // successful batch — wrong when the write had already landed, and unfixable when
  // it never will. It is now derived from the rows themselves: this session's
  // adapter that reports `ready` while claiming pool membership is what still
  // needs the aggregation restarted. `inPool: true` on the batch row is therefore
  // part of the fixture, not decoration.
  it("prompts for an aggregation restart after a successful create", async () => {
    const batchRow = adapter({ name: "HypoMux vNIC 5", adapterId: "adapter-5", state: "ready", address: "172.24.8.21", inPool: true });
    mocks.create.mockResolvedValue([batchRow]);
    renderPage();
    await tick();
    expect(screen.queryByText("virtual_adapters_restart_pending")).toBeNull();

    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();

    // In the banner area, not inside the (already closed) create dialog.
    expect(screen.queryByRole("dialog")).toBeNull();
    const banner = document.querySelector(".virtual-adapters-restart") as HTMLElement;
    expect(banner).toBeTruthy();
    expect(within(banner).getByText("virtual_adapters_restart_pending")).toBeTruthy();
    expect(mocks.notify).toHaveBeenCalledWith(
      expect.objectContaining({ dedupeKey: "virtual-adapters:info:create-done", intent: "info" }),
    );

    // Dismissible by the user, per the audit's requirement.
    fireEvent.click(within(banner).getByRole("button", { name: "routing_dialog_cancel" }));
    expect(screen.queryByText("virtual_adapters_restart_pending")).toBeNull();
  });

  // The complement of the test above, and the reason the banner is derived rather
  // than asserted: a create whose rows never claim pool membership must NOT send
  // the user to restart the aggregation engine, and an unrelated pre-existing
  // adapter must never borrow this session's prompt.
  it("does not prompt when the created adapters never claim pool membership", async () => {
    const batchRow = adapter({ name: "HypoMux vNIC 5", adapterId: "adapter-5", state: "ready", address: "172.24.8.21", inPool: false });
    mocks.create.mockResolvedValue([batchRow]);
    renderPage();
    await tick();
    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    expect(mocks.create).toHaveBeenCalled();
    expect(screen.queryByText("virtual_adapters_restart_pending")).toBeNull();
  });

  // A create that returned nothing must not push the user into a pointless
  // restart, and an adapter that was already on the host before this session
  // must not be named in this session's prompt.
  it("does not prompt on a failed create or on an adapter it did not create", async () => {
    mocks.create.mockRejectedValue(new Error("CreateVmNetworkAdapter: Access denied (0x80070005)"));
    renderPage();
    await tick();
    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    expect(mocks.create).toHaveBeenCalled();
    expect(screen.queryByText("virtual_adapters_restart_pending")).toBeNull();
    expect(mocks.notify).toHaveBeenCalledWith(
      expect.objectContaining({ title: "virtual_adapters_create_failed", intent: "error" }),
    );
  });

  // M3: the poll effect clears `mounted` when the user navigates away, so a
  // 180s create settling afterwards used to strand `busy` and make the whole
  // page read-only until the app restarted.
  it("stays writable when a write settles while the page is in the background", async () => {
    let settleCreate: (rows: HyperVAdapterStatus[]) => void = () => {};
    mocks.create.mockReturnValue(new Promise<HyperVAdapterStatus[]>((resolve) => {
      settleCreate = resolve;
    }));
    const view = renderPage();
    await tick();

    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    expect(mocks.create).toHaveBeenCalledTimes(1);

    // Leave the page: the poll effect cleanup clears the mounted flag.
    mocks.state.pageActive = false;
    view.rerender(<VirtualAdaptersPage />);
    await tick();

    // The elevated call now settles while nobody is looking.
    await act(async () => {
      settleCreate([readyRow]);
    });
    await tick();

    // Coming back must not leave refresh, create and remove permanently dead.
    mocks.state.pageActive = true;
    view.rerender(<VirtualAdaptersPage />);
    await tick();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_refresh" }).hasAttribute("disabled")).toBe(false);
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(false);
    expect(screen.getByRole("button", { name: "virtual_adapters_remove" }).hasAttribute("disabled")).toBe(false);
    expect(screen.queryByText("virtual_adapters_create_banner")).toBeNull();
  });
});

// Go's total budget. hypervCheckBatchCapacity (hyperv_adapter.go:362) fails the
// WHOLE batch when `existing + count > 32`, and Create() runs it inside the
// ledger transaction *before* the elevated script (hyperv_adapter.go:1355), so
// 30 existing + 16 requested used to look valid in the dialog, cost a UAC
// prompt, and came back as one generic batch error the user could not act on.
describe("VirtualAdaptersPage create quota", () => {
  const managedRows = (count: number): HyperVAdapterStatus[] =>
    Array.from({ length: count }, (_, index) => {
      const seq = String(index + 1).padStart(2, "0");
      return adapter({
        name: `HypoMux-vnic-${seq}`,
        interfaceName: `vEthernet (HypoMux-vnic-${seq})`,
        adapterId: `adapter-${seq}`,
        managed: true,
        inPool: false,
      });
    });

  const openCreateForm = () => {
    // A submit the dialog itself refuses (over quota) no longer closes the
    // dialog, and Fluent then exposes two role="dialog" nodes: the modal frame
    // plus the surface rendered inside it. The surface is always the last one in
    // document order and the only one that holds the form.
    const dialogs = screen.queryAllByRole("dialog");
    return dialogs[dialogs.length - 1] as HTMLElement;
  };

  const openCreateDialog = async () => {
    fireEvent.click(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    return openCreateForm();
  };

  const submitCount = async (value: string) => {
    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
  };

  it("states the remaining budget while the ledger is nowhere near the ceiling", async () => {
    mocks.list.mockResolvedValue(managedRows(4));
    renderPage();
    await tick();
    expect(screen.getByText("已创建 4 / 32 张虚拟网卡，本轮最多还能创建 28 张。")).toBeTruthy();
  });

  it("narrows the claim to the real gap once other batches filled the ledger", async () => {
    mocks.list.mockResolvedValue(managedRows(30));
    renderPage();
    await tick();
    expect(screen.getByText("已创建 30 / 32 张虚拟网卡，本轮最多还能创建 2 张。")).toBeTruthy();
  });

  it("says the same thing in English when the locale is en", async () => {
    mocks.state.locale = "en";
    mocks.list.mockResolvedValue(managedRows(30));
    renderPage();
    await tick();
    expect(
      screen.getByText("30 of 32 virtual adapters created — at most 2 more in this batch."),
    ).toBeTruthy();
    expect(screen.queryByText(/本轮最多还能创建/)).toBeNull();
  });

  it("explains at zero remaining why create is unavailable", async () => {
    mocks.list.mockResolvedValue(managedRows(32));
    renderPage();
    await tick();
    expect(screen.getByText("已创建 32 / 32 张虚拟网卡，本轮最多还能创建 0 张。")).toBeTruthy();
    expect(
      screen.getByText("已创建 32 / 32 张虚拟网卡，已达上限，本轮无法再创建。请先删除不再使用的虚拟网卡。"),
    ).toBeTruthy();
  });

  it("recomputes the budget from each poll instead of freezing it at mount", async () => {
    mocks.list.mockResolvedValue(managedRows(4));
    renderPage();
    await tick();
    expect(screen.getByText("已创建 4 / 32 张虚拟网卡，本轮最多还能创建 28 张。")).toBeTruthy();

    // Another batch lands elsewhere while this page is open.
    mocks.list.mockResolvedValue(managedRows(30));
    await tick(3_000);
    expect(screen.getByText("已创建 30 / 32 张虚拟网卡，本轮最多还能创建 2 张。")).toBeTruthy();
    expect(screen.queryByText("已创建 4 / 32 张虚拟网卡，本轮最多还能创建 28 张。")).toBeNull();
  });

  it("claims no budget at all while the list itself is unreadable", async () => {
    mocks.list.mockRejectedValue(new Error("wsl.exe exit 1"));
    renderPage();
    await tick();
    expect(screen.queryByText(/本轮最多还能创建/)).toBeNull();
  });

  // List() also returns read-only rows for Hyper-V objects that follow the naming
  // convention but were never in our ledger (hyperv_adapter.go:1166-1172). Go
  // counts `len(ledger.Adapters)`, so charging those rows here would refuse a
  // create the service accepts.
  it("does not charge read-only foreign rows against the budget", async () => {
    const foreign = [adapter({ name: "HypoMux-vnic-90", adapterId: "foreign-1", managed: false })];
    mocks.list.mockResolvedValue([...managedRows(30), ...foreign]);
    renderPage();
    await tick();
    expect(screen.getByText("已创建 30 / 32 张虚拟网卡，本轮最多还能创建 2 张。")).toBeTruthy();
  });

  it("still creates when the typed count fits the remaining budget", async () => {
    mocks.list.mockResolvedValue(managedRows(30));
    renderPage();
    await tick();
    await submitCount("2");
    expect(mocks.create).toHaveBeenCalledWith("外网交换机", 2);
    expect(mocks.notify).not.toHaveBeenCalledWith(expect.objectContaining({ intent: "error" }));
  });

  it("refuses an over-quota submit before it can reach the service", async () => {
    mocks.list.mockResolvedValue(managedRows(30));
    renderPage();
    await tick();
    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "3" },
    });
    const submit = within(dialog).getByRole("button", { name: "virtual_adapters_create" });
    // 77 §7: the dialog now blocks the batch itself, so the user is told the real
    // gap before clicking anything. No UAC prompt, no fail-closed batch error, and
    // no toast storm — the reason sits right under the field they typed into.
    expect(submit.hasAttribute("disabled")).toBe(true);
    expect(
      within(dialog).getByText("已创建 30 / 32 张虚拟网卡，本轮最多还能创建 2 张。请把创建数量改成 2 以内再提交。"),
    ).toBeTruthy();
    fireEvent.click(submit);
    await tick();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mocks.notify).not.toHaveBeenCalled();
  });

  it("refuses every submit once the budget is exhausted", async () => {
    mocks.list.mockResolvedValue(managedRows(32));
    renderPage();
    await tick();
    // The entry itself is shut, so there is no dialog left to submit from at all.
    const entry = within(toolbar()).getByRole("button", { name: "virtual_adapters_create" });
    expect(entry.hasAttribute("disabled")).toBe(true);
    fireEvent.click(entry);
    await tick();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(
      screen.getByText("已创建 32 / 32 张虚拟网卡，已达上限，本轮无法再创建。请先删除不再使用的虚拟网卡。"),
    ).toBeTruthy();
  });

  // A batch-shaped but out-of-budget count is refused; the 1..16 shape errors the
  // dialog already blocks stay silent, exactly as before this change.
  it("keeps the dialog's own 1..16 rejections silent", async () => {
    mocks.list.mockResolvedValue(managedRows(30));
    renderPage();
    await tick();
    const dialog = await openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "17" },
    });
    expect(within(dialog).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
    expect(mocks.create).not.toHaveBeenCalled();
    expect(mocks.notify).not.toHaveBeenCalled();
  });

  it("frees the budget again as soon as the poll shows a deletion", async () => {
    mocks.list.mockResolvedValue(managedRows(30));
    renderPage();
    await tick();
    const refused = await openCreateDialog();
    fireEvent.change(within(refused).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "3" },
    });
    const submit = within(refused).getByRole("button", { name: "virtual_adapters_create" });
    expect(submit.hasAttribute("disabled")).toBe(true);
    fireEvent.click(submit);
    await tick();
    expect(mocks.create).not.toHaveBeenCalled();
    fireEvent.click(within(refused).getByRole("button", { name: "routing_dialog_cancel" }));
    await tick();

    // Four adapters freed elsewhere; the next poll is enough to make the same
    // count acceptable again.
    mocks.list.mockResolvedValue(managedRows(20));
    await tick(3_000);
    const form = await openCreateDialog();
    fireEvent.change(within(form).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "3" },
    });
    const allowed = within(form).getByRole("button", { name: "virtual_adapters_create" });
    expect(allowed.hasAttribute("disabled")).toBe(false);
    fireEvent.click(allowed);
    await tick();
    expect(mocks.create).toHaveBeenCalledWith("外网交换机", 3);
  });
});

describe("VirtualAdaptersPage remove flow", () => {
  it("removes a registered adapter by name and prunes the row without re-reading the host", async () => {
    mocks.list.mockResolvedValue([readyRow]);
    // The single-card button goes through removeAdapters, not remove(): the
    // latter still carries the HypoMux naming gate and refused hand-built cards
    // ("xuni-05 不符合 HypoMux 命名规范"). Asserting the batch entry point here
    // is what pins that fix in place.
    mocks.removeAdapters.mockResolvedValue([
      { name: "HypoMux vNIC 1", removed: true, reason: "", interface: "vEthernet (HypoMux vNIC 1)" },
    ]);
    renderPage();
    await tick();
    const pollsBefore = mocks.list.mock.calls.length;
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("virtual_adapters_remove_body")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_remove" }));
    await tick();
    expect(mocks.removeAdapters).toHaveBeenCalledWith(["HypoMux vNIC 1"]);
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(mocks.notify).toHaveBeenCalled();
    expect(mocks.notify.mock.calls[0][0]).toMatchObject({ dedupeKey: "virtual-adapters:info:remove" });
    // Local prune, not refresh(): List() cannot reproduce the backend's
    // LastError markers, so a re-read here would wipe the restart hint the
    // Go side put on every row.
    expect(screen.queryByText("HypoMux vNIC 1")).toBeNull();
    expect(mocks.list.mock.calls.length).toBe(pollsBefore);
  });

  // The bug the single-card path had: a card the user built by hand was refused
  // by the naming gate, so the button existed but could never work.
  it("removes an adapter the ledger does not own instead of refusing it on the name", async () => {
    // The exact row and error the user hit: a card named like theirs, not ours.
    mocks.list.mockResolvedValue([
      adapter({ managed: false, name: "xuni-05", interfaceName: "vEthernet (xuni-05)" }),
    ]);
    mocks.removeAdapters.mockResolvedValue([
      { name: "xuni-05", removed: true, reason: "", interface: "vEthernet (xuni-05)" },
    ]);
    renderPage();
    await tick();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("virtual_adapters_remove_foreign_body")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_remove" }));
    await tick();
    expect(mocks.removeAdapters).toHaveBeenCalledWith(["xuni-05"]);
    expect(mocks.notify.mock.calls[0][0]).toMatchObject({ dedupeKey: "virtual-adapters:info:remove" });
  });

  // The single-card prune was by name, so deleting one of two same-named cards
  // took the other one off screen too — a row the user never touched, reporting
  // a deletion that may not have happened. The submitted row key bounds it.
  it("keeps the same-named twin on screen after deleting one card", async () => {
    const twin = adapter({
      adapterId: "adapter-9",
      name: "xuni-05",
      interfaceName: "vEthernet (xuni-05)",
      managed: false,
      macAddress: "00-15-5D-09-09-09",
    });
    mocks.list.mockResolvedValue([
      adapter({ managed: false, name: "xuni-05", interfaceName: "vEthernet (xuni-05)" }),
      twin,
    ]);
    mocks.removeAdapters.mockResolvedValue([
      { name: "xuni-05", removed: true, reason: "", interface: "vEthernet (xuni-05)" },
    ]);
    renderPage();
    await tick();
    // Target the FIRST row only; the twin's button must not be what is clicked.
    fireEvent.click(screen.getAllByRole("button", { name: "virtual_adapters_remove" })[0]);
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "virtual_adapters_remove" }));
    await tick();

    expect(mocks.removeAdapters).toHaveBeenCalledWith(["xuni-05"]);
    // Exactly one card is still listed. The poll decides which object went.
    expect(screen.getAllByText("xuni-05")).toHaveLength(1);
  });

  // Rule change (2026-05, user request): ledger membership no longer decides
  // removability. This test used to assert the opposite ("never offers a delete
  // control for an adapter the ledger does not own"); it was rewritten rather
  // than deleted, and the old title is quoted here on purpose.
  it("offers a delete control for an adapter the ledger does not own", async () => {
    mocks.list.mockResolvedValue([adapter({ managed: false })]);
    renderPage();
    await tick();
    expect(screen.getByText("virtual_adapters_foreign")).toBeTruthy();
    expect(screen.getByRole("button", { name: "virtual_adapters_remove" })).toBeTruthy();
  });

  it("still refuses a delete control for a Hyper-V owned adapter", async () => {
    mocks.list.mockResolvedValue([
      adapter({
        name: "Container NIC 9d844023",
        interfaceName: "vEthernet (Container NIC 9d844023)",
        managed: false,
      }),
    ]);
    renderPage();
    await tick();
    expect(screen.getByText("virtual_adapters_reserved")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "virtual_adapters_remove" })).toBeNull();
  });
});

describe("VirtualAdaptersPage batch removal", () => {
  const foreignRow = adapter({
    adapterId: "adapter-2",
    name: "xuni-01",
    interfaceName: "vEthernet (xuni-01)",
    managed: false,
  });
  const protectedRow = adapter({
    adapterId: "adapter-3",
    name: "Container NIC 9d844023",
    interfaceName: "vEthernet (Container NIC 9d844023)",
    managed: false,
  });
  const rows = [readyRow, foreignRow, protectedRow];
  const removed = (name: string): HyperVRemoveResult => ({
    name,
    removed: true,
    reason: "",
    interface: `vEthernet (${name})`,
  });
  const failed = (name: string, reason: string): HyperVRemoveResult => ({
    name,
    removed: false,
    reason,
    interface: `vEthernet (${name})`,
  });

  /** Enter batch mode and tick the given rows, left to right. */
  const enterAndTick = async (indexes: number[]) => {
    fireEvent.click(screen.getByRole("button", { name: "批量删除" }));
    for (const index of indexes) {
      fireEvent.click(screen.getAllByRole("checkbox")[index]);
    }
  };

  it("round-trips between normal mode and batch mode", async () => {
    mocks.list.mockResolvedValue(rows);
    renderPage();
    await tick();
    // Normal mode: per-row controls, no batch controls.
    expect(screen.getAllByRole("button", { name: "virtual_adapters_remove" })).toHaveLength(2);
    expect(screen.queryByRole("checkbox")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "批量删除" }));
    // The protected row has no checkbox, so there are exactly two.
    expect(screen.getAllByRole("checkbox")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "virtual_adapters_remove" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "routing_dialog_cancel" }));
    expect(screen.queryByRole("checkbox")).toBeNull();
    expect(screen.getByRole("button", { name: "批量删除" })).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "virtual_adapters_remove" })).toHaveLength(2);
  });

  it("shows the tick count and will not submit an empty selection", async () => {
    mocks.list.mockResolvedValue(rows);
    renderPage();
    await tick();
    await enterAndTick([]);
    const submit = () =>
      screen.getByRole("button", { name: /删除所选（\d+）/ });
    // Real language-pack text, so the count the user sees is the count asserted.
    expect(submit().hasAttribute("disabled")).toBe(true);
    expect(submit().textContent).toContain("0");

    fireEvent.click(screen.getAllByRole("checkbox")[1]);
    expect(submit().hasAttribute("disabled")).toBe(false);
    expect(submit().textContent).toContain("1");

    // Un-ticking drops it back to a no-op instead of submitting a stale name.
    fireEvent.click(screen.getAllByRole("checkbox")[1]);
    expect(submit().hasAttribute("disabled")).toBe(true);
    expect(mocks.removeAdapters).not.toHaveBeenCalled();
  });

  // User request 2026-10-04: "点批量删除后，加个全选按钮，没点时不出".
  it("offers select-all only in batch mode and ticks every removable row with it", async () => {
    mocks.list.mockResolvedValue(rows);
    renderPage();
    await tick();
    expect(screen.queryByRole("button", { name: "全选" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "批量删除" }));
    fireEvent.click(screen.getByRole("button", { name: "全选" }));

    // Two of three rows are removable; the count the user sees must be 2, not 3.
    // Ticking the protected row would build a batch the backend refuses row by
    // row, which is the one outcome the confirm dialog exists to prevent.
    const submit = screen.getByRole("button", { name: /删除所选（\d+）/ });
    expect(submit.textContent).toContain("2");
    fireEvent.click(submit);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("HypoMux vNIC 1")).toBeTruthy();
    expect(within(dialog).getByText("xuni-01")).toBeTruthy();
    expect(within(dialog).queryByText("Container NIC 9d844023")).toBeNull();

    fireEvent.click(within(dialog).getByRole("button", { name: /删除所选（\d+）/ }));
    expect(mocks.removeAdapters).toHaveBeenCalledWith(["HypoMux vNIC 1", "xuni-01"]);
  });

  // The entry point reads as "batch delete" because that is what it starts, and
  // because the row-level control is called "remove" — two different verbs for
  // two different scopes on one screen.
  it("labels the batch entry point 批量删除", async () => {
    mocks.list.mockResolvedValue(rows);
    renderPage();
    await tick();
    const entry = screen.getByRole("button", { name: "批量删除" });
    expect(entry).toBeTruthy();
    expect(screen.queryByRole("button", { name: "删除" })).toBeNull();
  });

  it("lists every ticked name in the confirm dialog and sends one batch", async () => {
    mocks.list.mockResolvedValue(rows);
    mocks.removeAdapters.mockResolvedValue([removed("HypoMux vNIC 1"), removed("xuni-01")]);
    renderPage();
    await tick();
    await enterAndTick([0, 1]);

    fireEvent.click(screen.getByRole("button", { name: /删除所选（2）/ }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("HypoMux vNIC 1")).toBeTruthy();
    expect(within(dialog).getByText("xuni-01")).toBeTruthy();
    // One elevated run for both, not N calls.
    expect(within(dialog).getByText(/不是本工具创建|Not created by/)).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("button", { name: /删除所选（2）/ }));
    await tick();
    expect(mocks.removeAdapters).toHaveBeenCalledTimes(1);
    expect(mocks.removeAdapters).toHaveBeenCalledWith(["HypoMux vNIC 1", "xuni-01"]);
    expect(mocks.remove).not.toHaveBeenCalled();
  });

  it("reports a clean batch with its count and prunes the removed rows", async () => {
    mocks.list.mockResolvedValue(rows);
    mocks.removeAdapters.mockResolvedValue([removed("HypoMux vNIC 1"), removed("xuni-01")]);
    renderPage();
    await tick();
    await enterAndTick([0, 1]);
    fireEvent.click(screen.getByRole("button", { name: /删除所选（2）/ }));
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: /删除所选（2）/ }),
    );
    await tick();

    expect(mocks.notify).toHaveBeenCalledTimes(1);
    expect(mocks.notify.mock.calls[0][0]).toMatchObject({
      intent: "success",
      dedupeKey: "virtual-adapters:info:remove-selected",
    });
    expect(String(mocks.notify.mock.calls[0][0].message)).toContain("已删除 2 张虚拟网卡");
    expect(screen.queryByText("HypoMux vNIC 1")).toBeNull();
    expect(screen.queryByText("xuni-01")).toBeNull();
    // The protected row was never in the batch and must survive it.
    expect(screen.getByText("Container NIC 9d844023")).toBeTruthy();
    // The mode closed itself, so the toolbar is back to normal.
    expect(screen.getByRole("button", { name: "批量删除" })).toBeTruthy();
    expect(screen.queryByRole("checkbox")).toBeNull();
  });

  it("keeps a partial failure visible: 2 removed, 1 named with the backend reason", async () => {
    mocks.list.mockResolvedValue(rows);
    mocks.removeAdapters.mockResolvedValue([
      removed("HypoMux vNIC 1"),
      removed("xuni-01"),
      failed("xuni-02", "适配器正被虚拟交换机占用"),
    ]);
    renderPage();
    await tick();
    await enterAndTick([0, 1]);
    fireEvent.click(screen.getByRole("button", { name: /删除所选（2）/ }));
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: /删除所选（2）/ }),
    );
    await tick();

    // A partial batch is NOT a failure: intent is warning, not error, and the
    // successes are still reported as successes.
    expect(mocks.notify).toHaveBeenCalledTimes(1);
    const toast = mocks.notify.mock.calls[0][0];
    expect(toast).toMatchObject({
      intent: "warning",
      dedupeKey: "virtual-adapters:error:remove-selected",
    });
    const message = String(toast.message);
    expect(message).toContain("已删除 2 张虚拟网卡");
    expect(message).toContain("xuni-02");
    // Backend runtime fact: shown verbatim, never through t().
    expect(message).toContain("适配器正被虚拟交换机占用");

    // The two that went are gone; the one that stayed is still on screen with
    // its row, because it was reported as not removed.
    expect(screen.queryByText("HypoMux vNIC 1")).toBeNull();
    expect(screen.queryByText("xuni-01")).toBeNull();
    expect(screen.getByText("Container NIC 9d844023")).toBeTruthy();
  });

  it("routes a whole-batch throw to the error path under its own dedupe key", async () => {
    mocks.list.mockResolvedValue(rows);
    mocks.removeAdapters.mockRejectedValue(new Error("读取 Hyper-V 适配器列表失败"));
    renderPage();
    await tick();
    await enterAndTick([0, 1]);
    fireEvent.click(screen.getByRole("button", { name: /删除所选（2）/ }));
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: /删除所选（2）/ }),
    );
    await tick();

    expect(mocks.notify).toHaveBeenCalledTimes(1);
    expect(mocks.notify.mock.calls[0][0]).toMatchObject({
      intent: "error",
      dedupeKey: "virtual-adapters:error:remove-selected",
    });
    // Nothing was removed, so no row may vanish from the local state.
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();
    expect(screen.getByRole("button", { name: "批量删除" })).toBeTruthy();
  });

  // P0-2: Hyper-V allows two adapters to carry one name, so a name is not a row
  // identity. Ticking was by name (one click lit both rows) and the post-delete
  // prune was by name too (one result erased both rows). Both ends are asserted
  // here on the page that owns the state.
  it("ticks one of two same-named rows, sends the name once, and leaves the twin on screen", async () => {
    const twin = adapter({
      adapterId: "adapter-9",
      name: "xuni-01",
      interfaceName: "vEthernet (xuni-01)",
      managed: false,
      macAddress: "00-15-5D-09-09-09",
    });
    mocks.list.mockResolvedValue([foreignRow, twin]);
    mocks.removeAdapters.mockResolvedValue([removed("xuni-01")]);
    renderPage();
    await tick();

    fireEvent.click(screen.getByRole("button", { name: "批量删除" }));
    const boxes = screen.getAllByRole("checkbox");
    expect(boxes).toHaveLength(2);
    fireEvent.click(boxes[0]);
    // One click, one row: the twin must not light up with it.
    expect((boxes[0] as HTMLInputElement).checked).toBe(true);
    expect((boxes[1] as HTMLInputElement).checked).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: /删除所选（1）/ }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /删除所选（1）/ }));
    await tick();

    // The API takes names and cannot tell the two apart, so ONE name goes up.
    expect(mocks.removeAdapters).toHaveBeenCalledTimes(1);
    expect(mocks.removeAdapters).toHaveBeenCalledWith(["xuni-01"]);
    // The twin the user never ticked is still on screen. The result carries no
    // object identity, so the table prunes only the row we submitted and lets
    // the 3s poll say which object actually went.
    expect(screen.getAllByText("xuni-01")).toHaveLength(1);
  });
});

describe("VirtualAdaptersPage DHCP pacing", () => {
  it("keeps the slow-DHCP hint hidden until 60s have actually elapsed", async () => {
    const dhcpRow = adapter({ adapterId: "adapter-new", state: "creating", address: "" });
    mocks.list.mockResolvedValue([dhcpRow]);
    renderPage();
    await tick();

    expect(screen.queryByText("virtual_adapters_dhcp_slow")).toBeNull();
    // Polls keep landing every 3s; the timer must not be reset by them.
    await tick(30_000);
    expect(screen.queryByText("virtual_adapters_dhcp_slow")).toBeNull();
    await tick(30_000);
    expect(screen.getByText("virtual_adapters_dhcp_slow")).toBeTruthy();
  });
});

describe("VirtualAdaptersPage browser preview", () => {
  it("renders fixtures, shows the preview bar and refuses every write", async () => {
    mocks.state.desktop = false;
    renderPage();
    await tick(3_000);

    expect(screen.getByText("virtual_adapters_preview")).toBeTruthy();
    expect(screen.getByText("HypoMux vNIC 1")).toBeTruthy();
    expect(screen.getByText("HypoMux vNIC 3")).toBeTruthy();
    expect(screen.getByText("CreateVmNetworkAdapter: Access denied (0x80070005)")).toBeTruthy();
    // Preview must never touch the desktop service.
    expect(mocks.list).not.toHaveBeenCalled();
    expect(mocks.switches).not.toHaveBeenCalled();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
    const removeButtons = screen.getAllByRole("button", { name: "virtual_adapters_remove" });
    expect(removeButtons).toHaveLength(3);
    expect(removeButtons.every((button) => button.hasAttribute("disabled"))).toBe(true);
  });
});

// Audit item 1: `notified` was a never-cleared Set wrapped around all four toasts,
// so the second and later create/remove failures produced nothing at all. The
// hard-failure contract (§3.7) keeps whatever already succeeded, so a silent
// retry is how orphaned adapters end up on the host.
describe("VirtualAdaptersPage action-failure notifications", () => {
  const openCreateDialog = async () => {
    fireEvent.click(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    const dialogs = screen.queryAllByRole("dialog");
    return dialogs[dialogs.length - 1] as HTMLElement;
  };

  const submitCreate = async () => {
    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
  };

  it("notifies every failed create instead of latching the first one", async () => {
    mocks.create.mockRejectedValue(new Error("CreateVmNetworkAdapter: Access denied (0x80070005)"));
    renderPage();
    await tick();
    await submitCreate();
    await submitCreate();

    const failures = mocks.notify.mock.calls.filter(
      ([input]) => (input as { dedupeKey?: string }).dedupeKey === "virtual-adapters:error:create",
    );
    expect(failures).toHaveLength(2);
    expect(failures[0][0]).toMatchObject({ intent: "error", title: "virtual_adapters_create_failed" });
  });

  it("notifies every failed remove instead of latching the first one", async () => {
    mocks.removeAdapters.mockRejectedValue(new Error("Remove-VMSwitch: Access denied"));
    renderPage();
    await tick();
    const confirm = async () => {
      fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
      fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "virtual_adapters_remove" }));
      await tick();
    };
    await confirm();
    await confirm();

    const failures = mocks.notify.mock.calls.filter(
      ([input]) => (input as { dedupeKey?: string }).dedupeKey === "virtual-adapters:error:remove",
    );
    expect(failures).toHaveLength(2);
    expect(failures[0][0]).toMatchObject({ intent: "error", title: "virtual_adapters_remove_failed" });
  });

  // Audit item 2: one key used as the title of three different operations, two of
  // which pointed the user the wrong way.
  it("titles each failure for the operation that actually failed", async () => {
    renderPage();
    await tick();

    mocks.list.mockRejectedValue(new Error("wsl.exe exit 1"));
    await tick(3_000);
    expect(mocks.notify).toHaveBeenCalledWith(
      expect.objectContaining({ dedupeKey: "virtual-adapters:error:list", title: "virtual_adapters_read_failed" }),
    );
    // The read latch is still there: a second failed poll must stay quiet.
    const before = mocks.notify.mock.calls.length;
    await tick(3_000);
    expect(mocks.notify.mock.calls.length).toBe(before);
  });
});

// Audit item 8: the two write flows share one `busy`, and nothing pinned that the
// flow started second cannot reach the service while the first is outstanding.
describe("VirtualAdaptersPage write serialization", () => {
  const openCreateDialog = async () => {
    fireEvent.click(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    const dialogs = screen.queryAllByRole("dialog");
    return dialogs[dialogs.length - 1] as HTMLElement;
  };

  it("refuses a remove while an elevated create is still running", async () => {
    let settleCreate: (rows: HyperVAdapterStatus[]) => void = () => {};
    mocks.create.mockReturnValue(new Promise<HyperVAdapterStatus[]>((resolve) => {
      settleCreate = resolve;
    }));
    renderPage();
    await tick();
    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();
    expect(mocks.create).toHaveBeenCalledTimes(1);

    const remove = screen.getAllByRole("button", { name: "virtual_adapters_remove" })[0];
    expect(remove.hasAttribute("disabled")).toBe(true);
    fireEvent.click(remove);
    await tick();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();

    // …and the same page cannot queue a second batch behind the first either.
    const create = within(toolbar()).getByRole("button", { name: "virtual_adapters_create" });
    expect(create.hasAttribute("disabled")).toBe(true);
    fireEvent.click(create);
    await tick();
    expect(mocks.create).toHaveBeenCalledTimes(1);

    settleCreate([]);
    await tick();
    expect(mocks.remove).not.toHaveBeenCalled();
    expect(mocks.create).toHaveBeenCalledTimes(1);
  });

  it("refuses a create while a remove is still running", async () => {
    let settleRemove: () => void = () => {};
    mocks.removeAdapters.mockReturnValue(new Promise((resolve) => {
      settleRemove = () => resolve([{ name: "HypoMux vNIC 1", removed: true, reason: "", interface: "" }]);
    }));
    renderPage();
    await tick();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "virtual_adapters_remove" }));
    await tick();
    expect(mocks.removeAdapters).toHaveBeenCalledWith(["HypoMux vNIC 1"]);

    const create = within(toolbar()).getByRole("button", { name: "virtual_adapters_create" });
    expect(create.hasAttribute("disabled")).toBe(true);
    fireEvent.click(create);
    await tick();
    expect(mocks.create).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();

    settleRemove();
    await tick();
    expect(mocks.create).not.toHaveBeenCalled();
  });
});