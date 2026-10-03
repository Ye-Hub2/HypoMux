// @vitest-environment jsdom

import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { FluentProvider, webLightTheme } from "@fluentui/react-components";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AppNotificationCenter, AppNotificationProvider } from "../notifications/AppNotifications";
import type { VirtualAdapterStatus } from "../../platform/services";
import { isValidIPv4, VirtualAdapterPanel } from "./VirtualAdapterPanel";

const mocks = vi.hoisted(() => ({
  create: vi.fn(),
  status: vi.fn(),
  remove: vi.fn(),
}));

vi.mock("../../platform/services", () => ({
  appServices: {
    virtualAdapter: {
      create: mocks.create,
      status: mocks.status,
      remove: mocks.remove,
    },
  },
  withServiceTimeout: (request: Promise<unknown>) => request,
}));

vi.mock("../../i18n/i18n", () => ({
  useI18n: () => ({ locale: "en" }),
}));

const text = (zh: string, en: string) => en;

const status = (overrides: Partial<VirtualAdapterStatus> = {}): VirtualAdapterStatus => ({
  state: "absent",
  interfaceName: "",
  address: "",
  prefixLength: 0,
  mtu: 0,
  adapterGuid: "",
  createdAt: "",
  lastError: "",
  ...overrides,
});

const present = status({
  state: "present",
  interfaceName: "HypoMux-VNIC",
  address: "10.66.0.1",
  prefixLength: 24,
  mtu: 1420,
  adapterGuid: "7c1e5b2a-3d4f-4a6b-8c9d-0e1f2a3b4c5d",
  createdAt: "2026-08-24T01:02:03Z",
});

const renderPanel = (props: Partial<Parameters<typeof VirtualAdapterPanel>[0]> = {}) => {
  const notifySuccess = vi.fn();
  const notifyError = vi.fn();
  const view = render(
    <FluentProvider theme={webLightTheme}>
      <AppNotificationProvider>
        <VirtualAdapterPanel
          locale="en"
          text={text}
          notifySuccess={notifySuccess}
          notifyError={notifyError}
          {...props}
        />
        <AppNotificationCenter />
      </AppNotificationProvider>
    </FluentProvider>,
  );
  return { ...view, notifySuccess, notifyError };
};

// jsdom has no layout: give the document a viewport so Fluent's dialog surface is
// treated as visible, following the established suite pattern.
const prepareViewport = () => {
  vi.spyOn(document.body, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 1280, 800));
  vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) {
    if (!this.isConnected || this === document.body || getComputedStyle(this).position === "fixed") return null;
    for (let element: HTMLElement | null = this; element; element = element.parentElement) {
      if (element.hidden || getComputedStyle(element).display === "none") return null;
    }
    return this.parentElement;
  });
};

const readyDialogAction = async (dialog: HTMLElement, name: string) => {
  const button = within(dialog).getByRole("button", { name });
  await waitFor(() => expect(button.hasAttribute("disabled")).toBe(false));
  return button;
};

beforeEach(() => {
  prepareViewport();
  mocks.create.mockReset();
  mocks.status.mockReset();
  mocks.remove.mockReset();
  mocks.status.mockResolvedValue(status());
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("isValidIPv4", () => {
  it("accepts dotted quads and rejects anything else", () => {
    expect(isValidIPv4("10.66.0.1")).toBe(true);
    expect(isValidIPv4(" 192.168.1.254 ")).toBe(true);
    expect(isValidIPv4("10.66.0")).toBe(false);
    expect(isValidIPv4("10.66.0.256")).toBe(false);
    expect(isValidIPv4("10.66.0.1.5")).toBe(false);
    expect(isValidIPv4("10.66.0.a")).toBe(false);
    expect(isValidIPv4("")).toBe(false);
  });
});

describe("VirtualAdapterPanel", () => {
  it("loads the current state on mount and offers creation", async () => {
    renderPanel();

    expect(screen.getByText("Reading virtual adapter status")).not.toBeNull();
    await waitFor(() => expect(screen.queryByText("Reading virtual adapter status")).toBeNull());
    expect(mocks.status).toHaveBeenCalledTimes(1);
    await screen.findByText("Virtual adapter · Not created");
    expect(screen.getByRole("button", { name: "Create virtual adapter" }).hasAttribute("disabled")).toBe(false);
    expect(screen.queryByRole("button", { name: "Remove virtual adapter" })).toBeNull();
  });

  it("shows the address, prefix, MTU, creation time and id of an existing adapter", async () => {
    mocks.status.mockResolvedValue(present);
    renderPanel();

    await screen.findByText("Virtual adapter · Present");
    expect(screen.getByText("HypoMux-VNIC")).not.toBeNull();
    expect(screen.getByText("10.66.0.1/24")).not.toBeNull();
    expect(screen.getByText("1420")).not.toBeNull();
    expect(screen.getByText("7c1e5b2a-3d4f-4a6b-8c9d-0e1f2a3b4c5d")).not.toBeNull();
    expect(screen.getByText(/Created:/).textContent).not.toContain("2026-08-24T01:02:03Z");
  });

  it("reports an unreadable state and keeps a retry available", async () => {
    mocks.status.mockRejectedValueOnce(new Error("core unavailable"));
    renderPanel();

    await screen.findByText(/Could not read the virtual adapter status/);
    expect(screen.getByRole("button", { name: "Retry" })).not.toBeNull();
  });

  it("creates the adapter with the displayed defaults", async () => {
    mocks.create.mockResolvedValue(present);
    mocks.status.mockResolvedValueOnce(status()).mockResolvedValue(present);
    const { notifySuccess } = renderPanel();
    await screen.findByText("Virtual adapter · Not created");

    fireEvent.click(screen.getByRole("button", { name: "Create virtual adapter" }));
    const dialog = await screen.findByRole("dialog", { name: "Create virtual adapter" });
    const nameInput = within(dialog).getByLabelText("Virtual adapter name") as HTMLInputElement;
    const addressInput = within(dialog).getByLabelText("Virtual adapter address") as HTMLInputElement;
    expect(nameInput.value).toBe("HypoMux-VNIC");
    expect(addressInput.value).toBe("10.66.0.1");
    // The dialog states the elevation and core-restart consequences up front.
    expect(within(dialog).getByText(/administrator rights/)).not.toBeNull();

    fireEvent.click(await readyDialogAction(dialog, "Create"));

    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith("HypoMux-VNIC", "10.66.0.1"));
    await waitFor(() => expect(notifySuccess).toHaveBeenCalledWith("Virtual adapter HypoMux-VNIC was created."));
    await screen.findByText("Virtual adapter · Present");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("submits an edited name and address and survives a failing create", async () => {
    mocks.create.mockRejectedValue(new Error("elevation required: Wintun needs administrator rights"));
    mocks.status.mockResolvedValue(status());
    const { notifyError } = renderPanel();
    await screen.findByText("Virtual adapter · Not created");

    fireEvent.click(screen.getByRole("button", { name: "Create virtual adapter" }));
    const dialog = await screen.findByRole("dialog", { name: "Create virtual adapter" });
    fireEvent.change(within(dialog).getByLabelText("Virtual adapter name"), { target: { value: "Mux-Lab" } });
    fireEvent.change(within(dialog).getByLabelText("Virtual adapter address"), { target: { value: "10.77.0.2" } });
    fireEvent.click(await readyDialogAction(dialog, "Create"));

    await waitFor(() => expect(mocks.create).toHaveBeenCalledWith("Mux-Lab", "10.77.0.2"));
    await waitFor(() => expect(notifyError).toHaveBeenCalledTimes(1));
    expect(String(notifyError.mock.calls[0][0])).toContain("elevation required");
    expect(typeof notifyError.mock.calls[0][1]).toBe("function");
    // The dialog stays open so the user can retry with corrected input.
    expect(screen.getByRole("dialog", { name: "Create virtual adapter" })).not.toBeNull();
    expect(within(dialog).getByRole("button", { name: "Create" }).hasAttribute("disabled")).toBe(false);
  });

  it("blocks an invalid IPv4 address before calling the service", async () => {
    renderPanel();
    await screen.findByText("Virtual adapter · Not created");

    fireEvent.click(screen.getByRole("button", { name: "Create virtual adapter" }));
    const dialog = await screen.findByRole("dialog", { name: "Create virtual adapter" });
    fireEvent.change(within(dialog).getByLabelText("Virtual adapter address"), { target: { value: "10.66.0.999" } });
    fireEvent.click(await readyDialogAction(dialog, "Create"));

    expect(await within(dialog).findByRole("alert")).not.toBeNull();
    expect(mocks.create).not.toHaveBeenCalled();
  });

  it("removes an existing adapter after confirmation", async () => {
    mocks.status.mockResolvedValueOnce(present).mockResolvedValue(status());
    mocks.remove.mockResolvedValue(status());
    const { notifySuccess } = renderPanel();
    await screen.findByText("Virtual adapter · Present");

    fireEvent.click(screen.getByRole("button", { name: "Remove virtual adapter" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove virtual adapter" });
    fireEvent.click(await readyDialogAction(dialog, "Remove"));

    await waitFor(() => expect(mocks.remove).toHaveBeenCalledTimes(1));
    await screen.findByText("Virtual adapter · Not created");
    expect(notifySuccess).toHaveBeenCalledWith("The virtual adapter was removed.");
  });

  it("keeps the action available when the removal fails", async () => {
    mocks.status.mockResolvedValue(present);
    mocks.remove.mockRejectedValue(new Error("stop failed"));
    const { notifyError } = renderPanel();
    await screen.findByText("Virtual adapter · Present");

    fireEvent.click(screen.getByRole("button", { name: "Remove virtual adapter" }));
    const dialog = await screen.findByRole("dialog", { name: "Remove virtual adapter" });
    fireEvent.click(await readyDialogAction(dialog, "Remove"));

    await waitFor(() => expect(notifyError).toHaveBeenCalledTimes(1));
    // A failure keeps the confirmation open; cancel it before checking the trigger,
    // because an open modal hides the rest of the page from the accessibility tree.
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(screen.getByRole("button", { name: "Remove virtual adapter" }).hasAttribute("disabled")).toBe(false);
  });
});
