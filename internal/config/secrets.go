package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	policyconfig "github.com/truvity/policy/config"
)

// The sources a `secrets` block names.
const (
	SourceEnv  = "env"
	SourceFile = "file"
	SourceSSM  = "ssm"
)

// secretName is a name under a root: relative, made of path segments that start
// with a letter, a digit or an underscore, so that it cannot be `..` and cannot
// start at `/`.
var secretName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*(/[A-Za-z0-9_][A-Za-z0-9_.-]*)*$`)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// check holds a `secrets` block to what the schema's shape cannot say: a root
// where there is a place to read from, and none where there is not.
func (s *SecretsSource) check() error {
	if s.Source == "" {
		s.Source = SourceEnv
	}
	switch s.Source {
	case SourceEnv:
		if s.Root != "" {
			return errors.New("secrets.root is for source file and ssm: an environment variable has no root")
		}
	case SourceFile:
		if !filepath.IsAbs(s.Root) {
			return errors.New("secrets.root must be an absolute directory with source file")
		}
	case SourceSSM:
		if !strings.HasPrefix(s.Root, "/") || strings.HasSuffix(s.Root, "/") || strings.Contains(s.Root, "//") {
			return errors.New("secrets.root must be an SSM parameter path starting with / and not ending in one, " +
				"such as /audit/main/private/config")
		}
	default:
		return fmt.Errorf("secrets.source is %q and must be env, file or ssm", s.Source)
	}
	return nil
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

// Secrets resolves the name a field holds to the secret it stands for, from the
// one source the file declares. The zero value reads environment variables.
type Secrets struct {
	src  SecretsSource
	once sync.Once
	api  ParameterAPI
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
// spells it, for the error: an error names the field and the secret's name and
// where it looked, and never a value.
func (s *Secrets) Get(ctx context.Context, field, name string) (string, error) {
	v, err := s.get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("%s: %w", field, err)
	}
	return v, nil
}

func (s *Secrets) get(ctx context.Context, name string) (string, error) {
	switch s.Source() {
	case SourceEnv:
		if !envName.MatchString(name) {
			return "", fmt.Errorf("%q is not the name of an environment variable", name)
		}
		return policyconfig.Secret(name)
	case SourceFile:
		if !secretName.MatchString(name) {
			return "", fmt.Errorf("%q is not a secret name: relative, with no .. in it", name)
		}
		file := filepath.Join(s.src.Root, filepath.FromSlash(name))
		b, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("secret %s is not readable under %s: %w", name, s.src.Root, pathError(err))
		}
		v := strings.TrimSuffix(string(b), "\n")
		if v == "" {
			return "", fmt.Errorf("secret %s is empty", name)
		}
		return v, nil
	case SourceSSM:
		if !secretName.MatchString(name) {
			return "", fmt.Errorf("%q is not a secret name: relative, with no .. in it", name)
		}
		s.once.Do(func() { s.api, s.err = OpenSSM(ctx) })
		if s.err != nil {
			return "", s.err
		}
		param := path.Join(s.src.Root, name)
		out, err := s.api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(param), WithDecryption: aws.Bool(true)})
		if err != nil {
			return "", fmt.Errorf("secret %s: ssm %s: %w", name, param, err)
		}
		if out.Parameter == nil || aws.ToString(out.Parameter.Value) == "" {
			return "", fmt.Errorf("secret %s: ssm %s is empty", name, param)
		}
		return aws.ToString(out.Parameter.Value), nil
	}
	return "", fmt.Errorf("secrets.source %q is not env, file or ssm", s.src.Source)
}

// pathError drops the path an *os.PathError repeats.
func pathError(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
