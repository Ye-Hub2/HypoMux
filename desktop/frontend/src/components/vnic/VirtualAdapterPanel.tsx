import {
  Badge,
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Input,
  Spinner,
} from "@fluentui/react-components";
import { Add20Regular, Delete20Regular } from "@fluentui/react-icons";
import { useCallback, useEffect, useRef, useState } from "react";
import { appServices, withServiceTimeout, type VirtualAdapterStatus } from "../../platform/services";
import "./vnic.css";

// The desktop service owns the real defaults (interfaceName empty -> "HypoMux-VNIC",
// address empty -> 10.66.0.1, prefix 24, MTU 1420). The dialog mirrors them so the
// user sees exactly what would be created, and so a browser build can be reviewed.
export const VIRTUAL_ADAPTER_DEFAULT_NAME = "HypoMux-VNIC";
export const VIRTUAL_ADAPTER_DEFAULT_ADDRESS = "10.66.0.1";

// Mirrors the service-side limit; the dialog must not stay busy forever.
export const VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS = 60_000;

export type VirtualAdapterPanelProps = {
  locale: "zh" | "en";
  text: (zh: string, en: string) => string;
  notifySuccess: (message: string) => void;
  notifyError: (message: string, retry?: () => void) => void;
  disabled?: boolean;
  className?: string;
};

type PanelState = "unknown" | "absent" | "creating" | "present" | "removing" | "failed";

const panelState = (value: string | undefined): PanelState => {
  switch (value) {
    case "absent":
    case "creating":
    case "present":
    case "removing":
    case "failed":
      return value;
    default:
      return "unknown";
  }
};

export const isValidIPv4 = (value: string): boolean => {
  const octets = value.trim().split(".");
  if (octets.length !== 4) return false;
  return octets.every((octet) => /^\d{1,3}$/.test(octet) && Number(octet) <= 255);
};

const stateText = (
  state: PanelState,
  text: (zh: string, en: string) => string,
): string => {
  switch (state) {
    case "absent":
      return text("未创建", "Not created");
    case "creating":
      return text("创建中…", "Creating…");
    case "present":
      return text("已存在", "Present");
    case "removing":
      return text("移除中…", "Removing…");
    case "failed":
      return text("创建失败", "Failed");
    default:
      return text("状态未知", "Unknown");
  }
};

const stateBadgeColour = (state: PanelState): "danger" | "warning" | "success" | "informative" => {
  if (state === "present") return "success";
  if (state === "failed") return "danger";
  if (state === "creating" || state === "removing") return "warning";
  return "informative";
};

const formatCreatedAt = (value: string, locale: "zh" | "en") => {
  const trimmed = value.trim();
  if (!trimmed) return "—";
  const parsed = new Date(trimmed);
  if (Number.isNaN(parsed.getTime())) return trimmed;
  return parsed.toLocaleString(locale === "en" ? "en-US" : "zh-CN");
};

export function VirtualAdapterPanel({
  locale,
  text,
  notifySuccess,
  notifyError,
  disabled = false,
  className,
}: VirtualAdapterPanelProps) {
  const [status, setStatus] = useState<VirtualAdapterStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadFailed, setLoadFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [createOpen, setCreateOpen] = useState(false);
  const [removeOpen, setRemoveOpen] = useState(false);
  const [draftName, setDraftName] = useState(VIRTUAL_ADAPTER_DEFAULT_NAME);
  const [draftAddress, setDraftAddress] = useState(VIRTUAL_ADAPTER_DEFAULT_ADDRESS);
  const [addressError, setAddressError] = useState("");
  const mounted = useRef(true);
  // The translator is recreated on every render; keeping it in a ref lets the
  // status request run exactly once per mount.
  const textRef = useRef(text);
  textRef.current = text;
  const notifySuccessRef = useRef(notifySuccess);
  notifySuccessRef.current = notifySuccess;
  const notifyErrorRef = useRef(notifyError);
  notifyErrorRef.current = notifyError;

  const refresh = useCallback(async () => {
    try {
      const next = await appServices.virtualAdapter.status();
      if (!mounted.current) return;
      setStatus(next);
      setLoadFailed(false);
    } catch {
      if (!mounted.current) return;
      setLoadFailed(true);
    } finally {
      if (mounted.current) setLoading(false);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => {
      mounted.current = false;
    };
  }, [refresh]);

  const state = panelState(status?.state);
  const operationBusy = busy || state === "creating" || state === "removing";
  const controlsDisabled = disabled || operationBusy;

  const openCreate = useCallback(() => {
    setDraftName(VIRTUAL_ADAPTER_DEFAULT_NAME);
    setDraftAddress(VIRTUAL_ADAPTER_DEFAULT_ADDRESS);
    setAddressError("");
    setCreateOpen(true);
  }, []);

  const create = useCallback(async () => {
    const name = draftName.trim() || VIRTUAL_ADAPTER_DEFAULT_NAME;
    const address = draftAddress.trim() || VIRTUAL_ADAPTER_DEFAULT_ADDRESS;
    if (!isValidIPv4(address)) {
      setAddressError(textRef.current("请输入合法的 IPv4 地址，例如 10.66.0.1。", "Enter a valid IPv4 address, for example 10.66.0.1."));
      return;
    }
    setAddressError("");
    setBusy(true);
    try {
      const next = await withServiceTimeout(
        appServices.virtualAdapter.create(name, address),
        VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS,
        textRef.current("创建虚拟网卡超时", "Creating the virtual adapter timed out"),
      );
      if (!mounted.current) return;
      setStatus(next);
      setLoadFailed(false);
      setCreateOpen(false);
      notifySuccessRef.current(
        textRef.current(`虚拟网卡 ${next?.interfaceName || name} 已创建。`, `Virtual adapter ${next?.interfaceName || name} was created.`),
      );
    } catch (error) {
      if (!mounted.current) return;
      notifyErrorRef.current(String(error), () => {
        void create();
      });
    } finally {
      if (mounted.current) setBusy(false);
    }
  }, [draftAddress, draftName]);

  const remove = useCallback(async () => {
    setBusy(true);
    try {
      const next = await withServiceTimeout(
        appServices.virtualAdapter.remove(),
        VIRTUAL_ADAPTER_OPERATION_TIMEOUT_MS,
        textRef.current("移除虚拟网卡超时", "Removing the virtual adapter timed out"),
      );
      if (!mounted.current) return;
      setStatus(next);
      setLoadFailed(false);
      setRemoveOpen(false);
      notifySuccessRef.current(textRef.current("虚拟网卡已移除。", "The virtual adapter was removed."));
    } catch (error) {
      if (!mounted.current) return;
      notifyErrorRef.current(String(error), () => {
        void remove();
      });
    } finally {
      if (mounted.current) setBusy(false);
    }
  }, []);

  const summary = status
    ? text(
      `虚拟网卡 · ${stateText(state, text)}`,
      `Virtual adapter · ${stateText(state, text)}`,
    )
    : text("虚拟网卡", "Virtual adapter");

  return (
    <section className={className ?? "virtual-adapter-section"} aria-labelledby="virtual-adapter-title">
      <div className="network-section-heading">
        <div>
          <span className="section-kicker">{text("独立虚拟网卡", "Dedicated virtual adapter")}</span>
          <div className="network-section-title-row">
            <h1 id="virtual-adapter-title">{text("虚拟网卡", "Virtual adapter")}</h1>
          </div>
        </div>
        <div className="network-section-actions">
          {status && !loading && (
            <Badge appearance="tint" color={stateBadgeColour(state)} aria-live="polite">
              {stateText(state, text)}
            </Badge>
          )}
          <Button
            size="small"
            appearance="primary"
            icon={<Add20Regular />}
            disabled={controlsDisabled}
            onClick={openCreate}
          >
            {text("创建虚拟网卡", "Create virtual adapter")}
          </Button>
          {state === "present" && (
            <Button
              size="small"
              appearance="subtle"
              icon={busy ? <Spinner size="tiny" /> : <Delete20Regular />}
              disabled={controlsDisabled}
              onClick={() => setRemoveOpen(true)}
            >
              {text("移除虚拟网卡", "Remove virtual adapter")}
            </Button>
          )}
        </div>
      </div>
      <div className="virtual-adapter-body hm-card" aria-busy={loading || operationBusy}>
        {loading ? (
          <Spinner label={text("正在读取虚拟网卡状态", "Reading virtual adapter status")} />
        ) : (
          <>
            <strong>{summary}</strong>
            <span>
              {text(
                "虚拟网卡由桌面核心按需创建，退出程序后不会常驻系统。",
                "The desktop core creates this adapter on demand; it does not persist after the app exits.",
              )}
            </span>
            {loadFailed && (
              <span>
                {text(
                  "无法读取虚拟网卡状态，请确认桌面服务正在运行。",
                  "Could not read the virtual adapter status. Make sure the desktop service is running.",
                )}
              </span>
            )}
            {(status?.interfaceName || status?.address) && (
              <span className="virtual-adapter-details">
                <span>{text("名称", "Name")}: <strong>{status?.interfaceName || "—"}</strong></span>
                <span>{text("地址", "Address")}: <strong>{status?.address ? `${status.address}/${status.prefixLength || 24}` : "—"}</strong></span>
                <span>MTU: <strong>{status?.mtu ? String(status.mtu) : "—"}</strong></span>
                {(status?.createdAt || state === "present") && (
                  <span>{text("创建时间", "Created")}: <strong>{formatCreatedAt(status?.createdAt ?? "", locale)}</strong></span>
                )}
                {status?.adapterGuid && <span>GUID: <strong>{status.adapterGuid}</strong></span>}
              </span>
            )}
            {status?.lastError && (
              <span className="virtual-adapter-error">{text("最后一次错误", "Last error")}: {status.lastError}</span>
            )}
            {loadFailed && (
              <Button appearance="secondary" disabled={operationBusy} onClick={() => void refresh()}>
                {text("重试", "Retry")}
              </Button>
            )}
          </>
        )}
      </div>

      <Dialog
        open={createOpen}
        onOpenChange={(_, data) => {
          if (!data.open && busy) return;
          if (!data.open) {
            setAddressError("");
            setCreateOpen(false);
          }
        }}
      >
        <DialogSurface aria-label={text("创建虚拟网卡", "Create virtual adapter")}>
          <DialogBody>
            <DialogTitle>{text("创建虚拟网卡", "Create virtual adapter")}</DialogTitle>
            <DialogContent>
              <span className="virtual-adapter-intro">
                {text(
                  "创建虚拟网卡需要管理员权限，可能弹出 UAC，并会短暂重启聚合核心（当前连接可能短暂中断）。",
                  "Creating a virtual adapter needs administrator rights; a UAC prompt may appear and the core will briefly restart (current connections may drop briefly).",
                )}
              </span>
              <div className="virtual-adapter-fields">
                <Input
                  value={draftName}
                  disabled={busy}
                  maxLength={128}
                  aria-label={text("虚拟网卡名称", "Virtual adapter name")}
                  onChange={(_, data) => setDraftName(data.value)}
                />
                <Input
                  value={draftAddress}
                  disabled={busy}
                  aria-label={text("虚拟网卡地址", "Virtual adapter address")}
                  aria-invalid={addressError ? true : undefined}
                  onChange={(_, data) => {
                    setDraftAddress(data.value);
                    if (addressError) setAddressError("");
                  }}
                />
                <span className="virtual-adapter-hint">
                  {text(
                    "名称默认为 HypoMux-VNIC，地址默认为 10.66.0.1（前缀 24）。",
                    "The name defaults to HypoMux-VNIC and the address to 10.66.0.1 (prefix 24).",
                  )}
                </span>
                {addressError && <span className="virtual-adapter-error" role="alert">{addressError}</span>}
                {busy && (
                  <span className="virtual-adapter-hint" role="status">
                    {text("正在创建虚拟网卡，最长可能需要 60 秒…", "Creating the virtual adapter; this can take up to 60 seconds…")}
                  </span>
                )}
              </div>
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" disabled={busy} onClick={() => setCreateOpen(false)}>
                {text("取消", "Cancel")}
              </Button>
              <Button
                appearance="primary"
                disabled={busy}
                icon={busy ? <Spinner size="tiny" /> : undefined}
                onClick={() => void create()}
              >
                {busy ? text("创建中…", "Creating…") : text("创建", "Create")}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>

      <Dialog
        open={removeOpen}
        onOpenChange={(_, data) => {
          if (!data.open && busy) return;
          if (!data.open) setRemoveOpen(false);
        }}
      >
        <DialogSurface aria-label={text("移除虚拟网卡", "Remove virtual adapter")}>
          <DialogBody>
            <DialogTitle>{text("移除虚拟网卡", "Remove virtual adapter")}</DialogTitle>
            <DialogContent>
              {text(
                "将停止虚拟网卡 keeper 并删除该适配器，正在使用它的连接会中断。",
                "This stops the virtual adapter keeper and removes the adapter; connections using it will drop.",
              )}
            </DialogContent>
            <DialogActions>
              <Button appearance="secondary" disabled={busy} onClick={() => setRemoveOpen(false)}>
                {text("取消", "Cancel")}
              </Button>
              <Button
                appearance="primary"
                disabled={busy}
                icon={busy ? <Spinner size="tiny" /> : undefined}
                onClick={() => void remove()}
              >
                {busy ? text("移除中…", "Removing…") : text("移除", "Remove")}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </section>
  );
}
