package secrets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// SSMBackend resolves secrets from AWS Systems Manager Parameter Store.
//
// Values are read with the host's own IAM identity, so there is no bootstrap
// secret to place on the gateway and every read is attributable in CloudTrail.
// A secret named "runner-dev3" resolves to "<prefix>/runner-dev3"; an optional
// "<prefix>/runner-dev3.expires" parameter carries an RFC3339 expiry, matching
// the directory backend's convention.
type SSMBackend struct {
	client *ssm.Client
	prefix string
	ttl    time.Duration

	mu    sync.RWMutex
	cache map[string]cachedSecret
	now   func() time.Time
}

type cachedSecret struct {
	secret  Secret
	fetched time.Time
	missing bool
}

// SSMOptions configure the backend.
type SSMOptions struct {
	// Prefix is the parameter path secrets live under, e.g. "/splitscreen".
	Prefix string
	// Region overrides the ambient region; empty uses the default chain.
	Region string
	// CacheTTL bounds how stale a value may be. Authentication resolves a
	// secret on every runner connection, so an uncached backend would put an
	// API call in the reconnect path of a flapping runner.
	CacheTTL time.Duration
}

// DefaultSSMCacheTTL is short enough that a rotation takes effect on its own,
// and long enough that reconnect storms do not become API storms.
const DefaultSSMCacheTTL = 5 * time.Minute

// NewSSMBackend builds a Parameter Store backend using the ambient AWS
// credential chain (instance role, environment, or shared config).
func NewSSMBackend(ctx context.Context, o SSMOptions) (*SSMBackend, error) {
	prefix := strings.TrimRight(o.Prefix, "/")
	if prefix == "" {
		return nil, errors.New("secrets: ssm prefix is required")
	}
	if !strings.HasPrefix(prefix, "/") {
		return nil, fmt.Errorf("secrets: ssm prefix %q must be absolute", prefix)
	}

	var opts []func(*awscfg.LoadOptions) error
	if o.Region != "" {
		opts = append(opts, awscfg.WithRegion(o.Region))
	}
	cfg, err := awscfg.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("secrets: load aws config: %w", err)
	}
	if cfg.Region == "" {
		return nil, errors.New("secrets: no AWS region configured for the ssm backend")
	}

	ttl := o.CacheTTL
	if ttl <= 0 {
		ttl = DefaultSSMCacheTTL
	}
	return &SSMBackend{
		client: ssm.NewFromConfig(cfg),
		prefix: prefix,
		ttl:    ttl,
		cache:  map[string]cachedSecret{},
		now:    time.Now,
	}, nil
}

func (b *SSMBackend) path(name string) string { return b.prefix + "/" + name }

// Get resolves one secret, serving from cache when fresh.
func (b *SSMBackend) Get(name string) (Secret, error) {
	if err := safeName(name); err != nil {
		return Secret{}, err
	}

	b.mu.RLock()
	entry, ok := b.cache[name]
	b.mu.RUnlock()
	if ok && b.now().Sub(entry.fetched) < b.ttl {
		if entry.missing {
			return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return entry.secret, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	sec, err := b.fetch(ctx, name)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// Cache the miss too: a config referencing a parameter that does
			// not exist would otherwise call the API on every attempt.
			b.store(name, cachedSecret{fetched: b.now(), missing: true})
		}
		return Secret{}, err
	}
	b.store(name, cachedSecret{secret: sec, fetched: b.now()})
	return sec, nil
}

func (b *SSMBackend) store(name string, e cachedSecret) {
	b.mu.Lock()
	b.cache[name] = e
	b.mu.Unlock()
}

func (b *SSMBackend) fetch(ctx context.Context, name string) (Secret, error) {
	withDecryption := true
	out, err := b.client.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           strPtr(b.path(name)),
		WithDecryption: &withDecryption,
	})
	if err != nil {
		var notFound *types.ParameterNotFound
		if errors.As(err, &notFound) {
			return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		return Secret{}, fmt.Errorf("secrets: ssm get %s: %w", b.path(name), err)
	}
	if out.Parameter == nil || out.Parameter.Value == nil {
		return Secret{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	s := Secret{
		Name: name,
		// Trailing newlines are the overwhelmingly common way these values get
		// written, and a token with one fails authentication opaquely.
		Value: strings.TrimRight(*out.Parameter.Value, "\r\n"),
	}

	// Expiry is a sibling parameter, optional and never fatal: a missing one
	// means "no known expiry", not "this secret is broken".
	expOut, err := b.client.GetParameter(ctx, &ssm.GetParameterInput{
		Name: strPtr(b.path(name) + ".expires"),
	})
	if err == nil && expOut.Parameter != nil && expOut.Parameter.Value != nil {
		t, perr := time.Parse(time.RFC3339, strings.TrimSpace(*expOut.Parameter.Value))
		if perr == nil {
			s.ExpiresAt = &t
		}
	}
	return s, nil
}

// Names lists every secret under the prefix, for expiry sweeps and status.
func (b *SSMBackend) Names() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	recursive := false
	var out []string
	var token *string
	for {
		page, err := b.client.GetParametersByPath(ctx, &ssm.GetParametersByPathInput{
			Path:      strPtr(b.prefix),
			Recursive: &recursive,
			NextToken: token,
		})
		if err != nil {
			return out
		}
		for _, p := range page.Parameters {
			if p.Name == nil {
				continue
			}
			name := strings.TrimPrefix(*p.Name, b.prefix+"/")
			if strings.HasSuffix(name, ".expires") || strings.Contains(name, "/") {
				continue
			}
			out = append(out, name)
		}
		if page.NextToken == nil || *page.NextToken == "" {
			return out
		}
		token = page.NextToken
	}
}

// Invalidate drops a cached value so the next read goes to the API. Used after
// a rotation when waiting out the TTL is not acceptable.
func (b *SSMBackend) Invalidate(name string) {
	b.mu.Lock()
	delete(b.cache, name)
	b.mu.Unlock()
}

func strPtr(s string) *string { return &s }
