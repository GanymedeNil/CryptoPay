/**
 * EVM payments through wagmi core + viem (loaded lazily).
 *
 * connect → switch to `quote.network_id` (wagmi adds the chain from its viem
 * definition when the wallet doesn't know it) → send the native transfer or
 * the ERC-20 `transfer` → return the tx hash. The backend detects the payment
 * on its own; the page keeps polling.
 */
import {
  connect,
  createConfig,
  getAccount,
  http,
  injected,
  sendTransaction,
  switchChain,
  type Config,
  type Connector,
  type CreateConnectorFn,
} from "@wagmi/core";
import { walletConnect } from "@wagmi/connectors";
import type { Transport } from "viem";
import { normalizeWalletError, WalletError } from "../errors";
import { evmProviderFor } from "../injected";
import type { PayRequest, PayResult, WalletAdapter } from "../types";
import { EVM_CHAINS, evmChainById } from "./chains";
import type { Eip1193Provider } from "./eip6963";
import { buildEvmPaymentRequest } from "./tx";

const configs = new Map<string, Config>();

export function getEvmConfig(walletConnectProjectId = ""): Config {
  const cached = configs.get(walletConnectProjectId);
  if (cached) return cached;
  const connectors: CreateConnectorFn[] = [];
  if (walletConnectProjectId && typeof window !== "undefined") {
    connectors.push(
      walletConnect({
        projectId: walletConnectProjectId,
        showQrModal: true,
        metadata: {
          name: "CryptoPay",
          description: "Crypto checkout",
          url: window.location.origin,
          icons: [],
        },
      }),
    );
  }
  const transports = Object.fromEntries(EVM_CHAINS.map((c) => [c.id, http()])) as Record<
    (typeof EVM_CHAINS)[number]["id"],
    Transport
  >;
  const config = createConfig({
    chains: EVM_CHAINS,
    connectors,
    transports,
    // Discovery is done by our own lightweight EIP-6963 store (eip6963.ts).
    multiInjectedProviderDiscovery: false,
    storage: null,
    ssr: false,
  });
  configs.set(walletConnectProjectId, config);
  return config;
}

/** Test helper. */
export function resetEvmConfigsForTests(): void {
  configs.clear();
}

export interface EvmAdapterDeps {
  /** Resolves the EIP-1193 provider of an injected option (tests inject mocks). */
  providerFor?: (optionId: string) => Eip1193Provider | undefined;
}

function connectorFor(
  config: Config,
  req: PayRequest,
  deps: EvmAdapterDeps,
): Connector | CreateConnectorFn {
  const { option } = req;
  if (option.kind === "walletconnect") {
    const wc = config.connectors.find((c) => c.id === "walletConnect");
    if (!wc) throw new WalletError("unsupported", "WalletConnect is not configured");
    return wc;
  }
  const provider = (deps.providerFor ?? evmProviderFor)(option.id);
  if (!provider) throw new WalletError("not_installed", `${option.name || option.id} not found`);
  return injected({
    target: () => ({
      id: option.id,
      name: option.name || "Browser wallet",
      icon: option.icon,
      provider: provider as never,
    }),
  });
}

export async function payWithEvm(req: PayRequest, deps: EvmAdapterDeps = {}): Promise<PayResult> {
  const { quote, amountBase, onStep } = req;
  try {
    const request = buildEvmPaymentRequest(quote, amountBase);
    const chain = evmChainById(request.chainId);
    if (!chain) throw new WalletError("unsupported", `Chain ${request.chainId} is not supported`);

    const config = getEvmConfig(req.walletConnectProjectId);
    onStep?.("connecting");
    const target = connectorFor(config, req, deps);
    const current = getAccount(config);
    let connector: Connector | undefined;
    let chainId: number | undefined;
    if (current.status === "connected" && current.connector.id === (typeof target === "function" ? req.option.id : target.id)) {
      connector = current.connector;
      chainId = current.chainId;
    } else {
      const result = await connect(config, { connector: target });
      connector = getAccount(config).connector;
      chainId = result.chainId;
    }
    const account = getAccount(config).address;
    if (!connector || !account) throw new WalletError("not_installed", "No account connected");

    if (chainId !== request.chainId) {
      onStep?.("switching");
      await switchChain(config, { chainId: request.chainId as never, connector });
    }

    onStep?.("confirm");
    const hash = await sendTransaction(config, {
      connector,
      account,
      chainId: request.chainId as never,
      to: request.to,
      value: request.value,
      data: request.data,
    });
    if (typeof hash !== "string" || !hash) throw new WalletError("unknown", "The wallet returned no transaction hash");
    return { txHash: hash };
  } catch (e) {
    throw normalizeWalletError(e);
  }
}

export const evmAdapter: WalletAdapter = { pay: (req) => payWithEvm(req) };
