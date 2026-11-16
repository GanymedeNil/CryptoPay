package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet"
	"cryptopay/internal/scanner"
	"cryptopay/internal/store"
)

// ---- chains ----

const (
	maxRPCURLs           = 10
	maxConfirmations     = 1000
	minPollIntervalMS    = 500
	maxPollIntervalMS    = 3_600_000
	maxScanBatch         = 100_000
	rateLookupTimeout    = 3 * time.Second
	previewAddressNumber = 3
)

func (a *API) adminListChains(w http.ResponseWriter, r *http.Request) {
	chains, err := a.store.ListChains(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsJSON[chainJSON]{Items: mapSlice(chains, newChainJSON)})
}

// checkRPCURL validates one RPC endpoint: an absolute http(s) URL with a host
// (every adapter speaks HTTP JSON-RPC / REST).
func checkRPCURL(raw string) (string, string) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", "must not be empty"
	}
	if len(u) > maxURLLen {
		return "", "is too long"
	}
	if strings.ContainsAny(u, " \t\r\n") {
		return "", "must not contain whitespace"
	}
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return "", "must be an absolute http or https URL"
	}
	if err := store.ValidateRPCURL(u); err != nil {
		return "", "is not a valid RPC URL"
	}
	return u, ""
}

func (a *API) adminUpdateChain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var body struct {
		Enabled        *bool            `json:"enabled"`
		RPCURLs        *[]string        `json:"rpc_urls"`
		Confirmations  *int             `json:"confirmations"`
		PollIntervalMS *int             `json:"poll_interval_ms"`
		ScanBatch      *int             `json:"scan_batch"`
		WalletID       optional[string] `json:"wallet_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	c, err := a.store.GetChain(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	var upd store.ChainUpdate
	detail := map[string]any{}
	if body.Enabled != nil {
		upd.Enabled, detail["enabled"] = body.Enabled, *body.Enabled
	}
	if body.RPCURLs != nil {
		if len(*body.RPCURLs) > maxRPCURLs {
			f.add("rpc_urls", fmt.Sprintf("at most %d endpoints", maxRPCURLs))
		}
		urls := make([]string, 0, len(*body.RPCURLs))
		masked := make([]string, 0, len(*body.RPCURLs))
		seen := map[string]bool{}
		for i, raw := range *body.RPCURLs {
			u, msg := checkRPCURL(raw)
			if msg != "" {
				f.add(fmt.Sprintf("rpc_urls.%d", i), msg)
				f.add("rpc_urls", "contains an invalid URL")
				continue
			}
			if !seen[u] {
				seen[u] = true
				urls = append(urls, u)
				masked = append(masked, maskURL(u))
			}
		}
		upd.RPCURLs, detail["rpc_urls"] = &urls, masked
	}
	if body.Confirmations != nil {
		if n := *body.Confirmations; n < 1 || n > maxConfirmations {
			f.add("confirmations", fmt.Sprintf("must be between 1 and %d", maxConfirmations))
		}
		upd.Confirmations, detail["confirmations"] = body.Confirmations, *body.Confirmations
	}
	if body.PollIntervalMS != nil {
		ms := *body.PollIntervalMS
		if ms < minPollIntervalMS || ms > maxPollIntervalMS {
			f.add("poll_interval_ms", fmt.Sprintf("must be between %d and %d", minPollIntervalMS, maxPollIntervalMS))
		}
		d := time.Duration(ms) * time.Millisecond
		upd.PollInterval, detail["poll_interval_ms"] = &d, ms
	}
	if body.ScanBatch != nil {
		if n := *body.ScanBatch; n < 1 || n > maxScanBatch {
			f.add("scan_batch", fmt.Sprintf("must be between 1 and %d", maxScanBatch))
		}
		upd.ScanBatch, detail["scan_batch"] = body.ScanBatch, *body.ScanBatch
	}
	if body.WalletID.Set {
		upd.SetWallet = true
		if body.WalletID.Null || strings.TrimSpace(body.WalletID.Value) == "" {
			detail["wallet_id"] = nil
		} else if wid, err := uuid.Parse(strings.TrimSpace(body.WalletID.Value)); err != nil {
			f.add("wallet_id", "must be a wallet id or null")
		} else if msg := a.checkWalletForChain(ctx, c, wid); msg != "" {
			f.add("wallet_id", msg)
		} else {
			upd.WalletID, detail["wallet_id"] = &wid, wid.String()
		}
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var out domain.Chain
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if out, err = tx.UpdateChain(ctx, id, upd); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "chain.update", TargetType: "chain", TargetID: id, Detail: detail})
	})
	if err != nil {
		if errors.Is(err, store.ErrInvalid) || errors.Is(err, store.ErrForeignKey) {
			err = errInvalid("the chain configuration was rejected", nil)
		}
		a.fail(w, r, err)
		return
	}
	if a.registry != nil && (upd.RPCURLs != nil || upd.Enabled != nil || upd.SetWallet) {
		a.registry.Invalidate(id)
	}
	if a.hooks.ChainChanged != nil {
		a.hooks.ChainChanged(id)
	}
	writeJSON(w, http.StatusOK, newChainJSON(out))
}

// checkWalletForChain returns why wallet wid cannot be assigned to chain c
// ("" when it can).
func (a *API) checkWalletForChain(ctx context.Context, c domain.Chain, wid uuid.UUID) string {
	wal, err := a.store.GetWallet(ctx, wid)
	if errors.Is(err, store.ErrNotFound) {
		return "unknown wallet"
	}
	if err != nil {
		return "could not load the wallet"
	}
	if wal.Family != c.Family {
		return fmt.Sprintf("the wallet is a %s wallet, the chain needs a %s wallet", wal.Family, c.Family)
	}
	if wal.Kind == domain.WalletStatic {
		if _, err := address.Normalize(c, wal.Address); err != nil {
			return "the wallet's address is not valid on " + c.Name
		}
	}
	return ""
}

func (a *API) adminTestRPC(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var body struct {
		RPCURL *string `json:"rpc_url"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	override := ""
	if body.RPCURL != nil && strings.TrimSpace(*body.RPCURL) != "" {
		u, msg := checkRPCURL(*body.RPCURL)
		if msg != "" {
			a.fail(w, r, errField("rpc_url", msg))
			return
		}
		override = u
	}
	c, err := a.store.GetChain(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if a.registry == nil {
		a.fail(w, r, &Error{Status: http.StatusServiceUnavailable, Code: CodeUnavailable, Message: "no chain registry configured"})
		return
	}
	head, latency, perr := a.registry.Probe(ctx, c, override)
	res := struct {
		OK        bool   `json:"ok"`
		Head      uint64 `json:"head"`
		LatencyMS int64  `json:"latency_ms"`
		Error     string `json:"error"`
	}{OK: perr == nil, Head: head, LatencyMS: latency.Milliseconds()}
	if perr != nil {
		res.Error = scanner.SanitizeError(perr)
	}
	target := "configured endpoints"
	if override != "" {
		target = maskURL(override)
	}
	a.auditAfter(ctx, auditEntry{Action: "chain.test_rpc", TargetType: "chain", TargetID: id,
		Detail: map[string]any{"target": target, "ok": res.OK}})
	writeJSON(w, http.StatusOK, res)
}

// ---- assets ----

func (a *API) adminListAssets(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	assets, err := a.store.ListAssets(ctx, store.AssetFilter{})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	usd := a.usdRates(ctx, assets)
	writeJSON(w, http.StatusOK, itemsJSON[assetJSON]{Items: mapSlice(assets, func(x domain.Asset) assetJSON {
		return newAssetJSON(x, cat, usd[x.RateKey])
	})})
}

// usdRates looks up the USD price of every distinct rate key, best effort
// (concurrently, bounded by rateLookupTimeout); missing prices are absent.
func (a *API) usdRates(ctx context.Context, assets []domain.Asset) map[string]*string {
	out := map[string]*string{}
	if a.rates == nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, rateLookupTimeout)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	seen := map[string]bool{}
	for _, as := range assets {
		key := as.RateKey
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		wg.Go(func() {
			rate, err := a.rates.Rate(ctx, key, "USD")
			if err != nil || rate.Sign() <= 0 {
				return
			}
			s := rate.String()
			mu.Lock()
			out[key] = &s
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

var (
	symbolRE  = regexp.MustCompile(`^[A-Za-z0-9.]{1,16}$`)
	rateKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,99}$`)
)

func (a *API) adminCreateAsset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		ChainID         string `json:"chain_id"`
		Symbol          string `json:"symbol"`
		Name            string `json:"name"`
		ContractAddress string `json:"contract_address"`
		Decimals        *int   `json:"decimals"`
		QuotePrecision  *int   `json:"quote_precision"`
		RateKey         string `json:"rate_key"`
		IsStablecoin    bool   `json:"is_stablecoin"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	var c domain.Chain
	chainID := strings.TrimSpace(body.ChainID)
	if chainID == "" {
		f.add("chain_id", "is required")
	} else if ch, err := a.store.GetChain(ctx, chainID); errors.Is(err, store.ErrNotFound) {
		f.add("chain_id", "unknown chain")
	} else if err != nil {
		a.fail(w, r, err)
		return
	} else {
		c = ch
	}
	symbol := strings.TrimSpace(body.Symbol)
	if !symbolRE.MatchString(symbol) {
		f.add("symbol", "must be 1-16 letters, digits or dots")
	}
	name := checkName(f, "name", body.Name, 100)
	contract := strings.TrimSpace(body.ContractAddress)
	if contract == "" {
		f.add("contract_address", "is required (the native coin of every chain is built in)")
	} else if c.ID != "" {
		norm, err := address.Normalize(c, contract)
		if err != nil {
			f.add("contract_address", "is not a valid address on "+c.Name)
		}
		contract = norm
	}
	decimals := 0
	if body.Decimals == nil {
		f.add("decimals", "is required")
	} else if decimals = *body.Decimals; decimals < 0 || decimals > 36 {
		f.add("decimals", "must be between 0 and 36")
	}
	precision := 0
	if body.QuotePrecision == nil {
		f.add("quote_precision", "is required")
	} else if precision = *body.QuotePrecision; precision < 0 || precision > decimals {
		f.add("quote_precision", "must be between 0 and decimals")
	}
	rateKey := strings.TrimSpace(body.RateKey)
	if !rateKeyRE.MatchString(rateKey) {
		f.add("rate_key", "must be a CoinGecko coin id such as \"tether\"")
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	in := domain.Asset{
		ID: store.AssetID(symbol, c.ID), ChainID: c.ID, Symbol: symbol, Name: name,
		ContractAddress: contract, Decimals: decimals, QuotePrecision: precision, RateKey: rateKey,
		IsStablecoin: body.IsStablecoin, Enabled: true, SortOrder: 100,
	}
	var out domain.Asset
	err := a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if out, err = tx.CreateAsset(ctx, in); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "asset.create", TargetType: "asset", TargetID: out.ID,
			Detail: map[string]any{"chain_id": out.ChainID, "symbol": out.Symbol, "contract_address": out.ContractAddress,
				"decimals": out.Decimals, "quote_precision": out.QuotePrecision, "rate_key": out.RateKey}})
	})
	switch {
	case store.IsConstraint(err, store.ConstraintAssetPK):
		a.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict,
			Message: "an asset with id " + in.ID + " already exists", Fields: map[string]string{"symbol": "already used on this chain"}})
		return
	case store.IsConstraint(err, store.ConstraintAssetChainContract):
		a.fail(w, r, &Error{Status: http.StatusConflict, Code: CodeConflict,
			Message: "this token is already configured", Fields: map[string]string{"contract_address": "already configured on this chain"}})
		return
	case err != nil:
		a.fail(w, r, err)
		return
	}
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	usd := a.usdRates(ctx, []domain.Asset{out})
	writeJSON(w, http.StatusCreated, newAssetJSON(out, cat, usd[out.RateKey]))
}

func (a *API) adminUpdateAsset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	var body struct {
		Enabled        *bool   `json:"enabled"`
		QuotePrecision *int    `json:"quote_precision"`
		SortOrder      *int    `json:"sort_order"`
		RateKey        *string `json:"rate_key"`
		Name           *string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	cur, err := a.store.GetAsset(ctx, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	upd := store.AssetUpdate{Enabled: body.Enabled, QuotePrecision: body.QuotePrecision, SortOrder: body.SortOrder}
	detail := map[string]any{}
	if body.Enabled != nil {
		detail["enabled"] = *body.Enabled
	}
	if body.QuotePrecision != nil {
		if p := *body.QuotePrecision; p < 0 || p > cur.Decimals {
			f.add("quote_precision", fmt.Sprintf("must be between 0 and %d (the asset's decimals)", cur.Decimals))
		}
		detail["quote_precision"] = *body.QuotePrecision
	}
	if body.SortOrder != nil {
		if s := *body.SortOrder; s < -1_000_000 || s > 1_000_000 {
			f.add("sort_order", "is out of range")
		}
		detail["sort_order"] = *body.SortOrder
	}
	if body.RateKey != nil {
		k := strings.TrimSpace(*body.RateKey)
		if !rateKeyRE.MatchString(k) {
			f.add("rate_key", "must be a CoinGecko coin id such as \"tether\"")
		}
		upd.RateKey, detail["rate_key"] = &k, k
	}
	if body.Name != nil {
		n := checkName(f, "name", *body.Name, 100)
		upd.Name, detail["name"] = &n, n
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var out domain.Asset
	err = a.inTx(ctx, func(tx *store.Store) error {
		var err error
		if out, err = tx.UpdateAsset(ctx, id, upd); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "asset.update", TargetType: "asset", TargetID: id, Detail: detail})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	cat, err := a.store.LoadCatalog(ctx)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	usd := a.usdRates(ctx, []domain.Asset{out})
	writeJSON(w, http.StatusOK, newAssetJSON(out, cat, usd[out.RateKey]))
}

// ---- wallets ----

func (a *API) adminListWallets(w http.ResponseWriter, r *http.Request) {
	ws, err := a.store.ListWallets(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, itemsJSON[walletJSON]{Items: mapSlice(ws, newWalletJSON)})
}

// walletInput is the key material of a wallet request.
type walletInput struct {
	Family  string `json:"family"`
	Kind    string `json:"kind"`
	Xpub    string `json:"xpub"`
	Address string `json:"address"`
}

// check validates family / kind / key material (not the address, which
// needs a chain). It never echoes the key in a message.
func (in walletInput) check(f fields) (domain.Family, domain.WalletKind, string) {
	family := domain.Family(strings.TrimSpace(in.Family))
	kind := domain.WalletKind(strings.TrimSpace(in.Kind))
	xpub := strings.TrimSpace(in.Xpub)
	if !family.Valid() {
		f.add("family", "must be one of evm, tron, utxo, solana")
	}
	switch kind {
	case domain.WalletXpub:
		if family == domain.FamilySolana {
			f.add("kind", "Solana has no public key derivation: use a static wallet (one fixed address)")
			break
		}
		if xpub == "" {
			f.add("xpub", "is required for xpub wallets")
		} else if family.Valid() {
			if err := hdwallet.Validate(family, xpub); err != nil {
				if errors.Is(err, hdwallet.ErrPrivateKey) {
					f.add("xpub", strings.TrimPrefix(hdwallet.ErrPrivateKey.Error(), "hdwallet: "))
				} else {
					f.add("xpub", "is not a valid account-level extended public key (xpub/zpub/tpub/vpub)")
				}
			}
		}
		if strings.TrimSpace(in.Address) != "" {
			f.add("address", "must be empty for xpub wallets")
		}
	case domain.WalletStatic:
		if strings.TrimSpace(in.Address) == "" {
			f.add("address", "is required for static wallets")
		}
		if xpub != "" {
			f.add("xpub", "must be empty for static wallets")
		}
	default:
		f.add("kind", "must be xpub or static")
	}
	return family, kind, xpub
}

func (a *API) adminPreviewWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		walletInput
		ChainID string `json:"chain_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	family, kind, xpub := body.check(f)
	var c domain.Chain
	if id := strings.TrimSpace(body.ChainID); id == "" {
		f.add("chain_id", "is required")
	} else if ch, err := a.store.GetChain(ctx, id); errors.Is(err, store.ErrNotFound) {
		f.add("chain_id", "unknown chain")
	} else if err != nil {
		a.fail(w, r, err)
		return
	} else if family.Valid() && ch.Family != family {
		f.add("chain_id", fmt.Sprintf("is a %s chain, not %s", ch.Family, family))
	} else {
		c = ch
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	addrs := []string{}
	if kind == domain.WalletXpub {
		for i := range uint32(previewAddressNumber) {
			addr, err := hdwallet.Derive(c, xpub, i)
			if err != nil {
				a.fail(w, r, errField("xpub", "cannot derive addresses for "+c.Name))
				return
			}
			addrs = append(addrs, addr)
		}
	} else {
		addr, err := address.Normalize(c, strings.TrimSpace(body.Address))
		if err != nil {
			a.fail(w, r, errField("address", "is not a valid address on "+c.Name))
			return
		}
		addrs = append(addrs, addr)
	}
	writeJSON(w, http.StatusOK, struct {
		Addresses []string `json:"addresses"`
	}{addrs})
}

func (a *API) adminCreateWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		walletInput
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	name := checkName(f, "name", body.Name, 100)
	family, kind, xpub := body.check(f)
	addr := ""
	if f.err() == nil && kind == domain.WalletStatic {
		norm, err := a.normalizeStatic(ctx, family, strings.TrimSpace(body.Address))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if norm == "" {
			f.add("address", "is not a valid "+string(family)+" address")
		}
		addr = norm
	}
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var out store.WalletView
	err := a.inTx(ctx, func(tx *store.Store) error {
		wal, err := tx.CreateWallet(ctx, domain.Wallet{Name: name, Family: family, Kind: kind, Xpub: xpub, Address: addr})
		if err != nil {
			return err
		}
		out = store.WalletView{Wallet: wal, ChainIDs: []string{}}
		detail := map[string]any{"name": name, "family": string(family), "kind": string(kind)}
		if addr != "" {
			detail["address"] = addr
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "wallet.create", TargetType: "wallet", TargetID: wal.ID.String(), Detail: detail})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, newWalletJSON(out))
}

// normalizeStatic returns the canonical form of a static address valid on
// at least one chain of the family ("" when none accepts it).
func (a *API) normalizeStatic(ctx context.Context, family domain.Family, addr string) (string, error) {
	chains, err := a.store.ListChains(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range chains {
		if c.Family != family {
			continue
		}
		if norm, err := address.Normalize(c, addr); err == nil {
			return norm, nil
		}
	}
	return "", nil
}

func (a *API) adminUpdateWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	f := fields{}
	name := checkName(f, "name", body.Name, 100)
	if err := f.err(); err != nil {
		a.fail(w, r, err)
		return
	}
	var out store.WalletView
	err = a.inTx(ctx, func(tx *store.Store) error {
		if _, err := tx.RenameWallet(ctx, id, name); err != nil {
			return err
		}
		var err error
		if out, err = tx.GetWalletView(ctx, id); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "wallet.update", TargetType: "wallet", TargetID: id.String(),
			Detail: map[string]any{"name": name}})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, newWalletJSON(out))
}

func (a *API) adminDeleteWallet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathUUID(r, "id")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var chainIDs []string
	err = a.inTx(ctx, func(tx *store.Store) error {
		wv, err := tx.GetWalletView(ctx, id)
		if err != nil {
			return err
		}
		chainIDs = wv.ChainIDs
		if err := tx.DeleteWallet(ctx, id); err != nil {
			return err
		}
		return a.audit(ctx, tx, nil, auditEntry{Action: "wallet.delete", TargetType: "wallet", TargetID: id.String(),
			Detail: map[string]any{"name": wv.Name, "family": string(wv.Family), "kind": string(wv.Kind), "unassigned_chains": wv.ChainIDs}})
	})
	if errors.Is(err, store.ErrInUse) {
		a.fail(w, r, errConflict("the wallet is referenced by quotes and cannot be deleted"))
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	for _, c := range chainIDs {
		if a.hooks.ChainChanged != nil {
			a.hooks.ChainChanged(c)
		}
	}
	noContent(w)
}
