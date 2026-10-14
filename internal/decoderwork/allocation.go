// Copyright 2026. Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package decoderwork

import (
	"math"
	"unsafe"
)

// AllocationCharge is the existing gomap COW conservative allocator policy,
// retainedalloc/owner.go at gomap 075ae77487778a872384a935c6d550359ca76452.
// Round each actual allocation separately before summing. This admission is
// distinct from requested/addressable bytes and observed allocator statistics.
// The small-object allowance covers pointerful allocation headers; applying it
// to all backings is conservative. Overflow fails closed, never wrapping.
func AllocationCharge(n uint64) uint64 {
	if n == 0 {
		return 0
	}
	if n > math.MaxInt64-8192-16 {
		return math.MaxUint64
	}
	ptr := uint64(unsafe.Sizeof(uintptr(0)))
	if n > ptr*ptr*8 && n < 32768 {
		n += 16
	}
	if n <= 32768 {
		c := uint64(16)
		for c < n {
			c *= 2
		}
		return c
	}
	return (n + 8191) &^ uint64(8191)
}
