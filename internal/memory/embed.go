package memory

import (
	"encoding/binary"
	"math"
	"strings"
	"unicode"
)

// embedDim is the dimension of the hashed subword embeddings. At 256
// float32s a stored vector is one kilobyte per memory, and feature
// collisions stay rare for vocabularies of this corpus's size.
const embedDim = 256

// gramWeight scales character n-gram features relative to whole-word
// features: subword evidence supports a match but should not outrank
// an actual word match.
const gramWeight = 0.35

// maxTokenLen bounds token length so pathological tokens (URLs, base64
// blobs) cannot dominate an embedding with thousands of n-grams.
const maxTokenLen = 32

// Embed maps text to a unit vector in a fixed-dimension feature
// space. Every word contributes its token, and every character
// 3..5-gram of the word contributes subword features; features are
// hashed into buckets with a sign so collisions partially cancel. The
// subword grams give the vectors morphology tolerance ("running" ends
// up near "runs") without a model file, a download, or cgo. The
// function is deterministic across processes, which is what lets the
// vectors be stored in the database next to the text they describe.
func Embed(text string) []float32 {
	vec := make([]float32, embedDim)
	for _, token := range tokenize(text) {
		addFeature(vec, "w"+token, 1)
		padded := "<" + token + ">"
		for n := 3; n <= 5; n++ {
			if n > len(padded) {
				break
			}
			for i := 0; i+n <= len(padded); i++ {
				addFeature(vec, "g"+padded[i:i+n], gramWeight)
			}
		}
	}
	normalize(vec)
	return vec
}

// addFeature hashes one feature string into a bucket, adding signed
// weight: the hash's top bit picks the sign so bucket collisions
// dilute each other instead of accumulating.
func addFeature(vec []float32, feature string, weight float64) {
	h := fnv32a(feature)
	idx := int(h % uint32(len(vec)))
	sign := 1.0
	if h&(1<<31) != 0 {
		sign = -1.0
	}
	vec[idx] += float32(sign * weight)
}

// cosine returns the cosine similarity of two unit vectors, which is
// their dot product. Vectors that came out all-zero (empty text)
// yield zero similarity against anything.
func cosine(a, b []float32) float64 {
	if len(a) != embedDim || len(b) != embedDim {
		return 0
	}
	sum := 0.0
	for i := range a {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}

// normalize scales the vector to unit length in place.
func normalize(vec []float32) {
	sum := 0.0
	for _, v := range vec {
		sum += float64(v) * float64(v)
	}
	if sum == 0 {
		return
	}
	inv := 1 / math.Sqrt(sum)
	for i := range vec {
		vec[i] *= float32(inv)
	}
}

// tokenize lowercases text and splits it into alphanumeric tokens,
// capped at maxTokenLen each.
func tokenize(text string) []string {
	tokens := make([]string, 0, 16)
	var b []rune
	flush := func() {
		if len(b) > 0 {
			tokens = append(tokens, string(b))
			b = b[:0]
		}
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(b) < maxTokenLen {
				b = append(b, r)
			}
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// fnv32a is the 32-bit FNV-1a hash over a string's bytes, without the
// allocation of hashing via []byte conversion.
func fnv32a(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// encodeEmbedding packs a vector for storage as a BLOB.
func encodeEmbedding(vec []float32) []byte {
	buf := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[4*i:], math.Float32bits(v))
	}
	return buf
}

// decodeEmbedding unpacks a stored BLOB back into a vector, returning
// nil when the bytes do not have the current expected shape so stale
// or foreign vectors are recomputed instead of trusted.
func decodeEmbedding(blob []byte) []float32 {
	if len(blob) != 4*embedDim {
		return nil
	}
	vec := make([]float32, embedDim)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[4*i:]))
	}
	return vec
}
