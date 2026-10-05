// Display-only panel for the Hyper-V virtual adapter page. It never calls the
// desktop service: the page (VirtualAdaptersPage) owns polling, elevation
// prompts and notifications, and hands this component a snapshot plus callbacks.
// That split keeps the interesting mapping (see managementAdapters.ts) testable
// without mocking Wails, exactly like MTUDetectionPage vs HealthPage.
import {
  Badge,
  Button,
  Checkbox,
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
  SelectAllOnRegular,
} from "@fluentui/react-icons";
import { useCallback, useEffect, useMemo, useState } from "react";
import { GlassSurface } from "../material/GlassSurface";
import { useI18n } from "../../i18n/i18n";
import type { HyperVAdapterStatus, HyperVSwitch } from "../../platform/services";
import {
  adapterRowKey,
  backendHintI18nKey,
  deriveAdapterState,
  describeSwitch,
  externalSwitches,
  formatAddressPrefix,
  formatLastError,
  formatMac,
  isAdapterRemovable,
  isCreatingRow,
  isInOutboundPool,
  isManagedAdapter,
  isSwitchSelectable,
  removableAdapters,
  splitBackendHint,
  summarizeAdapters,
  switchAvailability,
  validateCreateCount,
  VIRTUAL_ADAPTER_MAX_BATCH,
  VIRTUAL_ADAPTER_MAX_TOTAL,
  type CreateQuota,
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
  /**
   * The cause behind `switchesFailed`, verbatim from the desktop service. Shown
   * next to the generic hint so the user can tell "Hyper-V is missing" from
   * "the read was denied" from "the query script failed"; the generic sentence
   * alone cannot. Optional so the panel stays usable when the caller has none.
   */
  switchesError?: string | null;
  /** A create or remove request is in flight; every write control is disabled. */
  busy: boolean;
  /** A user-initiated read is in flight; guards the Retry buttons against double fire. */
  refreshing?: boolean;
  /** A create batch has not settled yet; drives the progress banner. */
  creating: boolean;
  creatingCount: number;
  /** A row has been waiting for DHCP longer than the slow threshold. */
  dhcpSlow: boolean;
  preview: boolean;
  /**
   * Names of adapters created in this session whose outbound-pool membership is
   * still only a claim from the row (contract §3.6). Owned by the page because it
   * is the page that knows which rows came from a `Create()` call here, and only
   * the page sees every poll; the panel just shows what is still outstanding.
   */
  pendingRestart: string[];
  onDismissRestartHint: () => void;
  /**
   * Managed-row budget for this host. Optional so a caller that has no ledger can
   * still render the dialog; when present the dialog greys out its submit button
   * before the page's own guard ever sees the click.
   */
  quota?: CreateQuota;
  unavailableReason?: string | null;
  onRefresh: () => void;
  onCreate: (switchName: string, count: number) => void;
  onRemove: (adapter: HyperVAdapterStatus) => void;
  /**
   * Multi-select mode is ON: the toolbar Delete button has been pressed and the
   * rows show a checkbox column. Optional so every existing caller (and every
   * existing test) renders in the normal single-select layout unchanged.
   */
  selectionMode?: boolean;
  /**
   * ROW KEYS (`adapterRowKey`) of the adapters currently ticked, not names.
   * Hyper-V can hold two adapters with the same name, and a name-keyed tick lit
   * both of their checkboxes for a single click. Ignored unless `selectionMode`.
   */
  selectedKeys?: readonly string[];
  onStartSelection?: () => void;
  /** Leave multi-select mode. The page also clears the ticks. */
  onExitSelection?: () => void;
  onToggleSelection?: (adapter: HyperVAdapterStatus) => void;
  /** Tick every removable row at once. Omitted => the button does not render. */
  onSelectAll?: () => void;
  /** Ask the page to delete every ticked row in one elevated run. */
  onRemoveSelected?: () => void;
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
  switchesError,
  busy,
  refreshing,
  creating,
  creatingCount,
  dhcpSlow,
  preview,
  pendingRestart,
  onDismissRestartHint,
  quota,
  unavailableReason,
  onRefresh,
  onCreate,
  onRemove,
  selectionMode = false,
  selectedKeys = [],
  onStartSelection,
  onExitSelection,
  onToggleSelection,
  onSelectAll,
  onRemoveSelected,
}: HyperVAdapterPanelProps) {
  const { t } = useI18n();
  const [createOpen, setCreateOpen] = useState(false);
  const [countDraft, setCountDraft] = useState("1");
  const [switchDraft, setSwitchDraft] = useState("");
  const [removeTarget, setRemoveTarget] = useState<HyperVAdapterStatus | null>(null);
  // The batch confirm is a separate state from the selection itself: the page
  // owns the ticks, this owns "the user is looking at the N names they are about
  // to delete". Closing it must NOT clear the selection — a user who backs out
  // keeps their ticks and can adjust them, which is why `onExitSelection` is a
  // different button from the dialog's cancel.
  const [batchOpen, setBatchOpen] = useState(false);
  // Identity is the row key, never the name: two same-named adapters are two
  // rows the user can act on separately, and a name-keyed Set lit both of them
  // for one tick. The page owns the keys and prunes the ones that stopped being
  // removable; this Set is only a lookup.
  const selected = useMemo(() => new Set(selectedKeys), [selectedKeys]);
  // Ticks that are no longer removable — a protected row that appeared, or a row
  // the 3s poll removed while it was ticked — must never reach the API, because
  // the backend would report them as per-row failures with a reason the user can
  // do nothing about. The page prunes the selection the same way; this copy is
  // what the dialog and the count actually render.
  const selectedRows = useMemo(
    () => adapters.filter((adapter) => isAdapterRemovable(adapter) && selected.has(adapterRowKey(adapter))),
    [adapters, selected],
  );
  const selectedForeign = useMemo(
    () => selectedRows.filter((adapter) => !isManagedAdapter(adapter)).length,
    [selectedRows],
  );
  // Everything the user is allowed to tick. Select-all means *all of these* —
  // never the protected rows, which would otherwise be ticked into a batch the
  // backend refuses and the user has to unpick one by one.
  const removableRows = useMemo(
    () => adapters.filter((adapter) => isAdapterRemovable(adapter)),
    [adapters],
  );
  const allSelected = removableRows.length > 0 && selectedRows.length === removableRows.length;
  // Which single-remove voice to use. Derived from the target rather than
  // remembered, so a row that the 3s poll updated from managed to foreign while
  // the dialog was open cannot keep showing the softer copy.
  const removeForeign = removeTarget ? !isManagedAdapter(removeTarget) : false;

  const external = useMemo(() => externalSwitches(switches), [switches]);
  const selectable = useMemo(() => external.filter(isSwitchSelectable), [external]);
  const summary = useMemo(() => summarizeAdapters(adapters), [adapters]);

  // The count is deliberately not pre-filled from the gap: the user types it.
  const openCreate = useCallback(() => {
    setCountDraft("1");
    setSwitchDraft(selectable[0]?.name ?? "");
    setCreateOpen(true);
  }, [selectable]);

  // Third argument only when the page actually has a ledger: a caller without one
  // keeps the exact two-argument behaviour (1..16 only) instead of being blocked
  // by an invented budget of zero.
  const count = validateCreateCount(countDraft, VIRTUAL_ADAPTER_MAX_BATCH, quota);
  const createDisabled = busy || preview || !selectable.length || !count.ok;
  // Same two sentences the page banner uses, so the dialog and the banner cannot
  // disagree about why a batch cannot be submitted.
  const quotaHint = !quota
    ? ""
    : quota.exhausted
      ? t("virtual_adapters_quota_exhausted", { used: quota.used, total: quota.total })
      : t("virtual_adapters_quota_exceeded", {
          used: quota.used,
          total: quota.total,
          remaining: quota.remaining,
          max: quota.remaining,
        });
  const quotaBlocked = quota !== undefined && !count.ok && count.reason === "over-quota";

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

  const closeBatch = useCallback((_: unknown, data: { open: boolean }) => {
    if (!data.open) setBatchOpen(false);
  }, []);

  // A switch can disappear under us while the dialog is open (unplugged uplink).
  useEffect(() => {
    if (createOpen && switchDraft && !selectable.some((item) => item.name === switchDraft)) {
      setSwitchDraft(selectable[0]?.name ?? "");
    }
  }, [createOpen, selectable, switchDraft]);

  // Three root causes, three sentences. "You have external switches but every one
  // of them has AllowManagementOS off" is the single most common real
  // misconfiguration and used to share a clause that named neither the cause nor
  // the fix, so users read it as "this host has no switch" and kept clicking
  // Create. The AllowManagementOS branch reuses the wording of
  // virtual_adapters_switch_hint (already shown in the dialog's hint) instead of
  // inventing a second, contradictory voice.
  const availability = switchAvailability(switches);
  // Only claim "no external switch" when the query actually succeeded and
  // returned nothing usable — a failed read is a different message.
  const switchNotice = !loadFailed && !switchesFailed && availability !== "ready";

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
          <Button
            icon={refreshing ? <Spinner size="tiny" /> : <ArrowSync20Regular />}
            disabled={loading || busy}
            onClick={onRefresh}
          >
            {t("virtual_adapters_refresh")}
          </Button>
          {/* Normal mode: the entry point into batch deletion. It sits right
              next to Refresh, as asked — deletion is a maintenance verb that
              belongs beside re-reading the list, not buried in a row menu.
              In selection mode the same slot becomes the two buttons that can
              actually finish or abandon the operation, so the toolbar never
              shows a Delete button that cannot do anything. */}
          {selectionMode ? (
            <>
              {onSelectAll && (
                <Button
                  icon={<SelectAllOnRegular />}
                  // Disabled once everything is ticked, not because the action is
                  // impossible but because the label would then lie: pressing
                  // "Select all" with everything already selected should not be
                  // the way to *clear* them.
                  disabled={busy || preview || allSelected || removableRows.length === 0}
                  onClick={() => onSelectAll()}
                >
                  {t("virtual_adapters_select_all")}
                </Button>
              )}
              <Button
                appearance="primary"
                icon={<Delete20Regular />}
                // N = 0 is not an error state, it is simply nothing chosen yet:
                // disabled rather than "click to get an empty dialog".
                disabled={busy || preview || selectedRows.length === 0}
                onClick={() => setBatchOpen(true)}
              >
                {t("virtual_adapters_remove_selected", { count: selectedRows.length })}
              </Button>
              <Button
                appearance="subtle"
                disabled={busy}
                onClick={() => {
                  setBatchOpen(false);
                  onExitSelection?.();
                }}
              >
                {t("routing_dialog_cancel")}
              </Button>
            </>
          ) : (
            <>
              <Button
                icon={<Delete20Regular />}
                // No removable row means there is nothing to select; offering the
                // mode anyway would show an empty checkbox column and a dead
                // Delete-selected button.
                disabled={busy || preview || !removableAdapters(adapters).length}
                onClick={() => onStartSelection?.()}
              >
                {t("virtual_adapters_delete")}
              </Button>
              <Button
                appearance="primary"
                icon={<Add20Regular />}
                disabled={createDisabled}
                onClick={openCreate}
              >
                {t("virtual_adapters_create")}
              </Button>
            </>
          )}
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
          button with nothing explaining why. The generic hint only narrows the
          search to "installed? elevated?" — the actual cause from the service is
          rendered underneath it, never in place of it, so neither hides the
          other. */}
      {switchesFailed && (
        <MessageBar intent="warning">
          <MessageBarBody>
            {t("virtual_adapters_switch_failed")}
            {switchesError && <span className="virtual-adapter-error">{switchesError}</span>}
            <Button
              size="small"
              disabled={refreshing}
              icon={refreshing ? <Spinner size="tiny" /> : undefined}
              onClick={onRefresh}
            >
              {refreshing ? t("virtual_adapters_retrying") : t("virtual_adapters_retry")}
            </Button>
          </MessageBarBody>
        </MessageBar>
      )}
      {switchNotice && (
        <MessageBar intent="warning">
          <MessageBarBody>
            {availability === "external-only"
              ? t("virtual_adapters_switch_external_only")
              : availability === "management-os-off"
                ? t("virtual_adapters_switch_management_os_off", { count: external.length })
                : availability === "none-usable"
                  ? t("virtual_adapters_switch_none_usable")
                  : t("virtual_adapters_no_switch")}
          </MessageBarBody>
        </MessageBar>
      )}
      {loadFailed && (
        <MessageBar intent="error">
          <MessageBarBody>
            {t("virtual_adapters_state_unknown")}
            <Button
              size="small"
              disabled={refreshing}
              icon={refreshing ? <Spinner size="tiny" /> : undefined}
              onClick={onRefresh}
            >
              {refreshing ? t("virtual_adapters_retrying") : t("virtual_adapters_retry")}
            </Button>
          </MessageBarBody>
        </MessageBar>
      )}
      {creating && (
        <div className="virtual-adapters-banner" role="status" aria-live="polite">
          <Spinner size="tiny" />
          <span>{t("virtual_adapters_create_banner", { count: Math.max(creatingCount, 1) })}</span>
        </div>
      )}
      {/* Stays up while any of this session's adapters is still only *claiming*
          pool membership (§3.6) — the pool write lands in a background goroutine
          after Create() returns, so an unconditional "restart the aggregation"
          prompt was either a lie (the write already landed) or silent (it never
          will). The page recomputes the list from every poll, so a row that stops
          claiming membership drops out of it by itself. Dismissed manually or
          superseded by the next create. */}
      {pendingRestart.length > 0 && (
        <div className="virtual-adapters-restart" role="status">
          <span>{t("virtual_adapters_restart_pending", { names: pendingRestart.join("、") })}</span>
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
          <div className={`virtual-adapters-row${selectionMode ? " is-selecting" : ""} is-header`} role="row">
            {/* Leading checkbox cell, present in both the header and every row
                (empty for protected rows) so the columns stay aligned. */}
            {selectionMode && (
              <span role="columnheader" className="virtual-adapters-row-select" />
            )}
            <span role="columnheader">{t("virtual_adapters_title")}</span>
            <span role="columnheader">MAC</span>
            <span role="columnheader">{t("virtual_adapters_switch")}</span>
            <span role="columnheader" aria-label={t("virtual_adapters_state_unknown")} />
          </div>
          {adapters.map((adapter) => {
            const state = deriveAdapterState(adapter);
            const busyRow = busy || isCreatingRow(adapter);
            const managed = isManagedAdapter(adapter);
            // One predicate decides the button, the checkbox and the disabled
            // state, so the three can never disagree about which rows are ours to
            // delete. `managed` no longer gates it — see isAdapterRemovable.
            const removable = isAdapterRemovable(adapter);
            const lastError = formatLastError(adapter);
            // §3.6 marker: "hypomux.hint.<code>" optionally followed by " | " and
            // detail only the backend has. A known code becomes language-pack copy
            // (detail appended as secondary text); anything else — free text, or a
            // marker this build does not know — is shown verbatim, because
            // mistranslating it would be worse than not translating it. The marker
            // deliberately rides a *ready* row, so it must not be gated on
            // state === "failed".
            const hint = splitBackendHint(lastError);
            const hintKey = hint.code ? backendHintI18nKey(hint.code) : undefined;
            const hintPrimary = hintKey ? t(hintKey) : lastError;
            const hintDetail = hintKey ? hint.detail : "";
            const showLastError = Boolean(lastError) && (state === "failed" || Boolean(hintKey));
            // A protected row must not react to a click at all: in selection mode
            // the row is the hit target, and silently ignoring a click on a row
            // that looks identical to its neighbours is worse than a dead
            // control you can see.
            const rowSelectable = selectionMode && removable && !busyRow && !preview;
            return (
              <div
                className={`virtual-adapters-row${selectionMode ? " is-selecting" : ""}${rowSelectable ? " is-clickable" : ""}`}
                role="row"
                key={adapterRowKey(adapter) || adapter.name}
                aria-busy={busyRow ? "true" : undefined}
                // Whole row is the hit target, matching the Home page where a
                // click anywhere on an adapter card toggles it — asking someone to
                // land on a 16px checkbox is precision we do not expect. The
                // checkbox stays the keyboard path, so this adds a shortcut rather
                // than replacing the accessible control.
                onClick={rowSelectable ? () => onToggleSelection?.(adapter) : undefined}
              >
                {selectionMode && (
                  <span
                    role="cell"
                    className="virtual-adapters-row-select"
                    // Without this the click reaches the row handler *after* the
                    // checkbox already toggled it: two toggles, no net change, and
                    // the checkbox appears to be broken.
                    onClick={(event) => event.stopPropagation()}
                  >
                    {/* A protected row gets no checkbox at all rather than a
                        disabled one: a greyed control next to a "system
                        reserved" badge invites the user to keep trying to select
                        it, and screen readers would announce it as an option
                        that exists. The rule is the backend's, mirrored in
                        managementAdapters.ts — both sides change together. */}
                    {removable && (
                      <Checkbox
                        checked={selected.has(adapterRowKey(adapter))}
                        disabled={busyRow || preview}
                        // The interface name goes in because the vNIC name is NOT
                        // unique: Hyper-V happily hosts two adapters called
                        // `xuni-01`, and two checkboxes announcing the identical
                        // label are indistinguishable to a screen reader — the user
                        // hears the same option twice and cannot say which one they
                        // are ticking. The interface alias is already printed on
                        // the row, so this adds no new vocabulary, only the
                        // disambiguator.
                        aria-label={`${t("virtual_adapters_select")} ${adapter.name} ${
                          adapter.interfaceName || ""
                        }`.trim()}
                        onChange={() => onToggleSelection?.(adapter)}
                      />
                    )}
                  </span>
                )}
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
                  {/* The only rows without a remove button, and the badge says
                      why rather than leaving the user to guess whether the app
                      is broken. The hint is title text, not body copy: it
                      explains the badge on hover/focus and keeps the row quiet. */}
                  {!removable && (
                    <Badge appearance="outline" color="warning" title={t("virtual_adapters_reserved_hint")}>
                      {t("virtual_adapters_reserved")}
                    </Badge>
                  )}
                  {state === "dhcp" && dhcpSlow && (
                    <small className="virtual-adapter-error">{t("virtual_adapters_dhcp_slow")}</small>
                  )}
                  {showLastError && (
                    <small className="virtual-adapter-error" role="alert">
                      {hintPrimary}
                      {hintDetail && <span className="virtual-adapter-detail">{hintDetail}</span>}
                    </small>
                  )}
                  {/* Every removable row carries its own remove button —
                      ledger membership no longer gates it (the user removed
                      that gate: "能检测到，能删除就加上移除按钮"). In selection
                      mode the per-row button steps aside for the checkbox, so
                      there is exactly one way to submit: the toolbar. */}
                  {removable && !selectionMode && (
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
              <Field label={t("virtual_adapters_count")} hint={t("virtual_adapters_count_hint", { max: quota?.perSubmitMax ?? VIRTUAL_ADAPTER_MAX_BATCH })}>
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
              {/* The dialog refuses to submit before the page's guard ever runs,
                  and says why in the same sentence the banner uses. */}
              {quotaBlocked && (
                <span className="virtual-adapter-error" role="alert">{quotaHint}</span>
              )}
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
              {/* Two voices, because these are two different acts. A managed card
                  is ours: removing it only hands back an IP lease we took. A
                  ledger-external card belongs to the user — HypoMux keeps no
                  record of how to rebuild it, there is no undo, and whatever the
                  card was wired to has to be repaired by hand. The second dialog
                  therefore says all three things and prints the identity fields,
                  because "delete this virtual NIC" is ambiguous the moment a
                  host shows five of them. */}
              {removeForeign ? (
                <>
                  <span className="virtual-adapter-error" role="alert">
                    {t("virtual_adapters_remove_foreign_body")}
                  </span>
                  <dl className="virtual-adapter-details">
                    <div>
                      <dt>{t("virtual_adapters_remove_field_interface")}</dt>
                      <dd>{removeTarget?.interfaceName}</dd>
                    </div>
                    <div>
                      <dt>MAC</dt>
                      <dd>{removeTarget ? formatMac(removeTarget) : ""}</dd>
                    </div>
                    <div>
                      <dt>{t("virtual_adapters_remove_field_address")}</dt>
                      <dd>{removeTarget ? formatAddressPrefix(removeTarget) : ""}</dd>
                    </div>
                  </dl>
                </>
              ) : (
                <span>{t("virtual_adapters_remove_body")}</span>
              )}
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

      {/* selectionMode is part of `open` on purpose: the page closes the mode
          after every settled batch (including a throw), and a confirm dialog
          left hanging over an empty list would block the toolbar. */}
      <Dialog open={batchOpen && selectionMode} onOpenChange={closeBatch}>
        <DialogSurface aria-label={t("virtual_adapters_remove_selected_title")}>
          <DialogBody>
            <DialogTitle>{t("virtual_adapters_remove_selected_title")}</DialogTitle>
            <DialogContent>
              <span>{t("virtual_adapters_remove_selected_body")}</span>
              {/* Every selected name is spelled out. A batch deletes all of them
                  in one elevated run — there is no second chance to notice the
                  wrong one — so the list is the whole point of the dialog, not a
                  summary of it. */}
              <ul className="virtual-adapter-selection">
                {selectedRows.map((row) => (
                  // Row key, not name — see the aria-label note on the checkbox.
                  // Two same-named rows are separately addressable rows here, so
                  // giving React a duplicate key makes it warn and can mis-patch
                  // the wrong <li> on re-render.
                  <li key={adapterRowKey(row) || row.name}>
                    <strong>{row.name}</strong>
                    <small>{row.interfaceName}</small>
                    {!isManagedAdapter(row) && (
                      <span className="virtual-adapter-flag">{t("virtual_adapters_foreign")}</span>
                    )}
                  </li>
                ))}
              </ul>
              {selectedForeign > 0 && (
                <span className="virtual-adapter-error" role="alert">
                  {t("virtual_adapters_remove_selected_warning", { count: selectedForeign })}
                </span>
              )}
            </DialogContent>
            <DialogActions>
              {/* Cancel closes the dialog but keeps the ticks: the user backed
                  out to change the selection, not to abandon it. The toolbar's
                  own cancel is what clears them. */}
              <Button disabled={busy} onClick={() => setBatchOpen(false)}>
                {t("routing_dialog_cancel")}
              </Button>
              <Button
                appearance="primary"
                disabled={busy || preview || selectedRows.length === 0}
                icon={busy ? <Spinner size="tiny" /> : <Delete20Regular />}
                onClick={() => {
                  setBatchOpen(false);
                  onRemoveSelected?.();
                }}
              >
                {t("virtual_adapters_remove_selected", { count: selectedRows.length })}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </GlassSurface>
  );
}