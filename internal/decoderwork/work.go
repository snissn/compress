// Copyright 2026. Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package decoderwork defines the caller-owned work and retention contract.
package decoderwork

// Work belongs to the enclosing operation. Reserve must atomically debit both
// operands before returning true. False must leave the operands unchanged.
// Retain admits actual containing capacity before allocation or borrowing.
// Release is called only after the final decoder alias has been cleared.
// None of these methods may perform uncharged preparation or wait for workers.
type Work interface {
	Reserve(records, bytes uint64) bool
	// Available returns the SAME enclosing operands; it performs no work.
	Available() (records, bytes uint64)
	Retain(bytes uint64) bool
	Release(bytes uint64)
}

func Charge(w Work, records, bytes uint64) bool {
	return w == nil || w.Reserve(records, bytes)
}
