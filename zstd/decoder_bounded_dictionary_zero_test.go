package zstd

import (
	"bytes"
	"testing"
)

func TestBoundedOriginalRawDictionaryZeroID(t *testing.T) {
	dictionary := make([]byte, 90000)
	x := uint32(731)
	for i := range dictionary {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		dictionary[i] = byte(x)
	}
	expected := dictionary[12000:82000]
	frame := boundedFixture(t, expected, true, WithEncoderDictRaw(0, dictionary))
	original, err := NewReader(nil, WithDecoderConcurrency(1), WithDecoderDictRaw(0, dictionary))
	if err != nil {
		t.Fatal(err)
	}
	oracle, err := original.DecodeAll(frame, nil)
	original.Close()
	if err != nil || !bytes.Equal(oracle, expected) {
		t.Fatal("ordinary zero-ID oracle", err)
	}
	noDictionary, _ := NewReader(nil, WithDecoderConcurrency(1))
	_, err = noDictionary.DecodeAll(frame, nil)
	noDictionary.Close()
	if err == nil {
		t.Fatal("fixture does not require original zero-ID dictionary")
	}
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		runBounded(t, frame, expected, mode, BorrowedDictionary{ID: 0, Data: dictionary, Raw: true, Owner: &dictionary})
	}
}

func TestBoundedRawDictionaryMustMatchFrameID(t *testing.T) {
	dictionary := make([]byte, 90000)
	x := uint32(731)
	for i := range dictionary {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		dictionary[i] = byte(x)
	}
	expected := dictionary[12000:82000]
	frame := boundedFixture(t, expected, true, WithEncoderDictRaw(0, dictionary))
	original, _ := NewReader(nil, WithDecoderConcurrency(1), WithDecoderDictRaw(17, dictionary))
	_, err := original.DecodeAll(frame, nil)
	original.Close()
	if err == nil {
		t.Fatal("ordinary mismatched dictionary accepted")
	}
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, err := NewBoundedReader(w)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer closeBoundedForTest(t, &d, w)
	ok, err = d.Begin(frame, &frame, BoundedSpans, nil, BorrowedDictionary{ID: 17, Data: dictionary, Raw: true, Owner: &dictionary})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	span := make([]byte, 4096)
	for calls := 0; calls < 10000; calls++ {
		w.quantum()
		_, done, err := d.Step(span)
		if err != nil {
			return
		}
		if done {
			t.Fatal("bounded decoder used an unselected dictionary")
		}
	}
	t.Fatal("mismatched dictionary stalled")
}
