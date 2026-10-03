import { describe, expect, it } from "vitest";
import type { HyperVAdapterStatus, HyperVSwitch } from "../../platform/services";
import {
  adapterRowKey,
  deriveAdapterState,
  describeSwitch,
  externalSwitches,
  findSelectableSwitch,
  formatAddressPrefix,
  formatLastError,
  formatMac,
  isCreatingRow,
  isExternalSwitch,
  isInOutboundPool,
  isManagedAdapter,
  isSwitchSelectable,
  mergeAdapterBatch,
  summarizeAdapters,
  validateCreateCount,
  VIRTUAL_ADAPTER_MAX_BATCH,
  VIRTUAL_ADAPTER_MAX_TOTAL,
} from "./managementAdapters";

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
  inPool: true,
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

// reports/vnic/70-frozen-hyperv-interface.md §3.2 only guarantees these four
// states; the mapping must stay total so a future value degrades to `unknown`
// instead of throwing inside a row render.
describe("deriveAdapterState", () => {
  it("splits creating into building vs waiting for a DHCP lease", () => {
    expect(deriveAdapterState(adapter({ state: "creating", address: "" }))).toBe("dhcp");
    expect(deriveAdapterState(adapter({ state: "creating", address: "172.24.8.11" }))).toBe("creating");
  });

  it("needs an address before a ready row counts as ready", () => {
    expect(deriveAdapterState(adapter({ state: "ready", address: "172.24.8.11" }))).toBe("ready");
    expect(deriveAdapterState(adapter({ state: "ready", address: "" }))).toBe("unknown");
  });

  it("maps failed regardless of the address", () => {
    expect(deriveAdapterState(adapter({ state: "failed", address: "" }))).toBe("failed");
    expect(deriveAdapterState(adapter({ state: "failed", address: "10.0.0.1" }))).toBe("failed");
  });

  it("falls back to unknown for absent and any unrecognised state", () => {
    expect(deriveAdapterState(adapter({ state: "absent" }))).toBe("unknown");
    expect(deriveAdapterState(adapter({ state: "some-future-state" }))).toBe("unknown");
    expect(deriveAdapterState(adapter({ state: "" }))).toBe("unknown");
    // Whitespace-only addresses are not leases.
    expect(deriveAdapterState(adapter({ state: "creating", address: "   " }))).toBe("dhcp");
  });
});

describe("managed / pool flags", () => {
  it("only treats an explicit managed=true as ours to delete", () => {
    expect(isManagedAdapter(adapter({ managed: true }))).toBe(true);
    expect(isManagedAdapter(adapter({ managed: false }))).toBe(false);
  });

  it("only asserts pool membership for inPool=true", () => {
    expect(isInOutboundPool(adapter({ inPool: true }))).toBe(true);
    expect(isInOutboundPool(adapter({ inPool: false }))).toBe(false);
  });

  it("treats only raw state=creating as an unsettled batch row", () => {
    expect(isCreatingRow(adapter({ state: "creating", address: "" }))).toBe(true);
    expect(isCreatingRow(adapter({ state: "creating", address: "10.0.0.1" }))).toBe(true);
    expect(isCreatingRow(adapter({ state: "ready", address: "" }))).toBe(false);
  });
});

describe("summarizeAdapters", () => {
  it("counts ready rows out of all rows", () => {
    expect(summarizeAdapters([
      adapter({ state: "ready", address: "10.0.0.1" }),
      adapter({ state: "ready", address: "10.0.0.2" }),
      adapter({ state: "failed", address: "" }),
    ])).toEqual({ ready: 2, total: 3 });
    expect(summarizeAdapters([])).toEqual({ ready: 0, total: 0 });
  });
});

describe("row identity and formatting", () => {
  it("prefers adapterId and degrades to interfaceName then name", () => {
    expect(adapterRowKey(adapter({ adapterId: "id", interfaceName: "if", name: "n" }))).toBe("id");
    expect(adapterRowKey(adapter({ adapterId: "", interfaceName: "if", name: "n" }))).toBe("if");
    expect(adapterRowKey(adapter({ adapterId: "", interfaceName: "", name: "n" }))).toBe("n");
    expect(adapterRowKey(adapter({ adapterId: "", interfaceName: "", name: "" }))).toBe("");
  });

  it("renders address/prefix and never invents a lease", () => {
    expect(formatAddressPrefix(adapter({ address: "10.0.0.5", prefixLength: 24 }))).toBe("10.0.0.5/24");
    expect(formatAddressPrefix(adapter({ address: "10.0.0.5", prefixLength: 0 }))).toBe("10.0.0.5");
    expect(formatAddressPrefix(adapter({ address: "" }))).toBe("—");
    expect(formatMac(adapter({ macAddress: "  " }))).toBe("—");
    expect(formatLastError(adapter({ lastError: " boom " }))).toBe("boom");
    expect(formatLastError(adapter({ lastError: "" }))).toBe("");
  });
});

describe("validateCreateCount", () => {
  it("accepts the whole 1..16 range", () => {
    expect(validateCreateCount("1")).toEqual({ ok: true, value: 1 });
    expect(validateCreateCount(" 8 ")).toEqual({ ok: true, value: 8 });
    expect(validateCreateCount("16")).toEqual({ ok: true, value: 16 });
  });

  it("rejects 0 and 17 with the matching reason", () => {
    expect(validateCreateCount("0")).toEqual({ ok: false, reason: "too-small" });
    expect(validateCreateCount("17")).toEqual({ ok: false, reason: "too-large" });
  });

  it("rejects empty, fractional and non-numeric drafts", () => {
    expect(validateCreateCount("")).toEqual({ ok: false, reason: "empty" });
    expect(validateCreateCount("   ")).toEqual({ ok: false, reason: "empty" });
    expect(validateCreateCount("1.5")).toEqual({ ok: false, reason: "not-integer" });
    expect(validateCreateCount("-1")).toEqual({ ok: false, reason: "not-integer" });
    expect(validateCreateCount("abc")).toEqual({ ok: false, reason: "not-integer" });
  });

  it("honours an explicit ceiling", () => {
    expect(validateCreateCount("4", 4)).toEqual({ ok: true, value: 4 });
    expect(validateCreateCount("5", 4)).toEqual({ ok: false, reason: "too-large" });
    expect(VIRTUAL_ADAPTER_MAX_BATCH).toBe(16);
    expect(VIRTUAL_ADAPTER_MAX_TOTAL).toBe(32);
  });
});

describe("switch filtering", () => {
  it("keeps only External switches", () => {
    const list = [
      hypervSwitch({ name: "ext", type: "External" }),
      hypervSwitch({ name: "int", type: "Internal" }),
      hypervSwitch({ name: "priv", type: "Private" }),
      hypervSwitch({ name: "lower", type: "external" }),
      hypervSwitch({ name: "junk", type: "" }),
    ];
    expect(externalSwitches(list).map((item) => item.name)).toEqual(["ext", "lower"]);
    expect(isExternalSwitch(hypervSwitch({ type: "Internal" }))).toBe(false);
  });

  it("marks allowManagementOs=false as unusable", () => {
    expect(isSwitchSelectable(hypervSwitch({ allowManagementOs: true }))).toBe(true);
    expect(isSwitchSelectable(hypervSwitch({ allowManagementOs: false }))).toBe(false);
  });

  it("findSelectableSwitch never returns an unusable or non-External switch", () => {
    const list = [
      hypervSwitch({ name: "locked", type: "External", allowManagementOs: false }),
      hypervSwitch({ name: "ok", type: "External" }),
      hypervSwitch({ name: "internal", type: "Internal" }),
    ];
    expect(findSelectableSwitch(list, "ok")?.name).toBe("ok");
    expect(findSelectableSwitch(list, "locked")).toBeUndefined();
    expect(findSelectableSwitch(list, "internal")).toBeUndefined();
    expect(findSelectableSwitch(list, "missing")).toBeUndefined();
  });

  it("describes an option with its uplink when there is one", () => {
    expect(describeSwitch(hypervSwitch({ uplink: "以太网" }))).toBe("外网交换机 · 以太网");
    expect(describeSwitch(hypervSwitch({ name: "仅名称", uplink: "", netAdapterName: "" }))).toBe("仅名称");
  });
});

// M1: Go returns only the batch it just created (hyperv_adapter.go:1088), so a
// straight `setAdapters(next)` would empty the table of everything already on
// screen. Every one of these properties is load-bearing for that bug.
describe("mergeAdapterBatch", () => {
  const existing = [
    adapter({ name: "vNIC 1", adapterId: "adapter-1", macAddress: "00-15-5D-01-01-01" }),
    adapter({ name: "vNIC 2", adapterId: "adapter-2", macAddress: "00-15-5D-01-01-02" }),
  ];

  it("keeps rows the batch does not mention instead of dropping them", () => {
    const merged = mergeAdapterBatch(existing, [adapter({ name: "vNIC 9", adapterId: "adapter-9" })]);
    expect(merged.map((row) => row.name)).toEqual(["vNIC 1", "vNIC 2", "vNIC 9"]);
    expect(merged).toHaveLength(3);
  });

  it("prefers the batch snapshot for names it reports", () => {
    const merged = mergeAdapterBatch(existing, [adapter({ name: "vNIC 2", state: "creating", address: "" })]);
    expect(merged).toHaveLength(2);
    expect(merged[1].state).toBe("creating");
    expect(merged[1].address).toBe("");
  });

  it("preserves the lastError channel Go only writes into Create rows", () => {
    // hyperv_adapter.go:1238 injects the restart hint here and the List() path
    // never does, so a refresh would lose it; merging must keep it.
    const hint = "需要重启 HypoMux 聚合后生效";
    const merged = mergeAdapterBatch(existing, [adapter({ name: "vNIC 3", lastError: hint })]);
    expect(merged[2].lastError).toBe(hint);
  });

  it("never duplicates a name the table already has", () => {
    const batch = [adapter({ name: "vNIC 1", state: "failed" }), adapter({ name: "vNIC 1", state: "ready" })];
    const merged = mergeAdapterBatch(existing, batch);
    expect(merged).toHaveLength(2);
    expect(merged[0].state).toBe("ready");
  });

  it("returns a fresh array for an empty batch so React still re-renders", () => {
    const merged = mergeAdapterBatch(existing, []);
    expect(merged).toEqual(existing);
    expect(merged).not.toBe(existing);
  });

  it("adopts the batch wholesale when the table was empty", () => {
    const merged = mergeAdapterBatch([], [adapter({ name: "vNIC 1" })]);
    expect(merged.map((row) => row.name)).toEqual(["vNIC 1"]);
  });
});