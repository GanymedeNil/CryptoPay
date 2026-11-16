package evm

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// Strict parsers for the hex encodings used by Ethereum JSON-RPC. Every value
// coming from a node is untrusted: malformed input yields an error, never a
// panic or a silently wrong number.

// maxQuantityDigits bounds hex quantities to 256 bits.
const maxQuantityDigits = 64

func has0x(s string) bool {
	return len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X')
}

func isHexDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// clip shortens untrusted strings before they are put into error messages.
func clip(s string) string {
	const n = 80
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func validQuantity(s string) bool {
	return has0x(s) && len(s) > 2 && len(s) <= 2+maxQuantityDigits && isHexDigits(s[2:])
}

// parseUint64 decodes a hex quantity ("0x1b4") that must fit in uint64.
func parseUint64(s string) (uint64, error) {
	if !validQuantity(s) {
		return 0, fmt.Errorf("invalid hex quantity %q", clip(s))
	}
	v, err := strconv.ParseUint(s[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("hex quantity %q overflows uint64", clip(s))
	}
	return v, nil
}

// parseBig decodes a hex quantity of at most 256 bits.
func parseBig(s string) (*big.Int, error) {
	if !validQuantity(s) {
		return nil, fmt.Errorf("invalid hex quantity %q", clip(s))
	}
	v, ok := new(big.Int).SetString(s[2:], 16)
	if !ok {
		return nil, fmt.Errorf("invalid hex quantity %q", clip(s))
	}
	return v, nil
}

// formatUint64 encodes n as a hex quantity.
func formatUint64(n uint64) string {
	return "0x" + strconv.FormatUint(n, 16)
}

// parseFixed decodes "0x" followed by exactly 2*n hex digits.
func parseFixed(s string, n int) ([]byte, error) {
	if !has0x(s) || len(s) != 2+2*n {
		return nil, fmt.Errorf("want 0x + %d hex digits, got %q", 2*n, clip(s))
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return nil, fmt.Errorf("invalid hex %q", clip(s))
	}
	return b, nil
}

// parseHash validates a 32-byte hash and returns it lower-cased.
func parseHash(s string) (string, error) {
	if _, err := parseFixed(s, 32); err != nil {
		return "", fmt.Errorf("invalid hash: %w", err)
	}
	return strings.ToLower(s), nil
}

// parseAddr decodes a 20-byte hex address (any case).
func parseAddr(s string) ([20]byte, error) {
	var a [20]byte
	b, err := parseFixed(s, 20)
	if err != nil {
		return a, fmt.Errorf("invalid address: %w", err)
	}
	copy(a[:], b)
	return a, nil
}

// parseData decodes arbitrary "0x"-prefixed hex data of even length.
func parseData(s string) ([]byte, error) {
	if !has0x(s) || len(s)%2 != 0 {
		return nil, fmt.Errorf("invalid hex data %q", clip(s))
	}
	b, err := hex.DecodeString(s[2:])
	if err != nil {
		return nil, fmt.Errorf("invalid hex data %q", clip(s))
	}
	return b, nil
}

// addrHex is the lower-case 0x form of a used in RPC filters.
func addrHex(a [20]byte) string {
	return "0x" + hex.EncodeToString(a[:])
}

// addrTopic left-pads a to a 32-byte log topic (lower-case).
func addrTopic(a [20]byte) string {
	return "0x000000000000000000000000" + hex.EncodeToString(a[:])
}
