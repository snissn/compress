// Copyright 2026 Klaus Post and contributors. All rights reserved.
// License information can be found in the LICENSE file.
package zstd

import (
	"encoding/binary"
	"errors"
	"io"
	"unsafe"

	"github.com/snissn/compress/huff0"
	"github.com/snissn/compress/internal/decoderwork"
	"github.com/snissn/compress/zstd/internal/xxhash"
)

// DecoderWork is the enclosing operation's work and capacity admission seam.
// Reserve debits the SAME caller operands before effects; false changes neither.
// Retain admits actual containing capacity, including allocator zeroing. Release
// follows removal of the last decoder alias, and does not release the caller's
// file, dictionary or output owner. Methods must be synchronous and bounded.
type DecoderWork = decoderwork.Work

// BorrowedDictionary binds one immutable definition to the enclosing owner.
// Raw uses the original WithDecoderDictRaw grammar. Otherwise Data is a complete
// Zstandard dictionary definition, validated in cold, suspended phases. Owner
// and the complete containing allocation must remain admitted until CloseStep.
type BorrowedDictionary struct {
	ID    uint32
	Data  []byte
	Raw   bool
	Owner any
}

// BoundedOutput selects original contiguous history in caller-admitted Raw, or
// original necessary history in individually admitted segments. Segmented output
// spans are never retained by the decoder; the caller retains bytes needed by its
// own downstream grammar (for example a template mask). No whole value is built.
type BoundedOutput uint8

const (
	BoundedRaw BoundedOutput = iota
	BoundedSpans
)

var ErrWorkRefused = errors.New("zstd: decoder capacity admission refused")
var ErrBoundedMode = errors.New("zstd: bounded decoder requires Begin/Step/CloseStep")
var ErrOutputOverlap = errors.New("zstd: bounded output overlaps immutable input or dictionary")

type boundedDecoder struct {
	work                                                                                        DecoderWork
	phase, coldTable, coldEntry, dictPhase, dictTable, tableIndex, tablePhase, closePhase       int
	dictOffsets                                                                                 [3]int
	rleScratch                                                                                  [4096]byte
	base                                                                                        [3][53]baseOffset
	predef, active, dictTables                                                                  [3]fseDecoder
	table                                                                                       sequenceTableState
	hist                                                                                        history
	segments                                                                                    segmentedHistory
	lookup                                                                                      historyLookup
	bits                                                                                        bitReader
	seq                                                                                         sequenceState
	block                                                                                       blockDec
	header                                                                                      Header
	headerState                                                                                 HeaderState
	huff                                                                                        huff0.Scratch
	dictHuff                                                                                    huff0.Scratch
	huffTable, dictHuffTable                                                                    huff0.TableState
	literal                                                                                     huff0.LiteralState
	hash                                                                                        xxhash.Digest
	in, boundInput, frameInput, seqInput, compressed, literals, pending, dictContent, dictInput []byte
	inputOwner                                                                                  any
	dictionary                                                                                  BorrowedDictionary
	output                                                                                      BoundedOutput
	raw                                                                                         []byte
	frameStart, position                                                                        uint64
	blockStart, skipRemaining                                                                   uint64
	blockType                                                                                   blockType
	blockSize, rawRemaining, litSize, mode, matchRemaining, matchOffset                         int
	four, last, begun, ready, failed, closing                                                   bool
	rle                                                                                         byte
	allocatedLiteral                                                                            uint64
	err                                                                                         error
	resetReady, coldReady                                                                       bool
	streaming, inputEnded                                                                       bool
	inputNeed                                                                                   int
	inputBuffer                                                                                 []byte
	allocatedInput                                                                              uint64
	rlePrepared, rleOffset                                                                      int
}

// NewBoundedReader creates a cold continuation on the original decoder engine.
// A false ready result has no allocation or retained edge. The caller can retry
// with the next quantum; no work is performed without an admitted containing
// Decoder and continuation. This does not initialize global tables or pools.
func NewBoundedReader(w DecoderWork) (d *Decoder, ready bool, err error) {
	if w == nil {
		return nil, false, ErrBoundedMode
	}
	size := decoderwork.AllocationCharge(uint64(unsafe.Sizeof(Decoder{}))) + decoderwork.AllocationCharge(uint64(unsafe.Sizeof(boundedDecoder{})))
	if !w.Reserve(1, size) {
		return nil, false, nil
	}
	if !w.Retain(size) {
		return nil, false, ErrWorkRefused
	}
	d = &Decoder{bounded: &boundedDecoder{work: w}}
	return d, true, nil
}

// Begin binds immutable caller-admitted input and its existing owner. Raw must
// have complete admitted capacity in BoundedRaw mode. Begin can pause before the
// binding; true does not imply that cold preparation or dictionary parsing is
// complete. A decoder is single-owner and has no background work.
func (d *Decoder) Begin(input []byte, owner any, mode BoundedOutput, raw []byte, dict BorrowedDictionary) (bool, error) {
	b := d.bounded
	if b == nil || b.begun || b.closing {
		return false, ErrBoundedMode
	}
	if owner == nil || mode > BoundedSpans || (len(dict.Data) > 0 && dict.Owner == nil) {
		return false, ErrBoundedMode
	}
	if !b.work.Reserve(3, 128) {
		return false, nil
	}
	if mode == BoundedRaw && (boundedOverlap(raw[:cap(raw)], input) || boundedOverlap(raw[:cap(raw)], dict.Data)) {
		return false, ErrOutputOverlap
	}
	b.in = input
	b.boundInput = input
	b.inputOwner = owner
	b.output = mode
	if mode == BoundedRaw {
		b.raw = raw[:0]
	} else {
		b.raw = nil
	}
	b.dictionary = dict
	b.resetReady = false
	b.begun = true
	b.phase = 0
	if !b.coldReady {
		b.coldTable = 0
		b.coldEntry = 0
	}
	b.hist.segmented = &b.segments
	return true, nil
}

// BeginStream binds the original immutable file/record owner without borrowing
// a whole compressed frame. The caller supplies wire bytes only when requested
// by InputRequest; the library performs no I/O or owner/registry lookup. One
// maximum original block plus a maximum header is admitted before allocation.
func (d *Decoder) BeginStream(owner any, mode BoundedOutput, raw []byte, dict BorrowedDictionary) (bool, error) {
	b := d.bounded
	if b == nil || b.begun || owner == nil || mode > BoundedSpans || (len(dict.Data) > 0 && dict.Owner == nil) {
		return false, ErrBoundedMode
	}
	if b.inputBuffer == nil {
		requested := maxCompressedBlockSize + 18
		size := decoderwork.AllocationCharge(uint64(requested))
		if !b.charge(1, size) {
			return false, nil
		}
		if !b.work.Retain(size) {
			return false, ErrWorkRefused
		}
		b.inputBuffer = make([]byte, requested)
		b.allocatedInput = size
	}
	ok, e := d.Begin(nil, owner, mode, raw, dict)
	if ok {
		b.streaming = true
		b.inputEnded = false
		b.inputNeed = 0
	}
	return ok, e
}

// InputRequest returns the total wire bytes required by the suspended phase.
// FeedInput may consume only part of the supplied span. The SAME enclosing
// owner prices its actual file read and containing input span before supplying
// it; no input span is borrowed after FeedInput returns.
func (d *Decoder) InputRequest() (bytes int, needed bool) {
	if d.bounded == nil {
		return 0, false
	}
	b := d.bounded
	return max(0, b.inputNeed-len(b.in)), b.inputNeed > len(b.in) && !b.inputEnded
}
func (d *Decoder) FeedInput(input []byte, final bool) (consumed int, ready bool, err error) {
	b := d.bounded
	if b == nil || !b.streaming || !b.begun || b.closing || b.inputEnded || b.inputNeed == 0 {
		return 0, false, ErrBoundedMode
	}
	if len(input) == 0 && final {
		if !b.charge(1, 0) {
			return 0, false, nil
		}
		b.inputEnded = true
		b.inputNeed = 0
		return 0, true, nil
	}
	_, avail := b.work.Available()
	n := min(len(input), b.inputNeed-len(b.in), 4096, int(min(avail/2, uint64(4096))))
	if n <= 0 || !b.charge(1, uint64(n)*2) {
		return 0, false, nil
	}
	start := len(b.in)
	b.in = b.inputBuffer[:start+n]
	copy(b.in[start:], input[:n])
	if final && n == len(input) {
		b.inputEnded = true
	}
	ready = len(b.in) >= b.inputNeed || b.inputEnded
	if ready {
		b.inputNeed = 0
	}
	return n, ready, nil
}

// needInput compacts only at a frame/header/block/trailer boundary after all
// dependent parser/bitstream/copy aliases have been cleared. A short final span
// reaches the original parser and reports its original truncation error.
func (b *boundedDecoder) needInput(n int, shortFinal bool) (bool, error) {
	if len(b.in) >= n {
		return true, nil
	}
	if !b.streaming {
		return false, io.ErrUnexpectedEOF
	}
	if b.inputEnded {
		if shortFinal {
			return true, nil
		}
		return false, io.ErrUnexpectedEOF
	}
	if b.inputNeed == 0 {
		if !b.charge(1, uint64(len(b.in))*2+64) {
			return false, nil
		}
		copy(b.inputBuffer, b.in)
		b.in = b.inputBuffer[:len(b.in)]
		b.inputNeed = n
	}
	return false, nil
}

// Step returns bytes written to the current caller-admitted output span and a
// done flag. In Raw mode output is ignored and RawResult borrows the supplied
// destination only after successful completion. Every pause preserves the exact
// original parser/table/bitstream/copy state. Errors require charged CloseStep.
func (d *Decoder) Step(output []byte) (written int, done bool, err error) {
	b := d.bounded
	if b == nil || !b.begun || b.closing {
		return 0, false, ErrBoundedMode
	}
	if b.failed {
		return 0, false, b.err
	}
	if b.output == BoundedSpans {
		if !b.charge(2, 64) {
			return 0, false, nil
		}
		if boundedOverlap(output, b.boundInput) || boundedOverlap(output, b.dictionary.Data) {
			return 0, false, ErrOutputOverlap
		}
	}
	written, done, err = b.step(output)
	if err != nil {
		b.failed = true
		b.err = err
	}
	return
}
func (d *Decoder) RawResult() ([]byte, error) {
	b := d.bounded
	if b == nil || !b.ready || b.output != BoundedRaw {
		return nil, ErrBoundedMode
	}
	return b.raw, nil
}

// Comparison covers the actual slice operands, including the complete admitted
// Raw capacity. No private layout or backing identity registry is used.
func boundedOverlap(a, b []byte) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	x, y := uintptr(unsafe.Pointer(unsafe.SliceData(a))), uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	if x <= y {
		return y-x < uintptr(len(a))
	}
	return x-y < uintptr(len(b))
}
func (b *boundedDecoder) charge(r, n uint64) bool { return b.work.Reserve(r, n) }
func (b *boundedDecoder) sequence(i int) *sequenceDec {
	switch i {
	case 0:
		return &b.hist.decoders.litLengths
	case 1:
		return &b.hist.decoders.offsets
	default:
		return &b.hist.decoders.matchLengths
	}
}
func (b *boundedDecoder) coldStep() (bool, error) {
	for b.coldTable < 3 {
		i := b.coldTable
		if b.coldEntry < 53 {
			if !b.charge(2, 12) {
				return false, nil
			}
			n := b.coldEntry
			// Same fillBase recurrence and format constants, one entry per transition.
			var base uint32
			var bits uint8
			switch i {
			case 0:
				if n < 16 {
					base = uint32(n)
				} else if n < 36 {
					bits = llBitsTable[n]
					base = b.base[i][n-1].baseLine + (1 << b.base[i][n-1].addBits)
				}
			case 1:
				if n == 1 {
					base = 1
					bits = 1
				} else if n >= 2 && n <= maxOffsetBits {
					bits = uint8(n)
					if n == 2 {
						base = 1
					} else {
						base = b.base[i][n-1].baseLine + (1 << b.base[i][n-1].addBits)
					}
				}
			case 2:
				if n < 32 {
					base = uint32(n) + 3
				} else {
					bits = mlBitsTable[n]
					base = b.base[i][n-1].baseLine + (1 << b.base[i][n-1].addBits)
				}
			}
			b.base[i][n] = baseOffset{baseLine: base, addBits: bits}
			b.predef[i].norm[n] = predefinedNorm[i][n]
			b.coldEntry++
			continue
		}
		if b.tablePhase == 0 {
			if !b.charge(1, uint64(unsafe.Sizeof(sequenceTableState{}))) {
				return false, nil
			}
			b.predef[i].actualTableLog = predefinedLogs[i]
			b.predef[i].symbolLen = uint16(predefinedLengths[i])
			b.predef[i].preDefined = true
			b.table.BeginBuild(&b.predef[i], b.base[i][:int(maxTableSymbol[i])+1])
			b.tablePhase = 1
		}
		ok, _, e := b.table.Step(b.work)
		if e != nil || !ok {
			return false, e
		}
		if !b.charge(1, uint64(unsafe.Sizeof(sequenceTableState{}))) {
			return false, nil
		}
		b.table.Clear()
		b.tablePhase = 0
		b.coldEntry = 0
		b.coldTable++
	}
	b.coldReady = true
	return true, nil
}

func (b *boundedDecoder) dictionaryStep() (bool, error) {
	for {
		switch b.dictPhase {
		case 0:
			if len(b.dictionary.Data) == 0 {
				b.dictPhase = 8
				continue
			}
			if !b.charge(1, 8+uint64(unsafe.Sizeof(huff0.TableState{}))) {
				return false, nil
			}
			b.dictInput = b.dictionary.Data
			if b.dictionary.Raw {
				b.dictContent = b.dictInput
				b.dictPhase = 8
				continue
			}
			if len(b.dictInput) <= 20 {
				return false, io.ErrUnexpectedEOF
			}
			if string(b.dictInput[:4]) != dictMagic {
				return false, ErrMagicMismatch
			}
			id := binary.LittleEndian.Uint32(b.dictInput[4:8])
			if id == 0 {
				return false, errStepDictionaryZeroID
			}
			if id != b.dictionary.ID {
				return false, ErrUnknownDictionary
			}
			b.dictInput = b.dictInput[8:]
			b.dictHuffTable.Begin(b.dictInput, &b.dictHuff)
			b.dictPhase = 1
		case 1:
			ok, in, e := b.dictHuffTable.Step(b.work)
			if e != nil {
				return false, errStepDictionaryLiteralTable
			}
			if !ok {
				return false, nil
			}
			b.dictInput = in
			b.dictTable = 0
			b.tablePhase = 0
			b.dictPhase = 2
		case 2:
			order := [3]int{1, 2, 0}
			if b.dictTable == 3 {
				b.dictPhase = 3
				continue
			}
			i := order[b.dictTable]
			if b.tablePhase == 0 {
				if !b.charge(1, uint64(unsafe.Sizeof(sequenceTableState{}))) {
					return false, nil
				}
				b.table.Begin(&b.dictTables[i], b.dictInput, uint16(maxTableSymbol[i]), b.base[i][:int(maxTableSymbol[i])+1])
				b.tablePhase = 1
			}
			ok, n, e := b.table.Step(b.work)
			if e != nil || !ok {
				return false, e
			}
			if !b.charge(1, uint64(unsafe.Sizeof(sequenceTableState{}))+24) {
				return false, nil
			}
			b.dictInput = b.dictInput[n:]
			b.table.Clear()
			b.tablePhase = 0
			b.dictTable++
		case 3, 4, 5:
			if !b.charge(1, 4) {
				return false, nil
			}
			if len(b.dictInput) < 4 {
				return false, io.ErrUnexpectedEOF
			}
			offset := int(binary.LittleEndian.Uint32(b.dictInput))
			b.dictInput = b.dictInput[4:]
			if offset <= 0 {
				return false, errStepDictionaryOffset
			}
			b.dictOffsets[b.dictPhase-3] = offset
			b.dictPhase++
		case 6:
			if !b.charge(3, 0) {
				return false, nil
			}
			for _, offset := range b.dictOffsets {
				if offset > len(b.dictInput) {
					return false, errStepDictionaryOffsetRange
				}
			}
			b.dictContent = b.dictInput
			b.dictPhase = 8
		case 8:
			return true, nil
		}
	}
}

func (b *boundedDecoder) step(output []byte) (written int, done bool, err error) {
	for {
		switch b.phase {
		case 0:
			ok, e := b.coldStep()
			if e != nil || !ok {
				return written, false, e
			}
			b.phase = 1
		case 1:
			ok, e := b.dictionaryStep()
			if e != nil || !ok {
				return written, false, e
			}
			b.phase = 2
		case 2:
			if b.streaming && !b.inputEnded {
				ok, e := b.needInput(18, true)
				if e != nil || !ok {
					return written, false, e
				}
			}
			if len(b.in) == 0 {
				b.ready = true
				return written, true, nil
			}
			if !b.charge(1, uint64(unsafe.Sizeof(Header{}))+uint64(unsafe.Sizeof(HeaderState{}))) {
				return written, false, nil
			}
			b.headerState.Begin(&b.header, b.in, false)
			b.phase = 3
		case 3:
			ok, in, e := b.headerState.Step(b.work)
			if e != nil || !ok {
				return written, false, e
			}
			if !b.charge(1, uint64(unsafe.Sizeof(HeaderState{}))+24) {
				return written, false, nil
			}
			b.in = in
			b.headerState.Clear()
			if b.header.Skippable {
				b.skipRemaining = uint64(b.header.SkippableSize)
				b.phase = 4
				continue
			}
			if !b.charge(1, 512) {
				b.phase = 5
				return written, false, nil
			}
			b.phase = 6
		case 4:
			if b.skipRemaining == 0 {
				b.phase = 2
				continue
			}
			if b.streaming && len(b.in) == 0 {
				ok, e := b.needInput(int(min(b.skipRemaining, 4096)), false)
				if e != nil || !ok {
					return written, false, e
				}
			}
			_, avail := b.work.Available()
			n := int(min(b.skipRemaining, 4096, avail))
			if b.streaming {
				n = min(n, len(b.in))
			}
			if n == 0 || !b.charge(1, uint64(n)) {
				return written, false, nil
			}
			if len(b.in) < n {
				return written, false, io.ErrUnexpectedEOF
			}
			b.in = b.in[n:]
			b.skipRemaining -= uint64(n)
		case 5:
			if !b.charge(1, 512) {
				return written, false, nil
			}
			b.phase = 6
		case 6:
			window := b.header.WindowSize
			if window > MaxWindowSize {
				return written, false, ErrWindowSizeExceeded
			}
			if b.header.SingleSegment {
				window = max(b.header.FrameContentSize, MinWindowSize)
			}
			if window == 0 || window > uint64(^uint(0)>>1) {
				return written, false, ErrWindowSizeExceeded
			}
			if b.header.HasFCS && b.header.FrameContentSize > 64<<30 {
				return written, false, ErrDecoderSizeExceeded
			}
			b.hist.windowSize = int(window)
			b.segments.window = window
			b.frameStart = b.position
			b.hist.recentOffsets = [3]int{1, 4, 8}
			b.hist.huffTree = nil
			b.hist.decoders = sequenceDecs{br: &b.bits}
			if b.header.DictionaryID != 0 || (b.dictionary.ID == 0 && len(b.dictionary.Data) != 0) {
				if b.header.DictionaryID != b.dictionary.ID {
					return written, false, ErrUnknownDictionary
				}
				b.hist.decoders.dict = b.dictContent
				if !b.dictionary.Raw {
					b.hist.huffTree = &b.dictHuff
					for i := 0; i < 3; i++ {
						b.sequence(i).fse = &b.dictTables[i]
					}
					// Offsets are saved separately by the dictionary parser below.
					b.hist.recentOffsets = b.dictOffsets
				}
			}
			b.hash.Reset()
			b.phase = 7
		case 7:
			ok, e := b.needInput(3, false)
			if e != nil || !ok {
				return written, false, e
			}
			if !b.charge(1, 3) {
				return written, false, nil
			}
			if len(b.in) < 3 {
				return written, false, io.ErrUnexpectedEOF
			}
			v := uint32(b.in[0]) | uint32(b.in[1])<<8 | uint32(b.in[2])<<16
			b.in = b.in[3:]
			b.last = v&1 != 0
			b.blockType = blockType((v >> 1) & 3)
			b.blockSize = int(v >> 3)
			b.blockStart = b.position
			if b.blockType == blockTypeReserved {
				return written, false, ErrReservedBlockType
			}
			if b.blockSize > maxCompressedBlockSize || b.blockSize > b.hist.windowSize {
				return written, false, ErrUnexpectedBlockSize
			}
			b.phase = 70
		case 70:
			stored := b.blockSize
			if b.blockType == blockTypeRLE {
				stored = 1
			}
			ok, e := b.needInput(stored, false)
			if e != nil || !ok {
				return written, false, e
			}
			if !b.charge(1, 128) {
				return written, false, nil
			}
			switch b.blockType {
			case blockTypeRaw:
				if len(b.in) < b.blockSize {
					return written, false, io.ErrUnexpectedEOF
				}
				b.pending = b.in[:b.blockSize]
				b.in = b.in[b.blockSize:]
				b.phase = 30
			case blockTypeRLE:
				if len(b.in) < 1 {
					return written, false, io.ErrUnexpectedEOF
				}
				b.rle = b.in[0]
				b.in = b.in[1:]
				b.rawRemaining = b.blockSize
				b.phase = 31
			case blockTypeCompressed:
				if len(b.in) < b.blockSize {
					return written, false, io.ErrUnexpectedEOF
				}
				b.compressed = b.in[:b.blockSize]
				b.in = b.in[b.blockSize:]
				b.phase = 8
			}
		case 8:
			if !b.charge(8, 8) {
				return written, false, nil
			}
			typ, regen, comp, four, in, e := parseLiteralHeader(b.compressed)
			if e != nil {
				return written, false, e
			}
			if regen > maxCompressedBlockSize || regen > b.hist.windowSize {
				return written, false, ErrUnexpectedBlockSize
			}
			b.litSize = regen
			b.four = four
			b.seqInput = in
			switch typ {
			case literalsBlockRaw:
				if len(in) < regen {
					return written, false, ErrBlockTooSmall
				}
				b.literals = in[:regen]
				b.seqInput = in[regen:]
				b.phase = 12
			case literalsBlockRLE:
				if len(in) < 1 {
					return written, false, ErrBlockTooSmall
				}
				b.rle = in[0]
				b.seqInput = in[1:]
				b.rawRemaining = regen
				b.phase = 9
			default:
				if len(in) < comp {
					return written, false, ErrBlockTooSmall
				}
				b.compressed = in[:comp]
				b.seqInput = in[comp:]
				if typ == literalsBlockCompressed {
					b.phase = 10
				} else {
					if b.hist.huffTree == nil {
						return written, false, errStepTreelessWithoutHistory
					}
					b.phase = 11
				}
			}
		case 9, 10, 11:
			if b.block.literalBuf == nil {
				requested := maxCompressedBlockSize + compressedBlockOverAlloc
				size := decoderwork.AllocationCharge(uint64(requested))
				if !b.charge(1, size) {
					return written, false, nil
				}
				if !b.work.Retain(size) {
					return written, false, ErrWorkRefused
				}
				b.block.literalBuf = make([]byte, requested)
				b.allocatedLiteral = size
			}
			if b.phase == 9 {
				_, avail := b.work.Available()
				n := min(b.rawRemaining, 4096, int(min(avail, uint64(4096))))
				if b.rawRemaining != 0 && (n == 0 || !b.charge(1, uint64(n))) {
					return written, false, nil
				}
				start := b.litSize - b.rawRemaining
				for i := range b.block.literalBuf[start : start+n] {
					b.block.literalBuf[start+i] = b.rle
				}
				b.rawRemaining -= n
				if b.rawRemaining == 0 {
					b.literals = b.block.literalBuf[:b.litSize]
					b.phase = 12
				}
				continue
			}
			if b.phase == 10 {
				if !b.charge(1, uint64(unsafe.Sizeof(huff0.TableState{}))) {
					return written, false, nil
				}
				b.huffTable.Begin(b.compressed, &b.huff)
				b.phase = 100
				continue
			}
			if !b.charge(1, uint64(unsafe.Sizeof(huff0.LiteralState{}))) {
				return written, false, nil
			}
			b.literal.Begin(b.hist.huffTree, b.compressed, b.block.literalBuf[:b.litSize], b.four)
			b.phase = 110
		case 100:
			ok, in, e := b.huffTable.Step(b.work)
			if e != nil || !ok {
				return written, false, e
			}
			b.hist.huffTree = &b.huff
			b.compressed = in
			b.phase = 11
		case 110:
			ok, e := b.literal.Step(b.work)
			if e != nil || !ok {
				return written, false, e
			}
			b.literals = b.block.literalBuf[:b.litSize]
			b.phase = 12
		case 12:
			if !b.charge(1, 3) {
				return written, false, nil
			}
			in := b.seqInput
			if len(in) < 1 {
				return written, false, ErrBlockTooSmall
			}
			n := int(in[0])
			in = in[1:]
			if n >= 128 && n < 255 {
				if len(in) < 1 {
					return written, false, ErrBlockTooSmall
				}
				n = (n-128)<<8 | int(in[0])
				in = in[1:]
			} else if n == 255 {
				if len(in) < 2 {
					return written, false, ErrBlockTooSmall
				}
				n = 0x7f00 + int(in[0]) + int(in[1])<<8
				in = in[2:]
			}
			b.hist.decoders.nSeqs = n
			b.seqInput = in
			if n == 0 {
				if len(in) != 0 {
					return written, false, ErrUnexpectedBlockSize
				}
				b.pending = b.literals
				b.phase = 30
				continue
			}
			b.phase = 13
		case 13:
			if !b.charge(1, 1) {
				return written, false, nil
			}
			if len(b.seqInput) < 1 {
				return written, false, ErrBlockTooSmall
			}
			b.mode = int(b.seqInput[0])
			b.seqInput = b.seqInput[1:]
			if b.mode&3 != 0 {
				return written, false, errStepReservedSequenceBits
			}
			b.tableIndex = 0
			b.tablePhase = 0
			b.phase = 14
		case 14:
			if b.tableIndex == 3 {
				b.phase = 15
				continue
			}
			i := b.tableIndex
			s := b.sequence(i)
			if b.tablePhase == 0 {
				if !b.charge(1, 0) {
					return written, false, nil
				}
				mode := seqCompMode((b.mode >> (6 - i*2)) & 3)
				switch mode {
				case compModePredefined:
					s.fse = &b.predef[i]
					b.tableIndex++
					continue
				case compModeRepeat:
					if s.fse == nil {
						return written, false, errStepUndefinedSequenceDecoder
					}
					b.tableIndex++
					continue
				case compModeRLE:
					b.tablePhase = 2
				case compModeFSE:
					b.table.Begin(&b.active[i], b.seqInput, uint16(maxTableSymbol[i]), b.base[i][:int(maxTableSymbol[i])+1])
					b.tablePhase = 1
				}
			}
			if b.tablePhase == 2 {
				if !b.charge(2, 9) {
					return written, false, nil
				}
				if len(b.seqInput) < 1 {
					return written, false, ErrBlockTooSmall
				}
				sym, e := decSymbolValue(b.seqInput[0], b.base[i][:int(maxTableSymbol[i])+1])
				if e != nil {
					return written, false, e
				}
				b.active[i].setRLE(sym)
				b.seqInput = b.seqInput[1:]
				s.fse = &b.active[i]
				b.tablePhase = 0
				b.tableIndex++
				continue
			}
			ok, n, e := b.table.Step(b.work)
			if e != nil || !ok {
				return written, false, e
			}
			if !b.charge(1, uint64(unsafe.Sizeof(sequenceTableState{}))+32) {
				return written, false, nil
			}
			b.seqInput = b.seqInput[n:]
			s.fse = &b.active[i]
			b.table.Clear()
			b.tablePhase = 0
			b.tableIndex++
		case 15:
			if !b.charge(1, 9+uint64(unsafe.Sizeof(sequenceState{}))) {
				return written, false, nil
			}
			if e := b.bits.init(b.seqInput); e != nil {
				return written, false, e
			}
			s := &b.hist.decoders
			s.br = &b.bits
			s.windowSize = b.hist.windowSize
			s.prevOffset = b.hist.recentOffsets
			s.maxBits = s.litLengths.fse.maxBits + s.offsets.fse.maxBits + s.matchLengths.fse.maxBits
			b.seq.Begin(s)
			b.phase = 16
		case 16:
			ready, end, e := b.seq.Next(b.work)
			if e != nil {
				return written, false, e
			}
			if !ready && !end {
				return written, false, nil
			}
			if end {
				b.pending = b.literals
				b.hist.recentOffsets = b.hist.decoders.prevOffset
				b.phase = 30
				continue
			}
			if !b.charge(1, 0) {
				b.phase = 160
				return written, false, nil
			}
			b.phase = 161
		case 160:
			if !b.charge(1, 0) {
				return written, false, nil
			}
			b.phase = 161
		case 161:
			ll, ml, mo := b.seq.literals, b.seq.match, b.seq.offset
			if ll > len(b.literals) {
				return written, false, errStepLiteralCount
			}
			if ml > maxMatchLen {
				return written, false, errStepMatchLength
			}
			if mo == 0 && ml > 0 {
				return written, false, errStepZeroMatchOffset
			}
			if b.position-b.blockStart+uint64(ll+ml) > uint64(min(b.hist.windowSize, maxCompressedBlockSize)) {
				return written, false, ErrUnexpectedBlockSize
			}
			b.pending = b.literals[:ll]
			b.literals = b.literals[ll:]
			b.matchRemaining = ml
			b.matchOffset = mo
			b.phase = 17
		case 17:
			if len(b.pending) > 0 {
				n, e := b.emit(output[written:], b.pending)
				if e != nil {
					return written, false, e
				}
				if n == 0 {
					return written, false, nil
				}
				b.pending = b.pending[n:]
				if b.output == BoundedSpans {
					written += n
				}
				continue
			}
			b.phase = 18
		case 18:
			if b.matchRemaining == 0 {
				b.lookup.clear()
				b.pending = nil
				b.rlePrepared = 0
				b.rleOffset = 0
				b.seq.Copied()
				b.phase = 16
				continue
			}
			if len(b.pending) == 0 {
				available := b.position - b.frameStart
				if uint64(b.matchOffset) > available || b.matchOffset > b.hist.windowSize {
					dictPos := int64(len(b.hist.decoders.dict)) - (int64(b.matchOffset) - int64(available))
					if dictPos < 0 || dictPos >= int64(len(b.hist.decoders.dict)) {
						return written, false, errStepMatchHistoryRange
					}
					if !b.charge(1, 0) {
						return written, false, nil
					}
					b.pending = b.hist.decoders.dict[int(dictPos):]
				} else if b.output == BoundedRaw {
					if !b.charge(1, 0) {
						return written, false, nil
					}
					b.pending = b.raw[int(b.position)-b.matchOffset:]
				} else {
					if !b.lookup.started {
						if !b.charge(1, 0) {
							return written, false, nil
						}
						// Segment coordinates restart with each independent frame. Raw output
						// remains cumulative, but it is never eligible as prior-frame history.
						b.lookup.begin(&b.segments, b.position-b.frameStart-uint64(b.matchOffset))
					}
					ok, src, e := b.lookup.step(b.work)
					if e != nil || !ok {
						return written, false, e
					}
					b.pending = src
				}
			}
			n := min(len(b.pending), b.matchRemaining, b.matchOffset)
			copied, e := b.emit(output[written:], b.pending[:n])
			if e != nil {
				return written, false, e
			}
			if copied == 0 {
				return written, false, nil
			}
			b.pending = b.pending[copied:]
			b.matchRemaining -= copied
			if b.output == BoundedSpans {
				written += copied
			}
			if len(b.pending) == 0 || b.matchRemaining == 0 {
				b.pending = nil
				b.rlePrepared = 0
				b.rleOffset = 0
			}
		case 30:
			if len(b.pending) > 0 {
				n, e := b.emit(output[written:], b.pending)
				if e != nil {
					return written, false, e
				}
				if n == 0 {
					return written, false, nil
				}
				b.pending = b.pending[n:]
				if b.output == BoundedSpans {
					written += n
				}
				continue
			}
			b.phase = 32
		case 31:
			if b.rawRemaining > 0 {
				n, e := b.emitRLE(output[written:], b.rawRemaining, b.rle)
				if e != nil {
					return written, false, e
				}
				if n == 0 {
					return written, false, nil
				}
				b.rawRemaining -= n
				if b.output == BoundedSpans {
					written += n
				}
				continue
			}
			b.phase = 32
		case 32:
			if !b.charge(1, 2048) {
				return written, false, nil
			}
			b.pending = nil
			b.rlePrepared = 0
			b.rleOffset = 0
			b.literals = nil
			b.compressed = nil
			b.seqInput = nil
			b.literal.Clear()
			b.huffTable.DetachInput()
			b.bits = bitReader{}
			b.seq.Clear()
			b.lookup.clear()
			if b.position-b.blockStart > uint64(min(b.hist.windowSize, maxCompressedBlockSize)) {
				return written, false, ErrUnexpectedBlockSize
			}
			b.phase = 33
		case 33:
			if b.output == BoundedSpans {
				ok, e := b.segments.retireStep(b.work)
				if e != nil || !ok {
					return written, false, e
				}
			}
			if b.last {
				b.phase = 34
			} else {
				b.phase = 7
			}
		case 34:
			if !b.charge(1, 0) {
				return written, false, nil
			}
			if b.header.HasFCS && b.position-b.frameStart != b.header.FrameContentSize {
				return written, false, ErrFrameSizeMismatch
			}
			if b.header.HasCheckSum {
				b.phase = 35
			} else {
				b.phase = 36
			}
		case 35:
			ok, e := b.needInput(4, false)
			if e != nil || !ok {
				return written, false, e
			}
			if !b.charge(1, 4+uint64(unsafe.Sizeof(xxhash.Digest{}))) {
				return written, false, nil
			}
			if len(b.in) < 4 {
				return written, false, io.ErrUnexpectedEOF
			}
			want := binary.LittleEndian.Uint32(b.in)
			b.in = b.in[4:]
			if uint32(b.hash.Sum64()) != want {
				return written, false, ErrCRCMismatch
			}
			b.phase = 36
		case 36:
			ok, e := b.segments.clearStep(b.work)
			if e != nil || !ok {
				return written, false, e
			}
			b.phase = 2
		}
	}
}

func (b *boundedDecoder) emit(dst, src []byte) (int, error) {
	if len(src) == 0 {
		return 0, nil
	}
	// Segment admission is its own resumable action; no checksum/output work is
	// reserved or performed until the exact copy operand is known.
	if b.output == BoundedSpans {
		if len(dst) == 0 {
			return 0, nil
		}
		ok, e := b.segments.ensureTail(b.work)
		if e != nil || !ok {
			return 0, e
		}
	}
	_, avail := b.work.Available()
	if avail <= 128 {
		return 0, nil
	}
	effects, perByte := uint64(2), uint64(4) // copy plus original checksum
	if b.output == BoundedSpans {
		effects, perByte = 3, 5
	} // history copy, span copy, checksum scan
	n := min(len(src), 4096, int(min((avail-128)/perByte, uint64(4096))))
	if b.output == BoundedSpans {
		n = min(n, len(dst), historySegmentBytes-b.segments.tail.used)
	}
	if n == 0 {
		return 0, nil
	}
	if b.position-b.frameStart+uint64(n) > 64<<30 {
		return 0, ErrDecoderSizeExceeded
	}
	if b.header.HasFCS && b.position-b.frameStart+uint64(n) > b.header.FrameContentSize {
		return 0, ErrFrameSizeExceeded
	}
	if b.output == BoundedRaw && n > cap(b.raw)-len(b.raw) {
		return 0, ErrDecoderSizeExceeded
	}
	// Original checksum's bounded carry buffer and state plus the actual copy,
	// checksum scan and (segmented mode) history copy are admitted together.
	if !b.charge(effects, uint64(n)*perByte+128) {
		return 0, nil
	}
	if b.output == BoundedRaw {
		start := len(b.raw)
		b.raw = b.raw[:start+n]
		copy(b.raw[start:], src[:n])
		b.hash.Write(b.raw[start:])
	} else {
		b.segments.appendAdmitted(src[:n], dst[:n])
		b.hash.Write(dst[:n])
	}
	b.position += uint64(n)
	return n, nil
}
func (b *boundedDecoder) emitRLE(dst []byte, count int, v byte) (int, error) {
	// Preserve already-expanded bytes across a refusal before the copy/hash phase.
	// The containing scratch was admitted with the original continuation.
	if b.rlePrepared == 0 {
		_, avail := b.work.Available()
		n := min(count, len(b.rleScratch), int(min(avail, uint64(len(b.rleScratch)))))
		if b.output == BoundedSpans {
			n = min(n, len(dst))
		}
		if n == 0 || !b.charge(1, uint64(n)) {
			return 0, nil
		}
		for i := range b.rleScratch[:n] {
			b.rleScratch[i] = v
		}
		b.rlePrepared = n
		b.rleOffset = 0
	}
	n, e := b.emit(dst, b.rleScratch[b.rleOffset:b.rleOffset+b.rlePrepared])
	b.rleOffset += n
	b.rlePrepared -= n
	if b.rlePrepared == 0 {
		b.rleOffset = 0
	}
	return n, e
}

// CloseStep is also cancellation. It can pause after any failure or decode
// phase. Actual borrowed owners are kept until every dependent alias is cleared;
// Release applies only to this decoder's admitted storage, never physical files.
func (d *Decoder) CloseStep() (bool, error) { return d.clearBounded(true) }

// BoundedControlSize is the conservative allocation admission of the fixed
// returned Decoder object, distinct from its addressable size. CloseStep detaches
// its owned continuation but cannot retire a caller-reachable closed Decoder.
// The enclosing SAME owner releases these admitted bytes only after dropping
// its final control pointer; this is not physical file/root retirement.
func BoundedControlSize() uint64 {
	return decoderwork.AllocationCharge(uint64(unsafe.Sizeof(Decoder{})))
}

// ResetStep clears previous input/dictionary aliases and backings in the SAME
// continuation, preserving already-constructed format tables. The caller must
// keep new arguments immutable/admitted while retrying; old owner retirement
// is allowed only after true. Fixed control/continuation is never readmitted.
func (d *Decoder) ResetStep(input []byte, owner any, mode BoundedOutput, raw []byte, dict BorrowedDictionary) (bool, error) {
	b := d.bounded
	if b == nil || owner == nil || mode > BoundedSpans || (len(dict.Data) > 0 && dict.Owner == nil) {
		return false, ErrBoundedMode
	}
	if !b.resetReady {
		ok, e := d.clearBounded(false)
		if e != nil || !ok {
			return false, e
		}
	}
	return d.Begin(input, owner, mode, raw, dict)
}

// ResetStreamStep retires the previous stream's aliases/backings and reuses
// the SAME fixed continuation/predefined tables with a new immutable owner.
func (d *Decoder) ResetStreamStep(owner any, mode BoundedOutput, raw []byte, dict BorrowedDictionary) (bool, error) {
	b := d.bounded
	if b == nil || owner == nil || mode > BoundedSpans || (len(dict.Data) > 0 && dict.Owner == nil) {
		return false, ErrBoundedMode
	}
	if !b.resetReady {
		ok, e := d.clearBounded(false)
		if e != nil || !ok {
			return false, e
		}
	}
	return d.BeginStream(owner, mode, raw, dict)
}

func (d *Decoder) clearBounded(releaseCore bool) (bool, error) {
	b := d.bounded
	if b == nil {
		return true, nil
	}
	b.closing = true
	for {
		switch b.closePhase {
		case 0:
			clearBytes := uint64(unsafe.Sizeof(huff0.LiteralState{})) + uint64(unsafe.Sizeof(sequenceState{})) + uint64(unsafe.Sizeof(sequenceTableState{})) + uint64(unsafe.Sizeof(historyLookup{})) + uint64(unsafe.Sizeof(HeaderState{})) + uint64(unsafe.Sizeof(bitReader{})) + uint64(unsafe.Sizeof(sequenceDecs{})) + 128
			if !b.charge(1, clearBytes) {
				return false, nil
			}
			b.pending = nil
			b.rlePrepared = 0
			b.rleOffset = 0
			b.literals = nil
			b.compressed = nil
			b.seqInput = nil
			b.literal.Clear()
			b.seq.Clear()
			b.table.Clear()
			b.lookup.clear()
			b.headerState.Clear()
			b.bits = bitReader{}
			b.hist.decoders = sequenceDecs{}
			b.hist.huffTree = nil
			b.hist.b = nil
			b.hist.dict = nil
			b.closePhase = 1
		case 1:
			ok, e := b.segments.clearStep(b.work)
			if e != nil || !ok {
				return false, e
			}
			b.closePhase = 2
		case 2:
			ok, e := b.huffTable.ClearStep(b.work)
			if e != nil || !ok {
				return false, e
			}
			b.closePhase = 3
		case 3:
			ok, e := b.dictHuffTable.ClearStep(b.work)
			if e != nil || !ok {
				return false, e
			}
			b.closePhase = 4
		case 4:
			if !b.charge(1, 1024) {
				return false, nil
			}
			b.block.literalBuf = nil
			if b.allocatedLiteral != 0 {
				b.work.Release(b.allocatedLiteral)
				b.allocatedLiteral = 0
			}
			b.closePhase = 5
		case 5:
			if !b.charge(1, 1024) {
				return false, nil
			}
			b.in = nil
			b.boundInput = nil
			b.frameInput = nil
			b.raw = nil
			b.dictContent = nil
			b.dictInput = nil
			b.dictionary = BorrowedDictionary{}
			b.inputOwner = nil
			b.inputBuffer = nil
			if b.allocatedInput != 0 {
				b.work.Release(b.allocatedInput)
				b.allocatedInput = 0
			}
			b.closePhase = 6
		case 6:
			if !b.charge(1, 1024) {
				return false, nil
			}
			if !releaseCore {
				b.err = nil
				b.failed = false
				b.closing = false
				b.begun = false
				b.ready = false
				b.position = 0
				b.frameStart = 0
				b.blockStart = 0
				b.dictPhase = 0
				b.dictTable = 0
				b.tablePhase = 0
				b.closePhase = 0
				b.phase = 0
				b.resetReady = true
				b.streaming = false
				b.inputEnded = false
				b.inputNeed = 0
				return true, nil
			}
			w := b.work
			b.work = nil
			d.bounded = nil
			b = nil
			w.Release(decoderwork.AllocationCharge(uint64(unsafe.Sizeof(boundedDecoder{}))))
			return true, nil
		}
	}
}

// Static bounded failures do not allocate or retain per-request diagnostics.
var (
	errStepDictionaryZeroID         = errors.New("dictionaries cannot have ID 0")
	errStepDictionaryLiteralTable   = errors.New("loading literal table: invalid definition")
	errStepDictionaryOffset         = errors.New("invalid offset in dictionary")
	errStepDictionaryOffsetRange    = errors.New("initial offset exceeds dictionary content size")
	errStepTreelessWithoutHistory   = errors.New("literal block was treeless, but no history was defined")
	errStepReservedSequenceBits     = errors.New("corrupt block: reserved bits not zero")
	errStepUndefinedSequenceDecoder = errors.New("sequence decoder not defined")
	errStepLiteralCount             = errors.New("literal count exceeds available literals")
	errStepMatchLength              = errors.New("match length exceeds maximum block length")
	errStepZeroMatchOffset          = errors.New("zero offset for nonempty match")
	errStepMatchHistoryRange        = errors.New("match offset exceeds current history")
)
