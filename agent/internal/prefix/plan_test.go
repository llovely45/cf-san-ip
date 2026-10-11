package prefix

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
)

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

func TestCompactPlanUsesAtMost16BlocksAndMigratesLegacyCheckpoint(t *testing.T) {
	plan, err := NewPlan(64512, []string{"8.8.0.0/23"}, []Checkpoint{{
		SourcePrefix: "8.8.0.0/23",
		BlockCIDR:    "8.8.0.0/24",
		Status:       "completed",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var got []Block
	if err := plan.ForEachPending(func(block Block) error {
		got = append(got, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BlockCIDR != "8.8.0.0/23" || got[0].Status != "active" {
		t.Fatalf("pending blocks = %#v, want the legacy /24 checkpoint's /23 parent rescanned", got)
	}

	largePlan, err := NewPlan(64512, []string{"8.8.0.0/14"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var large []Block
	if err := largePlan.ForEachPending(func(block Block) error {
		large = append(large, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.0.0/16", "8.9.0.0/16", "8.10.0.0/16", "8.11.0.0/16"}
	if len(large) != len(want) {
		t.Fatalf("/14 plan has %d blocks, want %d: %#v", len(large), len(want), large)
	}
	for index, block := range large {
		if block.BlockCIDR != want[index] {
			t.Errorf("block %d = %s, want %s", index, block.BlockCIDR, want[index])
		}
	}
}

func TestCompactPlanMigratesLegacyCheckpointIntoContainingSlash16(t *testing.T) {
	latestIP := "8.9.44.12"
	plan, err := NewPlan(64512, []string{"8.8.0.0/14"}, []Checkpoint{{
		SourcePrefix: "8.8.0.0/14",
		BlockCIDR:    "8.9.44.0/24",
		Status:       "completed",
		LatestIP:     &latestIP,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var got []Block
	if err := plan.ForEachPending(func(block Block) error {
		got = append(got, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"8.9.0.0/16", "8.10.0.0/16", "8.11.0.0/16"}
	if len(got) != len(want) {
		t.Fatalf("pending blocks = %#v, want %#v", got, want)
	}
	for index, block := range got {
		if block.BlockCIDR != want[index] {
			t.Errorf("block %d = %s, want %s", index, block.BlockCIDR, want[index])
		}
	}
	if got[0].Status != "active" || got[0].LatestIP == nil || *got[0].LatestIP != latestIP {
		t.Fatalf("migrated checkpoint = %#v, want active containing /16 with latest IP retained", got[0])
	}
}

func TestCompactPlanKeepsPrefixesNarrowerThanSlash16Intact(t *testing.T) {
	plan, err := NewPlan(64512, []string{"8.8.0.0/17", "1.0.0.0/25"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Block
	if err := plan.ForEachPending(func(block Block) error {
		got = append(got, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"1.0.0.0/25", "8.8.0.0/17"}
	if len(got) != len(want) {
		t.Fatalf("pending blocks = %#v, want %#v", got, want)
	}
	for index, block := range got {
		if block.BlockCIDR != want[index] {
			t.Errorf("block %d = %s, want %s", index, block.BlockCIDR, want[index])
		}
	}
}

func TestCompactPlanSkipsPrefixesCoveredByBroaderRoutes(t *testing.T) {
	plan, err := NewPlan(64512, []string{"8.8.0.0/23", "8.8.1.0/24"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []Block
	if err := plan.ForEachPending(func(block Block) error {
		got = append(got, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SourcePrefix != "8.8.0.0/23" || got[0].BlockCIDR != "8.8.0.0/23" {
		t.Fatalf("overlap plan = %#v, want the broader route scanned once", got)
	}
}

func TestCompactPlanResumesAfterGlobalCheckpointAcrossOverlappingPrefixes(t *testing.T) {
	completedPlan, err := NewPlan(64512,
		[]string{"8.8.0.0/23", "8.8.1.0/24"},
		[]Checkpoint{
			{SourcePrefix: "8.8.0.0/23", BlockCIDR: "8.8.0.0/24", Status: "completed"},
			{SourcePrefix: "8.8.1.0/24", BlockCIDR: "8.8.1.0/24", Status: "completed"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var completedPending []Block
	if err := completedPlan.ForEachPending(func(block Block) error {
		completedPending = append(completedPending, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(completedPending) != 1 || completedPending[0].BlockCIDR != "8.8.0.0/23" || completedPending[0].Status != "active" {
		t.Fatalf("pending blocks after legacy overlap checkpoints = %#v, want one conservative /23 rescan", completedPending)
	}

	activePlan, err := NewPlan(64512,
		[]string{"8.8.0.0/23", "8.8.1.0/24"},
		[]Checkpoint{
			{SourcePrefix: "8.8.0.0/23", BlockCIDR: "8.8.0.0/24", Status: "completed"},
			{SourcePrefix: "8.8.1.0/24", BlockCIDR: "8.8.1.0/24", Status: "active"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var activePending []Block
	if err := activePlan.ForEachPending(func(block Block) error {
		activePending = append(activePending, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(activePending) != 1 || activePending[0].SourcePrefix != "8.8.0.0/23" || activePending[0].BlockCIDR != "8.8.0.0/23" {
		t.Fatalf("pending blocks after active overlap = %#v, want the containing /23 rescanned", activePending)
	}
}

func TestCompactPlanPrefersExactNewCheckpointOverStaleLegacyCheckpoint(t *testing.T) {
	plan, err := NewPlan(64512,
		[]string{"8.8.0.0/23", "8.8.1.0/24"},
		[]Checkpoint{
			{SourcePrefix: "8.8.0.0/23", BlockCIDR: "8.8.0.0/23", Status: "completed"},
			{SourcePrefix: "8.8.1.0/24", BlockCIDR: "8.8.1.0/24", Status: "completed"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var got []Block
	if err := plan.ForEachPending(func(block Block) error {
		got = append(got, block)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pending blocks = %#v, want exact completed /23 checkpoint to suppress stale child checkpoint", got)
	}
}

func TestCompactPlanStreamsFullAS16509SizedRangeAndResumesWithoutTruncation(t *testing.T) {
	prefixes := make([]string, 0, 194)
	for index := 0; index < 10; index++ {
		prefixes = append(prefixes, fmt.Sprintf("%d.0.0.0/8", 11+index))
	}
	for index := 0; index < 26; index++ {
		prefixes = append(prefixes, fmt.Sprintf("21.%d.0.0/16", index))
	}
	for index := 0; index < 153; index++ {
		thirdOctet := 0
		if index%2 != 0 {
			thirdOctet = 128
		}
		prefixes = append(prefixes, fmt.Sprintf("22.%d.%d.0/17", index/2, thirdOctet))
	}
	for index := 0; index < 5; index++ {
		prefixes = append(prefixes, fmt.Sprintf("23.0.%d.0/24", index))
	}

	plan, err := NewPlan(64512, prefixes, nil)
	if err != nil {
		t.Fatal(err)
	}
	count, err := plan.CountPending()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2_744 {
		t.Fatalf("CountPending() = %d, want the complete 2744-block range", count)
	}

	const checkpointIndex = uint64(1_234)
	var checkpoint *Block
	var position uint64
	if err := plan.ForEachPending(func(block Block) error {
		if position == checkpointIndex {
			copy := block
			checkpoint = &copy
		}
		position++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if checkpoint == nil {
		t.Fatalf("block %d was not generated", checkpointIndex)
	}

	resumed, err := NewPlan(64512, prefixes, []Checkpoint{{
		SourcePrefix: checkpoint.SourcePrefix,
		BlockCIDR:    checkpoint.BlockCIDR,
		Status:       "completed",
	}})
	if err != nil {
		t.Fatal(err)
	}
	remaining, err := resumed.CountPending()
	if err != nil {
		t.Fatal(err)
	}
	if remaining != count-checkpointIndex-1 {
		t.Fatalf("remaining blocks = %d, want %d after resume", remaining, count-checkpointIndex-1)
	}
	var first Block
	if err := resumed.ForEachPending(func(block Block) error {
		first = block
		return errStopAfterFirstBlock
	}); err != errStopAfterFirstBlock {
		t.Fatalf("ForEachPending() error = %v, want early-stop sentinel", err)
	}
	firstPosition := blockPosition(first)
	checkpointPosition := blockPosition(*checkpoint)
	if comparePlanPosition(firstPosition.network, firstPosition.bits, checkpointPosition.network, checkpointPosition.bits) <= 0 {
		t.Fatalf("first resumed block %s is not after checkpoint %s", first.BlockCIDR, checkpoint.BlockCIDR)
	}
}

var errStopAfterFirstBlock = errors.New("stop after first generated block")

type orderedBlockPosition struct {
	network uint64
	bits    int
}

func blockPosition(block Block) orderedBlockPosition {
	prefix, _ := netip.ParsePrefix(block.BlockCIDR)
	network, _ := ipv4Range(prefix)
	return orderedBlockPosition{network: network, bits: prefix.Bits()}
}

func TestCompactPlanCountsLargeRangesWithoutMaterializingBlocks(t *testing.T) {
	plan, err := NewPlan(64512, []string{"8.8.0.0/14"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	count, err := plan.CountPending()
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Fatalf("CountPending() = %d, want 4 /16 blocks", count)
	}
}

func TestCompactPlanRejectsInvalidCheckpoints(t *testing.T) {
	_, err := NewPlan(64512, []string{"8.8.0.0/23"}, []Checkpoint{{
		SourcePrefix: "8.8.0.0/23",
		BlockCIDR:    "8.8.8.0/24",
		Status:       "completed",
	}})
	if err == nil {
		t.Fatal("NewPlan() error = nil, want out-of-source checkpoint error")
	}
}
