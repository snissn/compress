package zstd

import (
	"bytes"
	"fmt"
	"testing"
)

// Pause the actual public decoder at the first raw-block emission. History
// admission still uses the original work/retention operands before effects.
type checksumSelectionWork struct {
	boundedTestWork
	stopOutput bool
}

func (w *checksumSelectionWork) Available() (uint64, uint64) {
	if w.stopOutput && w.observed != nil && w.observed.phase == 30 {
		return w.r, 0
	}
	return w.boundedTestWork.Available()
}

func TestBoundedChecksumSelection(t *testing.T) {
	raw := []byte{19, 2, 77, 201, 4, 91, 6, 151, 23}
	for _, crc := range []bool{false, true} {
		frame := boundedFixture(t, raw, crc)
		ordinary, err := NewReader(nil, WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		oracle, err := ordinary.DecodeAll(frame, nil)
		ordinary.Close()
		if err != nil || !bytes.Equal(oracle, raw) {
			t.Fatal("ordinary producer oracle", err)
		}
		for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
			for _, fed := range []bool{false, true} {
				t.Run(fmt.Sprintf("crc=%v/mode=%d/fed=%v", crc, mode, fed), func(t *testing.T) {
					w := &checksumSelectionWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, stopOutput: true}
					w.quantum()
					d, ok, err := NewBoundedReader(w)
					if err != nil || !ok {
						t.Fatal("constructor", ok, err)
					}
					w.observed = d.bounded
					defer closeBoundedForTest(t, &d, &w.boundedTestWork)
					initialHash := d.bounded.hash
					output := make([]byte, 0, len(raw))
					if fed {
						ok, err = d.BeginStream(&frame, mode, output, BorrowedDictionary{})
					} else {
						ok, err = d.Begin(frame, &frame, mode, output, BorrowedDictionary{})
					}
					if err != nil || !ok {
						t.Fatal("begin", ok, err)
					}
					span := make([]byte, len(raw))
					position := 0
					for call := 0; call < 1000 && (d.bounded.phase != 30 || (mode == BoundedSpans && d.bounded.segments.tail == nil)); call++ {
						w.quantum()
						if _, need := d.InputRequest(); need {
							n, _, err := d.FeedInput(frame[position:], true)
							if err != nil {
								t.Fatal("feed", err)
							}
							position += n
						} else {
							n, done, err := d.Step(span)
							if err != nil || done || n != 0 {
								t.Fatal("pause before original emission", n, done, err)
							}
						}
					}
					if d.bounded.phase != 30 || d.bounded.blockType != blockTypeRaw || d.bounded.header.HasCheckSum != crc {
						t.Fatal("actual producer did not reach the expected raw block")
					}
					before := d.bounded.hash
					w.stopOutput = false
					w.quantum()
					// One actual copy in Raw; history and output copies in Spans.
					w.r, w.b = 1, 2*uint64(len(raw))
					if mode == BoundedSpans {
						w.r, w.b = 4, 64+4*uint64(len(raw)) // public span-overlap checks plus both copies
					}
					n, done, err := d.Step(span)
					if err != nil || done {
						t.Fatal("copy-only quantum", done, err)
					}
					if crc {
						wantR, wantB := uint64(0), uint64(0)
						if mode == BoundedSpans {
							wantR, wantB = 2, 64 // actual public overlap checks
						}
						if n != 0 || len(d.bounded.raw) != 0 || d.bounded.hash != before || w.usedR != wantR || w.usedB != wantB {
							t.Fatal("checksum emission escaped its refused admission")
						}
					} else {
						if d.bounded.position != uint64(len(raw)) || d.bounded.hash != initialHash || w.r != 0 || w.b != 0 {
							t.Fatalf("no-checksum frame consumed checksum work or failed copy-only admission: position=%d R=%d B=%d", d.bounded.position, w.usedR, w.usedB)
						}
						if mode == BoundedSpans && (n != len(raw) || !bytes.Equal(span[:n], raw)) {
							t.Fatal("copy-only span output", n)
						}
					}
					var got []byte
					if mode == BoundedSpans {
						got = append(got, span[:n]...)
					}
					for call := 0; call < 1000; call++ {
						w.quantum()
						if _, need := d.InputRequest(); need {
							n, _, err := d.FeedInput(frame[position:], true)
							if err != nil {
								t.Fatal(err)
							}
							position += n
							continue
						}
						n, done, err := d.Step(span)
						if err != nil {
							t.Fatal(err)
						}
						if mode == BoundedSpans {
							got = append(got, span[:n]...)
						}
						if done {
							if mode == BoundedRaw {
								got, err = d.RawResult()
								if err != nil {
									t.Fatal(err)
								}
							}
							if !bytes.Equal(got, oracle) || (!crc && d.bounded.hash != initialHash) || (crc && d.bounded.hash == initialHash) {
								t.Fatal("checksum selection or output changed")
							}
							t.Logf("copy-only admission: crc=%v mode=%d fed=%v; decoded=%d", crc, mode, fed, len(got))
							return
						}
					}
					t.Fatal("completion stalled")
				})
			}
		}
	}
}
