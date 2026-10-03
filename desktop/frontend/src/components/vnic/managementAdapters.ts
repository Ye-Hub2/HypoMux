// Pure helpers for the Hyper-V virtual adapter management page. Everything here
// is a total function of its inputs: no service calls, no React, no locale.
// The desktop service owns the real limits; these mirror them so the UI can
// validate before a long-running elevated call, and so a browser preview can
// render the exact same mapping.
import type { HyperVAdapterStatus, HyperVSwitch } from "../../platform/services";

// reports/vnic/70-frozen-hyperv-interface.md §3.7 freezes 16 per batch and
// 32 in total. Both numbers are surfaced in the dialog hint and the page
// summary, so they live in one place instead of being inlined twice.
export const VIRTUAL_ADAPTER_MAX_BATCH = 16;
export const VIRTUAL_ADAPTER_MAX_TOTAL = 32;

// AppShell keeps every visited page mounted, so the page must poll through the
// page-activity gate. 3s matches BlockedDomainsPage; list() is cheap because
// the Go side never needs elevation for it (§3.8).
export const VIRTUAL_ADAPTER_POLL_INTERVAL_MS = 3000;

// Contract §3.5: the card shows up around 15s and the router lease lands by
// ~45s; 60s is the overall budget. Past that the wait is worth explaining
// instead of leaving the user staring at a spinner.
export const VIRTUAL_ADAPTER_DHCP_SLOW_MS = 60_000;

// Contract §3.2 only ever sends absent|creating|ready|failed. The derived state
// splits `creating` into "still building the Hyper-V object" vs "the card exists
// but the router has not leased an address yet", which is what the user is
// actually waiting for. `unknown` is the safe sink: an unrecognised value must
// never crash the row or silently claim success.
export type DerivedAdapterState = "creating" | "dhcp" | "ready" | "failed" | "unknown";

export const deriveAdapterState = (adapter: HyperVAdapterStatus): DerivedAdapterState => {
  const address = (adapter.address ?? "").trim();
  switch (adapter.state) {
    case "creating":
      return address ? "creating" : "dhcp";
    case "ready":
      return address ? "ready" : "unknown";
    case "failed":
      return "failed";
    case "absent":
    default:
      return "unknown";
  }
};

/** Raw service state, used to decide when a create batch has settled. */
export const isCreatingRow = (adapter: HyperVAdapterStatus): boolean => adapter.state === "creating";

/**
 * `Create()` reports **only the rows of the batch it just created**
 * (hyperv_adapter.go:1088-1089, ":1233-1243"), not the full ledger. Writing
 * that slice back as the whole list makes every pre-existing adapter — and its
 * pool badge — blink out of the table until the next 3s poll restores them, and
 * makes the summary badge flash backwards ("12 of 12" -> "0 of 4").
 *
 * Merge by name instead: a name the batch reports wins (it is the fresher
 * snapshot), names the batch does not mention are kept untouched, and names the
 * table has never seen are appended. Rows keep their existing order so the new
 * cards land at the bottom rather than jumping around.
 *
 * A plain `await refresh()` is NOT equivalent — Go injects the "restart the
 * aggregation engine" hint into `LastError` of the rows it returns from Create
 * (hyperv_adapter.go:1238-1240) and the `List()` path never writes it, so a
 * refresh would erase the only channel that tells the user what to do next.
 */
export const mergeAdapterBatch = (
  previous: readonly HyperVAdapterStatus[],
  batch: readonly HyperVAdapterStatus[],
): HyperVAdapterStatus[] => {
  if (batch.length === 0) return [...previous];
  const incoming = new Map(batch.map((row) => [row.name, row]));
  const known = new Set(previous.map((row) => row.name));
  const merged = previous.map((row) => incoming.get(row.name) ?? row);
  for (const row of batch) {
    if (!known.has(row.name)) {
      known.add(row.name);
      merged.push(row);
    }
  }
  return merged;
};

/** Contract §3.4: only rows recorded in the ledger are ours to delete. */
export const isManagedAdapter = (adapter: HyperVAdapterStatus): boolean => adapter.managed === true;

/** Only `true` is an assertion; false/undefined must stay silent (see 63 §8.5). */
export const isInOutboundPool = (adapter: HyperVAdapterStatus): boolean => adapter.inPool === true;

export type AdapterSummary = { ready: number; total: number };

export const summarizeAdapters = (adapters: readonly HyperVAdapterStatus[]): AdapterSummary => ({
  ready: adapters.filter((adapter) => deriveAdapterState(adapter) === "ready").length,
  total: adapters.length,
});

/** Stable row identity. DeviceId is the only stable key per contract §1. */
export const adapterRowKey = (adapter: HyperVAdapterStatus): string =>
  (adapter.adapterId || adapter.interfaceName || adapter.name || "").trim();

export const formatAddressPrefix = (adapter: HyperVAdapterStatus): string => {
  const address = (adapter.address ?? "").trim();
  if (!address) return "—";
  return adapter.prefixLength > 0 ? `${address}/${adapter.prefixLength}` : address;
};

export const formatMac = (adapter: HyperVAdapterStatus): string =>
  (adapter.macAddress ?? "").trim() || "—";

export const formatLastError = (adapter: HyperVAdapterStatus): string =>
  (adapter.lastError ?? "").trim();

export type CreateCountValidation =
  | { ok: true; value: number }
  | { ok: false; reason: "empty" | "not-integer" | "too-small" | "too-large" };

/**
 * Count must be typed by hand (upstream m01604) — no "fill the gap" helper.
 * Rejects "0", "17", "1.5", "-1", "abc" and empty input before any UAC prompt.
 */
export const validateCreateCount = (
  raw: string,
  max: number = VIRTUAL_ADAPTER_MAX_BATCH,
): CreateCountValidation => {
  const trimmed = raw.trim();
  if (!trimmed) return { ok: false, reason: "empty" };
  if (!/^\d+$/.test(trimmed)) return { ok: false, reason: "not-integer" };
  const value = Number(trimmed);
  if (!Number.isSafeInteger(value)) return { ok: false, reason: "not-integer" };
  if (value < 1) return { ok: false, reason: "too-small" };
  if (value > max) return { ok: false, reason: "too-large" };
  return { ok: true, value };
};

/** Only an External switch can host a management-OS vNIC (contract §1). */
export const isExternalSwitch = (item: HyperVSwitch): boolean =>
  (item.type ?? "").trim().toLowerCase() === "external";

/** `allowManagementOs === false` is the only rejection signal we render. */
export const isSwitchSelectable = (item: HyperVSwitch): boolean => item.allowManagementOs !== false;

export const externalSwitches = (switches: readonly HyperVSwitch[]): HyperVSwitch[] =>
  switches.filter(isExternalSwitch);

export const findSelectableSwitch = (
  switches: readonly HyperVSwitch[],
  name: string,
): HyperVSwitch | undefined =>
  externalSwitches(switches).find((item) => item.name === name && isSwitchSelectable(item));

/** Human-readable suffix for a switch option; keeps the dialog self-explanatory. */
export const describeSwitch = (item: HyperVSwitch): string => {
  const uplink = (item.uplink || item.netAdapterName || "").trim();
  return uplink ? `${item.name} · ${uplink}` : item.name;
};