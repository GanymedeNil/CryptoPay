// Package catalog lists the chains and assets the gateway knows out of the
// box. On startup missing rows are inserted into the database; existing rows
// (which the admin may have edited) are never overwritten.
//
// The default RPC endpoints here are FREE PUBLIC endpoints meant for tests and
// local development only. Production deployments configure commercial RPC
// URLs (Alchemy, QuickNode, Infura, TronGrid, Helius, GetBlock, ...) per chain
// in the admin console or via the RPC_<CHAIN_ID> environment variables.
package catalog

import (
	"time"

	"cryptopay/internal/domain"
)

func evm(id, name, networkID, native string, testnet bool, rpc string, conf int, poll time.Duration, batch int, explorer string) domain.Chain {
	return domain.Chain{
		ID: id, Family: domain.FamilyEVM, Name: name, NetworkID: networkID, NativeSymbol: native,
		IsTestnet: testnet, RPCURLs: []string{rpc}, Confirmations: conf, PollInterval: poll, ScanBatch: batch,
		ExplorerTxURL: explorer + "/tx/{tx}", ExplorerAddr: explorer + "/address/{address}",
	}
}

// Chains returns the built-in chain definitions. Mainnets are enabled by
// default; testnets are disabled until switched on in the admin console.
func Chains() []domain.Chain {
	chains := []domain.Chain{
		evm("ethereum", "Ethereum", "1", "ETH", false, "https://ethereum-rpc.publicnode.com", 12, 12*time.Second, 50, "https://etherscan.io"),
		evm("bsc", "BNB Smart Chain", "56", "BNB", false, "https://bsc-rpc.publicnode.com", 15, 5*time.Second, 50, "https://bscscan.com"),
		evm("polygon", "Polygon", "137", "POL", false, "https://polygon-bor-rpc.publicnode.com", 64, 5*time.Second, 50, "https://polygonscan.com"),
		evm("arbitrum", "Arbitrum One", "42161", "ETH", false, "https://arbitrum-one-rpc.publicnode.com", 40, 5*time.Second, 200, "https://arbiscan.io"),
		evm("base", "Base", "8453", "ETH", false, "https://base-rpc.publicnode.com", 20, 5*time.Second, 100, "https://basescan.org"),
		{
			ID: "tron", Family: domain.FamilyTron, Name: "Tron", NativeSymbol: "TRX",
			RPCURLs: []string{"https://api.trongrid.io"}, Confirmations: 19, PollInterval: 5 * time.Second, ScanBatch: 20,
			ExplorerTxURL: "https://tronscan.org/#/transaction/{tx}", ExplorerAddr: "https://tronscan.org/#/address/{address}",
		},
		{
			ID: "bitcoin", Family: domain.FamilyUTXO, Name: "Bitcoin", NativeSymbol: "BTC",
			RPCURLs: []string{"https://bitcoin-rpc.publicnode.com"}, Confirmations: 2, PollInterval: 30 * time.Second, ScanBatch: 3,
			ExplorerTxURL: "https://mempool.space/tx/{tx}", ExplorerAddr: "https://mempool.space/address/{address}",
			Params: domain.ChainParam{Bech32HRP: "bc", P2PKHVersion: 0x00, P2SHVersion: 0x05, URIScheme: "bitcoin", BIP44CoinType: 0},
		},
		{
			ID: "litecoin", Family: domain.FamilyUTXO, Name: "Litecoin", NativeSymbol: "LTC",
			// No keyless Litecoin JSON-RPC exists on publicnode/drpc/blockpi/ankr/nownodes/getblock
			// (checked 2026-11). Tatum's gateway works without a key but anonymous use is limited
			// to 5 requests/minute: enough for local dev, not for production or catch-up scans.
			RPCURLs: []string{"https://litecoin-mainnet.gateway.tatum.io"}, Confirmations: 6, PollInterval: 60 * time.Second, ScanBatch: 1,
			ExplorerTxURL: "https://litecoinspace.org/tx/{tx}", ExplorerAddr: "https://litecoinspace.org/address/{address}",
			Params: domain.ChainParam{Bech32HRP: "ltc", P2PKHVersion: 0x30, P2SHVersion: 0x32, URIScheme: "litecoin", BIP44CoinType: 2},
		},
		{
			ID: "solana", Family: domain.FamilySolana, Name: "Solana", NativeSymbol: "SOL",
			RPCURLs: []string{"https://api.mainnet-beta.solana.com"}, Confirmations: 1, PollInterval: 5 * time.Second, ScanBatch: 2000,
			ExplorerTxURL: "https://solscan.io/tx/{tx}", ExplorerAddr: "https://solscan.io/account/{address}",
		},

		// ---- Testnets ----
		evm("sepolia", "Ethereum Sepolia", "11155111", "ETH", true, "https://ethereum-sepolia-rpc.publicnode.com", 3, 12*time.Second, 50, "https://sepolia.etherscan.io"),
		{
			ID: "tron-nile", Family: domain.FamilyTron, Name: "Tron Nile Testnet", NativeSymbol: "TRX", IsTestnet: true,
			RPCURLs: []string{"https://nile.trongrid.io"}, Confirmations: 19, PollInterval: 5 * time.Second, ScanBatch: 20,
			ExplorerTxURL: "https://nile.tronscan.org/#/transaction/{tx}", ExplorerAddr: "https://nile.tronscan.org/#/address/{address}",
		},
		{
			ID: "bitcoin-testnet", Family: domain.FamilyUTXO, Name: "Bitcoin Testnet", NativeSymbol: "BTC", IsTestnet: true,
			RPCURLs: []string{"https://bitcoin-testnet-rpc.publicnode.com"}, Confirmations: 1, PollInterval: 30 * time.Second, ScanBatch: 5,
			ExplorerTxURL: "https://mempool.space/testnet/tx/{tx}", ExplorerAddr: "https://mempool.space/testnet/address/{address}",
			Params: domain.ChainParam{Bech32HRP: "tb", P2PKHVersion: 0x6f, P2SHVersion: 0xc4, URIScheme: "bitcoin", BIP44CoinType: 1},
		},
		{
			ID: "solana-devnet", Family: domain.FamilySolana, Name: "Solana Devnet", NativeSymbol: "SOL", IsTestnet: true,
			RPCURLs: []string{"https://api.devnet.solana.com"}, Confirmations: 1, PollInterval: 5 * time.Second, ScanBatch: 2000,
			ExplorerTxURL: "https://solscan.io/tx/{tx}?cluster=devnet", ExplorerAddr: "https://solscan.io/account/{address}?cluster=devnet",
		},
	}
	for i := range chains {
		chains[i].Enabled = !chains[i].IsTestnet
	}
	return chains
}

func asset(id, chainID, symbol, name, contract string, decimals, precision int, rateKey string, stable bool, sort int) domain.Asset {
	return domain.Asset{
		ID: id, ChainID: chainID, Symbol: symbol, Name: name, ContractAddress: contract,
		Decimals: decimals, QuotePrecision: precision, RateKey: rateKey, IsStablecoin: stable, Enabled: true, SortOrder: sort,
	}
}

// Assets returns the built-in payable assets. Contract addresses are in the
// canonical form of package address. RateKey is a CoinGecko coin id.
func Assets() []domain.Asset {
	const (
		usdt = "tether"
		usdc = "usd-coin"
	)
	return []domain.Asset{
		// Stablecoins — the bulk of real-world crypto payments.
		asset("usdt-tron", "tron", "USDT", "Tether USD", "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", 6, 2, usdt, true, 10),
		asset("usdt-ethereum", "ethereum", "USDT", "Tether USD", "0xdAC17F958D2ee523a2206206994597C13D831ec7", 6, 2, usdt, true, 11),
		asset("usdt-bsc", "bsc", "USDT", "Tether USD", "0x55d398326f99059fF775485246999027B3197955", 18, 2, usdt, true, 12),
		asset("usdt-polygon", "polygon", "USDT", "Tether USD", "0xc2132D05D31c914a87C6611C10748AEb04B58e8F", 6, 2, usdt, true, 13),
		asset("usdt-arbitrum", "arbitrum", "USDT", "Tether USD", "0xFd086bC7CD5C481DCC9C85ebE478A1C0b69FCbb9", 6, 2, usdt, true, 14),
		asset("usdt-solana", "solana", "USDT", "Tether USD", "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB", 6, 2, usdt, true, 15),
		asset("usdc-ethereum", "ethereum", "USDC", "USD Coin", "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", 6, 2, usdc, true, 20),
		asset("usdc-base", "base", "USDC", "USD Coin", "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913", 6, 2, usdc, true, 21),
		asset("usdc-solana", "solana", "USDC", "USD Coin", "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v", 6, 2, usdc, true, 22),
		asset("usdc-polygon", "polygon", "USDC", "USD Coin", "0x3c499c542cEF5E3811e1192ce70d8cC03d5c3359", 6, 2, usdc, true, 23),
		asset("usdc-arbitrum", "arbitrum", "USDC", "USD Coin", "0xaf88d065e77c8cC2239327C5EDb3A432268e5831", 6, 2, usdc, true, 24),
		asset("usdc-bsc", "bsc", "USDC", "USD Coin", "0x8AC76a51cc950d9822D68b83fE1Ad97B32Cd580d", 18, 2, usdc, true, 25),

		// Native coins.
		asset("btc-bitcoin", "bitcoin", "BTC", "Bitcoin", "", 8, 8, "bitcoin", false, 30),
		asset("ltc-litecoin", "litecoin", "LTC", "Litecoin", "", 8, 6, "litecoin", false, 31),
		asset("eth-ethereum", "ethereum", "ETH", "Ether", "", 18, 6, "ethereum", false, 32),
		asset("trx-tron", "tron", "TRX", "TRON", "", 6, 2, "tron", false, 33),
		asset("bnb-bsc", "bsc", "BNB", "BNB", "", 18, 5, "binancecoin", false, 34),
		asset("sol-solana", "solana", "SOL", "Solana", "", 9, 4, "solana", false, 35),
		asset("eth-base", "base", "ETH", "Ether", "", 18, 6, "ethereum", false, 36),
		asset("eth-arbitrum", "arbitrum", "ETH", "Ether", "", 18, 6, "ethereum", false, 37),

		// Testnets.
		asset("eth-sepolia", "sepolia", "ETH", "Sepolia Ether", "", 18, 6, "ethereum", false, 90),
		asset("usdc-sepolia", "sepolia", "USDC", "USD Coin (Sepolia)", "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", 6, 2, usdc, true, 91),
		asset("trx-tron-nile", "tron-nile", "TRX", "TRON (Nile)", "", 6, 2, "tron", false, 92),
		asset("usdt-tron-nile", "tron-nile", "USDT", "Tether USD (Nile)", "TXYZopYRdj2D9XRtbG411XZZ3kM5VkAeBf", 6, 2, usdt, true, 93),
		asset("btc-bitcoin-testnet", "bitcoin-testnet", "BTC", "Bitcoin (Testnet)", "", 8, 8, "bitcoin", false, 94),
		asset("sol-solana-devnet", "solana-devnet", "SOL", "Solana (Devnet)", "", 9, 4, "solana", false, 95),
		asset("usdc-solana-devnet", "solana-devnet", "USDC", "USD Coin (Devnet)", "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU", 6, 2, usdc, true, 96),
	}
}
