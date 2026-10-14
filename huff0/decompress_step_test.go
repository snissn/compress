package huff0

import (
	"bytes"
	"github.com/snissn/compress/internal/decoderwork"
	"reflect"
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

// Use the real public table and jump-table grammar. Validation must suspend
// before each partition control and reject impossible geometry before any
// reader alias or output effect; literal probes retain their original 1R/7B.
func TestLiteralStateFourStreamBeforeEffect(t *testing.T) {
	scratch, _, err := ReadTable([]byte{128, 0x10}, nil)
	if err != nil {
		t.Fatal(err)
	}
	src := []byte{1, 0, 1, 0, 1, 0, 2, 1, 2, 1}
	dst := []byte{0xa5}
	p := &LiteralState{}
	p.Begin(scratch, src, dst, true)
	w := &tableCustodyWork{}
	for calls := 0; calls < 8; calls++ {
		before := *p
		w.r = 0
		w.b = 0
		done, e := p.Step(w)
		if done || e != nil || !reflect.DeepEqual(before, *p) || dst[0] != 0xa5 {
			t.Fatal("refusal changed validation state", calls, e)
		}
		w.r = 1
		w.b = 0
		done, e = p.Step(w)
		for i := range p.br {
			if p.br[i].in != nil {
				t.Fatal("initialized reader before partition validation")
			}
		}
		if dst[0] != 0xa5 {
			t.Fatal("interpreted malformed partition")
		}
		if e != nil {
			if e != errStepLiteralOutputSize {
				t.Fatal(e)
			}
			break
		}
		if done || calls == 7 {
			t.Fatal("malformed partition was accepted or stalled")
		}
	}
	p.Clear()
	if p.s != nil || p.src != nil || p.dst != nil {
		t.Fatal("cancel retained input/output aliases")
	}
	// Valid partition: refuse independently on records and on bytes exactly at
	// an actual table probe, then allow one probe without restarting or advancing.
	src = []byte{1, 0, 1, 0, 1, 0, 4, 4, 4, 4}
	dst = bytes.Repeat([]byte{0xa5}, 8)
	p.Begin(scratch, src, dst, true)
	for calls := 0; calls < 20 && p.phase != 4; calls++ {
		w.r = 1
		w.b = 11
		done, e := p.Step(w)
		if done || e != nil {
			t.Fatal(done, e)
		}
	}
	if p.phase != 4 || p.offset != 0 {
		t.Fatal("did not reach first symbol")
	}
	for _, budget := range [][2]uint64{{0, 7}, {1, 6}} {
		before := *p
		w.r = budget[0]
		w.b = budget[1]
		done, e := p.Step(w)
		if done || e != nil || !reflect.DeepEqual(before, *p) || !bytes.Equal(dst, bytes.Repeat([]byte{0xa5}, 8)) {
			t.Fatal("probe refusal changed state", e)
		}
	}
	w.r = 1
	w.b = 7
	done, e := p.Step(w)
	if done || e != nil || p.offset != 1 || dst[0] != 0 || !bytes.Equal(dst[1:], bytes.Repeat([]byte{0xa5}, 7)) {
		t.Fatal("probe advanced past budget", done, e, p.offset, dst)
	}
	p.Clear()
	for i := range p.br {
		if p.br[i].in != nil {
			t.Fatal("cancel retained reader alias")
		}
	}
}
