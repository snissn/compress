package zstd

import (
	"bytes"
	"fmt"
	"testing"
)

func TestBoundedConcatenatedCompressedHistory(t *testing.T) {
	first := bytes.Repeat([]byte("first independently compressed frame/"), 5000)
	second := bytes.Repeat([]byte("second frame overlap and segmented history/"), 5000)
	a := boundedFixture(t, first, true)
	b := boundedFixture(t, second, true)
	frame := append(append([]byte(nil), a...), b...)
	want := append(append([]byte(nil), first...), second...)
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		t.Run(map[BoundedOutput]string{BoundedRaw: "raw", BoundedSpans: "spans"}[mode], func(t *testing.T) { runBounded(t, frame, want, mode, BorrowedDictionary{}) })
	}
	got, err := streamedOutcome(t, frame, 17)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("fed concatenated history", err, len(got), len(want))
	}
}

// The same decoder is reset between independent attempts. This exercises the
// original frame base separately from enclosing-owner reuse and fed input.
func multiFrameOutcome(t *testing.T, d *Decoder, w *boundedTestWork, frame []byte, dict BorrowedDictionary, mode BoundedOutput, fed, reset bool) ([]byte, error) {
	t.Helper()
	raw := make([]byte, 0, 600000)
	var ok bool
	var err error
	for i := 0; i < 10000; i++ {
		w.quantum()
		switch {
		case fed && reset:
			ok, err = d.ResetStreamStep(&frame, mode, raw, dict)
		case fed:
			ok, err = d.BeginStream(&frame, mode, raw, dict)
		case reset:
			ok, err = d.ResetStep(frame, &frame, mode, raw, dict)
		default:
			ok, err = d.Begin(frame, &frame, mode, raw, dict)
		}
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
	}
	if !ok {
		t.Fatal("reset/begin stalled")
	}
	span := make([]byte, 4096)
	var got []byte
	pos := 0
	for call := 0; call < 200000; call++ {
		w.quantum()
		if _, need := d.InputRequest(); need {
			end := min(pos+17, len(frame))
			n, _, e := d.FeedInput(frame[pos:end], end == len(frame))
			pos += n
			if e != nil {
				return got, e
			}
		} else {
			n, done, e := d.Step(span)
			if mode == BoundedSpans {
				got = append(got, span[:n]...)
			}
			if e != nil {
				return got, e
			}
			if done {
				if mode == BoundedRaw {
					got, e = d.RawResult()
				}
				return got, e
			}
		}
		if w.usedR > 32 || w.usedB > 1<<20 {
			t.Fatal("quantum exceeded")
		}
	}
	t.Fatal("decode stalled")
	return nil, nil
}

func TestBoundedConcatenatedDictionaryAndFrameIndependence(t *testing.T) {
	dictionary := make([]byte, 24000)
	x := uint32(7)
	for i := range dictionary {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		dictionary[i] = byte(x)
	}
	// A real match references dictionary bytes before there is enough output in
	// this frame. An earlier frame containing those bytes must not make it valid.
	second := append(append([]byte(nil), dictionary[len(dictionary)-6000:]...), bytes.Repeat([]byte("compressed second independent frame/"), 6000)...)
	a := boundedFixture(t, dictionary, true)
	b := boundedFixture(t, second, true, WithEncoderDictRaw(0, dictionary))
	frame := append(append([]byte(nil), a...), b...)
	want := append(append([]byte(nil), dictionary...), second...)
	ordinary, err := NewReader(nil, WithDecoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	_, oracleErr := ordinary.DecodeAll(frame, nil)
	ordinary.Close()
	if oracleErr == nil {
		t.Fatal("producer did not require dictionary history")
	}
	ordinary, err = NewReader(nil, WithDecoderConcurrency(1), WithDecoderDictRaw(0, dictionary))
	if err != nil {
		t.Fatal(err)
	}
	oracle, err := ordinary.DecodeAll(frame, nil)
	ordinary.Close()
	if err != nil || !bytes.Equal(oracle, want) {
		t.Fatal("dictionary oracle", err)
	}
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		for _, fed := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode%d/fed%v", mode, fed), func(t *testing.T) {
				w := &boundedTestWork{limit: 32 << 20}
				w.quantum()
				d, ok, e := NewBoundedReader(w)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				defer closeBoundedForTest(t, &d, w)
				got, e := multiFrameOutcome(t, d, w, frame, BorrowedDictionary{}, mode, fed, false)
				if e == nil {
					t.Fatal("prior frame authorized dictionary-only match", len(got))
				}
				got, e = multiFrameOutcome(t, d, w, frame, BorrowedDictionary{ID: 0, Data: dictionary, Owner: &dictionary, Raw: true}, mode, fed, true)
				if e != nil || !bytes.Equal(got, want) {
					t.Fatal("dictionary reset oracle", e, len(got), len(want))
				}
				// Remove dictionary custody on reuse; stale dictionary/history aliases
				// must not authorize an otherwise-invalid second frame.
				_, e = multiFrameOutcome(t, d, w, frame, BorrowedDictionary{}, mode, fed, true)
				if e == nil {
					t.Fatal("reset preserved unselected dictionary")
				}
			})
		}
	}
}

func TestBoundedCancelSecondCompressedFrame(t *testing.T) {
	first := bytes.Repeat([]byte("first independently compressed/"), 5000)
	second := bytes.Repeat([]byte("second independent match across segments/"), 6000)
	frame := append(boundedFixture(t, first, true), boundedFixture(t, second, true)...)
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		for _, fed := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode%d/fed%v", mode, fed), func(t *testing.T) {
				w := &boundedTestWork{limit: 32 << 20}
				w.quantum()
				d, ok, e := NewBoundedReader(w)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				raw := make([]byte, 0, len(first)+len(second))
				if fed {
					ok, e = d.BeginStream(&frame, mode, raw, BorrowedDictionary{})
				} else {
					ok, e = d.Begin(frame, &frame, mode, raw, BorrowedDictionary{})
				}
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				span := make([]byte, 4096)
				pos := 0
				found := false
				for i := 0; i < 200000; i++ {
					w.quantum()
					if _, need := d.InputRequest(); need {
						end := min(pos+17, len(frame))
						n, _, e := d.FeedInput(frame[pos:end], end == len(frame))
						pos += n
						if e != nil {
							t.Fatal(e)
						}
					} else {
						_, done, e := d.Step(span)
						if done || e != nil {
							t.Fatal("finished before second-frame pause", done, e)
						}
					}
					if d.bounded.frameStart > 0 && d.bounded.phase == 18 {
						found = true
						break
					}
				}
				if !found {
					t.Fatal("no real second-frame match pause")
				}
				// Close first refuses with no work, preserving the exact suspended state.
				position, phase := d.bounded.position, d.bounded.phase
				w.quantum()
				w.refuse = true
				ok, e = d.CloseStep()
				if ok || e != nil || d.bounded.position != position || d.bounded.phase != phase {
					t.Fatal("close refusal changed decode state", ok, e)
				}
				w.refuse = false
				closeBoundedForTest(t, &d, w)
			})
		}
	}
}
