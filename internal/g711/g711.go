// Package g711 implements the ITU-T G.711 companding laws.
//
// Both sides of a SIM-to-SIP bridge need them: the IMS leg because carriers
// negotiate PCMA or PCMU, and the trunk leg because that is what every PBX
// speaks without a codec module. The conversion is a fixed standard, so it
// lives in one place rather than being written twice.
package g711

// LinearToMuLaw encodes one 16-bit sample as G.711 mu-law.
func LinearToMuLaw(sample int16) byte {
	value := int(sample)
	sign := byte(0)
	if value < 0 {
		sign, value = 0x80, -value
		if value > 32767 {
			value = 32767
		}
	}
	value += 132
	if value > 32635 {
		value = 32635
	}
	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; mask >>= 1 {
		exponent--
	}
	mantissa := (value >> (exponent + 3)) & 0x0f
	return ^(sign | byte(exponent<<4) | byte(mantissa))
}

// MuLawToLinear decodes one G.711 mu-law byte to a 16-bit sample.
func MuLawToLinear(value byte) int16 {
	value = ^value
	magnitude := ((int(value)&0x0f)<<3 + 132) << ((value & 0x70) >> 4)
	magnitude -= 132
	if value&0x80 != 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}

// LinearToALaw encodes one 16-bit sample as G.711 A-law.
func LinearToALaw(sample int16) byte {
	value := int(sample)
	mask := byte(0xd5)
	if value < 0 {
		mask, value = 0x55, -value-1
	}
	if value > 32767 {
		value = 32767
	}
	var encoded byte
	if value < 256 {
		encoded = byte(value >> 4)
	} else {
		exponent := 1
		for threshold := 512; exponent < 7 && value >= threshold; threshold <<= 1 {
			exponent++
		}
		encoded = byte(exponent<<4) | byte((value>>(exponent+3))&0x0f)
	}
	return encoded ^ mask
}

// ALawToLinear decodes one G.711 A-law byte to a 16-bit sample.
func ALawToLinear(value byte) int16 {
	value ^= 0x55
	magnitude := int(value&0x0f)<<4 + 8
	exponent := int((value & 0x70) >> 4)
	if exponent != 0 {
		magnitude = (magnitude + 0x100) << (exponent - 1)
	}
	if value&0x80 == 0 {
		return int16(-magnitude)
	}
	return int16(magnitude)
}
