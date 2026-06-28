package aws

import "testing"

func TestParseResourceARN(t *testing.T) {
	tests := []struct {
		name    string
		arn     string
		wantOK  bool
		service string
		typ     string
		id      string
	}{
		{
			name:    "ec2 vpc",
			arn:     "arn:aws:ec2:us-west-2:111111111111:vpc/vpc-0abc",
			wantOK:  true,
			service: "ec2", typ: "vpc", id: "vpc-0abc",
		},
		{
			name:    "ec2 security-group",
			arn:     "arn:aws:ec2:us-west-2:111111111111:security-group/sg-0def",
			wantOK:  true,
			service: "ec2", typ: "security-group", id: "sg-0def",
		},
		{
			name:    "ec2 natgateway",
			arn:     "arn:aws:ec2:us-west-2:111111111111:natgateway/nat-0aaa",
			wantOK:  true,
			service: "ec2", typ: "natgateway", id: "nat-0aaa",
		},
		{
			name:   "ec2 unknown type is skipped",
			arn:    "arn:aws:ec2:us-west-2:111111111111:instance/i-0abc",
			wantOK: false,
		},
		{
			name:    "eks cluster",
			arn:     "arn:aws:eks:us-west-2:111111111111:cluster/production-prod-eks",
			wantOK:  true,
			service: "eks", typ: "cluster", id: "production-prod-eks",
		},
		{
			name:    "eks nodegroup keeps cluster + name",
			arn:     "arn:aws:eks:us-west-2:111111111111:nodegroup/production-prod-eks/system/abc-uuid",
			wantOK:  true,
			service: "eks", typ: "nodegroup", id: "production-prod-eks/system",
		},
		{
			name:    "iam role strips the path",
			arn:     "arn:aws:iam::111111111111:role/eks-fleet/production-prod-eks-cluster",
			wantOK:  true,
			service: "iam", typ: "role", id: "production-prod-eks-cluster",
		},
		{
			name:    "iam oidc-provider keeps the full ARN",
			arn:     "arn:aws:iam::111111111111:oidc-provider/oidc.eks.us-west-2.amazonaws.com/id/ABC123",
			wantOK:  true,
			service: "iam", typ: "oidc-provider",
			id: "arn:aws:iam::111111111111:oidc-provider/oidc.eks.us-west-2.amazonaws.com/id/ABC123",
		},
		{
			name:    "logs log-group (colon tail)",
			arn:     "arn:aws:logs:us-west-2:111111111111:log-group:/aws/eks/production-prod-eks/cluster",
			wantOK:  true,
			service: "logs", typ: "log-group", id: "/aws/eks/production-prod-eks/cluster",
		},
		{
			name:    "kms key",
			arn:     "arn:aws:kms:us-west-2:111111111111:key/abcd-1234",
			wantOK:  true,
			service: "kms", typ: "key", id: "abcd-1234",
		},
		{
			name:    "kms alias keeps alias/ prefix",
			arn:     "arn:aws:kms:us-west-2:111111111111:alias/eks/production-prod-eks",
			wantOK:  true,
			service: "kms", typ: "alias", id: "alias/eks/production-prod-eks",
		},
		{
			name:    "sqs queue (bare name)",
			arn:     "arn:aws:sqs:us-west-2:111111111111:Karpenter-production-prod-eks",
			wantOK:  true,
			service: "sqs", typ: "queue", id: "Karpenter-production-prod-eks",
		},
		{
			name:    "eventbridge rule on a custom bus",
			arn:     "arn:aws:events:us-west-2:111111111111:rule/fleet-bus/Karpenter-prod",
			wantOK:  true,
			service: "events", typ: "rule", id: "fleet-bus/Karpenter-prod",
		},
		{
			name:    "autoscaling group name",
			arn:     "arn:aws:autoscaling:us-west-2:111111111111:autoScalingGroup:uuid-1:autoScalingGroupName/eks-system-abc",
			wantOK:  true,
			service: "autoscaling", typ: "autoScalingGroup", id: "eks-system-abc",
		},
		{
			name:    "elbv2 load balancer keeps the full ARN",
			arn:     "arn:aws:elasticloadbalancing:us-west-2:111111111111:loadbalancer/app/k8s-abc/123",
			wantOK:  true,
			service: "elasticloadbalancing", typ: "loadbalancer",
			id: "arn:aws:elasticloadbalancing:us-west-2:111111111111:loadbalancer/app/k8s-abc/123",
		},
		{
			name:   "unrelated service is skipped",
			arn:    "arn:aws:s3:::some-bucket",
			wantOK: false,
		},
		{
			name:   "malformed arn is skipped",
			arn:    "not-an-arn",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, ok := parseResourceARN(tt.arn)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (%s)", ok, tt.wantOK, tt.arn)
			}
			if !tt.wantOK {
				return
			}
			if r.Service != tt.service || r.Type != tt.typ || r.ID != tt.id {
				t.Errorf("got {service:%q type:%q id:%q}, want {service:%q type:%q id:%q}",
					r.Service, r.Type, r.ID, tt.service, tt.typ, tt.id)
			}
		})
	}
}
