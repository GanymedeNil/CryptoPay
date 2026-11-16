package tron

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"cryptopay/internal/address"
	"cryptopay/internal/chain"
)

// Shapes of the java-tron full-node HTTP API (/wallet/...) as served by
// TronGrid and commercial providers, requested with visible=false (addresses
// as "41"-prefixed hex). Only the fields the adapter needs are declared.
// Fields equal to their protobuf default are omitted by the node: block 0 has
// no "number", an empty block has no "transactions", a successful
// TransactionInfo has no top-level "result" and non-VM transactions have no
// "receipt.result".

// apiBlock is the response of /wallet/getblockbynum, /wallet/getnowblock and
// /wallet/getblock. A block the node does not have is returned as "{}".
type apiBlock struct {
	BlockID     string          `json:"blockID"`
	BlockHeader *apiBlockHeader `json:"block_header"`
	// Transactions is absent for an empty block and for /wallet/getblock
	// with detail=false.
	Transactions []apiTx `json:"transactions"`
}

type apiBlockHeader struct {
	RawData struct {
		Number     uint64 `json:"number"`
		Timestamp  int64  `json:"timestamp"` // milliseconds
		TxTrieRoot string `json:"txTrieRoot"`
		ParentHash string `json:"parentHash"`
	} `json:"raw_data"`
}

type apiTx struct {
	TxID string `json:"txID"`
	// Ret is the execution result recorded in the block. contractRet is
	// "SUCCESS" for every successful transaction (VM or not) and e.g.
	// "REVERT" / "OUT_OF_ENERGY" for failed smart-contract calls.
	Ret []struct {
		ContractRet string `json:"contractRet"`
	} `json:"ret"`
	RawData struct {
		Contract []struct {
			Type      string `json:"type"`
			Parameter struct {
				Value json.RawMessage `json:"value"`
			} `json:"parameter"`
		} `json:"contract"`
	} `json:"raw_data"`
}

// apiTransferValue is the parameter of a TransferContract (native TRX).
type apiTransferValue struct {
	Amount       json.Number `json:"amount"` // sun, int64
	OwnerAddress string      `json:"owner_address"`
	ToAddress    string      `json:"to_address"`
}

// apiTxInfo is one element of /wallet/gettransactioninfobyblocknum and the
// response of /wallet/gettransactioninfobyid ("{}" when unknown).
//
// Observed on mainnet: a failed contract call carries "result":"FAILED" and
// receipt.result "REVERT" / "OUT_OF_ENERGY" (/ other VM errors) and has no
// "log" field; a successful one has no "result" and receipt.result "SUCCESS".
type apiTxInfo struct {
	ID          string `json:"id"`
	BlockNumber uint64 `json:"blockNumber"`
	Result      string `json:"result"`
	Receipt     struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Log []apiLog `json:"log"`
}

type apiLog struct {
	Address string   `json:"address"` // 20-byte hex without the 41 prefix
	Topics  []string `json:"topics"`  // 32-byte hex, no 0x
	Data    string   `json:"data"`
}

const (
	contractTransfer = "TransferContract"
	contractTrigger  = "TriggerSmartContract"
	contractCreate   = "CreateSmartContract"
	retSuccess       = "SUCCESS"
)

// transferTopic is keccak256("Transfer(address,address,uint256)") in hex
// (ddf252ad...b3ef).
var transferTopic = hex.EncodeToString(address.Keccak256([]byte("Transfer(address,address,uint256)")))

type addrKey [20]byte

// filter is a validated ScanRequest.
type filter struct {
	native    bool
	addresses map[addrKey]string // account hash -> canonical base58
	tokens    map[addrKey]string // contract hash -> canonical base58
}

// tronKey decodes a Tron address in any form accepted by package address
// (base58, "41"-hex, bare 20-byte hex).
func tronKey(s string) (addrKey, error) {
	var k addrKey
	b, err := address.TronBytes(s)
	if err != nil {
		return k, err
	}
	copy(k[:], b)
	return k, nil
}

func canonical(k addrKey) string {
	s, _ := address.TronFromBytes(k[:]) // cannot fail for 20 bytes
	return s
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

func isZeroHex(s string) bool {
	return strings.Trim(s, "0") == ""
}

// validateBlock checks that b is the complete block num. A "{}" answer
// (block unknown to this node) yields errNotFound. withTxs reports whether
// transactions were requested (false for /wallet/getblock detail=false).
func validateBlock(b *apiBlock, num uint64, withTxs bool) error {
	if b.BlockID == "" && b.BlockHeader == nil && len(b.Transactions) == 0 {
		return fmt.Errorf("block %d: %w", num, errNotFound)
	}
	if b.BlockHeader == nil {
		return fmt.Errorf("block %d: %w: no block_header", num, errMalformed)
	}
	if !isHex(b.BlockID, 64) {
		return fmt.Errorf("block %d: %w: bad blockID %q", num, errMalformed, truncate(b.BlockID))
	}
	if h := b.BlockHeader.RawData.Number; h != num {
		return fmt.Errorf("block %d: %w: node returned block %d", num, errMalformed, h)
	}
	// A Tron block id starts with the 8-byte big-endian height.
	if idNum, _ := strconv.ParseUint(b.BlockID[:16], 16, 64); idNum != num {
		return fmt.Errorf("block %d: %w: blockID encodes height %d", num, errMalformed, idNum)
	}
	if num > 0 && b.BlockHeader.RawData.Timestamp <= 0 {
		return fmt.Errorf("block %d: %w: missing timestamp", num, errMalformed)
	}
	if !withTxs {
		return nil
	}
	// The txTrieRoot is all zeros exactly when the block has no transactions
	// (and java-tron always emits it): this tells a genuinely empty block from
	// a truncated or stripped response.
	root := b.BlockHeader.RawData.TxTrieRoot
	if num > 0 {
		switch {
		case !isHex(root, 64):
			return fmt.Errorf("block %d: %w: bad txTrieRoot %q", num, errMalformed, truncate(root))
		case len(b.Transactions) == 0 && !isZeroHex(root):
			return fmt.Errorf("block %d: %w: no transactions but txTrieRoot %s", num, errMalformed, truncate(root))
		case len(b.Transactions) > 0 && isZeroHex(root):
			return fmt.Errorf("block %d: %w: transactions but empty txTrieRoot", num, errMalformed)
		}
	}
	seen := make(map[string]struct{}, len(b.Transactions))
	for i := range b.Transactions {
		id := strings.ToLower(b.Transactions[i].TxID)
		if !isHex(id, 64) {
			return fmt.Errorf("block %d: %w: tx #%d has bad txID %q", num, errMalformed, i, truncate(id))
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("block %d: %w: duplicate tx %s", num, errMalformed, id)
		}
		seen[id] = struct{}{}
		b.Transactions[i].TxID = id
	}
	return nil
}

// validateTxInfos checks that infos describe exactly the transactions of
// blk and indexes them by tx id. A node that has the block but has not
// processed it yet answers "[]": that is reported as errNotFound.
func validateTxInfos(infos []apiTxInfo, blk *apiBlock, num uint64) (map[string]*apiTxInfo, error) {
	if len(infos) != len(blk.Transactions) {
		if len(infos) == 0 {
			return nil, fmt.Errorf("transaction infos of block %d: %w", num, errNotFound)
		}
		return nil, fmt.Errorf("transaction infos of block %d: %w: %d infos for %d transactions",
			num, errMalformed, len(infos), len(blk.Transactions))
	}
	byID := make(map[string]*apiTxInfo, len(infos))
	for i := range infos {
		inf := &infos[i]
		inf.ID = strings.ToLower(inf.ID)
		if num > 0 && inf.BlockNumber != num {
			return nil, fmt.Errorf("transaction infos of block %d: %w: info for block %d", num, errMalformed, inf.BlockNumber)
		}
		if _, dup := byID[inf.ID]; dup {
			return nil, fmt.Errorf("transaction infos of block %d: %w: duplicate %s", num, errMalformed, truncate(inf.ID))
		}
		byID[inf.ID] = inf
	}
	for i := range blk.Transactions {
		if _, ok := byID[blk.Transactions[i].TxID]; !ok {
			return nil, fmt.Errorf("transaction infos of block %d: %w: no info for tx %s (block/info from different forks?)",
				num, errMalformed, blk.Transactions[i].TxID)
		}
	}
	return byID, nil
}

// needsTxInfo reports whether any transaction of b ran in the TVM and could
// therefore have emitted TRC-20 Transfer events.
func needsTxInfo(b *apiBlock) bool {
	for i := range b.Transactions {
		for _, c := range b.Transactions[i].RawData.Contract {
			if c.Type == contractTrigger || c.Type == contractCreate {
				return true
			}
		}
	}
	return false
}

// blockRef is the block context stamped on every transfer.
type blockRef struct {
	number uint64
	hash   string
	time   time.Time
}

func refOf(b *apiBlock) blockRef {
	return blockRef{
		number: b.BlockHeader.RawData.Number,
		hash:   strings.ToLower(b.BlockID),
		time:   time.UnixMilli(b.BlockHeader.RawData.Timestamp).UTC(),
	}
}

// nativeTransfer returns the TRX transfer carried by tx if it is a
// successful TransferContract with a positive amount to a watched address.
func nativeTransfer(tx *apiTx, ref blockRef, watched map[addrKey]string) (*chain.Transfer, error) {
	cs := tx.RawData.Contract
	if len(cs) != 1 {
		// The protocol only accepts transactions with exactly one contract.
		return nil, fmt.Errorf("tx %s: %w: %d contracts", tx.TxID, errMalformed, len(cs))
	}
	if cs[0].Type != contractTransfer {
		return nil, nil
	}
	if len(tx.Ret) == 0 || tx.Ret[0].ContractRet != retSuccess {
		return nil, nil
	}
	var v apiTransferValue
	if err := json.Unmarshal(cs[0].Parameter.Value, &v); err != nil {
		return nil, fmt.Errorf("tx %s: %w: TransferContract value: %v", tx.TxID, errMalformed, err)
	}
	to, err := tronKey(v.ToAddress)
	if err != nil {
		return nil, fmt.Errorf("tx %s: %w: to_address: %v", tx.TxID, errMalformed, err)
	}
	toCanon, ok := watched[to]
	if !ok {
		return nil, nil
	}
	amount, err := parseAmount(v.Amount)
	if err != nil {
		return nil, fmt.Errorf("tx %s: %w: amount: %v", tx.TxID, errMalformed, err)
	}
	if amount.Sign() <= 0 {
		return nil, nil
	}
	var from string
	if k, err := tronKey(v.OwnerAddress); err == nil {
		from = canonical(k)
	}
	return &chain.Transfer{
		TxHash: tx.TxID, Index: -1,
		BlockNumber: ref.number, BlockHash: ref.hash, BlockTime: ref.time,
		From: from, To: toCanon, Amount: amount,
	}, nil
}

func parseAmount(n json.Number) (*big.Int, error) {
	if n == "" {
		return new(big.Int), nil // protobuf default 0 is omitted
	}
	v, ok := new(big.Int).SetString(string(n), 10)
	if !ok {
		return nil, fmt.Errorf("not an integer: %q", truncate(string(n)))
	}
	return v, nil
}

// infoSucceeded reports whether a transaction info describes a successful
// smart-contract execution.
func infoSucceeded(inf *apiTxInfo) bool {
	if inf.Result != "" && inf.Result != "SUCESS" && inf.Result != retSuccess { // proto enum is spelled SUCESS
		return false
	}
	return inf.Receipt.Result == retSuccess
}

// tokenTransfers returns the TRC-20 Transfer events of a successful
// transaction emitted by a watched token to a watched recipient. Index is the
// position of the log in the transaction's log list.
func tokenTransfers(inf *apiTxInfo, ref blockRef, f *filter) ([]chain.Transfer, error) {
	if !infoSucceeded(inf) {
		return nil, nil
	}
	var out []chain.Transfer
	for i := range inf.Log {
		t, err := decodeTransferLog(&inf.Log[i], f)
		if err != nil {
			return nil, fmt.Errorf("tx %s log %d: %w", inf.ID, i, err)
		}
		if t == nil {
			continue
		}
		t.TxHash, t.Index = inf.ID, i
		t.BlockNumber, t.BlockHash, t.BlockTime = ref.number, ref.hash, ref.time
		out = append(out, *t)
	}
	return out, nil
}

// decodeTransferLog decodes l as Transfer(address indexed from, address
// indexed to, uint256 value) of a watched token to a watched address; nil if
// it is anything else.
func decodeTransferLog(l *apiLog, f *filter) (*chain.Transfer, error) {
	if len(l.Topics) != 3 || !strings.EqualFold(strings.TrimPrefix(l.Topics[0], "0x"), transferTopic) {
		return nil, nil
	}
	tokenKey, err := tronKey(l.Address)
	if err != nil {
		return nil, fmt.Errorf("%w: log address: %v", errMalformed, err)
	}
	token, ok := f.tokens[tokenKey]
	if !ok {
		return nil, nil
	}
	to, ok, err := topicAddress(l.Topics[2])
	if err != nil || !ok {
		return nil, err
	}
	toCanon, watched := f.addresses[to]
	if !watched {
		return nil, nil
	}
	data, err := hex.DecodeString(strings.TrimPrefix(l.Data, "0x"))
	if err != nil {
		return nil, fmt.Errorf("%w: log data is not hex", errMalformed)
	}
	if len(data) != 32 {
		return nil, nil // not a standard ERC-20 Transfer
	}
	amount := new(big.Int).SetBytes(data)
	if amount.Sign() == 0 {
		return nil, nil // zero-value transfers (address-poisoning spam) are not payments
	}
	var from string
	if k, ok, err := topicAddress(l.Topics[1]); err == nil && ok {
		from = canonical(k)
	}
	return &chain.Transfer{From: from, To: toCanon, Token: token, Amount: amount}, nil
}

// topicAddress decodes an ABI-encoded address topic. ok is false for a
// well-formed topic whose upper 12 bytes are not zero (not an address).
func topicAddress(topic string) (addrKey, bool, error) {
	var k addrKey
	b, err := hex.DecodeString(strings.TrimPrefix(topic, "0x"))
	if err != nil || len(b) != 32 {
		return k, false, fmt.Errorf("%w: bad topic %q", errMalformed, truncate(topic))
	}
	if !bytes.Equal(b[:12], make([]byte, 12)) {
		return k, false, nil
	}
	copy(k[:], b[12:])
	return k, true, nil
}

func truncate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}
