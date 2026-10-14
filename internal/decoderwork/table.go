// Copyright 2018-2026 Klaus Post and contributors. All rights reserved.
// License information can be found in the LICENSE file.
// This is the original FSE decoder table builder with explicit continuation.
package decoderwork

import (
	"errors"
	"fmt"
	"math/bits"
)

// Table builds the original spread and next-state table in producer storage.
// The callbacks each access exactly one already-admitted entry.
type Table struct {
	norm                                []int16
	next                                []uint16
	symbols                             int
	log                                 uint8
	phase, i, j                         int
	size, high, pos                     uint32
	current                             int16
	ZeroBits                            bool
	readSymbol, writeSymbol, writeEntry uint64
}

func (t *Table) Begin(norm []int16, next []uint16, symbols int, log uint8, readSymbol, writeSymbol, writeEntry uint64) {
	*t = Table{norm: norm, next: next, symbols: symbols, log: log, size: 1 << log, high: (1 << log) - 1, readSymbol: readSymbol, writeSymbol: writeSymbol, writeEntry: writeEntry}
}
func (t *Table) Step(w Work, setSymbol func(int, uint8), symbol func(int) uint8, setEntry func(int, uint8, uint16)) (bool, error) {
	for {
		switch t.phase {
		case 0:
			if t.i == t.symbols {
				t.i = 0
				t.phase = 1
				continue
			}
			if !Charge(w, 3, 4+t.writeSymbol) {
				return false, nil
			}
			v := t.norm[t.i]
			if v >= int16(1<<(t.log-1)) {
				t.ZeroBits = true
			}
			if v == -1 {
				setSymbol(int(t.high), uint8(t.i))
				t.high--
				v = 1
			}
			t.next[t.i] = uint16(v)
			t.i++
		case 1:
			if t.i == t.symbols {
				if !Charge(w, 1, 0) {
					return false, nil
				}
				if t.pos != 0 {
					return false, errStepTablePosition
				}
				t.i = 0
				t.phase = 4
				continue
			}
			if !Charge(w, 1, 2) {
				return false, nil
			}
			t.current = t.norm[t.i]
			t.j = 0
			t.phase = 2
		case 2:
			if t.j >= int(t.current) {
				t.i++
				t.phase = 1
				continue
			}
			if !Charge(w, 1, t.writeSymbol) {
				return false, nil
			}
			setSymbol(int(t.pos), uint8(t.i))
			t.j++
			t.phase = 3
		case 3:
			if !Charge(w, 1, 0) {
				return false, nil
			}
			t.pos = (t.pos + (t.size >> 1) + (t.size >> 3) + 3) & (t.size - 1)
			if t.pos <= t.high {
				t.phase = 2
			}
		case 4:
			if t.i == int(t.size) {
				t.phase = 5
				continue
			}
			if !Charge(w, 3, t.readSymbol+4+t.writeEntry) {
				return false, nil
			}
			s := symbol(t.i)
			next := t.next[s]
			t.next[s] = next + 1
			n := t.log - uint8(bits.Len32(uint32(next))-1)
			ns := (next << n) - uint16(t.size)
			if uint32(ns) >= t.size {
				if w == nil {
					return false, fmt.Errorf("newState (%d) outside table size (%d)", ns, t.size)
				}
				return false, errStepTableStateRange
			}
			if ns == uint16(t.i) && n == 0 {
				if w == nil {
					return false, fmt.Errorf("newState (%d) == oldState (%d) and no bits", ns, t.i)
				}
				return false, errStepTableZeroBitCycle
			}
			setEntry(t.i, n, ns)
			t.i++
		case 5:
			return true, nil
		}
	}
}
func (t *Table) Clear() { *t = Table{} }

// Static bounded failures do not allocate or retain per-request diagnostics.
var (
	errStepTablePosition     = errors.New("corrupted input (position != 0)")
	errStepTableStateRange   = errors.New("new state outside table size")
	errStepTableZeroBitCycle = errors.New("state cycle without bits")
)
