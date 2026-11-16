package utxo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cryptopay/internal/chain"
)

// blockScan stream-decodes one getblock <hash> 2 result and collects the
// outputs paying watched addresses.
//
// Only one transaction is held in memory at a time, so memory is bounded by
// the largest transaction rather than by the block (verbosity-2 blocks are
// often 10+ MB of JSON). Member order is not assumed: Litecoin Core emits
// "tx" before "time", Bitcoin Core after it.
type blockScan struct {
	height uint64
	hash   string                           // requested block hash, lower-case hex
	match  func(addr string) (string, bool) // node address → canonical watched address

	transfers []chain.Transfer
}

// decode is a resultFunc. It may run several times (retries), so it starts
// from a clean slate every time.
func (b *blockScan) decode(dec *json.Decoder, first json.Token) error {
	b.transfers = nil
	if d, ok := first.(json.Delim); !ok || d != '{' {
		return badData("getblock result has type %s, not object (verbosity 2 unsupported?)", tokenKind(first))
	}

	var (
		hash                      string
		height, blockTime, nTx    int64
		conf                      int64
		haveHeight, haveTime      bool
		haveNTx, haveConf, haveTx bool
		txCount                   int64
	)
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "hash":
			if hash, err = readString(dec, key); err != nil {
				return err
			}
		case "height":
			if height, err = readInt(dec, key); err != nil {
				return err
			}
			haveHeight = true
		case "time":
			if blockTime, err = readInt(dec, key); err != nil {
				return err
			}
			haveTime = true
		case "confirmations":
			if conf, err = readInt(dec, key); err != nil {
				return err
			}
			haveConf = true
		case "nTx":
			if nTx, err = readInt(dec, key); err != nil {
				return err
			}
			haveNTx = true
		case "tx":
			if txCount, err = b.decodeTxs(dec); err != nil {
				return err
			}
			haveTx = true
		default:
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	if err := expectDelim(dec, '}'); err != nil {
		return err
	}

	switch {
	case !strings.EqualFold(hash, b.hash):
		return badData("getblock returned block %q, want %s", truncate(hash, 80), b.hash)
	case haveHeight && (height < 0 || uint64(height) != b.height):
		return badData("block %s is at height %d, want %d", b.hash, height, b.height)
	case !haveTx:
		return badData("block %s has no tx array", b.hash)
	case !haveTime:
		return badData("block %s has no time", b.hash)
	case haveNTx && nTx != txCount:
		return badData("block %s has nTx=%d but %d transactions were returned", b.hash, nTx, txCount)
	case haveConf && conf < 0:
		// Bitcoin Core reports -1 for blocks that left the active chain between
		// getblockhash and getblock. Retrying the same hash cannot help; the
		// caller retries the range and gets the new block.
		return &fatalError{err: fmt.Errorf("block %s at height %d is no longer in the active chain", b.hash, b.height)}
	}

	bt := time.Unix(blockTime, 0).UTC()
	for i := range b.transfers {
		b.transfers[i].BlockHash = b.hash
		b.transfers[i].BlockTime = bt
	}
	return nil
}

// decodeTxs walks the "tx" array one transaction at a time.
func (b *blockScan) decodeTxs(dec *json.Decoder) (int64, error) {
	if err := expectDelim(dec, '['); err != nil {
		return 0, err
	}
	var pos int64
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return 0, fmt.Errorf("tx #%d: %w", pos, err)
		}
		if err := b.scanTx(raw, pos); err != nil {
			return 0, err
		}
		pos++
	}
	if err := expectDelim(dec, ']'); err != nil {
		return 0, err
	}
	return pos, nil
}

type txJSON struct {
	Txid string            `json:"txid"`
	Vout []json.RawMessage `json:"vout"`
}

// voutJSON keeps every member raw: outputs we do not understand (Litecoin
// MWEB outputs, future script types) must not break decoding of the block.
type voutJSON struct {
	Value        json.RawMessage `json:"value"`
	N            json.RawMessage `json:"n"`
	ScriptPubKey json.RawMessage `json:"scriptPubKey"`
	IsMWEB       json.RawMessage `json:"ismweb"`
}

type scriptPubKeyJSON struct {
	Address   json.RawMessage `json:"address"`   // Bitcoin Core >= 22
	Addresses json.RawMessage `json:"addresses"` // older nodes, Litecoin Core 0.21
}

func (b *blockScan) scanTx(raw json.RawMessage, pos int64) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return badData("block %s tx #%d is not an object: node does not return getblock verbosity 2", b.hash, pos)
	}
	var tx txJSON
	if err := json.Unmarshal(raw, &tx); err != nil {
		return badData("block %s tx #%d: %v", b.hash, pos, err)
	}
	for i, rv := range tx.Vout {
		var vo voutJSON
		if json.Unmarshal(rv, &vo) != nil {
			continue // not an object: unknown shape, cannot pay an address
		}
		if string(bytes.TrimSpace(vo.IsMWEB)) == "true" {
			continue // Litecoin MWEB output: confidential, no address or value
		}
		addr := outputAddress(vo.ScriptPubKey)
		if addr == "" {
			continue // nulldata, non-standard, bare multisig...
		}
		to, ok := b.match(addr)
		if !ok {
			continue
		}

		// From here on the output pays a watched address: anything malformed
		// is an error, never a silently dropped payment.
		if !isHash(tx.Txid) {
			return badData("block %s tx #%d pays a watched address but has invalid txid %q", b.hash, pos, truncate(tx.Txid, 80))
		}
		n, err := parseIndex(vo.N)
		if err != nil {
			return badData("tx %s vout #%d: n: %v", tx.Txid, i, err)
		}
		amount, err := parseAmount(string(vo.Value), coinDecimals)
		if err != nil {
			return badData("tx %s vout %d: value: %v", tx.Txid, n, err)
		}
		if amount.Sign() == 0 {
			continue // no value moved
		}
		b.transfers = append(b.transfers, chain.Transfer{
			TxHash:      strings.ToLower(tx.Txid),
			Index:       n,
			BlockNumber: b.height,
			To:          to,
			Amount:      amount,
		})
	}
	return nil
}

// outputAddress returns the single address an output pays, or "" when the
// output has none (OP_RETURN, non-standard) or several (bare multisig in old
// nodes' "addresses").
func outputAddress(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var spk scriptPubKeyJSON
	if json.Unmarshal(raw, &spk) != nil {
		return ""
	}
	if len(spk.Address) > 0 {
		var s string
		if json.Unmarshal(spk.Address, &s) == nil && s != "" {
			return s
		}
	}
	if len(spk.Addresses) > 0 {
		var ss []string
		if json.Unmarshal(spk.Addresses, &ss) == nil && len(ss) == 1 {
			return ss[0]
		}
	}
	return ""
}

func parseIndex(raw json.RawMessage) (int, error) {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "null" {
		return 0, errors.New("missing")
	}
	n, err := strconv.ParseUint(s, 10, 31)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid output index", truncate(s, 40))
	}
	return int(n), nil
}

func readString(dec *json.Decoder, key string) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	s, ok := tok.(string)
	if !ok {
		if err := skipRest(dec, tok); err != nil {
			return "", err
		}
		return "", badData("%q has type %s, want string", key, tokenKind(tok))
	}
	return s, nil
}

func readInt(dec *json.Decoder, key string) (int64, error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, err
	}
	n, ok := tok.(json.Number)
	if !ok {
		if err := skipRest(dec, tok); err != nil {
			return 0, err
		}
		return 0, badData("%q has type %s, want number", key, tokenKind(tok))
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return 0, badData("%q: %q is not an integer", key, truncate(n.String(), 40))
	}
	return v, nil
}

// isHash reports whether s is a 32-byte hash in hex (block hash, txid).
func isHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}
