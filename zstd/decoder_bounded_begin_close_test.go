package zstd

import (
	"errors"
	"fmt"
	"testing"
)

type beginDuringCloseWork struct {
	boundedTestWork
	pausePhase int
	pause      bool
}

func (w *beginDuringCloseWork) Reserve(r, b uint64) bool {
	if w.pause && w.observed != nil && w.observed.closing && w.observed.closePhase == w.pausePhase {
		return false
	}
	return w.boundedTestWork.Reserve(r, b)
}

// Select a real public CloseStep pause through admission. Parser/lifecycle
// state is observed, never modified, and Begin must reject before any effect.
func TestBoundedBeginStreamRejectsPausedClose(t *testing.T) {
	for _, phase := range []int{0, 6} {
		for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
			t.Run(fmt.Sprintf("phase=%d/mode=%d", phase, mode), func(t *testing.T) {
				w := &beginDuringCloseWork{boundedTestWork: boundedTestWork{limit: 32 << 20}, pausePhase: phase, pause: true}
				w.quantum()
				d, ok, err := NewBoundedReader(w)
				if err != nil || !ok {
					t.Fatal("constructor", ok, err)
				}
				w.observed = d.bounded
				var done bool
				for calls := 0; calls < 1000; calls++ {
					w.quantum()
					done, err = d.CloseStep()
					if err != nil || done {
						t.Fatal("real CloseStep pause", done, err)
					}
					if d.bounded.closing && d.bounded.closePhase == phase {
						break
					}
				}
				if !d.bounded.closing || d.bounded.closePhase != phase {
					t.Fatal("original close did not reach selected pause")
				}
				w.pause = false
				w.quantum()
				retained, totalR, totalB := w.retained, w.totalR, w.totalB
				owner := new(byte)
				output := make([]byte, 0, 16)
				ok, err = d.BeginStream(owner, mode, output, BorrowedDictionary{})
				if ok || !errors.Is(err, ErrBoundedMode) {
					t.Fatal("BeginStream while closing", ok, err)
				}
				if w.retained != retained || w.totalR != totalR || w.totalB != totalB || d.bounded.inputBuffer != nil || d.bounded.allocatedInput != 0 || d.bounded.inputOwner != nil {
					t.Errorf("rejected BeginStream performed work or retained input: retained=%d->%d R=%d->%d B=%d->%d input=%d", retained, w.retained, totalR, w.totalR, totalB, w.totalB, d.bounded.allocatedInput)
				}
				ok, err = d.Begin(nil, owner, mode, output, BorrowedDictionary{})
				if ok || !errors.Is(err, ErrBoundedMode) {
					t.Fatal("Begin while closing", ok, err)
				}
				w.pause = false
				for calls := 0; calls < 1000; calls++ {
					w.quantum()
					done, err = d.CloseStep()
					if err != nil {
						t.Fatal("finish original CloseStep", err)
					}
					if done {
						break
					}
				}
				if !done {
					t.Fatal("finish original CloseStep stalled")
				}
				w.observed = nil
				if w.retained != BoundedControlSize() {
					t.Errorf("CloseStep stranded capacity: retained=%d want=%d", w.retained, BoundedControlSize())
				}
				d = nil // Drop the containing final control before refunding its admission.
				w.Release(BoundedControlSize())
				if w.retained != 0 {
					t.Fatal("original owner retirement", w.retained)
				}
			})
		}
	}
}
