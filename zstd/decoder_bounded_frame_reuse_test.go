package zstd

import (
	"bytes"
	"errors"
	"os"
	"testing"
)

type frameReuseTestWork struct {
	boundedTestWork
	denyRetain  bool
	retainCalls int
}

func (w *frameReuseTestWork) Retain(n uint64) bool {
	w.retainCalls++
	if w.denyRetain {
		return false
	}
	return w.boundedTestWork.Retain(n)
}
func frameReuseDecode(t *testing.T, d *Decoder, w *frameReuseTestWork, wire, raw []byte, dict BorrowedDictionary, reuse bool) ([]byte, error) {
	t.Helper()
	ok := false
	var err error
	for i := 0; i < 100000; i++ {
		w.quantum()
		if reuse {
			ok, err = d.ResetFrameStep(wire, &wire, BoundedRaw, raw)
		} else {
			ok, err = d.Begin(wire, &wire, BoundedRaw, raw, dict)
		}
		if err != nil {
			return nil, err
		}
		if ok {
			break
		}
	}
	if !ok {
		t.Fatal("bind stalled")
	}
	for i := 0; i < 300000; i++ {
		w.quantum()
		_, done, err := d.Step(nil)
		if err != nil {
			return nil, err
		}
		if done {
			return d.RawResult()
		}
	}
	t.Fatal("decode stalled")
	return nil, nil
}
func frameReuseDetach(t *testing.T, d *Decoder, w *frameReuseTestWork) {
	t.Helper()
	for i := 0; i < 100000; i++ {
		w.quantum()
		ok, err := d.ResetFrameStep(nil, d, BoundedRaw, nil)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return
		}
	}
	t.Fatal("detach stalled")
}

func TestBoundedResetFrameRetainsValidatedContext(t *testing.T) {
	for _, name := range []string{"leaf255-dictionary", "pointed-rich"} {
		t.Run(name, func(t *testing.T) {
			wire, err := os.ReadFile("testdata/bounded-gomap/" + name + ".zstd")
			if err != nil {
				t.Fatal(err)
			}
			var dict BorrowedDictionary
			opts := []DOption{WithDecoderConcurrency(1)}
			if name == "leaf255-dictionary" {
				definition, err := os.ReadFile("testdata/bounded-gomap/leaf255-dictionary.dict")
				if err != nil {
					t.Fatal(err)
				}
				dict = BorrowedDictionary{ID: 73, Data: definition, Owner: &definition}
				opts = append(opts, WithDecoderDicts(definition))
			}
			oracle, err := NewReader(nil, opts...)
			if err != nil {
				t.Fatal(err)
			}
			want, err := oracle.DecodeAll(wire, nil)
			oracle.Close()
			if err != nil {
				t.Fatal(err)
			}
			w := &frameReuseTestWork{boundedTestWork: boundedTestWork{limit: 32 << 20}}
			w.quantum()
			d, ok, err := NewBoundedReaderWithLimits(w, 4<<20, 8<<20)
			if !ok || err != nil {
				t.Fatal(ok, err)
			}
			defer closeBoundedForTest(t, &d, &w.boundedTestWork)
			constructorCalls := w.retainCalls
			raw := make([]byte, 0, len(want))
			got, err := frameReuseDecode(t, d, w, wire, raw, dict, false)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatal("cold oracle", err)
			}
			if w.retainCalls <= constructorCalls {
				t.Fatal("fixture did not admit reusable backing")
			}
			retained, calls := w.retained, w.retainCalls
			w.denyRetain = true
			// The outer decoder's actual admission now refuses EVERY attempted Retain.
			// This proves reuse, rather than success after readmitting warm backing.
			for attempt := 0; attempt < 3; attempt++ {
				frameReuseDetach(t, d, w)
				core := d.bounded
				if core.in != nil || core.boundInput != nil || core.frameInput != nil || core.raw != nil || core.inputOwner != d || core.literals != nil || core.compressed != nil || core.seqInput != nil || core.pending != nil || core.hist.b != nil || core.hist.dict != nil {
					t.Fatal("prior frame aliases survived explicit detach")
				}
				if core.dictionary.Owner != dict.Owner || core.dictPhase != 8 {
					t.Fatal("validated dictionary custody changed")
				}
				got, err = frameReuseDecode(t, d, w, wire, raw, dict, true)
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("warm refused-Retain oracle", attempt, err)
				}
				if w.retained != retained || w.retainCalls != calls {
					t.Fatalf("warm readmission: bytes %d/%d calls %d/%d", w.retained, retained, w.retainCalls, calls)
				}
			}
			if name == "pointed-rich" {
				// One sequence requesting repeat modes without any frame-local definition.
				// Previous-frame entropy backing must never grant semantic repeat permission.
				repeat := []byte{0x28, 0xb5, 0x2f, 0xfd, 0, 0, 0x25, 0, 0, 0, 1, 0xfc, 1}
				ordinary, err := NewReader(nil, WithDecoderConcurrency(1))
				if err != nil {
					t.Fatal(err)
				}
				_, oracleErr := ordinary.DecodeAll(repeat, nil)
				ordinary.Close()
				if oracleErr == nil {
					t.Fatal("repeat fixture accepted by ordinary decoder")
				}
				if _, err := frameReuseDecode(t, d, w, repeat, raw, BorrowedDictionary{}, true); !errors.Is(err, errStepUndefinedSequenceDecoder) {
					t.Fatal("previous frame granted repeat permission", err)
				}
			}
			// Original full reset still retires the definition and all owned buffers.
			withoutDict := boundedFixture(t, []byte("new independent frame"), true, WithSingleSegment(true))
			for i := 0; i < 100000; i++ {
				w.quantum()
				ok, err = d.ResetStep(withoutDict, &withoutDict, BoundedRaw, make([]byte, 0, 64), BorrowedDictionary{})
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					break
				}
			}
			if !ok || d.bounded.dictionary.Owner != nil || d.bounded.dictContent != nil || d.bounded.allocatedLiteral != 0 {
				t.Fatal("full reset retained previous definition/backing")
			}
			if w.retained >= retained {
				t.Fatal("full reset did not retire actual warm backing")
			}
			for i := 0; i < 100000; i++ {
				w.quantum()
				_, done, err := d.Step(nil)
				if err != nil {
					t.Fatal(err)
				}
				if done {
					break
				}
			}
			got, err = d.RawResult()
			if err != nil || string(got) != "new independent frame" {
				t.Fatal("full-reset result", string(got), err)
			}
		})
	}
}

func TestBoundedResetFrameRejectsBeforeEffects(t *testing.T) {
	w := &frameReuseTestWork{boundedTestWork: boundedTestWork{limit: 32 << 20}}
	w.quantum()
	d, ok, err := NewBoundedReader(w)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	defer closeBoundedForTest(t, &d, &w.boundedTestWork)
	raw := make([]byte, 0, 64)
	beforeR, beforeB, beforeRetain := w.totalR, w.totalB, w.retained
	ok, err = d.ResetFrameStep(nil, d, BoundedRaw, raw)
	if ok || !errors.Is(err, ErrBoundedMode) || w.totalR != beforeR || w.totalB != beforeB || w.retained != beforeRetain {
		t.Fatal("cold/incomplete dictionary accepted", ok, err)
	}
	bad := BorrowedDictionary{ID: 73, Data: []byte("bad definition"), Owner: d}
	if _, err := frameReuseDecode(t, d, w, nil, raw, bad, false); err == nil {
		t.Fatal("dictionary fixture did not fail")
	}
	beforeR, beforeB, beforeRetain = w.totalR, w.totalB, w.retained
	ok, err = d.ResetFrameStep(nil, d, BoundedRaw, raw)
	if ok || !errors.Is(err, ErrBoundedMode) || w.totalR != beforeR || w.totalB != beforeB || w.retained != beforeRetain {
		t.Fatal("failed dictionary accepted", ok, err)
	}
}

func TestBoundedResetFrameLimitsAndEntropyIndependence(t *testing.T) {
	w := &frameReuseTestWork{boundedTestWork: boundedTestWork{limit: 32 << 20}}
	w.quantum()
	d, ok, err := NewBoundedReaderWithLimits(w, 1024, 2048)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	defer closeBoundedForTest(t, &d, &w.boundedTestWork)
	raw := make([]byte, 0, 2048)
	small := boundedFixture(t, bytes.Repeat([]byte("x"), 600), true, WithSingleSegment(true))
	if _, err := frameReuseDecode(t, d, w, small, raw, BorrowedDictionary{}, false); err != nil {
		t.Fatal(err)
	}
	beforeR, beforeB, beforeRetain := w.totalR, w.totalB, w.retained
	ok, err = d.ResetFrameStep(nil, d, BoundedSpans, nil)
	if ok || !errors.Is(err, ErrBoundedMode) || w.totalR != beforeR || w.totalB != beforeB || w.retained != beforeRetain {
		t.Fatal("unsupported spans changed owner", ok, err)
	}
	twice := append(append([]byte(nil), small...), small...)
	if _, err := frameReuseDecode(t, d, w, twice, raw, BorrowedDictionary{}, true); !errors.Is(err, ErrDecoderSizeExceeded) {
		t.Fatal("aggregate limit lost", err)
	}
	// A failed FRAME is reusable only because its dictionary context is already
	// fully validated. It must start a new output allowance, not retain failure.
	got, err := frameReuseDecode(t, d, w, small, raw, BorrowedDictionary{}, true)
	if err != nil || len(got) != 600 {
		t.Fatal("failed-frame reset", len(got), err)
	}
}

func TestBoundedResetFrameCleanupPromotion(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "full-reset", true: "close"}[terminal], func(t *testing.T) {
			w := &beginDuringCloseWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, pausePhase: 5, pause: true}
			w.quantum()
			d, ok, err := NewBoundedReader(w)
			if !ok || err != nil {
				t.Fatal(ok, err)
			}
			w.observed = d.bounded
			wire := boundedFixture(t, bytes.Repeat([]byte("literal/reusable/"), 1000), true)
			// Establish validated context through the original public complete-input API.
			got, err := multiFrameOutcome(t, d, &w.boundedTestWork, wire, BorrowedDictionary{}, BoundedRaw, false, false)
			if err != nil || len(got) == 0 {
				t.Fatal(err)
			}
			for i := 0; i < 10000; i++ {
				w.quantum()
				ok, err = d.ResetFrameStep(nil, d, BoundedRaw, nil)
				if err != nil || ok {
					t.Fatal("expected original cleanup pause", ok, err)
				}
				if d.bounded.closePhase == 5 {
					break
				}
			}
			if d.bounded.closePhase != 5 {
				t.Fatal("reuse did not reach pause")
			}
			w.pause = false
			for i := 0; i < 10000; i++ {
				w.quantum()
				if terminal {
					ok, err = d.CloseStep()
				} else {
					ok, err = d.ResetStep(nil, d, BoundedRaw, nil, BorrowedDictionary{})
				}
				if err != nil {
					t.Fatal(err)
				}
				if ok {
					break
				}
			}
			if !ok {
				t.Fatal("cleanup promotion stalled")
			}
			if terminal {
				if d.bounded != nil || w.retained != BoundedControlSize() {
					t.Fatal("close skipped previously retained cohorts")
				}
				d = nil
				w.observed = nil
				w.Release(BoundedControlSize())
			} else {
				if d.bounded.dictionary.Owner != nil || d.bounded.allocatedLiteral != 0 {
					t.Fatal("full reset skipped reuse-retained cohorts")
				}
				closeBoundedForTest(t, &d, &w.boundedTestWork)
			}
			if w.retained != 0 {
				t.Fatal("cleanup promotion stranded capacity", w.retained)
			}
		})
	}
}
