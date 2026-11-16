// Package domain holds the shared business types of the gateway. It has no
// dependencies on storage, HTTP or chain packages so every layer can import it.
package domain

import (
	"encoding/json"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Family identifies a group of chains sharing address format and RPC protocol.
type Family string

const (
	FamilyEVM    Family = "evm"
	FamilyTron   Family = "tron"
	FamilyUTXO   Family = "utxo" // Bitcoin, Litecoin
	FamilySolana Family = "solana"
)

func (f Family) Valid() bool {
	switch f {
	case FamilyEVM, FamilyTron, FamilyUTXO, FamilySolana:
		return true
	}
	return false
}

// Chain is one blockchain network the gateway can watch.
type Chain struct {
	ID            string // e.g. "ethereum", "tron", "bitcoin", "sepolia"
	Family        Family
	Name          string
	NetworkID     string // EVM chain id in decimal ("1"); empty for other families
	NativeSymbol  string
	IsTestnet     bool
	Enabled       bool
	RPCURLs       []string // decrypted; first is primary, rest are failover
	Confirmations int      // confirmations required before a transfer is final
	PollInterval  time.Duration
	ScanBatch     int        // max blocks per ScanTransfers call
	ExplorerTxURL string     // template containing "{tx}"
	ExplorerAddr  string     // template containing "{address}"
	Params        ChainParam // family specific parameters
	WalletID      *uuid.UUID // default receiving wallet of this chain (merchants without their own wallet)
	// MerchantWallets is the number of merchant-specific wallets bound to
	// this chain (read-only, computed by the store). A chain without a
	// default wallet still receives payments for those merchants.
	MerchantWallets int

	// Scanner state.
	ScanCursor uint64 // last fully scanned block (0 = never scanned)
	LastHead   uint64
	LastScanAt *time.Time
	LastError  string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ChainParam carries per-chain constants that differ inside one family.
type ChainParam struct {
	// UTXO chains.
	Bech32HRP     string `json:"bech32_hrp,omitempty"`      // "bc", "tb", "ltc"
	P2PKHVersion  byte   `json:"p2pkh_version,omitempty"`   // 0x00 btc, 0x30 ltc, 0x6f testnet
	P2SHVersion   byte   `json:"p2sh_version,omitempty"`    // 0x05 btc, 0x32 ltc, 0xc4 testnet
	URIScheme     string `json:"uri_scheme,omitempty"`      // "bitcoin", "litecoin"
	BIP44CoinType uint32 `json:"bip44_coin_type,omitempty"` // informational
}

// Asset is a payable currency on a specific chain.
type Asset struct {
	ID              string // e.g. "usdt-tron", "btc-bitcoin"
	ChainID         string
	Symbol          string
	Name            string
	ContractAddress string // empty for the chain's native coin; canonical form
	Decimals        int
	QuotePrecision  int    // decimals shown/used when quoting an amount (<= Decimals)
	RateKey         string // price provider id, e.g. CoinGecko "tether"
	IsStablecoin    bool
	Enabled         bool
	SortOrder       int
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (a Asset) IsNative() bool { return a.ContractAddress == "" }

// WalletKind is how receiving addresses are produced.
type WalletKind string

const (
	// WalletXpub derives a fresh address per invoice from an extended public
	// key (watch-only: no private key ever touches the server).
	WalletXpub WalletKind = "xpub"
	// WalletStatic receives everything on one fixed address; invoices are told
	// apart by a unique amount.
	WalletStatic WalletKind = "static"
)

// Wallet is a receiving configuration that can be assigned to chains of its
// family, as a chain's default wallet or as one merchant's wallet on a chain.
type Wallet struct {
	ID        uuid.UUID
	Name      string
	Family    Family
	Kind      WalletKind
	Xpub      string // WalletXpub only
	Address   string // WalletStatic only (canonical form)
	NextIndex uint32 // next unused derivation index (WalletXpub)
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InvoiceStatus is the lifecycle state of an invoice.
type InvoiceStatus string

const (
	InvoicePending    InvoiceStatus = "pending"    // waiting for payment
	InvoicePartial    InvoiceStatus = "partial"    // some funds seen, less than due
	InvoiceConfirming InvoiceStatus = "confirming" // full amount seen, awaiting confirmations
	InvoicePaid       InvoiceStatus = "paid"       // full amount confirmed (terminal)
	InvoiceUnderpaid  InvoiceStatus = "underpaid"  // expired with confirmed partial funds (terminal unless topped up in grace)
	InvoiceExpired    InvoiceStatus = "expired"    // expired without funds (may still become paid during grace period)
	InvoiceCancelled  InvoiceStatus = "cancelled"  // cancelled by merchant/admin (terminal)
)

func (s InvoiceStatus) Valid() bool {
	switch s {
	case InvoicePending, InvoicePartial, InvoiceConfirming, InvoicePaid, InvoiceUnderpaid, InvoiceExpired, InvoiceCancelled:
		return true
	}
	return false
}

// Open reports whether the customer can still pay normally.
func (s InvoiceStatus) Open() bool {
	return s == InvoicePending || s == InvoicePartial || s == InvoiceConfirming
}

type Invoice struct {
	ID              uuid.UUID // also the public checkout id (unguessable v4)
	MerchantID      uuid.UUID
	MerchantOrderID string // merchant-side idempotency key; unique per merchant when non-empty
	Title           string
	Description     string
	PriceAmount     decimal.Decimal  // amount in PriceCurrency
	PriceCurrency   string           // ISO fiat ("USD") or a crypto symbol ("USDT")
	AmountUSD       *decimal.Decimal // USD value at creation, for reporting
	Status          InvoiceStatus
	Overpaid        bool
	PaidLate        bool // paid after ExpiresAt, inside the grace period
	PaidQuoteID     *uuid.UUID
	SuccessURL      string
	CancelURL       string
	CustomerEmail   string
	Metadata        json.RawMessage
	ExpiresAt       time.Time
	PaidAt          *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type QuoteStatus string

const (
	QuoteActive     QuoteStatus = "active"     // currently offered to the customer
	QuoteSuperseded QuoteStatus = "superseded" // customer switched asset; still watched
	QuotePaid       QuoteStatus = "paid"
	QuoteClosed     QuoteStatus = "closed" // no longer watched
)

// Quote is a locked price for paying an invoice with one asset, bound to a
// receiving address. An invoice may accumulate several quotes if the customer
// switches asset; all of them stay watched until WatchUntil.
type Quote struct {
	ID              uuid.UUID
	InvoiceID       uuid.UUID
	AssetID         string
	ChainID         string
	WalletID        uuid.UUID
	WalletKind      WalletKind
	Address         string
	AddressIndex    *uint32         // derivation index (xpub wallets)
	Rate            decimal.Decimal // PriceCurrency per 1 whole asset unit
	AmountDue       *big.Int        // base units
	AmountReceived  *big.Int        // base units seen on chain (pending + confirmed, excluding orphaned)
	AmountConfirmed *big.Int        // base units with enough confirmations
	Status          QuoteStatus
	WatchUntil      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type TransferStatus string

const (
	TransferPending   TransferStatus = "pending"   // seen, not enough confirmations
	TransferConfirmed TransferStatus = "confirmed" // final
	TransferOrphaned  TransferStatus = "orphaned"  // dropped by a reorg
)

// Transfer is an incoming on-chain transfer to a watched address.
type Transfer struct {
	ID            uuid.UUID
	ChainID       string
	TxHash        string
	Index         int // position inside the tx: log index / vout / instruction index
	BlockNumber   uint64
	BlockHash     string
	BlockTime     time.Time
	FromAddress   string
	ToAddress     string
	TokenContract string // empty for native coin
	AssetID       string // empty if the token is not a configured asset
	Amount        *big.Int
	QuoteID       *uuid.UUID // nil = unmatched
	InvoiceID     *uuid.UUID
	Status        TransferStatus
	Confirmations int
	DetectedAt    time.Time
	ConfirmedAt   *time.Time
}

type MerchantStatus string

const (
	MerchantActive   MerchantStatus = "active"
	MerchantDisabled MerchantStatus = "disabled"
)

type Merchant struct {
	ID            uuid.UUID
	Name          string
	Status        MerchantStatus
	WebhookURL    string
	WebhookSecret string // decrypted; used to sign webhook payloads
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type APIKey struct {
	ID         uuid.UUID
	MerchantID uuid.UUID
	Name       string
	Prefix     string // first characters of the key, for display
	KeyHash    string // hex sha256 of the full key
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

type Role string

const (
	RoleOwner  Role = "owner"  // everything, including user management
	RoleAdmin  Role = "admin"  // read + write configuration
	RoleViewer Role = "viewer" // read only
)

func (r Role) Valid() bool     { return r == RoleOwner || r == RoleAdmin || r == RoleViewer }
func (r Role) CanWrite() bool  { return r == RoleOwner || r == RoleAdmin }
func (r Role) CanManage() bool { return r == RoleOwner }

type AdminUser struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Role         Role
	Active       bool
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AdminSession struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	TokenHash string // hex sha256 of the cookie token
	IP        string
	UserAgent string
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Webhook event types sent to merchants.
const (
	EventPaymentDetected = "invoice.payment_detected"
	EventConfirming      = "invoice.confirming"
	EventPaid            = "invoice.paid"
	EventUnderpaid       = "invoice.underpaid"
	EventExpired         = "invoice.expired"
	EventCancelled       = "invoice.cancelled"
)

type WebhookStatus string

const (
	WebhookPending   WebhookStatus = "pending"
	WebhookDelivered WebhookStatus = "delivered"
	WebhookFailed    WebhookStatus = "failed" // retries exhausted
)

// WebhookEvent is an outbox row: one notification to one merchant endpoint.
type WebhookEvent struct {
	ID             uuid.UUID
	MerchantID     uuid.UUID
	InvoiceID      uuid.UUID
	EventType      string
	Payload        json.RawMessage
	URL            string
	Status         WebhookStatus
	Attempts       int
	NextAttemptAt  time.Time
	LastStatusCode int
	LastError      string
	LastResponse   string // truncated response body
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

type AuditLog struct {
	ID         int64
	ActorID    *uuid.UUID
	ActorEmail string
	Action     string // e.g. "chain.update", "merchant.create"
	TargetType string
	TargetID   string
	Detail     json.RawMessage
	IP         string
	CreatedAt  time.Time
}

// Settings are runtime-tunable options stored in the database.
type Settings struct {
	InvoiceTTLSeconds     int    `json:"invoice_ttl_seconds"`    // default lifetime of an invoice
	GracePeriodSeconds    int    `json:"grace_period_seconds"`   // keep watching addresses after expiry
	UnderpayToleranceBps  int    `json:"underpay_tolerance_bps"` // accepted shortfall, basis points of amount due
	StablecoinPeg         bool   `json:"stablecoin_peg"`         // treat stablecoins as exactly 1 USD
	RateMarkupBps         int    `json:"rate_markup_bps"`        // added to crypto amount to cover volatility
	WebhookMaxAttempts    int    `json:"webhook_max_attempts"`
	WebhookTimeoutSeconds int    `json:"webhook_timeout_seconds"`
	CheckoutBaseURL       string `json:"checkout_base_url"` // public origin of the checkout UI, e.g. https://pay.example.com
	WalletConnectID       string `json:"walletconnect_project_id"`
}

func DefaultSettings() Settings {
	return Settings{
		InvoiceTTLSeconds:     30 * 60,
		GracePeriodSeconds:    24 * 60 * 60,
		UnderpayToleranceBps:  50,
		StablecoinPeg:         true,
		RateMarkupBps:         0,
		WebhookMaxAttempts:    10,
		WebhookTimeoutSeconds: 10,
		CheckoutBaseURL:       "http://localhost:3000",
	}
}
