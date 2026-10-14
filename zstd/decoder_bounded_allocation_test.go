package zstd

import (
	"bytes"
	"github.com/snissn/compress/internal/decoderwork"
	"os"
	"runtime"
	"testing"
	"unsafe"
)

// Run this witness in its own process. It measures the constructor interval,
// not a rounded process total or the ordinary encoder/oracle setup.
func TestBoundedColdConstructorAllocationWitness(t *testing.T) {
	if os.Getenv("BOUNDED_COLD_ALLOCATION") != "1" {
		t.Skip("fresh-process witness")
	}
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	d, ok, err := NewBoundedReader(w)
	runtime.ReadMemStats(&after)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	requested := uint64(unsafe.Sizeof(Decoder{})) + uint64(unsafe.Sizeof(boundedDecoder{}))
	admitted := decoderwork.AllocationCharge(uint64(unsafe.Sizeof(Decoder{}))) + decoderwork.AllocationCharge(uint64(unsafe.Sizeof(boundedDecoder{})))
	allocated, allocations := after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs
	if allocations != 2 || allocated < requested || allocated > admitted || w.retained != admitted || w.usedB != admitted || w.usedR != 1 {
		t.Fatalf("constructor: requested=%d actualAllocated=%d allocations=%d admitted=%d R=%d B=%d", requested, allocated, allocations, w.retained, w.usedR, w.usedB)
	}
	t.Logf("requestedContaining=%d conservativeAdmission=%d Decoder=%d continuation=%d allocatorAllocatedDelta=%d allocationCount=%d R=%d B=%d; fixed Decoder remains caller reachable after CloseStep", requested, admitted, unsafe.Sizeof(Decoder{}), unsafe.Sizeof(boundedDecoder{}), allocated, allocations, w.usedR, w.usedB)
	closeBoundedForTest(t, &d, w)
}

func TestBoundedMalformedErrorAllocations(t *testing.T) {
	dictionary := make([]byte, 21)
	copy(dictionary, dictMagic)
	cases := []struct {
		name       string
		phase      int
		dictionary []byte
		in         []byte
		expected   error
	}{
		{"dictionary-zero-id", 1, dictionary, nil, errStepDictionaryZeroID},
		{"header-truncated", 2, nil, []byte{1}, nil},
		{"reserved-block", 7, nil, []byte{7, 0, 0}, ErrReservedBlockType},
		{"literal-truncated", 8, nil, []byte{3}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := &boundedTestWork{limit: 32 << 20}
			w.quantum()
			d, ok, err := NewBoundedReader(w)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			b := d.bounded
			b.begun = true
			b.output = BoundedRaw
			b.phase = c.phase
			b.dictionary = BorrowedDictionary{Data: c.dictionary, Owner: &dictionary}
			b.in = c.in
			b.compressed = c.in
			b.hist.windowSize = 1 << 20
			var first, result error
			allocations := testing.AllocsPerRun(100, func() {
				// Restore only malformed parser inputs, never Scratch's sync.Pool.
				// Production uses charged ResetStep.
				b.phase, b.dictPhase = c.phase, 0
				b.failed, b.err = false, nil
				b.in, b.compressed = c.in, c.in
				b.headerState = HeaderState{}
				w.quantum()
				_, _, result = d.Step(nil)
				if first == nil {
					first = result
				}
			})
			if result == nil || result != first || (c.expected != nil && result != c.expected) || allocations != 0 {
				t.Fatalf("error=%v sameStatic=%v allocations=%g", result, result == first, allocations)
			}
			t.Logf("returned=%q allocations=%g R=%d B=%d; static package error has no per-operation capacity refund", result, allocations, w.usedR, w.usedB)
			closeBoundedForTest(t, &d, w)
		})
	}
}

// N/2N uses the original encoder and no growing test output collector. Oracle
// input and spans are constructed before the measured decoder interval.
func TestBoundedNecessaryHistoryN2N(t *testing.T) {
	for _, size := range []int{1 << 20, 2 << 20} {
		raw := make([]byte, size)
		x := uint32(1)
		for i := range raw {
			x ^= x << 13
			x ^= x >> 17
			x ^= x << 5
			raw[i] = byte(x & 63)
			if i%2048 >= 1024 {
				raw[i] = raw[i-1024]
			}
		}
		frame := boundedFixture(t, raw, true)
		span := make([]byte, 4096)
		w := &boundedTestWork{limit: 32 << 20}
		w.quantum()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		d, ok, err := NewBoundedReader(w)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		if ok, err = d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{}); err != nil || !ok {
			t.Fatal(ok, err)
		}
		produced, calls := 0, 0
		for ; calls < 200000; calls++ {
			w.quantum()
			n, done, err := d.Step(span)
			if err != nil {
				t.Fatal(err)
			}
			if produced+n > len(raw) || !bytes.Equal(span[:n], raw[produced:produced+n]) {
				t.Fatal("original producer output")
			}
			produced += n
			if done {
				break
			}
		}
		if produced != size || calls == 200000 {
			t.Fatal("incomplete", produced, calls)
		}
		runtime.ReadMemStats(&after)
		t.Logf("N=%d stored=%d calls=%d R=%d B=%d peakConservative=%d observedDecoderAllocated=%d observedDecoderAllocations=%d; original immutable input/oracle/spans excluded; linked history lookup probes included", size, len(frame), calls+1, w.totalR, w.totalB, w.peak, after.TotalAlloc-before.TotalAlloc, after.Mallocs-before.Mallocs)
		closeBoundedForTest(t, &d, w)
	}
}
