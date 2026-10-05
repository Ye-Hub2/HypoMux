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

const panelProps = (patch: Partial<HyperVAdapterPanelProps> = {}): HyperVAdapterPanelProps => ({
  adapters: [],
  switches: [hypervSwitch()],
  loading: false,
  loadFailed: false,
  switchesFailed: false,
  switchesError: null,
  busy: false,
  creating: false,
  creatingCount: 1,
  dhcpSlow: false,
  preview: false,
  pendingRestart: [],
  onDismissRestartHint: noop,
  unavailableReason: null,
  onRefresh: noop,
  onCreate: noop,
  onRemove: noop,
  ...patch,
});

const renderPanel = (patch: Partial<HyperVAdapterPanelProps> = {}) => {
  const props = panelProps(patch);
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

  // Rule change (2026-05, user request): "不要管是不是这个软件创建的虚拟网卡,
  // 能检测到, 能删除就加上移除按钮". Ledger membership no longer decides
  // removability, so the two tests below used to assert the OPPOSITE of the
  // shipped behaviour ("refuses to offer a delete button for them" /
  // "delete only for ledger-registered adapters"). They were rewritten, not
  // deleted, and the old titles are quoted above so the change is auditable.
  it("marks adapters the ledger does not own AND still offers them a delete button", () => {
    renderPanel({ adapters: [adapter({ managed: false })] });
    expect(screen.getByText("virtual_adapters_foreign")).toBeTruthy();
    // The badge now describes the row; it no longer withholds the control.
    expect(screen.getByRole("button", { name: "virtual_adapters_remove" })).toBeTruthy();
  });

  it("offers delete for ledger-registered adapters", () => {
    renderPanel({ adapters: [adapter({ managed: true })] });
    expect(screen.queryByText("virtual_adapters_foreign")).toBeNull();
    expect(screen.getByRole("button", { name: "virtual_adapters_remove" })).toBeTruthy();
  });

  // Restored verbatim: an earlier edit of this file replaced this test instead
  // of appending after it. Nothing in this change touches the DHCP pacing rule.
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

describe("HyperVAdapterPanel removal policy", () => {
  // Both protected shapes, because the mirror in managementAdapters.ts has two
  // rules and a container-only test would let the alias rule rot unnoticed.
  const containerNic = adapter({
    adapterId: "c1",
    name: "Container NIC 9d844023",
    interfaceName: "vEthernet (Container NIC 9d844023)",
    managed: false,
  });
  const defaultSwitch = adapter({
    adapterId: "c2",
    name: "Default Switch",
    interfaceName: "vEthernet (Default Switch)",
    managed: false,
  });
  const foreign = adapter({
    adapterId: "c3",
    name: "xuni-01",
    interfaceName: "vEthernet (xuni-01)",
    managed: false,
  });

  it("gives a protected row no remove button and says it is system reserved", () => {
    renderPanel({ adapters: [containerNic, defaultSwitch] });
    expect(screen.queryByRole("button", { name: "virtual_adapters_remove" })).toBeNull();
    // One badge per protected row, not a silent missing control. The why is a
    // tooltip/accessible name rather than a second visible line, so the row
    // stays one line tall.
    expect(screen.getAllByText("virtual_adapters_reserved")).toHaveLength(2);
    expect(screen.getAllByTitle("virtual_adapters_reserved_hint")).toHaveLength(2);
  });

  it("keeps the delete button on a ledger-external row next to the reserved rows", () => {
    renderPanel({ adapters: [containerNic, foreign, defaultSwitch] });
    expect(screen.getAllByRole("button", { name: "virtual_adapters_remove" })).toHaveLength(1);
    // All three fixtures are outside our ledger, so all three are badged; only
    // the two protected ones are locked.
    expect(screen.getAllByText("virtual_adapters_foreign")).toHaveLength(3);
    expect(screen.getAllByText("virtual_adapters_reserved")).toHaveLength(2);
  });

  it("warns about provenance and identity when the target is not ours", () => {
    renderPanel({ adapters: [foreign] });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    const dialog = screen.getByRole("dialog");
    // The managed-only sentence must NOT be shown for a card we did not create.
    expect(within(dialog).queryByText("virtual_adapters_remove_body")).toBeNull();
    expect(within(dialog).getByText("virtual_adapters_remove_foreign_body")).toBeTruthy();
    // Identity, so "delete this NIC" is unambiguous on a host that has five.
    expect(within(dialog).getByText("vEthernet (xuni-01)")).toBeTruthy();
    expect(within(dialog).getByText("00-15-5D-01-02-03")).toBeTruthy();
    expect(within(dialog).getByText("172.24.8.11/24")).toBeTruthy();
  });

  it("keeps the milder managed-card copy for an adapter from our own ledger", () => {
    const onRemove = vi.fn();
    renderPanel({ adapters: [adapter({ managed: true })], onRemove });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("virtual_adapters_remove_body")).toBeTruthy();
    expect(within(dialog).queryByText("virtual_adapters_remove_foreign_body")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_remove" }));
    expect(onRemove).toHaveBeenCalledTimes(1);
  });
});

describe("HyperVAdapterPanel multi-select", () => {
  // Ticks are row keys, so these rows are ticked below by DeviceId ("a"/"b"/"c")
  // and never by their names. A name-keyed tick used to light every row that
  // happened to carry the same name — which is a real host state, not a fixture
  // artefact, hence the keys here and the duplicate-name case at the bottom of
  // this block.
  const rows = [
    adapter({ adapterId: "a", name: "HypoMux vNIC 1", managed: true }),
    adapter({ adapterId: "b", name: "xuni-01", interfaceName: "vEthernet (xuni-01)", managed: false }),
    adapter({
      adapterId: "c",
      name: "Container NIC 9d844023",
      interfaceName: "vEthernet (Container NIC 9d844023)",
      managed: false,
    }),
  ];

  it("swaps the toolbar for the batch controls and keeps the count in sync", () => {
    const onStartSelection = vi.fn();
    const { rerender, props } = renderPanel({ adapters: rows, onStartSelection });
    expect(screen.getByRole("button", { name: "virtual_adapters_delete" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_delete" }));
    expect(onStartSelection).toHaveBeenCalledTimes(1);

    // N = 0 is "nothing ticked", not an error: the button exists and is off.
    rerender(<HyperVAdapterPanel {...props} selectionMode />);
    const submit = screen.getByRole("button", { name: "virtual_adapters_remove_selected" });
    expect(submit.hasAttribute("disabled")).toBe(true);

    rerender(<HyperVAdapterPanel {...props} selectionMode selectedKeys={["a", "b"]} />);
    const enabled = screen.getByRole("button", { name: "virtual_adapters_remove_selected" });
    expect(enabled.hasAttribute("disabled")).toBe(false);
  });

  it("offers a checkbox only for removable rows", () => {
    renderPanel({ adapters: rows, selectionMode: true });
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes).toHaveLength(2);
    expect(screen.queryByRole("checkbox", { name: /Container NIC/ })).toBeNull();
  });

  // User request 2026-10-04: "点批量删除后…主页那里是点一行就选中了" — the row
  // is the hit target, the 16px checkbox is not. Without this the batch flow
  // needs pixel-accurate clicking that nothing in the UI promises.
  it("toggles a row when the row itself is clicked, not only its checkbox", () => {
    const onToggleSelection = vi.fn();
    renderPanel({ adapters: rows, selectionMode: true, onToggleSelection });
    fireEvent.click(screen.getByText("xuni-01"));
    expect(onToggleSelection).toHaveBeenCalledTimes(1);
    expect(onToggleSelection.mock.calls[0][0]).toMatchObject({ name: "xuni-01" });
  });

  // The checkbox swallows the click; otherwise the event reaches the row handler
  // after the checkbox already toggled it and the two cancel out — a checkbox
  // that visibly snaps on and then leaves the state unchanged.
  it("still toggles exactly once when the click lands on the checkbox", () => {
    const onToggleSelection = vi.fn();
    renderPanel({ adapters: rows, selectionMode: true, onToggleSelection });
    fireEvent.click(screen.getByRole("checkbox", { name: /xuni-01/ }));
    expect(onToggleSelection).toHaveBeenCalledTimes(1);
  });

  // A protected row must not respond to a row click at all: it looks identical
  // to its neighbours, so silently swallowing the click is worse than a control
  // the user can see is absent.
  it("ignores a row click on a protected row", () => {
    const onToggleSelection = vi.fn();
    renderPanel({ adapters: rows, selectionMode: true, onToggleSelection });
    fireEvent.click(screen.getByText("Container NIC 9d844023"));
    expect(onToggleSelection).not.toHaveBeenCalled();
  });

  // Outside selection mode the row is inert: a click there used to mean nothing,
  // and wiring it up would let a click near the remove button tick a card the
  // user never aimed at.
  it("leaves rows inert when selection mode is off", () => {
    const onToggleSelection = vi.fn();
    renderPanel({ adapters: rows, selectionMode: false, onToggleSelection });
    fireEvent.click(screen.getByText("xuni-01"));
    expect(onToggleSelection).not.toHaveBeenCalled();
  });

  it("shows select-all only inside selection mode and asks the page to run it", () => {
    const onSelectAll = vi.fn();
    renderPanel({ adapters: rows, selectionMode: false, onSelectAll });
    expect(screen.queryByRole("button", { name: "virtual_adapters_select_all" })).toBeNull();

    renderPanel({ adapters: rows, selectionMode: true, onSelectAll });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_select_all" }));
    expect(onSelectAll).toHaveBeenCalledTimes(1);
  });

  // Disabled rather than a second press that clears: a button labelled "select
  // all" that empties the selection is a label that lies.
  it("disables select-all once every removable row is already ticked", () => {
    renderPanel({
      adapters: rows,
      selectionMode: true,
      selectedKeys: ["a", "b"],
      onSelectAll: vi.fn(),
    });
    expect(screen.getByRole("button", { name: "virtual_adapters_select_all" }).hasAttribute("disabled")).toBe(true);
  });

  // Protected rows are not removable, so "select all" must not tick them — a
  // batch the backend refuses row by row is exactly what this dialog prevents.
  it("disables select-all when nothing is removable", () => {
    renderPanel({
      adapters: [adapter({ name: "Container NIC 9d844023", interfaceName: "vEthernet (Container NIC 9d844023)" })],
      selectionMode: true,
      onSelectAll: vi.fn(),
    });
    expect(screen.getByRole("button", { name: "virtual_adapters_select_all" }).hasAttribute("disabled")).toBe(true);
  });

  it("lists every selected name in the confirm dialog and warns about the foreign ones", () => {
    const onRemoveSelected = vi.fn();
    renderPanel({
      adapters: rows,
      selectionMode: true,
      selectedKeys: ["a", "b"],
      onRemoveSelected,
    });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove_selected" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText("HypoMux vNIC 1")).toBeTruthy();
    expect(within(dialog).getByText("xuni-01")).toBeTruthy();
    // Exactly one provenance marker: only xuni-01 is not ours.
    expect(within(dialog).getByText("virtual_adapters_foreign")).toBeTruthy();
    expect(within(dialog).getByText("virtual_adapters_remove_selected_warning")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_remove_selected" }));
    expect(onRemoveSelected).toHaveBeenCalledTimes(1);
  });

  it("closes the confirm dialog without asking the page to delete anything", () => {
    const onRemoveSelected = vi.fn();
    renderPanel({
      adapters: rows,
      selectionMode: true,
      selectedKeys: ["a"],
      onRemoveSelected,
    });
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove_selected" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "routing_dialog_cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(onRemoveSelected).not.toHaveBeenCalled();
  });

  it("leaves the mode through the toolbar cancel", () => {
    const onExitSelection = vi.fn();
    renderPanel({ adapters: rows, selectionMode: true, selectedKeys: ["b"], onExitSelection });
    fireEvent.click(screen.getByRole("button", { name: "routing_dialog_cancel" }));
    expect(onExitSelection).toHaveBeenCalledTimes(1);
    // selectionMode is a controlled prop, so the panel only asks the page to
    // leave; it must not open the batch dialog on the way out. The page-level
    // test covers the actual return to normal mode.
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("ignores a tick that is no longer removable instead of sending it to the backend", () => {
    renderPanel({
      adapters: rows,
      selectionMode: true,
      // "Container NIC 9d844023" was ticked before it turned out to be
      // protected; the count and the dialog must both drop it.
      selectedKeys: ["a", "c"],
    });
    const list = screen.getByRole("table");
    expect(within(list).getAllByRole("checkbox")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove_selected" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).queryByText("Container NIC 9d844023")).toBeNull();
  });

  // P0-2: Hyper-V holds two adapters under one name, and the tick used to be
  // the name — so one click lit both checkboxes and the confirm dialog then
  // said the user had picked two. Identity is the DeviceId.
  it("ticks one row only when two adapters share a name", () => {
    const twins = [
      adapter({ adapterId: "a", name: "xuni-01", interfaceName: "vEthernet (xuni-01)", managed: false }),
      adapter({ adapterId: "a2", name: "xuni-01", interfaceName: "vEthernet (xuni-01)", managed: false }),
    ];
    renderPanel({ adapters: twins, selectionMode: true, selectedKeys: ["a"] });
    // Both checkboxes carry the same label, because the label is the name.
    const boxes = within(screen.getByRole("table")).getAllByRole("checkbox", { name: /xuni-01/ });
    expect(boxes).toHaveLength(2);
    expect((boxes[0] as HTMLInputElement).checked).toBe(true);
    expect((boxes[1] as HTMLInputElement).checked).toBe(false);
  });

  // Same host state, other half of the bug: a name-keyed dialog matched BOTH
  // same-named rows, so the count and the list lied about what was submitted.
  it("counts one row when the ticked key matches one of two same-named rows", () => {
    const twins = [
      adapter({ adapterId: "a", name: "xuni-01", interfaceName: "vEthernet (xuni-01)", managed: false }),
      adapter({ adapterId: "a2", name: "xuni-01", interfaceName: "vEthernet (xuni-01)", managed: false }),
    ];
    renderPanel({ adapters: twins, selectionMode: true, selectedKeys: ["a"] });
    expect(screen.getByRole("button", { name: "virtual_adapters_remove_selected" }).hasAttribute("disabled")).toBe(
      false,
    );
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove_selected" }));
    const dialog = screen.getByRole("dialog");
    // One entry, not two — even though the text reads the same twice.
    expect(within(dialog).getAllByText("xuni-01")).toHaveLength(1);
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

// §3.6: the outbound pool is written in a background goroutine *after*
// Create() returns, so "restart the aggregation" used to be asserted
// unconditionally on every successful batch — a prompt that was either wrong
// (the write already landed) or silent (it never will). The page now re-derives
// which of this session's adapters still claim pool membership on every poll and
// hands that list down; the panel only renders it.
describe("HyperVAdapterPanel restart prompt", () => {
  it("stays hidden while the page reports no outstanding adapter", () => {
    renderPanel({ adapters: [adapter()] });
    expect(screen.queryByText("virtual_adapters_restart_pending")).toBeNull();
  });

  it("renders the restart hint outside the create dialog and dismisses it", () => {
    const onDismissRestartHint = vi.fn();
    renderPanel({
      adapters: [adapter()],
      creating: false,
      pendingRestart: ["HypoMux vNIC 1"],
      onDismissRestartHint,
    });
    // One copy in the banner area, not inside any dialog.
    expect(screen.getAllByText("virtual_adapters_restart_pending")).toHaveLength(1);
    expect(screen.queryByRole("dialog")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "routing_dialog_cancel" }));
    expect(onDismissRestartHint).toHaveBeenCalled();
  });

  it("shows the banner alongside the in-flight batch without hiding either", () => {
    renderPanel({ adapters: [adapter({ state: "creating", address: "" })], creating: true, creatingCount: 3, pendingRestart: ["HypoMux vNIC 1"] });
    expect(screen.getByText("virtual_adapters_create_banner")).toBeTruthy();
    expect(screen.getByText("virtual_adapters_restart_pending")).toBeTruthy();
  });

  // A render must never invent the prompt: an empty list is not a settled list.
  it("drops the prompt as soon as the page empties the list", () => {
    const { rerender, props } = renderPanel({ pendingRestart: ["HypoMux vNIC 1"] });
    expect(screen.getByText("virtual_adapters_restart_pending")).toBeTruthy();
    rerender(<HyperVAdapterPanel {...props} pendingRestart={[]} />);
    expect(screen.queryByText("virtual_adapters_restart_pending")).toBeNull();
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

  it("shows the service's cause alongside the generic hint", () => {
    // The generic hint only says "installed? elevated?"; the caller passes the
    // real message so the user can tell a missing Hyper-V from a denied read.
    const cause = "无法读取 Hyper-V 虚拟交换机：PowerShell 以退出码 1 结束";
    renderPanel({ adapters: [adapter()], switches: [], switchesFailed: true, switchesError: cause });
    expect(screen.getByText("virtual_adapters_switch_failed")).toBeTruthy();
    expect(screen.getByText(cause)).toBeTruthy();
  });

  it("renders the hint alone when no cause was captured", () => {
    renderPanel({ adapters: [adapter()], switches: [], switchesFailed: true, switchesError: null });
    expect(screen.getByText("virtual_adapters_switch_failed")).toBeTruthy();
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

// An elevated create holds `busy` for up to 180s (HYPERV_ADAPTER_CREATE_TIMEOUT_MS).
// Nothing below is about a slow machine: it is about proving the page cannot be
// talked into a second concurrent write while that one is outstanding.
describe("HyperVAdapterPanel busy gate", () => {
  it("locks the toolbar entries and refuses their clicks", () => {
    const onRefresh = vi.fn();
    const onCreate = vi.fn();
    renderPanel({ busy: true, onRefresh, onCreate });

    const refresh = within(toolbar()).getByRole("button", { name: "virtual_adapters_refresh" });
    expect(refresh.hasAttribute("disabled")).toBe(true);
    fireEvent.click(refresh);
    expect(onRefresh).not.toHaveBeenCalled();

    const create = within(toolbar()).getByRole("button", { name: "virtual_adapters_create" });
    expect(create.hasAttribute("disabled")).toBe(true);
    fireEvent.click(create);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("locks the create dialog that is already open when busy flips", () => {
    const onCreate = vi.fn();
    const props = panelProps({ onCreate });
    const { rerender } = render(<HyperVAdapterPanel {...props} />);
    openCreateDialog();

    rerender(<HyperVAdapterPanel {...props} busy />);
    const dialog = screen.getByRole("dialog");
    const cancel = within(dialog).getByRole("button", { name: "routing_dialog_cancel" });
    const submit = within(dialog).getByRole("button", { name: "virtual_adapters_create" });
    expect(cancel.hasAttribute("disabled")).toBe(true);
    expect(submit.hasAttribute("disabled")).toBe(true);
    fireEvent.click(cancel);
    fireEvent.click(submit);
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("locks the remove dialog that is already open when busy flips", () => {
    const onRemove = vi.fn();
    const props = panelProps({ adapters: [adapter({ managed: true })], onRemove });
    const { rerender } = render(<HyperVAdapterPanel {...props} />);
    fireEvent.click(screen.getByRole("button", { name: "virtual_adapters_remove" }));

    rerender(<HyperVAdapterPanel {...props} busy />);
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "virtual_adapters_remove" });
    expect(confirm.hasAttribute("disabled")).toBe(true);
    fireEvent.click(confirm);
    expect(onRemove).not.toHaveBeenCalled();
  });

  it("wires the toolbar refresh through to the page's read", () => {
    const onRefresh = vi.fn();
    renderPanel({ onRefresh });
    fireEvent.click(within(toolbar()).getByRole("button", { name: "virtual_adapters_refresh" }));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });
});

// Audit item 4: the two Retry buttons used to be the one control a panicking user
// could spam, and the read behind them had no in-flight gate at all.
describe("HyperVAdapterPanel retry gate", () => {
  it("shows a retrying state and swallows the second click on the list banner", () => {
    const onRefresh = vi.fn();
    renderPanel({ loadFailed: true, refreshing: true, onRefresh });
    const retry = screen.getByRole("button", { name: "virtual_adapters_retrying" });
    expect(retry.hasAttribute("disabled")).toBe(true);
    fireEvent.click(retry);
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("gates the switch banner the same way", () => {
    const onRefresh = vi.fn();
    renderPanel({ switchesFailed: true, switchesError: "Get-VMSwitch: Access denied", refreshing: true, onRefresh });
    const retry = screen.getByRole("button", { name: "virtual_adapters_retrying" });
    expect(retry.hasAttribute("disabled")).toBe(true);
    fireEvent.click(retry);
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("goes back to an actionable retry once the read settles", () => {
    const onRefresh = vi.fn();
    renderPanel({ loadFailed: true, refreshing: false, onRefresh });
    const retry = screen.getByRole("button", { name: "virtual_adapters_retry" });
    expect(retry.hasAttribute("disabled")).toBe(false);
    fireEvent.click(retry);
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });
});

// 77 §7: the budget used to be enforced on the page only, so the dialog took a
// valid-looking batch and the toolbar entry stayed clickable at zero remaining.
describe("HyperVAdapterPanel create budget", () => {
  it("shuts the toolbar entry once the ledger is full", () => {
    renderPanel({ quota: { used: 32, total: 32, remaining: 0, perSubmitMax: 16, exhausted: true } });
    const create = within(toolbar()).getByRole("button", { name: "virtual_adapters_create" });
    expect(create.hasAttribute("disabled")).toBe(true);
    fireEvent.click(create);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("explains the real gap inside the dialog and refuses the batch there", () => {
    const onCreate = vi.fn();
    renderPanel({ quota: { used: 30, total: 32, remaining: 2, perSubmitMax: 16, exhausted: false }, onCreate });
    const dialog = openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "3" },
    });
    const submit = within(dialog).getByRole("button", { name: "virtual_adapters_create" });
    expect(submit.hasAttribute("disabled")).toBe(true);
    expect(within(dialog).getByText("virtual_adapters_quota_exceeded")).toBeTruthy();
    fireEvent.click(submit);
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("still allows a batch that fits without saying anything about the budget", () => {
    const onCreate = vi.fn();
    renderPanel({ quota: { used: 30, total: 32, remaining: 2, perSubmitMax: 16, exhausted: false }, onCreate });
    const dialog = openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "2" },
    });
    expect(within(dialog).queryByText("virtual_adapters_quota_exceeded")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    expect(onCreate).toHaveBeenCalledWith("外网交换机", 2);
  });

  it("keeps the dialog's own 1..16 rules when no quota is passed at all", () => {
    const onCreate = vi.fn();
    renderPanel({ onCreate });
    const dialog = openCreateDialog();
    fireEvent.change(within(dialog).getByRole("spinbutton", { name: "virtual_adapters_count" }), {
      target: { value: "16" },
    });
    expect(within(dialog).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(false);
    expect(within(dialog).queryByText("virtual_adapters_quota_exceeded")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "virtual_adapters_create" }));
    expect(onCreate).toHaveBeenCalledWith("外网交换机", 16);
  });
});

// Audit item 3: "no external switch" and "external switches exist but none of them
// allows the management OS" are different mistakes with different fixes.
describe("HyperVAdapterPanel switch availability", () => {
  it("names AllowManagementOS when every external switch has it off", () => {
    renderPanel({ switches: [hypervSwitch({ allowManagementOs: false })] });
    expect(screen.getByText("virtual_adapters_switch_management_os_off")).toBeTruthy();
    expect(screen.queryByText("virtual_adapters_switch_external_only")).toBeNull();
    expect(within(toolbar()).getByRole("button", { name: "virtual_adapters_create" }).hasAttribute("disabled")).toBe(true);
  });

  it("tells the two cases apart when an internal switch is in the mix", () => {
    renderPanel({ switches: [hypervSwitch({ type: "Internal", allowManagementOs: false })] });
    expect(screen.getByText("virtual_adapters_switch_external_only")).toBeTruthy();
    expect(screen.queryByText("virtual_adapters_switch_management_os_off")).toBeNull();
  });

  it("still points at the switch list when the host has none at all", () => {
    renderPanel({ switches: [] });
    expect(screen.getByText("virtual_adapters_no_switch")).toBeTruthy();
  });
});

// Audit item 6: Go now ships machine-readable markers. Only the two it actually
// emits may be translated; everything else is backend truth and stays verbatim.
describe("HyperVAdapterPanel backend hints", () => {
  it("renders the pool-restart marker as copy on a healthy row", () => {
    renderPanel({ adapters: [adapter({ state: "ready", inPool: true, lastError: "hypomux.hint.pool_restart" })] });
    expect(screen.getByText("virtual_adapters_hint_pool_restart")).toBeTruthy();
    expect(screen.queryByText("hypomux.hint.pool_restart")).toBeNull();
  });

  it("keeps the backend-only detail as secondary text under the copy", () => {
    renderPanel({
      adapters: [adapter({ state: "ready", inPool: true, lastError: "hypomux.hint.pool_update_failed | 更新出口池失败：拒绝访问" })],
    });
    expect(screen.getByText("virtual_adapters_hint_pool_update_failed")).toBeTruthy();
    expect(screen.getByText("更新出口池失败：拒绝访问")).toBeTruthy();
  });

  it("shows an unknown marker verbatim instead of guessing a translation", () => {
    renderPanel({ adapters: [adapter({ state: "failed", lastError: "hypomux.hint.brand_new_code" })] });
    expect(screen.getByText("hypomux.hint.brand_new_code")).toBeTruthy();
  });

  it("never translates free text — a DHCP reason is the only diagnosis there is", () => {
    renderPanel({ adapters: [adapter({ state: "failed", lastError: "DhcpClient: timeout after 120s" })] });
    expect(screen.getByText("DhcpClient: timeout after 120s")).toBeTruthy();
  });

  it("renders the marker even though the adapter itself is fine", () => {
    // The pool_update_failed marker deliberately survives on a `ready` row — a
    // `failed` row would have hidden it behind the failure the user is already
    // looking at.
    renderPanel({ adapters: [adapter({ state: "ready", inPool: false, lastError: "hypomux.hint.pool_update_failed" })] });
    expect(screen.getByText("virtual_adapters_hint_pool_update_failed")).toBeTruthy();
  });
});