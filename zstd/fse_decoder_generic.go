//go:build !amd64 || appengine || !gc || noasm
// +build !amd64 appengine !gc noasm

package zstd

import (
	"github.com/snissn/compress/internal/decoderwork"
)

// buildDtable will build the decoding table.
func (s *fseDecoder) buildDtable() error {
	var p decoderwork.Table
	p.Begin(s.norm[:], s.stateTable[:], int(s.symbolLen), s.actualTableLog, 8, 16, 16)
	_, err := p.Step(nil,
		func(i int, v uint8) { s.dt[i].setAddBits(v) },
		func(i int) uint8 { return s.dt[i].addBits() },
		func(i int, n uint8, v uint16) { x := s.dt[i]; x.setNBits(n); x.setNewState(v); s.dt[i] = x })
	return err
}
