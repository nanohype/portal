package aws

import (
	"context"
	"strings"
	"testing"
)

// oneARNPerType is a representative tagged ARN for every shape parseResourceARN
// recognises. The coverage test parses each and asserts the dispatch table has a
// handler for it — so discovery can never surface a resource the engine then has
// nowhere to delete. Add a type to the parser and this list, and a missing
// handler fails the build's tests rather than a live teardown.
var oneARNPerType = []string{
	// EC2
	"arn:aws:ec2:us-west-2:111111111111:vpc/vpc-0abc",
	"arn:aws:ec2:us-west-2:111111111111:subnet/subnet-0abc",
	"arn:aws:ec2:us-west-2:111111111111:security-group/sg-0abc",
	"arn:aws:ec2:us-west-2:111111111111:route-table/rtb-0abc",
	"arn:aws:ec2:us-west-2:111111111111:natgateway/nat-0abc",
	"arn:aws:ec2:us-west-2:111111111111:internet-gateway/igw-0abc",
	"arn:aws:ec2:us-west-2:111111111111:egress-only-internet-gateway/eigw-0abc",
	"arn:aws:ec2:us-west-2:111111111111:elastic-ip/eipalloc-0abc",
	"arn:aws:ec2:us-west-2:111111111111:vpc-endpoint/vpce-0abc",
	"arn:aws:ec2:us-west-2:111111111111:launch-template/lt-0abc",
	"arn:aws:ec2:us-west-2:111111111111:network-acl/acl-0abc",
	"arn:aws:ec2:us-west-2:111111111111:network-interface/eni-0abc",
	"arn:aws:ec2:us-west-2:111111111111:vpc-flow-log/fl-0abc",
	// EKS
	"arn:aws:eks:us-west-2:111111111111:cluster/production-prod-eks",
	"arn:aws:eks:us-west-2:111111111111:nodegroup/production-prod-eks/ng-default/a1b2c3",
	"arn:aws:eks:us-west-2:111111111111:fargateprofile/production-prod-eks/fp-default/a1b2c3",
	"arn:aws:eks:us-west-2:111111111111:addon/production-prod-eks/vpc-cni/a1b2c3",
	// IAM
	"arn:aws:iam::111111111111:role/eks-fleet/production-prod-eks-node",
	"arn:aws:iam::111111111111:policy/eks-fleet/production-prod-eks-policy",
	"arn:aws:iam::111111111111:instance-profile/eks-fleet/production-prod-eks-ip",
	"arn:aws:iam::111111111111:oidc-provider/oidc.eks.us-west-2.amazonaws.com/id/ABCD1234",
	// leaf services
	"arn:aws:logs:us-west-2:111111111111:log-group:/aws/eks/production-prod-eks/cluster",
	"arn:aws:kms:us-west-2:111111111111:key/1234abcd-12ab-34cd-56ef-1234567890ab",
	"arn:aws:kms:us-west-2:111111111111:alias/eks-fleet-production",
	"arn:aws:sqs:us-west-2:111111111111:production-prod-eks-events",
	"arn:aws:events:us-west-2:111111111111:rule/production-prod-eks-drift",
	"arn:aws:autoscaling:us-west-2:111111111111:autoScalingGroup:uuid:autoScalingGroupName/eks-production-ng",
	"arn:aws:elasticloadbalancing:us-west-2:111111111111:loadbalancer/app/ingress/0abc",
	"arn:aws:elasticloadbalancing:us-west-2:111111111111:targetgroup/ingress/0abc",
}

func TestDelete_HandlerForEveryDiscoverableType(t *testing.T) {
	deleters := (&clusterTeardown{}).deleters()
	seen := map[string]bool{}

	for _, arn := range oneARNPerType {
		r, ok := parseResourceARN(arn)
		if !ok {
			t.Errorf("representative ARN no longer parses: %s", arn)
			continue
		}
		key := r.Service + ":" + r.Type
		seen[key] = true
		if _, ok := deleters[key]; !ok {
			t.Errorf("discoverable type %q (from %s) has no delete handler", key, arn)
		}
	}

	// And no dead handlers: every entry in the table is reachable from a real
	// discovered type, so the dispatch map can't drift from what discovery emits.
	for key := range deleters {
		if !seen[key] {
			t.Errorf("delete handler %q is never produced by discovery", key)
		}
	}
}

func TestDelete_UnsupportedTypeIsHardError(t *testing.T) {
	// A type discovery can't produce (here, a raw EC2 instance) must error, not
	// silently succeed — a no-op "delete" would let the engine record a
	// still-present resource as torn down.
	err := (&clusterTeardown{}).Delete(context.Background(), Resource{
		Service: "ec2", Type: "instance", ID: "i-0abc",
		ARN: "arn:aws:ec2:us-west-2:111111111111:instance/i-0abc",
	})
	if err == nil {
		t.Fatal("Delete on an unhandled type returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "no delete handler") {
		t.Errorf("error = %q, want it to name the missing handler", err)
	}
}
