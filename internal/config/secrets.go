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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// The sources a `secrets` block names.
const (
	SourceEnv  = "env"
	SourceFile = "file"
	SourceSSM  = "ssm"
)

// lambdaEnv is the variable the Lambda runtime sets in every function. A function's
// environment is not a place for a secret (it is shown by the console and the API
// and kept in every version), so a process that finds it set refuses to read a
// secret from the environment, whatever the file says.
const lambdaEnv = "AWS_LAMBDA_FUNCTION_NAME"

// rootSegment is one segment of a root: no empty segment and no `.` or `..`, so
// path cleaning cannot move a name out from under it.
var rootSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// cleanRoot says whether root is made of rootSegments after its first slash.
func cleanRoot(root string) bool {
	if !strings.HasPrefix(root, "/") || root == "/" {
		return false
	}
	for _, seg := range strings.Split(root[1:], "/") {
		if !rootSegment.MatchString(seg) {
			return false
		}
	}
	return true
}

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
		if !filepath.IsAbs(s.Root) || !cleanRoot(s.Root) {
			return errors.New("secrets.root must be an absolute directory with source file, with no empty, . or .. segment")
		}
	case SourceSSM:
		if !cleanRoot(s.Root) || !strings.HasPrefix(s.Root, "/") {
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
	src SecretsSource
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
func (s *Secrets) client(ctx context.Context) (ParameterAPI, error) {
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
// name nor a value: the file says which name the field holds, an error is logged
// and rendered where a name that is a path or a variable has no business, and a
// refusal that quoted what it was given would be one more place a mistake (a
// secret pasted in place of its name) reaches a log.
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
		if os.Getenv(lambdaEnv) != "" {
			return "", errors.New("secrets.source is env on AWS Lambda, where a function's environment is not a place for a secret: " +
				"use secrets.source ssm")
		}
		if !envName.MatchString(name) {
			return "", errors.New("the name is not that of an environment variable (secrets.source is env)")
		}
		v, ok := os.LookupEnv(name)
		switch {
		case !ok:
			return "", errors.New("the environment variable it names is not set (secrets.source is env)")
		case v == "":
			return "", errors.New("the environment variable it names is empty (secrets.source is env)")
		}
		return v, nil
	case SourceFile:
		if !secretName.MatchString(name) {
			return "", errors.New("the name is not a secret name: a relative path of letters, digits, dots, underscores and dashes, which does not climb")
		}
		b, err := os.ReadFile(filepath.Join(s.src.Root, filepath.FromSlash(name)))
		if err != nil {
			return "", fmt.Errorf("the secret it names is not readable under secrets.root %s: %w", s.src.Root, pathError(err))
		}
		v := strings.TrimSuffix(string(b), "\n")
		if v == "" {
			return "", fmt.Errorf("the secret it names is empty (a file under secrets.root %s)", s.src.Root)
		}
		return v, nil
	case SourceSSM:
		if !secretName.MatchString(name) {
			return "", errors.New("the name is not a secret name: a relative path of letters, digits, dots, underscores and dashes, which does not climb")
		}
		api, err := s.client(ctx)
		if err != nil {
			return "", err
		}
		out, err := api.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(path.Join(s.src.Root, name)), WithDecryption: aws.Bool(true)})
		if err != nil {
			return "", fmt.Errorf("the SSM parameter it names under secrets.root %s could not be read: %w", s.src.Root, err)
		}
		if out.Parameter == nil || aws.ToString(out.Parameter.Value) == "" {
			return "", fmt.Errorf("the SSM parameter it names under secrets.root %s is empty", s.src.Root)
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
