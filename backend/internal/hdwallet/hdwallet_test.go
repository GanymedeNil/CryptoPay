package hdwallet

import (
	"bytes"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/btcsuite/btcd/btcutil/base58"
	"github.com/btcsuite/btcd/btcutil/bech32"
	"github.com/btcsuite/btcd/btcutil/hdkeychain"
	"github.com/btcsuite/btcd/chaincfg"

	"cryptopay/internal/address"
	"cryptopay/internal/domain"
	"cryptopay/internal/hdwallet/hdwallettest"
)

const (
	bip84Mnemonic = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	bip84Zpub     = "zpub6rFR7y4Q2AijBEqTUquhVz398htDFrtymD9xYYfG1m4wAcvPhXNfE3EfH1r1ADqtfSdVCToUG868RvUUkgDKf31mGDtKsAYz2oz2AGutZYs"
	hardhat       = "test test test test test test test test test test test junk"
)

var (
	evmChain  = domain.Chain{ID: "ethereum", Family: domain.FamilyEVM, NetworkID: "1"}
	tronChain = domain.Chain{ID: "tron", Family: domain.FamilyTron}
	btcChain  = domain.Chain{ID: "bitcoin", Family: domain.FamilyUTXO, Params: domain.ChainParam{Bech32HRP: "bc"}}
	ltcChain  = domain.Chain{ID: "litecoin", Family: domain.FamilyUTXO, Params: domain.ChainParam{Bech32HRP: "ltc"}}
	tbtcChain = domain.Chain{ID: "bitcoin-testnet", Family: domain.FamilyUTXO, Params: domain.ChainParam{Bech32HRP: "tb"}}
	solChain  = domain.Chain{ID: "solana", Family: domain.FamilySolana}
)

func TestBIP84Vectors(t *testing.T) {
	for i, want := range []string{
		"bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu",
		"bc1qnjg0jd8228aq7egyzacy8cys3knf9xvrerkf9g",
	} {
		got, err := Derive(btcChain, bip84Zpub, uint32(i))
		if err != nil || got != want {
			t.Errorf("index %d: got %q err %v want %q", i, got, err, want)
		}
	}
	// Same address via our own mnemonic helper at m/84'/0'/0' (plain xpub encoding).
	xpub := hdwallettest.AccountXpub(bip84Mnemonic, "m/84'/0'/0'")
	got, err := Derive(btcChain, xpub, 0)
	if err != nil || got != "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu" {
		t.Errorf("helper xpub: %q %v", got, err)
	}
}

func TestEVMHardhat(t *testing.T) {
	xpub := hdwallettest.AccountXpub(hardhat, "m/44'/60'/0'")
	for i, want := range []string{
		"0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266",
		"0x70997970C51812dc3A010C7d01b50e0d17dc79C8",
	} {
		got, err := Derive(evmChain, xpub, uint32(i))
		if err != nil || got != want {
			t.Errorf("index %d: got %q err %v want %q", i, got, err, want)
		}
	}
}

func TestTron(t *testing.T) {
	xpub := hdwallettest.AccountXpub(hardhat, "m/44'/195'/0'")
	xpubEVM := hdwallettest.AccountXpub(hardhat, "m/44'/195'/0'")
	for i := uint32(0); i < 3; i++ {
		tr, err := Derive(tronChain, xpub, i)
		if err != nil {
			t.Fatal(err)
		}
		ev, err := Derive(evmChain, xpubEVM, i)
		if err != nil {
			t.Fatal(err)
		}
		// Same 20 bytes in both encodings.
		tb, err := address.TronBytes(tr)
		if err != nil {
			t.Fatal(err)
		}
		eb, _ := address.EVMBytes(ev)
		if !bytes.Equal(tb, eb) {
			t.Errorf("bytes differ at %d", i)
		}
		// Independent base58check decode: 0x41 || 20 bytes || 4 checksum bytes.
		raw := base58.Decode(tr)
		if len(tr) != 34 || tr[0] != 'T' || len(raw) != 25 || raw[0] != 0x41 {
			t.Fatalf("bad tron address %q", tr)
		}
		h := sha256.Sum256(raw[:21])
		h = sha256.Sum256(h[:])
		if !bytes.Equal(h[:4], raw[21:]) {
			t.Errorf("checksum mismatch %q", tr)
		}
	}
	// Known public vector: the Hardhat account 0 key material encodes to the
	// Tron form of 0xf39F...; derive it directly from the helper at the EVM path.
	evx := hdwallettest.AccountXpub(hardhat, "m/44'/60'/0'")
	tr, err := Derive(tronChain, evx, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := address.TronFromBytes(mustHex(t, "f39Fd6e51aad88F6F4ce6aB8827279cffFb92266"))
	if tr != want {
		t.Errorf("tron %q want %q", tr, want)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := address.EVMBytes("0x" + s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func program(t *testing.T, addr string) []byte {
	t.Helper()
	_, data, err := bech32.Decode(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, err := bech32.ConvertBits(data[1:], 5, 8, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLitecoinAndTestnet(t *testing.T) {
	btc, _ := Derive(btcChain, bip84Zpub, 3)
	ltc, err := Derive(ltcChain, bip84Zpub, 3)
	if err != nil || !strings.HasPrefix(ltc, "ltc1q") {
		t.Fatalf("ltc %q %v", ltc, err)
	}
	if !bytes.Equal(program(t, btc), program(t, ltc)) {
		t.Error("ltc program differs")
	}
	// Re-serialise the key with other SLIP-132 / testnet version bytes.
	k, _ := hdkeychain.NewKeyFromString(bip84Zpub)
	for name, ver := range map[string][]byte{
		"xpub": {0x04, 0x88, 0xB2, 0x1E},
		"ypub": {0x04, 0x9D, 0x7C, 0xB2},
		"tpub": {0x04, 0x35, 0x87, 0xCF},
		"upub": {0x04, 0x4A, 0x52, 0x62},
		"vpub": {0x04, 0x5F, 0x1C, 0xF6},
		"Ltub": {0x01, 0x9D, 0xA4, 0x62},
		"Mtub": {0x01, 0xB2, 0x6E, 0xF6},
	} {
		c, err := k.CloneWithVersion(ver)
		if err != nil {
			t.Fatal(err)
		}
		s := c.String()
		if err := Validate(domain.FamilyUTXO, s); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		got, err := Derive(tbtcChain, s, 0)
		if err != nil || !strings.HasPrefix(got, "tb1q") {
			t.Fatalf("%s: %q %v", name, got, err)
		}
		if !bytes.Equal(program(t, got), program(t, "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu")) {
			t.Errorf("%s: program differs", name)
		}
	}
}

func TestRejectPrivate(t *testing.T) {
	k, err := hdkeychain.NewMaster(hdwallettest.Seed(hardhat), mainnet())
	if err != nil {
		t.Fatal(err)
	}
	priv := k.String()
	if !strings.HasPrefix(priv, "xprv") {
		t.Fatal(priv)
	}
	z, _ := k.CloneWithVersion([]byte{0x04, 0xB2, 0x43, 0x0C})  // zprv
	tp, _ := k.CloneWithVersion([]byte{0x04, 0x35, 0x83, 0x94}) // tprv
	for _, s := range []string{priv, z.String(), tp.String()} {
		for _, f := range []domain.Family{domain.FamilyEVM, domain.FamilyTron, domain.FamilyUTXO} {
			err := Validate(f, s)
			if err == nil || !strings.Contains(err.Error(), "never paste private keys") {
				t.Errorf("Validate(%.4s) = %v", s, err)
			}
		}
		if _, err := Derive(evmChain, s, 0); err == nil {
			t.Error("Derive accepted private key")
		}
	}
}

func TestValidateRejects(t *testing.T) {
	good := hdwallettest.AccountXpub(hardhat, "m/44'/60'/0'")
	if err := Validate(domain.FamilyEVM, good); err != nil {
		t.Fatal(err)
	}
	if err := Validate(domain.FamilySolana, good); err == nil {
		t.Error("solana must be rejected")
	}
	if err := Validate(domain.Family("x"), good); err == nil {
		t.Error("unknown family")
	}
	bad := good[:len(good)-1] + "A"
	if bad == good {
		bad = good[:len(good)-1] + "B"
	}
	for _, s := range []string{"", "garbage", "xpub", "0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266", bad, " " + good, good + "x", "bc1qcr8te4kr609gcawutmrza0j4xv80jy8z306fyu"} {
		if err := Validate(domain.FamilyEVM, s); err == nil {
			t.Errorf("Validate(%q) should fail", s)
		}
		if _, err := Derive(evmChain, s, 0); err == nil {
			t.Errorf("Derive(%q) should fail", s)
		}
	}
}

func TestIndexBoundsAndDeterminism(t *testing.T) {
	x := hdwallettest.AccountXpub(hardhat, "m/44'/60'/0'")
	for _, i := range []uint32{hdkeychain.HardenedKeyStart, hdkeychain.HardenedKeyStart + 1, ^uint32(0)} {
		if _, err := Derive(evmChain, x, i); err == nil {
			t.Errorf("index %d must be rejected", i)
		}
	}
	if _, err := Derive(evmChain, x, hdkeychain.HardenedKeyStart-1); err != nil {
		t.Errorf("max non-hardened index: %v", err)
	}
	a, _ := Derive(evmChain, x, 7)
	b, _ := Derive(evmChain, x, 7)
	c, _ := Derive(evmChain, x, 8)
	if a != b || a == c {
		t.Error("determinism")
	}
	if _, err := Derive(solChain, x, 0); err == nil {
		t.Error("solana derive must fail")
	}
	if _, err := Derive(domain.Chain{Family: domain.FamilyUTXO}, x, 0); err == nil {
		t.Error("utxo without hrp must fail")
	}
}

func mainnet() *chaincfg.Params { return &chaincfg.MainNetParams }
