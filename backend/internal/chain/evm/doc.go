// Package evm is the read-only chain.Client adapter for EVM networks
// (Ethereum, BNB Smart Chain, Polygon, Arbitrum, Base, Sepolia, ...).
//
// It talks raw Ethereum JSON-RPC over HTTP using only the standard library:
// requests are batched, every HTTP attempt has its own timeout, failed
// requests are retried a bounded number of times with exponential backoff
// and fail over across the configured endpoints (transport errors, timeouts,
// HTTP 5xx/408/429/401/403/404, rate limits). Provider throttling is reported
// as a wrapped chain.ErrRateLimited and eth_getLogs range / result-size
// rejections as a wrapped chain.ErrRangeTooLarge. Before an endpoint is first
// used its eth_chainId is checked against domain.Chain.NetworkID. Errors and
// log records name the endpoint host only: RPC URLs usually embed API keys.
//
// # Detection
//
// ERC-20 tokens: eth_getLogs over the block range with address = the
// requested token contracts and topics
// [Transfer(address,address,uint256), any, watched recipients]. Watched
// addresses are split into chunks (WithAddressChunkSize) because providers
// cap topic OR-lists. Only logs with exactly three topics and 32 bytes of
// data are accepted, which excludes ERC-721 Transfer events (the token id is
// indexed, so they carry four topics). Removed logs and zero-amount transfers
// (typical of address-poisoning spam) are ignored. Index is the log index.
// The amount is the value of the Transfer event, so fee-on-transfer tokens
// report what the recipient actually got, while rebasing balance changes that
// emit no event are not seen.
//
// Native coin: every block of the range is fetched with full transactions;
// a transaction counts when its "to" is watched and its value is non-zero,
// and only those candidates are confirmed successful through their receipts
// (status 1). Index is -1.
//
// Native coin sent by a contract through an internal call (CALL with value
// from a smart-contract wallet such as a Safe, an exchange batch payout
// contract, a bridge or a DEX router unwrapping WETH) is NOT detected: it
// appears in no transaction "to" field and emits no log, and seeing it would
// need debug/trace APIs that public and most commercial endpoints do not
// offer. Merchants should tell customers to pay native coin from a regular
// account (EOA).
//
// # Consistency
//
// ScanTransfers is exhaustive or fails: a block or receipt the provider
// cannot (yet) return, a node lagging behind the requested range, a
// malformed response or a block hash changing during the scan (reorg) make
// the whole call fail with no partial result, so the caller retries the same
// range. Results are ordered by block number, then index (native transfers,
// index -1, come first in transaction order).
//
// VerifyTransfer reads the block header at the transfer's height and the
// transaction receipt in one batch. It returns false when the block at that
// height has a different hash (reorg) or the transaction failed, and an error
// whenever the node cannot answer conclusively (height not available yet,
// canonical block but missing receipt), so a lagging node can never cause a
// valid payment to be orphaned.
package evm
