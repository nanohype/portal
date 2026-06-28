package aws

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

// taggingAPI is the slice of the Resource Groups Tagging API the teardown uses
// to discover a cluster's resources. One method, so it mocks cleanly.
type taggingAPI interface {
	GetResources(ctx context.Context, in *resourcegroupstaggingapi.GetResourcesInput, optFns ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

// clusterTeardown implements the TeardownAPI for one wedged cluster in one
// workload account, using credentials assumed into the fleet-unwedge role. The
// per-type Delete dispatch is added alongside the service clients; Discover (the
// entry point) lands first.
type clusterTeardown struct {
	tagging taggingAPI
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
