package storetest

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"

	"cryptopay/internal/address"
	"cryptopay/internal/auth"
	"cryptopay/internal/domain"
	"cryptopay/internal/store"
)

// Fixture builders insert a row with sensible defaults, let the caller
// adjust the value before insertion via mods, and fail the test on error.

// Ptr returns a pointer to v (handy for optional fields and …Update structs).
func Ptr[T any](v T) *T { return &v }

// SeedCatalog inserts the built-in chains/assets and default settings.
func SeedCatalog(t testing.TB, s *store.Store) {
	t.Helper()
	if _, err := s.Bootstrap(context.Background()); err != nil {
		t.Fatalf("storetest: seed catalog: %v", err)
	}
}

var seq atomic.Int64

// nextSeq returns a process-unique number for default names / hashes.
func nextSeq() int64 { return seq.Add(1) }

// Merchant inserts an active merchant with a webhook URL and secret.
func Merchant(t testing.TB, s *store.Store, mods ...func(*domain.Merchant)) domain.Merchant {
	t.Helper()
	m := domain.Merchant{
		Name:          fmt.Sprintf("Merchant %d", nextSeq()),
		Status:        domain.MerchantActive,
		WebhookURL:    "https://merchant.example/webhooks",
		WebhookSecret: auth.NewWebhookSecret(),
	}
	for _, f := range mods {
		f(&m)
	}
	out, err := s.CreateMerchant(context.Background(), m)
	if err != nil {
		t.Fatalf("storetest: create merchant: %v", err)
	}
	return out
}

// APIKey creates an API key for a merchant and returns it with its plaintext.
func APIKey(t testing.TB, s *store.Store, merchantID uuid.UUID, mods ...func(*domain.APIKey)) (domain.APIKey, string) {
	t.Helper()
	plain, prefix, hash := auth.NewAPIKey()
	k := domain.APIKey{MerchantID: merchantID, Name: "test key", Prefix: prefix, KeyHash: hash}
	for _, f := range mods {
		f(&k)
	}
	out, err := s.CreateAPIKey(context.Background(), k)
	if err != nil {
		t.Fatalf("storetest: create api key: %v", err)
	}
	return out, plain
}

// Password is the plaintext password of users created by AdminUser.
const Password = "storetest-password"

var passwordHash = sync.OnceValue(func() string {
	h, err := auth.HashPasswordCost(Password, bcrypt.MinCost)
	if err != nil {
		panic(err)
	}
	return h
})

// PasswordHash returns a (low-cost) bcrypt hash of Password.
func PasswordHash() string { return passwordHash() }

// AdminUser inserts an active admin user with the given role, a unique
// e-mail and Password as password.
func AdminUser(t testing.TB, s *store.Store, role domain.Role, mods ...func(*domain.AdminUser)) domain.AdminUser {
	t.Helper()
	u := domain.AdminUser{
		Email:        fmt.Sprintf("user%d@example.com", nextSeq()),
		PasswordHash: PasswordHash(),
		Role:         role,
		Active:       true,
	}
	for _, f := range mods {
		f(&u)
	}
	out, err := s.CreateAdminUser(context.Background(), u)
	if err != nil {
		t.Fatalf("storetest: create admin user: %v", err)
	}
	return out
}

// Session creates a session for a user valid for 24h (store clock) and
// returns it with its plaintext token.
func Session(t testing.TB, s *store.Store, userID uuid.UUID, mods ...func(*domain.AdminSession)) (domain.AdminSession, string) {
	t.Helper()
	plain, hash := auth.NewSessionToken()
	x := domain.AdminSession{UserID: userID, TokenHash: hash, IP: "127.0.0.1", UserAgent: "storetest",
		ExpiresAt: s.Now().Add(24 * time.Hour)}
	for _, f := range mods {
		f(&x)
	}
	out, err := s.CreateSession(context.Background(), x)
	if err != nil {
		t.Fatalf("storetest: create session: %v", err)
	}
	return out, plain
}

// Default receiving material per family. TestXpub is the BIP-32 test
// vector 1 master public key (valid encoding, not an account-level key);
// pass your own via mods when derivation matters.
const TestXpub = "xpub661MyMwAqRbcFtXgS5sYJABqqG9YLmC4Q1Rdap9gSE8NqtwybGhePY2gZ29ESFjqJoCu1Rupje8YtGqsefD265TMg7usUDFdp6W1EGMcet8"

// UniqueXpub returns a valid extended public key no other call returns (a
// child of TestXpub at a process-unique index): wallets must not share key
// material (store.ConstraintWalletXpub).
func UniqueXpub() string {
	k, err := hdkeychain.NewKeyFromString(TestXpub)
	if err != nil {
		panic(err)
	}
	child, err := k.Derive(uint32(nextSeq()) % hdkeychain.HardenedKeyStart)
	if err != nil {
		panic(err) // astronomically unlikely invalid child: never for the indices used in tests
	}
	return child.String()
}

// StaticAddress returns the n-th (n ≥ 1) generated canonical receiving
// address of a family, distinct from StaticAddresses[family] and from every
// other n: for a second static wallet of one family (static addresses are
// unique per family, store.ConstraintWalletStaticAddress). UTXO addresses
// are Bitcoin mainnet (bc1…) P2WPKH.
func StaticAddress(family domain.Family, n int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("storetest static address %s %d", family, n)))
	var (
		out string
		err error
	)
	switch family {
	case domain.FamilyEVM:
		out, err = address.EVMFromBytes(h[:20])
	case domain.FamilyTron:
		out, err = address.TronFromBytes(h[:20])
	case domain.FamilyUTXO:
		out, err = address.SegwitEncode("bc", 0, h[:20])
	case domain.FamilySolana:
		out = base58.Encode(h[:])
	default:
		err = fmt.Errorf("unknown family %q", family)
	}
	if err != nil {
		panic(fmt.Sprintf("storetest: static address: %v", err))
	}
	return out
}

// StaticAddresses are default static receiving addresses per family.
var StaticAddresses = map[domain.Family]string{
	domain.FamilyEVM:    "0x52908400098527886E0F7030069857D2E4169EE7",
	domain.FamilyTron:   "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
	domain.FamilyUTXO:   "bc1qar0srrr7xfkvy5l643lydnw9re59gtzzwf5mdq",
	domain.FamilySolana: "4zMMC9srt5Ri5X14GAgXhaHii3GnPAEERYPJgZJDncDU",
}

// Wallet inserts a wallet of the given family and kind (xpub: a fresh
// UniqueXpub, static: StaticAddresses[family] — a second static wallet of
// the same family needs its own address, e.g. StaticAddress(family, n)).
func Wallet(t testing.TB, s *store.Store, family domain.Family, kind domain.WalletKind, mods ...func(*domain.Wallet)) domain.Wallet {
	t.Helper()
	w := domain.Wallet{Name: fmt.Sprintf("%s %s wallet %d", family, kind, nextSeq()), Family: family, Kind: kind}
	if kind == domain.WalletXpub {
		w.Xpub = UniqueXpub()
	} else {
		w.Address = StaticAddresses[family]
	}
	for _, f := range mods {
		f(&w)
	}
	out, err := s.CreateWallet(context.Background(), w)
	if err != nil {
		t.Fatalf("storetest: create wallet: %v", err)
	}
	return out
}

// AssignWallet makes a wallet the default wallet of a chain (and makes sure
// the chain is enabled).
func AssignWallet(t testing.TB, s *store.Store, chainID string, walletID uuid.UUID) domain.Chain {
	t.Helper()
	c, err := s.UpdateChain(context.Background(), chainID, store.ChainUpdate{
		Enabled: Ptr(true), SetWallet: true, WalletID: &walletID,
	})
	if err != nil {
		t.Fatalf("storetest: assign wallet: %v", err)
	}
	return c
}

// BindWallet makes a wallet the merchant's own receiving wallet on a chain
// (and makes sure the chain is enabled).
func BindWallet(t testing.TB, s *store.Store, merchantID uuid.UUID, chainID string, walletID uuid.UUID) store.MerchantWallet {
	t.Helper()
	ctx := context.Background()
	if _, err := s.UpdateChain(ctx, chainID, store.ChainUpdate{Enabled: Ptr(true)}); err != nil {
		t.Fatalf("storetest: enable chain: %v", err)
	}
	b, err := s.SetMerchantWallet(ctx, merchantID, chainID, walletID)
	if err != nil {
		t.Fatalf("storetest: bind wallet: %v", err)
	}
	return b
}

// Invoice inserts a pending 10 USD invoice (AmountUSD 10) expiring 30
// minutes after the store clock's now.
func Invoice(t testing.TB, s *store.Store, merchantID uuid.UUID, mods ...func(*domain.Invoice)) domain.Invoice {
	t.Helper()
	usd := decimal.NewFromInt(10)
	inv := domain.Invoice{
		MerchantID:    merchantID,
		Title:         "Test order",
		PriceAmount:   decimal.NewFromInt(10),
		PriceCurrency: "USD",
		AmountUSD:     &usd,
		Status:        domain.InvoicePending,
		Metadata:      json.RawMessage(`{}`),
		ExpiresAt:     s.Now().Add(30 * time.Minute),
	}
	for _, f := range mods {
		f(&inv)
	}
	out, err := s.CreateInvoice(context.Background(), inv)
	if err != nil {
		t.Fatalf("storetest: create invoice: %v", err)
	}
	return out
}

// Quote inserts an active quote of inv for assetID (which must exist —
// seed the catalog) on wallet w. Static wallets use w.Address; xpub wallets
// get a fresh index from AllocateAddressIndex and a placeholder address
// unique to the wallet and index ("test:<wallet>:<index>") unless mods set
// one. Defaults: rate 1, amount due 10_000_000 base units, watch_until =
// inv.ExpiresAt + 24h.
func Quote(t testing.TB, s *store.Store, inv domain.Invoice, assetID string, w domain.Wallet, mods ...func(*domain.Quote)) domain.Quote {
	t.Helper()
	ctx := context.Background()
	a, err := s.GetAsset(ctx, assetID)
	if err != nil {
		t.Fatalf("storetest: quote asset %s: %v", assetID, err)
	}
	q := domain.Quote{
		InvoiceID:  inv.ID,
		AssetID:    a.ID,
		ChainID:    a.ChainID,
		WalletID:   w.ID,
		WalletKind: w.Kind,
		Rate:       decimal.NewFromInt(1),
		AmountDue:  big.NewInt(10_000_000),
		Status:     domain.QuoteActive,
		WatchUntil: inv.ExpiresAt.Add(24 * time.Hour),
	}
	if w.Kind == domain.WalletStatic {
		q.Address = w.Address
	}
	for _, f := range mods {
		f(&q)
	}
	if q.Address == "" && q.WalletKind == domain.WalletXpub {
		idx, err := s.AllocateAddressIndex(ctx, w.ID)
		if err != nil {
			t.Fatalf("storetest: allocate index: %v", err)
		}
		q.AddressIndex = &idx
		q.Address = fmt.Sprintf("test:%s:%d", w.ID, idx)
	}
	out, err := s.CreateQuote(ctx, q)
	if err != nil {
		t.Fatalf("storetest: create quote: %v", err)
	}
	return out
}

// Transfer inserts a pending, unmatched transfer of 1 base unit on chain
// "tron" with a unique tx hash; adjust with mods (e.g. set QuoteID,
// InvoiceID, AssetID, Amount, ToAddress).
func Transfer(t testing.TB, s *store.Store, mods ...func(*domain.Transfer)) domain.Transfer {
	t.Helper()
	x := domain.Transfer{
		ChainID:     "tron",
		TxHash:      randomHex(32),
		Index:       0,
		BlockNumber: 100,
		BlockHash:   "blockhash",
		BlockTime:   s.Now(),
		FromAddress: "TFromAddress",
		ToAddress:   "TToAddress",
		Amount:      big.NewInt(1),
		Status:      domain.TransferPending,
	}
	for _, f := range mods {
		f(&x)
	}
	out, inserted, err := s.InsertTransferIfNew(context.Background(), x)
	if err != nil {
		t.Fatalf("storetest: insert transfer: %v", err)
	}
	if !inserted {
		t.Fatalf("storetest: transfer %s/%s/%d already exists", x.ChainID, x.TxHash, x.Index)
	}
	return out
}

// TransferFor inserts a transfer credited to quote q (same chain, asset,
// address and invoice) for amount base units.
func TransferFor(t testing.TB, s *store.Store, q domain.Quote, amount int64, mods ...func(*domain.Transfer)) domain.Transfer {
	t.Helper()
	ctx := context.Background()
	a, err := s.GetAsset(ctx, q.AssetID)
	if err != nil {
		t.Fatalf("storetest: transfer asset: %v", err)
	}
	all := append([]func(*domain.Transfer){func(x *domain.Transfer) {
		x.ChainID = q.ChainID
		x.AssetID = q.AssetID
		x.TokenContract = a.ContractAddress
		x.ToAddress = q.Address
		x.Amount = big.NewInt(amount)
		x.QuoteID = &q.ID
		x.InvoiceID = &q.InvoiceID
	}}, mods...)
	return Transfer(t, s, all...)
}

// WebhookEvent enqueues a pending invoice.paid event for inv.
func WebhookEvent(t testing.TB, s *store.Store, inv domain.Invoice, mods ...func(*domain.WebhookEvent)) domain.WebhookEvent {
	t.Helper()
	e := domain.WebhookEvent{
		ID:         uuid.New(),
		MerchantID: inv.MerchantID,
		InvoiceID:  inv.ID,
		EventType:  domain.EventPaid,
		URL:        "https://merchant.example/webhooks",
	}
	e.Payload = json.RawMessage(fmt.Sprintf(`{"id":%q,"type":%q,"data":{"id":%q}}`, e.ID, e.EventType, inv.ID))
	for _, f := range mods {
		f(&e)
	}
	out, err := s.EnqueueWebhook(context.Background(), e)
	if err != nil {
		t.Fatalf("storetest: enqueue webhook: %v", err)
	}
	return out
}

// FixedClock returns a controllable clock for store.WithClock.
func FixedClock(start time.Time) *Clock { c := &Clock{}; c.Set(start); return c }

// Clock is a goroutine-safe settable clock.
type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the clock's time.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Set sets the clock's time.
func (c *Clock) Set(t time.Time) { c.mu.Lock(); defer c.mu.Unlock(); c.now = t.UTC() }

// Advance moves the clock forward by d.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.now = c.now.Add(d) }
