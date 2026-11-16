import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ApiError } from "@/lib/api/client";
import { chainFixture, renderAdmin, walletFixture } from "@/test/admin";
import { CreateWalletDialog, defaultPreviewChain } from "./create-wallet-dialog";

const api = vi.hoisted(() => ({ listChains: vi.fn(), listWallets: vi.fn(), previewWallet: vi.fn(), createWallet: vi.fn(), listAuditLogs: vi.fn() }));
vi.mock("@/lib/api/admin", () => ({ adminApi: api }));

const XPUB = "xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8";
const CHAINS = [
  chainFixture(),
  chainFixture({ id: "sepolia", name: "Ethereum Sepolia", is_testnet: true }),
  chainFixture({ id: "tron", family: "tron", name: "Tron" }),
  chainFixture({ id: "solana", family: "solana", name: "Solana" }),
];

beforeEach(() => {
  vi.clearAllMocks();
  api.listChains.mockResolvedValue({ items: CHAINS });
  api.listWallets.mockResolvedValue({ items: [] });
  api.previewWallet.mockResolvedValue({
    addresses: ["0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222", "0x3333333333333333333333333333333333333333"],
  });
  api.createWallet.mockResolvedValue({ id: "w-new" });
});

describe("defaultPreviewChain", () => {
  it("prefers an enabled mainnet of the family", () => {
    expect(defaultPreviewChain(CHAINS, "evm")).toBe("ethereum");
    expect(defaultPreviewChain([chainFixture({ enabled: false }), CHAINS[1]], "evm")).toBe("ethereum");
    expect(defaultPreviewChain(CHAINS, "utxo")).toBe("");
  });
});

describe("CreateWalletDialog", () => {
  it("walks through type → key + preview → name, refusing secrets on the way", async () => {
    const onOpenChange = vi.fn();
    renderAdmin(<CreateWalletDialog open onOpenChange={onOpenChange} />);
    const dialog = screen.getByRole("dialog", { name: "Add a receiving wallet" });

    // step 1: EVM + extended public key are the defaults
    expect(within(dialog).getByRole("radio", { name: /^EVM/ })).toBeChecked();
    expect(within(dialog).getByRole("radio", { name: /Extended public key \(recommended\)/ })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));

    // step 2: the warning is prominent, secrets are refused locally
    expect(within(dialog).getByText("Never paste a private key or seed phrase here")).toBeInTheDocument();
    const key = within(dialog).getByLabelText("Extended public key");
    await userEvent.type(key, "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about");
    expect(within(dialog).getByText(/looks like a private key or seed phrase/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Show addresses to check" })).toBeDisabled();
    expect(api.previewWallet).not.toHaveBeenCalled();

    await userEvent.clear(key);
    await userEvent.type(key, XPUB);
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Show addresses to check" })).toBeEnabled());
    await userEvent.click(within(dialog).getByRole("button", { name: "Show addresses to check" }));
    expect(api.previewWallet).toHaveBeenCalledWith({ family: "evm", kind: "xpub", chain_id: "ethereum", xpub: XPUB });
    const list = await within(dialog).findByTestId("preview-addresses");
    expect(within(list).getAllByRole("listitem")).toHaveLength(3);
    expect(within(list).getByText("0x1111111111111111111111111111111111111111")).toBeInTheDocument();

    // must confirm the comparison before continuing
    const next = within(dialog).getByRole("button", { name: "Next" });
    expect(next).toBeDisabled();
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "I checked: these match my wallet exactly" }));
    expect(next).toBeEnabled();
    await userEvent.click(next);

    // step 3: name and create
    await userEvent.click(within(dialog).getByRole("button", { name: "Add wallet" }));
    expect(within(dialog).getByText("Enter a name")).toBeInTheDocument();
    await userEvent.type(within(dialog).getByLabelText("Name"), "Ledger main");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add wallet" }));
    expect(api.createWallet).toHaveBeenCalledWith({ name: "Ledger main", family: "evm", kind: "xpub", xpub: XPUB });
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("offers only fixed addresses for Solana and previews the normalised address", async () => {
    api.previewWallet.mockResolvedValue({ addresses: ["9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"] });
    renderAdmin(<CreateWalletDialog open onOpenChange={() => {}} />);
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("radio", { name: /^Solana/ }));
    expect(within(dialog).getByRole("radio", { name: /Extended public key/ })).toBeDisabled();
    expect(within(dialog).getByRole("radio", { name: /Fixed address/ })).toBeChecked();
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText("Receiving address"), "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM");
    await userEvent.click(within(dialog).getByRole("button", { name: "Show addresses to check" }));
    await waitFor(() =>
      expect(api.previewWallet).toHaveBeenCalledWith({
        family: "solana",
        kind: "static",
        chain_id: "solana",
        address: "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM",
      }),
    );
    expect(await within(dialog).findByText(/normalised address/)).toBeInTheDocument();
  });

  it("shows preview errors from the server", async () => {
    api.previewWallet.mockRejectedValue(new ApiError({ status: 400, code: "invalid_request", message: "bad checksum" }));
    renderAdmin(<CreateWalletDialog open onOpenChange={() => {}} />);
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText("Extended public key"), XPUB);
    await userEvent.click(within(dialog).getByRole("button", { name: "Show addresses to check" }));
    expect(await within(dialog).findByText("Some of the values are invalid: bad checksum")).toBeInTheDocument();
  });

  it("refuses key material that is already registered, before and after submitting", async () => {
    api.listWallets.mockResolvedValue({ items: [walletFixture({ name: "Ledger main", xpub: XPUB })] });
    renderAdmin(<CreateWalletDialog open onOpenChange={() => {}} />);
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText("Extended public key"), XPUB);
    expect(await within(dialog).findByText(/Already registered as the wallet “Ledger main”/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Show addresses to check" })).toBeDisabled();
    expect(api.previewWallet).not.toHaveBeenCalled();
  });

  it("puts the server's duplicate error (409) back on the key field", async () => {
    api.createWallet.mockRejectedValue(
      new ApiError({ status: 409, code: "conflict", message: "this extended public key is already registered", fields: { xpub: "is already registered" } }),
    );
    const onOpenChange = vi.fn();
    renderAdmin(<CreateWalletDialog open onOpenChange={onOpenChange} />);
    const dialog = screen.getByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText("Extended public key"), XPUB);
    await waitFor(() => expect(within(dialog).getByRole("button", { name: "Show addresses to check" })).toBeEnabled());
    await userEvent.click(within(dialog).getByRole("button", { name: "Show addresses to check" }));
    await userEvent.click(await within(dialog).findByRole("checkbox", { name: "I checked: these match my wallet exactly" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText("Name"), "Copy");
    await userEvent.click(within(dialog).getByRole("button", { name: "Add wallet" }));

    // Back on step 2, the error is on the key and the wizard can't continue with it.
    const key = await within(dialog).findByLabelText("Extended public key");
    expect(key).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByText(/Already registered; it can't be added twice/)).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "Next" })).toBeDisabled();
    expect(onOpenChange).not.toHaveBeenCalled();
    // Changing the key clears it.
    await userEvent.type(key, "x");
    expect(within(dialog).queryByText(/Already registered; it can't be added twice/)).not.toBeInTheDocument();
  });
});
