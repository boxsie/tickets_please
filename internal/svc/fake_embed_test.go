package svc

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
)

// fakeEmbed is the deterministic embed.Provider used in unit tests. It maps a
// text to a 768-float vector by hashing the text with sha256, expanding the
// hash to 96 bytes (24 floats × 4 bytes) repeated 32 times in 24-float
// chunks, then L2-normalizing — same shape ollama returns. Determinism makes
// search results reproducible across test runs.
type fakeEmbed struct{}

func newFakeEmbed() *fakeEmbed { return &fakeEmbed{} }

func (f *fakeEmbed) Name() string                  { return "fake" }
func (f *fakeEmbed) Dim() int                      { return 768 }
func (f *fakeEmbed) Probe(_ context.Context) error { return nil }

func (f *fakeEmbed) Embed(_ context.Context, text string) ([]float32, error) {
	const dim = 768
	out := make([]float32, dim)
	// Seed with sha256(text). Each iteration appends i and re-hashes to
	// produce the next 8 floats (32 bytes / 4 bytes per float). 96 iterations
	// give us 768 floats.
	var seed [32]byte = sha256.Sum256([]byte(text))
	for i := 0; i < dim/8; i++ {
		var nonce [4]byte
		binary.BigEndian.PutUint32(nonce[:], uint32(i))
		h := sha256.New()
		h.Write(seed[:])
		h.Write(nonce[:])
		block := h.Sum(nil)
		for j := 0; j < 8; j++ {
			u := binary.BigEndian.Uint32(block[j*4 : j*4+4])
			// Map uint32 → float in (-1, 1) so cosine works sensibly.
			out[i*8+j] = float32(int32(u))/float32(math.MaxInt32) - 0.0
		}
	}
	// L2 normalize so the vec matches what real providers return.
	var sum float64
	for _, v := range out {
		sum += float64(v) * float64(v)
	}
	if sum > 0 {
		inv := 1.0 / math.Sqrt(sum)
		for i, v := range out {
			out[i] = float32(float64(v) * inv)
		}
	}
	return out, nil
}
