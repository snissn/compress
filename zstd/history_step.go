// Copyright 2019-2026 Klaus Post and contributors. All rights reserved.
// License information can be found in the LICENSE file.
package zstd

import (
	"errors"
	"github.com/snissn/compress/internal/decoderwork"
	"unsafe"
)

const historySegmentBytes = 32 << 10

type historySegment struct {
	previous, next *historySegment
	start          uint64
	used           int
	data           [historySegmentBytes]byte
}
type segmentedHistory struct {
	head, tail *historySegment
	position   uint64
	window     uint64
}

// ensureTail admits one original-history segment before allocation. A pause
// after allocation retains that SAME segment without another admission.
func (h *segmentedHistory) ensureTail(w decoderwork.Work) (bool, error) {
	if h.tail != nil && h.tail.used < historySegmentBytes {
		return true, nil
	}
	size := decoderwork.AllocationCharge(uint64(unsafe.Sizeof(historySegment{})))
	if !decoderwork.Charge(w, 1, size) {
		return false, nil
	}
	if !w.Retain(size) {
		return false, errStepHistoryRetention
	}
	p := &historySegment{previous: h.tail, start: h.position}
	if h.tail == nil {
		h.head = p
	} else {
		h.tail.next = p
	}
	h.tail = p
	return true, nil
}

// appendAdmitted consumes the exact operand already reserved with checksum
// work by the original decoder. Input/output slices do not escape this call.
func (h *segmentedHistory) appendAdmitted(src, dst []byte) {
	copy(h.tail.data[h.tail.used:h.tail.used+len(src)], src)
	copy(dst, h.tail.data[h.tail.used:h.tail.used+len(src)])
	h.tail.used += len(src)
	h.position += uint64(len(src))
}

// retireStep runs only between blocks, after partial match aliases are clear.
// Retention is the actual required window plus the current bounded block.
func (h *segmentedHistory) retireStep(w decoderwork.Work) (bool, error) {
	for h.head != nil && h.head != h.tail && h.position-(h.head.start+uint64(h.head.used)) >= h.window {
		if !decoderwork.Charge(w, 1, 40) {
			return false, nil
		}
		old := h.head
		h.head = old.next
		h.head.previous = nil
		old.next = nil
		old.previous = nil
		old = nil
		w.Release(decoderwork.AllocationCharge(uint64(unsafe.Sizeof(historySegment{}))))
	}
	return true, nil
}
func (h *segmentedHistory) clearStep(w decoderwork.Work) (bool, error) {
	for h.head != nil {
		if !decoderwork.Charge(w, 1, 40) {
			return false, nil
		}
		old := h.head
		h.head = old.next
		if h.head != nil {
			h.head.previous = nil
		} else {
			h.tail = nil
		}
		old.next = nil
		old.previous = nil
		old = nil
		w.Release(decoderwork.AllocationCharge(uint64(unsafe.Sizeof(historySegment{}))))
	}
	h.position = 0
	h.window = 0
	return true, nil
}

type historyLookup struct {
	node     *historySegment
	position uint64
	started  bool
}

func (p *historyLookup) begin(h *segmentedHistory, position uint64) {
	*p = historyLookup{node: h.tail, position: position, started: true}
}
func (p *historyLookup) step(w decoderwork.Work) (done bool, src []byte, err error) {
	for p.node != nil {
		if !decoderwork.Charge(w, 1, 24) {
			return false, nil, nil
		}
		n := p.node
		if p.position >= n.start && p.position < n.start+uint64(n.used) {
			offset := int(p.position - n.start)
			src = n.data[offset:n.used]
			p.node = nil
			p.started = false
			return true, src, nil
		}
		p.node = n.previous
	}
	return false, nil, errStepMissingHistory
}
func (p *historyLookup) clear() { *p = historyLookup{} }

// Static bounded failures do not allocate or retain per-request diagnostics.
var (
	errStepHistoryRetention = errors.New("zstd: history retention refused")
	errStepMissingHistory   = errors.New("zstd: match outside retained history")
)
