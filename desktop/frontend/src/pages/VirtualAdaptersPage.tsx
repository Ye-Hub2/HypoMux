import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AppNotificationInput,
  useAppNotifications,
} from "../components/notifications/AppNotifications";
import { usePageActive } from "../components/shell/PageActivity";
import { HyperVAdapterPanel } from "../components/vnic/HyperVAdapterPanel";
import {
  deriveAdapterState,
  isCreatingRow,
  isManagedAdapter,
  mergeAdapterBatch,
  validateCreateCount,
  VIRTUAL_ADAPTER_DHCP_SLOW_MS,
  VIRTUAL_ADAPTER_POLL_INTERVAL_MS,
} from "../components/vnic/managementAdapters";
import { useI18n } from "../i18n/i18n";
import { isDesktopRuntime } from "../platform/runtime";
import {
  appServices,
  withServiceTimeout,
  HYPERV_ADAPTER_CREATE_TIMEOUT_MS,
  HYPERV_ADAPTER_READ_TIMEOUT_MS,
  HYPERV_ADAPTER_WRITE_TIMEOUT_MS,
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

  const preview = import.meta.env.DEV && !isDesktopRuntime();

  const [adapters, setAdapters] = useState<HyperVAdapterStatus[]>(preview ? PREVIEW_ADAPTERS : []);
  const [switches, setSwitches] = useState<HyperVSwitch[]>(preview ? PREVIEW_SWITCHES : []);
  const [loading, setLoading] = useState(!preview);
  const [loadFailed, setLoadFailed] = useState(false);
  const [switchesFailed, setSwitchesFailed] = useState(false);
  const [unavailableReason, setUnavailableReason] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [creating, setCreating] = useState(false);
  const [creatingCount, setCreatingCount] = useState(1);
  const [dhcpSlow, setDhcpSlow] = useState(false);
  // Contract §3.6: freshly created adapters do not enter the outbound pool
  // until the aggregation engine restarts. Go tries to deliver that fact
  // through `LastError` on the non-failed rows it returns from Create()
  // (hyperv_adapter.go:1238-1240), but the row renderer deliberately ignores
  // `LastError` on non-failed rows, so those two conditions are complementary
  // and the hint would never appear. The page therefore owns the prompt itself.
  const [restartRequired, setRestartRequired] = useState(false);

  const requestSequence = useRef(0);
  const mounted = useRef(true);
  const notified = useRef(new Set<string>());
  const switchesRef = useRef(switches);
  switchesRef.current = switches;
  // t() is read through a ref so a locale swap cannot invalidate `refresh` and
  // restart the poll loop; the rendered text still uses the live t().
  const tRef = useRef(t);
  tRef.current = t;

  // Contract: one toast per mount. Repeated failures replace nothing and stack
  // nothing — the dedupeKey alone would keep re-opening the same toast every
  // 3s while list() is down, so we also latch locally.
  const notifyOnce = useCallback(
    (key: string, input: Omit<AppNotificationInput, "dedupeKey">) => {
      if (notified.current.has(key)) return;
      notified.current.add(key);
      notify({ ...input, dedupeKey: key });
    },
    [notify],
  );

  const refresh = useCallback(async () => {
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
    } else {
      // Keep the dropdown disabled rather than claim "no external switch".
      setSwitches([]);
      setSwitchesFailed(true);
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
        title: tRef.current("virtual_adapters_state_failed"),
        message: errorText(listResult.reason),
        intent: "error",
      });
    }
    setLoading(false);
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

  const create = useCallback(
    async (switchName: string, count: number) => {
      if (preview) return;
      // Re-validate at the call site: the dialog is not the only door, and an
      // out-of-range count would trip the §3.7 batch cap on the Go side.
      if (!validateCreateCount(String(count)).ok) return;
      setBusy(true);
      setCreating(true);
      setCreatingCount(count);
      notifyOnce("virtual-adapters:info:create", {
        title: tRef.current("virtual_adapters_create"),
        message: tRef.current("virtual_adapters_create_banner", { count }),
        intent: "info",
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
        setRestartRequired(true);
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
        notifyOnce("virtual-adapters:error:create", {
          title: tRef.current("virtual_adapters_state_failed"),
          message: errorText(error),
          intent: "error",
        });
      } finally {
        // Unconditional: the poll effect's cleanup clears `mounted` as soon as
        // the user leaves the page, and a create can run for 180s. Guarding here
        // would strand `busy === true` and leave the whole page read-only until
        // the app restarts.
        setBusy(false);
      }
    },
    [notifyOnce, preview],
  );

  const remove = useCallback(
    async (adapter: HyperVAdapterStatus) => {
      // Contract §3.4: unregistered adapters belong to the user, never to us.
      if (preview || !isManagedAdapter(adapter)) return;
      setBusy(true);
      try {
        await withServiceTimeout(
          appServices.virtualAdapters.remove(adapter.name),
          HYPERV_ADAPTER_WRITE_TIMEOUT_MS,
          tRef.current("virtual_adapters_remove"),
        );
        if (!mounted.current) return;
        notifyOnce("virtual-adapters:info:remove", {
          title: tRef.current("virtual_adapters_remove"),
          message: tRef.current("virtual_adapters_remove_body"),
          intent: "success",
        });
        await refresh();
      } catch (error) {
        if (!mounted.current) return;
        notifyOnce("virtual-adapters:error:remove", {
          title: tRef.current("virtual_adapters_state_failed"),
          message: errorText(error),
          intent: "error",
        });
      } finally {
        // Same leak as create(): a 60s remove can settle after the user left.
        setBusy(false);
      }
    },
    [notifyOnce, preview, refresh],
  );

  return (
    <main className="virtual-adapters-page">
      <header className="page-heading">
        <div>
          <span className="section-kicker">Hyper-V</span>
          <h1>{t("virtual_adapters_title")}</h1>
          <p>{t("virtual_adapters_hint")}</p>
        </div>
      </header>

      <HyperVAdapterPanel
        adapters={adapters}
        switches={switches}
        loading={loading}
        loadFailed={loadFailed}
        switchesFailed={switchesFailed}
        busy={busy}
        creating={creating}
        creatingCount={creatingCount}
        dhcpSlow={dhcpSlow}
        preview={preview}
        restartRequired={restartRequired}
        onDismissRestartHint={() => setRestartRequired(false)}
        unavailableReason={unavailableReason}
        onRefresh={() => void refresh()}
        onCreate={(switchName, count) => void create(switchName, count)}
        onRemove={(adapter) => void remove(adapter)}
      />
    </main>
  );
}

export default VirtualAdaptersPage;