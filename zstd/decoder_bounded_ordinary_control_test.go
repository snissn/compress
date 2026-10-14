package zstd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"runtime"
	"testing"
)

func BenchmarkBoundedOrdinaryCompatibilityControl(b *testing.B) {
	for _, name := range []string{"leaf255-full", "pointed-rich"} {
		frame, e := os.ReadFile("testdata/bounded-gomap/" + name + ".zstd")
		if e != nil {
			b.Fatal(e)
		}
		ordinary, e := NewReader(nil, WithDecoderConcurrency(1))
		if e != nil {
			b.Fatal(e)
		}
		raw, e := ordinary.DecodeAll(frame, nil)
		ordinary.Close()
		if e != nil {
			b.Fatal(e)
		}
		b.Run(name+"/DecodeAll-reuse", func(b *testing.B) {
			d, e := NewReader(nil, WithDecoderConcurrency(1))
			if e != nil {
				b.Fatal(e)
			}
			defer d.Close()
			dst := make([]byte, 0, len(raw))
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, e := d.DecodeAll(frame, dst[:0])
				if e != nil || len(got) != len(raw) {
					b.Fatal(e)
				}
			}
		})
		b.Run(name+"/stream-reuse", func(b *testing.B) {
			d, e := NewReader(nil, WithDecoderConcurrency(1))
			if e != nil {
				b.Fatal(e)
			}
			defer d.Close()
			input := bytes.NewReader(frame)
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				input.Reset(frame)
				if e := d.Reset(input); e != nil {
					b.Fatal(e)
				}
				n, e := io.Copy(io.Discard, d)
				if e != nil || n != int64(len(raw)) {
					b.Fatal(e)
				}
			}
		})
		b.Run(name+"/construct-decode-close", func(b *testing.B) {
			dst := make([]byte, 0, len(raw))
			b.SetBytes(int64(len(raw)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				d, e := NewReader(nil, WithDecoderConcurrency(1))
				if e != nil {
					b.Fatal(e)
				}
				got, e := d.DecodeAll(frame, dst[:0])
				d.Close()
				if e != nil || len(got) != len(raw) {
					b.Fatal(e)
				}
			}
		})
	}
}

// Run once per fresh pinned process. These ordinary complete-call capacities
// are diagnostic RED operands, not a test that weakens ordinary compatibility.
func TestBoundedOriginalProducerAllocationControl(t *testing.T) {
	name := os.Getenv("BOUNDED_ALLOCATION_FIXTURE")
	if name == "" {
		t.Skip("one explicit fresh-process fixture")
	}
	frame, e := os.ReadFile("testdata/bounded-gomap/" + name + ".zstd")
	if e != nil {
		t.Fatal(e)
	}
	var before, ctor, decoded runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	d, e := NewReader(nil, WithDecoderConcurrency(1))
	if e != nil {
		t.Fatal(e)
	}
	runtime.ReadMemStats(&ctor)
	raw, e := d.DecodeAll(frame, nil)
	if e != nil {
		t.Fatal(e)
	}
	runtime.ReadMemStats(&decoded)
	d.Close()
	runtime.KeepAlive(raw)
	fmt.Printf("ORIGINAL_ORDINARY fixture=%s stored=%d decoded=%d constructor_alloc_bytes=%d constructor_allocs=%d complete_decode_alloc_bytes=%d complete_decode_allocs=%d retained_output_cap=%d indivisible_original_decode_call=true\n", name, len(frame), len(raw), ctor.TotalAlloc-before.TotalAlloc, ctor.Mallocs-before.Mallocs, decoded.TotalAlloc-ctor.TotalAlloc, decoded.Mallocs-ctor.Mallocs, cap(raw))
	if len(raw) == 0 {
		t.Fatal("empty actual producer oracle")
	}
}
