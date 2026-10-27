package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"testing"
)

// This is a valid no-FCS, no-checksum frame with one raw block and a 1KiB
// advertised window. Unlike EncodeAll, it deliberately leaves FCS unknown.
func boundedLimitsUnknownFrame(raw []byte) []byte {
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0}
	block := uint32(len(raw))<<3 | 1
	frame = append(frame, byte(block), byte(block>>8), byte(block>>16))
	return append(frame, raw...)
}

func TestBoundedReaderLimitsOrdinaryParity(t *testing.T) {
	small := bytes.Repeat([]byte("s"), 600)
	known := boundedFixture(t, small, true, WithSingleSegment(true))
	unknown := boundedLimitsUnknownFrame(small)
	windowRaw := bytes.Repeat([]byte("window/"), 200)
	window := boundedFixture(t, windowRaw, true, WithSingleSegment(false), WithWindowSize(2048))
	var header Header
	if err := header.Decode(window); err != nil || header.SingleSegment || header.WindowSize != 2048 {
		t.Fatalf("window fixture: %+v, %v", header, err)
	}
	skip := make([]byte, 11)
	binary.LittleEndian.PutUint32(skip, 0x184d2a50)
	binary.LittleEndian.PutUint32(skip[4:], 3)
	copy(skip[8:], "pad")
	concat := func(a, b []byte) []byte {
		result := append([]byte(nil), a...)
		result = append(result, skip...)
		return append(result, b...)
	}
	badCRC := append([]byte(nil), known...)
	badCRC[len(badCRC)-1] ^= 1
	dictionary := bytes.Repeat([]byte("original borrowed dictionary content/"), 100)
	dictRaw := append(append([]byte(nil), dictionary[len(dictionary)-500:]...), small...)
	dictFrame := boundedFixture(t, dictRaw, true, WithSingleSegment(false), WithWindowSize(2048), WithEncoderDictRaw(73, dictionary))
	dict := BorrowedDictionary{ID: 73, Data: dictionary, Raw: true, Owner: &dictionary}
	tests := []struct {
		name           string
		wire, want     []byte
		memory, window uint64
		wantErr        error
		dict           BorrowedDictionary
	}{
		{name: "known-boundary", wire: known, want: small, memory: 1024, window: 1024},
		{name: "unknown-boundary", wire: boundedLimitsUnknownFrame(bytes.Repeat([]byte("u"), 1024)), want: bytes.Repeat([]byte("u"), 1024), memory: 1024, window: 1024},
		{name: "window-boundary", wire: window, want: windowRaw, memory: 2048, window: 2048},
		{name: "window-too-large", wire: window, memory: 4096, window: 1024, wantErr: ErrWindowSizeExceeded},
		{name: "window-exceeds-memory", wire: window, memory: 1024, window: 2048, wantErr: ErrWindowSizeExceeded},
		{name: "single-segment-minimum", wire: known, memory: 1023, window: 2048, wantErr: ErrDecoderSizeExceeded},
		{name: "single-segment-window", wire: boundedFixture(t, windowRaw, true, WithSingleSegment(true)), memory: 4096, window: 1024, wantErr: ErrWindowSizeExceeded},
		{name: "known-aggregate", wire: concat(known, known), memory: 1024, window: 2048, wantErr: ErrDecoderSizeExceeded},
		{name: "unknown-aggregate", wire: concat(unknown, unknown), memory: 1024, window: 2048, wantErr: ErrDecoderSizeExceeded},
		{name: "later-frame-window", wire: concat(known, window), memory: 4096, window: 1024, wantErr: ErrWindowSizeExceeded},
		{name: "concat-boundary", wire: concat(known, known), want: append(append([]byte(nil), small...), small...), memory: 1200, window: 2048},
		{name: "checksum", wire: badCRC, memory: 2048, window: 2048, wantErr: ErrCRCMismatch},
		{name: "borrowed-dictionary", wire: dictFrame, want: dictRaw, memory: 4096, window: 2048, dict: dict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := []DOption{WithDecoderConcurrency(1), WithDecoderLowmem(true), WithDecoderMaxMemory(tc.memory), WithDecoderMaxWindow(tc.window)}
			if tc.dict.Raw {
				opts = append(opts, WithDecoderDictRaw(tc.dict.ID, tc.dict.Data))
			}
			ordinary, err := NewReader(nil, opts...)
			if err != nil {
				t.Fatal(err)
			}
			oracle, oracleErr := ordinary.DecodeAll(tc.wire, nil)
			ordinary.Close()
			if !errors.Is(oracleErr, tc.wantErr) || (tc.wantErr == nil && !bytes.Equal(oracle, tc.want)) {
				t.Fatalf("ordinary fixture: output=%d err=%v, want output=%d err=%v", len(oracle), oracleErr, len(tc.want), tc.wantErr)
			}
			for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
				for _, fed := range []bool{false, true} {
					t.Run(fmt.Sprintf("mode%d/fed%v", mode, fed), func(t *testing.T) {
						w := &boundedTestWork{limit: 32 << 20}
						w.quantum()
						d, ok, err := NewBoundedReaderWithLimits(w, tc.memory, tc.window)
						if !ok || err != nil {
							t.Fatal(ok, err)
						}
						defer closeBoundedForTest(t, &d, w)
						for attempt := 0; attempt < 2; attempt++ {
							got, err := multiFrameOutcome(t, d, w, tc.wire, tc.dict, mode, fed, attempt != 0)
							if !errors.Is(err, oracleErr) || (err == nil && !bytes.Equal(got, oracle)) {
								t.Fatalf("attempt%d output=%d err=%v; ordinary output=%d err=%v", attempt, len(got), err, len(oracle), oracleErr)
							}
						}
					})
				}
			}
		})
	}
}

func TestBoundedReaderLimitsConstructionAndLegacyDefault(t *testing.T) {
	for _, tc := range []struct {
		memory, window uint64
		want           error
	}{
		{0, 1024, ErrDecoderSizeExceeded}, {1<<63 + 1, 1024, ErrDecoderSizeExceeded},
		{1024, 1023, ErrWindowSizeExceeded}, {1024, (1 << 41) + 7*(1<<38) + 1, ErrWindowSizeExceeded},
	} {
		w := &boundedTestWork{limit: 32 << 20}
		w.quantum()
		d, ok, err := NewBoundedReaderWithLimits(w, tc.memory, tc.window)
		if d != nil || ok || !errors.Is(err, tc.want) || w.totalR != 0 || w.totalB != 0 || w.retained != 0 {
			t.Fatalf("invalid configuration admitted: %d/%d d=%v ready=%v err=%v work=%d/%d retained=%d", tc.memory, tc.window, d, ok, err, w.totalR, w.totalB, w.retained)
		}
	}
	// The existing constructor continues accepting a frame above the explicit
	// limit. No existing bounded reader silently gains a new window policy.
	raw := bytes.Repeat([]byte("legacy window/"), 100)
	frame := boundedFixture(t, raw, true, WithSingleSegment(false), WithWindowSize(2048))
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, err := NewBoundedReader(w)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	got, err := multiFrameOutcome(t, d, w, frame, BorrowedDictionary{}, BoundedRaw, false, false)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatal("legacy policy changed", err)
	}
	closeBoundedForTest(t, &d, w)
	// Both public valid extrema can create a cold reader; neither eagerly
	// allocates a window. Work refusal and retained-capacity refusal stay distinct.
	for _, memory := range []uint64{1, 1 << 63} {
		w := &boundedTestWork{limit: 32 << 20}
		w.quantum()
		d, ok, err := NewBoundedReaderWithLimits(w, memory, (1<<41)+7*(1<<38))
		if !ok || err != nil {
			t.Fatal(ok, err)
		}
		closeBoundedForTest(t, &d, w)
	}
	w = &boundedTestWork{limit: 32 << 20, refuse: true}
	w.quantum()
	d, ok, err = NewBoundedReaderWithLimits(w, 1024, 2048)
	if d != nil || ok || err != nil || w.retained != 0 {
		t.Fatal("work refusal", d, ok, err, w.retained)
	}
	w = &boundedTestWork{limit: 1}
	w.quantum()
	d, ok, err = NewBoundedReaderWithLimits(w, 1024, 2048)
	if d != nil || ok || !errors.Is(err, ErrWorkRefused) || w.retained != 0 {
		t.Fatal("capacity refusal", d, ok, err, w.retained)
	}
}
