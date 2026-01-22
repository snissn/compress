package zstd

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

var benchSink []byte

func BenchmarkEncodeAllPrewarmed_DictResetCost_1KiB(b *testing.B) {
	// This benchmark is intended to isolate the hot-path cost of repeatedly
	// encoding small independent frames when a dictionary is configured.
	//
	// - EncodeAll (baseline): pays encoder.Reset(dict, ...) on the hot path.
	// - EncodeAllPrewarmed: uses a clean/dirty pool where Reset happens on a
	//   background goroutine, allowing overlap once concurrency > 1.
	//
	// Run:
	//   go test ./zstd -bench EncodeAllPrewarmed_DictResetCost_1KiB -benchmem -run ^$
	dictID := uint32(1)
	src := bytes.Repeat([]byte("a"), 1024)

	// Use a "real" dictionary (zstd format) to match TreeDB usage and to
	// exercise the Reset(dict, ...) cost that motivated EncodeAllPrewarmed.
	//
	// NOTE: We intentionally build this once outside the benchmark timing.
	history := makeRepeat(40 << 10) // matches TreeDB default dict size (40KiB)
	rng := rand.New(rand.NewSource(1))
	samples := make([][]byte, 256)
	for i := range samples {
		samples[i] = makePattern(rng, 1024)
	}
	dictBytes, err := BuildDict(BuildDictOptions{
		ID:       dictID,
		History:  history,
		Contents: samples,
		Offsets:  [3]int{1, 4, 8},
		Level:    SpeedFastest,
	})
	if err != nil {
		b.Fatalf("build dict: %v", err)
	}

	stdConcs := []int{1, 2, 4, 8, 16}
	prewarmConcs := []int{1, 2, 4, 8, 16}

	for _, stdConc := range stdConcs {
		stdConc := stdConc
		b.Run(fmt.Sprintf("EncodeAll/std=%d", stdConc), func(b *testing.B) {
			enc, err := NewWriter(nil,
				WithEncoderDict(dictBytes),
				WithEncoderLevel(SpeedFastest),
				WithEncoderConcurrency(stdConc),
				WithEncoderCRC(false),
				WithNoEntropyCompression(true),
			)
			if err != nil {
				b.Fatalf("new encoder: %v", err)
			}
			defer enc.closePrewarm()

			dst := make([]byte, 0, enc.MaxEncodedSize(len(src)))
			// Ensure init cost is not included in the benchmark.
			dst = enc.EncodeAll(src, dst[:0])
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				dst = enc.EncodeAll(src, dst[:0])
			}
			b.StopTimer()
			benchSink = dst
		})

		for _, prewarmConc := range prewarmConcs {
			prewarmConc := prewarmConc
			b.Run(fmt.Sprintf("EncodeAllPrewarmed/std=%d/prewarm=%d", stdConc, prewarmConc), func(b *testing.B) {
				enc, err := NewWriter(nil,
					WithEncoderDict(dictBytes),
					WithEncoderLevel(SpeedFastest),
					WithEncoderConcurrency(stdConc),
					WithEncoderPrewarmConcurrency(prewarmConc),
					WithEncoderCRC(false),
					WithNoEntropyCompression(true),
				)
				if err != nil {
					b.Fatalf("new encoder: %v", err)
				}
				defer enc.closePrewarm()

				dst := make([]byte, 0, enc.MaxEncodedSize(len(src)))
				// Ensure init + prewarm cost is not included in the benchmark.
				dst = enc.EncodeAllPrewarmed(src, dst[:0])
				b.ReportAllocs()
				b.SetBytes(int64(len(src)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					dst = enc.EncodeAllPrewarmed(src, dst[:0])
				}
				b.StopTimer()
				benchSink = dst
			})
		}
	}
}
