package zstd

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"
)

// Public wire uses the original two-symbol Huffman table, jump table, and
// four independently terminated streams; only declared output and stream bits
// vary. In particular, length three has a valid empty fourth stream.
func shortFourStreamFrame(n int, streams [4]byte) []byte {
	h := uint32(6 | n<<4 | 12<<14)
	return []byte{0x28, 0xb5, 0x2f, 0xfd, 0x20, byte(n), 0x85, 0, 0,
		byte(h), byte(h >> 8), byte(h >> 16), 128, 0x10, 1, 0, 1, 0, 1, 0,
		streams[0], streams[1], streams[2], streams[3], 0}
}

func TestBoundedFourStreamPartitions(t *testing.T) {
	exact, err := hex.DecodeString("28b52ffd200185000016000380100100010001000201020100")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		frame []byte
		valid bool
	}{
		{"reported-length1", exact, false},
		{"length2", shortFourStreamFrame(2, [4]byte{2, 2, 1, 2}), false},
		{"length5", shortFourStreamFrame(5, [4]byte{4, 4, 2, 2}), false},
		{"length3-empty-fourth", shortFourStreamFrame(3, [4]byte{2, 2, 2, 1}), true},
		{"length4", shortFourStreamFrame(4, [4]byte{2, 2, 2, 2}), true},
		{"length6-empty-fourth", shortFourStreamFrame(6, [4]byte{4, 4, 4, 1}), true},
		{"length7", shortFourStreamFrame(7, [4]byte{4, 4, 4, 2}), true},
		{"length8", shortFourStreamFrame(8, [4]byte{4, 4, 4, 4}), true},
		{"length9-empty-fourth", shortFourStreamFrame(9, [4]byte{8, 8, 8, 1}), true},
		{"length4-first-stream-overrun", shortFourStreamFrame(4, [4]byte{4, 2, 2, 2}), false},
		{"length4-first-stream-underrun", shortFourStreamFrame(4, [4]byte{1, 2, 2, 2}), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ordinary, e := NewReader(nil, WithDecoderConcurrency(1))
			if e != nil {
				t.Fatal(e)
			}
			want, oracleErr := ordinary.DecodeAll(c.frame, nil)
			ordinary.Close()
			if (oracleErr == nil) != c.valid {
				t.Fatalf("ordinary classification: %v", oracleErr)
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
						got, e := multiFrameOutcome(t, d, w, c.frame, BorrowedDictionary{}, mode, fed, false)
						if (e == nil) != c.valid {
							t.Fatalf("bounded classification: %v, oracle %v", e, oracleErr)
						}
						if c.valid && !bytes.Equal(got, want) {
							t.Fatalf("output %x != ordinary %x", got, want)
						}
						// Error cleanup/reset must detach the malformed input and preserve same-owner
						// cold tables without allowing stale partition state to affect the next frame.
						valid := shortFourStreamFrame(3, [4]byte{2, 2, 2, 1})
						got, e = multiFrameOutcome(t, d, w, valid, BorrowedDictionary{}, mode, fed, true)
						if e != nil || !bytes.Equal(got, []byte{0, 0, 0}) {
							t.Fatalf("reset after short stream: %x %v", got, e)
						}
					})
				}
			}
		})
	}
}

type fourStreamPauseWork struct {
	*boundedTestWork
	decoder *boundedDecoder
	pause   bool
}

func (w *fourStreamPauseWork) Reserve(r, b uint64) bool {
	if w.pause && w.decoder != nil && w.decoder.phase == 110 {
		return false
	}
	return w.boundedTestWork.Reserve(r, b)
}

func TestBoundedFourStreamCancelBeforeValidation(t *testing.T) {
	frame := shortFourStreamFrame(1, [4]byte{2, 1, 2, 1})
	for _, mode := range []BoundedOutput{BoundedRaw, BoundedSpans} {
		for _, fed := range []bool{false, true} {
			t.Run(fmt.Sprintf("mode%d/fed%v", mode, fed), func(t *testing.T) {
				w := &boundedTestWork{limit: 32 << 20}
				w.quantum()
				gate := &fourStreamPauseWork{boundedTestWork: w, pause: true}
				d, ok, e := NewBoundedReader(gate)
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				gate.decoder = d.bounded
				raw := make([]byte, 0, 1)
				if fed {
					ok, e = d.BeginStream(&frame, mode, raw, BorrowedDictionary{})
				} else {
					ok, e = d.Begin(frame, &frame, mode, raw, BorrowedDictionary{})
				}
				if !ok || e != nil {
					t.Fatal(ok, e)
				}
				pos := 0
				span := make([]byte, 8)
				found := false
				for calls := 0; calls < 10000; calls++ {
					// Earlier grammar transitions need multi-record reservations. The
					// callback gate refuses the first real LiteralState reservation,
					// before partition validation, without modifying parser state.
					w.quantum()
					if _, need := d.InputRequest(); need {
						end := min(pos+3, len(frame))
						n, _, e := d.FeedInput(frame[pos:end], end == len(frame))
						pos += n
						if e != nil {
							t.Fatal(e)
						}
					} else {
						n, done, e := d.Step(span)
						if n != 0 || done || e != nil {
							t.Fatal(n, done, e)
						}
					}
					if d.bounded.phase == 110 {
						found = true
						break
					}
				}
				if !found {
					t.Fatal("did not pause before validation")
				}
				gate.pause = false
				retained := w.retained
				phase := d.bounded.phase
				literal := d.bounded.literal
				buffer := append([]byte(nil), d.bounded.block.literalBuf...)
				input := append([]byte(nil), frame...)
				w.quantum()
				beforeR, beforeB := w.r, w.b
				w.refuse = true
				n, done, e := d.Step(span)
				if n != 0 || done || e != nil || d.bounded.phase != phase || w.retained != retained || w.usedR != 0 || w.usedB != 0 || w.r != beforeR || w.b != beforeB ||
					!reflect.DeepEqual(literal, d.bounded.literal) || !bytes.Equal(buffer, d.bounded.block.literalBuf) || !bytes.Equal(input, frame) {
					t.Fatal("refused validation changed effects/custody")
				}
				closed, e := d.CloseStep()
				if closed || e != nil || w.retained != retained {
					t.Fatal("refused close changed custody")
				}
				w.refuse = false
				closeBoundedForTest(t, &d, w)
			})
		}
	}
}
