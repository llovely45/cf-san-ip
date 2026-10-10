package prefix

import (
	"fmt"
	"net/netip"
	"sort"
)

type Block struct {
	ASN          uint32  `json:"-"`
	SourcePrefix string  `json:"sourcePrefix"`
	BlockCIDR    string  `json:"blockCidr"`
	Status       string  `json:"status"`
	LatestIP     *string `json:"latestIp"`
}

func SortBlocks(blocks []Block) error {
	for _, block := range blocks {
		if _, err := validateBlock(block); err != nil {
			return err
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		left, _ := netip.ParsePrefix(blocks[i].BlockCIDR)
		right, _ := netip.ParsePrefix(blocks[j].BlockCIDR)
		if left.Masked().Addr() != right.Masked().Addr() {
			return left.Masked().Addr().Less(right.Masked().Addr())
		}
		return left.Bits() < right.Bits()
	})
	return nil
}

func ValidatePlan(asn uint32, blocks []Block) ([]Block, error) {
	ordered := append([]Block(nil), blocks...)
	seen := make(map[string]struct{}, len(ordered))
	for i := range ordered {
		if ordered[i].ASN != 0 && ordered[i].ASN != asn {
			return nil, fmt.Errorf("plan contains a block for a different ASN")
		}
		ordered[i].ASN = asn
		if _, err := validateBlock(ordered[i]); err != nil {
			return nil, err
		}
		if _, exists := seen[ordered[i].BlockCIDR]; exists {
			return nil, fmt.Errorf("plan contains a duplicate scan block")
		}
		seen[ordered[i].BlockCIDR] = struct{}{}
	}
	if err := SortBlocks(ordered); err != nil {
		return nil, err
	}
	return ordered, nil
}

func PendingBlocks(asn uint32, blocks []Block) ([]Block, error) {
	ordered, err := ValidatePlan(asn, blocks)
	if err != nil {
		return nil, err
	}
	pending := make([]Block, 0, len(ordered))
	for _, block := range ordered {
		if block.Status != "completed" {
			pending = append(pending, block)
		}
	}
	return pending, nil
}

var nonPublicIPv4Prefixes = mustParsePrefixes([]string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
	"192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
	"203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
})

func mustParsePrefixes(values []string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			panic("invalid built-in non-public prefix")
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes
}

func overlapsNonPublic(prefix netip.Prefix) bool {
	prefix = prefix.Masked()
	low, high := ipv4Range(prefix)
	for _, excluded := range nonPublicIPv4Prefixes {
		excludedLow, excludedHigh := ipv4Range(excluded)
		if low <= excludedHigh && excludedLow <= high {
			return true
		}
	}
	return false
}

func ipv4Range(prefix netip.Prefix) (uint64, uint64) {
	address := prefix.Addr().As4()
	value := uint64(address[0])<<24 | uint64(address[1])<<16 | uint64(address[2])<<8 | uint64(address[3])
	hostBits := 32 - prefix.Bits()
	if hostBits == 32 {
		return 0, 0xffffffff
	}
	mask := uint64(0xffffffff) << hostBits & 0xffffffff
	low := value & mask
	return low, low | (0xffffffff ^ mask)
}

func validateBlock(block Block) (netip.Prefix, error) {
	if block.Status != "active" && block.Status != "completed" {
		return netip.Prefix{}, fmt.Errorf("plan contains an invalid block state")
	}
	source, err := netip.ParsePrefix(block.SourcePrefix)
	if err != nil || !source.Addr().Is4() || source != source.Masked() {
		return netip.Prefix{}, fmt.Errorf("plan contains an invalid IPv4 source prefix")
	}
	blockPrefix, err := netip.ParsePrefix(block.BlockCIDR)
	if err != nil || !blockPrefix.Addr().Is4() || blockPrefix != blockPrefix.Masked() {
		return netip.Prefix{}, fmt.Errorf("plan contains an invalid IPv4 scan block")
	}
	if source.Bits() > blockPrefix.Bits() || !source.Contains(blockPrefix.Addr()) {
		return netip.Prefix{}, fmt.Errorf("scan block is outside its source prefix")
	}
	if !source.Addr().IsGlobalUnicast() || !blockPrefix.Addr().IsGlobalUnicast() || overlapsNonPublic(source) || overlapsNonPublic(blockPrefix) {
		return netip.Prefix{}, fmt.Errorf("scan plan contains non-public IPv4 space")
	}
	return blockPrefix, nil
}
