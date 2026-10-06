// @vitest-environment jsdom
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { MTUDetectionPage } from "./MTUDetectionPage";
import type { AdapterView } from "../platform/services";

const api = vi.hoisted(() => ({ current: vi.fn(), detect: vi.fn(), cancel: vi.fn(), apply: vi.fn(), restore: vi.fn(), setBatch: vi.fn(), restoreBatch: vi.fn() }));
vi.mock("../platform/services", () => ({ appServices: { mtu: api } }));
const info = { adapter_id: "Ethernet", guid: "a", if_index: 7, address: "192.0.2.10", current: 1500 };
const adapters = [{ id: "Ethernet", name: "Ethernet", address: info.address, if_index: 7, selected: true }] as AdapterView[];
const batchAdapters = [
  { id: "Ethernet", name: "Ethernet", address: "192.0.2.10", if_index: 7, selected: true },
  { id: "Wi-Fi", name: "Wi-Fi", address: "192.0.2.11", if_index: 9, selected: false },
] as AdapterView[];
const props = { adapters, enginePhase: "stopped" as const, loading: false, preview: false, text: (zh: string) => zh };
beforeEach(() => { vi.resetAllMocks(); api.current.mockResolvedValue(info); api.detect.mockResolvedValue({ ...info, recommended: 1492, target: "223.5.5.5", at_limit: false }); api.apply.mockResolvedValue({ ...info, current: 1492, original: 1500 }); api.cancel.mockResolvedValue(undefined); api.setBatch.mockResolvedValue([]); api.restoreBatch.mockResolvedValue([]); });
afterEach(cleanup);

it("requires confirmation before changing MTU and preserves a recovery action", async () => {
  render(<MTUDetectionPage {...props} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "检测推荐 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "检测推荐 MTU" }));
  await screen.findByText(/推荐从 1500 调整为 1492/);
  fireEvent.click(screen.getByRole("button", { name: "应用推荐值" }));
  expect(api.apply).not.toHaveBeenCalled();
  fireEvent.click(screen.getByRole("button", { name: "确认修改" }));
  await screen.findByText("已应用推荐 MTU。");
  expect(api.apply).toHaveBeenCalledWith("Ethernet");
  expect((screen.getByRole("button", { name: "恢复原值" }) as HTMLButtonElement).disabled).toBe(false);
});
it("does not offer a recommendation after probe failure", async () => {
  api.detect.mockRejectedValue(new Error("ICMP timeout"));
  render(<MTUDetectionPage {...props} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "检测推荐 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "检测推荐 MTU" }));
  expect((await screen.findByRole("alert")).textContent).toContain("ICMP timeout");
  expect((screen.getByRole("button", { name: "应用推荐值" }) as HTMLButtonElement).disabled).toBe(true);
});
it("blocks testing while the network service is running", async () => {
  render(<MTUDetectionPage {...props} enginePhase="running" />);
  await screen.findByText(/请先停止网络服务/);
  expect((screen.getByRole("button", { name: "检测推荐 MTU" }) as HTMLButtonElement).disabled).toBe(true);
  expect(api.detect).not.toHaveBeenCalled();
});
it("invalidates the recommendation when the target changes", async () => {
  render(<MTUDetectionPage {...props} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "检测推荐 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "检测推荐 MTU" }));
  await screen.findByText(/推荐从 1500 调整为 1492/);
  fireEvent.change(screen.getByRole("textbox"), { target: { value: "1.1.1.1" } });
  expect((screen.getByRole("button", { name: "应用推荐值" }) as HTMLButtonElement).disabled).toBe(true);
});

it("keeps cancellation available during a probe without enabling changes", async () => {
  let rejectProbe!: (reason: Error) => void;
  api.detect.mockReturnValue(new Promise((_, reject) => { rejectProbe = reject; }));
  api.cancel.mockImplementation(async () => { rejectProbe(new Error("Cancelled")); });
  render(<MTUDetectionPage {...props} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "检测推荐 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "检测推荐 MTU" }));
  expect(screen.getByText("正在探测并复测，最长约 50 秒…")).toBeTruthy();
  expect((screen.getByRole("button", { name: "应用推荐值" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(screen.getByRole("button", { name: "取消检测" }));
  await screen.findByRole("alert");
  expect(api.cancel).toHaveBeenCalledOnce();
  expect(screen.queryByRole("button", { name: "取消检测" })).toBeNull();
});
it("explains the tested upper limit and prevents applying an unchanged MTU", async () => {
  api.detect.mockResolvedValue({ ...info, recommended: 1500, target: "223.5.5.5", at_limit: true });
  render(<MTUDetectionPage {...props} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "检测推荐 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "检测推荐 MTU" }));
  await screen.findByText(/当前 MTU 已通过检测，无需修改/);
  expect((screen.getByRole("button", { name: "应用推荐值" }) as HTMLButtonElement).disabled).toBe(true);
  expect(api.apply).not.toHaveBeenCalled();
});
it("applies one MTU to every selected adapter and reports each result in the dialog", async () => {
  api.setBatch.mockResolvedValue([
    { adapter_id: "Ethernet", guid: "a", address: "192.0.2.10", before: 1500, after: 1400, original: 1500, changed: true },
    { adapter_id: "Wi-Fi", guid: "b", address: "192.0.2.11", before: 1400, after: 1400, original: 1400, changed: false, error: "需要管理员授权" },
  ]);
  render(<MTUDetectionPage {...props} adapters={batchAdapters} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "批量修改 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "批量修改 MTU" }));
  const boxes = await screen.findAllByRole("checkbox");
  expect(boxes).toHaveLength(2);
  expect((screen.getByRole("button", { name: "批量恢复原值" }) as HTMLButtonElement).disabled).toBe(true);
  fireEvent.click(boxes[0]);
  fireEvent.click(boxes[1]);
  expect((screen.getByRole("button", { name: "批量恢复原值" }) as HTMLButtonElement).disabled).toBe(false);
  fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "1400" } });
  fireEvent.click(screen.getByRole("button", { name: "批量应用" }));
  await waitFor(() => expect(api.setBatch).toHaveBeenCalledWith(["Ethernet", "Wi-Fi"], 1400));
  expect(await screen.findByText("Ethernet：1500 → 1400")).toBeTruthy();
  expect(screen.getByText("Wi-Fi：需要管理员授权")).toBeTruthy();
  expect(screen.getByRole("dialog")).toBeTruthy();
});
it("restores the original MTU of the selected adapter from the batch dialog", async () => {
  api.restoreBatch.mockResolvedValue([
    { adapter_id: "Wi-Fi", guid: "b", address: "192.0.2.11", before: 1400, after: 1500, original: 1500, changed: true },
  ]);
  render(<MTUDetectionPage {...props} adapters={batchAdapters} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "批量修改 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(screen.getByRole("button", { name: "批量修改 MTU" }));
  const boxes = await screen.findAllByRole("checkbox");
  fireEvent.click(boxes[1]);
  fireEvent.click(screen.getByRole("button", { name: "批量恢复原值" }));
  await waitFor(() => expect(api.restoreBatch).toHaveBeenCalledWith(["Wi-Fi"]));
  expect(await screen.findByText("Wi-Fi：1400 → 1500")).toBeTruthy();
  expect(api.setBatch).not.toHaveBeenCalled();
});
it("refreshes the page value after closing a batch that changed an adapter", async () => {
  api.setBatch.mockResolvedValue([
    { adapter_id: "Ethernet", guid: "a", address: "192.0.2.10", before: 1500, after: 1400, original: 1500, changed: true },
  ]);
  render(<MTUDetectionPage {...props} adapters={batchAdapters} />);
  await waitFor(() => expect((screen.getByRole("button", { name: "批量修改 MTU" }) as HTMLButtonElement).disabled).toBe(false));
  const reads = api.current.mock.calls.length;
  fireEvent.click(screen.getByRole("button", { name: "批量修改 MTU" }));
  fireEvent.click((await screen.findAllByRole("checkbox"))[0]);
  fireEvent.change(screen.getByRole("spinbutton"), { target: { value: "1400" } });
  fireEvent.click(screen.getByRole("button", { name: "批量应用" }));
  await screen.findByText("Ethernet：1500 → 1400");
  expect(api.current.mock.calls.length).toBe(reads);
  fireEvent.click(screen.getByRole("button", { name: "关闭" }));
  await waitFor(() => expect(api.current.mock.calls.length).toBeGreaterThan(reads));
  expect(screen.queryByRole("dialog")).toBeNull();
});
it("keeps the batch entry disabled while the network service is running", async () => {
  render(<MTUDetectionPage {...props} adapters={batchAdapters} enginePhase="running" />);
  await screen.findByText(/请先停止网络服务/);
  expect((screen.getByRole("button", { name: "批量修改 MTU" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(api.setBatch).not.toHaveBeenCalled();
  expect(api.restoreBatch).not.toHaveBeenCalled();
});
it("keeps the batch entry disabled in browser preview mode", async () => {
  render(<MTUDetectionPage {...props} adapters={batchAdapters} preview />);
  await screen.findByText(/浏览器预览仅展示界面/);
  expect((screen.getByRole("button", { name: "批量修改 MTU" }) as HTMLButtonElement).disabled).toBe(true);
  expect(screen.queryByRole("dialog")).toBeNull();
});
