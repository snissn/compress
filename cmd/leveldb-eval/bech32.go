package main

import "strings"

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32CharsetRev = func() [128]byte {
	var rev [128]byte
	for i := range rev {
		rev[i] = 0xFF
	}
	for i := 0; i < len(bech32Charset); i++ {
		rev[bech32Charset[i]] = byte(i)
	}
	return rev
}()

func decodeBech32Address(addr string) ([]byte, bool) {
	if len(addr) < 8 {
		return nil, false
	}
	lower := strings.ToLower(addr)
	upper := strings.ToUpper(addr)
	if addr != lower && addr != upper {
		return nil, false
	}
	if addr == upper {
		addr = lower
	}

	pos := strings.LastIndexByte(addr, '1')
	if pos < 1 || pos+7 > len(addr) {
		return nil, false
	}
	hrp := addr[:pos]
	dataPart := addr[pos+1:]
	data := make([]byte, len(dataPart))
	for i := 0; i < len(dataPart); i++ {
		c := dataPart[i]
		if c >= 128 {
			return nil, false
		}
		v := bech32CharsetRev[c]
		if v == 0xFF {
			return nil, false
		}
		data[i] = v
	}

	if !verifyBech32Checksum(hrp, data) {
		return nil, false
	}
	payload := data[:len(data)-6]
	decoded, ok := convertBits(payload, 5, 8, false)
	if !ok {
		return nil, false
	}
	if len(decoded) != 20 {
		return nil, false
	}
	return decoded, true
}

func verifyBech32Checksum(hrp string, data []byte) bool {
	values := append(bech32HrpExpand(hrp), data...)
	return bech32Polymod(values) == 1
}

func bech32HrpExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

func bech32Polymod(values []byte) uint32 {
	const (
		gen0 = 0x3b6a57b2
		gen1 = 0x26508e6d
		gen2 = 0x1ea119fa
		gen3 = 0x3d4233dd
		gen4 = 0x2a1462b3
	)
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		if (top & 1) != 0 {
			chk ^= gen0
		}
		if (top & 2) != 0 {
			chk ^= gen1
		}
		if (top & 4) != 0 {
			chk ^= gen2
		}
		if (top & 8) != 0 {
			chk ^= gen3
		}
		if (top & 16) != 0 {
			chk ^= gen4
		}
	}
	return chk
}

func convertBits(data []byte, fromBits, toBits uint, pad bool) ([]byte, bool) {
	var acc uint
	var bits uint
	maxv := uint((1 << toBits) - 1)
	ret := make([]byte, 0, len(data)*int(fromBits)/int(toBits))
	for _, value := range data {
		if value>>fromBits != 0 {
			return nil, false
		}
		acc = (acc << fromBits) | uint(value)
		bits += fromBits
		for bits >= toBits {
			bits -= toBits
			ret = append(ret, byte((acc>>bits)&maxv))
		}
	}
	if pad {
		if bits > 0 {
			ret = append(ret, byte((acc<<(toBits-bits))&maxv))
		}
	} else if bits >= fromBits || ((acc<<(toBits-bits))&maxv) != 0 {
		return nil, false
	}
	return ret, true
}
