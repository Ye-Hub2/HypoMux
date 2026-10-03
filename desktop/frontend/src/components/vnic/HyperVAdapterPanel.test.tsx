// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import type { HyperVAdapterStatus, HyperVSwitch } from "../../platform/services";
import { HyperVAdapterPanel, type HyperVAdapterPanelProps } from "./HyperVAdapterPanel";

// t() is stubbed to echo the key, so every assertion below pins the exact i18n
// key the row renders. A key rename breaks these tests instead of silently
// shipping an English string into the zh locale.
vi.mock("../../i18n/i18n", () => ({
  useI18n: () => ({ locale: "zh", t: (key: string) => key }),
}));

// FluentUI MessageBar/Dialog measure their trigger; jsdom has no ResizeObserver.
beforeAll(() => {
  (globalThis as unknown as { ResizeObserver: unknown }).ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

afterEach(cleanup);

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

const noop = () => {};

const renderPanel = (patch: Partial<HyperVAdapterPanelProps> = {}) => {
  const props: HyperVAdapterPanelProps = {
    adapters: [],
    switches: [hypervSwitch()],
    loading: false,
    loadFailed: false,
    switchesFailed: false,
    busy: false,
    creating: false,
    creatingCount: 1,
    dhcpSlow: false,
    preview: false,
    restartRequired: false,
    onDismissRestartHint: noop,
    unavailableReason: null,
    onRefresh: noop,
    onCreate: noop,
    onRemove: noop,
    ...patch,
  };
  return { props, ...render(<HyperVAdapterPanel {...props} />) };
};

const openCreateDialog = () => {
  // The empty state renders a second Create button, so the trigger is scoped
  // to the toolbar to keep this deterministic.
  fireEvent.click(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }));
  return screen.getByRole("dialog");
};

const toolbar = () => document.querySelector(".virtual-adapters-toolbar") as HTMLElement;

describe("HyperVAdapterPanel state mapping", () => {
  it("renders every derived state with its own key", () => {
    renderPanel({
      adapters: [
        adapter({ adapterId: "a", state: "creating", address: "" }),
        adapter({ adapterId: "b", state: "creating", address: "10.0.0.9" }),
        adapter({ adapterId: "c", state: "ready", address: "10.0.0.5" }),
        adapter({ adapterId: "d", state: "failed", address: "", lastError: "Hyper-V 模块加载失败" }),
        adapter({ adapterId: "e", state: "absent", address: "" }),
      ],
    });
    expect(screen.getByText("virtual_adapters_state_dhcp")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_state_creating")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_state_ready")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_state_failed")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_state_unknown")).toBeTruthy();
  });

  it("shows the lease details and lastError on the rows that have them", () => {
    renderPanel({
      adapters: [
        adapter({ adapterId: "c", state: "ready", address: "10.0.0.5", prefixLength: 24, macAddress: "00-15-5D-0A-0B-0C" }),
        adapter({ adapterId: "d", state: "failed", address: "", lastError: "Hyper-V 模块加载失败" }),
      ],
    });
    expect(screen.getByText("10.0.0.5/24")).toBeTruthy();
    expect(screen.getByText("00-15-5D-0A-0B-0C")).toBeTruthy();
    expect(screen.getByText("00-15-5D-01-02-03")).toBeTruthy();
    // A row with no address must show a dash, never an invented lease.
    expect(screen.getByText("—")).toBeTruthy();
    expect(screen.getByText("Hyper-V 模块加载失败")).toBeTruthy();
  });

  it("never claims pool membership unless inPool is exactly true", () => {
    const { rerender, props } = renderPanel({ adapters: [adapter({ inPool: false })] });
    expect(screen.queryByText("virtual_adapters_pool")).toBeNull();
    rerender(<HyperVAdapterPanel {...props} adapters={[adapter({ inPool: true })]} />);
    expect(screen.getByText("virtual_adapters_pool")).toBeTruthy();
  });

  it("marks unmanaged adapters and refuses to offer a delete button for them", () => {
    renderPanel({ adapters: [adapter({ managed: false })] });
    expect(screen.getByText("virtual_adapters_foreign")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "virtual_adapters_remove" })).toBeNull();
  });

  it("offers delete only for ledger-registered adapters", () => {
    renderPanel({ adapters: [adapter({ managed: true })] });
    expect(screen.queryByText("virtual_adapters_foreign")).toBeNull();
    expect(screen.getByRole("button", { name: "virtual_adapters_remove" })).toBeTruthy();
  });

  it("explains a stalled DHCP wait only once the page flags it as slow", () => {
    const { rerender, props } = renderPanel({
      adapters: [adapter({ state: "creating", address: "" })],
      dhcpSlow: false,
    });
    expect(screen.queryByText("virtual_adapters_dhcp_slow")).toBeNull();
    rerender(<HyperVAdapterPanel {...props} dhcpSlow />);
    expect(screen.getByText("virtual_adapters_dhcp_slow")).toBeTruthy();
  });
});

describe("HyperVAdapterPanel toolbar and messages", () => {
  it("summarises ready rows and the 32-card ceiling", () => {
    renderPanel({ adapters: [adapter({ adapterId: "a" }), adapter({ adapterId: "b", state: "failed", address: "" })] });
    expect(screen.getByText("virtual_adapters_summary")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_total_hint")).toBeTruthy();
  });

  it("shows a progress banner while a create batch is unsettled", () => {
    renderPanel({ creating: true, creatingCount: 4 });
    expect(screen.getByText("virtual_adapters_create_banner")).toBeTruthy();
  });

  it("surfaces load failure with a retry button", () => {
    const onRefresh = vi.fn();
    renderPanel({ loadFailed: true, unavailableReason: "wsl.exe exit 1", onRefresh });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_retry" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("warns and disables create when no switch can be listed", () => {
    renderPanel({ switches: [] });
    expect(screen.getByText("virtual_adapters_no_switch")).toBeTruthy();
    const createButtons = screen.getAllByRole("button", { name: "virtual_adapters_create" });
    expect(createButtons.length).toBeGreaterThan(0);
    expect(createButtons.every((button) => button.hasAttribute("disabled"))).toBe(true);
  });

  it("says only External switches qualify when non-External ones exist", () => {
    renderPanel({ switches: [hypervSwitch({ name: "内网", type: "Internal" })] });
    expect(screen.getByText("virtual_adapters_switch_external_only")).toBeTruthy();
    expect(screen.queryByText("virtual_adapters_no_switch")).toBeNull();
  });

  it("stays silent about switches when the query itself failed", () => {
    renderPanel({ switches: [], switchesFailed: true });
    expect(screen.queryByText("virtual_adapters_no_switch")).toBeNull();
    expect(screen.queryByText("virtual_adapters_switch_external_only")).toBeNull();
  });

  it("renders the empty and loading states separately", () => {
    const { rerender, props } = renderPanel({ adapters: [] });
    expect(screen.getByText("virtual_adapters_empty")).toBeTruthy();
    rerender(<HyperVAdapterPanel {...props} loading />);
    expect(screen.getByText("virtual_adapters_loading")).toBeTruthy();
  });

  it("disables every write in preview mode and says so", () => {
    renderPanel({ adapters: [adapter({ managed: true })], preview: true });
    expect(screen.getByText("virtual_adapters_preview")).toBeTruthy();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "virtual_adapters_remove" }).hasAttribute("disabled")).toBe(true);
  });
});

// M2: Go writes "restart the aggregation engine" into LastError on non-failed
// rows (hyperv_adapter.go:1238) while the row renderer ignores LastError there,
// so the two conditions are complementary and the hint never rendered. The
// page owns the prompt and hands it down instead.
describe("HyperVAdapterPanel restart prompt", () => {
  it("stays hidden until a create has succeeded", () => {
    renderPanel({ adapters: [adapter()] });
    expect(screen.queryByText("virtual_adapters_restart_hint")).toBeNull();
  });

  it("renders the restart hint outside the create dialog and dismisses it", () => {
    const onDismissRestartHint = vi.fn();
    renderPanel({
      adapters: [adapter()],
      creating: false,
      restartRequired: true,
      onDismissRestartHint,
    });
    // One copy in the banner area, not inside any dialog.
    expect(screen.getAllByText("virtual_adapters_restart_hint")).toHaveLength(1);
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "routing_dialog_cancel" }));
    expect(onDismissRestartHint).toHaveBeenCalled();
  });

  it("shows the banner alongside the in-flight batch without hiding either", () => {
    renderPanel({ adapters: [adapter({ state: "creating", address: "" })], creating: true, creatingCount: 3, restartRequired: true });
    expect(screen.getByText("virtual_adapters_create_banner")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_restart_hint")).toBeTruthy();
  });
});

// M4: a failed switches() read used to only suppress the hint, leaving a
// greyed-out Create button and no explanation at all.
describe("HyperVAdapterPanel switch read failure", () => {
  it("explains a failed switches() read and offers a retry", () => {
    const onRefresh = vi.fn();
    renderPanel({ adapters: [adapter()], switches: [], switchesFailed: true, onRefresh });
    expect(screen.getByText("virtual_adapters_switch_failed")).toBeTruthy();
    // The failure must not masquerade as "this host has no external switch".
    expect(screen.queryByText("virtual_adapters_no_switch")).toBeNull();
    expect(screen.queryByText("virtual_adapters_switch_external_only")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_retry" }));
    expect(onRefresh).toHaveBeenCalled();
  });

  it("still disables create, because the list behind it is unusable", () => {
    renderPanel({ switches: [], switchesFailed: true });
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
  });
});

describe("HyperVAdapterPanel create dialog", () => {
  it("opens with a manual count of 1 and the first selectable External switch", () => {
    const onCreate = vi.fn();
    renderPanel({
      switches: [hypervSwitch({ name: "内网", type: "Internal" }), hypervSwitch({ name: "外网-A" })],
      onCreate,
    });
    const dialog = openCreateDialog();
    expect(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" })).toHaveProperty("value", "1");
    expect(within(dialog).getByText("virtual_adapters_restart_hint")).toBeTruthy();
  });

  it("rejects 0 and 17 but accepts 16", () => {
    const onCreate = vi.fn();
    renderPanel({ onCreate });
    const countInput = () => screen.getByRole("spinbutton", { name: "virtual_adapters_count" });
    const submit = () => within(screen.getByRole("dialog")).getByRole("button", { name: "virtual_adapters_create" });

    openCreateDialog();
    fireEvent.change(countInput(), { target: { value: "0" } });
    expect(submit().hasAttribute("disabled")).toBe(true);
    expect(onCreate).not.toHaveBeenCalled();

    fireEvent.change(countInput(), { target: { value: "17" } });
    expect(submit().hasAttribute("disabled")).toBe(true);
    expect(onCreate).not.toHaveBeenCalled();

    fireEvent.change(countInput(), { target: { value: "16" } });
    expect(submit().hasAttribute("disabled")).toBe(false);
    fireEvent.click(submit());
    expect(onCreate).toHaveBeenCalledWith("外网交换机", 16);
  });

  it("keeps the dialog closed when the count is invalid", () => {
    const onCreate = vi.fn();
    renderPanel({ onCreate });
    openCreateDialog();
    fireEvent.change(screen.getByRole("spinbutton", { name: "virtual_adapters_count" }), { target: { value: "abc" } });
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(onCreate).not.toHaveBeenCalled();
  });
});

describe("HyperVAdapterPanel remove dialog", () => {
  it("states the IP quota is reclaimed and calls back with the row", () => {
    const onRemove = vi.fn();
    renderPanel({ adapters: [adapter({ managed: true })], onRemove });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("virtual_adapters_remove_title")).toBeTruthy();
    expect(within(dialog).getByText("virtual_adapters_remove_body")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_remove" }));
    expect(onRemove).toHaveBeenCalledWith(expect.objectContaining({ name: "HypoMux vNIC 1" }));
  });

  it("does nothing when the confirmation is dismissed", () => {
    const onRemove = vi.fn();
    renderPanel({ adapters: [adapter({ managed: true })], onRemove });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "routing_dialog_cancel" }));
    expect(onRemove).not.toHaveBeenCalled();
  });
});