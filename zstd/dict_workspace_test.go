package zstd

import (
	"bytes"
	"testing"
)

func testWorkspaceDict(t *testing.T, id uint32, suffix string) []byte {
	t.Helper()
	samples := [][]byte{
		[]byte("alpha alpha alpha city=honolulu email=a@example.com " + suffix),
		[]byte("alpha alpha alpha city=seattle email=b@example.com " + suffix),
		[]byte("alpha alpha alpha city=london email=c@example.com " + suffix),
		[]byte("alpha alpha alpha city=honolulu email=d@example.com " + suffix),
	}
	history := bytes.Join(samples, nil)
	dict, err := BuildDict(BuildDictOptions{
		ID:       id,
		Contents: samples,
		History:  history,
		Offsets:  [3]int{1, 4, 8},
		Level:    SpeedFastest,
	})
	if err != nil {
		t.Fatalf("BuildDict failed: %v", err)
	}
	return dict
}

func encodeAllWithFreshDict(t *testing.T, dict, src []byte, level EncoderLevel) []byte {
	t.Helper()
	enc, err := NewWriter(nil,
		WithEncoderLevel(level),
		WithEncoderCRC(false),
		WithEncoderConcurrency(1),
		WithEncoderDict(dict),
	)
	if err != nil {
		t.Fatalf("NewWriter failed: %v", err)
	}
	defer enc.Close()
	return enc.EncodeAll(src, nil)
}

func decodeAllWithDict(t *testing.T, dict, encoded []byte) []byte {
	t.Helper()
	dec, err := NewReader(nil, WithDecoderDicts(dict))
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	defer dec.Close()
	decoded, err := dec.DecodeAll(encoded, nil)
	if err != nil {
		t.Fatalf("DecodeAll failed: %v", err)
	}
	return decoded
}

func TestDictEncodeWorkspaceMatchesFreshEncoder(t *testing.T) {
	dict := testWorkspaceDict(t, 99, "one")
	src := []byte("alpha alpha alpha city=honolulu email=workspace@example.com one")
	var ws DictEncodeWorkspace
	for level := SpeedFastest; level < speedLast; level++ {
		level := level
		t.Run(level.String(), func(t *testing.T) {
			want := encodeAllWithFreshDict(t, dict, src, level)
			got, err := ws.EncodeAllWithDict(src, nil, dict, level)
			if err != nil {
				t.Fatalf("EncodeAllWithDict failed: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("workspace output differs from fresh encoder: got %d bytes, want %d bytes", len(got), len(want))
			}
			if decoded := decodeAllWithDict(t, dict, got); !bytes.Equal(decoded, src) {
				t.Fatalf("decoded output mismatch: got %q want %q", decoded, src)
			}
		})
	}
}

func TestDictEncodeWorkspaceSwitchesDictsWithSameID(t *testing.T) {
	dictA := testWorkspaceDict(t, 101, "one")
	dictB := testWorkspaceDict(t, 101, "two")
	srcA := []byte("alpha alpha alpha city=seattle email=a@example.com one")
	srcB := []byte("alpha alpha alpha city=seattle email=b@example.com two")
	var ws DictEncodeWorkspace

	gotA, err := ws.EncodeAllWithDict(srcA, nil, dictA, SpeedFastest)
	if err != nil {
		t.Fatalf("EncodeAllWithDict A failed: %v", err)
	}
	gotB, err := ws.EncodeAllWithDict(srcB, nil, dictB, SpeedFastest)
	if err != nil {
		t.Fatalf("EncodeAllWithDict B failed: %v", err)
	}
	wantB := encodeAllWithFreshDict(t, dictB, srcB, SpeedFastest)
	if !bytes.Equal(gotB, wantB) {
		t.Fatalf("workspace did not refresh same-ID dictionary state: got %d bytes, want %d bytes", len(gotB), len(wantB))
	}
	if decoded := decodeAllWithDict(t, dictA, gotA); !bytes.Equal(decoded, srcA) {
		t.Fatalf("decoded A mismatch: got %q want %q", decoded, srcA)
	}
	if decoded := decodeAllWithDict(t, dictB, gotB); !bytes.Equal(decoded, srcB) {
		t.Fatalf("decoded B mismatch: got %q want %q", decoded, srcB)
	}
}

func TestNilDictEncodeWorkspaceMatchesFreshEncoder(t *testing.T) {
	dict := testWorkspaceDict(t, 103, "nil")
	src := []byte("alpha alpha alpha city=london email=nil@example.com nil")
	var ws *DictEncodeWorkspace
	got, err := ws.EncodeAllWithDict(src, nil, dict, SpeedFastest)
	if err != nil {
		t.Fatalf("EncodeAllWithDict failed: %v", err)
	}
	want := encodeAllWithFreshDict(t, dict, src, SpeedFastest)
	if !bytes.Equal(got, want) {
		t.Fatalf("nil workspace output differs from fresh encoder: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestBuildDictWorkspaceMatchesBuildDict(t *testing.T) {
	samples := [][]byte{
		[]byte("workspace build dict alpha alpha alpha"),
		[]byte("workspace build dict beta beta beta"),
		[]byte("workspace build dict gamma gamma gamma"),
	}
	history := bytes.Join(samples, nil)
	var ws BuildDictWorkspace
	withWorkspace, err := BuildDict(BuildDictOptions{
		ID:        201,
		Contents:  samples,
		History:   history,
		Offsets:   [3]int{1, 4, 8},
		Level:     SpeedFastest,
		Workspace: &ws,
	})
	if err != nil {
		t.Fatalf("BuildDict with workspace failed: %v", err)
	}
	withoutWorkspace, err := BuildDict(BuildDictOptions{
		ID:       201,
		Contents: samples,
		History:  history,
		Offsets:  [3]int{1, 4, 8},
		Level:    SpeedFastest,
	})
	if err != nil {
		t.Fatalf("BuildDict without workspace failed: %v", err)
	}
	if !bytes.Equal(withWorkspace, withoutWorkspace) {
		t.Fatalf("BuildDict workspace output differs: got %d bytes, want %d bytes", len(withWorkspace), len(withoutWorkspace))
	}
}
