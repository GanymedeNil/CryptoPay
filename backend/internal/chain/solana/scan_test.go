package solana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"cryptopay/internal/chain"
)

// Transactions recorded from devnet / mainnet-beta (see testdata/).
const (
	fxSOLTransfer   = "tx_sol_transfer_legacy.json"      // devnet: 100 lamports → 9B5X… (system transfer)
	fxSPLExisting   = "tx_spl_existing_ata_v0.json"      // devnet v0: 1000 USDC-dev → existing ATA of 7Zvo… (owner not in tx)
	fxSPLCreateATA  = "tx_spl_create_ata_legacy.json"    // devnet: createIdempotent ATA + transferChecked 5 USDC-dev → 7kL8…
	fxSOLAndSPL     = "tx_sol_and_spl_legacy.json"       // devnet: SOL + USDC-dev to 49AC…, sender closes its token account
	fxSOLAndSPLCPI  = "tx_sol_and_spl_inner_legacy.json" // devnet: program (CPI) pays SOL + USDC-dev to 2y1y… (new ATA)
	fxMultiV1       = "tx_multi_recipient_v1.json"       // devnet v1: one tx pays USDC-dev to 4 owners
	fxALTSPL        = "tx_alt_spl_v0.json"               // mainnet v0 swap with 4 address lookup tables
	fxALTNative     = "tx_alt_native_v0.json"            // mainnet v0 swap, lamports credited to lookup-table accounts
	fxFailedV1      = "tx_failed_v1.json"                // mainnet v1, meta.err set
	devnetFeePayer  = "CNH3eKGGKVTP8PiZyZdgc4Pc9jshFzr3bR1u1RtCCmEK"
	devnetSOLRecv   = "9B5XszUGdMaxCZ7uSQhPzdks5ZQSmWxrmzCSvtJ6Ns6g"
	skrMint         = "SKRbvo6Gf7GondiT3BbTfuRDPqLWei4j2Qy2NPGZhW3"
	pumpMint        = "3EMheh5MPVWpAGDdXnGBgs6uaukKL29zv65uuXxspump"
	solAndSPLOwner  = "49ACsG4JNz13vZpJhzGKqNZptofvD8gwXSx8xvDJGRej"
	solAndSPLSender = "25RdjJQUooi7R1jA3u2WZx9SXWJCnJmbBT7Y3XRtHcpb"
)

func tr(sig string, slot uint64, bt int64, idx int, from, to, token string, amount int64) chain.Transfer {
	return chain.Transfer{
		TxHash: sig, Index: idx, BlockNumber: slot, BlockTime: time.Unix(bt, 0).UTC(),
		From: from, To: to, Token: token, Amount: big.NewInt(amount),
	}
}

func scan(t *testing.T, c *Client, from, to uint64, native bool, tokens []string, addrs ...string) []chain.Transfer {
	t.Helper()
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: from, ToBlock: to, Addresses: addrs, Tokens: tokens, Native: native,
	})
	require.NoError(t, err)
	return got
}

func TestHead(t *testing.T) {
	f := newFakeNode(t)
	f.setHead(470031846)
	got, err := f.client(t).Head(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint64(470031846), got)
}

func TestScanNativeSOLTransfer(t *testing.T) {
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSOLTransfer))
	c := f.client(t)

	got := scan(t, c, fx.Slot, fx.Slot, true, []string{usdcDevnet}, devnetSOLRecv)
	// The receiver's balance (10154631887802677) exceeds 2^53: a float
	// based delta would not be exactly 100.
	assert.Equal(t, []chain.Transfer{tr(fx.Sig, fx.Slot, fx.BlockTime, 1, devnetFeePayer, devnetSOLRecv, "", 100)}, got)

	// The sender's view: outgoing value is not an incoming transfer.
	assert.Empty(t, scan(t, c, fx.Slot, fx.Slot, true, nil, devnetFeePayer))

	// Native not requested: nothing.
	assert.Empty(t, scan(t, c, fx.Slot, fx.Slot, false, []string{usdcDevnet}, devnetSOLRecv))
}

func TestScanSPLToExistingATA(t *testing.T) {
	const owner = "7ZvoVfPEz5q4jYEQWYreeSusKKGhR6ximpt43TpuVTav"
	const ata = "2Jm6mt9Qjn2y8xB8QexAGVDL874g2pUWp2MfHoZvEDi8"
	const authority = "EnpqMsgfNnhFVCqfbAbE3Xd7AQ7bgmYEaNgMWg8oaFfL" // not the fee payer

	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSPLExisting))
	require.NotContains(t, fx.Keys, owner, "fixture: owner is not referenced by the tx")

	// Without the token account lookup the payment is invisible: the tx only
	// references the token account.
	f2 := newFakeNode(t)
	f2.addFixture(fx)
	assert.Empty(t, scan(t, f2.client(t), fx.Slot, fx.Slot, true, []string{usdcDevnet}, owner))

	f.addTokenAccount(owner, tokenAcct{Pubkey: ata, Mint: usdcDevnet, Amount: "272195000"})
	got := scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{usdcDevnet}, owner)
	assert.Equal(t, []chain.Transfer{tr(fx.Sig, fx.Slot, fx.BlockTime, 2, authority, owner, usdcDevnet, 1000)}, got)
}

func TestScanSPLWithATACreatedInSameTx(t *testing.T) {
	const owner = "7kL87prFVvm36nwLLwpDQF2J2w7ZURYjGoewwsZvv6hq"
	const ata = "Ha51uJ8ePZQnsCdWHCZbQThN2t8vFzJJFxF7azqp8GXj"
	const payer = "6op6kHdEuXvTg3qnrWag72wYFCbZgKjT9xqq8kQLQgdA"

	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSPLCreateATA))
	want := []chain.Transfer{tr(fx.Sig, fx.Slot, fx.BlockTime, 2, payer, owner, usdcDevnet, 5_000_000)}

	// The ATA is unknown to getTokenAccountsByOwner (e.g. closed since): the
	// creating tx references the owner, so the owner's signatures cover it.
	// The ATA's rent deposit is not a credit to the owner.
	assert.Equal(t, want, scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{usdcDevnet}, owner))

	// With the ATA known the signature shows up twice and is fetched once.
	f.addTokenAccount(owner, tokenAcct{Pubkey: ata, Mint: usdcDevnet, Amount: "5000000"})
	before := f.txCount(fx.Sig)
	assert.Equal(t, want, scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{usdcDevnet}, owner))
	assert.Equal(t, 1, f.txCount(fx.Sig)-before, "signature de-duplicated across owner and token account")
}

func TestScanSOLAndSPLInOneTx(t *testing.T) {
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSOLAndSPL))
	f.addTokenAccount(solAndSPLOwner, tokenAcct{Pubkey: "7JcceuTy5LyvPRqBrRMSy7AxU9roVStjAVdh4bcvAqfH", Mint: usdcDevnet, Amount: "46891968"})
	c := f.client(t)

	native := tr(fx.Sig, fx.Slot, fx.BlockTime, 1, solAndSPLSender, solAndSPLOwner, "", 29_116_440)
	spl := tr(fx.Sig, fx.Slot, fx.BlockTime, 2, solAndSPLSender, solAndSPLOwner, usdcDevnet, 999_577)
	assert.Equal(t, []chain.Transfer{native, spl}, scan(t, c, fx.Slot, fx.Slot, true, []string{usdcDevnet}, solAndSPLOwner))
	assert.Equal(t, []chain.Transfer{spl}, scan(t, c, fx.Slot, fx.Slot, false, []string{usdcDevnet}, solAndSPLOwner))
	assert.Equal(t, []chain.Transfer{native}, scan(t, c, fx.Slot, fx.Slot, true, nil, solAndSPLOwner))

	// The sender loses SOL and closes its token account (pre balance only).
	assert.Empty(t, scan(t, c, fx.Slot, fx.Slot, true, []string{usdcDevnet}, solAndSPLSender))
}

func TestScanSOLAndSPLViaInnerInstructions(t *testing.T) {
	const owner = "2y1yzRannWLf1s4dDWjNM9zWDcreS3kDA3oxs9a14z1C"
	const sender = "GaKNCd7z87ShUXnJj6RBfkRB9aUm1L7DhmpYyFhjr22Q"
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSOLAndSPLCPI))
	got := scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{usdcDevnet}, owner)
	assert.Equal(t, []chain.Transfer{
		tr(fx.Sig, fx.Slot, fx.BlockTime, 1, sender, owner, "", 2_240_280),
		tr(fx.Sig, fx.Slot, fx.BlockTime, 3, sender, owner, usdcDevnet, 100_000), // ATA created by the program: no pre balance
	}, got)
}

func TestScanFailedTransactionsIgnored(t *testing.T) {
	const watched = "Ep6QHn9uArG8p84sVdQpYzaVA23APUtqQHYVoa6hbt4J"
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxFailedV1))
	require.NotNil(t, fx.Err)
	c := f.client(t)

	assert.Empty(t, scan(t, c, fx.Slot, fx.Slot, true, []string{usdcMainnet}, watched))
	assert.Zero(t, f.txCount(fx.Sig), "failed signatures are dropped from the signature list")

	// A signature listed as successful whose transaction turns out failed
	// (meta.err) is still ignored.
	sig := fakeSig("failed-but-listed-ok")
	f.putTx(sig, rewriteFixture(t, fx.Raw, sig, 0, nil), 1)
	f.addSig(watched, sigEntry{Signature: sig, Slot: fx.Slot, BlockTime: fx.BlockTime})
	assert.Empty(t, scan(t, c, fx.Slot, fx.Slot, true, []string{usdcMainnet}, watched))
	assert.Equal(t, 1, f.txCount(sig))
}

func TestScanUnrelatedMintIgnored(t *testing.T) {
	const owner = "GDDocBd8tK66JPk3mFWj9MZfzJ8X2NuuLexL996aFDix" // fee payer: sells SKR, buys USDC
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxALTSPL))
	f.addTokenAccount(owner, tokenAcct{Pubkey: "AkdMAEyCdaJzrjtxWQkp1uUyHw6UFfTbxJWsEJFeQB57", Mint: usdcMainnet, Amount: "179984"})
	c := f.client(t)

	// USDT is not involved; SKR left the wallet; SOL only paid fees.
	assert.Empty(t, scan(t, c, fx.Slot, fx.Slot, true, []string{usdtMainnet, skrMint}, owner))

	got := scan(t, c, fx.Slot, fx.Slot, true, []string{usdtMainnet, usdcMainnet}, owner)
	assert.Equal(t, []chain.Transfer{
		tr(fx.Sig, fx.Slot, fx.BlockTime, 2, "8HwsY8kyGdKJihAJj4NEH1awqGzqt9rkxm4tGADouAGY", owner, usdcMainnet, 117),
	}, got)
}

func TestScanV0AddressLookupTables(t *testing.T) {
	t.Run("spl credit to lookup-table account", func(t *testing.T) {
		const owner = "6n7XP2mQPQ7YawQRP7TVUx6abntWsDF8Z24Lb5iCBX9L" // key 25, loaded from a lookup table
		f := newFakeNode(t)
		fx := f.addFixture(loadFixture(t, fxALTSPL))
		want := []chain.Transfer{tr(fx.Sig, fx.Slot, fx.BlockTime, 26, "59ztpLPKVKjifh9jRaU5wGCnv6jeAZ1oWS9S82ANPJLG", owner, pumpMint, 5_031_184)}
		assert.Equal(t, want, scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{pumpMint}, owner))

		// Same tx in the non-parsed layout: accountKeys hold only the static
		// keys and meta.loadedAddresses the lookup-table ones.
		g := newFakeNode(t)
		raw := rewriteFixture(t, fx.Raw, "", 0, func(m map[string]any) {
			msg := m["transaction"].(map[string]any)["message"].(map[string]any)
			var static []any
			writable, readonly := []any{}, []any{}
			sawReadonly := false
			for _, k := range msg["accountKeys"].([]any) {
				km := k.(map[string]any)
				if km["source"] != "lookupTable" {
					static = append(static, km["pubkey"])
					continue
				}
				if km["writable"].(bool) {
					require.False(t, sawReadonly, "lookup-table writable keys precede readonly ones")
					writable = append(writable, km["pubkey"])
				} else {
					sawReadonly = true
					readonly = append(readonly, km["pubkey"])
				}
			}
			msg["accountKeys"] = static
			m["meta"].(map[string]any)["loadedAddresses"] = map[string]any{"writable": writable, "readonly": readonly}
		})
		g.addFixture(fixture{Sig: fx.Sig, Slot: fx.Slot, BlockTime: fx.BlockTime, Keys: fx.Keys, Raw: raw})
		assert.Equal(t, want, scan(t, g.client(t), fx.Slot, fx.Slot, true, []string{pumpMint}, owner))
	})

	t.Run("native credit to lookup-table accounts", func(t *testing.T) {
		const selfOwned = "4xZYFLR2eyo2vrfsGuJZEPsgZafamE9fFPJ5wYJDW6nU" // key 37: a wSOL account owning itself
		const other = "bHHnvxhkzBebvqxpnzVaXSdQ1GdFeZFg8yi9YKgL7zE"      // key 28
		const signer = "337z5eEbVf7ZWS4Zx1gJQszh7KZcrDUtjB9mNy1dHwUd"
		f := newFakeNode(t)
		fx := f.addFixture(loadFixture(t, fxALTNative))
		got := scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{wsolMint}, selfOwned, other)
		assert.Equal(t, []chain.Transfer{
			tr(fx.Sig, fx.Slot, fx.BlockTime, 28, signer, other, "", 4_100_000),
			tr(fx.Sig, fx.Slot, fx.BlockTime, 37, signer, selfOwned, "", 900_000),
			// The same account's token credit must not collide with its lamport credit.
			tr(fx.Sig, fx.Slot, fx.BlockTime, SelfOwnedIndexOffset+37, signer, selfOwned, wsolMint, 900_000),
		}, got)
	})
}

func TestScanMultipleRecipientsInV1Tx(t *testing.T) {
	const a = "9BM5b6j2G66n9JmeZxAu2gFrUW4NBMXHJyWTwsnMNZgQ"
	const b = "DC5EZwf84sdjHTqqM15Qzhg1Rff9HL2QeEtzsisfSMyZ"
	const sender = "BvTTJStPnzX9t6wwwTmk2dMyBT5D3sUyBZhUR5MLxzkL"
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxMultiV1))
	f.addTokenAccount(a, tokenAcct{Pubkey: "7VKZxcAz13vJRVj6Uryjbju6sJYUy6n1rgoLBdcf7WXV", Mint: usdcDevnet, Amount: "1"})
	f.addTokenAccount(b, tokenAcct{Pubkey: "G65PPZbprPvs3ojK7RHWtstZCmn8VW54iJhE2iNJqsZy", Mint: usdcDevnet, Amount: "1"})

	got := scan(t, f.client(t), fx.Slot, fx.Slot, true, []string{usdcDevnet}, b, a, b) // duplicates are fine
	assert.Equal(t, []chain.Transfer{
		tr(fx.Sig, fx.Slot, fx.BlockTime, 1, sender, a, usdcDevnet, 6),
		tr(fx.Sig, fx.Slot, fx.BlockTime, 4, sender, b, usdcDevnet, 13),
	}, got)
	assert.Equal(t, 1, f.txCount(fx.Sig), "one fetch for a tx touching four watched accounts")
}

func TestScanPaginationAndSlotRange(t *testing.T) {
	const from, to = 1000, 2000
	f := newFakeNode(t)
	tmpl := loadFixture(t, fxSOLTransfer)
	addTx := func(seed string, slot uint64, failed bool) string {
		sig := fakeSig(seed)
		e := sigEntry{Signature: sig, Slot: slot, BlockTime: tmpl.BlockTime}
		if failed {
			e.Err = json.RawMessage(`{"InstructionError":[0,{"Custom":1}]}`)
		}
		f.putTx(sig, rewriteFixture(t, tmpl.Raw, sig, slot, nil), -1)
		f.addSig(devnetSOLRecv, e)
		return sig
	}
	// Newest first: 994 above the range, ToBlock+1, then 6 in the range
	// (straddling the first page boundary, one failed), FromBlock-1, then
	// 1500 older ones.
	for i := range 994 {
		addTx(fmt.Sprint("above-", i), to+10+uint64(i), false)
	}
	tooNew := addTx("too-new", to+1, false)
	var want []chain.Transfer
	for _, s := range []struct {
		seed   string
		slot   uint64
		failed bool
	}{{"in-a", to, false}, {"in-b", 1500, false}, {"in-c", 1500, false}, {"in-d", 1500, false}, {"in-failed", 1200, true}, {"in-e", from, false}} {
		sig := addTx(s.seed, s.slot, s.failed)
		if !s.failed {
			want = append(want, tr(sig, s.slot, tmpl.BlockTime, 1, devnetFeePayer, devnetSOLRecv, "", 100))
		}
	}
	tooOld := addTx("too-old", from-1, false)
	for i := range 1500 {
		addTx(fmt.Sprint("below-", i), from-10-uint64(i%900), false)
	}
	chainSortTransfers(want)

	got := scan(t, f.client(t, WithConcurrency(8)), from, to, true, nil, devnetSOLRecv)
	assert.Equal(t, want, got)
	assert.Equal(t, 2, f.count("getSignaturesForAddress"), "stops paging once below FromBlock")
	assert.Equal(t, 5, f.count("getTransaction"), "only successful in-range signatures are fetched")
	assert.Zero(t, f.txCount(tooNew))
	assert.Zero(t, f.txCount(tooOld))
}

func chainSortTransfers(ts []chain.Transfer) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0; j-- {
			a, b := ts[j-1], ts[j]
			if a.BlockNumber < b.BlockNumber || (a.BlockNumber == b.BlockNumber && a.TxHash <= b.TxHash) {
				break
			}
			ts[j-1], ts[j] = b, a
		}
	}
}

func TestScanShortPagesDoNotTruncate(t *testing.T) {
	// A provider capping pages below the requested 1000 must not make the
	// adapter believe it reached the end of the history.
	f := newFakeNode(t)
	tmpl := loadFixture(t, fxSOLTransfer)
	var want []chain.Transfer
	for i := range 5 {
		sig := fakeSig(fmt.Sprint("short-", i))
		slot := uint64(100 + i)
		f.putTx(sig, rewriteFixture(t, tmpl.Raw, sig, slot, nil), -1)
		f.addSig(devnetSOLRecv, sigEntry{Signature: sig, Slot: slot, BlockTime: tmpl.BlockTime})
		want = append(want, tr(sig, slot, tmpl.BlockTime, 1, devnetFeePayer, devnetSOLRecv, "", 100))
	}
	f.addSig(devnetSOLRecv, sigEntry{Signature: fakeSig("old"), Slot: 50})
	f.setPageCap(2)
	got := scan(t, f.client(t), 100, 104, true, nil, devnetSOLRecv)
	assert.Equal(t, want, got)
	assert.Equal(t, 3, f.count("getSignaturesForAddress"))
}

func TestScanMaxPagesGuard(t *testing.T) {
	t.Run("busy account above the range", func(t *testing.T) {
		f := newFakeNode(t)
		for i := range 2500 {
			f.addSig(devnetSOLRecv, sigEntry{Signature: fakeSig(fmt.Sprint("busy-", i)), Slot: 5000 + uint64(i)})
		}
		got, err := f.client(t, WithMaxSignaturePages(2)).ScanTransfers(context.Background(),
			chain.ScanRequest{FromBlock: 100, ToBlock: 200, Addresses: []string{devnetSOLRecv}, Native: true})
		require.ErrorIs(t, err, ErrTooManyPages)
		assert.NotErrorIs(t, err, chain.ErrRangeTooLarge, "a smaller range would not help")
		assert.Nil(t, got)
		assert.Equal(t, 2, f.count("getSignaturesForAddress"))
		assert.Zero(t, f.count("getTransaction"))
	})
	t.Run("too many signatures inside the range", func(t *testing.T) {
		f := newFakeNode(t)
		for i := range 2500 {
			f.addSig(devnetSOLRecv, sigEntry{Signature: fakeSig(fmt.Sprint("dense-", i)), Slot: 100 + uint64(i%100)})
		}
		got, err := f.client(t, WithMaxSignaturePages(2)).ScanTransfers(context.Background(),
			chain.ScanRequest{FromBlock: 100, ToBlock: 200, Addresses: []string{devnetSOLRecv}, Native: true})
		require.ErrorIs(t, err, ErrTooManyPages)
		assert.ErrorIs(t, err, chain.ErrRangeTooLarge)
		assert.Nil(t, got)
	})
}

func TestScanNullTransactionIsAnError(t *testing.T) {
	f := newFakeNode(t)
	tmpl := loadFixture(t, fxSOLTransfer)
	sig := fakeSig("missing")
	f.addSig(devnetSOLRecv, sigEntry{Signature: sig, Slot: tmpl.Slot}) // listed, but getTransaction → null
	got, err := f.client(t, WithRetries(2)).ScanTransfers(context.Background(),
		chain.ScanRequest{FromBlock: tmpl.Slot, ToBlock: tmpl.Slot, Addresses: []string{devnetSOLRecv}, Native: true})
	require.ErrorIs(t, err, ErrTransactionNotFound)
	assert.Contains(t, err.Error(), sig)
	assert.Nil(t, got)
	assert.Equal(t, 3, f.txCount(sig), "null results are retried")
}

func TestScanUnsupportedTransactionVersion(t *testing.T) {
	const owner = "9BM5b6j2G66n9JmeZxAu2gFrUW4NBMXHJyWTwsnMNZgQ"
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxMultiV1))
	_, err := f.client(t, WithMaxTransactionVersion(0)).ScanTransfers(context.Background(),
		chain.ScanRequest{FromBlock: fx.Slot, ToBlock: fx.Slot, Addresses: []string{owner}, Native: true})
	var rpcErr *RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, -32015, rpcErr.Code)
	assert.Equal(t, 1, f.txCount(fx.Sig), "deterministic errors are not retried")
}

func TestScanRangeBeyondFinalizedHead(t *testing.T) {
	f := newFakeNode(t)
	f.setHead(1000)
	_, err := f.client(t, WithRetries(1)).ScanTransfers(context.Background(),
		chain.ScanRequest{FromBlock: 990, ToBlock: 1001, Addresses: []string{devnetSOLRecv}, Tokens: []string{usdcDevnet}, Native: true})
	var rpcErr *RPCError
	require.ErrorAs(t, err, &rpcErr)
	assert.Equal(t, -32016, rpcErr.Code, "minContextSlot guards against lagging nodes")
}

func TestScanRequestValidation(t *testing.T) {
	f := newFakeNode(t)
	c := f.client(t)
	for name, req := range map[string]chain.ScanRequest{
		"inverted range":  {FromBlock: 10, ToBlock: 9, Addresses: []string{devnetSOLRecv}, Native: true},
		"no addresses":    {FromBlock: 1, ToBlock: 2, Native: true},
		"bad address":     {FromBlock: 1, ToBlock: 2, Addresses: []string{"0xdAC17F958D2ee523a2206206994597C13D831ec7"}, Native: true},
		"bad mint":        {FromBlock: 1, ToBlock: 2, Addresses: []string{devnetSOLRecv}, Tokens: []string{"not-a-mint"}},
		"short base58 id": {FromBlock: 1, ToBlock: 2, Addresses: []string{"1111"}, Native: true},
	} {
		_, err := c.ScanTransfers(context.Background(), req)
		assert.Error(t, err, name)
	}
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{FromBlock: 1, ToBlock: 2, Addresses: []string{devnetSOLRecv}})
	require.NoError(t, err)
	assert.Empty(t, got, "nothing requested")
	assert.Zero(t, f.count("getSignaturesForAddress"))
}

func TestScanMalformedTransactions(t *testing.T) {
	tmpl := loadFixture(t, fxSOLAndSPL)
	for name, edit := range map[string]func(m map[string]any){
		"balance count mismatch": func(m map[string]any) {
			meta := m["meta"].(map[string]any)
			meta["postBalances"] = meta["postBalances"].([]any)[1:]
		},
		"token index out of range": func(m map[string]any) {
			b := m["meta"].(map[string]any)["postTokenBalances"].([]any)[0].(map[string]any)
			b["accountIndex"] = json.Number("99")
		},
		"non-integer token amount": func(m map[string]any) {
			b := m["meta"].(map[string]any)["postTokenBalances"].([]any)[0].(map[string]any)
			b["uiTokenAmount"].(map[string]any)["amount"] = "1.5"
		},
		"fractional lamports": func(m map[string]any) {
			m["meta"].(map[string]any)["postBalances"].([]any)[1] = json.Number("1.5")
		},
		"missing meta": func(m map[string]any) { m["meta"] = nil },
		"wrong slot":   func(m map[string]any) { m["slot"] = json.Number("1") },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeNode(t)
			f.addFixture(fixture{Sig: tmpl.Sig, Slot: tmpl.Slot, BlockTime: tmpl.BlockTime, Keys: tmpl.Keys, Raw: rewriteFixture(t, tmpl.Raw, "", 0, edit)})
			got, err := f.client(t).ScanTransfers(context.Background(), chain.ScanRequest{
				FromBlock: tmpl.Slot, ToBlock: tmpl.Slot, Addresses: []string{solAndSPLOwner}, Tokens: []string{usdcDevnet}, Native: true,
			})
			require.Error(t, err)
			assert.Contains(t, err.Error(), tmpl.Sig)
			assert.Nil(t, got)
		})
	}
}

func TestScanDeterministicOrdering(t *testing.T) {
	f := newFakeNode(t)
	for _, name := range []string{fxSOLTransfer, fxSPLCreateATA, fxSOLAndSPL, fxSOLAndSPLCPI, fxMultiV1, fxSPLExisting} {
		f.addFixture(loadFixture(t, name))
	}
	f.addTokenAccount("7ZvoVfPEz5q4jYEQWYreeSusKKGhR6ximpt43TpuVTav", tokenAcct{Pubkey: "2Jm6mt9Qjn2y8xB8QexAGVDL874g2pUWp2MfHoZvEDi8", Mint: usdcDevnet, Amount: "1"})
	owners := []string{
		devnetSOLRecv, "7kL87prFVvm36nwLLwpDQF2J2w7ZURYjGoewwsZvv6hq", solAndSPLOwner,
		"2y1yzRannWLf1s4dDWjNM9zWDcreS3kDA3oxs9a14z1C", "Hd1e8oUcg34exfqnESJjFCR53G6iyeSoxBvBLjiSJGH7",
		"FJFbhFS8rfiFvXoM2j9meDzTMQ5nza2mqGruMEgSZT6w", "7ZvoVfPEz5q4jYEQWYreeSusKKGhR6ximpt43TpuVTav",
	}
	c := f.client(t, WithConcurrency(16))
	first := scan(t, c, 522_818_000, 522_820_000, true, []string{usdcDevnet}, owners...)
	require.Len(t, first, 9)
	for i := 1; i < len(first); i++ {
		a, b := first[i-1], first[i]
		assert.True(t, a.BlockNumber < b.BlockNumber || (a.BlockNumber == b.BlockNumber && (a.TxHash < b.TxHash || (a.TxHash == b.TxHash && a.Index < b.Index))), "sorted at %d", i)
	}
	for range 5 {
		assert.Equal(t, first, scan(t, c, 522_818_000, 522_820_000, true, []string{usdcDevnet}, owners...))
	}
}

func TestScanContextCancellation(t *testing.T) {
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSOLTransfer))
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f.setIntercept(func(w http.ResponseWriter, method string, _ []json.RawMessage) bool {
		if method != "getTransaction" {
			return false
		}
		<-release // hang until the test ends
		return true
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := f.client(t).ScanTransfers(ctx, chain.ScanRequest{FromBlock: fx.Slot, ToBlock: fx.Slot, Addresses: []string{devnetSOLRecv}, Native: true})
	require.ErrorIs(t, err, context.Canceled)
	assert.Less(t, time.Since(start), 3*time.Second)
}

func TestScanTokenAccountOwnerMismatch(t *testing.T) {
	f := newFakeNode(t)
	f.setIntercept(func(w http.ResponseWriter, method string, _ []json.RawMessage) bool {
		if method != "getTokenAccountsByOwner" {
			return false
		}
		writeResult(w, json.RawMessage("1"), map[string]any{"context": map[string]any{"slot": 1}, "value": []any{map[string]any{
			"pubkey": "7JcceuTy5LyvPRqBrRMSy7AxU9roVStjAVdh4bcvAqfH",
			"account": map[string]any{"data": map[string]any{"program": "spl-token", "parsed": map[string]any{"info": map[string]any{
				"mint": usdcDevnet, "owner": solAndSPLSender,
			}}}},
		}}})
		return true
	})
	_, err := f.client(t).ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: 1, ToBlock: 2, Addresses: []string{solAndSPLOwner}, Tokens: []string{usdcDevnet},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to")
}

func TestRunLimitedFirstErrorWins(t *testing.T) {
	boom := errors.New("boom")
	err := runLimited(context.Background(), 50, 4, func(ctx context.Context, i int) error {
		if i == 3 {
			return boom
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})
	assert.ErrorIs(t, err, boom)
}

func TestScanRateLimitedMidScanReturnsNoPartialResult(t *testing.T) {
	f := newFakeNode(t)
	a := f.addFixture(loadFixture(t, fxSOLAndSPL))
	b := f.addFixture(loadFixture(t, fxSOLAndSPLCPI))
	f.setIntercept(func(w http.ResponseWriter, method string, params []json.RawMessage) bool {
		var sig string
		if method != "getTransaction" || json.Unmarshal(params[0], &sig) != nil || sig != b.Sig {
			return false
		}
		rateLimited(w, "0")
		return true
	})
	got, err := f.client(t, WithRetries(1)).ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: a.Slot, ToBlock: b.Slot, Native: true, Tokens: []string{usdcDevnet},
		Addresses: []string{solAndSPLOwner, "2y1yzRannWLf1s4dDWjNM9zWDcreS3kDA3oxs9a14z1C"},
	})
	require.ErrorIs(t, err, chain.ErrRateLimited)
	assert.Nil(t, got)
}

func TestScanFailsOverToSecondEndpoint(t *testing.T) {
	f := newFakeNode(t)
	fx := f.addFixture(loadFixture(t, fxSOLTransfer))
	down := newScripted(t, func(_ int, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	c := newTestClient(t, []string{down.srv.URL, f.srv.URL})
	got, err := c.ScanTransfers(context.Background(), chain.ScanRequest{
		FromBlock: fx.Slot, ToBlock: fx.Slot, Addresses: []string{devnetSOLRecv}, Tokens: []string{usdcDevnet}, Native: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []chain.Transfer{tr(fx.Sig, fx.Slot, fx.BlockTime, 1, devnetFeePayer, devnetSOLRecv, "", 100)}, got)
	assert.Positive(t, down.calls.Load())
}
