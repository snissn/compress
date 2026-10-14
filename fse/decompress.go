package fse

import (
	"fmt"
	"github.com/snissn/compress/internal/decoderwork"
)

const (
	tablelogAbsoluteMax = 15
)

// Decompress a block of data.
// You can provide a scratch buffer to avoid allocations.
// If nil is provided a temporary one will be allocated.
// It is possible, but by no way guaranteed that corrupt data will
// return an error.
// It is up to the caller to verify integrity of the returned data.
// Use a predefined Scratch to set maximum acceptable output size.
func Decompress(b []byte, s *Scratch) ([]byte, error) {
	s, err := s.prepare(b)
	if err != nil {
		return nil, err
	}
	s.Out = s.Out[:0]
	err = s.readNCount()
	if err != nil {
		return nil, err
	}
	err = s.buildDtable()
	if err != nil {
		return nil, err
	}
	err = s.decompress()
	if err != nil {
		return nil, err
	}

	return s.Out, nil
}

// readNCount will read the symbol distribution so decoding tables can be constructed.
func (s *Scratch) readNCount() error {
	var p decoderwork.NCount
	p.Begin(s.br.unread(), s.norm[:], maxSymbolValue+1)
	_, used, err := p.Step(nil, tablelogAbsoluteMax)
	if err != nil {
		return err
	}
	s.actualTableLog, s.symbolLen = p.TableLog, p.Symbols
	s.br.advance(uint(used))
	return nil
}

// decSymbol contains information about a state entry,
// Including the state offset base, the output symbol and
// the number of bits to read for the low part of the destination state.
type decSymbol struct {
	newState uint16
	symbol   uint8
	nbBits   uint8
}

// allocDtable will allocate decoding tables if they are not big enough.
func (s *Scratch) allocDtable() {
	tableSize := 1 << s.actualTableLog
	if cap(s.decTable) < tableSize {
		s.decTable = make([]decSymbol, tableSize)
	}
	s.decTable = s.decTable[:tableSize]

	if cap(s.ct.tableSymbol) < 256 {
		s.ct.tableSymbol = make([]byte, 256)
	}
	s.ct.tableSymbol = s.ct.tableSymbol[:256]

	if cap(s.ct.stateTable) < 256 {
		s.ct.stateTable = make([]uint16, 256)
	}
	s.ct.stateTable = s.ct.stateTable[:256]
}

// buildDtable will build the decoding table.
func (s *Scratch) buildDtable() error {
	s.allocDtable()
	var p decoderwork.Table
	p.Begin(s.norm[:], s.ct.stateTable, int(s.symbolLen), s.actualTableLog, 1, 1, 3)
	_, err := p.Step(nil,
		func(i int, v uint8) { s.decTable[i].symbol = v },
		func(i int) uint8 { return s.decTable[i].symbol },
		func(i int, n uint8, v uint16) { s.decTable[i].nbBits = n; s.decTable[i].newState = v })
	s.zeroBits = p.ZeroBits
	return err
}

// decompress will decompress the bitstream.
// If the buffer is over-read an error is returned.
func (s *Scratch) decompress() error {
	br := &s.bits
	if err := br.init(s.br.unread()); err != nil {
		return err
	}

	var s1, s2 decoder
	// Initialize and decode first state and symbol.
	s1.init(br, s.decTable, s.actualTableLog)
	s2.init(br, s.decTable, s.actualTableLog)

	// Use temp table to avoid bound checks/append penalty.
	var tmp = s.ct.tableSymbol[:256]
	var off uint8

	// Main part
	if !s.zeroBits {
		for br.off >= 8 {
			br.fillFast()
			tmp[off+0] = s1.nextFast()
			tmp[off+1] = s2.nextFast()
			br.fillFast()
			tmp[off+2] = s1.nextFast()
			tmp[off+3] = s2.nextFast()
			off += 4
			// When off is 0, we have overflowed and should write.
			if off == 0 {
				s.Out = append(s.Out, tmp...)
				if len(s.Out) >= s.DecompressLimit {
					return fmt.Errorf("output size (%d) > DecompressLimit (%d)", len(s.Out), s.DecompressLimit)
				}
			}
		}
	} else {
		for br.off >= 8 {
			br.fillFast()
			tmp[off+0] = s1.next()
			tmp[off+1] = s2.next()
			br.fillFast()
			tmp[off+2] = s1.next()
			tmp[off+3] = s2.next()
			off += 4
			if off == 0 {
				s.Out = append(s.Out, tmp...)
				// When off is 0, we have overflowed and should write.
				if len(s.Out) >= s.DecompressLimit {
					return fmt.Errorf("output size (%d) > DecompressLimit (%d)", len(s.Out), s.DecompressLimit)
				}
			}
		}
	}
	s.Out = append(s.Out, tmp[:off]...)

	// Final bits, a bit more expensive check
	for {
		if s1.finished() {
			s.Out = append(s.Out, s1.final(), s2.final())
			break
		}
		br.fill()
		s.Out = append(s.Out, s1.next())
		if s2.finished() {
			s.Out = append(s.Out, s2.final(), s1.final())
			break
		}
		s.Out = append(s.Out, s2.next())
		if len(s.Out) >= s.DecompressLimit {
			return fmt.Errorf("output size (%d) > DecompressLimit (%d)", len(s.Out), s.DecompressLimit)
		}
	}
	return br.close()
}

// decoder keeps track of the current state and updates it from the bitstream.
type decoder struct {
	state uint16
	br    *bitReader
	dt    []decSymbol
}

// init will initialize the decoder and read the first state from the stream.
func (d *decoder) init(in *bitReader, dt []decSymbol, tableLog uint8) {
	d.dt = dt
	d.br = in
	d.state = in.getBits(tableLog)
}

// next returns the next symbol and sets the next state.
// At least tablelog bits must be available in the bit reader.
func (d *decoder) next() uint8 {
	n := &d.dt[d.state]
	lowBits := d.br.getBits(n.nbBits)
	d.state = n.newState + lowBits
	return n.symbol
}

// finished returns true if all bits have been read from the bitstream
// and the next state would require reading bits from the input.
func (d *decoder) finished() bool {
	return d.br.finished() && d.dt[d.state].nbBits > 0
}

// final returns the current state symbol without decoding the next.
func (d *decoder) final() uint8 {
	return d.dt[d.state].symbol
}

// nextFast returns the next symbol and sets the next state.
// This can only be used if no symbols are 0 bits.
// At least tablelog bits must be available in the bit reader.
func (d *decoder) nextFast() uint8 {
	n := d.dt[d.state]
	lowBits := d.br.getBitsFast(n.nbBits)
	d.state = n.newState + lowBits
	return n.symbol
}
