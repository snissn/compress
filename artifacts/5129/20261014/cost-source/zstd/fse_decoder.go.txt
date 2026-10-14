// Copyright 2019+ Klaus Post. All rights reserved.
// License information can be found in the LICENSE file.
// Based on work by Yann Collet, released under BSD License.

package zstd

import (
	"encoding/binary"
	"fmt"
	"github.com/snissn/compress/internal/decoderwork"
	"io"
)

const (
	tablelogAbsoluteMax = 9
)

const (
	/*!MEMORY_USAGE :
	 *  Memory usage formula : N->2^N Bytes (examples : 10 -> 1KB; 12 -> 4KB ; 16 -> 64KB; 20 -> 1MB; etc.)
	 *  Increasing memory usage improves compression ratio
	 *  Reduced memory usage can improve speed, due to cache effect
	 *  Recommended max value is 14, for 16KB, which nicely fits into Intel x86 L1 cache */
	maxMemoryUsage = tablelogAbsoluteMax + 2

	maxTableLog    = maxMemoryUsage - 2
	maxTablesize   = 1 << maxTableLog
	maxTableMask   = (1 << maxTableLog) - 1
	minTablelog    = 5
	maxSymbolValue = 255
)

// fseDecoder provides temporary storage for compression and decompression.
type fseDecoder struct {
	dt             [maxTablesize]decSymbol // Decompression table.
	symbolLen      uint16                  // Length of active part of the symbol table.
	actualTableLog uint8                   // Selected tablelog.
	maxBits        uint8                   // Maximum number of additional bits

	// used for table creation to avoid allocations.
	stateTable [256]uint16
	norm       [maxSymbolValue + 1]int16
	preDefined bool
}

// tableStep returns the next table index.
func tableStep(tableSize uint32) uint32 {
	return (tableSize >> 1) + (tableSize >> 3) + 3
}

// readNCount will read the symbol distribution so decoding tables can be constructed.
func (s *fseDecoder) readNCount(b *byteReader, maxSymbol uint16) error {
	var p decoderwork.NCount
	p.Begin(b.unread(), s.norm[:], maxSymbol)
	_, used, err := p.Step(nil, tablelogAbsoluteMax)
	if err != nil {
		return err
	}
	s.actualTableLog, s.symbolLen = p.TableLog, p.Symbols
	b.advance(uint(used))
	return s.buildDtable()
}

func (s *fseDecoder) mustReadFrom(r io.Reader) {
	fatalErr := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	// 	dt             [maxTablesize]decSymbol // Decompression table.
	//	symbolLen      uint16                  // Length of active part of the symbol table.
	//	actualTableLog uint8                   // Selected tablelog.
	//	maxBits        uint8                   // Maximum number of additional bits
	//	// used for table creation to avoid allocations.
	//	stateTable [256]uint16
	//	norm       [maxSymbolValue + 1]int16
	//	preDefined bool
	fatalErr(binary.Read(r, binary.LittleEndian, &s.dt))
	fatalErr(binary.Read(r, binary.LittleEndian, &s.symbolLen))
	fatalErr(binary.Read(r, binary.LittleEndian, &s.actualTableLog))
	fatalErr(binary.Read(r, binary.LittleEndian, &s.maxBits))
	fatalErr(binary.Read(r, binary.LittleEndian, &s.stateTable))
	fatalErr(binary.Read(r, binary.LittleEndian, &s.norm))
	fatalErr(binary.Read(r, binary.LittleEndian, &s.preDefined))
}

// decSymbol contains information about a state entry,
// Including the state offset base, the output symbol and
// the number of bits to read for the low part of the destination state.
// Using a composite uint64 is faster than a struct with separate members.
type decSymbol uint64

func newDecSymbol(nbits, addBits uint8, newState uint16, baseline uint32) decSymbol {
	return decSymbol(nbits) | (decSymbol(addBits) << 8) | (decSymbol(newState) << 16) | (decSymbol(baseline) << 32)
}

func (d decSymbol) nbBits() uint8 {
	return uint8(d)
}

func (d decSymbol) addBits() uint8 {
	return uint8(d >> 8)
}

func (d decSymbol) newState() uint16 {
	return uint16(d >> 16)
}

func (d decSymbol) baselineInt() int {
	return int(d >> 32)
}

func (d *decSymbol) setNBits(nBits uint8) {
	const mask = 0xffffffffffffff00
	*d = (*d & mask) | decSymbol(nBits)
}

func (d *decSymbol) setAddBits(addBits uint8) {
	const mask = 0xffffffffffff00ff
	*d = (*d & mask) | (decSymbol(addBits) << 8)
}

func (d *decSymbol) setNewState(state uint16) {
	const mask = 0xffffffff0000ffff
	*d = (*d & mask) | decSymbol(state)<<16
}

func (d *decSymbol) setExt(addBits uint8, baseline uint32) {
	const mask = 0xffff00ff
	*d = (*d & mask) | (decSymbol(addBits) << 8) | (decSymbol(baseline) << 32)
}

// decSymbolValue returns the transformed decSymbol for the given symbol.
func decSymbolValue(symb uint8, t []baseOffset) (decSymbol, error) {
	if int(symb) >= len(t) {
		return 0, fmt.Errorf("rle symbol %d >= max %d", symb, len(t))
	}
	lu := t[symb]
	return newDecSymbol(0, lu.addBits, 0, lu.baseLine), nil
}

// setRLE will set the decoder til RLE mode.
func (s *fseDecoder) setRLE(symbol decSymbol) {
	s.actualTableLog = 0
	s.maxBits = symbol.addBits()
	s.dt[0] = symbol
}

// transform will transform the decoder table into a table usable for
// decoding without having to apply the transformation while decoding.
// The state will contain the base value and the number of bits to read.
func (s *fseDecoder) transform(t []baseOffset) error {
	tableSize := uint16(1 << s.actualTableLog)
	s.maxBits = 0
	for i, v := range s.dt[:tableSize] {
		add := v.addBits()
		if int(add) >= len(t) {
			return fmt.Errorf("invalid decoding table entry %d, symbol %d >= max (%d)", i, v.addBits(), len(t))
		}
		lu := t[add]
		if lu.addBits > s.maxBits {
			s.maxBits = lu.addBits
		}
		v.setExt(lu.addBits, lu.baseLine)
		s.dt[i] = v
	}
	return nil
}

type fseState struct {
	dt    []decSymbol
	state decSymbol
}

// Initialize and decodeAsync first state and symbol.
func (s *fseState) init(br *bitReader, tableLog uint8, dt []decSymbol) {
	s.dt = dt
	br.fill()
	s.state = dt[br.getBits(tableLog)]
}

// final returns the current state symbol without decoding the next.
func (s decSymbol) final() (int, uint8) {
	return s.baselineInt(), s.addBits()
}
