package zstd

import (
	"bytes"
	"testing"
)

// The original encoder generates offset38 most often;105 and68 tie. Prefer
// the longer offset for equal frequencies, independent of map iteration order.
func TestBuildDictEqualFrequencyOffsets(t *testing.T) {
	samples := [][]byte{[]byte("workspace build dict alpha alpha alpha"), []byte("workspace build dict beta beta beta"), []byte("workspace build dict gamma gamma gamma")}
	b, e := BuildDict(BuildDictOptions{ID: 201, Contents: samples, History: bytes.Join(samples, nil), Offsets: [3]int{1, 4, 8}, Level: SpeedFastest})
	if e != nil {
		t.Fatal(e)
	}
	d, e := InspectDictionary(b)
	if e != nil {
		t.Fatal(e)
	}
	if got := d.Offsets(); got != [3]int{38, 105, 68} {
		t.Fatalf("equal-frequency offsets: %v", got)
	}
}
