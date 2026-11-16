import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { renderAdmin } from "@/test/admin";
import { CopyButton } from "./copy-button";
import { ConfirmDialog } from "./confirm-dialog";
import { DataTable } from "./data-table";
import { SearchFilter } from "./filter-bar";
import { JsonViewer, tokenizeJson } from "./json-viewer";
import { MonoValue } from "./mono-value";
import { PaginationBar } from "./pagination-bar";
import { SecretRevealDialog } from "./secret-dialog";
import { ErrorState } from "./states";
import { InvoiceStatusBadge, RoleBadge } from "./status-badge";

vi.mock("@/lib/clipboard", () => ({ copyText: vi.fn(async () => true) }));

describe("CopyButton / MonoValue", () => {
  it("copies the full value and names what it copies", async () => {
    const { copyText } = await import("@/lib/clipboard");
    renderAdmin(<MonoValue value="0x742d35Cc6634C0532925a3b844Bc454e4438f44e" label="address" short href="https://etherscan.io/address/x" />);
    expect(screen.getByText("0x742d…f44e")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Copy address" }));
    expect(copyText).toHaveBeenCalledWith("0x742d35Cc6634C0532925a3b844Bc454e4438f44e");
    const link = screen.getByRole("link", { name: /block explorer/ });
    expect(link).toHaveAttribute("href", "https://etherscan.io/address/x");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  it("standalone copy button", async () => {
    renderAdmin(<CopyButton value="secret" label="API key" />);
    await userEvent.click(screen.getByRole("button", { name: "Copy API key" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Copy API key" }).querySelector("svg")).toHaveClass("text-success"));
  });
});

describe("status badges", () => {
  it("label every status in the current language", () => {
    renderAdmin(
      <>
        <InvoiceStatusBadge status="partial" />
        <RoleBadge role="viewer" />
      </>,
      { locale: "zh" },
    );
    expect(screen.getByText("部分支付")).toBeInTheDocument();
    expect(screen.getByText("查看者")).toBeInTheDocument();
  });
});

describe("ConfirmDialog", () => {
  function Harness({ onConfirm }: { onConfirm: () => Promise<unknown> }) {
    const [open, setOpen] = useState(true);
    return (
      <>
        <span>{open ? "open" : "closed"}</span>
        <ConfirmDialog open={open} onOpenChange={setOpen} title="Cancel this invoice?" confirmLabel="Cancel invoice" destructive onConfirm={onConfirm} />
      </>
    );
  }

  it("stays open while the action runs and closes when it succeeds", async () => {
    let resolve!: () => void;
    const onConfirm = vi.fn(() => new Promise<void>((r) => (resolve = r)));
    renderAdmin(<Harness onConfirm={onConfirm} />);
    const dialog = screen.getByRole("alertdialog", { name: "Cancel this invoice?" });
    const confirm = within(dialog).getByRole("button", { name: "Cancel invoice" });
    await userEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(confirm).toBeDisabled();
    expect(within(dialog).getByRole("button", { name: "Cancel" })).toBeDisabled();
    await act(async () => resolve());
    await waitFor(() => expect(screen.getByText("closed")).toBeInTheDocument());
  });

  it("stays open when the action fails", async () => {
    const onConfirm = vi.fn(async () => {
      throw new Error("409");
    });
    renderAdmin(<Harness onConfirm={onConfirm} />);
    await userEvent.click(screen.getByRole("button", { name: "Cancel invoice" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Cancel invoice" })).toBeEnabled());
    expect(screen.getByText("open")).toBeInTheDocument();
  });
});

describe("SecretRevealDialog", () => {
  it("shows the secret once and only closes after the operator confirms storing it", async () => {
    const onClose = vi.fn();
    renderAdmin(<SecretRevealDialog secret="whsec_abc123" title="Store the webhook signing secret" label="Signing secret" onClose={onClose} />);
    const dialog = screen.getByRole("dialog", { name: "Store the webhook signing secret" });
    expect(within(dialog).getByTestId("secret-value")).toHaveTextContent("whsec_abc123");
    expect(within(dialog).getByText(/only time it's shown/)).toBeInTheDocument();
    const done = within(dialog).getByRole("button", { name: "Done" });
    expect(done).toBeDisabled();
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "I've stored it somewhere safe" }));
    expect(done).toBeEnabled();
    await userEvent.click(done);
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("renders nothing without a secret", () => {
    renderAdmin(<SecretRevealDialog secret={null} title="t" label="l" onClose={() => {}} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });
});

describe("JsonViewer", () => {
  it("pretty-prints objects and JSON strings with token classes", () => {
    renderAdmin(<JsonViewer value={{ a: 1, b: "x", c: null }} label="Payload" />);
    const pre = screen.getByLabelText("Payload", { selector: "pre" });
    expect(pre.textContent).toBe('{\n  "a": 1,\n  "b": "x",\n  "c": null\n}');
    expect(tokenizeJson('{"a": true}').map((t) => t.kind)).toEqual(["punct", "key", "punct", "space", "literal", "punct"]);
  });

  it("shows an empty marker", () => {
    renderAdmin(<JsonViewer value={null} />);
    expect(screen.getByText("(empty)")).toBeInTheDocument();
  });
});

describe("PaginationBar", () => {
  it("shows the range and disables what can't be used", async () => {
    const onPageChange = vi.fn();
    renderAdmin(<PaginationBar page={1} pageSize={20} total={45} onPageChange={onPageChange} />);
    expect(screen.getByText("1–20 of 45")).toBeInTheDocument();
    expect(screen.getByText("Page 1 of 3")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Previous page" })).toBeDisabled();
    await userEvent.click(screen.getByRole("button", { name: "Next page" }));
    expect(onPageChange).toHaveBeenCalledWith(2);
  });
});

describe("DataTable", () => {
  const rows = [
    { id: "a", name: "Alpha" },
    { id: "b", name: "Beta" },
  ];
  const columns = [{ id: "name", header: "Name", cell: (r: (typeof rows)[number]) => r.name }];

  it("renders loading, empty and error states", async () => {
    const onRetry = vi.fn();
    const { rerender } = renderAdmin(
      <DataTable caption="Things" columns={columns} rows={undefined} getRowId={(r) => r.id} isLoading empty={<p>Nothing yet</p>} />,
    );
    expect(screen.getByRole("status", { name: "Loading rows" })).toBeInTheDocument();
    rerender(<DataTable caption="Things" columns={columns} rows={[]} getRowId={(r) => r.id} isLoading={false} empty={<p>Nothing yet</p>} />);
    expect(screen.getByText("Nothing yet")).toBeInTheDocument();
    rerender(
      <DataTable
        caption="Things"
        columns={columns}
        rows={undefined}
        getRowId={(r) => r.id}
        isLoading={false}
        error={new ApiError({ status: 0, code: "network_error", message: "offline" })}
        onRetry={onRetry}
        empty={null}
      />,
    );
    expect(screen.getByText("Can't reach the server. Check the connection and try again.")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledOnce();
  });

  it("renders rows and expands details", async () => {
    renderAdmin(
      <DataTable
        caption="Things"
        columns={columns}
        rows={rows}
        getRowId={(r) => r.id}
        isLoading={false}
        empty={null}
        renderExpanded={(r) => <p>Details of {r.name}</p>}
      />,
    );
    const table = screen.getByRole("table", { name: "Things" });
    expect(within(table).getByText("Alpha")).toBeInTheDocument();
    const toggles = within(table).getAllByRole("button", { name: "Show details" });
    await userEvent.click(toggles[1]);
    expect(screen.getByText("Details of Beta")).toBeInTheDocument();
    expect(within(table).getByRole("button", { name: "Hide details" })).toHaveAttribute("aria-expanded", "true");
  });
});

describe("SearchFilter", () => {
  it("commits after a pause and on Enter", async () => {
    vi.useFakeTimers();
    const onCommit = vi.fn();
    renderAdmin(<SearchFilter value="" onCommit={onCommit} label="Search invoices" />);
    const input = screen.getByRole("searchbox", { name: "Search invoices" });
    fireEvent.change(input, { target: { value: "0xabc" } });
    expect(onCommit).not.toHaveBeenCalled();
    act(() => vi.advanceTimersByTime(400));
    expect(onCommit).toHaveBeenCalledWith("0xabc");
    fireEvent.change(input, { target: { value: "0xdef" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(onCommit).toHaveBeenLastCalledWith("0xdef");
    vi.useRealTimers();
  });
});

describe("ErrorState", () => {
  it("explains 403 without offering a retry", () => {
    renderAdmin(<ErrorState error={new ApiError({ status: 403, code: "forbidden", message: "no" })} onRetry={() => {}} />);
    expect(screen.getByText("No access")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
  });
});
