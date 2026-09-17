package backend

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// Known-answer tests for the hand-written scrypt in scrypt_local.go.
//
// The expected values are the official test vectors from RFC 7914 section 12,
// "Test Vectors for scrypt". They were transcribed from the reference copy of those
// vectors carried in the Go x/crypto/scrypt test suite, which reproduces the RFC
// values byte for byte:
//
//	https://raw.githubusercontent.com/golang/crypto/master/scrypt/scrypt_test.go
//
// (this checkout could not resolve www.rfc-editor.org, and every GitHub mirror of the
// RFC text that was reachable returned 404).
//
// Every vector below is the RFC's own, including password "pleaseletmein" / salt
// "SodiumChloride" with N=16384, r=8, p=1, and dkLen=64 for all three. To rule out a
// transcription error in the fetched copy, all three expected values were reproduced
// independently with a from-scratch scrypt written directly against the RFC's
// specification (PBKDF2-HMAC-SHA256, Salsa20/8, BlockMix, ROMix) in a separate
// language, and matched byte for byte.
//
// These matter because HashPassword and VerifyPassword share this implementation: a
// deviation from the specification would still verify its own hashes, so login would
// keep working while the real work factor silently differed from the parameters the
// code claims (auth.go uses N=16384, r=8, p=1, keyLen=64).
type scryptVector struct {
	name     string
	password string
	salt     string
	n, r, p  int
	want     string // hex, dkLen derived from its length
}

var scryptRFC7914Vectors = []scryptVector{
	{
		name:     "RFC 7914 vector 4: empty password and salt, N=16 r=1 p=1",
		password: "",
		salt:     "",
		n:        16, r: 1, p: 1,
		want: "77d6576238657b203b19ca42c18a0497f16b4844e3074ae8dfdffa3fede21442" +
			"fcd0069ded0948f8326a753a0fc81f17e8d3e0fb2e0d3628cf35e20c38d18906",
	},
	{
		name:     "RFC 7914 vector 2: password/NaCl, N=1024 r=8 p=16",
		password: "password",
		salt:     "NaCl",
		n:        1024, r: 8, p: 16,
		want: "fdbabe1c9d3472007856e7190d01e9fe7c6ad7cbc8237830e77376634b373162" +
			"2eaf30d92e22a3886ff109279d9830dac727afb94a83ee6d8360cbdfa2cc0640",
	},
	{
		name:     "RFC 7914 vector 3: pleaseletmein/SodiumChloride, N=16384 r=8 p=1",
		password: "pleaseletmein",
		salt:     "SodiumChloride",
		n:        16384, r: 8, p: 1,
		want: "7023bdcb3afd7348461c06cd81fd38ebfda8fbba904f8e3ea9b543f6545da1f2" +
			"d5432955613f0fcf62d49705242a9af9e61e85dc0d651e40dfcf017b45575887",
	},
}

func TestScryptKeyMatchesRFC7914Vectors(t *testing.T) {
	for _, vector := range scryptRFC7914Vectors {
		vector := vector
		t.Run(vector.name, func(t *testing.T) {
			want, err := hex.DecodeString(vector.want)
			if err != nil {
				t.Fatalf("bad expected value: %v", err)
			}
			got, err := scryptKey([]byte(vector.password), []byte(vector.salt), vector.n, vector.r, vector.p, len(want))
			if err != nil {
				t.Fatalf("scryptKey: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("scryptKey(N=%d, r=%d, p=%d) mismatch\n got  %s\n want %s",
					vector.n, vector.r, vector.p, hex.EncodeToString(got), vector.want)
			}
		})
	}
}

// The storage format is salt:hex with no version marker (auth.go), so the exact
// auth.go parameters must also be covered by a known answer, not just the RFC set.
// This is the RFC's N=16384 vector reached through the same call shape HashPassword
// uses, in case the parameter plumbing changes.
func TestScryptKeyAtAuthParametersMatchesRFC(t *testing.T) {
	const want = "7023bdcb3afd7348461c06cd81fd38ebfda8fbba904f8e3ea9b543f6545da1f2" +
		"d5432955613f0fcf62d49705242a9af9e61e85dc0d651e40dfcf017b45575887"
	got, err := scryptKey([]byte("pleaseletmein"), []byte("SodiumChloride"), 16384, 8, 1, 64)
	if err != nil {
		t.Fatalf("scryptKey: %v", err)
	}
	if hex.EncodeToString(got) != want {
		t.Errorf("scryptKey at auth.go parameters mismatch\n got  %s\n want %s", hex.EncodeToString(got), want)
	}
}

// A smaller independent check on PBKDF2-HMAC-SHA256, which scrypt uses twice. The
// RFC 7914 section 12 vectors above already exercise it end to end, but the first
// published PBKDF2-HMAC-SHA256 vector pins the primitive on its own.
func TestPBKDF2SHA256KnownAnswer(t *testing.T) {
	// RFC 7914 section 10 gives scrypt's PBKDF2 step. This is the standard
	// PBKDF2-HMAC-SHA256 KAT (P="password", S="salt", c=1, dkLen=32); it was checked
	// against an independent implementation rather than transcribed from memory.
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	const want = "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	if hex.EncodeToString(got) != want {
		t.Errorf("pbkdf2SHA256(iter=1) = %s, want %s", hex.EncodeToString(got), want)
	}
}

// Guard the parameter validation so the vectors above cannot silently pass on a
// degenerate input.
func TestScryptKeyRejectsInvalidParameters(t *testing.T) {
	if _, err := scryptKey([]byte("p"), []byte("s"), 1, 1, 1, 64); err == nil {
		t.Error("N=1 must be rejected")
	}
	if _, err := scryptKey([]byte("p"), []byte("s"), 7, 1, 1, 64); err == nil {
		t.Error("non-power-of-two N must be rejected")
	}
	if _, err := scryptKey([]byte("p"), []byte("s"), 16, 0, 1, 64); err == nil {
		t.Error("r=0 must be rejected")
	}
}

// The vectors must not be reachable by accident: a wrong password must not produce
// the expected key.
func TestScryptKeyVectorIsDiscriminating(t *testing.T) {
	wrong, err := scryptKey([]byte("pleaseletmein!"), []byte("SodiumChloride"), 16384, 8, 1, 64)
	if err != nil {
		t.Fatalf("scryptKey: %v", err)
	}
	if strings.HasPrefix(scryptRFC7914Vectors[2].want, hex.EncodeToString(wrong)) {
		t.Error("a mutated password reproduced the expected key")
	}
}
