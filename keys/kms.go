package keys

import (
	"context"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

// KMSSigner signs the digest chain with an AWS KMS asymmetric key.
//
// It is the answer to the local signer's stated limit: whoever can write the
// archive can also sign for what they wrote. Here the private key never leaves
// KMS, the writer's role has no kms:Sign on it, and only the digest job's role
// does — so writing the archive and vouching for it are different privileges,
// held by different identities, with every signature in KMS's own log.
//
// The key is an ECC_NIST_P256 SIGN_VERIFY key. A digest is signed as a SHA-256
// (MessageType DIGEST) because a busy hour's digest is larger than the 4 KiB
// KMS takes whole; Verify hashes the same way, so an auditor needs only the
// public half this exports.
type KMSSigner struct {
	Client *kms.Client
	// KeyID is the key's ARN, ID or alias. It is also what each digest names
	// as its signer.
	Key string

	once   sync.Once
	public []byte
	err    error
}

// Sign implements Signer.
func (s *KMSSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if s.Client == nil || s.Key == "" {
		return nil, errors.New("keys: a KMS signer needs a client and a key")
	}
	sum := sha256.Sum256(message)
	out, err := s.Client.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(s.Key),
		Message:          sum[:],
		MessageType:      types.MessageTypeDigest,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return nil, fmt.Errorf("keys: kms sign with %s: %w", s.Key, err)
	}
	return out.Signature, nil
}

// PublicKey implements Signer. It is fetched once: a key's public half does not
// change, and the digest job asks for it on every run.
func (s *KMSSigner) PublicKey(ctx context.Context) ([]byte, error) {
	s.once.Do(func() {
		out, err := s.Client.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(s.Key)})
		if err != nil {
			s.err = fmt.Errorf("keys: kms public key of %s: %w", s.Key, err)
			return
		}
		if out.KeySpec != types.KeySpecEccNistP256 {
			s.err = fmt.Errorf("keys: %s is a %s key; the digest chain is signed with ECC_NIST_P256", s.Key, out.KeySpec)
			return
		}
		s.public = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: out.PublicKey})
	})
	return s.public, s.err
}

// KeyID implements Signer.
func (s *KMSSigner) KeyID() string { return "kms:" + s.Key }
