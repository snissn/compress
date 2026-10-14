// Copyright 2018-2026 Klaus Post and contributors. All rights reserved.
// License information can be found in the LICENSE file.
package fse

import (
	"errors"
	"fmt"
	"github.com/snissn/compress/internal/decoderwork"
	"unsafe"
)

// DecompressState suspends the original FSE decoder. The enclosing decoder
// admits this containing object before construction. Input and destination are
// borrowed immutable/admitted storage; no output growth is permitted.
type DecompressState struct {
	scratch               *Scratch
	count                 decoderwork.NCount
	table                 decoderwork.Table
	a, b                  decoder
	dst                   []byte
	phase, closePhase     int
	used                  int
	ownedTable, ownedNext uint64
}

func (p *DecompressState) Begin(s *Scratch, in, dst []byte) {
	p.scratch = s
	p.a = decoder{}
	p.b = decoder{}
	p.scratch.bits = bitReader{}
	p.closePhase = 0
	p.scratch.br.init(in)
	p.dst = dst[:0]
	p.phase = 0
	p.used = 0
	p.count.Begin(in, p.scratch.norm[:], maxSymbolValue+1)
}

// Step debits the caller's work before every interpreted entry or byte effect.
// done is true only after the original bitstream close succeeds.
func (p *DecompressState) Step(w decoderwork.Work) (done bool, out []byte, err error) {
	for {
		s := p.scratch
		switch p.phase {
		case 0:
			ok, n, e := p.count.Step(w, tablelogAbsoluteMax)
			if e != nil {
				return false, nil, e
			}
			if !ok {
				return false, nil, nil
			}
			s.actualTableLog, s.symbolLen = p.count.TableLog, p.count.Symbols
			s.br.advance(uint(n))
			p.used = n
			p.phase = 1
		case 1:
			size := 1 << s.actualTableLog
			if cap(s.decTable) < size {
				bytes := uint64(size) * 4
				if w != nil {
					bytes = decoderwork.AllocationCharge(bytes)
				}
				if !decoderwork.Charge(w, 1, bytes) {
					return false, nil, nil
				}
				if w != nil && !w.Retain(bytes) {
					return false, nil, errStepTableRetention
				}
				old := s.decTable
				s.decTable = make([]decSymbol, size)
				old = nil
				_ = old
				if w != nil && p.ownedTable != 0 {
					w.Release(p.ownedTable)
				}
				p.ownedTable = bytes
			}
			s.decTable = s.decTable[:size]
			p.phase = 2
		case 2:
			if cap(s.ct.stateTable) < 256 {
				bytes := uint64(512)
				if w != nil {
					bytes = decoderwork.AllocationCharge(bytes)
				}
				if !decoderwork.Charge(w, 1, bytes) {
					return false, nil, nil
				}
				if w != nil && !w.Retain(bytes) {
					return false, nil, errStepStateRetention
				}
				s.ct.stateTable = make([]uint16, 256)
				p.ownedNext = bytes
			}
			if !decoderwork.Charge(w, 1, uint64(unsafe.Sizeof(decoderwork.Table{}))) {
				return false, nil, nil
			}
			s.ct.stateTable = s.ct.stateTable[:256]
			p.table.Begin(s.norm[:], s.ct.stateTable, int(s.symbolLen), s.actualTableLog, 1, 1, 3)
			p.phase = 3
		case 3:
			ok, e := p.table.Step(w,
				func(i int, v uint8) { s.decTable[i].symbol = v },
				func(i int) uint8 { return s.decTable[i].symbol },
				func(i int, n uint8, v uint16) { s.decTable[i].nbBits = n; s.decTable[i].newState = v })
			if e != nil {
				return false, nil, e
			}
			if !ok {
				return false, nil, nil
			}
			p.phase = 4
		case 4:
			if !decoderwork.Charge(w, 2, 64) {
				return false, nil, nil
			}
			if e := s.bits.init(s.br.unread()); e != nil {
				return false, nil, e
			}
			p.a.init(&s.bits, s.decTable, s.actualTableLog)
			p.phase = 5
		case 5:
			if !decoderwork.Charge(w, 1, 64) {
				return false, nil, nil
			}
			p.b.init(&s.bits, s.decTable, s.actualTableLog)
			p.phase = 6
		case 6, 7:
			if !decoderwork.Charge(w, 3, 32) {
				return false, nil, nil
			}
			first, second := &p.a, &p.b
			if p.phase == 7 {
				first, second = second, first
			}
			if first.finished() {
				if len(p.dst)+2 > cap(p.dst) {
					if w == nil {
						return false, nil, fmt.Errorf("output size > destination capacity")
					}
					return false, nil, errStepOutputCapacity
				}
				p.dst = append(p.dst, first.final(), second.final())
				p.phase = 8
				continue
			}
			if len(p.dst) == cap(p.dst) {
				if w == nil {
					return false, nil, fmt.Errorf("output size > destination capacity")
				}
				return false, nil, errStepOutputCapacity
			}
			if p.phase == 6 {
				s.bits.fill()
			}
			p.dst = append(p.dst, first.next())
			if p.phase == 6 {
				p.phase = 7
			} else {
				p.phase = 6
			}
		case 8:
			if !decoderwork.Charge(w, 1, 64) {
				return false, nil, nil
			}
			if e := s.bits.close(); e != nil {
				return false, nil, e
			}
			p.phase = 9
		case 9:
			return true, p.dst, nil
		}
	}
}

// DetachInput clears completed wire/output aliases while keeping the SAME
// admitted tables for subsequent blocks. The enclosing phase prices this fixed
// bookkeeping before calling it; Clear remains the capacity retirement seam.
func (p *DecompressState) DetachInput() {
	p.count.Clear()
	p.table.Clear()
	p.a = decoder{}
	p.b = decoder{}
	p.dst = nil
	if p.scratch != nil {
		p.scratch.br = byteReader{}
		p.scratch.bits = bitReader{}
	}
}

// Clear removes borrowed aliases before releasing admitted table capacities.
// It is resumable and safe after any partial parse or error.
func (p *DecompressState) Clear(w decoderwork.Work) (bool, error) {
	for {
		switch p.closePhase {
		case 0:
			clearBytes := uint64(unsafe.Sizeof(decoderwork.NCount{})) + uint64(unsafe.Sizeof(decoderwork.Table{})) + 2*uint64(unsafe.Sizeof(decoder{})) + uint64(unsafe.Sizeof(byteReader{})) + uint64(unsafe.Sizeof(bitReader{})) + 40
			if !decoderwork.Charge(w, 1, clearBytes) {
				return false, nil
			}
			p.count.Clear()
			p.table.Clear()
			p.a = decoder{}
			p.b = decoder{}
			if p.scratch != nil {
				p.scratch.br = byteReader{}
				p.scratch.bits = bitReader{}
			}
			p.dst = nil
			p.closePhase = 1
		case 1:
			if !decoderwork.Charge(w, 1, 64) {
				return false, nil
			}
			if p.scratch != nil {
				p.scratch.decTable = nil
			}
			if w != nil && p.ownedTable != 0 {
				w.Release(p.ownedTable)
			}
			p.ownedTable = 0
			p.closePhase = 2
		case 2:
			if !decoderwork.Charge(w, 1, 64) {
				return false, nil
			}
			if p.scratch != nil {
				p.scratch.ct.stateTable = nil
			}
			if w != nil && p.ownedNext != 0 {
				w.Release(p.ownedNext)
			}
			p.ownedNext = 0
			p.scratch = nil
			p.phase = 0
			p.closePhase = 3
		case 3:
			return true, nil
		}
	}
}

// Static bounded failures do not allocate or retain per-request diagnostics.
var (
	errStepTableRetention = errors.New("fse: table retention refused")
	errStepStateRetention = errors.New("fse: state retention refused")
	errStepOutputCapacity = errors.New("output size > destination capacity")
)
