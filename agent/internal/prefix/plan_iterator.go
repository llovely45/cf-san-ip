package prefix

import (
	"container/heap"
	"fmt"
	"net/netip"
	"sort"
)

type Checkpoint struct {
	SourcePrefix string  `json:"sourcePrefix"`
	BlockCIDR    string  `json:"blockCidr"`
	Status       string  `json:"status"`
	LatestIP     *string `json:"latestIp"`
}

type planPrefix struct {
	source   string
	prefix   netip.Prefix
	blockBit int
	start    uint64
	end      uint64
	step     uint64
}

// Plan keeps source prefixes and sparse checkpoints, not one object per /24.
// Large ASNs therefore have bounded plan memory regardless of their block count.
type Plan struct {
	asn      uint32
	prefixes []planPrefix
	cursor   *Checkpoint
	legacy   []Block
}

func NewPlan(asn uint32, prefixes []string, checkpoints []Checkpoint) (*Plan, error) {
	if asn == 0 {
		return nil, fmt.Errorf("ASN must be positive")
	}
	unique := make(map[string]planPrefix, len(prefixes))
	for _, value := range prefixes {
		parsed, err := netip.ParsePrefix(value)
		if err != nil || !parsed.Addr().Is4() || parsed != parsed.Masked() {
			return nil, fmt.Errorf("plan contains an invalid IPv4 source prefix")
		}
		if _, err := validateBlock(Block{SourcePrefix: value, BlockCIDR: value, Status: "active"}); err != nil {
			return nil, err
		}
		canonical := parsed.String()
		if canonical != value {
			return nil, fmt.Errorf("plan contains a non-canonical source prefix")
		}
		start, end := ipv4Range(parsed)
		blockBit := max(parsed.Bits(), 24)
		step := uint64(1) << (32 - blockBit)
		unique[value] = planPrefix{
			source: value, prefix: parsed, blockBit: blockBit,
			start: start, end: end, step: step,
		}
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("backend returned an empty scan plan")
	}

	ordered := make([]planPrefix, 0, len(unique))
	for _, source := range unique {
		ordered = append(ordered, source)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].start != ordered[j].start {
			return ordered[i].start < ordered[j].start
		}
		if ordered[i].prefix.Bits() != ordered[j].prefix.Bits() {
			return ordered[i].prefix.Bits() < ordered[j].prefix.Bits()
		}
		return ordered[i].source < ordered[j].source
	})
	prefixBySource := make(map[string]int, len(ordered))
	for index := range ordered {
		prefixBySource[ordered[index].source] = index
	}

	var cursor *Checkpoint
	var cursorNetwork uint64
	cursorBits := -1
	seenCheckpoints := make(map[string]struct{}, len(checkpoints))
	for _, checkpoint := range checkpoints {
		index, exists := prefixBySource[checkpoint.SourcePrefix]
		if !exists {
			return nil, fmt.Errorf("checkpoint references a source prefix outside the current plan")
		}
		if _, duplicate := seenCheckpoints[checkpoint.SourcePrefix]; duplicate {
			return nil, fmt.Errorf("plan contains a duplicate source-prefix checkpoint")
		}
		seenCheckpoints[checkpoint.SourcePrefix] = struct{}{}
		block, err := validateBlock(Block{
			SourcePrefix: checkpoint.SourcePrefix,
			BlockCIDR:    checkpoint.BlockCIDR,
			Status:       checkpoint.Status,
		})
		if err != nil {
			return nil, err
		}
		if block.Bits() != ordered[index].blockBit || block != block.Masked() {
			return nil, fmt.Errorf("checkpoint contains an invalid scan block")
		}
		if checkpoint.LatestIP != nil {
			address, parseErr := netip.ParseAddr(*checkpoint.LatestIP)
			if parseErr != nil || !address.Is4() || !block.Contains(address) {
				return nil, fmt.Errorf("checkpoint contains an invalid latest IP")
			}
		}
		checkpointNetwork, _ := ipv4Range(block)
		if cursor == nil || comparePlanPosition(checkpointNetwork, block.Bits(), cursorNetwork, cursorBits) > 0 {
			copy := checkpoint
			cursor = &copy
			cursorNetwork = checkpointNetwork
			cursorBits = block.Bits()
		}
	}

	plan := &Plan{asn: asn, prefixes: ordered, cursor: cursor}
	for index := range plan.prefixes {
		source := &plan.prefixes[index]
		if cursor == nil || source.start >= cursorNetwork {
			continue
		}
		distance := cursorNetwork - source.start
		steps := (distance + source.step - 1) / source.step
		source.start += steps * source.step
	}
	return plan, nil
}

// comparePlanPosition orders blocks the same way as the legacy expanded plan:
// first by numeric network address, then by prefix length.
func comparePlanPosition(leftNetwork uint64, leftBits int, rightNetwork uint64, rightBits int) int {
	if leftNetwork < rightNetwork || (leftNetwork == rightNetwork && leftBits < rightBits) {
		return -1
	}
	if leftNetwork > rightNetwork || (leftNetwork == rightNetwork && leftBits > rightBits) {
		return 1
	}
	return 0
}

func NewPlanFromBlocks(asn uint32, blocks []Block) (*Plan, error) {
	ordered, err := ValidatePlan(asn, blocks)
	if err != nil {
		return nil, err
	}
	return &Plan{asn: asn, legacy: ordered}, nil
}

func (p *Plan) ASN() uint32 {
	if p == nil {
		return 0
	}
	return p.asn
}

func (p *Plan) PrefixCount() int {
	if p == nil {
		return 0
	}
	if p.legacy != nil {
		seen := make(map[string]struct{}, len(p.legacy))
		for _, block := range p.legacy {
			seen[block.SourcePrefix] = struct{}{}
		}
		return len(seen)
	}
	return len(p.prefixes)
}

func (p *Plan) ForEachPending(visit func(Block) error) error {
	if p == nil || visit == nil {
		return fmt.Errorf("scan plan and visitor are required")
	}
	if p.legacy != nil {
		for _, block := range p.legacy {
			if block.Status == "completed" {
				continue
			}
			if err := visit(block); err != nil {
				return err
			}
		}
		return nil
	}

	nextNetworks := make([]uint64, len(p.prefixes))
	items := make(planMinHeap, 0, len(p.prefixes))
	for index, source := range p.prefixes {
		nextNetworks[index] = source.start
		if item, ok := nextPlanItem(index, p.prefixes, nextNetworks); ok {
			items = append(items, item)
		}
	}
	heap.Init(&items)
	for items.Len() > 0 {
		first := heap.Pop(&items).(planItem)
		group := []planItem{first}
		for items.Len() > 0 && sameBlock(items[0], first) {
			group = append(group, heap.Pop(&items).(planItem))
		}
		for _, item := range group {
			if next, ok := nextPlanItem(item.prefixIndex, p.prefixes, nextNetworks); ok {
				heap.Push(&items, next)
			}
		}

		block := Block{
			ASN:          p.asn,
			SourcePrefix: first.source,
			BlockCIDR:    formatIPv4Block(first.network, first.blockBit),
			Status:       "active",
		}
		if p.cursor != nil {
			cursorBlock, _ := netip.ParsePrefix(p.cursor.BlockCIDR)
			cursorNetwork, _ := ipv4Range(cursorBlock)
			position := comparePlanPosition(first.network, first.blockBit, cursorNetwork, cursorBlock.Bits())
			if position < 0 || (position == 0 && p.cursor.Status == "completed") {
				continue
			}
			if position == 0 && p.cursor.Status == "active" {
				if p.cursor.SourcePrefix != block.SourcePrefix || p.cursor.BlockCIDR != block.BlockCIDR {
					return fmt.Errorf("active checkpoint does not match the current plan's block ownership")
				}
				block.LatestIP = p.cursor.LatestIP
			}
		}
		if err := visit(block); err != nil {
			return err
		}
	}
	return nil
}

func (p *Plan) CountPending() (uint64, error) {
	var count uint64
	err := p.ForEachPending(func(Block) error {
		count++
		return nil
	})
	return count, err
}

type planItem struct {
	prefixIndex int
	network     uint64
	blockBit    int
	source      string
	sourceBit   int
}

type planMinHeap []planItem

func (h planMinHeap) Len() int { return len(h) }
func (h planMinHeap) Less(i, j int) bool {
	if h[i].network != h[j].network {
		return h[i].network < h[j].network
	}
	if h[i].blockBit != h[j].blockBit {
		return h[i].blockBit < h[j].blockBit
	}
	if h[i].sourceBit != h[j].sourceBit {
		return h[i].sourceBit > h[j].sourceBit
	}
	return h[i].source < h[j].source
}
func (h planMinHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *planMinHeap) Push(value any) { *h = append(*h, value.(planItem)) }
func (h *planMinHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func nextPlanItem(index int, prefixes []planPrefix, nextNetworks []uint64) (planItem, bool) {
	source := prefixes[index]
	network := nextNetworks[index]
	if network > source.end {
		return planItem{}, false
	}
	nextNetworks[index] = network + source.step
	return planItem{
		prefixIndex: index,
		network:     network,
		blockBit:    source.blockBit,
		source:      source.source,
		sourceBit:   source.prefix.Bits(),
	}, true
}

func sameBlock(left, right planItem) bool {
	return left.network == right.network && left.blockBit == right.blockBit
}

func formatIPv4Block(network uint64, bits int) string {
	address := netip.AddrFrom4([4]byte{
		byte(network >> 24),
		byte(network >> 16),
		byte(network >> 8),
		byte(network),
	})
	return netip.PrefixFrom(address, bits).String()
}
