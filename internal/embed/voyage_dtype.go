// SPDX-License-Identifier: Apache-2.0

package embed

import "fmt"

// This file holds the Voyage arm's DTYPE SEAM: the two vocabularies the arm
// speaks and the translation between them. It is split out of voyage.go rather
// than living beside callVoyageBatch only because that file reached the repo's
// per-file length gate; the seam is one subject and is kept in one place.

// voyageDtypeUbinary and voyageDtypeFloat32 are the two representations this
// arm can decode, spelled as the RESOLVED CONFIG spells them — the same strings
// config.AcceptedEmbedDtypes carries. They are CAPABILITY STATEMENTS and are
// deliberately NOT the values that go on the wire; voyageWireDtype below
// translates them.
//
// THEY ARE THE CONFIG'S VOCABULARY, NOT A CLAIM ABOUT VOYAGE'S, and the two
// sets are now known to differ. Sending the config spelling straight through as
// output_dtype is what the arm used to do, and the provider refused it: the
// verbatim 400 is recorded in testdata/voyage_float32_wire_verification.txt.
// This arm's job is to decode the answer according to the representation the
// request asked for, so the decode consults these config spellings while the
// request carries Voyage's.
const (
	voyageDtypeUbinary = "ubinary"
	voyageDtypeFloat32 = "float32"
)

// voyageWireDtypeUbinary and voyageWireDtypeFloat are what this arm puts in the
// request's output_dtype field. They are VOYAGE'S OWN VOCABULARY, a different
// set from the config one above, and BOTH ARE OBSERVED VALUES rather than
// spellings reasoned into existence: the provider's rejection of the config
// spelling enumerated its accepted set for this model as
// ['binary', 'float', 'int8', 'ubinary', 'uint8'], recorded verbatim in
// testdata/voyage_float32_wire_verification.txt. The two sets happen to
// coincide at ubinary and to disagree at the unquantized representation, which
// is exactly why one string cannot do both jobs — the same separation
// openaicompat.go keeps between openAICompatDtypeFloat32 and
// openAICompatWireEncodingFormat.
const (
	voyageWireDtypeUbinary = "ubinary"
	voyageWireDtypeFloat   = "float"
)

// voyageWireDtype translates a resolved CONFIG dtype into the output_dtype
// spelling Voyage accepts.
//
// AN UNRECOGNIZED DTYPE IS REFUSED, NEVER PASSED THROUGH. Passing it through is
// precisely the defect this function exists to remove: it is how the literal
// "float32" reached the wire and drew a 400, and a pass-through default would
// keep doing that for every value outside the two named here. A translation
// that quietly forwards what it does not understand is a cover for a
// misconfiguration, so this refuses and names both sides — the value it could
// not translate and the wire spellings it can produce — following the same
// shape as openAICompatDtypeRefusal.
func voyageWireDtype(dtype string) (string, error) {
	switch dtype {
	case voyageDtypeUbinary:
		return voyageWireDtypeUbinary, nil
	case voyageDtypeFloat32:
		return voyageWireDtypeFloat, nil
	default:
		return "", fmt.Errorf(
			"%w: the voyage arm has no observed output_dtype spelling for dtype %q; it can ask for "+
				"%q (config dtype %q) or %q (config dtype %q), and sending an untranslated value is "+
				"what the provider rejected",
			ErrInvalidConfig, dtype,
			voyageWireDtypeUbinary, voyageDtypeUbinary,
			voyageWireDtypeFloat, voyageDtypeFloat32)
	}
}
