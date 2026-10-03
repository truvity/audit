package keys

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// KMSSigner signs seals with an AWS KMS asymmetric key.
//
// It is the answer to the local signer's stated limit: whoever can write the
// archive can also sign for what they wrote. Here the private key never leaves
// KMS, the writer's role has no kms:Sign on it, and only the notary's role
// does — so writing the archive and vouching for it are different privileges,
// held by different identities, with every signature in KMS's own log.
//
// The key is an ECC_NIST_P384 SIGN_VERIFY key, which is what ES384 is: a seal
// is signed ECDSA_SHA_384. The message is hashed here, with SHA-384, and KMS is
// given the digest (MessageType DIGEST): a JWS signing input is larger than the
// 4 KiB KMS takes whole once a seal carries its meters, and a digest is the
// same signature either way. Verify hashes the same way, so an auditor needs
// only the public half this exports. An ECC_NIST_P256 key still signs
// (ECDSA_SHA_256), for the callers that predate seals.
type KMSSigner struct {
	Client *kms.Client
	// KeyID is the key's ARN, ID or alias. It is also what the operator's
	// log names as the signer.
	Key string

	once   sync.Once
	spec   types.KeySpec
	public []byte
	err    error
}

// Sign implements Signer.
func (s *KMSSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if s.Client == nil || s.Key == "" {
		return nil, errors.New("keys: a KMS signer needs a client and a key")
	}
	// The key's spec says which hash and which algorithm, and a key of
	// another kind is refused here, before anything is signed with it.
	if _, err := s.PublicKey(ctx); err != nil {
		return nil, err
	}
	var digest []byte
	var algorithm types.SigningAlgorithmSpec
	switch s.spec {
	case types.KeySpecEccNistP384:
		sum := sha512.Sum384(message)
		digest, algorithm = sum[:], types.SigningAlgorithmSpecEcdsaSha384
	default:
		sum := sha256.Sum256(message)
		digest, algorithm = sum[:], types.SigningAlgorithmSpecEcdsaSha256
	}
	out, err := s.Client.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(s.Key),
		Message:          digest,
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: algorithm,
	})
	if err != nil {
		return nil, fmt.Errorf("keys: kms sign with %s: %w", s.Key, err)
	}
	return out.Signature, nil
}

// PublicKey implements Signer. It is fetched once: a key's public half does not
// change, and the notary asks for it on every run.
func (s *KMSSigner) PublicKey(ctx context.Context) ([]byte, error) {
	s.once.Do(func() {
		if s.Client == nil || s.Key == "" {
			s.err = errors.New("keys: a KMS signer needs a client and a key")
			return
		}
		out, err := s.Client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(s.Key)})
		if err != nil {
			s.err = fmt.Errorf("keys: kms public key of %s: %w", s.Key, err)
			return
		}
		if out.KeySpec != types.KeySpecEccNistP384 && out.KeySpec != types.KeySpecEccNistP256 {
			s.err = fmt.Errorf("keys: %s is a %s key; seals are signed with ECC_NIST_P384 (ECC_NIST_P256 is accepted for older callers)",
				s.Key, out.KeySpec)
			return
		}
		s.spec = out.KeySpec
		s.public = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: out.PublicKey})
	})
	return s.public, s.err
}

// KeyID implements Signer.
func (s *KMSSigner) KeyID() string { return "kms:" + s.Key }
