// Display-only panel for the Hyper-V virtual adapter page. It never calls the
// desktop service: the page (VirtualAdaptersPage) owns polling, elevation
// prompts and notifications, and hands this component a snapshot plus callbacks.
// That split keeps the interesting mapping (see managementAdapters.ts) testable
// without mocking Wails, exactly like MTUDetectionPage vs HealthPage.
import {
  Badge,
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Dropdown,
  Field,
  Input,
  MessageBar,
  MessageBarBody,
  Option,
  Spinner,
} from "@fluentui/react-components";
import {
  Add20Regular,
  ArrowSync20Regular,
  Delete20Regular,
  Dismiss20Regular,
} from "@fluentui/react-icons";
import { useCallback, useEffect, useMemo, useState } from "react";
import { GlassSurface } from "../material/GlassSurface";
import { useI18n } from "../../i18n/i18n";
import type { HyperVAdapterStatus, HyperVSwitch } from "../../platform/services";
import {
  adapterRowKey,
  deriveAdapterState,
  describeSwitch,
  externalSwitches,
  formatAddressPrefix,
  formatLastError,
  formatMac,
  isCreatingRow,
  isInOutboundPool,
  isManagedAdapter,
  isSwitchSelectable,
  summarizeAdapters,
  validateCreateCount,
  VIRTUAL_ADAPTER_MAX_BATCH,
  VIRTUAL_ADAPTER_MAX_TOTAL,
  type DerivedAdapterState,
} from "./managementAdapters";
import "./vnic.css";

export type HyperVAdapterPanelProps = {
  adapters: HyperVAdapterStatus[];
  switches: HyperVSwitch[];
  loading: boolean;
  loadFailed: boolean;
  /** `switches()` itself failed; suppress the "no external switch" claim. */
  switchesFailed: boolean;
  /** A create or remove request is in flight; every write control is disabled. */
  busy: boolean;
  /** A create batch has not settled yet; drives the progress banner. */
  creating: boolean;
  creatingCount: number;
  /** A row has been waiting for DHCP longer than the slow threshold. */
  dhcpSlow: boolean;
  preview: boolean;
  /**
   * The last successful create added cards that stay out of the outbound pool
   * until the aggregation engine restarts (contract §3.6). Owned by the page
   * because the Go `LastError` channel that also carries this fact is filtered
   * out of non-failed rows below.
   */
  restartRequired: boolean;
  onDismissRestartHint: () => void;
  unavailableReason?: string | null;
  onRefresh: () => void;
  onCreate: (switchName: string, count: number) => void;
  onRemove: (adapter: HyperVAdapterStatus) => void;
};

const stateLabelKey: Record<DerivedAdapterState, string> = {
  creating: "virtual_adapters_state_creating",
  dhcp: "virtual_adapters_state_dhcp",
  ready: "virtual_adapters_state_ready",
  failed: "virtual_adapters_state_failed",
  unknown: "virtual_adapters_state_unknown",
};

const stateColor: Record<DerivedAdapterState, "informative" | "warning" | "success" | "danger"> = {
  creating: "warning",
  dhcp: "warning",
  ready: "success",
  failed: "danger",
  unknown: "informative",
};

export function HyperVAdapterPanel({
  adapters,
  switches,
  loading,
  loadFailed,
  switchesFailed,
  busy,
  creating,
  creatingCount,
  dhcpSlow,
  preview,
  restartRequired,
  onDismissRestartHint,
  unavailableReason,
  onRefresh,
  onCreate,
  onRemove,
}: HyperVAdapterPanelProps) {
  const { t } = useI18n();
  const [createOpen, setCreateOpen] = useState(false);
  const [countDraft, setCountDraft] = useState("1");
  const [switchDraft, setSwitchDraft] = useState("");
  const [removeTarget, setRemoveTarget] = useState<HyperVAdapterStatus | null>(null);

  const external = useMemo(() => externalSwitches(switches), [switches]);
  const selectable = useMemo(() => external.filter(isSwitchSelectable), [external]);
  const summary = useMemo(() => summarizeAdapters(adapters), [adapters]);

  // The count is deliberately not pre-filled from the gap: the user types it.
  const openCreate = useCallback(() => {
    setCountDraft("1");
    setSwitchDraft(selectable[0]?.name ?? "");
    setCreateOpen(true);
  }, [selectable]);

  const count = validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH);
  const createDisabled = busy || preview || !selectable.length || !count.ok;

  const submitCreate = useCallback(() => {
    if (!count.ok || !switchDraft) return;
    setCreateOpen(false);
    onCreate(switchDraft, count.value);
  }, [count, onCreate, switchDraft]);

  const closeCreate = useCallback((_: unknown, data: { open: boolean }) => {
    if (!data.open) setCreateOpen(false);
  }, []);

  const closeRemove = useCallback((_: unknown, data: { open: boolean }) => {
    if (!data.open) setRemoveTarget(null);
  }, []);

  // A switch can disappear under us while the dialog is open (unplugged uplink).
  useEffect(() => {
    if (createOpen && switchDraft && !selectable.some((item) => item.name === switchDraft)) {
      setSwitchDraft(selectable[0]?.name ?? "");
    }
  }, [createOpen, selectable, switchDraft]);

  const noExternalSwitch = switches.length > 0 && external.length === 0;
  const noUsableSwitch = external.length > 0 && selectable.length === 0;
  // Only claim "no external switch" when the query actually succeeded and
  // returned nothing usable — a failed read is a different message.
  const switchNotice = !loadFailed && !switchesFailed && (switches.length === 0 || noExternalSwitch || noUsableSwitch);

  return (
    <GlassSurface className="virtual-adapters-surface">
      <div className="virtual-adapters-toolbar">
        <div>
          <Badge appearance="outline" color={summary.ready === summary.total ? "success" : "subtle"}>
            {t("virtual_adapters_summary", { ready: summary.ready, total: summary.total })}
          </Badge>
          <span>{t("virtual_adapters_total_hint", { used: summary.total, total: VIRTUAL_ADAPTER_MAX_TOTAL })}</span>
        </div>
        <div>
          <Button icon={<ArrowSync20Regular />} disabled={loading || busy} onClick={onRefresh}>
            {t("virtual_adapters_refresh")}
          </Button>
          <Button
            appearance="primary"
            icon={<Add20Regular />}
            disabled={createDisabled}
            onClick={openCreate}
          >
            {t("virtual_adapters_create")}
          </Button>
        </div>
      </div>

      {preview && (
        <MessageBar intent="info">
          <MessageBarBody>{t("virtual_adapters_preview")}</MessageBarBody>
        </MessageBar>
      )}
      {unavailableReason && (
        <MessageBar intent="warning">
          <MessageBarBody>{t("virtual_adapters_unavailable", { reason: unavailableReason })}</MessageBarBody>
        </MessageBar>
      )}
      {/* A failed switches() read is a different problem from an empty switch
          list: the host may have no Hyper-V at all, or deny the enumeration.
          Suppressing every hint here used to leave a dead-end greyed-out Create
          button with nothing explaining why. */}
      {switchesFailed && (
        <MessageBar intent="warning">
          <MessageBarBody>
            {t("virtual_adapters_switch_failed")}
            <Button size="small" onClick={onRefresh}>{t("virtual_adapters_retry")}</Button>
          </MessageBarBody>
        </MessageBar>
      )}
      {switchNotice && (
        <MessageBar intent="warning">
          <MessageBarBody>
            {noExternalSwitch || noUsableSwitch ? t("virtual_adapters_switch_external_only") : t("virtual_adapters_no_switch")}
          </MessageBarBody>
        </MessageBar>
      )}
      {loadFailed && (
        <MessageBar intent="error">
          <MessageBarBody>
            {t("virtual_adapters_state_unknown")}
            <Button size="small" onClick={onRefresh}>{t("virtual_adapters_retry")}</Button>
          </MessageBarBody>
        </MessageBar>
      )}
      {creating && (
        <div className="virtual-adapters-banner" role="status" aria-live="polite">
          <Spinner size="tiny" />
          <span>{t("virtual_adapters_create_banner", { count: Math.max(creatingCount, 1) })}</span>
        </div>
      )}
      {/* Stays up after the batch settles, because "restart the aggregation
          engine" is the next action the user has to take (§3.6) and nothing on
          screen says so. Dismissed manually or replaced by the next create. */}
      {restartRequired && (
        <div className="virtual-adapters-restart" role="status">
          <span>{t("virtual_adapters_restart_hint")}</span>
          <Button
            size="small"
            appearance="subtle"
            icon={<Dismiss20Regular />}
            aria-label={t("routing_dialog_cancel")}
            onClick={onDismissRestartHint}
          />
        </div>
      )}

      {loading && adapters.length === 0 ? (
        <div className="virtual-adapters-empty">
          <Spinner label={t("virtual_adapters_loading")} />
        </div>
      ) : adapters.length === 0 ? (
        <div className="virtual-adapters-empty">
          <span>{t("virtual_adapters_empty")}</span>
          <Button appearance="primary" icon={<Add20Regular />} disabled={createDisabled} onClick={openCreate}>
            {t("virtual_adapters_create")}
          </Button>
        </div>
      ) : (
        <div className="virtual-adapters-list" role="table" aria-label={t("virtual_adapters_title")}>
          <div className="virtual-adapters-row is-header" role="row">
            <span role="columnheader">{t("virtual_adapters_title")}</span>
            <span role="columnheader">MAC</span>
            <span role="columnheader">{t("virtual_adapters_switch")}</span>
            <span role="columnheader" aria-label={t("virtual_adapters_state_unknown")} />
          </div>
          {adapters.map((adapter) => {
            const state = deriveAdapterState(adapter);
            const busyRow = busy || isCreatingRow(adapter);
            const managed = isManagedAdapter(adapter);
            const lastError = formatLastError(adapter);
            return (
              <div
                className="virtual-adapters-row"
                role="row"
                key={adapterRowKey(adapter) || adapter.name}
                aria-busy={busyRow ? "true" : undefined}
              >
                <span role="cell">
                  <strong>{adapter.name}</strong>
                  <small>{adapter.interfaceName}</small>
                </span>
                <span role="cell">
                  {formatMac(adapter)}
                  <small>{formatAddressPrefix(adapter)}</small>
                </span>
                <span role="cell">{adapter.switchName}</span>
                <span role="cell" className="virtual-adapters-row-actions">
                  <Badge appearance="tint" color={stateColor[state]}>
                    {t(stateLabelKey[state])}
                  </Badge>
                  {isInOutboundPool(adapter) && (
                    <Badge appearance="tint" color="informative">{t("virtual_adapters_pool")}</Badge>
                  )}
                  {!managed && (
                    <Badge appearance="outline" color="subtle">{t("virtual_adapters_foreign")}</Badge>
                  )}
                  {state === "dhcp" && dhcpSlow && (
                    <small className="virtual-adapter-error">{t("virtual_adapters_dhcp_slow")}</small>
                  )}
                  {state === "failed" && lastError && (
                    <small className="virtual-adapter-error" role="alert">{lastError}</small>
                  )}
                  {/* Contract §3.4: unregistered adapters are never deleted. */}
                  {managed && (
                    <Button
                      size="small"
                      appearance="subtle"
                      icon={<Delete20Regular />}
                      disabled={busyRow || preview}
                      onClick={() => setRemoveTarget(adapter)}
                    >
                      {t("virtual_adapters_remove")}
                    </Button>
                  )}
                </span>
              </div>
            );
          })}
        </div>
      )}

      <Dialog open={createOpen} onOpenChange={closeCreate}>
        <DialogSurface aria-label={t("virtual_adapters_create")}>
          <DialogBody>
            <DialogTitle>{t("virtual_adapters_create")}</DialogTitle>
            <DialogContent className="virtual-adapter-fields">
              <Field label={t("virtual_adapters_switch")} hint={t("virtual_adapters_switch_hint")}>
                <Dropdown
                  value={switchDraft}
                  selectedOptions={switchDraft ? [switchDraft] : []}
                  placeholder={t("virtual_adapters_no_switch")}
                  disabled={busy || preview || selectable.length === 0}
                  onOptionSelect={(_, data) => {
                    if (data.optionValue) setSwitchDraft(data.optionValue);
                  }}
                >
                  {external.map((item) => (
                    <Option
                      key={item.name}
                      value={item.name}
                      text={describeSwitch(item)}
                      disabled={!isSwitchSelectable(item)}
                    >
                      {describeSwitch(item)}
                    </Option>
                  ))}
                </Dropdown>
              </Field>
              <Field label={t("virtual_adapters_count")} hint={t("virtual_adapters_count_hint", { max: VIRTUAL_ADAPTER_MAX_BATCH })}>
                <Input
                  type="number"
                  min={1}
                  max={VIRTUAL_ADAPTER_MAX_BATCH}
                  value={countDraft}
                  disabled={busy || preview}
                  aria-label={t("virtual_adapters_count")}
                  aria-invalid={countDraft.trim() && !count.ok ? true : undefined}
                  onChange={(_, data) => setCountDraft(data.value)}
                />
              </Field>
              <span className="virtual-adapter-hint">{t("virtual_adapters_restart_hint")}</span>
            </DialogContent>
            <DialogActions>
              <Button disabled={busy} onClick={() => setCreateOpen(false)}>{t("routing_dialog_cancel")}</Button>
              <Button appearance="primary" disabled={createDisabled} onClick={submitCreate}>
                {t("virtual_adapters_create")}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      <Dialog open={removeTarget !== null} onOpenChange={closeRemove}>
        <DialogSurface aria-label={t("virtual_adapters_remove_title")}>
          <DialogBody>
            <DialogTitle>{t("virtual_adapters_remove_title")}</DialogTitle>
            <DialogContent>
              <span className="virtual-adapter-intro">
                {removeTarget ? `${removeTarget.name} · ${removeTarget.interfaceName}` : ""}
              </span>
              <span>{t("virtual_adapters_remove_body")}</span>
            </DialogContent>
            <DialogActions>
              <Button disabled={busy} icon={<Dismiss20Regular />} onClick={() => setRemoveTarget(null)}>
                {t("routing_dialog_cancel")}
              </Button>
              <Button
                appearance="primary"
                disabled={busy || preview}
                icon={busy ? <Spinner size="tiny" /> : <Delete20Regular />}
                onClick={() => {
                  const target = removeTarget;
                  setRemoveTarget(null);
                  if (target) onRemove(target);
                }}
              >
                {t("virtual_adapters_remove")}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </GlassSurface>
  );
}