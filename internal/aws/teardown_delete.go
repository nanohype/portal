package aws

import (
	"context"
	"errors"
	"fmt"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/autoscaling"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// newClusterTeardown builds a teardown bound to one workload account, from a
// config already assumed into that account's fleet-unwedge role. Every client
// shares the one tag-scoped, boundary-capped credential — the unwedge role can
// only ever touch ProvisionedBy=eks-fleet resources, so a wrong dispatch here
// fails closed at IAM rather than reaching across the account.
func newClusterTeardown(cfg awssdk.Config) *clusterTeardown {
	return &clusterTeardown{
		tagging: resourcegroupstaggingapi.NewFromConfig(cfg),
		ec2:     ec2.NewFromConfig(cfg),
		eks:     eks.NewFromConfig(cfg),
		iam:     iam.NewFromConfig(cfg),
		logs:    cloudwatchlogs.NewFromConfig(cfg),
		kms:     kms.NewFromConfig(cfg),
		sqs:     sqs.NewFromConfig(cfg),
		events:  eventbridge.NewFromConfig(cfg),
		elb:     elasticloadbalancingv2.NewFromConfig(cfg),
		asg:     autoscaling.NewFromConfig(cfg),
	}
}

var _ TeardownAPI = (*clusterTeardown)(nil)

// Delete removes one discovered resource and reports the engine's outcome. It
// dispatches on service:type to the typed delete, then routes the raw AWS error
// through classifyDeleteError so the engine sees exactly nil (gone) /
// DependencyError (retry) / fatal. A type with no handler is a hard error rather
// than a silent skip: discovery only yields types parseResourceARN recognises,
// so a missing handler means a real gap, and the engine must surface it — never
// report a still-present resource as torn down.
func (t *clusterTeardown) Delete(ctx context.Context, r Resource) error {
	del, ok := t.deleters()[r.Service+":"+r.Type]
	if !ok {
		return fmt.Errorf("teardown: no delete handler for %s:%s (%s)", r.Service, r.Type, r.ARN)
	}
	return classifyDeleteError(del(ctx, r))
}

// deleters maps service:type onto the typed delete. It is the dispatch table
// Delete walks and the completeness check the test asserts against — every type
// parseResourceARN can emit must have an entry here, or a discovered resource
// would have nowhere to go.
func (t *clusterTeardown) deleters() map[string]func(context.Context, Resource) error {
	return map[string]func(context.Context, Resource) error{
		// EKS — the control plane and its children. Children rank ahead of the
		// cluster; each delete blocks until gone so the cluster (and then the VPC)
		// can follow without tripping a dependency error every pass.
		"eks:nodegroup":      t.deleteNodegroup,
		"eks:fargateprofile": t.deleteFargateProfile,
		"eks:addon":          t.deleteAddon,
		"eks:cluster":        t.deleteCluster,

		// EC2 — the network. Order is the engine's job; each handler only breaks
		// the references it owns (SG rules, IGW attachment) so retries converge.
		"ec2:network-interface":            t.deleteNetworkInterface,
		"ec2:natgateway":                   t.deleteNatGateway,
		"ec2:security-group":               t.deleteSecurityGroup,
		"ec2:subnet":                       t.deleteSubnet,
		"ec2:route-table":                  t.deleteRouteTable,
		"ec2:network-acl":                  t.deleteNetworkACL,
		"ec2:internet-gateway":             t.deleteInternetGateway,
		"ec2:egress-only-internet-gateway": t.deleteEgressOnlyIGW,
		"ec2:elastic-ip":                   t.deleteElasticIP,
		"ec2:vpc-endpoint":                 t.deleteVPCEndpoint,
		"ec2:launch-template":              t.deleteLaunchTemplate,
		"ec2:vpc-flow-log":                 t.deleteFlowLog,
		"ec2:vpc":                          t.deleteVPC,

		// IAM — global, so it leans on the unwedge role's /eks-fleet/ path scope,
		// not a region. Each handler empties the resource (detach, remove, prune
		// versions) before the delete IAM would otherwise reject.
		"iam:role":             t.deleteRole,
		"iam:instance-profile": t.deleteInstanceProfile,
		"iam:policy":           t.deletePolicy,
		"iam:oidc-provider":    t.deleteOIDCProvider,

		// The leaf services the stack also stands up.
		"logs:log-group":                    t.deleteLogGroup,
		"kms:key":                           t.deleteKMSKey,
		"kms:alias":                         t.deleteKMSAlias,
		"sqs:queue":                         t.deleteQueue,
		"events:rule":                       t.deleteEventRule,
		"autoscaling:autoScalingGroup":      t.deleteASG,
		"elasticloadbalancing:loadbalancer": t.deleteLoadBalancer,
		"elasticloadbalancing:targetgroup":  t.deleteTargetGroup,
	}
}

// teardownWait bounds how long a "delete then confirm gone" waiter blocks. The
// async EKS/NAT deletes are the only ones that need it; the cap keeps one stuck
// resource from pinning a teardown pass forever — the waiter timing out surfaces
// as a fatal error the operator + runbook take over, which is the right outcome.
const teardownWait = 20 * time.Minute

// ── EKS ────────────────────────────────────────────────────────────────────

func (t *clusterTeardown) deleteCluster(ctx context.Context, r Resource) error {
	if _, err := t.eks.DeleteCluster(ctx, &eks.DeleteClusterInput{Name: &r.ID}); err != nil {
		return err
	}
	return eks.NewClusterDeletedWaiter(t.eks).Wait(ctx, &eks.DescribeClusterInput{Name: &r.ID}, teardownWait)
}

func (t *clusterTeardown) deleteNodegroup(ctx context.Context, r Resource) error {
	cluster, name, ok := splitSlash(r.ID)
	if !ok {
		return fmt.Errorf("teardown: malformed nodegroup id %q", r.ID)
	}
	if _, err := t.eks.DeleteNodegroup(ctx, &eks.DeleteNodegroupInput{ClusterName: &cluster, NodegroupName: &name}); err != nil {
		return err
	}
	return eks.NewNodegroupDeletedWaiter(t.eks).Wait(ctx,
		&eks.DescribeNodegroupInput{ClusterName: &cluster, NodegroupName: &name}, teardownWait)
}

func (t *clusterTeardown) deleteFargateProfile(ctx context.Context, r Resource) error {
	cluster, name, ok := splitSlash(r.ID)
	if !ok {
		return fmt.Errorf("teardown: malformed fargate profile id %q", r.ID)
	}
	if _, err := t.eks.DeleteFargateProfile(ctx, &eks.DeleteFargateProfileInput{ClusterName: &cluster, FargateProfileName: &name}); err != nil {
		return err
	}
	return eks.NewFargateProfileDeletedWaiter(t.eks).Wait(ctx,
		&eks.DescribeFargateProfileInput{ClusterName: &cluster, FargateProfileName: &name}, teardownWait)
}

func (t *clusterTeardown) deleteAddon(ctx context.Context, r Resource) error {
	cluster, name, ok := splitSlash(r.ID)
	if !ok {
		return fmt.Errorf("teardown: malformed addon id %q", r.ID)
	}
	if _, err := t.eks.DeleteAddon(ctx, &eks.DeleteAddonInput{ClusterName: &cluster, AddonName: &name}); err != nil {
		return err
	}
	return eks.NewAddonDeletedWaiter(t.eks).Wait(ctx,
		&eks.DescribeAddonInput{ClusterName: &cluster, AddonName: &name}, teardownWait)
}

// ── EC2 ────────────────────────────────────────────────────────────────────

// deleteSecurityGroup revokes the group's own rules before deleting it. EKS
// wires the cluster and node groups to reference each other, so a plain delete
// deadlocks on a cycle no retry can break. Revoking each group's egress/ingress
// drops its outbound references; once both ends are revoked, the deletes land on
// the next pass. The revokes are best-effort — a missing rule is fine.
func (t *clusterTeardown) deleteSecurityGroup(ctx context.Context, r Resource) error {
	if out, err := t.ec2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{GroupIds: []string{r.ID}}); err == nil && len(out.SecurityGroups) == 1 {
		sg := out.SecurityGroups[0]
		if len(sg.IpPermissions) > 0 {
			_, _ = t.ec2.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{GroupId: &r.ID, IpPermissions: sg.IpPermissions})
		}
		if len(sg.IpPermissionsEgress) > 0 {
			_, _ = t.ec2.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{GroupId: &r.ID, IpPermissions: sg.IpPermissionsEgress})
		}
	}
	_, err := t.ec2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteSubnet(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{SubnetId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteVPC(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteVpc(ctx, &ec2.DeleteVpcInput{VpcId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteNatGateway(ctx context.Context, r Resource) error {
	if _, err := t.ec2.DeleteNatGateway(ctx, &ec2.DeleteNatGatewayInput{NatGatewayId: &r.ID}); err != nil {
		return err
	}
	// NAT deletion is async and holds the EIP + subnet until it finishes.
	return ec2.NewNatGatewayDeletedWaiter(t.ec2).Wait(ctx,
		&ec2.DescribeNatGatewaysInput{NatGatewayIds: []string{r.ID}}, teardownWait)
}

// deleteInternetGateway detaches from its VPC before deleting — an attached IGW
// can't be removed, and the attachment is the one reference the gateway owns.
func (t *clusterTeardown) deleteInternetGateway(ctx context.Context, r Resource) error {
	if out, err := t.ec2.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{r.ID}}); err == nil {
		for _, igw := range out.InternetGateways {
			for _, att := range igw.Attachments {
				if att.VpcId != nil {
					_, _ = t.ec2.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{InternetGatewayId: &r.ID, VpcId: att.VpcId})
				}
			}
		}
	}
	_, err := t.ec2.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{InternetGatewayId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteEgressOnlyIGW(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteEgressOnlyInternetGateway(ctx, &ec2.DeleteEgressOnlyInternetGatewayInput{EgressOnlyInternetGatewayId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteRouteTable(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{RouteTableId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteElasticIP(ctx context.Context, r Resource) error {
	// The tagged ARN carries the allocation id (eipalloc-…), which is what a VPC
	// EIP release takes.
	_, err := t.ec2.ReleaseAddress(ctx, &ec2.ReleaseAddressInput{AllocationId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteVPCEndpoint(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteVpcEndpoints(ctx, &ec2.DeleteVpcEndpointsInput{VpcEndpointIds: []string{r.ID}})
	return err
}

func (t *clusterTeardown) deleteLaunchTemplate(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteLaunchTemplate(ctx, &ec2.DeleteLaunchTemplateInput{LaunchTemplateId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteNetworkACL(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteNetworkAcl(ctx, &ec2.DeleteNetworkAclInput{NetworkAclId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteNetworkInterface(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: &r.ID})
	return err
}

func (t *clusterTeardown) deleteFlowLog(ctx context.Context, r Resource) error {
	_, err := t.ec2.DeleteFlowLogs(ctx, &ec2.DeleteFlowLogsInput{FlowLogIds: []string{r.ID}})
	return err
}

// ── IAM ────────────────────────────────────────────────────────────────────

// deleteRole empties the role before deleting it: IAM rejects a role that still
// has attached managed policies, inline policies, or instance-profile
// memberships. Each emptying step is best-effort so a partially-cleaned role
// (some passes already done) still converges to the delete.
func (t *clusterTeardown) deleteRole(ctx context.Context, r Resource) error {
	name := r.ID
	if att, err := t.iam.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: &name}); err == nil {
		for _, p := range att.AttachedPolicies {
			_, _ = t.iam.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: &name, PolicyArn: p.PolicyArn})
		}
	}
	if inl, err := t.iam.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: &name}); err == nil {
		for i := range inl.PolicyNames {
			_, _ = t.iam.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: &name, PolicyName: &inl.PolicyNames[i]})
		}
	}
	if ips, err := t.iam.ListInstanceProfilesForRole(ctx, &iam.ListInstanceProfilesForRoleInput{RoleName: &name}); err == nil {
		for _, ip := range ips.InstanceProfiles {
			_, _ = t.iam.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{InstanceProfileName: ip.InstanceProfileName, RoleName: &name})
		}
	}
	_, err := t.iam.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: &name})
	return err
}

// deleteInstanceProfile removes any roles from the profile before deleting it.
func (t *clusterTeardown) deleteInstanceProfile(ctx context.Context, r Resource) error {
	name := r.ID
	if gp, err := t.iam.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: &name}); err == nil && gp.InstanceProfile != nil {
		for _, role := range gp.InstanceProfile.Roles {
			_, _ = t.iam.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{InstanceProfileName: &name, RoleName: role.RoleName})
		}
	}
	_, err := t.iam.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: &name})
	return err
}

// deletePolicy prunes non-default versions first — IAM won't delete a managed
// policy that still has them. The delete takes the policy ARN, which the tagged
// resource carries verbatim.
func (t *clusterTeardown) deletePolicy(ctx context.Context, r Resource) error {
	arn := r.ARN
	if vs, err := t.iam.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: &arn}); err == nil {
		for _, v := range vs.Versions {
			if !v.IsDefaultVersion {
				_, _ = t.iam.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: &arn, VersionId: v.VersionId})
			}
		}
	}
	_, err := t.iam.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: &arn})
	return err
}

func (t *clusterTeardown) deleteOIDCProvider(ctx context.Context, r Resource) error {
	// parseResourceARN keeps the full ARN as the id for OIDC providers.
	_, err := t.iam.DeleteOpenIDConnectProvider(ctx, &iam.DeleteOpenIDConnectProviderInput{OpenIDConnectProviderArn: &r.ID})
	return err
}

// ── leaf services ──────────────────────────────────────────────────────────

func (t *clusterTeardown) deleteLogGroup(ctx context.Context, r Resource) error {
	_, err := t.logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: &r.ID})
	return err
}

// deleteKMSKey schedules deletion with the minimum 7-day window — KMS keys can't
// be deleted outright. A key already pending deletion (or otherwise not in a
// schedulable state) comes back as KMSInvalidStateException, which for a
// teardown is the goal state, so treat it as done.
func (t *clusterTeardown) deleteKMSKey(ctx context.Context, r Resource) error {
	_, err := t.kms.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               &r.ID,
		PendingWindowInDays: awssdk.Int32(7),
	})
	var badState *kmstypes.KMSInvalidStateException
	if errors.As(err, &badState) {
		return nil
	}
	return err
}

func (t *clusterTeardown) deleteKMSAlias(ctx context.Context, r Resource) error {
	// parseResourceARN stores the id as alias/<name>, which DeleteAlias takes.
	_, err := t.kms.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: &r.ID})
	return err
}

// deleteQueue resolves the queue URL from its name, then deletes it. A queue
// that's already gone surfaces at the lookup as QueueDoesNotExist, which
// classifyDeleteError reads as success.
func (t *clusterTeardown) deleteQueue(ctx context.Context, r Resource) error {
	out, err := t.sqs.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: &r.ID})
	if err != nil {
		return err
	}
	_, err = t.sqs.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: out.QueueUrl})
	return err
}

// deleteEventRule clears the rule's targets before deleting it — EventBridge
// rejects a delete while targets remain. The id is the rule name on the default
// bus, or <bus>/<name> on a custom one.
func (t *clusterTeardown) deleteEventRule(ctx context.Context, r Resource) error {
	name := r.ID
	var bus *string
	if b, n, ok := splitSlash(r.ID); ok {
		busName := b
		name = n
		bus = &busName
	}
	if tg, err := t.events.ListTargetsByRule(ctx, &eventbridge.ListTargetsByRuleInput{Rule: &name, EventBusName: bus}); err == nil && len(tg.Targets) > 0 {
		ids := make([]string, 0, len(tg.Targets))
		for _, tt := range tg.Targets {
			if tt.Id != nil {
				ids = append(ids, *tt.Id)
			}
		}
		if len(ids) > 0 {
			_, _ = t.events.RemoveTargets(ctx, &eventbridge.RemoveTargetsInput{Rule: &name, EventBusName: bus, Ids: ids, Force: true})
		}
	}
	_, err := t.events.DeleteRule(ctx, &eventbridge.DeleteRuleInput{Name: &name, EventBusName: bus, Force: true})
	return err
}

func (t *clusterTeardown) deleteASG(ctx context.Context, r Resource) error {
	// ForceDelete terminates instances rather than waiting them out — the cluster
	// is being torn down, so draining gracefully buys nothing.
	_, err := t.asg.DeleteAutoScalingGroup(ctx, &autoscaling.DeleteAutoScalingGroupInput{
		AutoScalingGroupName: &r.ID,
		ForceDelete:          awssdk.Bool(true),
	})
	return err
}

func (t *clusterTeardown) deleteLoadBalancer(ctx context.Context, r Resource) error {
	// parseResourceARN keeps the full ARN as the id for ELBv2 resources.
	_, err := t.elb.DeleteLoadBalancer(ctx, &elasticloadbalancingv2.DeleteLoadBalancerInput{LoadBalancerArn: &r.ID})
	return err
}

func (t *clusterTeardown) deleteTargetGroup(ctx context.Context, r Resource) error {
	_, err := t.elb.DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{TargetGroupArn: &r.ID})
	return err
}
