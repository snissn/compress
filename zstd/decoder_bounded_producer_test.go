package zstd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/snissn/compress/internal/decoderwork"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// Actual gomap Writer frames, not codec helper images. The complete recipe and
// immutable identities are in testdata/bounded-gomap/receipts.json. Test oracle
// setup is outside the operation; the exercised file reads/copies are charged.
func TestBoundedActualGomapProducerFrames(t *testing.T) {
	var receipt struct {
		Fixtures []struct {
			Name       string `json:"name"`
			RawBytes   int    `json:"raw_bytes"`
			RawSHA     string `json:"raw_sha256"`
			FrameBytes int    `json:"frame_bytes"`
			FrameSHA   string `json:"frame_sha256"`
			DictName   string `json:"dict_name"`
			DictID     uint32 `json:"dict_id"`
			DictSHA    string `json:"dict_sha256"`
		} `json:"fixtures"`
	}
	data, err := os.ReadFile("testdata/bounded-gomap/receipts.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, c := range receipt.Fixtures {
		path := filepath.Join("testdata/bounded-gomap", c.Name+".zstd")
		frame, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(frame)
		if len(frame) != c.FrameBytes || hex.EncodeToString(sum[:]) != c.FrameSHA {
			t.Fatal("frame identity drift")
		}
		var definition []byte
		options := []DOption{WithDecoderConcurrency(1)}
		if c.DictName != "" {
			definition, err = os.ReadFile(filepath.Join("testdata/bounded-gomap", c.DictName))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(definition)
			if hex.EncodeToString(sum[:]) != c.DictSHA {
				t.Fatal("dictionary identity drift")
			}
			options = append(options, WithDecoderDicts(definition))
		}
		ordinary, err := NewReader(nil, options...)
		if err != nil {
			t.Fatal(err)
		}
		oracle, err := ordinary.DecodeAll(frame, nil)
		ordinary.Close()
		if err != nil {
			t.Fatal(err)
		}
		sum = sha256.Sum256(oracle)
		if len(oracle) != c.RawBytes || hex.EncodeToString(sum[:]) != c.RawSHA {
			t.Fatal("original writer/oracle identity drift")
		}
		for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
			if mode == BoundedRaw && c.RawBytes > 1<<20 {
				continue
			} // Complete Raw is the admitted leaf-group route; oversized file output uses Spans.
			t.Run(c.Name+"/"+[]string{"raw", "spans"}[mode], func(t *testing.T) {
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				w := &boundedTestWork{limit: 32 << 20}
				w.quantum()
				// Real caller-owned I/O and output containing capacities, admitted before make.
				callerSpans := 2 * decoderwork.AllocationCharge(4096)
				if !w.Reserve(2, callerSpans) || !w.Retain(callerSpans) {
					t.Fatal("caller span admission")
				}
				var got []byte
				input := make([]byte, 4096)
				span := make([]byte, 4096)
				defer func() { input = nil; span = nil; got = nil; w.Release(callerSpans) }()
				var raw []byte
				rawCharge := decoderwork.AllocationCharge(uint64(c.RawBytes))
				dictCharge := decoderwork.AllocationCharge(uint64(cap(definition)))
				if mode == BoundedRaw {
					w.quantum()
					if !w.Reserve(1, rawCharge) || !w.Retain(rawCharge) {
						t.Fatal("caller whole-group output admission")
					}
					raw = make([]byte, 0, c.RawBytes)
					defer func() { raw = nil; got = nil; w.Release(rawCharge) }()
				}
				w.quantum()
				if len(definition) > 0 {
					if !w.Retain(dictCharge) {
						t.Fatal("dictionary containing admission")
					}
					defer w.Release(dictCharge)
				}
				d, ok, err := NewBoundedReader(w)
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				w.phaseSource = &d.bounded.phase
				defer func() {
					w.phaseSource = nil // release the test observer edge before continuation retirement
					baseline := callerSpans + dictCharge
					if mode == BoundedRaw {
						baseline += rawCharge
					}
					for turn := 0; turn < 10000; turn++ {
						w.quantum()
						done, e := d.CloseStep()
						if e != nil {
							t.Fatal(e)
						}
						if done {
							break
						}
						if turn == 9999 {
							t.Fatal("close stalled")
						}
					}
					if w.retained != baseline+BoundedControlSize() {
						t.Fatal("close retired a caller alias", w.retained, baseline)
					}
					d = nil
					w.Release(BoundedControlSize())
				}()
				w.quantum()
				ok, err = d.BeginStream(f, mode, raw, BorrowedDictionary{ID: c.DictID, Data: definition, Owner: &definition})
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
				pos, have, at, calls, reads := 0, 0, 0, 0, 0
				for ; calls < 200000; calls++ {
					w.quantum()
					need, wanted := d.InputRequest()
					if wanted {
						if at == have {
							have = min(need, len(input), c.FrameBytes-pos)
							at = 0
							if have == 0 {
								_, _, err = d.FeedInput(nil, true)
							} else {
								if !w.Reserve(1, uint64(have)) {
									t.Fatal("test I/O quantum too small")
								}
								n, e := f.ReadAt(input[:have], int64(pos))
								if e != nil && e != io.EOF {
									t.Fatal(e)
								}
								if n != have {
									t.Fatal("short fixture read")
								}
								reads++
								pos += n
							}
						}
						if at < have {
							n, _, e := d.FeedInput(input[at:have], pos == c.FrameBytes)
							at += n
							err = e
						}
					} else {
						n, done, e := d.Step(span)
						err = e
						if mode == BoundedSpans {
							got = append(got, span[:n]...)
						}
						if done {
							if mode == BoundedRaw {
								got, err = d.RawResult()
							}
							break
						}
					}
					if err != nil {
						t.Fatal("actual frame continuation", err, calls, d.bounded.phase)
					}
					if w.usedR > 32 || w.usedB > 1<<20 {
						t.Fatal("actual read/feed/decode quantum overrun")
					}
				}
				if err != nil || calls == 200000 || !bytes.Equal(got, oracle) {
					t.Fatal("producer differential", err, calls, len(got))
				}
				for phase := range w.phaseR {
					if w.phaseR[phase] != 0 || w.phaseB[phase] != 0 {
						t.Logf("phase=%d R=%d B=%d (199=enclosing preparation, includes caller read/feed while suspended)", phase, w.phaseR[phase], w.phaseB[phase])
					}
				}
				t.Logf("raw=%d stored=%d mode=%d calls=%d R=%d B=%d peakConservativeAdmission=%d callerReadOperations=%d; verification collector/oracle excluded, caller dictionary is a borrowed operation edge rather than physical definition death", c.RawBytes, c.FrameBytes, mode, calls+1, w.totalR, w.totalB, w.peak, reads)
			})
		}
	}
}
