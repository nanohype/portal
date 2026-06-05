package aws

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	// eksTokenPrefix + base64url(presigned STS URL) is the bearer token EKS
	// accepts — the aws-iam-authenticator "k8s-aws-v1." token format. No static
	// credential is stored anywhere; the token is derived on demand from the
	// assumed account role and expires on its own.
	eksTokenPrefix = "k8s-aws-v1."
	// clusterIDHeader is signed into the presigned URL; EKS validates it names the
	// cluster being accessed, which is what binds the token to one cluster.
	clusterIDHeader = "x-k8s-aws-id"
	// eksTokenTTL is how long we treat a minted token as usable. EKS accepts the
	// derived token for ~14 minutes; we refresh conservatively before that so a
	// cached k8s client never carries an expired token into a request.
	eksTokenTTL = 13 * time.Minute
)

// GetEKSToken mints a short-lived EKS bearer token for clusterName by presigning
// an STS GetCallerIdentity request as the assumed account role, with the
// x-k8s-aws-id header signed in. This is the standard aws-iam-authenticator token
// format the EKS API server accepts.
func (p *Provider) GetEKSToken(ctx context.Context, roleARN, externalID, region, clusterName string) (string, error) {
	if clusterName == "" {
		return "", fmt.Errorf("cluster name is required for an EKS IAM token")
	}
	cfg, err := p.AssumeRoleConfig(ctx, roleARN, externalID, region)
	if err != nil {
		return "", err
	}
	presign := sts.NewPresignClient(sts.NewFromConfig(cfg))
	out, err := presign.PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}, func(o *sts.PresignOptions) {
		o.ClientOptions = append(o.ClientOptions, sts.WithAPIOptions(
			smithyhttp.SetHeaderValue(clusterIDHeader, clusterName),
		))
	})
	if err != nil {
		return "", fmt.Errorf("presign sts:GetCallerIdentity: %w", err)
	}
	return eksTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(out.URL)), nil
}

// EKSTokenSource returns a token-source closure that mints and caches an EKS
// bearer token, refreshing it shortly before expiry. It is what a
// k8s.SlimConfig.TokenSource wants: the built client can be cached indefinitely
// while the token underneath rotates, so portal stores no long-lived credential.
func (p *Provider) EKSTokenSource(roleARN, externalID, region, clusterName string) func(ctx context.Context) (string, error) {
	var (
		mu     sync.Mutex
		cached string
		expiry time.Time
	)
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if cached != "" && time.Now().Before(expiry) {
			return cached, nil
		}
		tok, err := p.GetEKSToken(ctx, roleARN, externalID, region, clusterName)
		if err != nil {
			return "", err
		}
		cached = tok
		expiry = time.Now().Add(eksTokenTTL)
		return tok, nil
	}
}
