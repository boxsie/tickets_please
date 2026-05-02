package worker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
)

// fakeProvider returns a deterministic 768-dim vector derived from sha256 of
// the input text. Used by every test in this package; no Ollama / OpenAI
// dependency, no flake.
type fakeProvider struct {
	dim   int
	calls int
}

func newFake() *fakeProvider { return &fakeProvider{dim: 768} }

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Dim() int     { return f.dim }

func (f *fakeProvider) Embed(_ context.Context, text string) ([]float32, error) {
	f.calls++
	dim := f.dim
	if dim <= 0 {
		dim = 768
	}
	out := make([]float32, dim)
	seed := sha256.Sum256([]byte(text))
	for i := 0; i < (dim+7)/8; i++ {
		var nonce [4]byte
		binary.BigEndian.PutUint32(nonce[:], uint32(i))
		h := sha256.New()
		h.Write(seed[:])
		h.Write(nonce[:])
		block := h.Sum(nil)
		for j := 0; j < 8 && i*8+j < dim; j++ {
			u := binary.BigEndian.Uint32(block[j*4 : j*4+4])
			out[i*8+j] = float32(int32(u)) / float32(math.MaxInt32)
		}
	}
	// L2 normalize.
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
