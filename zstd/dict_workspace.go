package zstd

import "bytes"

// BuildDictWorkspace reuses encoder scratch across repeated BuildDict calls.
//
// A workspace is not safe for concurrent use. Callers should keep it scoped to
// a single dictionary training loop.
type BuildDictWorkspace struct {
	enc        encoder
	encKey     dictWorkspaceEncoderKey
	dictBytes  []byte
	dictLoaded bool
}

// DictEncodeWorkspace reuses encoder scratch across repeated dictionary-backed
// EncodeAll calls.
//
// A workspace is not safe for concurrent use. Callers should keep it scoped to
// a single dictionary evaluation loop.
type DictEncodeWorkspace struct {
	enc        encoder
	encKey     dictWorkspaceEncoderKey
	dict       *dict
	dictBytes  []byte
	dictLoaded bool
}

type dictWorkspaceEncoderKey struct {
	level         EncoderLevel
	blockSize     int
	windowSize    int
	lowMem        bool
	noEntropy     bool
	allLitEntropy bool
}

func encoderKeyForOptions(o encoderOptions) dictWorkspaceEncoderKey {
	return dictWorkspaceEncoderKey{
		level:         o.level,
		blockSize:     o.blockSize,
		windowSize:    o.windowSize,
		lowMem:        o.lowMem,
		noEntropy:     o.noEntropy,
		allLitEntropy: o.allLitEntropy,
	}
}

func (w *BuildDictWorkspace) encoderFor(o encoderOptions, dictBytes []byte) encoder {
	key := encoderKeyForOptions(o)
	if w.enc == nil || w.encKey != key {
		w.enc = o.encoder()
		w.encKey = key
		w.dictLoaded = false
	}
	if !w.dictLoaded || !bytes.Equal(w.dictBytes, dictBytes) {
		clearEncoderDictCache(w.enc)
		w.dictBytes = append(w.dictBytes[:0], dictBytes...)
		w.dictLoaded = true
	}
	return w.enc
}

// EncodeAllWithDict encodes src using dictBytes and appends the result to dst.
// It matches NewWriter(nil, WithEncoderLevel(level), WithEncoderCRC(false),
// WithEncoderConcurrency(1), WithEncoderDict(dictBytes)) followed by EncodeAll.
func (w *DictEncodeWorkspace) EncodeAllWithDict(src, dst, dictBytes []byte, level EncoderLevel) ([]byte, error) {
	if w == nil {
		enc, err := NewWriter(nil,
			WithEncoderLevel(level),
			WithEncoderCRC(false),
			WithEncoderConcurrency(1),
			WithEncoderDict(dictBytes),
		)
		if err != nil {
			return nil, err
		}
		defer enc.Close()
		return enc.EncodeAll(src, dst), nil
	}

	initPredefined()
	o, err := dictWorkspaceEncodeOptions(level)
	if err != nil {
		return nil, err
	}

	d, changed, err := w.dictFor(dictBytes)
	if err != nil {
		return nil, err
	}
	o.dict = d

	key := encoderKeyForOptions(o)
	if w.enc == nil || w.encKey != key {
		w.enc = o.encoder()
		w.encKey = key
		changed = false
	}
	if changed {
		clearEncoderDictCache(w.enc)
	}

	var enc Encoder
	enc.o = o
	return enc.encodeAll(w.enc, src, dst), nil
}

func (w *DictEncodeWorkspace) dictFor(dictBytes []byte) (*dict, bool, error) {
	if w.dictLoaded && bytes.Equal(w.dictBytes, dictBytes) {
		return w.dict, false, nil
	}
	d, err := loadDict(dictBytes)
	if err != nil {
		return nil, false, err
	}
	w.dict = d
	w.dictBytes = append(w.dictBytes[:0], dictBytes...)
	w.dictLoaded = true
	return d, true, nil
}

func dictWorkspaceEncodeOptions(level EncoderLevel) (encoderOptions, error) {
	var o encoderOptions
	o.setDefault()
	if err := WithEncoderLevel(level)(&o); err != nil {
		return encoderOptions{}, err
	}
	o.concurrent = 1
	o.crc = false
	return o, nil
}

func clearEncoderDictCache(enc encoder) {
	switch e := enc.(type) {
	case *fastEncoderDict:
		clearFastBaseDictCache(&e.fastBase)
		e.dictTable = nil
		e.allDirty = true
	case *doubleFastEncoderDict:
		clearFastBaseDictCache(&e.fastBase)
		e.dictTable = nil
		e.dictLongTable = nil
		e.allDirty = true
	case *betterFastEncoderDict:
		clearFastBaseDictCache(&e.fastBase)
		e.dictTable = nil
		e.dictLongTable = nil
		e.allDirty = true
	case *bestFastEncoder:
		clearFastBaseDictCache(&e.fastBase)
		e.dictTable = nil
		e.dictLongTable = nil
	}
}

func clearFastBaseDictCache(e *fastBase) {
	e.lastDictID = 0
	e.histHasDict = false
	e.histDictID = 0
	e.histDictLen = 0
}
