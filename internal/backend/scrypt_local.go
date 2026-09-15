package backend

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math/bits"
)

func scryptKey(password, salt []byte, N, r, p, keyLen int) ([]byte, error) {
	if N <= 1 || N&(N-1) != 0 {
		return nil, errors.New("scrypt: N must be > 1 and a power of 2")
	}
	if r <= 0 || p <= 0 || keyLen <= 0 {
		return nil, errors.New("scrypt: invalid parameters")
	}

	blockLen := 128 * r
	B := pbkdf2SHA256(password, salt, 1, p*blockLen)
	for i := 0; i < p; i++ {
		smix(B[i*blockLen:(i+1)*blockLen], r, N)
	}
	return pbkdf2SHA256(password, B, 1, keyLen), nil
}

func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	const hLen = 32
	numBlocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, numBlocks*hLen)
	var ctr [4]byte
	for block := 1; block <= numBlocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		binary.BigEndian.PutUint32(ctr[:], uint32(block))
		mac.Write(ctr[:])
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iter; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

func smix(block []byte, r, N int) {
	x := bytesToWords(block)
	v := make([]uint32, len(x)*N)
	for i := 0; i < N; i++ {
		copy(v[i*len(x):(i+1)*len(x)], x)
		blockMix(x, r)
	}
	for i := 0; i < N; i++ {
		j := int(integerify(x, r) & uint64(N-1))
		base := j * len(x)
		for k := range x {
			x[k] ^= v[base+k]
		}
		blockMix(x, r)
	}
	wordsToBytes(x, block)
}

func bytesToWords(in []byte) []uint32 {
	out := make([]uint32, len(in)/4)
	for i := range out {
		out[i] = binary.LittleEndian.Uint32(in[i*4:])
	}
	return out
}

func wordsToBytes(in []uint32, out []byte) {
	for i, word := range in {
		binary.LittleEndian.PutUint32(out[i*4:], word)
	}
}

func integerify(x []uint32, r int) uint64 {
	index := (2*r - 1) * 16
	return uint64(x[index]) | uint64(x[index+1])<<32
}

func blockMix(b []uint32, r int) {
	x := make([]uint32, 16)
	copy(x, b[(2*r-1)*16:2*r*16])
	y := make([]uint32, len(b))
	tmp := make([]uint32, 16)
	for i := 0; i < 2*r; i++ {
		start := i * 16
		for j := 0; j < 16; j++ {
			tmp[j] = x[j] ^ b[start+j]
		}
		salsa208(tmp)
		copy(x, tmp)
		copy(y[start:start+16], x)
	}
	for i := 0; i < r; i++ {
		copy(b[i*16:(i+1)*16], y[(i*2)*16:(i*2+1)*16])
		copy(b[(i+r)*16:(i+r+1)*16], y[(i*2+1)*16:(i*2+2)*16])
	}
}

func salsa208(inout []uint32) {
	x := make([]uint32, 16)
	copy(x, inout)
	for i := 0; i < 4; i++ {
		x[4] ^= bits.RotateLeft32(x[0]+x[12], 7)
		x[8] ^= bits.RotateLeft32(x[4]+x[0], 9)
		x[12] ^= bits.RotateLeft32(x[8]+x[4], 13)
		x[0] ^= bits.RotateLeft32(x[12]+x[8], 18)

		x[9] ^= bits.RotateLeft32(x[5]+x[1], 7)
		x[13] ^= bits.RotateLeft32(x[9]+x[5], 9)
		x[1] ^= bits.RotateLeft32(x[13]+x[9], 13)
		x[5] ^= bits.RotateLeft32(x[1]+x[13], 18)

		x[14] ^= bits.RotateLeft32(x[10]+x[6], 7)
		x[2] ^= bits.RotateLeft32(x[14]+x[10], 9)
		x[6] ^= bits.RotateLeft32(x[2]+x[14], 13)
		x[10] ^= bits.RotateLeft32(x[6]+x[2], 18)

		x[3] ^= bits.RotateLeft32(x[15]+x[11], 7)
		x[7] ^= bits.RotateLeft32(x[3]+x[15], 9)
		x[11] ^= bits.RotateLeft32(x[7]+x[3], 13)
		x[15] ^= bits.RotateLeft32(x[11]+x[7], 18)

		x[1] ^= bits.RotateLeft32(x[0]+x[3], 7)
		x[2] ^= bits.RotateLeft32(x[1]+x[0], 9)
		x[3] ^= bits.RotateLeft32(x[2]+x[1], 13)
		x[0] ^= bits.RotateLeft32(x[3]+x[2], 18)

		x[6] ^= bits.RotateLeft32(x[5]+x[4], 7)
		x[7] ^= bits.RotateLeft32(x[6]+x[5], 9)
		x[4] ^= bits.RotateLeft32(x[7]+x[6], 13)
		x[5] ^= bits.RotateLeft32(x[4]+x[7], 18)

		x[11] ^= bits.RotateLeft32(x[10]+x[9], 7)
		x[8] ^= bits.RotateLeft32(x[11]+x[10], 9)
		x[9] ^= bits.RotateLeft32(x[8]+x[11], 13)
		x[10] ^= bits.RotateLeft32(x[9]+x[8], 18)

		x[12] ^= bits.RotateLeft32(x[15]+x[14], 7)
		x[13] ^= bits.RotateLeft32(x[12]+x[15], 9)
		x[14] ^= bits.RotateLeft32(x[13]+x[12], 13)
		x[15] ^= bits.RotateLeft32(x[14]+x[13], 18)
	}
	for i := range inout {
		inout[i] += x[i]
	}
}
