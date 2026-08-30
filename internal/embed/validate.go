package embed

import (
	"fmt"
	"math"
)

// ValidateVector rejects a vector that is empty or carries a NaN/Inf
// component, returning a descriptive error naming the first bad index.
//
// This is a hard boundary, not a nicety. Cosine similarity against a vector
// holding NaN yields NaN for every comparison, and NaN loses every ordering
// test it appears in — so a single poisoned entry doesn't just fail to match,
// it can quietly distort the ranking of everything it's compared against. An
// entry that never made it into the index is a visible hole; an entry that
// made it in with NaN is a silent corruption of search itself. Prefer the
// hole.
//
// Real backends do produce these. Ollama serving bge-m3 returns HTTP 500
// ("json: unsupported value: NaN") for certain ordinary ASCII inputs, which
// surfaces as a transport error — but a provider that one day serialises NaN
// successfully would sail straight past every other check we have.
func ValidateVector(provider string, vec []float32) error {
	if len(vec) == 0 {
		return fmt.Errorf("%s: empty embedding vector", provider)
	}
	for i, v := range vec {
		f := float64(v)
		if math.IsNaN(f) {
			return fmt.Errorf("%s: embedding contains NaN at index %d of %d", provider, i, len(vec))
		}
		if math.IsInf(f, 0) {
			return fmt.Errorf("%s: embedding contains %+v at index %d of %d", provider, v, i, len(vec))
		}
	}
	return nil
}
