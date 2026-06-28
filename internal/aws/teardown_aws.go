package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
)

// taggingAPI is the slice of the Resource Groups Tagging API the teardown uses
// to discover a cluster's resources. One method, so it mocks cleanly.
type taggingAPI interface {
	GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, optFns ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

// clusterTeardown implements the TeardownAPI for one wedged cluster in one
// workload account, using credentials assumed into the fleet-unwedge role.
// Discover (tagging) and the per-type Delete dispatch (the service clients) hang
// off this one value; see teardown_delete.go for the constructor + Delete.
type clusterTeardown struct {
	tagging taggingAPI
	ec2     *ec2.Client
	eks     *eks.Client
	iam     *iam.Client
	logs    *cloudwatchlogs.Client
	kms     *kms.Client
	sqs     *sqs.Client
	events  *eventbridge.Client
	elb     *elasticloadbalancingv2.Client
	asg     *autoscaling.Client
}

// AssumeTeardown assumes a workload account's fleet-unwedge role and returns a
// teardown bound to it, ready to hand to Teardown. Keeping the assume-role +
// client construction here means the caller (the unwedge worker) depends only on
// the TeardownAPI, never on the concrete SDK clients or config — and the returned
// teardown can only ever touch ProvisionedBy=eks-fleet resources, because that's
// all the assumed role's permissions boundary allows.
func (p *Provider) AssumeTeardown(ctx context.Context, roleARN, externalID, region string) (TeardownAPI, error) {
	cfg, err := p.AssumeRoleConfig(ctx, roleARN, externalID, region)
	if err != nil {
		return nil, fmt.Errorf("assume unwedge role: %w", err)
	}
	return newClusterTeardown(cfg), nil
}

// Discover finds every resource carrying BOTH ProvisionedBy=eks-fleet AND
// Cluster=<clusterTag> via the Resource Groups Tagging API — the two-tag AND is
// what scopes a teardown to exactly this spoke. ARNs the teardown doesn't handle
// are skipped (parseResourceARN ok=false) rather than guessed at.
func (t *clusterTeardown) Discover(ctx context.Context, clusterTag string) ([]Resource, error) {
	filters := []tagtypes.TagFilter{
		{Key: awssdk.String("ProvisionedBy"), Values: []string{"eks-fleet"}},
		{Key: awssdk.String("Cluster"), Values: []string{clusterTag}},
	}

	var out []Resource
	var token *string
	for {
		page, err := t.tagging.GetResources(ctx, &resourcegroupstaggingapi.GetResourcesInput{
			TagFilters:      filters,
			PaginationToken: token,
		})
		if err != nil {
			return nil, fmt.Errorf("discover tagged resources: %w", err)
		}
		for _, m := range page.ResourceTagMappingList {
			if m.ResourceARN == nil {
				continue
			}
			if r, ok := parseResourceARN(*m.ResourceARN); ok {
				out = append(out, r)
			}
		}
		if page.PaginationToken == nil || *page.PaginationToken == "" {
			break
		}
		token = page.PaginationToken
	}
	return out, nil
}

// classifyDeleteError maps an AWS delete error onto the engine's three outcomes,
// and is the safety-critical core of every per-type Delete: get this wrong and
// the teardown either aborts a recoverable wedge (a dependency error read as
// fatal) or loops on a hopeless one (a fatal error read as retryable).
//
//   - already gone  → nil   : the resource is the goal state; deleting a missing
//     one is success, which is what makes the whole teardown idempotent.
//   - still referenced → *DependencyError : retry once the blocker is gone.
//   - anything else → the error unchanged : fatal; the operator + runbook take over.
func classifyDeleteError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		code := apiErr.ErrorCode()
		switch {
		case isNotFoundCode(code):
			return nil
		case code == "ValidationError" && messageSaysGone(apiErr.ErrorMessage()):
			// Auto Scaling reports a missing group as a generic ValidationError, so
			// only treat it as already-gone when the message actually says so — a
			// real validation failure must stay fatal, not be swallowed.
			return nil
		case isDependencyCode(code):
			return &DependencyError{Err: err}
		}
	}
	return err
}

func messageSaysGone(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not found") || strings.Contains(m, "does not exist")
}

// isNotFoundCode covers the per-service "already gone" codes. EC2 uses an
// Invalid<Thing>ID.NotFound family, so match the suffix as well as the named ones.
func isNotFoundCode(code string) bool {
	switch code {
	case "ResourceNotFoundException", // EKS, CloudWatch Logs, KMS, EventBridge
		"NotFoundException",
		"NoSuchEntity",                            // IAM
		"NoSuchEntityException",                   // IAM (typed)
		"AWS.SimpleQueueService.NonExistentQueue", // SQS
		"QueueDoesNotExist",                       // SQS (typed)
		"LoadBalancerNotFound",                    // ELBv2
		"TargetGroupNotFound":                     // ELBv2
		return true
	}
	return strings.HasSuffix(code, ".NotFound") // EC2: InvalidVpcID.NotFound, InvalidGroup.NotFound, ...
}

// isDependencyCode covers "can't delete yet, something still references this".
func isDependencyCode(code string) bool {
	switch code {
	case "DependencyViolation", // EC2 (subnet/SG/IGW/VPC with dependents)
		"ResourceInUse",          // EC2 in-use variants
		"ResourceInUseException", // EKS, KMS, CloudWatch Logs
		"DeleteConflict",         // IAM: role still has attached policies / instance profile
		"DeleteConflictException",
		"ResourceInUseFault",        // Auto Scaling: group has instances
		"InvalidParameterException": // EKS: cluster has nodegroups / addons still attached
		return true
	}
	return false
}
