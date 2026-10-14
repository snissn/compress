package zstd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"testing"
)

func TestBoundedDictionarySelectionDefinition(t *testing.T) {
	// A public raw-block frame carrying dictionary ID 73. The block does not
	// reference dictionary history; frame selection itself must require it.
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x21, 73, 4, 0x21, 0, 0, 't', 'e', 's', 't'}
	withoutID := append([]byte{0x28, 0xb5, 0x2f, 0xfd, 0x20}, frame[6:]...)
	definition, err := os.ReadFile("testdata/bounded-gomap/leaf255-dictionary.dict")
	if err != nil {
		t.Fatal(err)
	}
	differentDefinition := append([]byte(nil), definition...)
	differentDefinition[4] = 72 // Complete binary definition with a different frame ID.
	cases := []struct {
		name  string
		frame []byte
		dict  BorrowedDictionary
		opts  []DOption
	}{
		{"binary", frame, BorrowedDictionary{ID: 73, Data: definition, Owner: &definition}, []DOption{WithDecoderDicts(definition)}},
		{"binary-id-only-nil", frame, BorrowedDictionary{ID: 73}, nil},
		{"binary-id-only-empty", frame, BorrowedDictionary{ID: 73, Data: []byte{}}, nil},
		{"raw-empty-nil", frame, BorrowedDictionary{ID: 73, Raw: true}, []DOption{WithDecoderDictRaw(73, nil)}},
		{"raw-empty-slice", frame, BorrowedDictionary{ID: 73, Raw: true, Data: []byte{}}, []DOption{WithDecoderDictRaw(73, []byte{})}},
		{"raw-mismatch", frame, BorrowedDictionary{ID: 72, Raw: true}, []DOption{WithDecoderDictRaw(72, nil)}},
		{"binary-mismatch", frame, BorrowedDictionary{ID: 72, Data: differentDefinition, Owner: &differentDefinition}, []DOption{WithDecoderDicts(differentDefinition)}},
		{"absent", frame, BorrowedDictionary{}, nil},
		{"no-id-absent", withoutID, BorrowedDictionary{}, nil},
		{"no-id-binary-id-only", withoutID, BorrowedDictionary{ID: 73}, nil},
		{"no-id-raw-empty", withoutID, BorrowedDictionary{Raw: true}, []DOption{WithDecoderDictRaw(0, nil)}},
		{"no-id-raw-different-id", withoutID, BorrowedDictionary{ID: 73, Raw: true}, []DOption{WithDecoderDictRaw(73, nil)}},
		{"binary-id-only-after-reset", frame, BorrowedDictionary{ID: 73}, nil},
	}
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		for _, fed := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode%d/fed%v", mode, fed), func(t *testing.T) {
				w := &boundedTestWork{limit: 32 << 20}
				w.quantum()
				d, ok, e := NewBoundedReader(w)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				defer closeBoundedForTest(t, &d, w)
				for i, c := range cases {
					t.Run(c.name, func(t *testing.T) {
						ordinary, e := NewReader(nil, append([]DOption{WithDecoderConcurrency(1)}, c.opts...)...)
						if e != nil {
							t.Fatal(e)
						}
						want, oracleErr := ordinary.DecodeAll(c.frame, nil)
						ordinary.Close()
						if oracleErr != nil && !errors.Is(oracleErr, ErrUnknownDictionary) {
							t.Fatal("unexpected ordinary error", oracleErr)
						}
						if i != 0 {
							// Refused cleanup must preserve capacity and the SAME operands.
							w.quantum()
							w.refuse = true
							retained, r, b := w.retained, w.r, w.b
							var ok bool
							if fed {
								ok, e = d.ResetStreamStep(&c.frame, mode, nil, c.dict)
							} else {
								ok, e = d.ResetStep(c.frame, &c.frame, mode, nil, c.dict)
							}
							w.refuse = false
							if ok || e != nil || w.retained != retained || w.r != r || w.b != b {
								t.Fatal("refused reset changed custody or work", ok, e)
							}
						}
						got, boundedErr := multiFrameOutcome(t, d, w, c.frame, c.dict, mode, fed, i != 0)
						t.Logf("ordinary error=%v output=%q; bounded error=%v output=%q", oracleErr, want, boundedErr, got)
						if !errors.Is(boundedErr, oracleErr) || (oracleErr == nil && !bytes.Equal(got, want)) || (oracleErr != nil && len(got) != 0) {
							t.Errorf("dictionary selection diverged from ordinary oracle")
						}
					})
				}
				w.quantum()
				w.refuse = true
				retained, r, b := w.retained, w.r, w.b
				ok, e = d.CloseStep()
				w.refuse = false
				if ok || e != nil || w.retained != retained || w.r != r || w.b != b {
					t.Fatal("refused close changed custody or work", ok, e)
				}
			})
		}
	}
}

func TestBoundedEmptyDictionaryColdRefusal(t *testing.T) {
	frame := []byte{0x28, 0xb5, 0x2f, 0xfd, 0x21, 73, 4, 0x21, 0, 0, 't', 'e', 's', 't'}
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		for _, fed := range []bool{false, true} {
			for _, rawDict := range []bool{false, true} {
				t.Run(fmt.Sprintf("mode%d/fed%v/rawdict%v", mode, fed, rawDict), func(t *testing.T) {
					w := &boundedTestWork{limit: 32 << 20}
					w.quantum()
					d, ok, e := NewBoundedReader(w)
					if !ok || e != nil {
						t.Fatal(ok, e)
					}
					defer closeBoundedForTest(t, &d, w)
					raw := bytes.Repeat([]byte{0xa5}, 4)
					dict := BorrowedDictionary{ID: 73, Raw: rawDict}
					for i := 0; i < 10000; i++ {
						w.quantum()
						if fed {
							ok, e = d.BeginStream(&frame, mode, raw[:0], dict)
						} else {
							ok, e = d.Begin(frame, &frame, mode, raw[:0], dict)
						}
						if e != nil || ok {
							break
						}
					}
					if !ok || e != nil {
						t.Fatal("begin", ok, e)
					}
					span := bytes.Repeat([]byte{0xa5}, 4096)
					w.quantum()
					w.refuse = true
					retained, r, b := w.retained, w.r, w.b
					n, done, e := d.Step(span)
					w.refuse = false
					if n != 0 || done || e != nil || w.retained != retained || w.r != r || w.b != b || !bytes.Equal(span, bytes.Repeat([]byte{0xa5}, len(span))) || !bytes.Equal(raw, []byte{0xa5, 0xa5, 0xa5, 0xa5}) {
						t.Fatal("cold refusal changed output, custody or work", n, done, e)
					}
					var got []byte
					pos := 0
					for call := 0; call < 10000; call++ {
						w.quantum()
						if _, need := d.InputRequest(); need {
							end := min(pos+3, len(frame))
							n, _, e = d.FeedInput(frame[pos:end], end == len(frame))
							pos += n
						} else {
							n, done, e = d.Step(span)
							if mode == BoundedSpans {
								got = append(got, span[:n]...)
							}
						}
						if w.usedR > 32 || w.usedB > 1<<20 {
							t.Fatal("quantum exceeded")
						}
						if e != nil || done {
							break
						}
						if call == 9999 {
							t.Fatal("resume stalled")
						}
					}
					if rawDict {
						if mode == BoundedRaw && e == nil {
							got, e = d.RawResult()
						}
						if e != nil || !done || !bytes.Equal(got, []byte("test")) {
							t.Fatal("empty raw resume", done, e, got)
						}
					} else if !errors.Is(e, ErrUnknownDictionary) || len(got) != 0 || !bytes.Equal(raw, []byte{0xa5, 0xa5, 0xa5, 0xa5}) {
						t.Fatal("ID-only binary wrote output or installed a definition", e, got)
					}
				})
			}
		}
	}
}
