// @vitest-environment jsdom

import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { HyperVAdapterStatus, HyperVSwitch } from "../platform/services";
import { VirtualAdaptersPage } from "./VirtualAdaptersPage";

const mocks = vi.hoisted(() => ({
  state: { pageActive: true, desktop: true },
  list: vi.fn(),
  switches: vi.fn(),
  create: vi.fn(),
  remove: vi.fn(),
  notify: vi.fn(),
}));

// The real module pulls in generated Wails bindings; the page only needs the
// four calls plus the three timeout budgets the wiring freeze exported.
vi.mock("../platform/services", () => ({
  appServices: {
    virtualAdapters: {
      list: mocks.list,
      switches: mocks.switches,
      create: mocks.create,
      remove: mocks.remove,
    },
  },
  withServiceTimeout: <T,>(request: Promise<T>) => request,
  HYPERV_ADAPTER_READ_TIMEOUT_MS: 10_000,
  HYPERV_ADAPTER_WRITE_TIMEOUT_MS: 60_000,
  HYPERV_ADAPTER_CREATE_TIMEOUT_MS: 180_000,
}));

vi.mock("../platform/runtime", () => ({
  isDesktopRuntime: () => mocks.state.desktop,
}));

vi.mock("../i18n/i18n", () => ({
  useI18n: () => ({ locale: "zh", t: (key: string) => key }),
}));

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
  mocks.list.mockReset().mockResolvedValue([readyRow]);
  mocks.switches.mockReset().mockResolvedValue([hypervSwitch()]);
  mocks.create.mockReset().mockResolvedValue([readyRow]);
  mocks.remove.mockReset().mockResolvedValue(undefined);
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

  // M2: Go's "restart the aggregation engine" hint rides in LastError on
  // non-failed rows (hyperv_adapter.go:1238) while the row renderer ignores
  // LastError there — complementary conditions, so it never displayed. The page
  // owns the prompt (contract §3.6's required next action).
  it("prompts for an aggregation restart after a successful create", async () => {
    const batchRow = adapter({ name: "HypoMux vNIC 5", adapterId: "adapter-5", state: "ready", address: "172.24.8.21" });
    mocks.create.mockResolvedValue([batchRow]);
    renderPage();
    await tick();
    expect(screen.queryByText("virtual_adapters_restart_hint")).toBeNull();

    const dialog = await openCreateDialog();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    await tick();

    // In the banner area, not inside the (already closed) create dialog.
    expect(screen.queryByRole("dialog")).toBeNull();
    const banner = document.querySelector(".virtual-adapters-restart") as HTMLElement;
    expect(banner).toBeTruthy();
    expect(within(banner).getByText("virtual_adapters_restart_hint")).toBeTruthy();
    expect(mocks.notify).toHaveBeenCalledWith(
      expect.objectContaining({ dedupeKey: "virtual-adapters:info:create-done", intent: "info" }),
    );

    // Dismissible by the user, per the audit's requirement.
    fireEvent.click(within(banner).getByRole("button", { name: "routing_dialog_cancel" }));
    expect(screen.queryByText("virtual_adapters_restart_hint")).toBeNull();
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

describe("VirtualAdaptersPage remove flow", () => {
  it("removes a registered adapter by name and refreshes", async () => {
    mocks.list.mockResolvedValue([readyRow]);
    renderPage();
    await tick();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("virtual_adapters_remove_body")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_remove" }));
    await tick();
    expect(mocks.remove).toHaveBeenCalledWith("HypoMux vNIC 1");
    expect(mocks.notify).toHaveBeenCalled();
    expect(mocks.notify.mock.calls[0][0]).toMatchObject({ dedupeKey: "virtual-adapters:info:remove" });
  });

  it("never offers a delete control for an adapter the ledger does not own", async () => {
    mocks.list.mockResolvedValue([adapter({ managed: false })]);
    renderPage();
    await tick();
    expect(screen.getByText("virtual_adapters_foreign")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "virtual_adapters_remove" })).toBeNull();
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