// Copyright 2020+ Klaus Post. All rights reserved.
// License information can be found in the LICENSE file.

package zstd

// HeaderMaxSize is the maximum size of a Frame and Block Header.
// If less is sent to Header.Decode it *may* still contain enough information.
const HeaderMaxSize = 14 + 3

// Header contains information about the first frame and block within that.
type Header struct {
	// SingleSegment specifies whether the data is to be decompressed into a
	// single contiguous memory segment.
	// It implies that WindowSize is invalid and that FrameContentSize is valid.
	SingleSegment bool

	// WindowSize is the window of data to keep while decoding.
	// Will only be set if SingleSegment is false.
	WindowSize uint64

	// Dictionary ID.
	// If 0, no dictionary.
	DictionaryID uint32

	// HasFCS specifies whether FrameContentSize has a valid value.
	HasFCS bool

	// FrameContentSize is the expected uncompressed size of the entire frame.
	FrameContentSize uint64

	// Skippable will be true if the frame is meant to be skipped.
	// This implies that FirstBlock.OK is false.
	Skippable bool

	// SkippableID is the user-specific ID for the skippable frame.
	// Valid values are between 0 to 15, inclusive.
	SkippableID int

	// SkippableSize is the length of the user data to skip following
	// the header.
	SkippableSize uint32

	// HeaderSize is the raw size of the frame header.
	//
	// For normal frames, it includes the size of the magic number and
	// the size of the header (per section 3.1.1.1).
	// It does not include the size for any data blocks (section 3.1.1.2) nor
	// the size for the trailing content checksum.
	//
	// For skippable frames, this counts the size of the magic number
	// along with the size of the size field of the payload.
	// It does not include the size of the skippable payload itself.
	// The total frame size is the HeaderSize plus the SkippableSize.
	HeaderSize int

	// First block information.
	FirstBlock struct {
		// OK will be set if first block could be decoded.
		OK bool

		// Is this the last block of a frame?
		Last bool

		// Is the data compressed?
		// If true CompressedSize will be populated.
		// Unfortunately DecompressedSize cannot be determined
		// without decoding the blocks.
		Compressed bool

		// DecompressedSize is the expected decompressed size of the block.
		// Will be 0 if it cannot be determined.
		DecompressedSize int

		// CompressedSize of the data in the block.
		// Does not include the block header.
		// Will be equal to DecompressedSize if not Compressed.
		CompressedSize int
	}

	// If set there is a checksum present for the block content.
	// The checksum field at the end is always 4 bytes long.
	HasCheckSum bool
}

// Decode the header from the beginning of the stream.
// This will decode the frame header and the first block header if enough bytes are provided.
// It is recommended to provide at least HeaderMaxSize bytes.
// If the frame header cannot be read an error will be returned.
// If there isn't enough input, io.ErrUnexpectedEOF is returned.
// The FirstBlock.OK will indicate if enough information was available to decode the first block header.
func (h *Header) Decode(in []byte) error {
	_, err := h.DecodeAndStrip(in)
	return err
}

// DecodeAndStrip will decode the header from the beginning of the stream
// and on success return the remaining bytes.
// This will decode the frame header and the first block header if enough bytes are provided.
// It is recommended to provide at least HeaderMaxSize bytes.
// If the frame header cannot be read an error will be returned.
// If there isn't enough input, io.ErrUnexpectedEOF is returned.
// The FirstBlock.OK will indicate if enough information was available to decode the first block header.
func (h *Header) DecodeAndStrip(in []byte) (remain []byte, err error) {
	var p HeaderState
	p.Begin(h, in, true)
	_, remain, err = p.Step(nil)
	return remain, err
}

// AppendTo will append the encoded header to the dst slice.
// There is no error checking performed on the header values.
func (h *Header) AppendTo(dst []byte) ([]byte, error) {
	if h.Skippable {
		magic := [4]byte{0x50, 0x2a, 0x4d, 0x18}
		magic[0] |= byte(h.SkippableID & 0xf)
		dst = append(dst, magic[:]...)
		f := h.SkippableSize
		return append(dst, uint8(f), uint8(f>>8), uint8(f>>16), uint8(f>>24)), nil
	}
	f := frameHeader{
		ContentSize:   h.FrameContentSize,
		WindowSize:    uint32(h.WindowSize),
		SingleSegment: h.SingleSegment,
		Checksum:      h.HasCheckSum,
		DictID:        h.DictionaryID,
	}
	return f.appendTo(dst), nil
}
