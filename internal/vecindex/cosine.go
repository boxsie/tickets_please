package vecindex

import "math"

// cosine returns the cosine similarity between a and b.
//
// The two vectors must be the same length; mismatched lengths return 0.
// If either vector has zero magnitude, the result is 0 (rather than NaN) so
// pathological/empty entries don't poison the top-k heap.
//
// Callers are expected to pass already-normalized vectors (Ollama and OpenAI
// both return normalized embeddings); this function does not assume that and
// computes the full cosine in case a caller forgets — the cost is one extra
// sqrt per call which is negligible next to the dot product.
func cosine(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		x := float64(a[i])
		y := float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	denom := math.Sqrt(na) * math.Sqrt(nb)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}
