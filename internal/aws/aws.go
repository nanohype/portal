// Package aws wraps the AWS SDK v2 to give portal a single way to assume into
// the per-account roles configured via the Account entity.
//
// portal's own identity comes from the default credential chain:
// AWS_PROFILE / shared config locally, IRSA in-cluster. From that base
// identity, AssumeRoleConfig produces an aws.Config that authenticates as the
// configured cross-account role — that's what all downstream AWS calls use.
package aws

import (
	"context"
	"fmt"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type assumeRoleKey struct{ roleARN, externalID, region string }

// Provider holds portal's base AWS identity. One instance per server/worker
// process; reused across all Account-driven assume-role calls.
type Provider struct {
	base aws.Config

	mu    sync.Mutex
	cache map[assumeRoleKey]aws.Config
}

// NewProvider loads portal's base credentials from the default chain. This
// works both locally (env vars / AWS_PROFILE / shared config) and inside the
// cluster (IRSA, EKS Pod Identity, EC2 instance profile) without code paths
// branching on environment.
func NewProvider(ctx context.Context) (*Provider, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load default aws config: %w", err)
	}
	return &Provider{base: cfg}, nil
}

// AssumeRoleConfig returns an aws.Config whose credentials come from
// sts:AssumeRole into roleARN. externalID is optional (pass "" when the
// trust policy doesn't require one). region sets the resolved region on the
// returned config so downstream service clients don't need to specify it.
//
// The returned config carries a credential cache: subsequent SDK calls reuse
// the same temporary credentials until they expire, and the stscreds provider
// transparently refreshes when they do.
//
// The assembled config is itself cached per (roleARN, externalID, region) and
// reused across calls. Without this, every call built a fresh provider +
// credential cache, so the per-cluster health watcher re-ran sts:AssumeRole on
// every tick for every cluster — N STS calls each interval. The shared
// CredentialsCache is concurrency-safe, so a cached config is safe to hand to
// concurrent callers (the parallel health fan-out).
func (p *Provider) AssumeRoleConfig(ctx context.Context, roleARN, externalID, region string) (aws.Config, error) {
	if roleARN == "" {
		return aws.Config{}, fmt.Errorf("role arn is required")
	}

	key := assumeRoleKey{roleARN: roleARN, externalID: externalID, region: region}

	p.mu.Lock()
	defer p.mu.Unlock()
	if cfg, ok := p.cache[key]; ok {
		return cfg, nil
	}

	stsClient := sts.NewFromConfig(p.base)
	creds := stscreds.NewAssumeRoleProvider(stsClient, roleARN, func(o *stscreds.AssumeRoleOptions) {
		o.RoleSessionName = "portal"
		if externalID != "" {
			o.ExternalID = aws.String(externalID)
		}
	})

	cfg := p.base.Copy()
	cfg.Credentials = aws.NewCredentialsCache(creds)
	if region != "" {
		cfg.Region = region
	}

	if p.cache == nil {
		p.cache = make(map[assumeRoleKey]aws.Config)
	}
	p.cache[key] = cfg
	return cfg, nil
}

// VerifyAssumeRole performs an sts:GetCallerIdentity using the assumed-role
// credentials. Returns the assumed identity's ARN on success. Used by the
// async connection-test job to surface "credentials work" / "credentials
// don't" to the UI without waiting until the next real AWS call fails.
func (p *Provider) VerifyAssumeRole(ctx context.Context, roleARN, externalID, region string) (string, error) {
	cfg, err := p.AssumeRoleConfig(ctx, roleARN, externalID, region)
	if err != nil {
		return "", err
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("sts:GetCallerIdentity: %w", err)
	}
	if out.Arn == nil {
		return "", fmt.Errorf("sts:GetCallerIdentity returned no ARN")
	}
	return *out.Arn, nil
}
