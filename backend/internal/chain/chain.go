// Package chain defines the contract between the gateway core and the
// per-family blockchain adapters (evm, tron, utxo, solana).
//
// Adapters are read-only: they never sign or broadcast. They only answer
// "what is the chain head" and "which transfers arrived at these addresses".
package chain

import (
	"context"
	"errors"
	"math/big"
	"time"

	"cryptopay/internal/domain"
)

// Transfer is one incoming value movement found on chain.
//
// Addresses and Token are in the canonical form produced by package address
// (EVM: EIP-55 checksum, Tron: base58 "T...", UTXO: as encoded by the node,
// Solana: base58). (TxHash, Index) is unique per chain; for one tx paying the
// same address twice the adapter must return two transfers with distinct Index.
type Transfer struct {
	TxHash      string
	Index       int // EVM/Tron: log index (native transfer: -1); UTXO: vout; Solana: 0-based ordinal within tx
	BlockNumber uint64
	BlockHash   string
	BlockTime   time.Time
	From        string // best effort; may be empty (UTXO)
	To          string
	Token       string // contract / mint address; "" for the native coin
	Amount      *big.Int
}

// ScanRequest asks for incoming transfers in the inclusive block range
// [FromBlock, ToBlock] whose recipient is one of Addresses.
type ScanRequest struct {
	FromBlock uint64
	ToBlock   uint64
	// Addresses are the watched recipients (canonical form). Never empty.
	Addresses []string
	// Tokens are the token contracts / mints to look at (canonical form).
	Tokens []string
	// Native asks for native-coin transfers as well.
	Native bool
}

// Client is implemented by every chain adapter. Implementations must be safe
// for concurrent use and must honour ctx cancellation.
type Client interface {
	// Head returns the latest block height (Solana: latest finalized slot).
	Head(ctx context.Context) (uint64, error)

	// ScanTransfers returns all successful incoming transfers matching req.
	// It must be exhaustive for the range: if any part of the range cannot be
	// read it returns an error and no partial result, so the caller can retry
	// the same range. Failed/reverted transactions must not be returned.
	ScanTransfers(ctx context.Context, req ScanRequest) ([]Transfer, error)

	// VerifyTransfer reports whether a previously returned transfer is still
	// part of the canonical chain (same block hash at that height and the tx
	// is still successful). Used to detect reorgs before finalising.
	VerifyTransfer(ctx context.Context, t Transfer) (bool, error)
}

// Factory builds a Client for a configured chain. c.RPCURLs holds at least
// one endpoint; adapters should fail over to the next URL on transport errors.
type Factory func(c domain.Chain) (Client, error)

var (
	// ErrRateLimited is returned (wrapped) when the RPC provider throttles us.
	ErrRateLimited = errors.New("chain: rpc rate limited")
	// ErrRangeTooLarge is returned (wrapped) when the provider rejects the
	// block range; the caller should retry with a smaller range.
	ErrRangeTooLarge = errors.New("chain: block range too large")
	// ErrNoRPC is returned when a chain has no RPC endpoint configured.
	ErrNoRPC = errors.New("chain: no rpc endpoint configured")
)
