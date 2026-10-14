package zstd

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func streamedOutcome(t *testing.T, frame []byte, chunk int) ([]byte, error) {
	t.Helper()
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, e := NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	defer closeBoundedForTest(t, &d, w)
	for {
		w.quantum()
		ok, e = d.BeginStream(&frame, BoundedSpans, nil, BorrowedDictionary{})
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			break
		}
	}
	span := make([]byte, 8192)
	pos := 0
	var got []byte
	for call := 0; call < 400000; call++ {
		w.quantum()
		if _, need := d.InputRequest(); need {
			end := min(pos+chunk, len(frame))
			n, _, e := d.FeedInput(frame[pos:end], end == len(frame))
			if e != nil {
				return got, e
			}
			pos += n
		} else {
			n, done, e := d.Step(span)
			got = append(got, span[:n]...)
			if d.bounded.boundInput != nil || cap(d.bounded.inputBuffer) != maxCompressedBlockSize+18 {
				t.Fatal("whole-frame retention")
			}
			if e != nil || done {
				if done && pos != len(frame) {
					t.Fatal("ignored input tail", pos, len(frame))
				}
				return got, e
			}
		}
		if w.usedR > 32 || w.usedB > 1<<20 {
			t.Fatal("fed quantum exceeded")
		}
	}
	t.Fatal("fed decoder stalled", pos, len(frame), d.bounded.phase, d.bounded.inputNeed)
	return nil, nil
}
func TestBoundedOriginalBlockInput(t *testing.T) {
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
	random := make([]byte, 2300000)
	for i := range random {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		random[i] = byte(x)
	}
	for _, c := range []struct {
		name  string
		data  []byte
		chunk int
	}{
		{"tiny-split-header", []byte("short frame"), 1},
		{"rich-compressed-blocks", rich, 4096},
		{"stored-input-over-two-MiB", random, 4096},
		{"RLE", bytes.Repeat([]byte{7}, 150000), 17},
	} {
		t.Run(c.name, func(t *testing.T) {
			frame := boundedFixture(t, c.data, true)
			got, e := streamedOutcome(t, frame, c.chunk)
			if e != nil || !bytes.Equal(got, c.data) {
				t.Fatal("fed oracle", e, len(got), len(c.data))
			}
			t.Logf("decoded=%d stored=%d inputCapacity=%d chunk=%d", len(c.data), len(frame), maxCompressedBlockSize+18, c.chunk)
		})
	}
	a := boundedFixture(t, bytes.Repeat([]byte("first frame"), 15000), true)
	b := boundedFixture(t, []byte("second"), true)
	skip := make([]byte, 8+9000)
	binary.LittleEndian.PutUint32(skip, 0x184d2a50)
	binary.LittleEndian.PutUint32(skip[4:], 9000)
	full := append(append(append([]byte(nil), a...), skip...), b...)
	got, e := streamedOutcome(t, full, 17)
	if e != nil || !bytes.Equal(got, append(bytes.Repeat([]byte("first frame"), 15000), []byte("second")...)) {
		t.Fatal("fed multiframe/skippable", e)
	}
	_, e = streamedOutcome(t, a[:len(a)-1], 17)
	if !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatal("fed truncation", e)
	}
	tail := append(append([]byte(nil), a...), 1)
	_, e = streamedOutcome(t, tail, 17)
	if !errors.Is(e, io.ErrUnexpectedEOF) {
		t.Fatal("fed tail", e)
	}
}

func TestBoundedStreamRefusalResetAndCancel(t *testing.T) {
	raw := bytes.Repeat([]byte("actual original frame reset/cancel"), 5000)
	frame := boundedFixture(t, raw, true)
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, e := NewBoundedReader(w)
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	w.quantum()
	w.limit = w.retained
	ok, e = d.BeginStream(&frame, BoundedSpans, nil, BorrowedDictionary{})
	if ok || !errors.Is(e, ErrWorkRefused) || d.bounded.inputBuffer != nil {
		t.Fatal("input capacity refusal effects")
	}
	w.limit = 32 << 20
	w.quantum()
	ok, e = d.BeginStream(&frame, BoundedSpans, nil, BorrowedDictionary{})
	if e != nil || !ok {
		t.Fatal(ok, e)
	}
	span := make([]byte, 8192)
	// First Step performs the real cold table build; pause until the header needs bytes.
	for i := 0; i < 1000; i++ {
		w.quantum()
		_, _, e = d.Step(span)
		if e != nil {
			t.Fatal(e)
		}
		if _, need := d.InputRequest(); need {
			break
		}
	}
	if _, need := d.InputRequest(); !need {
		t.Fatal("header not suspended")
	}
	before := len(d.bounded.in)
	needBefore := d.bounded.inputNeed
	w.quantum()
	w.refuse = true
	n, _, e := d.FeedInput(frame[:1], false)
	if e != nil || n != 0 || len(d.bounded.in) != before || d.bounded.inputNeed != needBefore {
		t.Fatal("input refusal advanced")
	}
	w.refuse = false
	w.quantum()
	n, _, e = d.FeedInput(frame[:1], false)
	if e != nil || n != 1 {
		t.Fatal("partial feed", n, e)
	}
	original := d.bounded
	tables := original.predef
	for i := 0; i < 10000; i++ {
		w.quantum()
		ok, e = d.ResetStreamStep(&frame, BoundedSpans, nil, BorrowedDictionary{})
		if e != nil {
			t.Fatal(e)
		}
		if ok {
			break
		}
		if i == 9999 {
			t.Fatal("stream reset stalled")
		}
	}
	if d.bounded != original || original.predef != tables || !original.coldReady || len(original.in) != 0 {
		t.Fatal("stream reset lost same table owner")
	}
	// Exercise cancellation after a real block span is partly filled.
	pos := 0
	for i := 0; i < 10000; i++ {
		w.quantum()
		if _, need := d.InputRequest(); need {
			end := min(pos+17, len(frame))
			n, _, e = d.FeedInput(frame[pos:end], end == len(frame))
			if e != nil {
				t.Fatal(e)
			}
			pos += n
			if pos > 20 {
				break
			}
		} else {
			_, _, e = d.Step(span)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if pos <= 20 {
		t.Fatal("partial block not exercised")
	}
	closeBoundedForTest(t, &d, w)
	corrupt := append([]byte(nil), frame...)
	corrupt[len(corrupt)-1] ^= 1
	_, e = streamedOutcome(t, corrupt, 17)
	if !errors.Is(e, ErrCRCMismatch) {
		t.Fatal("stream checksum corruption", e)
	}
}

// A legal skippable length spans the uint32 wire range even on 386.
func TestBoundedSkippableUint32Length(t *testing.T) {
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, err := NewBoundedReader(w)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer closeBoundedForTest(t, &d, w)
	frame := make([]byte, 8)
	binary.LittleEndian.PutUint32(frame, 0x184d2a50)
	binary.LittleEndian.PutUint32(frame[4:], ^uint32(0))
	ok, err = d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	for calls := 0; calls < 10000; calls++ {
		w.quantum()
		_, done, err := d.Step(nil)
		if err != nil {
			if err != io.ErrUnexpectedEOF || d.bounded.skipRemaining != uint64(^uint32(0)) {
				t.Fatal(err, d.bounded.skipRemaining)
			}
			return
		}
		if done {
			t.Fatal("accepted truncated skippable payload")
		}
	}
	t.Fatal("skippable did not progress")
}
