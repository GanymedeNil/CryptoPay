package api

import (
	"encoding/json"
	"net/url"
	"time"

	"cryptopay/internal/domain"
	"cryptopay/internal/store"
	"cryptopay/internal/view"
)

// JSON shapes of the admin API (docs/api.md §3). Invoice, Quote, Transfer
// and AssetInfo come from package view.

type adminUserJSON struct {
	ID               string     `json:"id"`
	Email            string     `json:"email"`
	Role             string     `json:"role"`
	Active           bool       `json:"active"`
	LastLoginAt      *time.Time `json:"last_login_at"`
	CreatedAt        time.Time  `json:"created_at"`
	TwoFactorEnabled bool       `json:"two_factor_enabled"`
}

func newAdminUserJSON(u domain.AdminUser) adminUserJSON {
	return adminUserJSON{ID: u.ID.String(), Email: u.Email, Role: string(u.Role), Active: u.Active,
		LastLoginAt: utcPtr(u.LastLoginAt), CreatedAt: u.CreatedAt.UTC(), TwoFactorEnabled: u.TwoFactorEnabled()}
}

type merchantJSON struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Status           string    `json:"status"`
	WebhookURL       string    `json:"webhook_url"`
	HasWebhookSecret bool      `json:"has_webhook_secret"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func newMerchantJSON(m domain.Merchant) merchantJSON {
	return merchantJSON{ID: m.ID.String(), Name: m.Name, Status: string(m.Status), WebhookURL: m.WebhookURL,
		HasWebhookSecret: m.WebhookSecret != "", CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC()}
}

type apiKeyJSON struct {
	ID         string     `json:"id"`
	MerchantID string     `json:"merchant_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	LastUsedAt *time.Time `json:"last_used_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func newAPIKeyJSON(k domain.APIKey) apiKeyJSON {
	return apiKeyJSON{ID: k.ID.String(), MerchantID: k.MerchantID.String(), Name: k.Name, Prefix: k.Prefix,
		LastUsedAt: utcPtr(k.LastUsedAt), RevokedAt: utcPtr(k.RevokedAt), CreatedAt: k.CreatedAt.UTC()}
}

type walletJSON struct {
	ID               string              `json:"id"`
	Name             string              `json:"name"`
	Family           string              `json:"family"`
	Kind             string              `json:"kind"`
	Xpub             string              `json:"xpub"`
	Address          string              `json:"address"`
	NextIndex        uint32              `json:"next_index"`
	ChainIDs         []string            `json:"chain_ids"`
	MerchantBindings []walletBindingJSON `json:"merchant_bindings"`
	CreatedAt        time.Time           `json:"created_at"`
}

type walletBindingJSON struct {
	MerchantID   string `json:"merchant_id"`
	MerchantName string `json:"merchant_name"`
	ChainID      string `json:"chain_id"`
}

func newWalletJSON(w store.WalletView) walletJSON {
	ids := w.ChainIDs
	if ids == nil {
		ids = []string{}
	}
	bindings := make([]walletBindingJSON, 0, len(w.MerchantBindings))
	for _, b := range w.MerchantBindings {
		bindings = append(bindings, walletBindingJSON{MerchantID: b.MerchantID.String(), MerchantName: b.MerchantName, ChainID: b.ChainID})
	}
	return walletJSON{ID: w.ID.String(), Name: w.Name, Family: string(w.Family), Kind: string(w.Kind),
		Xpub: w.Xpub, Address: w.Address, NextIndex: w.NextIndex, ChainIDs: ids, MerchantBindings: bindings,
		CreatedAt: w.CreatedAt.UTC()}
}

// merchantWalletJSON is a merchant's own receiving wallet on one chain.
type merchantWalletJSON struct {
	ChainID    string `json:"chain_id"`
	WalletID   string `json:"wallet_id"`
	WalletName string `json:"wallet_name"`
	Kind       string `json:"kind"`
}

func newMerchantWalletJSON(b store.MerchantWallet) merchantWalletJSON {
	return merchantWalletJSON{ChainID: b.ChainID, WalletID: b.WalletID.String(), WalletName: b.WalletName, Kind: string(b.WalletKind)}
}

type chainJSON struct {
	ID              string     `json:"id"`
	Family          string     `json:"family"`
	Name            string     `json:"name"`
	NetworkID       string     `json:"network_id"`
	NativeSymbol    string     `json:"native_symbol"`
	IsTestnet       bool       `json:"is_testnet"`
	Enabled         bool       `json:"enabled"`
	RPCURLs         []string   `json:"rpc_urls"`
	RPCSource       string     `json:"rpc_source"`
	Confirmations   int        `json:"confirmations"`
	PollIntervalMS  int64      `json:"poll_interval_ms"`
	ScanBatch       int        `json:"scan_batch"`
	ExplorerTxURL   string     `json:"explorer_tx_url"`
	ExplorerAddrURL string     `json:"explorer_addr_url"`
	WalletID        *string    `json:"wallet_id"`
	ScanCursor      uint64     `json:"scan_cursor"`
	LastHead        uint64     `json:"last_head"`
	Lag             uint64     `json:"lag"`
	LastScanAt      *time.Time `json:"last_scan_at"`
	LastError       string     `json:"last_error"`
	Payable         bool       `json:"payable"`
	MerchantWallets int        `json:"merchant_wallets"`
}

// newChainJSON renders a chain with its effective RPC endpoints, masked.
// payable describes the chain's default wallet only (merchant_wallets counts
// the merchants with their own wallet on it, who can be paid regardless).
func newChainJSON(c domain.Chain) chainJSON {
	urls, src := store.ResolveRPC(c)
	masked := make([]string, 0, len(urls))
	for _, u := range urls {
		masked = append(masked, maskURL(u))
	}
	source := string(src)
	if src == store.RPCSourceNone {
		// Only custom chains without any endpoint; the contract knows three
		// sources, and "default" with an empty list reads correctly.
		source = string(store.RPCSourceDefault)
	}
	out := chainJSON{
		ID: c.ID, Family: string(c.Family), Name: c.Name, NetworkID: c.NetworkID, NativeSymbol: c.NativeSymbol,
		IsTestnet: c.IsTestnet, Enabled: c.Enabled, RPCURLs: masked, RPCSource: source,
		Confirmations: c.Confirmations, PollIntervalMS: c.PollInterval.Milliseconds(), ScanBatch: c.ScanBatch,
		ExplorerTxURL: c.ExplorerTxURL, ExplorerAddrURL: c.ExplorerAddr,
		ScanCursor: c.ScanCursor, LastHead: c.LastHead, LastScanAt: utcPtr(c.LastScanAt), LastError: c.LastError,
		Payable:         c.Enabled && c.WalletID != nil && len(urls) > 0,
		MerchantWallets: c.MerchantWallets,
	}
	if c.LastHead > c.ScanCursor {
		out.Lag = c.LastHead - c.ScanCursor
	}
	if c.WalletID != nil {
		s := c.WalletID.String()
		out.WalletID = &s
	}
	return out
}

// maskURL reduces an RPC URL to scheme and host; a path, query or fragment
// (where providers put API keys) is replaced by "/…". User info is dropped.
func maskURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "…"
	}
	out := u.Scheme + "://" + u.Host
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		out += "/…"
	}
	return out
}

type assetJSON struct {
	view.AssetInfo
	QuotePrecision int     `json:"quote_precision"`
	RateKey        string  `json:"rate_key"`
	Enabled        bool    `json:"enabled"`
	SortOrder      int     `json:"sort_order"`
	RateUSD        *string `json:"rate_usd"`
}

func newAssetJSON(a domain.Asset, cat *store.Catalog, rateUSD *string) assetJSON {
	return assetJSON{AssetInfo: view.NewAssetInfo(a, cat), QuotePrecision: a.QuotePrecision, RateKey: a.RateKey,
		Enabled: a.Enabled, SortOrder: a.SortOrder, RateUSD: rateUSD}
}

type adminInvoiceJSON struct {
	view.Invoice
	MerchantName string `json:"merchant_name"`
}

func (s snapshot) adminInvoice(v store.InvoiceView) adminInvoiceJSON {
	return adminInvoiceJSON{Invoice: s.invoice(v), MerchantName: v.MerchantName}
}

type invoiceDetailJSON struct {
	view.Invoice
	MerchantName  string             `json:"merchant_name"`
	Quotes        []view.Quote       `json:"quotes"`
	Transfers     []view.Transfer    `json:"transfers"`
	WebhookEvents []webhookEventJSON `json:"webhook_events"`
}

type webhookEventJSON struct {
	ID             string          `json:"id"`
	MerchantID     string          `json:"merchant_id"`
	InvoiceID      string          `json:"invoice_id"`
	EventType      string          `json:"event_type"`
	URL            string          `json:"url"`
	Status         string          `json:"status"`
	Attempts       int             `json:"attempts"`
	NextAttemptAt  time.Time       `json:"next_attempt_at"`
	LastStatusCode int             `json:"last_status_code"`
	LastError      string          `json:"last_error"`
	LastResponse   string          `json:"last_response"`
	DeliveredAt    *time.Time      `json:"delivered_at"`
	CreatedAt      time.Time       `json:"created_at"`
	Payload        json.RawMessage `json:"payload"`
}

func newWebhookEventJSON(e domain.WebhookEvent) webhookEventJSON {
	payload := e.Payload
	if len(payload) == 0 || !json.Valid(payload) {
		payload = json.RawMessage("null")
	}
	return webhookEventJSON{ID: e.ID.String(), MerchantID: e.MerchantID.String(), InvoiceID: e.InvoiceID.String(),
		EventType: e.EventType, URL: e.URL, Status: string(e.Status), Attempts: e.Attempts,
		NextAttemptAt: e.NextAttemptAt.UTC(), LastStatusCode: e.LastStatusCode, LastError: e.LastError,
		LastResponse: e.LastResponse, DeliveredAt: utcPtr(e.DeliveredAt), CreatedAt: e.CreatedAt.UTC(), Payload: payload}
}

type auditLogJSON struct {
	ID         int64           `json:"id"`
	ActorEmail string          `json:"actor_email"`
	Action     string          `json:"action"`
	TargetType string          `json:"target_type"`
	TargetID   string          `json:"target_id"`
	Detail     json.RawMessage `json:"detail"`
	IP         string          `json:"ip"`
	CreatedAt  time.Time       `json:"created_at"`
}

func newAuditLogJSON(l domain.AuditLog) auditLogJSON {
	detail := l.Detail
	if len(detail) == 0 || !json.Valid(detail) {
		detail = json.RawMessage("{}")
	}
	return auditLogJSON{ID: l.ID, ActorEmail: l.ActorEmail, Action: l.Action, TargetType: l.TargetType,
		TargetID: l.TargetID, Detail: detail, IP: l.IP, CreatedAt: l.CreatedAt.UTC()}
}

type overviewJSON struct {
	RangeDays  int                `json:"range_days"`
	MerchantID *string            `json:"merchant_id"`
	Totals     overviewTotalsJSON `json:"totals"`
	Daily      []dailyJSON        `json:"daily"`
	ByAsset    []byAssetJSON      `json:"by_asset"`
	ByStatus   []byStatusJSON     `json:"by_status"`
}

type overviewTotalsJSON struct {
	Invoices           int     `json:"invoices"`
	PaidInvoices       int     `json:"paid_invoices"`
	OpenInvoices       int     `json:"open_invoices"`
	VolumeUSD          string  `json:"volume_usd"`
	ConversionRate     float64 `json:"conversion_rate"`
	UnmatchedTransfers int     `json:"unmatched_transfers"`
	FailedWebhooks     int     `json:"failed_webhooks"`
}

type dailyJSON struct {
	Date      string `json:"date"`
	Invoices  int    `json:"invoices"`
	Paid      int    `json:"paid"`
	VolumeUSD string `json:"volume_usd"`
}

type byAssetJSON struct {
	AssetID   string `json:"asset_id"`
	Symbol    string `json:"symbol"`
	ChainID   string `json:"chain_id"`
	ChainName string `json:"chain_name"`
	Paid      int    `json:"paid"`
	VolumeUSD string `json:"volume_usd"`
}

type byStatusJSON struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

func newOverviewJSON(o store.Overview) overviewJSON {
	out := overviewJSON{
		RangeDays: o.RangeDays,
		Totals: overviewTotalsJSON{
			Invoices: o.Totals.Invoices, PaidInvoices: o.Totals.PaidInvoices, OpenInvoices: o.Totals.OpenInvoices,
			VolumeUSD: o.Totals.VolumeUSD.String(), ConversionRate: o.Totals.ConversionRate,
			UnmatchedTransfers: o.Totals.UnmatchedTransfers, FailedWebhooks: o.Totals.FailedWebhooks,
		},
		Daily: mapSlice(o.Daily, func(d store.DailyStat) dailyJSON {
			return dailyJSON{Date: d.Date, Invoices: d.Invoices, Paid: d.Paid, VolumeUSD: d.VolumeUSD.String()}
		}),
		ByAsset: mapSlice(o.ByAsset, func(a store.AssetStat) byAssetJSON {
			return byAssetJSON{AssetID: a.AssetID, Symbol: a.Symbol, ChainID: a.ChainID, ChainName: a.ChainName,
				Paid: a.Paid, VolumeUSD: a.VolumeUSD.String()}
		}),
		ByStatus: mapSlice(o.ByStatus, func(s store.StatusCount) byStatusJSON {
			return byStatusJSON{Status: string(s.Status), Count: s.Count}
		}),
	}
	if o.MerchantID != nil {
		id := o.MerchantID.String()
		out.MerchantID = &id
	}
	return out
}

// merchantOverviewJSON is `MerchantOverview` (GET /overview/merchants).
type merchantOverviewJSON struct {
	MerchantID     string     `json:"merchant_id"`
	MerchantName   string     `json:"merchant_name"`
	Status         string     `json:"status"`
	Invoices       int        `json:"invoices"`
	PaidInvoices   int        `json:"paid_invoices"`
	VolumeUSD      string     `json:"volume_usd"`
	ConversionRate float64    `json:"conversion_rate"`
	OpenInvoices   int        `json:"open_invoices"`
	FailedWebhooks int        `json:"failed_webhooks"`
	LastInvoiceAt  *time.Time `json:"last_invoice_at"`
	LastPaidAt     *time.Time `json:"last_paid_at"`
}

func newMerchantOverviewJSON(m store.MerchantOverview) merchantOverviewJSON {
	return merchantOverviewJSON{MerchantID: m.MerchantID.String(), MerchantName: m.MerchantName, Status: string(m.Status),
		Invoices: m.Invoices, PaidInvoices: m.PaidInvoices, VolumeUSD: m.VolumeUSD.String(), ConversionRate: m.ConversionRate,
		OpenInvoices: m.OpenInvoices, FailedWebhooks: m.FailedWebhooks,
		LastInvoiceAt: utcPtr(m.LastInvoiceAt), LastPaidAt: utcPtr(m.LastPaidAt)}
}

// merchantOverviewPageJSON is `Page<MerchantOverview> & { range_days: number }`.
type merchantOverviewPageJSON struct {
	pageJSON[merchantOverviewJSON]
	RangeDays int `json:"range_days"`
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
