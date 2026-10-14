package zstd

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
	"unsafe"
)

type boundedTestWork struct {
	r, b                         uint64
	usedR, usedB, totalR, totalB uint64
	retained, peak, limit        uint64
	refuse                       bool
	observed                     *boundedDecoder
	historyProbes                int
	historyLimit                 int
	phaseSource                  *int
	phaseR, phaseB               [200]uint64
}

func (w *boundedTestWork) quantum() { w.r = 32; w.b = 1 << 20; w.usedR = 0; w.usedB = 0 }
func (w *boundedTestWork) Reserve(r, b uint64) bool {
	if w.observed != nil && w.observed.lookup.started && w.observed.lookup.node != nil {
		if w.historyLimit > 0 && w.historyProbes >= w.historyLimit {
			return false
		}
		w.historyProbes++
	}
	if w.refuse || r > w.r || b > w.b {
		return false
	}
	w.r -= r
	w.b -= b
	w.usedR += r
	w.usedB += b
	w.totalR += r
	w.totalB += b
	phase := 199
	if w.phaseSource != nil && *w.phaseSource >= 0 && *w.phaseSource < 199 {
		phase = *w.phaseSource
	}
	w.phaseR[phase] += r
	w.phaseB[phase] += b
	return true
}
func (w *boundedTestWork) Available() (uint64, uint64) { return w.r, w.b }
func (w *boundedTestWork) Retain(n uint64) bool {
	if w.retained+n > w.limit {
		return false
	}
	w.retained += n
	if w.retained > w.peak {
		w.peak = w.retained
	}
	return true
}
func (w *boundedTestWork) Release(n uint64) {
	if n > w.retained {
		panic("capacity refunded without owner")
	}
	w.retained -= n
}
func boundedFixture(t *testing.T, raw []byte, crc bool, opts ...EOption) []byte {
	t.Helper()
	o := []EOption{WithEncoderConcurrency(1), WithEncoderLevel(SpeedFastest), WithWindowSize(4 << 20), WithEncoderCRC(crc)}
	o = append(o, opts...)
	e, err := NewWriter(nil, o...)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	return e.EncodeAll(raw, nil)
}
func runBounded(t *testing.T, frame, expected []byte, mode BoundedOutput, dict BorrowedDictionary) (calls int, peak, totalR uint64) {
	t.Helper()
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, e := NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatalf("new: %v %v", ok, e)
	}
	raw := []byte(nil)
	if mode == BoundedRaw {
		raw = make([]byte, 0, len(expected))
	}
	ok, e = d.Begin(frame, &frame, mode, raw, dict)
	if e != nil || !ok {
		t.Fatalf("begin: %v %v", ok, e)
	}
	var got []byte
	span := make([]byte, 8192)
	for calls = 0; calls < 200000; calls++ {
		w.quantum()
		n, done, e := d.Step(span)
		if w.usedR > 32 || w.usedB > 1<<20 {
			t.Fatal("quantum exceeded")
		}
		if e != nil {
			t.Fatalf("call %d phase %d: %v", calls, d.bounded.phase, e)
		}
		if mode == BoundedSpans {
			got = append(got, span[:n]...)
		}
		if done {
			if mode == BoundedRaw {
				got, e = d.RawResult()
				if e != nil {
					t.Fatal(e)
				}
			}
			break
		}
	}
	if calls == 200000 {
		t.Fatalf("no completion: phase %d", d.bounded.phase)
	}
	if !bytes.Equal(got, expected) {
		t.Fatalf("output mismatch: got %d want %d", len(got), len(expected))
	}
	peak = w.peak
	totalR = w.totalR
	for i := 0; i < 100000; i++ {
		w.historyProbes = 0
		w.quantum()
		ok, e := d.CloseStep()
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			if w.retained != BoundedControlSize() {
				t.Fatalf("retained after close: %d", w.retained)
			}
			return
		}
	}
	t.Fatal("close failed to progress")
	return
}
func TestBoundedOriginalProducerDifferential(t *testing.T) {
	rich := make([]byte, 2101248)
	x := uint32(1)
	for i := range rich {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		rich[i] = byte(x & 63)
		if i%2048 >= 1024 {
			rich[i] = rich[i-1024]
		}
	}
	leaf := bytes.Repeat([]byte("physical-leaf-prefix/anchor/tombstone/"), 28000)
	leaf = leaf[:255*4096]
	cases := []struct {
		name string
		data []byte
	}{{"empty", nil}, {"small", []byte("native leaf")}, {"rle", bytes.Repeat([]byte{7}, 150000)}, {"max-leaf", leaf}, {"oversized-rich", rich}, {"cross-segment-overlap", bytes.Repeat([]byte("abcdefghijklmnop"), 200000)}}
	for _, c := range cases {
		for _, crc := range []bool{false, true} {
			frame := boundedFixture(t, c.data, crc)
			ordinary, e := NewReader(nil, WithDecoderConcurrency(1))
			if e != nil {
				t.Fatal(e)
			}
			oracle, e := ordinary.DecodeAll(frame, nil)
			ordinary.Close()
			if e != nil || !bytes.Equal(oracle, c.data) {
				t.Fatal("ordinary oracle", e)
			}
			for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
				t.Run(fmt.Sprintf("%s/crc=%v/mode=%d", c.name, crc, mode), func(t *testing.T) {
					calls, peak, r := runBounded(t, frame, c.data, mode, BorrowedDictionary{})
					t.Logf("raw=%d frame=%d calls=%d R=%d peak=%d fixed=%d", len(c.data), len(frame), calls, r, peak, uint64(unsafe.Sizeof(Decoder{}))+uint64(unsafe.Sizeof(boundedDecoder{})))
				})
			}
		}
	}
}

func TestBoundedExistingDictionaryCorpus(t *testing.T) {
	zr := testCreateZipReader("testdata/dict-tests-small.zip", t)
	defs := readDicts(t, zr)
	dictionaries := map[uint32][]byte{}
	for _, def := range defs {
		dictionaries[binary.LittleEndian.Uint32(def[4:8])] = def
	}
	ordinary, e := NewReader(nil, WithDecoderConcurrency(1), WithDecoderDicts(defs...))
	if e != nil {
		t.Fatal(e)
	}
	defer ordinary.Close()
	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".zst") {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			r, e := f.Open()
			if e != nil {
				t.Fatal(e)
			}
			frame, e := io.ReadAll(r)
			r.Close()
			if e != nil {
				t.Fatal(e)
			}
			want, e := ordinary.DecodeAll(frame, nil)
			if e != nil {
				t.Fatal(e)
			}
			var h Header
			if e = h.Decode(frame); e != nil {
				t.Fatal(e)
			}
			def := dictionaries[h.DictionaryID]
			dict := BorrowedDictionary{}
			if len(def) > 0 {
				dict = BorrowedDictionary{ID: h.DictionaryID, Data: def, Owner: &def}
			}
			for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
				runBounded(t, frame, want, mode, dict)
			}
		})
	}
	rawDict := bytes.Repeat([]byte("retained original raw dictionary"), 4096)
	value := append([]byte(nil), rawDict[2345:]...)
	frame := boundedFixture(t, value, true, WithEncoderDictRaw(17, rawDict))
	runBounded(t, frame, value, BoundedSpans, BorrowedDictionary{ID: 17, Data: rawDict, Raw: true, Owner: &rawDict})
}

func TestBoundedPauseAndCancel(t *testing.T) {
	data := make([]byte, 600000)
	x := uint32(19)
	for i := range data[:40000] {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		data[i] = byte(x & 63)
	}
	for i := 40000; i < len(data); i++ {
		data[i] = data[i%40000]
	}
	frame := boundedFixture(t, data, true)
	for _, at := range []int{0, 1, 60, 63, 100, 1000, 2000} {
		t.Run(fmt.Sprint(at), func(t *testing.T) {
			w := &boundedTestWork{limit: 32 << 20}
			w.quantum()
			d, _, e := NewBoundedReader(w)
			if e != nil {
				t.Fatal(e)
			}
			d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
			span := make([]byte, 4096)
			for i := 0; i < at; i++ {
				w.quantum()
				_, done, e := d.Step(span)
				if e != nil {
					t.Fatal(e)
				}
				if done {
					break
				}
			}
			alreadyDone := d.bounded.ready
			owner := d.bounded.inputOwner
			w.refuse = true
			w.quantum()
			n, done, e := d.Step(span)
			if e != nil || n != 0 || (done && !alreadyDone) || d.bounded.inputOwner != owner {
				t.Fatalf("refusal performed effects: %d %v %v", n, done, e)
			}
			for i := 0; i < 10000; i++ {
				w.refuse = false
				w.quantum()
				w.r = 1
				ok, e := d.CloseStep()
				if e != nil {
					t.Fatal(e)
				}
				if ok {
					if w.retained != BoundedControlSize() {
						t.Fatal("retained", w.retained)
					}
					return
				}
			}
			t.Fatal("single-record close never progresses")
		})
	}
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, _, _ := NewBoundedReader(w)
	d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
	w.observed = d.bounded
	w.historyLimit = 1
	span := make([]byte, 4096)
	found := false
	for i := 0; i < 100000; i++ {
		w.historyProbes = 0
		w.quantum()
		_, done, e := d.Step(span)
		if e != nil {
			t.Fatal(e)
		}
		b := d.bounded
		if b.lookup.started && b.lookup.node != nil {
			node := b.lookup.node
			position := b.position
			w.refuse = true
			w.quantum()
			_, _, e = d.Step(span)
			if e != nil || b.lookup.node != node || b.position != position {
				t.Fatal("backward probe advanced on refusal", e)
			}
			w.refuse = false
			found = true
			break
		}
		if done {
			break
		}
	}
	if !found {
		t.Fatal("producer did not exercise suspended backward history probe")
	}
	w.observed = nil
	for {
		w.quantum()
		ok, e := d.CloseStep()
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			break
		}
	}
	if w.retained != BoundedControlSize() {
		t.Fatal("retained", w.retained)
	}
}
