package zstd

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestEncoder_EncodeAllParts_RoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))

	parts := make([][]byte, 32)
	total := 0
	for i := range parts {
		n := rng.Intn(4096)
		if rng.Intn(8) == 0 {
			n = 0
		}
		b := make([]byte, n)
		if _, err := rng.Read(b); err != nil {
			t.Fatalf("rng.Read: %v", err)
		}
		parts[i] = b
		total += n
	}

	want := make([]byte, 0, total)
	for i := range parts {
		want = append(want, parts[i]...)
	}

	enc, err := NewWriter(nil, WithEncoderCRC(false), WithEncoderConcurrency(1))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer enc.Close()

	dec, err := NewReader(nil, WithDecoderConcurrency(1))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer dec.Close()

	encoded := enc.EncodeAllParts(parts, nil)
	got, err := dec.DecodeAll(encoded, nil)
	if err != nil {
		t.Fatalf("DecodeAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip mismatch: got=%d want=%d", len(got), len(want))
	}
}

func TestEncoder_EncodeAllParts_RoundTrip_WithDict(t *testing.T) {
	rng := rand.New(rand.NewSource(2))

	// Create a simple, deterministic training set.
	samples := make([][]byte, 128)
	for i := range samples {
		n := 256 + rng.Intn(512)
		b := make([]byte, n)
		copy(b, bytes.Repeat([]byte("{\"k\":\"v\",\"n\":"), (n/16)+1))
		for j := 0; j < n; j += 64 {
			_, _ = rng.Read(b[j:minInt(j+32, n)])
		}
		samples[i] = b
	}
	history := make([]byte, 0, 40<<10)
	for i := range samples {
		if len(history) >= cap(history) {
			break
		}
		need := cap(history) - len(history)
		if len(samples[i]) > need {
			history = append(history, samples[i][:need]...)
		} else {
			history = append(history, samples[i]...)
		}
	}
	if len(history) < 8 {
		t.Fatalf("history too small: %d", len(history))
	}

	dict, err := BuildDict(BuildDictOptions{
		ID:       123,
		Contents: samples,
		History:  history,
		Level:    SpeedFastest,
	})
	if err != nil {
		t.Fatalf("BuildDict: %v", err)
	}

	parts := make([][]byte, 16)
	total := 0
	for i := range parts {
		b := samples[(i*7)%len(samples)]
		parts[i] = b
		total += len(b)
	}

	want := make([]byte, 0, total)
	for i := range parts {
		want = append(want, parts[i]...)
	}

	enc, err := NewWriter(nil,
		WithEncoderCRC(false),
		WithEncoderConcurrency(1),
		WithEncoderDict(dict),
		WithEncoderLevel(SpeedFastest),
	)
	if err != nil {
		t.Fatalf("NewWriter(dict): %v", err)
	}
	defer enc.Close()

	dec, err := NewReader(nil, WithDecoderConcurrency(1), WithDecoderDicts(dict))
	if err != nil {
		t.Fatalf("NewReader(dict): %v", err)
	}
	defer dec.Close()

	encoded := enc.EncodeAllParts(parts, nil)
	got, err := dec.DecodeAll(encoded, nil)
	if err != nil {
		t.Fatalf("DecodeAll(dict): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip mismatch: got=%d want=%d", len(got), len(want))
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
