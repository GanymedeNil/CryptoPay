import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { chainFixture, renderAdmin } from "@/test/admin";
import { RpcEditor, validateRpcRows } from "./rpc-editor";

const api = vi.hoisted(() => ({ testChainRpc: vi.fn(), updateChain: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

beforeEach(() => {
  api.updateChain.mockImplementation(async (_id: string, body: { rpc_urls?: string[] }) =>
    chainFixture({ rpc_urls: (body.rpc_urls ?? []).map(() => "https://masked.example/…") }),
  );
});

describe("validateRpcRows", () => {
  it("rejects masked, invalid and duplicate URLs and ignores blank rows", () => {
    const { errors, urls } = validateRpcRows([
      "https://a.example/v2/key",
      "",
      "https://eth-mainnet.g.alchemy.com/…",
      "not a url",
      "https://a.example/v2/key",
    ]);
    expect(errors).toEqual([null, null, "admin.chains.rpc.urlMasked", "admin.chains.rpc.urlInvalid", "admin.chains.rpc.duplicate"]);
    expect(urls).toHaveLength(4);
  });
});

describe("RpcEditor", () => {
  it("shows the masked endpoints, their source, and tests the configured ones", async () => {
    api.testChainRpc.mockResolvedValue({ ok: true, head: 23812440, latency_ms: 87, error: "" });
    renderAdmin(<RpcEditor chain={chainFixture()} />);
    expect(screen.getByText("https://eth-mainnet.g.alchemy.com/…")).toBeInTheDocument();
    expect(screen.getByText("Set in console")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Test current endpoints" }));
    expect(api.testChainRpc).toHaveBeenCalledWith("ethereum", {});
    expect(await screen.findByText("Works · block 23,812,440 · 87 ms")).toBeInTheDocument();
  });

  it("reports a failing endpoint", async () => {
    api.testChainRpc.mockResolvedValue({ ok: false, head: 0, latency_ms: 0, error: "429 Too Many Requests" });
    renderAdmin(<RpcEditor chain={chainFixture()} />);
    await userEvent.click(screen.getByRole("button", { name: "Test current endpoints" }));
    expect(await screen.findByText("Doesn't work: 429 Too Many Requests")).toBeInTheDocument();
  });

  it("warns when a mainnet still runs on the free default RPC", () => {
    renderAdmin(<RpcEditor chain={chainFixture({ rpc_source: "default", rpc_urls: ["https://ethereum-rpc.publicnode.com"] })} />);
    expect(screen.getByText("This mainnet still uses the free public RPC")).toBeInTheDocument();
    expect(screen.getByText("Free public default")).toBeInTheDocument();
    // nothing to reset when nothing is stored in the console
    expect(screen.queryByRole("button", { name: "Reset to default" })).not.toBeInTheDocument();
  });

  it("does not warn for testnets", () => {
    renderAdmin(<RpcEditor chain={chainFixture({ id: "sepolia", is_testnet: true, rpc_source: "default" })} />);
    expect(screen.queryByText("This mainnet still uses the free public RPC")).not.toBeInTheDocument();
  });

  it("replaces the whole list: refuses masked values, tests a new URL, saves", async () => {
    api.testChainRpc.mockResolvedValue({ ok: true, head: 5, latency_ms: 40, error: "" });
    renderAdmin(<RpcEditor chain={chainFixture()} />);
    await userEvent.click(screen.getByRole("button", { name: "Replace endpoints" }));
    const first = screen.getByLabelText("Endpoint 1");
    await userEvent.type(first, "https://eth-mainnet.g.alchemy.com/…");
    expect(screen.getByText("That's the masked value; paste the full URL")).toBeInTheDocument();
    await userEvent.clear(first);
    await userEvent.type(first, "https://eth.example/v2/secret-key");
    await userEvent.click(screen.getByRole("button", { name: "Add endpoint" }));
    await userEvent.type(screen.getByLabelText("Endpoint 2"), "wss://eth-ws.example");

    const row1 = first.closest("div")!;
    await userEvent.click(within(row1).getByRole("button", { name: "Test" }));
    expect(api.testChainRpc).toHaveBeenCalledWith("ethereum", { rpc_url: "https://eth.example/v2/secret-key" });
    expect(await screen.findByText("Works · block 5 · 40 ms")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save endpoints" }));
    expect(api.updateChain).toHaveBeenCalledWith("ethereum", { rpc_urls: ["https://eth.example/v2/secret-key", "wss://eth-ws.example"] });
    await waitFor(() => expect(screen.queryByLabelText("Endpoint 1")).not.toBeInTheDocument());
  });

  it("needs at least one endpoint to save; reset sends an empty list after confirmation", async () => {
    renderAdmin(<RpcEditor chain={chainFixture()} />);
    await userEvent.click(screen.getByRole("button", { name: "Replace endpoints" }));
    await userEvent.click(screen.getByRole("button", { name: "Save endpoints" }));
    expect(screen.getByText("Enter at least one endpoint, or use “Reset to default”")).toBeInTheDocument();
    expect(api.updateChain).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));

    await userEvent.click(screen.getByRole("button", { name: "Reset to default" }));
    const confirm = await screen.findByRole("alertdialog", { name: "Reset to the default endpoints?" });
    await userEvent.click(within(confirm).getByRole("button", { name: "Reset to default" }));
    expect(api.updateChain).toHaveBeenCalledWith("ethereum", { rpc_urls: [] });
  });

  it("is read-only for viewers", () => {
    renderAdmin(<RpcEditor chain={chainFixture()} />, { role: "viewer" });
    expect(screen.getByRole("button", { name: "Replace endpoints" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Test current endpoints" })).toBeDisabled();
  });
});
