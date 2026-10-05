// Pure helpers for the Hyper-V virtual adapter management page. Everything here
// is a total function of its inputs: no service calls, no React, no locale.
// The desktop service owns the real limits; these mirror them so the UI can
// validate before a long-running elevated call, and so a browser preview can
// render the exact same mapping.
import type { HyperVAdapterStatus, HyperVRemoveResult, HyperVSwitch } from "../../platform/services";

// reports/vnic/70-frozen-hyperv-interface.md §3.7 freezes 16 per batch and
// 32 in total. Both numbers are surfaced in the dialog hint and the page
// summary, so they live in one place instead of being inlined twice.
//
// VIRTUAL_ADAPTER_MAX_TOTAL is the only copy of the 32 on the frontend and it
// MUST stay equal to `hypervMaxTotalAdapters` in
// desktop/internal/services/hyperv_adapter.go:192. Go stays the authority
// (hypervCheckBatchCapacity, :362) — if the two ever drift, the page shows a
// quota the service does not enforce. Go never *reads* this file, so there is
// no build-time link: changing the Go constant means changing it here too.
// VIRTUAL_ADAPTER_MAX_BATCH mirrors `hypervMaxBatchSize` (hyperv_adapter.go:191).
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

// ---------------------------------------------------------------------------
// Removal policy
// ---------------------------------------------------------------------------

/**
 * Frontend mirror of the backend protection list.
 *
 * These two objects belong to Hyper-V itself, not to us: `Container NIC*` is the
 * vNIC Windows containers and Docker attach to, and `vEthernet (Default Switch)`
 * is the switch Windows builds for them. Deleting either cuts container and
 * Docker networking on the host, so the user gets no remove button on those rows
 * and a "system reserved" badge instead.
 *
 * This is a MIRROR, not the authority: Go decides again inside `RemoveAdapters`
 * and refuses the same rows. There is no build-time link between the two sides —
 * they live in different languages and different processes — so the rule must be
 * changed in BOTH places at the same time:
 *   - here, for the button, the checkbox and the disabled state;
 *   - `desktop/internal/services/hyperv_adapter.go`, for the actual refusal.
 * If they drift, the frontend stops offering a button for a row the backend
 * would accept, or — far worse — offers one the backend rejects after the user
 * has already confirmed and a UAC prompt has already appeared.
 */
export const PROTECTED_ADAPTER_NAME_PREFIX = "container nic";
export const PROTECTED_ADAPTER_INTERFACE = "vEthernet (Default Switch)";

/**
 * Name rule, usable before a row exists: `Container NIC <suffix>` in any casing
 * (`container nic`, `Container NIC`, `CONTAINER NIC …`). Prefix, not equality —
 * Windows appends a generated suffix to the container vNIC name.
 */
export const isProtectedAdapterName = (name: string | undefined | null): boolean =>
  (name ?? "").trim().toLowerCase().startsWith(PROTECTED_ADAPTER_NAME_PREFIX);

/**
 * Alias rule: the Default Switch is identified by the *host* adapter name, so it
 * is matched case-insensitively against `interfaceName` rather than against the
 * Hyper-V object name (which is just "Default Switch" and is far too broad a
 * string to match on its own).
 */
export const isProtectedAdapterInterface = (interfaceName: string | undefined | null): boolean =>
  (interfaceName ?? "").trim().toLowerCase() === PROTECTED_ADAPTER_INTERFACE.toLowerCase();

/** Either rule matched ⇒ this row must not offer a remove button. */
export const isProtectedAdapter = (adapter: HyperVAdapterStatus): boolean =>
  isProtectedAdapterName(adapter.name) || isProtectedAdapterInterface(adapter.interfaceName);

/**
 * The single question every removal surface asks: may this row be deleted?
 *
 * Note what is NOT checked here: `managed`. The ledger flag no longer gates
 * removal (the user removed that gate — "能检测到，能删除就加上移除按钮"), it only
 * decides whether the confirm dialog warns about deleting something this tool did
 * not create. Ownership is a description of the row, removability is a property
 * of the host.
 */
export const isAdapterRemovable = (adapter: HyperVAdapterStatus): boolean => !isProtectedAdapter(adapter);

/** The rows a multi-select batch is allowed to submit. */
export const removableAdapters = (
  adapters: readonly HyperVAdapterStatus[],
): HyperVAdapterStatus[] => adapters.filter(isAdapterRemovable);

/**
 * Stable row identity. DeviceId is the only stable key per contract §1.
 *
 * It is the ONLY identity in this module. `adapter.name` is not one: Hyper-V
 * happily holds two adapters with the same name, so every place that used a name
 * as a row identity ("is this row ticked?", "which row do I drop after the
 * backend says this name is gone?") lit up both same-named rows at once. The
 * fallback chain only matters for rows the ledger does not own and whose DeviceId
 * the host did not report; two such rows are indistinguishable on screen too, so
 * falling back to the interface alias — then the name — loses nothing that was
 * not already lost.
 */
export const adapterRowKey = (adapter: HyperVAdapterStatus): string =>
  (adapter.adapterId || adapter.interfaceName || adapter.name || "").trim();

/**
 * Resolve ticked ROW KEYS into the name list `RemoveAdapters` takes.
 *
 * Two levels of de-duplication, and both are load-bearing:
 *   1. **Key level** — one key resolves to at most one name, so ticking the same
 *      row twice (row click + checkbox click racing, a re-render mid-batch)
 *      cannot submit it twice.
 *   2. **Name level** — two DIFFERENT rows may share a name. The backend takes
 *      names and cannot tell them apart (the remove script locates an object by
 *      DeviceId but the result carries only `{name, removed, reason, interface}`),
 *      so submitting the same name twice buys nothing and only makes the failure
 *      text ambiguous. The first row in TABLE order wins, which also makes the
 *      batch read top-to-bottom exactly like the screen does.
 *
 * Order follows `adapters`, not `keys`, so the submit list is deterministic no
 * matter what order the user ticked in. Keys with no matching row (a row the 3s
 * poll already removed, or a stale tick) resolve to nothing rather than to a
 * guessed name: a name we can no longer tie to a row must not be sent.
 */
export const adapterNamesForKeys = (
  adapters: readonly HyperVAdapterStatus[],
  keys: readonly string[],
): string[] => {
  if (keys.length === 0) return [];
  const wanted = new Set(keys.filter((key) => key));
  if (wanted.size === 0) return [];
  const names: string[] = [];
  const seen = new Set<string>();
  for (const adapter of adapters) {
    if (!wanted.has(adapterRowKey(adapter))) continue;
    const name = (adapter.name ?? "").trim();
    if (!name || seen.has(name)) continue;
    seen.add(name);
    names.push(name);
  }
  return names;
};

/**
 * Drop the rows the host confirmed gone.
 *
 * Used from a functional setState right after a successful removal instead of
 * `await refresh()`, for the reason documented on mergeAdapterBatch: `List()`
 * never writes the `LastError` markers Go injects, so a refresh wipes the
 * restart/pool hint channel. The 3s poll reconciles anything the host disagrees
 * about a moment later.
 *
 * `submittedKeys` is optional and changes the pruning rule:
 *   - omitted / null → prune purely by name. Two rows share a name, the backend
 *     reports that name as removed, and BOTH rows vanish from the table — the
 *     original bug. Kept verbatim for callers (and existing tests) that have no
 *     row identity to offer.
 *   - provided → prune a row only when its row key is in `submittedKeys` AND its
 *     name is in `removedNames`.
 *
 * Why the third parameter is needed at all: a NAME is not an identity. When the
 * user ticks one of two same-named rows and submits, the batch carries that one
 * name; the script removes one object; the answer says `removed: true` for that
 * name. Nothing in `HyperVRemoveResult` says WHICH of the two went. Pruning by
 * name alone therefore deletes a row the user never touched and may not have been
 * removed at all — the table then claims a deletion that did not happen. What we
 * DO know is which rows we submitted, so that is the narrower half of the rule:
 * prune only submitted rows, and let the row the host really removed either
 * return on the next 3s poll or disappear from it. Being one poll late is honest;
 * deleting the wrong row is not.
 *
 * An empty `submittedKeys` therefore drops nothing: an empty batch submitted
 * nothing, so nothing can be pruned on its account.
 */
export const dropAdapters = (
  previous: readonly HyperVAdapterStatus[],
  removedNames: readonly string[],
  submittedKeys?: readonly string[] | null,
): HyperVAdapterStatus[] => {
  if (removedNames.length === 0) return [...previous];
  const gone = new Set(removedNames.filter((name) => name));
  if (gone.size === 0) return [...previous];
  if (submittedKeys === undefined || submittedKeys === null) {
    return previous.filter((row) => !gone.has(row.name));
  }
  const submitted = new Set(submittedKeys.filter((key) => key));
  return previous.filter((row) => !gone.has(row.name) || !submitted.has(adapterRowKey(row)));
};

export type RemoveFailure = { name: string; reason: string };
export type RemoveBatchOutcome = {
  /** Names the host confirmed are gone; these are the rows to drop locally. */
  removed: string[];
  /** Per-adapter failures, each carrying the backend's own reason verbatim. */
  failed: RemoveFailure[];
};

/**
 * Split one `RemoveAdapters` answer into "gone" and "still here".
 *
 * A batch is partially successful by design: Go removes what it can and reports
 * the rest per row instead of failing the whole call, so this function — not a
 * try/catch — is where "did it work" is decided. Collapsing the array into a
 * single boolean is what made a batch of 5 with 1 failure look like a total loss
 * (or, worse, like a clean success with a card silently left behind).
 *
 * A non-empty `reason` always counts as a failure even when `removed` claims
 * otherwise: the contract defines `reason` as "why this one did not go away", so
 * a contradictory row is the one the user must be told about. Such a row is
 * still counted as removed when `removed === true`, because only the host knows
 * whether the object is there.
 */
export const partitionRemoveResults = (
  results: readonly HyperVRemoveResult[] | null | undefined,
): RemoveBatchOutcome => {
  const removed: string[] = [];
  const failed: RemoveFailure[] = [];
  for (const row of results ?? []) {
    const name = (row?.name ?? "").trim();
    const reason = (row?.reason ?? "").trim();
    if (row?.removed === true) {
      if (name) removed.push(name);
      if (!reason) continue;
    }
    failed.push({ name, reason });
  }
  return { removed, failed };
};

/** Only `true` is an assertion; false/undefined must stay silent (see 63 §8.5). */
export const isInOutboundPool = (adapter: HyperVAdapterStatus): boolean => adapter.inPool === true;

export type AdapterSummary = { ready: number; total: number };

export const summarizeAdapters = (adapters: readonly HyperVAdapterStatus[]): AdapterSummary => ({
  ready: adapters.filter((adapter) => deriveAdapterState(adapter) === "ready").length,
  total: adapters.length,
});

export const formatAddressPrefix = (adapter: HyperVAdapterStatus): string => {
  const address = (adapter.address ?? "").trim();
  if (!address) return "—";
  return adapter.prefixLength > 0 ? `${address}/${adapter.prefixLength}` : address;
};

export const formatMac = (adapter: HyperVAdapterStatus): string =>
  (adapter.macAddress ?? "").trim() || "—";

export const formatLastError = (adapter: HyperVAdapterStatus): string =>
  (adapter.lastError ?? "").trim();

/**
 * Rows that actually consume the Go-side total budget.
 *
 * Go counts `len(ledger.Adapters)` (hyperv_adapter.go:1355), i.e. **ledger rows
 * only**. `List()` additionally returns read-only rows for Hyper-V objects that
 * happen to follow the `HypoMux-vnic-NN` naming convention but were created by
 * the user (`Managed=false`, hyperv_adapter.go:1166-1172); those do NOT eat into
 * the 32. Counting `adapters.length` here would therefore block creation while
 * the service would have accepted it — the mirror has to count `managed`.
 */
export const countQuotaAdapters = (adapters: readonly HyperVAdapterStatus[]): number =>
  adapters.filter(isManagedAdapter).length;

export type CreateQuota = {
  /** Ledger rows the service would count against its own budget. */
  used: number;
  /** The service's ceiling (`hypervMaxTotalAdapters`). */
  total: number;
  /** `max(0, total - used)` — how many more rows fit right now. */
  remaining: number;
  /** `min(VIRTUAL_ADAPTER_MAX_BATCH, remaining)`: the largest count one submission may carry. */
  perSubmitMax: number;
  /** `remaining === 0`: create is unavailable until something is deleted. */
  exhausted: boolean;
};

/**
 * Quota derived from the *current* list, never from a cached constant: another
 * batch may have been created elsewhere, and the page polls every
 * VIRTUAL_ADAPTER_POLL_INTERVAL_MS anyway.
 *
 * `used > total` is possible if the ledger was hand-edited or rows were created
 * by a build with a different ceiling, so `remaining` clamps at 0 instead of
 * going negative (a negative budget would make the UI claim "you may create -3").
 */
export const createQuota = (
  adapters: readonly HyperVAdapterStatus[],
  total: number = VIRTUAL_ADAPTER_MAX_TOTAL,
): CreateQuota => {
  const used = countQuotaAdapters(adapters);
  const remaining = Math.max(0, total - used);
  return {
    used,
    total,
    remaining,
    perSubmitMax: Math.min(VIRTUAL_ADAPTER_MAX_BATCH, remaining),
    exhausted: remaining === 0,
  };
};

export type CreateCountValidation =
  | { ok: true; value: number }
  | { ok: false; reason: "empty" | "not-integer" | "too-small" | "too-large" | "over-quota" };

/**
 * Count must be typed by hand (upstream m01604) — no "fill the gap" helper.
 * Rejects "0", "17", "1.5", "-1", "abc" and empty input before any UAC prompt.
 *
 * `quota` is optional and additive: the create dialog validates the 1..16 batch
 * shape without it, and the page additionally passes the live quota so an
 * in-range count that no longer fits is reported as `over-quota` *before* the
 * elevated call. Check order mirrors Go (hyperv_adapter.go:362-369): the batch
 * ceiling first (`:1324`), capacity second (`:1355`), so 17 with 32 free is
 * `too-large` and 3 with 2 free is `over-quota`.
 */
export const validateCreateCount = (
  raw: string,
  max: number = VIRTUAL_ADAPTER_MAX_BATCH,
  quota?: CreateQuota,
): CreateCountValidation => {
  const trimmed = raw.trim();
  if (!trimmed) return { ok: false, reason: "empty" };
  if (!/^\d+$/.test(trimmed)) return { ok: false, reason: "not-integer" };
  const value = Number(trimmed);
  if (!Number.isSafeInteger(value)) return { ok: false, reason: "not-integer" };
  if (value < 1) return { ok: false, reason: "too-small" };
  if (value > max) return { ok: false, reason: "too-large" };
  if (quota && value > quota.remaining) return { ok: false, reason: "over-quota" };
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

/**
 * Why create is unavailable, as distinct outcomes a user can act on.
 * - `none`: Hyper-V reported no switch at all.
 * - `external-only`: switches exist but none is External.
 * - `management-os-off`: External switches exist and *every* one has
 *   AllowManagementOS off — the most common real misconfiguration, and the one
 *   that used to share a clause naming neither the cause nor the fix, so users
 *   read it as "this host has no switch" and kept clicking Create.
 * - `none-usable`: defensive mixed case (External switches exist, none is
 *   selectable, yet the cause is not uniformly AllowManagementOS). Unreachable
 *   while `isSwitchSelectable` is `allowManagementOs !== false`; kept so a future
 *   stricter predicate degrades into an explanation instead of a silent gap.
 * - `ready`: at least one External switch accepts the adapter.
 */
export type SwitchAvailability =
  | "ready"
  | "none"
  | "external-only"
  | "management-os-off"
  | "none-usable";

export const switchAvailability = (switches: readonly HyperVSwitch[]): SwitchAvailability => {
  if (switches.length === 0) return "none";
  const external = externalSwitches(switches);
  if (external.length === 0) return "external-only";
  if (external.some(isSwitchSelectable)) return "ready";
  return external.every((item) => item.allowManagementOs === false) ? "management-os-off" : "none-usable";
};

// ---------------------------------------------------------------------------
// Backend hint markers
// ---------------------------------------------------------------------------

/**
 * The Go side sends machine-readable markers in `LastError` so the frontend can
 * localise them; everything else in that field is free text (a DHCP timeout
 * reason, a PowerShell exit code) that only the backend knows and that must be
 * shown verbatim. Contract: a marker is `hypomux.hint.<name>` and may be
 * followed by " | " plus runtime detail only the backend has.
 *
 * Splitting it here keeps the whole rule out of React and out of the i18n
 * runtime: unknown markers degrade to "free text", never to a lost message and
 * never to a mistranslated one.
 */
export const HYPOMUX_HINT_PREFIX = "hypomux.hint.";
export const HYPOMUX_HINT_POOL_RESTART = "hypomux.hint.pool_restart";
export const HYPOMUX_HINT_POOL_UPDATE_FAILED = "hypomux.hint.pool_update_failed";
export const HYPOMUX_HINT_SEPARATOR = " | ";

export type BackendHint = {
  /** The marker including its prefix, or "" when the text is not a marker. */
  code: string;
  /** Everything after the first separator, or the whole string when unmarked. */
  detail: string;
};

/**
 * Split `lastError` into marker + detail. Never throws and never drops text:
 * an unrecognised `hypomux.hint.*` value comes back with `code` set and the raw
 * string intact, so the caller can render it as-is instead of swallowing it.
 */
export const splitBackendHint = (lastError: string): BackendHint => {
  const raw = (lastError ?? "").trim();
  if (!raw) return { code: "", detail: "" };
  if (!raw.startsWith(HYPOMUX_HINT_PREFIX)) return { code: "", detail: raw };
  const separator = raw.indexOf(HYPOMUX_HINT_SEPARATOR);
  if (separator < 0) return { code: raw, detail: "" };
  return {
    code: raw.slice(0, separator).trim(),
    detail: raw.slice(separator + HYPOMUX_HINT_SEPARATOR.length).trim(),
  };
};

/**
 * The only two markers the contract defines. A key map rather than an `if`, so
 * adding a third marker is one line here plus one zh/en pair.
 */
export const BACKEND_HINT_I18N_KEYS: Readonly<Record<string, string>> = {
  [HYPOMUX_HINT_POOL_RESTART]: "virtual_adapters_hint_pool_restart",
  [HYPOMUX_HINT_POOL_UPDATE_FAILED]: "virtual_adapters_hint_pool_update_failed",
};

/** i18n key for a known marker, or undefined for free text / future markers. */
export const backendHintI18nKey = (code: string): string | undefined =>
  Object.prototype.hasOwnProperty.call(BACKEND_HINT_I18N_KEYS, code)
    ? BACKEND_HINT_I18N_KEYS[code]
    : undefined;

/**
 * Contract §3.6: a freshly created adapter only joins the outbound pool after
 * the aggregation engine restarts. The page used to assert that unconditionally
 * right after `Create()` resolved, so the prompt showed even when the list said
 * the card had already settled — and hid nothing when the pool write silently
 * never landed. Derive it from the row instead: a card this session created
 * that reports `ready` while already claiming pool membership is exactly the
 * case where the claim may be ahead of the background pool write, so that is
 * when the user has to be told to restart the aggregation.
 *
 * Raw `state` (not `deriveAdapterState`) on purpose: the question is "did the
 * backend settle this row", which is the ledger's own state field, and a lease
 * that has not landed yet must not silently mute the prompt.
 */
export const needsRestartPrompt = (adapter: HyperVAdapterStatus): boolean =>
  adapter.state === "ready" && isInOutboundPool(adapter);

/**
 * Names of this session's created adapters that still need the restart. Pure so
 * the page can recompute it from every poll: a row whose `inPool` flips to false
 * drops out on its own, which is how the prompt converges without a timer.
 */
export const pendingRestartNames = (
  adapters: readonly HyperVAdapterStatus[],
  createdNames: readonly string[],
): string[] => {
  if (createdNames.length === 0) return [];
  const created = new Set(createdNames);
  return adapters
    .filter((adapter) => created.has(adapter.name) && needsRestartPrompt(adapter))
    .map((adapter) => adapter.name);
};