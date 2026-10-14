package huff0

import (
	"github.com/snissn/compress/internal/decoderwork"
	"testing"
)

type tableCustodyWork struct {
	r, b, retained uint64
	scratch        *Scratch
	state          *TableState
	t              *testing.T
}

func (w *tableCustodyWork) Reserve(r, b uint64) bool {
	if r > w.r || b > w.b {
		return false
	}
	w.r -= r
	w.b -= b
	return true
}
func (w *tableCustodyWork) Available() (uint64, uint64) { return w.r, w.b }
func (w *tableCustodyWork) Retain(n uint64) bool        { w.retained += n; return true }
func (w *tableCustodyWork) Release(n uint64) {
	if w.state.closePhase == 3 && (w.scratch.prevTable != nil || w.scratch.dt.single != nil) {
		w.t.Fatal("refunded table with surviving original scratch alias")
	}
	if n > w.retained {
		w.t.Fatal("refund exceeds admission")
	}
	w.retained -= n
}
func TestTableStateLastAliasBeforeRefund(t *testing.T) {
	raw := make([]byte, 20000)
	x := uint32(19)
	for i := range raw {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		raw[i] = byte(x & 63)
	}
	encoded, _, err := Compress1X(raw, &Scratch{})
	if err != nil {
		t.Fatal(err)
	}
	scratch := &Scratch{}
	state := &TableState{}
	w := &tableCustodyWork{scratch: scratch, state: state, t: t}
	w.r = 32
	w.b = 1 << 20
	// Containing controls are caller-owned; this test audits the phase-owned backings.
	if !w.Reserve(1, 256) {
		t.Fatal("begin admission")
	}
	state.Begin(encoded, scratch)
	for calls := 0; calls < 10000; calls++ {
		w.r = 32
		w.b = 1 << 20
		done, _, err := state.Step(w)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if calls == 9999 {
			t.Fatal("build stalled")
		}
	}
	if len(scratch.prevTable) == 0 || len(scratch.dt.single) == 0 || w.retained == 0 {
		t.Fatal("fixture did not construct actual tables")
	}
	w.r = 0
	w.b = 0
	before := w.retained
	done, err := state.ClearStep(w)
	if done || err != nil || w.retained != before || len(scratch.prevTable) == 0 {
		t.Fatal("refusal changed custody")
	}
	for calls := 0; calls < 10000; calls++ {
		w.r = 1
		w.b = 1 << 20
		done, err := state.ClearStep(w)
		if err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if calls == 9999 {
			t.Fatal("clear stalled")
		}
	}
	if scratch.prevTable != nil || scratch.dt.single != nil || scratch.fse != nil || state.in != nil || state.remain != nil || w.retained != 0 {
		t.Fatal("retained last alias after clear")
	}
	var _ decoderwork.Work = w
}
