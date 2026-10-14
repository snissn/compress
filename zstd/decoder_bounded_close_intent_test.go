package zstd

import (
	"errors"
	"fmt"
	"testing"
	"unsafe"
)

// Every binding API rejects a real paused terminal close before work. The
// admission seam selects the pause; tests never manufacture lifecycle state.
func TestBoundedTerminalCloseRejectsBindings(t *testing.T) {
	for _, phase := range []int{0, 4, 5, 6} {
		for _, begun := range []bool{false, true} {
			for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
				for _, operation := range []string{"begin", "stream", "reset", "reset-stream"} {
					t.Run(fmt.Sprintf("phase=%d/begun=%v/mode=%d/op=%s", phase, begun, mode, operation), func(t *testing.T) {
						w := &beginDuringCloseWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, pausePhase: phase, pause: true}
						w.quantum()
						d, ok, err := NewBoundedReader(w)
						if err != nil || !ok {
							t.Fatal("constructor", ok, err)
						}
						oldOwner, newOwner := new(byte), new(byte)
						if begun {
							ok, err = d.BeginStream(oldOwner, mode, nil, BorrowedDictionary{})
							if err != nil || !ok {
								t.Fatal("old stream", ok, err)
							}
						}
						w.observed = d.bounded
						var done bool
						for calls := 0; calls < 1000; calls++ {
							w.quantum()
							done, err = d.CloseStep()
							if err != nil || done {
								t.Fatal("real close pause", done, err)
							}
							if d.bounded.closePhase == phase {
								break
							}
						}
						if !d.bounded.closing || d.bounded.closePhase != phase {
							t.Fatal("selected pause unreachable")
						}
						w.pause = false
						w.quantum()
						retained, r, b := w.retained, w.totalR, w.totalB
						core, owner, allocated, inputLen := d.bounded, d.bounded.inputOwner, d.bounded.allocatedInput, len(d.bounded.inputBuffer)
						switch operation {
						case "begin":
							ok, err = d.Begin(nil, newOwner, mode, nil, BorrowedDictionary{})
						case "stream":
							ok, err = d.BeginStream(newOwner, mode, nil, BorrowedDictionary{})
						case "reset":
							ok, err = d.ResetStep(nil, newOwner, mode, nil, BorrowedDictionary{})
						case "reset-stream":
							ok, err = d.ResetStreamStep(newOwner, mode, nil, BorrowedDictionary{})
						}
						if ok || !errors.Is(err, ErrBoundedMode) {
							t.Errorf("binding replaced terminal close: ok=%v err=%v", ok, err)
						}
						if d.bounded != core || !core.closing || core.closePhase != phase || core.inputOwner != owner || core.allocatedInput != allocated || len(core.inputBuffer) != inputLen || w.retained != retained || w.totalR != r || w.totalB != b {
							t.Errorf("rejected binding changed original lifecycle/admission: phase=%d->%d closing=%v retained=%d->%d R=%d->%d B=%d->%d", phase, core.closePhase, core.closing, retained, w.retained, r, w.totalR, b, w.totalB)
						}
						for calls := 0; calls < 1000; calls++ {
							w.quantum()
							done, err = d.CloseStep()
							if err != nil {
								t.Fatal("finish close", err)
							}
							if done {
								break
							}
						}
						if !done || d.bounded != nil {
							t.Fatal("terminal close did not retire continuation")
						}
						w.observed = nil
						if w.retained != BoundedControlSize() {
							t.Fatal("stranded capacity", w.retained)
						}
						d = nil
						w.Release(BoundedControlSize())
						if w.retained != 0 {
							t.Fatal("last control capacity", w.retained)
						}
					})
				}
			}
		}
	}
}

// A paused reset remains resumable. A subsequent Close promotes cleanup to
// terminal intent, even when Close itself cannot perform its first effect.
func TestBoundedClosePromotesPausedReset(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, promote := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/promote=%v", stream, promote), func(t *testing.T) {
				w := &beginDuringCloseWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, pausePhase: 6, pause: true}
				w.quantum()
				d, ok, err := NewBoundedReader(w)
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				owner := new(byte)
				ok, err = d.Begin(nil, owner, BoundedSpans, nil, BorrowedDictionary{})
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				core := d.bounded
				w.observed = core
				reset := func() (bool, error) {
					if stream {
						return d.ResetStreamStep(owner, BoundedSpans, nil, BorrowedDictionary{})
					}
					return d.ResetStep(nil, owner, BoundedSpans, nil, BorrowedDictionary{})
				}
				for calls := 0; calls < 1000; calls++ {
					w.quantum()
					ok, err = reset()
					if err != nil || ok {
						t.Fatal("real reset pause", ok, err)
					}
					if core.closePhase == 6 {
						break
					}
				}
				if core.closePhase != 6 {
					t.Fatal("reset pause unreachable")
				}
				if promote {
					w.quantum()
					done, e := d.CloseStep()
					if done || e != nil {
						t.Fatal("paused terminal promotion", done, e)
					}
					w.pause = false
					w.quantum()
					r, b, retained := w.totalR, w.totalB, w.retained
					ok, err = reset()
					if ok || !errors.Is(err, ErrBoundedMode) || w.totalR != r || w.totalB != b || w.retained != retained || d.bounded != core || !core.closing || core.closePhase != 6 {
						t.Errorf("reset downgraded promoted close: ok=%v err=%v phase=%d", ok, err, core.closePhase)
					}
				} else {
					w.pause = false
					w.quantum()
					ok, err = reset()
					if err != nil || !ok || d.bounded != core {
						t.Fatal("valid reset retry", ok, err)
					}
				}
				w.pause = false
				for calls := 0; calls < 1000; calls++ {
					w.quantum()
					done, e := d.CloseStep()
					if e != nil {
						t.Fatal(e)
					}
					if done {
						w.observed = nil
						d = nil
						w.Release(BoundedControlSize())
						if w.retained != 0 {
							t.Fatal(w.retained)
						}
						return
					}
				}
				t.Fatal("close stalled")
			})
		}
	}
	t.Logf("fixed continuation sizeof=%d control=%d", unsafe.Sizeof(boundedDecoder{}), BoundedControlSize())
}

// Query methods cannot expose aliases or ask for more input during cleanup.
func TestBoundedClosingQueries(t *testing.T) {
	t.Run("raw-result", func(t *testing.T) {
		raw := []byte("completed raw result")
		frame := boundedFixture(t, raw, true)
		w := &beginDuringCloseWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, pausePhase: 0, pause: true}
		w.quantum()
		d, ok, err := NewBoundedReader(w)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		out := make([]byte, 0, len(raw))
		ok, err = d.Begin(frame, &frame, BoundedRaw, out, BorrowedDictionary{})
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		var done bool
		for calls := 0; calls < 10000; calls++ {
			w.quantum()
			_, done, err = d.Step(nil)
			if err != nil {
				t.Fatal(err)
			}
			if done {
				break
			}
		}
		if !done {
			t.Fatal("decode stalled")
		}
		got, err := d.RawResult()
		if err != nil || string(got) != string(raw) {
			t.Fatal("finished raw result", err)
		}
		got = nil
		w.observed = d.bounded
		w.quantum()
		done, err = d.CloseStep()
		if done || err != nil {
			t.Fatal("close pause", done, err)
		}
		got, err = d.RawResult()
		if got != nil || !errors.Is(err, ErrBoundedMode) {
			t.Errorf("closing raw result exposed alias: n=%d err=%v", len(got), err)
		}
		got = nil
		w.pause = false
		w.observed = nil
		closeBoundedForTest(t, &d, &w.boundedTestWork)
	})
	t.Run("input-request", func(t *testing.T) {
		w := &beginDuringCloseWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, pausePhase: 0, pause: true}
		w.quantum()
		d, ok, err := NewBoundedReader(w)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		owner := new(byte)
		ok, err = d.BeginStream(owner, BoundedSpans, nil, BorrowedDictionary{})
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
		needed := false
		for calls := 0; calls < 10000; calls++ {
			w.quantum()
			_, done, e := d.Step(nil)
			if done || e != nil {
				t.Fatal("stream step", done, e)
			}
			_, needed = d.InputRequest()
			if needed {
				break
			}
		}
		if !needed {
			t.Fatal("real input request did not occur")
		}
		w.observed = d.bounded
		w.quantum()
		done, err := d.CloseStep()
		if done || err != nil {
			t.Fatal("close pause", done, err)
		}
		n, needed := d.InputRequest()
		if n != 0 || needed {
			t.Errorf("closing input request exposed work: n=%d needed=%v", n, needed)
		}
		w.pause = false
		w.observed = nil
		closeBoundedForTest(t, &d, &w.boundedTestWork)
	})
}
