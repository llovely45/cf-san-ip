package prefix

import "testing"

func TestSortAndPendingBlocksUseNumericAddressOrder(t *testing.T) {
	blocks := []Block{
		{SourcePrefix: "8.8.0.0/23", BlockCIDR: "8.8.1.0/24", Status: "active"},
		{SourcePrefix: "1.0.0.0/8", BlockCIDR: "1.0.0.0/24", Status: "completed"},
		{SourcePrefix: "2.0.0.0/8", BlockCIDR: "2.0.0.0/24", Status: "active"},
	}
	pending, err := PendingBlocks(64512, blocks)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].BlockCIDR != "2.0.0.0/24" || pending[1].BlockCIDR != "8.8.1.0/24" {
		t.Fatalf("PendingBlocks() = %#v, want numeric ascending incomplete blocks", pending)
	}
}

func TestPendingBlocksRejectsPrefixOutsideSourceOrDifferentASN(t *testing.T) {
	tests := []struct {
		name  string
		asn   uint32
		block Block
	}{
		{name: "block outside source", asn: 64512, block: Block{SourcePrefix: "8.8.8.0/24", BlockCIDR: "8.8.9.0/24", Status: "active"}},
		{name: "mismatched ASN", asn: 64512, block: Block{ASN: 64513, SourcePrefix: "8.8.8.0/24", BlockCIDR: "8.8.8.0/24", Status: "active"}},
		{name: "non IPv4", asn: 64512, block: Block{SourcePrefix: "2001:db8::/32", BlockCIDR: "2001:db8::/32", Status: "active"}},
		{name: "invalid state", asn: 64512, block: Block{SourcePrefix: "8.8.8.0/24", BlockCIDR: "8.8.8.0/24", Status: "unknown"}},
		{name: "private block", asn: 64512, block: Block{SourcePrefix: "10.0.0.0/8", BlockCIDR: "10.0.0.0/24", Status: "active"}},
		{name: "mixed public and private source", asn: 64512, block: Block{SourcePrefix: "8.0.0.0/5", BlockCIDR: "8.0.0.0/24", Status: "active"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := PendingBlocks(tt.asn, []Block{tt.block}); err == nil {
				t.Fatal("PendingBlocks() error = nil, want validation error")
			}
		})
	}
}
