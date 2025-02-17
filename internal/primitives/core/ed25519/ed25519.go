// Copyright 2024 ChainSafe Systems (ON)
// SPDX-License-Identifier: LGPL-3.0-only

package ed25519

import (
	gocrypto "crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ChainSafe/go-schnorrkel"
	"github.com/ChainSafe/gossamer/internal/primitives/core/crypto"
	"github.com/ChainSafe/gossamer/internal/primitives/core/hashing"
	"github.com/ChainSafe/gossamer/pkg/scale"
	"github.com/tyler-smith/go-bip39"
)

// A secret seed.
type seed [32]byte

// A Public key.
type Public [32]byte

// Bytes returns a byte slice
func (p Public) Bytes() []byte {
	return p[:]
}

// Verify a signature on a message. Returns true if the signature is good.
func (p Public) Verify(sig Signature, message []byte) bool {
	return ed25519.Verify(p[:], message, sig[:])
}

// NewPublic creates a new instance from the given 32-byte `data`.
//
// NOTE: No checking goes on to ensure this is a real public key. Only use it if
// you are certain that the array actually is a pubkey.
func NewPublic(data [32]byte) Public {
	return Public(data)
}

var _ crypto.Public[Signature] = Public{}

// Derive a single hard junction.
func deriveHardJunction(secretSeed seed, cc [32]byte) seed {
	tuple := struct {
		ID         string
		SecretSeed seed
		CC         [32]byte
	}{"Ed25519HDKD", secretSeed, cc}
	encoded := scale.MustMarshal(tuple)
	return hashing.BlakeTwo256(encoded)
}

// Pair is a key pair.
type Pair struct {
	public gocrypto.PublicKey
	secret ed25519.PrivateKey
}

// Derive a child key from a series of given junctions.
func (p Pair) Derive(path []crypto.DeriveJunction, seed *[32]byte) (crypto.Pair[[32]byte, Signature], [32]byte, error) {
	var acc [32]byte
	copy(acc[:], p.secret.Seed())
	for _, j := range path {
		switch cc := j.Value().(type) {
		case crypto.DeriveJunctionSoft:
			return Pair{}, [32]byte{}, fmt.Errorf("soft key in path")
		case crypto.DeriveJunctionHard:
			acc = deriveHardJunction(acc, cc)
		}
	}
	pair := NewPairFromSeed(acc)
	return pair, acc, nil
}

// Seed is the seed for this key.
func (p Pair) Seed() [32]byte {
	var seed [32]byte
	copy(seed[:], p.secret.Seed())
	return seed
}

// Public will return the public key.
func (p Pair) Public() crypto.Public[Signature] {
	pubKey, ok := p.public.(ed25519.PublicKey)
	if !ok {
		panic("huh?")
	}
	if len(pubKey) != 32 {
		panic("huh?")
	}
	var pub Public
	copy(pub[:], pubKey)
	return pub
}

// Sign a message.
func (p Pair) Sign(message []byte) Signature {
	signed := ed25519.Sign(p.secret, message)
	if len(signed) != 64 {
		panic("huh?")
	}
	var sig Signature
	copy(sig[:], signed)
	return sig
}

// NewGeneratedPair will generate new secure (random) key pair.
//
// This is only for ephemeral keys really, since you won't have access to the secret key
// for storage. If you want a persistent key pair, use `generate_with_phrase` instead.
func NewGeneratedPair() (Pair, [32]byte) {
	seedSlice := make([]byte, 32)
	_, err := rand.Read(seedSlice)
	if err != nil {
		panic(err)
	}

	var seed [32]byte
	copy(seed[:], seedSlice)
	return NewPairFromSeed(seed), seed
}

// NewGeneratedPairWithPhrase will generate new secure (random) key pair and provide the recovery phrase.
//
// You can recover the same key later with `from_phrase`.
//
// This is generally slower than `generate()`, so prefer that unless you need to persist
// the key from the current session.
func NewGeneratedPairWithPhrase(password *string) (Pair, string, [32]byte) {
	entropy, err := bip39.NewEntropy(128)
	if err != nil {
		panic(err)
	}
	phrase, err := bip39.NewMnemonic(entropy)
	if err != nil {
		panic(err)
	}
	pair, seed, err := NewPairFromPhrase(phrase, password)
	if err != nil {
		panic(err)
	}
	return pair, phrase, seed
}

// NewPairFromPhrase returns the KeyPair from the English BIP39 seed `phrase`, or `None` if it's invalid.
func NewPairFromPhrase(phrase string, password *string) (pair Pair, seed [32]byte, err error) {
	pass := ""
	if password != nil {
		pass = *password
	}
	bigSeed, err := schnorrkel.SeedFromMnemonic(phrase, pass)
	if err != nil {
		return Pair{}, [32]byte{}, err
	}

	if !(32 <= len(bigSeed)) {
		panic("huh?")
	}

	seedSlice := bigSeed[:][0:32]
	copy(seed[:], seedSlice)
	return NewPairFromSeedSlice(seedSlice), seed, nil
}

// NewPairFromSeed will generate new key pair from the provided `seed`.
//
// @WARNING: THIS WILL ONLY BE SECURE IF THE `seed` IS SECURE. If it can be guessed
// by an attacker then they can also derive your key.
func NewPairFromSeed(seed [32]byte) Pair {
	return NewPairFromSeedSlice(seed[:])
}

// NewPairFromSeedSlice will make a new key pair from secret seed material. The slice must be the correct size or
// it will return `None`.
//
// @WARNING: THIS WILL ONLY BE SECURE IF THE `seed` IS SECURE. If it can be guessed
// by an attacker then they can also derive your key.
func NewPairFromSeedSlice(seedSlice []byte) Pair {
	secret := ed25519.NewKeyFromSeed(seedSlice)
	public := secret.Public()
	return Pair{
		public: public,
		secret: secret,
	}
}

// NewPairFromStringWithSeed interprets the string `s` in order to generate a key Pair. Returns
// both the pair and an optional seed, in the case that the pair can be expressed as a direct
// derivation from a seed (some cases, such as Sr25519 derivations with path components, cannot).
//
// This takes a helper function to do the key generation from a phrase, password and
// junction iterator.
//
// - If `s` is a possibly `0x` prefixed 64-digit hex string, then it will be interpreted
// directly as a secret key (aka "seed" in `subkey`).
// - If `s` is a valid BIP-39 key phrase of 12, 15, 18, 21 or 24 words, then the key will
// be derived from it. In this case:
//   - the phrase may be followed by one or more items delimited by `/` characters.
//   - the path may be followed by `///`, in which case everything after the `///` is treated
//
// as a password.
//   - If `s` begins with a `/` character it is prefixed with the Substrate public `DevPhrase`
//     and
//
// interpreted as above.
//
// In this case they are interpreted as HDKD junctions; purely numeric items are interpreted as
// integers, non-numeric items as strings. Junctions prefixed with `/` are interpreted as soft
// junctions, and with `//` as hard junctions.
//
// There is no correspondence mapping between SURI strings and the keys they represent.
// Two different non-identical strings can actually lead to the same secret being derived.
// Notably, integer junction indices may be legally prefixed with arbitrary number of zeros.
// Similarly an empty password (ending the SURI with `///`) is perfectly valid and will
// generally be equivalent to no password at all.
//
// `nil` is returned if no matches are found.
func NewPairFromStringWithSeed(s string, passwordOverride *string) (
	pair crypto.Pair[[32]byte, Signature], seed [32]byte, err error,
) {
	sURI, err := crypto.NewSecretURI(s)
	if err != nil {
		return Pair{}, [32]byte{}, err
	}
	var password *string
	if passwordOverride != nil {
		password = passwordOverride
	} else {
		password = sURI.Password
	}

	var (
		root Pair
		// seed []byte
	)
	trimmedPhrase := strings.TrimPrefix(sURI.Phrase, "0x")
	if trimmedPhrase != sURI.Phrase {
		seedBytes, err := hex.DecodeString(trimmedPhrase)
		if err != nil {
			return Pair{}, [32]byte{}, err
		}
		root = NewPairFromSeedSlice(seedBytes)
		copy(seed[:], seedBytes)
	} else {
		root, seed, err = NewPairFromPhrase(sURI.Phrase, password)
		if err != nil {
			return Pair{}, [32]byte{}, err
		}
	}
	return root.Derive(sURI.Junctions, &seed)
}

// NewPairFromString interprets the string `s` in order to generate a key pair.
func NewPairFromString(s string, passwordOverride *string) (crypto.Pair[[32]byte, Signature], error) {
	pair, _, err := NewPairFromStringWithSeed(s, passwordOverride)
	return pair, err
}

var _ crypto.Pair[[32]byte, Signature] = Pair{}

// Signature is a signature (a 512-bit value).
type Signature [64]byte

// NewSignatureFromRaw constructors a new instance from the given 64-byte `data`.
//
// NOTE: No checking goes on to ensure this is a real signature. Only use it if
// you are certain that the array actually is a signature.
func NewSignatureFromRaw(data [64]byte) Signature {
	return Signature(data)
}
