import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { chainFixture, renderAdmin, walletFixture } from "@/test/admin";
import { WalletsPage } from "./wallets-page";

const api = vi.hoisted(() => ({ listWallets: vi.fn(), listChains: vi.fn(), deleteWallet: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const DEFAULT = walletFixture({ id: "w-1", name: "Ledger main", chain_ids: ["ethereum", "polygon"] });
const SHARED = walletFixture({
  id: "w-2",
  name: "Pixel treasury",
  xpub: "xpub6Pixel",
  chain_ids: ["base"],
  merchant_bindings: [
    { merchant_id: "m-2", merchant_name: "Pixel Forge", chain_id: "ethereum" },
    { merchant_id: "m-2", merchant_name: "Pixel Forge", chain_id: "polygon" },
  ],
});
const UNUSED = walletFixture({ id: "w-3", name: "Spare", xpub: "xpub6Spare", chain_ids: [] });

beforeEach(() => {
  vi.clearAllMocks();
  api.listWallets.mockResolvedValue({ items: [DEFAULT, SHARED, UNUSED] });
  api.listChains.mockResolvedValue({
    items: [chainFixture(), chainFixture({ id: "polygon", name: "Polygon" }), chainFixture({ id: "base", name: "Base" })],
  });
  api.listAuditLogs.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20 });
});

function rowOf(name: string) {
  const cell = screen.getAllByText(name).find((el) => el.closest("tr"));
  return cell!.closest("tr")!;
}

describe("WalletsPage", () => {
  it("shows default-wallet chains next to the merchants using a wallet as their own", async () => {
    renderAdmin(<WalletsPage />);
    await screen.findAllByText("Pixel treasury");
    await waitFor(() => expect(within(rowOf("Ledger main")).getByText("Polygon")).toBeInTheDocument());
    const ledger = rowOf("Ledger main");
    expect(within(ledger).getByText("Default wallet")).toBeInTheDocument();
    expect(within(ledger).queryByText("Merchant wallet")).not.toBeInTheDocument();

    const pixel = rowOf("Pixel treasury");
    expect(within(pixel).getByText("Default wallet")).toBeInTheDocument();
    expect(within(pixel).getByText("Merchant wallet")).toBeInTheDocument();
    const links = within(pixel).getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(["Pixel Forge · Ethereum", "Pixel Forge · Polygon"]);
    expect(links[0]).toHaveAttribute("href", "/admin/merchants/m-2");

    expect(within(rowOf("Spare")).getByText("Not in use")).toBeInTheDocument();
  });

  it("explains which merchants block deleting a wallet", async () => {
    api.deleteWallet.mockRejectedValue(
      new ApiError({ status: 409, code: "conflict", message: "the wallet is the receiving wallet of merchant \"Pixel Forge\" on ethereum" }),
    );
    renderAdmin(<WalletsPage />);
    await screen.findAllByText("Pixel treasury");
    await userEvent.click(within(rowOf("Pixel treasury")).getByRole("button", { name: "More actions: Pixel treasury" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
    const confirm = await screen.findByRole("alertdialog");
    await userEvent.click(within(confirm).getByRole("button", { name: "Delete wallet" }));
    await waitFor(() => expect(api.deleteWallet).toHaveBeenCalledWith("w-2"));

    const dialog = await screen.findByRole("dialog", { name: "This wallet is in use and can't be deleted" });
    expect(within(dialog).getByText(/use it as their own receiving wallet/)).toBeInTheDocument();
    const merchants = within(dialog).getByTestId("wallet-in-use-merchants");
    expect(within(merchants).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Pixel Forge · Ethereum", "Pixel Forge · Polygon"]);
    expect(within(merchants).getAllByRole("link")[0]).toHaveAttribute("href", "/admin/merchants/m-2");
  });

  it("keeps the quotes explanation for wallets without merchant bindings", async () => {
    api.deleteWallet.mockRejectedValue(new ApiError({ status: 409, code: "conflict", message: "referenced by quotes" }));
    renderAdmin(<WalletsPage />);
    await screen.findAllByText("Ledger main");
    await userEvent.click(within(rowOf("Ledger main")).getByRole("button", { name: "More actions: Ledger main" }));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Delete" }));
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Delete wallet" }));
    const dialog = await screen.findByRole("dialog", { name: "This wallet is in use and can't be deleted" });
    expect(within(dialog).getByText(/Invoice quotes have used its addresses/)).toBeInTheDocument();
    expect(within(dialog).queryByTestId("wallet-in-use-merchants")).not.toBeInTheDocument();
  });
});
