import { MessageBar, MessageBarBody } from "@fluentui/react-components";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AppNotificationInput,
  useAppNotifications,
} from "../components/notifications/AppNotifications";
import { usePageActive } from "../components/shell/PageActivity";
import { HyperVAdapterPanel } from "../components/vnic/HyperVAdapterPanel";
import {
  adapterNamesForKeys,
  adapterRowKey,
  createQuota,
  deriveAdapterState,
  dropAdapters,
  isAdapterRemovable,
  isCreatingRow,
  mergeAdapterBatch,
  partitionRemoveResults,
  pendingRestartNames,
  removableAdapters,
  validateCreateCount,
  VIRTUAL_ADAPTER_DHCP_SLOW_MS,
  VIRTUAL_ADAPTER_MAX_BATCH,
  VIRTUAL_ADAPTER_POLL_INTERVAL_MS,
} from "../components/vnic/managementAdapters";
import { useI18n } from "../i18n/i18n";
import { isDesktopRuntime } from "../platform/runtime";
import {
  appServices,
  withServiceTimeout,
  HYPERV_ADAPTER_BATCH_TIMEOUT_MS,
  HYPERV_ADAPTER_CREATE_TIMEOUT_MS,
  HYPERV_ADAPTER_READ_TIMEOUT_MS,
  type HyperVAdapterStatus,
  type HyperVSwitch,
} from "../platform/services";
import { startSerialPoll } from "../platform/serialPoll";
import "./VirtualAdaptersPage.css";

// Browser visual QA (ConnectionsPage.tsx:204 precedent): in DEV without the
// Wails bridge there is no real Hyper-V to read, so we render the three row
// shapes the mapping can produce and hard-disable every write. Without this the
// page would render an empty table and look broken instead of disconnected.
// Sample values stay locale-neutral ASCII, like connectionPreview.ts, so the
// zh locale preview shows no stray English prose.
const PREVIEW_ADAPTERS: HyperVAdapterStatus[] = [
  {
    name: "HypoMux vNIC 1",
    interfaceName: "vEthernet (HypoMux vNIC 1)",
    adapterId: "preview-adapter-1",
    macAddress: "00-15-5D-01-02-03",
    switchName: "LAN-External",
    state: "ready",
    address: "172.24.8.11",
    prefixLength: 24,
    gateway: "172.24.8.1",
    managed: true,
    inPool: true,
    batchId: "preview-batch-1",
    createdAt: "2026-01-01T00:00:00Z",
    lastError: "",
  },
  {
    name: "HypoMux vNIC 2",
    interfaceName: "vEthernet (HypoMux vNIC 2)",
    adapterId: "preview-adapter-2",
    macAddress: "00-15-5D-04-05-06",
    switchName: "LAN-External",
    state: "ready",
    address: "172.24.8.12",
    prefixLength: 24,
    gateway: "172.24.8.1",
    managed: true,
    inPool: false,
    batchId: "preview-batch-1",
    createdAt: "2026-01-01T00:00:00Z",
    lastError: "",
  },
  {
    name: "HypoMux vNIC 3",
    interfaceName: "vEthernet (HypoMux vNIC 3)",
    adapterId: "preview-adapter-3",
    macAddress: "00-15-5D-07-08-09",
    switchName: "LAN-External",
    state: "failed",
    address: "",
    prefixLength: 0,
    gateway: "",
    managed: true,
    inPool: false,
    batchId: "preview-batch-2",
    createdAt: "2026-01-01T00:00:00Z",
    lastError: "CreateVmNetworkAdapter: Access denied (0x80070005)",
  },
];

const PREVIEW_SWITCHES: HyperVSwitch[] = [
  {
    name: "LAN-External",
    type: "External",
    allowManagementOs: true,
    uplink: "Ethernet",
    netAdapterName: "Ethernet",
  },
  {
    name: "Lab-Internal",
    type: "Internal",
    allowManagementOs: false,
    uplink: "",
    netAdapterName: "",
  },
];

const errorText = (error: unknown): string =>
  error instanceof Error ? error.message : String(error);

/** Cap on the detail line so a stack trace cannot flood the banner. */
const ERROR_DETAIL_MAX = 300;

/**
 * Go wraps the real cause — "PowerShell exited with code 1", "powershell.exe
 * not found", a JSON decode failure — into a single error message. Showing only
 * the generic "check that Hyper-V is installed and elevated" advice left the
 * user with nothing actionable, so surface that cause verbatim instead. It is
 * already a single sentence starting with the cause (hyperv_adapter.go wraps as
 * `无法读取 Hyper-V 虚拟交换机：%v`), so whitespace is collapsed and the tail
 * capped rather than the cause being dropped.
 */
const errorDetail = (error: unknown): string => {
  const text = errorText(error).replace(/\s+/g, " ").trim();
  return text.length > ERROR_DETAIL_MAX ? `${text.slice(0, ERROR_DETAIL_MAX)}…` : text;
};

export type VirtualAdaptersPageProps = {
  /**
   * Accepted for shell convenience. The page is a top-level nav destination
   * (contract §4 puts it after `connections`), so there is nothing to go back
   * to and no back button is rendered; unknown props are simply ignored.
   */
  onBack?: () => void;
};

export function VirtualAdaptersPage(_props: VirtualAdaptersPageProps = {}) {
  const pageActive = usePageActive();
  const { t } = useI18n();
  const { notify } = useAppNotifications();

  // This page used to render its three quota lines through a local
  // text("中文", "English") helper, on the argument that live numbers make a
  // static key pointless. They do not: the language pack already interpolates
  // {used}/{total}, so the helper only guaranteed a page-local zh/en fork that
  // every other locale silently falls through. All prose here is a key now.

  const preview = import.meta.env.DEV && !isDesktopRuntime();

  const [adapters, setAdapters] = useState<HyperVAdapterStatus[]>(preview ? PREVIEW_ADAPTERS : []);
  const [switches, setSwitches] = useState<HyperVSwitch[]>(preview ? PREVIEW_SWITCHES : []);
  const [loading, setLoading] = useState(!preview);
  const [loadFailed, setLoadFailed] = useState(false);
  const [switchesFailed, setSwitchesFailed] = useState(false);
  // The real cause behind `switchesFailed`; `switchesFailed` alone only says
  // "the read failed", which is what produced the dead-end generic banner.
  const [switchesError, setSwitchesError] = useState<string | null>(null);
  const [unavailableReason, setUnavailableReason] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [creating, setCreating] = useState(false);
  const [creatingCount, setCreatingCount] = useState(1);
  const [dhcpSlow, setDhcpSlow] = useState(false);
  // Contract §3.6: freshly created adapters only enter the outbound pool after
  // the aggregation engine restarts, and Go writes the pool in a background
  // goroutine *after* Create() resolves — so the honest question is not "did a
  // create just succeed?" but "which of this session's adapters still claim pool
  // membership the list cannot vouch for?". Keeping the names (not a boolean) is
  // what lets every 3s poll re-derive the answer and let a settled adapter drop
  // out of the prompt on its own.
  const [createdNames, setCreatedNames] = useState<string[]>([]);
  // Batch selection. The page owns it (not the panel) because the page is what
  // calls RemoveAdapters and what has to survive a re-render between the tick
  // and the submit; the panel only draws the checkbox column and the count.
  const [selectionMode, setSelectionMode] = useState(false);
  // Ticks are stored as ROW KEYS, never as names. Hyper-V allows two adapters to
  // share a name, and a name-keyed tick lit BOTH of their checkboxes for one
  // click — the user asked to delete a card and the dialog then said two. The
  // row key is the same identity the table already keys its React rows with, so
  // the tick now means exactly "this row" all the way down to the prune step.
  const [tickedKeys, setTickedKeys] = useState<string[]>([]);
  // The rows a batch would actually put on the wire right now: ticked keys that
  // still resolve to a row the host would let us remove. Both halves are
  // re-evaluated on every poll, so a tick cannot outlive the row it names — and
  // a tick that was legal when the user made it never reaches the API if the row
  // has since turned protected or disappeared. Sending such a name would come
  // back as a per-row failure with a reason the user cannot act on: a failure
  // the frontend manufactured itself. The panel applies the same filter for the
  // checkbox column; this copy is what actually goes on the wire.
  const tickedRows = useMemo(() => {
    const wanted = new Set(tickedKeys.filter((key) => key));
    if (wanted.size === 0) return [] as HyperVAdapterStatus[];
    return adapters.filter((adapter) => wanted.has(adapterRowKey(adapter)) && isAdapterRemovable(adapter));
  }, [adapters, tickedKeys]);
  // Names are resolved here, at submit time, from the keys against the CURRENT
  // rows: `RemoveAdapters` takes names and nothing else, so this is the one
  // place a key becomes a name. `adapterNamesForKeys` de-duplicates twice — one
  // name per key, and one name per distinct name — because the backend cannot
  // tell two same-named adapters apart anyway, so submitting the name twice buys
  // nothing and only muddies the failure text.
  const ticked = useMemo(
    () => adapterNamesForKeys(adapters, tickedRows.map(adapterRowKey)),
    [adapters, tickedRows],
  );
  const pendingRestart = useMemo(
    () => pendingRestartNames(adapters, createdNames),
    [adapters, createdNames],
  );

  const requestSequence = useRef(0);
  const mounted = useRef(true);
  // In-flight read latch. A Retry click with no gate let the user fire three
  // overlapping list()/switches() pairs from a dead-looking page, and each one
  // bumped `requestSequence`, so the earliest response was discarded and the
  // page kept waiting on a promise nobody would use.
  const refreshInFlight = useRef(false);
  // Read-path-only latch. `notify` itself is replace-and-count (one toast with
  // ×N), which is what an *action* outcome wants: the 2nd create failure must be
  // announced again, or the user retries blind and leaves an orphan adapter on
  // the host (§3.7). A read, on the other hand, repeats every 3s forever, so its
  // toast is latched — see notifyNotifyOnce below.
  const notified = useRef(new Set<string>());
  const switchesRef = useRef(switches);
  switchesRef.current = switches;
  // t() is read through a ref so a locale swap cannot invalidate `refresh` and
  // restart the poll loop; the rendered text still uses the live t().
  const tRef = useRef(t);
  tRef.current = t;

  // Latched **for the 3s-polled reads only** (list / switches). Those repeat
  // forever while the service is down, so they announce themselves once per
  // mount; `notify`'s dedupeKey alone would re-open the same toast every 3s.
  // Action outcomes (create started/failed, remove done/failed) deliberately do
  // NOT come through here — see the `notifyOnce` removal in create()/remove().
  const notifyOnce = useCallback(
    (key: string, input: Omit<AppNotificationInput, "dedupeKey">) => {
      if (notified.current.has(key)) return;
      notified.current.add(key);
      notify({ ...input, dedupeKey: key });
    },
    [notify],
  );

  // The in-flight gate returns *before* `++requestSequence`: an overlapped call
  // must not be able to supersede the read that is actually running. That keeps
  // the last-writer-wins contract intact — a stale response is still discarded by
  // `sequence !== requestSequence.current` — while guaranteeing that every issued
  // read is the one that paints. The poll is already serialised by
  // startSerialPoll, so the only way to overlap is to click Retry, which is
  // exactly what this refuses.
  const refresh = useCallback(async (options: { manual?: boolean } = {}) => {
    if (refreshInFlight.current) return;
    refreshInFlight.current = true;
    const manual = options.manual === true;
    // Only a user-initiated read drives the "retrying…" affordance: a flag tied
    // to the poll would blink the button 20 times a minute.
    if (manual) setRefreshing(true);
    try {
      const sequence = ++requestSequence.current;
      if (preview) {
        setAdapters(PREVIEW_ADAPTERS);
        setSwitches(PREVIEW_SWITCHES);
        setLoading(false);
        return;
      }
      setLoading(true);
      // A switches() failure must not hide the adapter table, and vice versa.
      const [listResult, switchResult] = await Promise.allSettled([
        withServiceTimeout(
          appServices.virtualAdapters.list(),
          HYPERV_ADAPTER_READ_TIMEOUT_MS,
          tRef.current("virtual_adapters_loading"),
        ),
        withServiceTimeout(
          appServices.virtualAdapters.switches(),
          HYPERV_ADAPTER_READ_TIMEOUT_MS,
          tRef.current("virtual_adapters_switch"),
        ),
      ]);
      if (!mounted.current || sequence !== requestSequence.current) return;

      if (switchResult.status === "fulfilled") {
        setSwitches(switchResult.value ?? []);
        setSwitchesFailed(false);
        setSwitchesError(null);
      } else {
        // Keep the dropdown disabled rather than claim "no external switch" — but
        // keep the cause. It used to be dropped on the floor here, which left the
        // user with only the generic "is Hyper-V installed / am I elevated" text.
        const detail = errorDetail(switchResult.reason);
        setSwitches([]);
        setSwitchesFailed(true);
        setSwitchesError(detail);
        notifyOnce("virtual-adapters:error:switches", {
          title: tRef.current("virtual_adapters_switch_failed"),
          message: detail,
          intent: "error",
        });
      }

      if (listResult.status === "fulfilled") {
        const next = listResult.value ?? [];
        setAdapters(next);
        setLoadFailed(false);
        setUnavailableReason(null);
        // A create batch settles when no row still reports `creating`; until then
        // the banner stays up so the user knows DHCP is still being waited on.
        if (!next.some(isCreatingRow)) setCreating(false);
      } else {
        setLoadFailed(true);
        setUnavailableReason(errorText(listResult.reason));
        notifyOnce("virtual-adapters:error:list", {
          title: tRef.current("virtual_adapters_read_failed"),
          message: errorText(listResult.reason),
          intent: "error",
        });
      }
      setLoading(false);
    } finally {
      refreshInFlight.current = false;
      if (manual) setRefreshing(false);
    }
  }, [notifyOnce, preview]);

  useEffect(() => {
    if (!pageActive) return;
    mounted.current = true;
    void refresh();
    const stop = preview
      ? null
      : startSerialPoll(refresh, VIRTUAL_ADAPTER_POLL_INTERVAL_MS);
    return () => {
      mounted.current = false;
      requestSequence.current += 1;
      // Release the read latch next to the sequence bump: a read that hangs
      // while the user is on another tab would otherwise leave the page
      // permanently read-only on return, which is a worse outcome than a
      // duplicated poll tick.
      refreshInFlight.current = false;
      stop?.();
    };
  }, [pageActive, preview, refresh]);

  // Contract §3.5: the lease normally lands by ~45s. Wait past that and say so
  // instead of leaving a silent spinner. Keyed on the boolean, never on the
  // array identity, or the 3s poll would reset the timer forever.
  const waitingForDhcp = useMemo(
    () => adapters.some((adapter) => deriveAdapterState(adapter) === "dhcp"),
    [adapters],
  );
  useEffect(() => {
    setDhcpSlow(false);
    if (!waitingForDhcp) return;
    const timer = window.setTimeout(() => setDhcpSlow(true), VIRTUAL_ADAPTER_DHCP_SLOW_MS);
    return () => window.clearTimeout(timer);
  }, [waitingForDhcp]);

  // Live create budget. Go refuses a whole batch on capacity inside the ledger
  // transaction (hyperv_adapter.go:1355 -> hypervCheckBatchCapacity, :362),
  // i.e. *before* the elevated script runs, so an over-quota submit used to cost
  // a UAC prompt and still create nothing — and the user only saw the generic
  // batch error. The budget is recomputed from whatever List() last returned
  // (3s poll), never from a constant cached at mount, because another batch may
  // have been created anywhere else in the meantime.
  const quota = useMemo(() => createQuota(adapters), [adapters]);
  // Same reason as switchesRef/tRef: `create` must not be rebuilt by the 3s
  // poll, yet it has to read the freshest budget at click time.
  const quotaRef = useRef(quota);
  quotaRef.current = quota;
  // Real language-pack keys, not a local zh/en pair: `text()` broke every third
  // locale the moment a page grows one, and these three strings already had to
  // exist on both sides. Placeholder names match the quota fields below.
  const quotaLineText = t("virtual_adapters_quota_line", {
    used: quota.used,
    total: quota.total,
    remaining: quota.remaining,
  });
  const quotaExceededText = t("virtual_adapters_quota_exceeded", {
    used: quota.used,
    total: quota.total,
    remaining: quota.remaining,
    max: quota.perSubmitMax,
  });
  const quotaExhaustedText = t("virtual_adapters_quota_exhausted", {
    used: quota.used,
    total: quota.total,
  });
  // Read through a ref so the 3s poll cannot rebuild `create` (same reason as
  // tRef/switchesRef above); the rendered text still comes from the live `text`.
  const quotaNoticeRef = useRef(quotaExceededText);
  quotaNoticeRef.current = quota.exhausted ? quotaExhaustedText : quotaExceededText;

  const create = useCallback(
    async (switchName: string, count: number) => {
      if (preview) return;
      // Re-validate at the call site: the dialog is not the only door, and an
      // out-of-range count would trip the §3.7 batch cap on the Go side.
      const check = validateCreateCount(String(count), VIRTUAL_ADAPTER_MAX_BATCH, quotaRef.current);
      if (!check.ok) {
        // Silent for the shape errors the dialog already blocks; loud for
        // `over-quota`, because that one is invisible to the user until now.
        if (check.reason === "over-quota") {
          notify({
            title: tRef.current("virtual_adapters_create"),
            message: quotaNoticeRef.current,
            intent: "error",
            dedupeKey: "virtual-adapters:error:quota",
          });
        }
        return;
      }
      setBusy(true);
      setCreating(true);
      setCreatingCount(count);
      notify({
        title: tRef.current("virtual_adapters_create"),
        message: tRef.current("virtual_adapters_create_banner", { count }),
        intent: "info",
        dedupeKey: "virtual-adapters:info:create",
      });
      try {
        // 180s: UAC + Hyper-V module load are unbounded, DHCP alone is 60s.
        const next = await withServiceTimeout(
          appServices.virtualAdapters.create(switchName, count),
          HYPERV_ADAPTER_CREATE_TIMEOUT_MS,
          tRef.current("virtual_adapters_create"),
        );
        // These are deliberately above the `mounted` guard: the batch either
        // happened or it did not, both the toast and the restart prompt are
        // global, and a progress banner stranded across a page switch is the
        // same leak class as the busy flag below. Only the row write below can
        // be stale, so only it is guarded.
        if (next && !next.some(isCreatingRow)) setCreating(false);
        // §3.6 is not an assumption, it is a question the list answers every 3s:
        // remember which adapters this batch produced and let `pendingRestart`
        // (above) decide whether the user still has to restart the aggregation.
        // Create() can also partially fail, so a batch that came back empty must
        // not push the user into a pointless restart.
        if (next && next.length > 0) {
          setCreatedNames((previous) => {
            const added = next.map((adapter) => adapter.name).filter((name) => name);
            return added.length === 0 ? previous : Array.from(new Set([...previous, ...added]));
          });
        }
        notify({
          title: tRef.current("virtual_adapters_state_ready"),
          message: tRef.current("virtual_adapters_restart_hint"),
          intent: "info",
          dedupeKey: "virtual-adapters:info:create-done",
        });
        if (!mounted.current) return;
        if (next && next.length > 0) {
          // Create() returns only this batch — merge, never replace, or every
          // adapter already on screen blinks out until the next poll.
          setAdapters((previous) => mergeAdapterBatch(previous, next));
        }
      } catch (error) {
        // Also unguarded: leaving a stuck progress banner behind after the user
        // navigated away is the same leak class as the busy flag below.
        setCreating(false);
        if (!mounted.current) return;
        // Every failure is announced, not just the first: the latch that used to
        // sit here was written for the 3s poll, and on a create it meant the 2nd
        // failure flashed past silently (§3.7 — the card the batch already added
        // is kept, so a user who was never told will happily retry and leave
        // orphans behind). `notify` replaces and counts instead of stacking.
        notify({
          title: tRef.current("virtual_adapters_create_failed"),
          message: errorText(error),
          intent: "error",
          dedupeKey: "virtual-adapters:error:create",
        });
      } finally {
        // Unconditional: the poll effect's cleanup clears `mounted` as soon as
        // the user leaves the page, and a create can run for 180s. Guarding here
        // would strand `busy === true` and leave the whole page read-only until
        // the app restarts.
        setBusy(false);
      }
    },
    [preview],
  );

  const remove = useCallback(
    async (adapter: HyperVAdapterStatus) => {
      // The protection list is the only gate left. It used to be "is this card in
      // our ledger", which meant a card the user built by hand — exactly the one
      // they cannot delete from anywhere else in Windows — had no delete control
      // at all. Ownership now only changes the wording of the dialog; refusing
      // the call is reserved for the rows Hyper-V owns for itself.
      if (preview || !isAdapterRemovable(adapter)) return;
      setBusy(true);
      try {
        // Must go through removeAdapters, NOT the single-card remove(): that one
        // still carries the HypoMux naming gate, so a card the user built by hand
        // was refused with "xuni-05 不符合 HypoMux 命名规范" the moment they used
        // the per-row button — the exact thing this feature exists to fix. One
        // name in a batch is the same code path as five.
        //
        // The budget is the BATCH one, not HYPERV_ADAPTER_WRITE_TIMEOUT_MS: this is
        // literally a batch of one, and Go sizes that batch by item count
        // (hypervRemoveBatchTimeout(1) = hypervRemoveScriptTimeout = 90s,
        // hyperv_adapter.go:42/2037-2046). A 60s frontend budget expired first while
        // the elevated child was still deleting — withServiceTimeout only rejects the
        // awaiter (services.ts:268), so the host finished the work, the UI reported
        // failure, and the retry popped a second UAC prompt. The batch ceiling is the
        // one number that can never fall below the Go side, so both call sites use it.
        const results = await withServiceTimeout(
          appServices.virtualAdapters.removeAdapters([adapter.name]),
          HYPERV_ADAPTER_BATCH_TIMEOUT_MS,
          tRef.current("virtual_adapters_remove"),
        );
        if (!mounted.current) return;
        const { removed, failed } = partitionRemoveResults(results);
        if (removed.length > 0) {
          // Prune in place, never `await refresh()`: List() cannot reproduce the
          // `LastError` markers Go injects, so a full reload here would silently
          // wipe the restart hint every unpooled row is still carrying. The 3s
          // poll reconciles whatever the host disagrees about a moment later.
          // (Same discipline as mergeAdapterBatch above.)
          //
          // The third argument is this row's key — the only row we asked about.
          // Without it the prune is by name alone, and a second adapter sharing
          // the name would be erased from the table on the strength of a result
          // that says nothing about which object the script deleted.
          setAdapters((previous) => dropAdapters(previous, removed, [adapterRowKey(adapter)]));
        }
        if (failed.length > 0) {
          // The adapter is still on the host. Say so, and say why, rather than
          // reporting a success the user will contradict the moment they look.
          const row = failed[0];
          notify({
            title: tRef.current("virtual_adapters_remove_failed"),
            message: tRef.current("virtual_adapters_remove_failure_line", {
              name: row.name,
              reason: row.reason || tRef.current("virtual_adapters_remove_reason_unknown"),
            }),
            intent: "error",
            dedupeKey: "virtual-adapters:error:remove",
          });
          return;
        }
        notify({
          title: tRef.current("virtual_adapters_remove"),
          message: tRef.current("virtual_adapters_remove_body"),
          intent: "success",
          dedupeKey: "virtual-adapters:info:remove",
        });
      } catch (error) {
        if (!mounted.current) return;
        // Same reasoning as create(): a silent 2nd removal failure leaves the
        // adapter on the host and the user with no reason to try again.
        notify({
          title: tRef.current("virtual_adapters_remove_failed"),
          message: errorText(error),
          intent: "error",
          dedupeKey: "virtual-adapters:error:remove",
        });
      } finally {
        // Same leak as create(): a 60s remove can settle after the user left.
        setBusy(false);
      }
    },
    [preview],
  );

  const startSelection = useCallback(() => setSelectionMode(true), []);

  // Leaving multi-select always drops the ticks. Keeping them would leave a
  // hidden selection behind that silently re-arms the Delete-selected button
  // the next time the user enters the mode — and a batch that removes cards the
  // user cannot see is the one thing this dialog exists to prevent.
  const exitSelection = useCallback(() => {
    setSelectionMode(false);
    setTickedKeys([]);
  }, []);

  const toggleSelection = useCallback((adapter: HyperVAdapterStatus) => {
    if (!isAdapterRemovable(adapter)) return;
    // A row with no usable key (no DeviceId, no alias, no name) cannot be ticked
    // honestly — it could not be submitted either, so it is refused rather than
    // stored under a key that would match every other such row.
    const key = adapterRowKey(adapter);
    if (!key) return;
    setTickedKeys((previous) =>
      previous.includes(key) ? previous.filter((item) => item !== key) : [...previous, key],
    );
  }, []);

  // Select-all covers every removable row and nothing else. Ticking a protected
  // row here would build a batch the backend refuses per row, which is the one
  // outcome the batch dialog exists to avoid.
  const selectAllRemovable = useCallback(() => {
    setTickedKeys((previous) => {
      const removable = removableAdapters(adapters);
      const removableKeys = new Set(removable.map(adapterRowKey));
      const kept = previous.filter((key) => removableKeys.has(key));
      // Append whatever is not already ticked; `removableAdapters` keeps the
      // table order, so the list reads top-to-bottom like the screen does.
      const next = [...kept];
      for (const adapter of removable) {
        const key = adapterRowKey(adapter);
        if (key && !next.includes(key)) next.push(key);
      }
      return next;
    });
  }, [adapters]);

  /**
   * Batch removal: N adapters, one elevated run, one UAC prompt.
   *
   * The interesting part is the outcome. Go reports each adapter separately and
   * deliberately does NOT fail the whole call when one adapter cannot go — a
   * locked switch or a busy vNIC must not cost the user the other four. So a
   * returned array is never "failure": it is a mixed result, split here
   * (partitionRemoveResults) into "gone" and "still here"; the gone rows are
   * pruned and each surviving failure is announced with the backend's own
   * reason. A thrown error means the batch itself failed — Hyper-V unreadable,
   * platform unsupported — and takes the existing error path.
   */
  const removeSelected = useCallback(async () => {
    const names = ticked;
    if (preview || names.length === 0) return;
    // Snapshot the row identities BEFORE the await, not after it: the poll can
    // land while the elevated run is still going, and the rows this batch asked
    // about are the ones it asked about — whatever the table looks like when the
    // answer comes back.
    const submittedKeys = tickedRows.map(adapterRowKey).filter((key) => key);
    setBusy(true);
    try {
      const results = await withServiceTimeout(
        appServices.virtualAdapters.removeAdapters(names),
        HYPERV_ADAPTER_BATCH_TIMEOUT_MS,
        tRef.current("virtual_adapters_remove_selected", { count: names.length }),
      );
      const { removed, failed } = partitionRemoveResults(results);
      if (!mounted.current) return;
      if (removed.length > 0) {
        // Same reason as the single-remove path: prune, do not refresh.
        // `submittedKeys` keeps the prune on the rows we actually asked about.
        // Without it a same-named neighbour the user never ticked would vanish
        // on the strength of a result that names no object at all.
        setAdapters((previous) => dropAdapters(previous, removed, submittedKeys));
      }
      if (failed.length === 0) {
        notify({
          title: tRef.current("virtual_adapters_remove"),
          message: tRef.current("virtual_adapters_remove_selected_ok", { count: removed.length }),
          intent: "success",
          dedupeKey: "virtual-adapters:info:remove-selected",
        });
      } else {
        // `reason` is a runtime fact only the backend has (which cmdlet refused,
        // which object was locked). It rides through the language pack as a
        // {reason} placeholder — never translated, never summarised, never
        // replaced by a generic "failed" — so the toast says why each card is
        // still there.
        const lines = failed.map((row) =>
          tRef.current("virtual_adapters_remove_failure_line", {
            name: row.name,
            reason: row.reason || tRef.current("virtual_adapters_remove_reason_unknown"),
          }),
        );
        notify({
          title: tRef.current("virtual_adapters_remove_failed"),
          message: [
            tRef.current("virtual_adapters_remove_selected_partial", { count: removed.length }),
            ...lines,
          ].join("\n"),
          intent: "warning",
          // Its own key, not the single-card one: a batch that half worked and a
          // later single-card failure are different events and must not collapse
          // into one ×2 toast.
          dedupeKey: "virtual-adapters:error:remove-selected",
        });
      }
      // Selection is cleared on every settled outcome — including a partial
      // failure. The cards that failed are still on screen with their rows
      // intact, so re-ticking them is one click, while stale ticks would invite
      // a second batch over the same names.
      exitSelection();
    } catch (error) {
      if (!mounted.current) return;
      notify({
        title: tRef.current("virtual_adapters_remove_failed"),
        message: errorText(error),
        intent: "error",
        dedupeKey: "virtual-adapters:error:remove-selected",
      });
      // Same rule as the resolved outcomes: a batch that threw leaves no
      // trustworthy answer about which names the host still has, so the ticks
      // are dropped instead of inviting a blind re-send of the same list.
      exitSelection();
    } finally {
      // Unconditional, like create(): the batch can outlive the page.
      setBusy(false);
    }
  }, [exitSelection, preview, ticked, tickedRows]);

  return (
    <main className="virtual-adapters-page">
      <header className="page-heading">
        <div>
          <span className="section-kicker">Hyper-V</span>
          <h1>{t("virtual_adapters_title")}</h1>
          <p>{t("virtual_adapters_hint")}</p>
        </div>
      </header>

      {/* The create budget, always on screen: the dialog only validates the
          1–16 batch shape (HyperVAdapterPanel.tsx:141), so before this line the
          user had no way to learn that 16 no longer fits in the 32 total, or
          that the budget is gone. Hidden only while the list itself failed —
          an unknown list cannot claim a quota. */}
      {!loadFailed && (
        <MessageBar intent={quota.exhausted ? "error" : "info"}>
          <MessageBarBody>
            <span>{quotaLineText}</span>
            {quota.exhausted && <span className="virtual-adapter-error">{quotaExhaustedText}</span>}
          </MessageBarBody>
        </MessageBar>
      )}

      <HyperVAdapterPanel
        adapters={adapters}
        switches={switches}
        loading={loading}
        loadFailed={loadFailed}
        switchesFailed={switchesFailed}
        switchesError={switchesError}
        busy={busy}
        refreshing={refreshing}
        creating={creating}
        creatingCount={creatingCount}
        dhcpSlow={dhcpSlow}
        preview={preview}
        pendingRestart={pendingRestart}
        // Dismissal only clears what the user chose to ignore; the next poll still
        // re-derives the rest, and the next create re-arms the prompt.
        onDismissRestartHint={() => setCreatedNames([])}
        quota={quota}
        unavailableReason={unavailableReason}
        onRefresh={() => void refresh({ manual: true })}
        onCreate={(switchName, count) => void create(switchName, count)}
        onRemove={(adapter) => void remove(adapter)}
        selectionMode={selectionMode}
        selectedKeys={tickedKeys}
        onStartSelection={startSelection}
        onExitSelection={exitSelection}
        onToggleSelection={toggleSelection}
        onSelectAll={selectAllRemovable}
        onRemoveSelected={() => void removeSelected()}
      />
    </main>
  );
}

export default VirtualAdaptersPage;