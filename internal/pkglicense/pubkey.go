package pkglicense

import "crypto/ed25519"

// PublicKey is the Ed25519 public key used by Parse() (the no-key variant)
// to verify license signatures. Vendored verbatim from cnak/pkg/license so
// any .lic blobs signed by that key continue to verify here. Production
// call sites should prefer ParseWithKey with a per-deployment key sourced
// from the root_keys table instead of relying on this embedded constant.
var PublicKey ed25519.PublicKey = []byte{
	0x77, 0x1c, 0x72, 0xe4, 0xf6, 0xea, 0x35, 0x4a,
	0xa0, 0x20, 0x47, 0x28, 0x3c, 0xbd, 0x15, 0x10,
	0xbd, 0x9c, 0x43, 0xaa, 0x29, 0x31, 0xfa, 0x5c,
	0xcd, 0xa2, 0x3f, 0xd0, 0xe1, 0xfe, 0xf0, 0xb3,
}
