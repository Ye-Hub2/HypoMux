import { describe, expect, it } from "vitest";
import type {
  HyperVAdapterStatus,
  HyperVRemoveResult,
  HyperVSwitch,
} from "../../platform/services";
import {
  adapterNamesForKeys,
  adapterRowKey,
  backendHintI18nKey,
  countQuotaAdapters,
  createQuota,
  deriveAdapterState,
  describeSwitch,
  dropAdapters,
  externalSwitches,
  findSelectableSwitch,
  formatAddressPrefix,
  formatLastError,
  formatMac,
  isAdapterRemovable,
  isCreatingRow,
  isExternalSwitch,
  isInOutboundPool,
  isManagedAdapter,
  isSwitchSelectable,
  mergeAdapterBatch,
  needsRestartPrompt,
  partitionRemoveResults,
  pendingRestartNames,
  removableAdapters,
  splitBackendHint,
  summarizeAdapters,
  switchAvailability,
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

/**
 * N ledger rows as List() would return them: distinct DeviceIds, all `managed`.
 * Every quota assertion runs through this so a test can never accidentally
 * describe a list the service could not produce.
 */
const managedRows = (count: number): HyperVAdapterStatus[] =>
  Array.from({ length: count }, (_, index) => {
    const seq = String(index + 1).padStart(2, "0");
    return adapter({
      name: `HypoMux-vnic-${seq}`,
      interfaceName: `vEthernet (HypoMux-vnic-${seq})`,
      adapterId: `adapter-${seq}`,
      macAddress: `00-15-5D-01-02-${seq}`,
      managed: true,
      inPool: false,
    });
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

  // hypervCheckBatchCapacity (hyperv_adapter.go:362) refuses the WHOLE batch on
  // `existing + count > 32`, so an in-range count that no longer fits must be
  // distinguishable from the 1..16 shape errors the dialog already blocks.
  it("accepts a count that exactly fits the remaining quota", () => {
    const quota = createQuota(managedRows(30));
    expect(validateCreateCount("2", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: true, value: 2 });
    expect(validateCreateCount("1", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: true, value: 1 });
  });

  it("rejects a count past the remaining quota as over-quota, not too-large", () => {
    const quota = createQuota(managedRows(30));
    expect(validateCreateCount("3", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "over-quota" });
    expect(validateCreateCount("16", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "over-quota" });
  });

  it("rejects every positive count once the quota is exhausted", () => {
    const quota = createQuota(managedRows(32));
    expect(quota.exhausted).toBe(true);
    expect(validateCreateCount("1", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "over-quota" });
  });

  it("keeps the batch ceiling ahead of the quota, exactly like Go does", () => {
    // Go checks `count < 1 || count > 16` (:1324) before the ledger capacity
    // check (:1355), so 17 on a full machine is still a batch-shape error.
    const quota = createQuota(managedRows(32));
    expect(validateCreateCount("17", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "too-large" });
    expect(validateCreateCount("0", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "too-small" });
    expect(validateCreateCount("", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "empty" });
    expect(validateCreateCount("1.5", VIRTUAL_ADAPTER_MAX_BATCH, quota)).toEqual({ ok: false, reason: "not-integer" });
  });

  it("leaves the dialog's own 1..16 validation untouched when no quota is passed", () => {
    // HyperVAdapterPanel calls validateCreateCount(draft, VIRTUAL_ADAPTER_MAX_BATCH)
    // with two arguments; that path must keep behaving exactly as before.
    expect(validateCreateCount("16")).toEqual({ ok: true, value: 16 });
    expect(validateCreateCount("16", VIRTUAL_ADAPTER_MAX_BATCH, createQuota(managedRows(32)))).toEqual({
      ok: false,
      reason: "over-quota",
    });
  });
});

// Go's total budget: `existing` is `len(ledger.Adapters)` (hyperv_adapter.go:1355).
describe("createQuota", () => {
  it("reports the whole budget on an empty ledger, capped by the batch size", () => {
    expect(createQuota([])).toEqual({
      used: 0,
      total: VIRTUAL_ADAPTER_MAX_TOTAL,
      remaining: VIRTUAL_ADAPTER_MAX_TOTAL,
      perSubmitMax: VIRTUAL_ADAPTER_MAX_BATCH,
      exhausted: false,
    });
  });

  it("counts the real gap instead of pretending the batch cap is the limit", () => {
    expect(createQuota(managedRows(30))).toEqual({
      used: 30,
      total: 32,
      remaining: 2,
      perSubmitMax: 2,
      exhausted: false,
    });
  });

  it("reports exactly zero remaining at the ceiling", () => {
    expect(createQuota(managedRows(32))).toEqual({
      used: 32,
      total: 32,
      remaining: 0,
      perSubmitMax: 0,
      exhausted: true,
    });
  });

  it("clamps a ledger that is already over the ceiling instead of going negative", () => {
    // Hand-edited ledger or an older build with a different cap: remaining must
    // read 0, never a negative budget.
    const quota = createQuota(managedRows(35));
    expect(quota.used).toBe(35);
    expect(quota.remaining).toBe(0);
    expect(quota.perSubmitMax).toBe(0);
    expect(quota.exhausted).toBe(true);
  });

  // List() also returns read-only rows for Hyper-V objects that follow the naming
  // convention but were never in our ledger (hyperv_adapter.go:1166-1172). Go
  // counts `len(ledger.Adapters)`, so those rows must not shrink the budget here
  // or the UI would refuse a create the service accepts.
  it("does not charge read-only foreign rows against the budget", () => {
    const rows = [...managedRows(30), ...managedRows(2).map((row) => ({ ...row, managed: false }))];
    expect(rows).toHaveLength(32);
    expect(countQuotaAdapters(rows)).toBe(30);
    expect(createQuota(rows).remaining).toBe(2);
    expect(createQuota(rows).exhausted).toBe(false);
  });

  it("treats managed=false and missing flags alike", () => {
    const rows = [
      adapter({ adapterId: "a", managed: true }),
      adapter({ adapterId: "b", managed: false }),
      adapter({ adapterId: "c", managed: undefined as unknown as boolean }),
    ];
    expect(countQuotaAdapters(rows)).toBe(1);
  });

  it("honours an explicit total so a future ceiling needs no second code path", () => {
    expect(createQuota(managedRows(3), 4)).toEqual({
      used: 3,
      total: 4,
      remaining: 1,
      perSubmitMax: 1,
      exhausted: false,
    });
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

// The hint contract is fixed by the Go side (hyperv_adapter.go:121-124):
// `hypomux.hint.` + one of two codes, optionally followed by " | " and a
// backend-only detail. Everything else is free text that must survive untouched.
describe("splitBackendHint", () => {
  it("treats an empty or blank lastError as nothing at all", () => {
    expect(splitBackendHint("")).toEqual({ code: "", detail: "" });
    expect(splitBackendHint("   \n\t ")).toEqual({ code: "", detail: "" });
  });

  it("leaves free text alone — no code, verbatim detail", () => {
    expect(splitBackendHint("DhcpClient: timeout after 120s")).toEqual({
      code: "",
      detail: "DhcpClient: timeout after 120s",
    });
  });

  it("does not mistake a lookalike prefix for a marker", () => {
    // The contract's prefix carries a trailing dot; without it the string is
    // backend text, and so is anything that only *mentions* the prefix.
    expect(splitBackendHint("hypomux.hint")).toEqual({ code: "", detail: "hypomux.hint" });
    expect(splitBackendHint("hypomux.hints.pool_restart")).toEqual({ code: "", detail: "hypomux.hints.pool_restart" });
    expect(splitBackendHint("see hypomux.hint.pool_restart for details")).toEqual({
      code: "",
      detail: "see hypomux.hint.pool_restart for details",
    });
  });

  it("returns a bare marker with an empty detail", () => {
    expect(splitBackendHint("hypomux.hint.pool_restart")).toEqual({
      code: "hypomux.hint.pool_restart",
      detail: "",
    });
  });

  it("splits the first separator only, so a detail may contain the separator", () => {
    expect(splitBackendHint("hypomux.hint.pool_update_failed | exit 1 | stderr: denied")).toEqual({
      code: "hypomux.hint.pool_update_failed",
      detail: "exit 1 | stderr: denied",
    });
  });

  it("trims the whitespace around both halves", () => {
    expect(splitBackendHint("  hypomux.hint.pool_restart   |   重启聚合引擎  ")).toEqual({
      code: "hypomux.hint.pool_restart",
      detail: "重启聚合引擎",
    });
  });

  it("maps only the two markers Go actually emits to language-pack keys", () => {
    expect(backendHintI18nKey("hypomux.hint.pool_restart")).toBe("virtual_adapters_hint_pool_restart");
    expect(backendHintI18nKey("hypomux.hint.pool_update_failed")).toBe("virtual_adapters_hint_pool_update_failed");
  });

  it("refuses to invent a translation for an unknown code", () => {
    expect(backendHintI18nKey("hypomux.hint.brand_new_code")).toBeUndefined();
    expect(backendHintI18nKey("")).toBeUndefined();
    // Prototype keys must not resolve either.
    expect(backendHintI18nKey("toString")).toBeUndefined();
    expect(backendHintI18nKey("constructor")).toBeUndefined();
  });
});

// The restart banner used to be an unconditional setState(true) after Create()
// resolved, even though Go writes the outbound pool in a background goroutine
// that runs afterwards.
describe("needsRestartPrompt / pendingRestartNames", () => {
  it("only prompts for a ready adapter that claims pool membership", () => {
    expect(needsRestartPrompt(adapter({ state: "ready", inPool: true }))).toBe(true);
    expect(needsRestartPrompt(adapter({ state: "ready", inPool: false }))).toBe(false);
    expect(needsRestartPrompt(adapter({ state: "failed", inPool: true }))).toBe(false);
    expect(needsRestartPrompt(adapter({ state: "creating", inPool: true }))).toBe(false);
  });

  it("names only this session's adapters", () => {
    const rows = [adapter({ name: "mine", adapterId: "a", inPool: true }), adapter({ name: "someone else's", adapterId: "b", inPool: true })];
    expect(pendingRestartNames(rows, ["mine"])).toEqual(["mine"]);
  });

  it("converges on its own once a claim disappears from the next poll", () => {
    const rows = [adapter({ name: "mine", adapterId: "a", inPool: false })];
    expect(pendingRestartNames(rows, ["mine"])).toEqual([]);
  });

  it("never names an adapter that is not in the list at all", () => {
    expect(pendingRestartNames([], ["ghost"])).toEqual([]);
    expect(pendingRestartNames([], [])).toEqual([]);
  });
});

describe("switchAvailability", () => {
  it("reports an empty host as 'none' rather than as a filtered-out switch", () => {
    expect(switchAvailability([])).toBe("none");
  });

  it("reports a host with no external switch at all", () => {
    expect(switchAvailability([hypervSwitch({ type: "Internal" })])).toBe("external-only");
  });

  it("names the AllowManagementOS case when every external switch has it off", () => {
    expect(switchAvailability([hypervSwitch({ allowManagementOs: false })])).toBe("management-os-off");
  });

  it("is ready as soon as one external switch allows the management OS", () => {
    expect(
      switchAvailability([hypervSwitch({ name: "off", allowManagementOs: false }), hypervSwitch({ name: "on" })]),
    ).toBe("ready");
  });
});

// The protection list below mirrors Go. Every rule is mirrored and pinned on
// purpose: if the backend ever stops protecting Default Switch, the frontend
// must stop protecting it in the same commit, and this suite is what forces the
// two edits to be noticed together.
describe("isAdapterRemovable", () => {
  it("allows an ordinary adapter, whoever created it", () => {
    expect(isAdapterRemovable(adapter({ managed: true }))).toBe(true);
    // Ownership no longer decides removability; only the confirmation wording
    // still depends on it.
    expect(isAdapterRemovable(adapter({ managed: false, name: "xuni-01" }))).toBe(true);
  });

  it("protects any container NIC by name prefix, whatever the casing", () => {
    expect(isAdapterRemovable(adapter({ name: "Container NIC 9d844023" }))).toBe(false);
    expect(isAdapterRemovable(adapter({ name: "container nic 9d844023" }))).toBe(false);
    expect(isAdapterRemovable(adapter({ name: "CONTAINER NIC" }))).toBe(false);
    expect(isAdapterRemovable(adapter({ name: "  Container NIC {guid}  " }))).toBe(false);
  });

  it("protects the default switch through its host alias, whatever the casing", () => {
    expect(
      isAdapterRemovable(adapter({ name: "Default Switch", interfaceName: "vEthernet (Default Switch)" })),
    ).toBe(false);
    expect(
      isAdapterRemovable(adapter({ name: "默认交换机", interfaceName: "VETHERNET (DEFAULT SWITCH)" })),
    ).toBe(false);
    expect(
      isAdapterRemovable(adapter({ name: "padded", interfaceName: " vEthernet (Default Switch) " })),
    ).toBe(false);
  });

  it("does not treat a merely similar name or alias as protected", () => {
    // Prefix match, not substring match: "My Container NIC" is a user's card.
    expect(isAdapterRemovable(adapter({ name: "My Container NIC" }))).toBe(true);
    expect(isAdapterRemovable(adapter({ name: "Container Network" }))).toBe(true);
    expect(
      isAdapterRemovable(adapter({ name: "switch", interfaceName: "vEthernet (Default Switch) 2" })),
    ).toBe(true);
    expect(isAdapterRemovable(adapter({ interfaceName: "" }))).toBe(true);
  });

  it("filters a mixed list down to the rows the backend will accept", () => {
    const rows = [
      adapter({ adapterId: "a", name: "HypoMux vNIC 1" }),
      adapter({ adapterId: "b", name: "Container NIC 9d844023" }),
      adapter({ adapterId: "c", name: "Default Switch", interfaceName: "vEthernet (Default Switch)" }),
      adapter({ adapterId: "d", name: "xuni-01" }),
    ];
    expect(removableAdapters(rows).map((row) => row.adapterId)).toEqual(["a", "d"]);
  });
});

describe("adapterNamesForKeys", () => {
  const rows = [
    adapter({ adapterId: "a", name: "HypoMux vNIC 1" }),
    adapter({ adapterId: "b", name: "xuni-01" }),
    adapter({ adapterId: "c", name: "xuni-02" }),
  ];

  it("resolves keys to names in table order, not tick order", () => {
    expect(adapterNamesForKeys(rows, ["c", "a"])).toEqual(["HypoMux vNIC 1", "xuni-02"]);
  });

  it("submits a name once no matter how many keys share it", () => {
    // Hyper-V can hold two adapters with one name. RemoveAdapters takes names,
    // so the second copy would not reach a second object — it would only make
    // the failure text say the same card failed twice.
    const twins = [adapter({ adapterId: "a", name: "xuni-01" }), adapter({ adapterId: "b", name: "xuni-01" })];
    expect(adapterNamesForKeys(twins, ["a", "b"])).toEqual(["xuni-01"]);
  });

  it("submits a repeated key once", () => {
    expect(adapterNamesForKeys(rows, ["b", "b"])).toEqual(["xuni-01"]);
  });

  it("drops keys that no longer resolve to a row instead of guessing a name", () => {
    // A key whose row the 3s poll already removed (or that was never one) has no
    // name we can attribute to anything, so it must not be put on the wire.
    expect(adapterNamesForKeys(rows, ["b", "gone", " "])).toEqual(["xuni-01"]);
  });

  it("is empty for an empty selection", () => {
    expect(adapterNamesForKeys(rows, [])).toEqual([]);
    expect(adapterNamesForKeys(rows, [" "])).toEqual([]);
  });
});

describe("dropAdapters", () => {
  it("removes only the named rows and leaves the object order alone", () => {
    const rows = [
      adapter({ adapterId: "a", name: "HypoMux vNIC 1" }),
      adapter({ adapterId: "b", name: "xuni-01" }),
      adapter({ adapterId: "c", name: "xuni-02" }),
    ];
    expect(dropAdapters(rows, ["xuni-01"]).map((row) => row.adapterId)).toEqual(["a", "c"]);
  });

  it("is a no-op for names that are not on screen", () => {
    const rows = [adapter({ adapterId: "a" })];
    expect(dropAdapters(rows, [])).toEqual(rows);
    expect(dropAdapters(rows, ["ghost"])).toEqual(rows);
  });

  // Two rows, one name. Without submittedKeys the prune is by name alone, so
  // the backend reporting "xuni-01 is gone" erases a row nobody asked about.
  const twins = [
    adapter({ adapterId: "a", name: "xuni-01" }),
    adapter({ adapterId: "b", name: "xuni-01" }),
    adapter({ adapterId: "c", name: "xuni-02" }),
  ];

  it("erases BOTH same-named rows when no row identity is supplied", () => {
    // The legacy two-argument contract, kept for callers that have none. It is
    // the behaviour the third argument exists to make avoidable.
    expect(dropAdapters(twins, ["xuni-01"]).map((row) => row.adapterId)).toEqual(["c"]);
    expect(dropAdapters(twins, ["xuni-01"], null).map((row) => row.adapterId)).toEqual(["c"]);
  });

  it("keeps the same-named row the batch never submitted", () => {
    expect(dropAdapters(twins, ["xuni-01"], ["a"]).map((row) => row.adapterId)).toEqual(["b", "c"]);
  });

  it("prunes both same-named rows when the batch submitted both", () => {
    // We cannot tell which object the script removed, so the honest reading is
    // "both are candidates"; the poll decides which one was actually gone.
    expect(dropAdapters(twins, ["xuni-01"], ["a", "b"]).map((row) => row.adapterId)).toEqual(["c"]);
  });

  it("prunes nothing for an empty submission", () => {
    expect(dropAdapters(twins, ["xuni-01"], []).map((row) => row.adapterId)).toEqual(["a", "b", "c"]);
  });

  it("prunes only the submitted row among several names", () => {
    expect(dropAdapters(twins, ["xuni-01", "xuni-02"], ["b", "c"]).map((row) => row.adapterId)).toEqual(["a"]);
  });
});

describe("partitionRemoveResults", () => {
  const result = (patch: Partial<HyperVRemoveResult> = {}): HyperVRemoveResult => ({
    name: "HypoMux vNIC 1",
    removed: true,
    reason: "",
    interface: "vEthernet (HypoMux vNIC 1)",
    ...patch,
  });

  it("splits a clean run", () => {
    const outcome = partitionRemoveResults([
      result({ name: "HypoMux vNIC 1" }),
      result({ name: "xuni-01" }),
    ]);
    expect(outcome.removed).toEqual(["HypoMux vNIC 1", "xuni-01"]);
    expect(outcome.failed).toEqual([]);
  });

  it("keeps a partially failed batch usable: the successes still count", () => {
    const outcome = partitionRemoveResults([
      result({ name: "HypoMux vNIC 1" }),
      result({ name: "xuni-01" }),
      result({
        name: "xuni-02",
        removed: false,
        reason: "适配器正被虚拟交换机占用",
        interface: "vEthernet (xuni-02)",
      }),
    ]);
    expect(outcome.removed).toEqual(["HypoMux vNIC 1", "xuni-01"]);
    expect(outcome.failed).toHaveLength(1);
    expect(outcome.failed[0].name).toBe("xuni-02");
    // The reason is a backend runtime fact and is carried through untouched.
    expect(outcome.failed[0].reason).toBe("适配器正被虚拟交换机占用");
    // RemoveFailure deliberately does not carry the backend's "interface"
    // alias: the UI already shows the host alias from the row it holds, and a
    // second copy would be a chance for the two to disagree.
  });

  it("treats a contradictory row as a failure, never as a silent success", () => {
    // removed=true with a reason, and removed=false with none: both are
    // inconsistencies, and both must stay visible instead of pruning the row.
    expect(partitionRemoveResults([result({ reason: "复制超时，但脚本报告已完成" })]).failed).toHaveLength(1);
    const contradictory = partitionRemoveResults([result({ name: "xuni-03", removed: false })]);
    expect(contradictory.removed).toEqual([]);
    expect(contradictory.failed).toHaveLength(1);
  });

  it("survives an empty or missing result list", () => {
    expect(partitionRemoveResults([])).toEqual({ removed: [], failed: [] });
    expect(partitionRemoveResults(null)).toEqual({ removed: [], failed: [] });
    expect(partitionRemoveResults(undefined)).toEqual({ removed: [], failed: [] });
  });
});