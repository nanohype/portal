package aws

import (
	"context"
	"testing"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	tagtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
)

type fakeTagging struct {
	pages      []*resourcegroupstaggingapi.GetResourcesOutput
	gotFilters [][]tagtypes.TagFilter
	gotTokens  []*string
	calls      int
}

func (f *fakeTagging) GetResources(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	f.gotFilters = append(f.gotFilters, in.TagFilters)
	f.gotTokens = append(f.gotTokens, in.PaginationToken)
	out := f.pages[f.calls]
	f.calls++
	return out, nil
}

func mapping(arn string) tagtypes.ResourceTagMapping {
	return tagtypes.ResourceTagMapping{ResourceARN: awssdk.String(arn)}
}

func page(token string, arns ...string) *resourcegroupstaggingapi.GetResourcesOutput {
	out := &resourcegroupstaggingapi.GetResourcesOutput{}
	for _, a := range arns {
		out.ResourceTagMappingList = append(out.ResourceTagMappingList, mapping(a))
	}
	if token != "" {
		out.PaginationToken = awssdk.String(token)
	}
	return out
}

func TestDiscover_PaginatesParsesAndScopes(t *testing.T) {
	f := &fakeTagging{pages: []*resourcegroupstaggingapi.GetResourcesOutput{
		page("next",
			"arn:aws:ec2:us-west-2:111111111111:vpc/vpc-0abc",
			"arn:aws:s3:::not-ours", // unknown service → skipped
		),
		page("",
			"arn:aws:eks:us-west-2:111111111111:cluster/production-prod-eks",
			"arn:aws:ec2:us-west-2:111111111111:instance/i-0abc", // out-of-scope ec2 type → skipped
		),
	}}
	td := &clusterTeardown{tagging: f}

	got, err := td.Discover(context.Background(), "production-prod-eks")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Two known resources across two pages; the s3 + instance ARNs are skipped.
	if len(got) != 2 {
		t.Fatalf("discovered %d resources, want 2: %+v", len(got), got)
	}
	if got[0].Type != "vpc" || got[1].Type != "cluster" {
		t.Errorf("types = %q,%q, want vpc,cluster", got[0].Type, got[1].Type)
	}

	// Both pages were fetched, the second with the first's pagination token.
	if f.calls != 2 {
		t.Fatalf("GetResources calls = %d, want 2", f.calls)
	}
	if f.gotTokens[0] != nil {
		t.Errorf("first call token = %v, want nil", f.gotTokens[0])
	}
	if f.gotTokens[1] == nil || *f.gotTokens[1] != "next" {
		t.Errorf("second call token = %v, want \"next\"", f.gotTokens[1])
	}

	// Scoped by BOTH tags — that AND is what keeps a teardown to one spoke.
	filters := f.gotFilters[0]
	if len(filters) != 2 {
		t.Fatalf("tag filters = %d, want 2", len(filters))
	}
	want := map[string]string{"ProvisionedBy": "eks-fleet", "Cluster": "production-prod-eks"}
	for _, fl := range filters {
		if fl.Key == nil || len(fl.Values) != 1 || want[*fl.Key] != fl.Values[0] {
			t.Errorf("unexpected tag filter %v=%v", fl.Key, fl.Values)
		}
	}
}

func TestDiscover_Empty(t *testing.T) {
	f := &fakeTagging{pages: []*resourcegroupstaggingapi.GetResourcesOutput{page("")}}
	got, err := (&clusterTeardown{tagging: f}).Discover(context.Background(), "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no resources, got %d", len(got))
	}
}
