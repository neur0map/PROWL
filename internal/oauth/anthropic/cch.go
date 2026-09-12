package anthropic

import (
	"bytes"

	"github.com/cespare/xxhash/v2"
)

const (
	cchSeed            = 0x4d659218e32a3268
	cchPlaceholderText = "cch=00000"
)

var (
	cchPlaceholder = []byte(cchPlaceholderText)
	// Only the first system block is attested. Identical text in user content
	// or in the caller's later system blocks must remain untouched.
	billingSystemMarker = []byte(`"system":[{"type":"text","text":"` + claudeBillingHeaderPrefix)
)

// patchCch stamps the low 20 bits of the seeded XXH64 of the final serialized
// request, with the billing placeholder still zeroed, as in the OMP reference.
// A missing anchor fails closed rather than sending an unattested request.
func patchCch(body []byte) bool {
	marker := bytes.Index(body, billingSystemMarker)
	if marker < 0 {
		return false
	}
	start := marker + len(billingSystemMarker)
	end := bytes.IndexByte(body[start:], '"')
	if end < 0 {
		return false
	}
	rel := bytes.Index(body[start:start+end], cchPlaceholder)
	if rel < 0 {
		return false
	}
	digest := xxhash.NewWithSeed(cchSeed)
	_, _ = digest.Write(body)
	hash := digest.Sum64()
	const hex = "0123456789abcdef"
	for i := 4; i >= 0; i-- {
		body[start+rel+4+i] = hex[hash&0xf]
		hash >>= 4
	}
	return true
}
