package zstd

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// The closed exported control remains reachable after its continuation retires.
// Its ordinary methods must obey the existing ordinary Decoder.Close contract.
func TestBoundedClosedControlOrdinaryInterface(t *testing.T) {
	cases := []struct {
		name string
		call func(*Decoder) error
	}{
		{"ReadEmpty", func(d *Decoder) error { _, e := d.Read(nil); return e }},
		{"Read", func(d *Decoder) error { _, e := d.Read(make([]byte, 1)); return e }},
		{"WriteTo", func(d *Decoder) error { _, e := d.WriteTo(io.Discard); return e }},
		{"ResetNil", func(d *Decoder) error { return d.Reset(nil) }},
		{"ResetReader", func(d *Decoder) error { return d.Reset(bytes.NewReader(nil)) }},
		{"DecodeAll", func(d *Decoder) error { _, e := d.DecodeAll(nil, nil); return e }},
		{"WrapperRead", func(d *Decoder) error { _, e := d.IOReadCloser().Read(make([]byte, 1)); return e }},
		{"WrapperWriteTo", func(d *Decoder) error { _, e := d.IOReadCloser().(io.WriterTo).WriteTo(io.Discard); return e }},
	}
	for _, state := range []string{"unbegun", "whole_raw", "whole_spans", "fed_raw", "fed_spans", "failed"} {
		for _, tc := range cases {
			t.Run(state+"/"+tc.name, func(t *testing.T) {
				ordinary, err := NewReader(nil, WithDecoderConcurrency(1))
				if err != nil {
					t.Fatal(err)
				}
				ordinary.Close()
				want := tc.call(ordinary)
				if !errors.Is(want, ErrDecoderClosed) {
					t.Fatalf("ordinary closed contract: %v", want)
				}
				d, w, wrapper := closedBoundedControl(t, state)
				beforeR, beforeB, retained := w.totalR, w.totalB, w.retained
				if got := tc.call(d); !errors.Is(got, want) {
					t.Fatalf("closed bounded contract: got %v want %v", got, want)
				}
				if _, err := wrapper.Read(nil); !errors.Is(err, ErrDecoderClosed) {
					t.Fatal("preexisting wrapper read", err)
				}
				if _, err := wrapper.(io.WriterTo).WriteTo(io.Discard); !errors.Is(err, ErrDecoderClosed) {
					t.Fatal("preexisting wrapper write", err)
				}
				// All original public wrappers and both close entrypoints are terminal.
				d.Close()
				if err := d.IOReadCloser().Close(); err != nil {
					t.Fatal(err)
				}
				if ok, err := d.CloseStep(); err != nil || !ok {
					t.Fatal(ok, err)
				}
				if w.totalR != beforeR || w.totalB != beforeB || w.retained != retained {
					t.Fatal("closed control performed work or changed custody")
				}
				wrapper = nil
				d = nil
				w.Release(BoundedControlSize())
				if w.retained != 0 {
					t.Fatal("control retirement", w.retained)
				}
			})
		}
	}
}

func TestBoundedClosedControlBoundedInterface(t *testing.T) {
	d, w, wrapper := closedBoundedControl(t, "fed_spans")
	beforeR, beforeB, retained := w.totalR, w.totalB, w.retained
	owner := new(byte)
	if ok, e := d.Begin(nil, owner, BoundedRaw, nil, BorrowedDictionary{}); ok || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(ok, e)
	}
	if ok, e := d.BeginStream(owner, BoundedSpans, nil, BorrowedDictionary{}); ok || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(ok, e)
	}
	if ok, e := d.ResetStep(nil, owner, BoundedRaw, nil, BorrowedDictionary{}); ok || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(ok, e)
	}
	if ok, e := d.ResetStreamStep(owner, BoundedSpans, nil, BorrowedDictionary{}); ok || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(ok, e)
	}
	if n, needed := d.InputRequest(); n != 0 || needed {
		t.Fatal(n, needed)
	}
	if n, ok, e := d.FeedInput(nil, true); n != 0 || ok || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(n, ok, e)
	}
	if n, done, e := d.Step(nil); n != 0 || done || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(n, done, e)
	}
	if raw, e := d.RawResult(); raw != nil || !errors.Is(e, ErrBoundedMode) {
		t.Fatal(raw, e)
	}
	if ok, e := d.CloseStep(); !ok || e != nil {
		t.Fatal(ok, e)
	}
	if err := wrapper.Close(); err != nil {
		t.Fatal(err)
	}
	if w.totalR != beforeR || w.totalB != beforeB || w.retained != retained {
		t.Fatal("closed binding performed work or changed custody")
	}
	wrapper = nil
	d = nil
	w.Release(BoundedControlSize())
	if w.retained != 0 {
		t.Fatal("control retirement", w.retained)
	}
}

func closedBoundedControl(t *testing.T, state string) (*Decoder, *boundedTestWork, io.ReadCloser) {
	t.Helper()
	w := &boundedTestWork{limit: 32 << 20}
	w.quantum()
	d, ok, err := NewBoundedReader(w)
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	wrapper := d.IOReadCloser()
	if state != "unbegun" {
		frame := boundedFixture(t, []byte("closed original decoder control"), true)
		owner := new(byte)
		mode := BoundedRaw
		if state == "whole_spans" || state == "fed_spans" {
			mode = BoundedSpans
		}
		raw := make([]byte, 0, 64)
		if state == "fed_raw" || state == "fed_spans" {
			ok, err = d.BeginStream(owner, mode, raw, BorrowedDictionary{})
		} else {
			if state == "failed" {
				frame = []byte{1, 2, 3}
			}
			ok, err = d.Begin(frame, owner, mode, raw, BorrowedDictionary{})
		}
		if !ok || err != nil {
			t.Fatal(ok, err)
		}
		// Keep actual live input/history and failure cases, then cancel via Close.
		for i := 0; i < 500; i++ {
			w.quantum()
			_, done, err := d.Step(make([]byte, 64))
			if state == "failed" && err != nil {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if done {
				break
			}
			if n, needed := d.InputRequest(); needed {
				w.quantum()
				if n > len(frame) {
					n = len(frame)
				}
				used, _, e := d.FeedInput(frame[:n], n == len(frame))
				if e != nil {
					t.Fatal(e)
				}
				frame = frame[used:]
			}
			if i == 499 {
				t.Fatal("decode did not terminate")
			}
		}
	}
	// Refused terminal intent and repeated finite cleanup exercise the real API.
	w.quantum()
	w.refuse = true
	if done, err := d.CloseStep(); done || err != nil {
		t.Fatal(done, err)
	}
	w.refuse = false
	for i := 0; i < 100; i++ {
		w.quantum()
		w.r = 1
		done, err := d.CloseStep()
		if err != nil {
			t.Fatal(err)
		}
		if done {
			if w.retained != BoundedControlSize() {
				t.Fatal("reachable control admission", w.retained)
			}
			return d, w, wrapper
		}
	}
	t.Fatal("terminal cleanup did not complete")
	return nil, nil, nil
}

func TestBoundedPublicModeCrossingClose(t *testing.T) {
	t.Run("ordinary_active", func(t *testing.T) {
		d, err := NewReader(nil, WithDecoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		if ok, err := d.CloseStep(); ok || !errors.Is(err, ErrBoundedMode) {
			t.Fatal("ordinary false completion", ok, err)
		}
		raw := []byte("ordinary decoder remains usable after rejected bounded close")
		frame := boundedFixture(t, raw, true)
		got, err := d.DecodeAll(frame, nil)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatal("rejection changed ordinary decoder", err)
		}
		if err := d.IOReadCloser().Close(); err != nil {
			t.Fatal(err)
		}
		if ok, err := d.CloseStep(); !ok || err != nil {
			t.Fatal("already closed control", ok, err)
		}
	})
	for _, state := range []string{"unbegun", "begun_raw", "begun_spans", "reset_paused", "close_refused", "close_paused"} {
		t.Run(state, func(t *testing.T) {
			w := &boundedTestWork{limit: 32 << 20}
			w.quantum()
			d, ok, err := NewBoundedReader(w)
			if !ok || err != nil {
				t.Fatal(ok, err)
			}
			wrapper := d.IOReadCloser()
			owner := new(byte)
			if state != "unbegun" {
				mode := BoundedRaw
				if state == "begun_spans" {
					mode = BoundedSpans
				}
				if ok, err := d.Begin(nil, owner, mode, nil, BorrowedDictionary{}); !ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			if state == "reset_paused" {
				w.refuse = true
				if ok, err := d.ResetStep(nil, owner, BoundedRaw, nil, BorrowedDictionary{}); ok || err != nil {
					t.Fatal(ok, err)
				}
			} else if state == "close_refused" {
				w.refuse = true
				if ok, err := d.CloseStep(); ok || err != nil {
					t.Fatal(ok, err)
				}
			} else if state == "close_paused" {
				w.quantum()
				w.r = 1
				if ok, err := d.CloseStep(); ok || err != nil {
					t.Fatal(ok, err)
				}
			}
			beforeR, beforeB, retained := w.totalR, w.totalB, w.retained
			if err := wrapper.Close(); !errors.Is(err, ErrBoundedMode) {
				t.Fatal("bounded wrapper false completion", err)
			}
			if _, err := wrapper.Read(nil); !errors.Is(err, ErrBoundedMode) {
				t.Fatal(err)
			}
			if _, err := wrapper.(io.WriterTo).WriteTo(io.Discard); !errors.Is(err, ErrBoundedMode) {
				t.Fatal(err)
			}
			d.Close() // Existing void interface deliberately delegates bounded cleanup.
			if w.totalR != beforeR || w.totalB != beforeB || w.retained != retained {
				t.Fatal("rejected mode crossing performed work or changed custody")
			}
			w.refuse = false
			for i := 0; i < 100; i++ {
				w.quantum()
				w.r = 1
				done, err := d.CloseStep()
				if err != nil {
					t.Fatal(err)
				}
				if done {
					if err := wrapper.Close(); err != nil {
						t.Fatal(err)
					}
					wrapper = nil
					d = nil
					w.Release(BoundedControlSize())
					if w.retained != 0 {
						t.Fatal("last control edge", w.retained)
					}
					return
				}
			}
			t.Fatal("bounded physical cleanup did not terminate")
		})
	}
}
