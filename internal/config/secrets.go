package config

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	policyconfig "github.com/truvity/policy/config"
)

// The sources a `secrets` block names. This repository reads env, file and ssm;
// truvity/policy's resolver also knows openbao, for which it has no store here.
const (
	SourceEnv  = policyconfig.SourceEnv
	SourceFile = policyconfig.SourceFile
	SourceSSM  = policyconfig.SourceSSM
)

// SecretsSource is the `secrets` block: where a field named `...Secret` finds the
// secret it names. One source serves the whole file. It is truvity/policy's, so
// that one implementation holds the rules for a root and for the source.
type SecretsSource = policyconfig.SecretsSource

// checkSecretsSource holds a `secrets` block to what the schema's shape cannot say. It is
// truvity/policy's check without its refusal of env on Lambda, which is made
// when a secret is read, so that a function whose file names no secret still
// loads, and with openbao refused, which has no store here.
func checkSecretsSource(s *SecretsSource) error {
	switch s.Source {
	case "", SourceEnv:
		s.Source = SourceEnv
		if s.Root != "" {
			return errors.New("secrets.root is for source file and ssm: an environment variable has no root")
		}
		return nil
	case policyconfig.SourceOpenBao:
		return fmt.Errorf("secrets.source is %q and must be env, file or ssm", s.Source)
	}
	return s.Check()
}

// ParameterAPI is the part of the SSM client a secret is read with.
type ParameterAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, opts ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// OpenSSM connects to SSM with the process's own identity: on Lambda the
// function's role, on Kubernetes the pod's. A test replaces it.
var OpenSSM = func(ctx context.Context) (ParameterAPI, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading the AWS configuration for SSM: %w", err)
	}
	return ssm.NewFromConfig(cfg), nil
}

// ssmStore is the policyconfig.Store over SSM Parameter Store: it reads one
// SecureString decrypted by its full name. truvity/policy's resolver checks the
// root and the name and never prints what the store returns.
type ssmStore struct {
	mu  sync.Mutex
	api ParameterAPI
	// failedAt and failures back off a client that could not be made: the next read
	// retries it once the wait has passed, so a cold start that met a transient
	// fault is not a function that never reads a secret.
	failedAt time.Time
	failures int
	err      error
}

// client is the SSM client, made on first use. A failure to make it is kept for
// a wait that grows with each (1s, 2s, ... 30s) and then tried again.
func (s *ssmStore) client(ctx context.Context) (ParameterAPI, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.api != nil {
		return s.api, nil
	}
	if s.err != nil && time.Since(s.failedAt) < backoff(s.failures) {
		return nil, s.err
	}
	api, err := OpenSSM(ctx)
	if err != nil {
		s.err, s.failedAt, s.failures = err, time.Now(), s.failures+1
		return nil, err
	}
	s.api, s.err, s.failures = api, nil, 0
	return api, nil
}

func backoff(failures int) time.Duration {
	d := time.Second << min(failures-1, 5)
	return min(d, 30*time.Second)
}

func (s *ssmStore) Get(ctx context.Context, path string) (string, error) {
	api, err := s.client(ctx)
	if err != nil {
		return "", err
	}
	out, err := api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(path), WithDecryption: aws.Bool(true)})
	if err != nil {
		var nf *ssmtypes.ParameterNotFound
		if errors.As(err, &nf) {
			return "", fmt.Errorf("%w: %w", policyconfig.ErrNotFound, err)
		}
		return "", err
	}
	if out.Parameter == nil {
		return "", nil
	}
	return aws.ToString(out.Parameter.Value), nil
}

// Secrets resolves the name a field holds to the secret it stands for, from the
// one source the file declares, through truvity/policy's resolver. The zero value
// reads environment variables.
//
// The resolver is made on the first read and not when the file is loaded, so a
// source it refuses (env on Lambda) fails the read of a secret, as it always
// has, and a file that names no secret still loads there.
type Secrets struct {
	src  SecretsSource
	once sync.Once
	r    *policyconfig.Secrets
	err  error
}

// NewSecrets is a resolver for a `secrets` block that has been checked.
func NewSecrets(src SecretsSource) *Secrets {
	if src.Source == "" {
		src.Source = SourceEnv
	}
	return &Secrets{src: src}
}

// SecretReader is the resolver for this file's `secrets` block.
func (h *Header) SecretReader() *Secrets {
	if h.reader == nil {
		h.reader = NewSecrets(h.Secrets)
	}
	return h.reader
}

// Source is the declared source, `env` when none was.
func (s *Secrets) Source() string {
	if s == nil || s.src.Source == "" {
		return SourceEnv
	}
	return s.src.Source
}

// Get reads the secret `name` that `field` holds. field is the key as the file
// spells it. An error names the field, the source and the root, and neither the
// name nor a value; see [policyconfig.Secrets.Get]. On AWS Lambda the env source
// is refused whatever the file says.
func (s *Secrets) Get(ctx context.Context, field, name string) (string, error) {
	s.once.Do(func() {
		src := s.src
		if src.Source == "" {
			src.Source = SourceEnv
		}
		s.r, s.err = policyconfig.NewSecrets(src, policyconfig.WithStore(SourceSSM, &ssmStore{}))
	})
	if s.err != nil {
		return "", fmt.Errorf("%s: %w", field, s.err)
	}
	return s.r.Get(ctx, field, name)
}
