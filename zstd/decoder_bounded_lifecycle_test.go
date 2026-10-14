package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/snissn/compress/internal/decoderwork"
	"io"
	"testing"
	"unsafe"
)

func closeBoundedForTest(t *testing.T, d **Decoder, w *boundedTestWork) {
	t.Helper()
	for i := 0; i < 100000; i++ {
		w.quantum()
		w.r = 1
		ok, e := (*d).CloseStep()
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			if w.retained != BoundedControlSize() {
				t.Fatalf("reachable control refunded: %d", w.retained)
			}
			*d = nil // enclosing last-owner retirement, separate from library CloseStep.
			w.Release(BoundedControlSize())
			if w.retained != 0 {
				t.Fatal("retained after final edge", w.retained)
			}
			return
		}
	}
	t.Fatal("close did not progress")
}
func TestBoundedResetSameOwner(t *testing.T) {
	raw := bytes.Repeat([]byte("same original history/table owner!"), 9000)
	frame := boundedFixture(t, raw, true)
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, e := NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	core := d.bounded
	fixed := decoderwork.AllocationCharge(uint64(unsafe.Sizeof(Decoder{}))) + decoderwork.AllocationCharge(uint64(unsafe.Sizeof(boundedDecoder{})))
	ok, e = d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	span := make([]byte, 8192)
	decode := func() {
		t.Helper()
		var got []byte
		for i := 0; i < 100000; i++ {
			w.quantum()
			n, done, e := d.Step(span)
			if e != nil {
				t.Fatal(e)
			}
			got = append(got, span[:n]...)
			if done {
				if !bytes.Equal(got, raw) {
					t.Fatal("reset output")
				}
				return
			}
		}
		t.Fatal("decode stalled")
	}
	decode()
	if !core.coldReady {
		t.Fatal("cold tables not complete")
	}
	tables := core.predef
	before := w.totalR
	for i := 0; i < 100000; i++ {
		w.quantum()
		w.r = 3
		ok, e = d.ResetStep(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			break
		}
		if i == 99999 {
			t.Fatal("reset stalled")
		}
	}
	if d.bounded != core || core.predef != tables || core.coldTable != 3 || w.retained != fixed {
		t.Fatalf("reuse changed owner/tables/admission: cold=%d retained=%d fixed=%d", core.coldTable, w.retained, fixed)
	}
	if w.totalR-before >= 1905 {
		t.Fatal("reset repeated cold construction")
	}
	decode()
	closeBoundedForTest(t, &d, w)
}
func TestBoundedAdmissionAndOutputBinding(t *testing.T) {
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	w.refuse = true
	d, ok, e := NewBoundedReader(w)
	if d != nil || ok || e != nil || w.retained != 0 || w.totalR != 0 {
		t.Fatal("constructor refusal effects")
	}
	w.refuse = false
	w.limit = 1
	d, ok, e = NewBoundedReader(w)
	if !errors.Is(e, ErrWorkRefused) || ok || d != nil || w.retained != 0 {
		t.Fatal("capacity refusal effects")
	}
	w.limit = 32 << 20
	w.quantum()
	d, ok, e = NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	frame := boundedFixture(t, bytes.Repeat([]byte("immutable bytes"), 3000), true)
	w.quantum()
	ok, e = d.Begin(frame, &frame, BoundedRaw, frame[:0], BorrowedDictionary{})
	if !errors.Is(e, ErrOutputOverlap) || ok || d.bounded.begun {
		t.Fatal("overlap bound", ok, e)
	}
	w.quantum()
	ok, e = d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	saved := append([]byte(nil), frame...)
	w.quantum()
	n, done, e := d.Step(frame)
	if !errors.Is(e, ErrOutputOverlap) || n != 0 || done || !bytes.Equal(frame, saved) {
		t.Fatal("span overlap effects", n, done, e)
	}
	closeBoundedForTest(t, &d, w)
}
func boundedOutcome(t *testing.T, frame []byte) ([]byte, error) {
	t.Helper()
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, e := NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	defer closeBoundedForTest(t, &d, w)
	ok, e = d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	span := make([]byte, 8192)
	var got []byte
	for i := 0; i < 200000; i++ {
		w.quantum()
		n, done, e := d.Step(span)
		got = append(got, span[:n]...)
		if e != nil || done {
			return got, e
		}
	}
	t.Fatal("no outcome")
	return nil, nil
}
func TestBoundedOriginalFramingFailures(t *testing.T) {
	raw := bytes.Repeat([]byte("actual checksum and framing"), 5000)
	a := boundedFixture(t, raw, true)
	b := boundedFixture(t, []byte("second frame"), true)
	skip := make([]byte, 8+17)
	binary.LittleEndian.PutUint32(skip, 0x184d2a50)
	binary.LittleEndian.PutUint32(skip[4:], 17)
	full := append(append(append([]byte(nil), a...), skip...), b...)
	got, e := boundedOutcome(t, full)
	if e != nil || !bytes.Equal(got, append(append([]byte(nil), raw...), []byte("second frame")...)) {
		t.Fatal("multi/skippable", e)
	}
	for _, cut := range []int{1, 2, 4, 7, len(a) / 2, len(a) - 4, len(a) - 1} {
		t.Run("truncate", func(t *testing.T) {
			_, e := boundedOutcome(t, a[:cut])
			if e == nil {
				t.Fatal("accepted truncated frame", cut)
			}
		})
	}
	corrupt := append([]byte(nil), a...)
	corrupt[len(corrupt)-1] ^= 1
	_, e = boundedOutcome(t, corrupt)
	if !errors.Is(e, ErrCRCMismatch) {
		t.Fatal("CRC", e)
	}
	tail := append(append([]byte(nil), a...), 1)
	_, e = boundedOutcome(t, tail)
	if !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatal("tail", e)
	}
}
func TestBoundedRLEExpansionPause(t *testing.T) {
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, e := NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	b := d.bounded
	b.output = BoundedRaw
	b.raw = make([]byte, 0, 4096)
	b.header.HasFCS = false
	w.quantum()
	w.r = 1
	n, e := b.emitRLE(nil, 4096, 77)
	if n != 0 || e != nil || b.rlePrepared != 4096 {
		t.Fatal("expansion pause", n, e, b.rlePrepared)
	}
	before := b.rleScratch
	w.quantum()
	w.refuse = true
	n, e = b.emitRLE(nil, 4096, 77)
	if n != 0 || e != nil || b.rleScratch != before || b.rlePrepared != 4096 {
		t.Fatal("paused expansion repeated")
	}
	w.refuse = false
	w.quantum()
	w.r = 2
	n, e = b.emitRLE(nil, 4096, 77)
	if n != 4096 || e != nil || b.rlePrepared != 0 || !bytes.Equal(b.raw, bytes.Repeat([]byte{77}, 4096)) {
		t.Fatal("continuation", n, e)
	}
	closeBoundedForTest(t, &d, w)
}
