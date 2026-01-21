package zstd

import (
	"bytes"
	"math/rand"
	"sync"
	"testing"
)

func TestEncodeAllPrewarmedMatchesDecode(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	dictID := uint32(1)
	dictContent := makeRepeat(8 << 10)

	enc, err := NewWriter(nil, WithEncoderDictRaw(dictID, dictContent))
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	defer enc.closePrewarm()
	dec, err := NewReader(nil, WithDecoderDictRaw(dictID, dictContent))
	if err != nil {
		t.Fatalf("new decoder: %v", err)
	}

	inputs := [][]byte{
		makePattern(rng, 1<<10),
		makePattern(rng, 256<<10),
	}

	for i, input := range inputs {
		encoded := enc.EncodeAllPrewarmed(input, nil)
		decoded, err := dec.DecodeAll(encoded, nil)
		if err != nil {
			t.Fatalf("decode prewarmed[%d]: %v", i, err)
		}
		if !bytes.Equal(decoded, input) {
			t.Fatalf("decode prewarmed[%d] mismatch", i)
		}

		encodedStd := enc.EncodeAll(input, nil)
		decodedStd, err := dec.DecodeAll(encodedStd, nil)
		if err != nil {
			t.Fatalf("decode standard[%d]: %v", i, err)
		}
		if !bytes.Equal(decodedStd, input) {
			t.Fatalf("decode standard[%d] mismatch", i)
		}
	}
}

func TestEncodeAllPrewarmedConcurrent(t *testing.T) {
	dictID := uint32(1)
	dictContent := makeRepeat(8 << 10)

	enc, err := NewWriter(nil, WithEncoderDictRaw(dictID, dictContent), WithEncoderConcurrency(4))
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	defer enc.closePrewarm()

	input := makePattern(rand.New(rand.NewSource(2)), 64<<10)

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dec, err := NewReader(nil, WithDecoderDictRaw(dictID, dictContent))
			if err != nil {
				errs <- err
				return
			}
			for j := 0; j < 50; j++ {
				encoded := enc.EncodeAllPrewarmed(input, nil)
				decoded, err := dec.DecodeAll(encoded, nil)
				if err != nil {
					errs <- err
					return
				}
				if !bytes.Equal(decoded, input) {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func makePattern(rng *rand.Rand, size int) []byte {
	buf := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz012345"), (size/32)+1)
	out := make([]byte, size)
	copy(out, buf[:size])
	if size > 64 {
		rng.Read(out[size-64:])
	}
	return out
}

func makeRepeat(size int) []byte {
	pattern := []byte("abcdefghijklmnopqrstuvwxyz012345")
	out := make([]byte, size)
	for i := 0; i < size; i += len(pattern) {
		copy(out[i:], pattern)
	}
	return out
}
