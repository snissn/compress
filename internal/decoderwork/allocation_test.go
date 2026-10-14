package decoderwork

import (
	"math"
	"testing"
)

func TestAllocationChargeEnclosingAllocations(t *testing.T) {
	if AllocationCharge(0) != 0 || AllocationCharge(math.MaxUint64) != math.MaxUint64 || AllocationCharge(uint64(math.MaxInt64)) != math.MaxUint64 {
		t.Fatal("overflow/zero contract")
	}
	for _, requested := range []uint64{1, 16, 264, 512, 4096, 8249, 32768, 32800, 58208, 131090} {
		admitted := AllocationCharge(requested)
		if admitted < requested || admitted&(admitted-1) != 0 && admitted%8192 != 0 {
			t.Fatalf("invalid admission %d -> %d", requested, admitted)
		}
	}
	// Two actual allocations cannot share the unused tail of one allocator unit.
	if AllocationCharge(264)+AllocationCharge(58208) != 66048 {
		t.Fatal("separate containing allocation admission")
	}
}
