// Package clusterspec turns a portal cluster-vend form into a namespaced
// eks-fleet Cluster custom resource (fleet.nanohype.dev/v1alpha1). The portal
// commits the rendered manifest to the clusters GitOps repo; the hub's ArgoCD
// applies it and Crossplane vends the EKS cluster. Required inputs are name,
// account, region, and team; everything else carries eks-fleet's defaults when
// unset (matching apis/cluster/definition.yaml). name is the cluster base — the
// EKS cluster is <environment>-<name> — and becomes spec.clusterName on the CR;
// it must be unique per (account, region, environment) and not equal environment.
package clusterspec

import (
	"fmt"
	"net/netip"
	"regexp"

	"sigs.k8s.io/yaml"
)

const (
	apiVersionSuffix      = "v1alpha1"
	apiVersion            = "fleet.nanohype.dev/" + apiVersionSuffix
	kind                  = "Cluster"
	defaultEnvironment    = "development"
	defaultClusterVersion = "1.36"
	// defaultVpcCidr mirrors the XRD's network.create.vpcCidr default. The IPAM
	// exclusion rule is written against it: leaving the CIDR at the default is
	// how you say "I did not pick one".
	defaultVpcCidr = "10.0.0.0/16"
)

// k8sName is the RFC 1123 label/subdomain shape Kubernetes requires for the
// Cluster's metadata.name and namespace.
var (
	k8sName  = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	awsAcct  = regexp.MustCompile(`^[0-9]{12}$`)
	awsRegn  = regexp.MustCompile(`^[a-z]{2}-[a-z]+-[0-9]$`)
	validEnv = map[string]bool{"development": true, "staging": true, "production": true}
)

// Input is the portal-facing shape (snake_case JSON for the API + the
// cluster_operations.spec_json column). Render maps it to the camelCase CR.
//
// Three classes of field live here, and the distinction is the reason
// TestEveryXRDFieldIsAccountedFor exists:
//
//   - Ordered — what the operator placing the vend chooses. Name through
//     ObservabilityTier, plus the per-account substrate ARNs the order supplies
//     by reference (VendRoleArn and friends are SSM-published prerequisites, not
//     things the vend creates).
//   - Stamped — what portal knows about itself and fills in on the operator's
//     behalf: BootstrapAccessRoleArn, PortalAccessRoleArn, TenantsRepoURL. An
//     explicit value always wins, so these stay overridable.
//   - Absent by decision — enableAgentPlatform, gitopsRepoUrl, gitopsRepoBranch
//     and moduleSource are org-wide constants or escape hatches; portal takes
//     the XRD default deliberately. They are named in unexpressedXRDFields so
//     "we chose not to" is checked rather than merely believed.
type Input struct {
	Name                      string       `json:"name"`
	Account                   string       `json:"account"`
	Region                    string       `json:"region"`
	Team                      string       `json:"team"`
	Environment               string       `json:"environment,omitempty"`
	ClusterVersion            string       `json:"cluster_version,omitempty"`
	SystemNodes               *SystemNodes `json:"system_nodes,omitempty"`
	Network                   *Network     `json:"network,omitempty"`
	EndpointPublicAccess      *bool        `json:"endpoint_public_access,omitempty"`
	EndpointPublicAccessCidrs []string     `json:"endpoint_public_access_cidrs,omitempty"`

	// ObservabilityTier is floor (CloudWatch alone) or full (adds the in-cluster
	// LGTM stack + Amazon Managed Prometheus/Grafana). Empty takes the XRD's
	// floor default — a vended cluster is born light, and opting up is deliberate
	// because full has a managed-monitoring substrate prerequisite.
	ObservabilityTier string `json:"observability_tier,omitempty"`
	// TTLDays > 0 tags the spoke ephemeral and the hub reaper deletes it that
	// many days after creation. Empty/0 is persistent.
	TTLDays *int `json:"ttl_days,omitempty"`

	// Per-account substrate prerequisites, supplied by reference. These are
	// published to SSM by landing-zone and are not created by the vend; empty
	// means ungated, which is the XRD default.
	VendRoleArn                    string `json:"vend_role_arn,omitempty"`
	DataKmsKeyArn                  string `json:"data_kms_key_arn,omitempty"`
	ClusterPermissionsBoundaryArn  string `json:"cluster_permissions_boundary_arn,omitempty"`
	OperatorPermissionsBoundaryArn string `json:"operator_permissions_boundary_arn,omitempty"`

	// BootstrapAccessRoleArn is normally left unset and stamped by the worker for
	// cross-account vends (see WithCrossAccountBootstrap); an explicit value here
	// overrides that.
	BootstrapAccessRoleArn string `json:"bootstrap_access_role_arn,omitempty"`
	// PortalAccessRoleArn is stamped by the order service from the ordering
	// account's spoke role (see WithPortalWiring). Without it cluster-stack adds
	// no EKS access entry for portal, so portal can reach this cluster's control
	// plane but not its kube API — no token minting, no tenant watch.
	PortalAccessRoleArn string `json:"portal_access_role_arn,omitempty"`
	// TenantsRepoURL is stamped by the apply worker from the tenants repo portal
	// itself commits to (see WithPortalWiring). Without it the bootstrap
	// registers no deploy key, and ArgoCD on the vended cluster cannot pull the
	// tenant manifests portal writes for it.
	TenantsRepoURL string `json:"tenants_repo_url,omitempty"`
}

// SystemNodes is the system node group sizing.
type SystemNodes struct {
	InstanceTypes []string `json:"instance_types,omitempty"`
	MinSize       *int     `json:"min_size,omitempty"`
	MaxSize       *int     `json:"max_size,omitempty"`
	DesiredSize   *int     `json:"desired_size,omitempty"`
	DiskSize      *int     `json:"disk_size,omitempty"`
}

// Network is the VPC the cluster lands in, discriminated on Mode: create
// provisions a VPC the stack owns, adopt participates in one provisioned
// elsewhere (a same-account shared VPC, or one shared cross-account over RAM).
// Populate the sub-object matching the mode; the other side stays at its
// defaults.
type Network struct {
	Mode   string         `json:"mode,omitempty"`
	Create *NetworkCreate `json:"create,omitempty"`
	Adopt  *NetworkAdopt  `json:"adopt,omitempty"`
}

// NetworkCreate holds the create-mode levers. VpcCidr and IpamPoolID are
// mutually exclusive — a pool-drawn CIDR cannot also be a literal one.
type NetworkCreate struct {
	VpcCidr           string `json:"vpc_cidr,omitempty"`
	IpamPoolID        string `json:"ipam_pool_id,omitempty"`
	IpamNetmaskLength *int   `json:"ipam_netmask_length,omitempty"`
	TransitGatewayID  string `json:"transit_gateway_id,omitempty"`
	CentralizedEgress *bool  `json:"centralized_egress,omitempty"`
	MaxAzs            *int   `json:"max_azs,omitempty"`
	NatGateways       *int   `json:"nat_gateways,omitempty"`
}

// NetworkAdopt references a VPC the stack does not own. The owner runs the VPC
// and the subnet tagging; the cluster only needs the IDs.
type NetworkAdopt struct {
	VpcID     string          `json:"vpc_id,omitempty"`
	SubnetIDs *NetworkSubnets `json:"subnet_ids,omitempty"`
}

// NetworkSubnets are the adopted subnets. Private is required and non-empty in
// adopt mode — nodes land there.
type NetworkSubnets struct {
	Private []string `json:"private,omitempty"`
	Public  []string `json:"public,omitempty"`
}

// Validate checks the required fields and their shapes. The eks-fleet defaults
// cover the optional fields, so only the four identity inputs are enforced here —
// plus the fleet's private-by-default endpoint invariant: opting into a public
// API endpoint requires a non-empty CIDR allowlist. That mirrors the Cluster
// XRD's CEL rule so a bad order fails here (400) instead of twenty minutes into
// the vend.
func (in Input) Validate() error {
	switch {
	case !k8sName.MatchString(in.Name):
		return fmt.Errorf("name %q must be a lowercase RFC-1123 label", in.Name)
	case len(in.Name) > 12:
		return fmt.Errorf("name %q must be <= 12 chars: the derived <environment>-<name> feeds cluster-scoped S3/IAM names; the tightest (agent-iam's account+region-qualified model-artifacts bucket) fits within S3's 63-char limit in us-west-2", in.Name)
	case !awsAcct.MatchString(in.Account):
		return fmt.Errorf("account %q must be a 12-digit AWS account id", in.Account)
	case !awsRegn.MatchString(in.Region):
		return fmt.Errorf("region %q is not a valid AWS region", in.Region)
	case !k8sName.MatchString(in.Team):
		return fmt.Errorf("team %q must be a lowercase RFC-1123 label (it is the Cluster's namespace)", in.Team)
	}
	if in.Environment != "" && !validEnv[in.Environment] {
		return fmt.Errorf("environment %q must be development, staging, or production", in.Environment)
	}
	// name is the cluster base; the EKS cluster is <environment>-<name>. If the two are
	// equal the name doubles (development-development). Mirrors the Cluster XRD's CEL rule so it fails
	// here (400) instead of at admission.
	if env := in.EffectiveEnvironment(); in.Name == env {
		return fmt.Errorf("name must not equal environment %q (the cluster name would double, e.g. %[1]s-%[1]s)", env)
	}
	if in.EndpointPublicAccess != nil && *in.EndpointPublicAccess && len(in.EndpointPublicAccessCidrs) == 0 {
		return fmt.Errorf("endpoint_public_access requires endpoint_public_access_cidrs: clusters vend with a private API endpoint unless a CIDR allowlist scopes the public one")
	}
	for _, cidr := range in.EndpointPublicAccessCidrs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("endpoint_public_access_cidrs entry %q is not a valid CIDR", cidr)
		}
	}
	// A cross-account vend runs as the fleet-vend role, and the XRD refuses one
	// that does not also cap the IAM it mints. Mirrored here because the
	// rejection would otherwise land at admission — after portal has committed
	// the manifest and reported the operation committed — where the order desk
	// never looks and the vend timeline simply stops advancing.
	if in.VendRoleArn != "" && (in.ClusterPermissionsBoundaryArn == "" || in.OperatorPermissionsBoundaryArn == "") {
		return fmt.Errorf("vend_role_arn requires cluster_permissions_boundary_arn and operator_permissions_boundary_arn: a cross-account vend mints IAM under the fleet-vend boundary (SSM /eks-fleet/%s/fleet-vend/vend_permissions_boundary_arn)", in.EffectiveEnvironment())
	}
	if in.ObservabilityTier != "" && in.ObservabilityTier != "floor" && in.ObservabilityTier != "full" {
		return fmt.Errorf("observability_tier %q must be floor or full", in.ObservabilityTier)
	}
	if in.TTLDays != nil && *in.TTLDays < 0 {
		return fmt.Errorf("ttl_days must be >= 0 (0 means persistent)")
	}
	return in.Network.validate()
}

// validate mirrors the Cluster XRD's network CEL rules. A nil Network is valid —
// the XRD defaults to a created VPC.
func (n *Network) validate() error {
	if n == nil {
		return nil
	}
	switch n.Mode {
	case "", "create", "adopt":
	default:
		return fmt.Errorf("network.mode %q must be create or adopt", n.Mode)
	}
	// The unused side is not merely ignored: sending adopt IDs on a create-mode
	// order reads as "portal understood me" while the stack builds a fresh VPC.
	if n.Mode == "adopt" && n.Create != nil {
		return fmt.Errorf("network.create is set but network.mode is adopt; populate only the sub-object matching the mode")
	}
	if n.Mode != "adopt" && n.Adopt != nil {
		return fmt.Errorf("network.adopt is set but network.mode is create; populate only the sub-object matching the mode")
	}
	if n.Mode == "adopt" {
		if n.Adopt == nil || n.Adopt.VpcID == "" {
			return fmt.Errorf("network.mode adopt requires network.adopt.vpc_id")
		}
		if n.Adopt.SubnetIDs == nil || len(n.Adopt.SubnetIDs.Private) == 0 {
			return fmt.Errorf("network.mode adopt requires a non-empty network.adopt.subnet_ids.private (nodes land there)")
		}
		return nil
	}
	c := n.Create
	if c == nil {
		return nil
	}
	if c.VpcCidr != "" {
		if _, err := netip.ParsePrefix(c.VpcCidr); err != nil {
			return fmt.Errorf("network.create.vpc_cidr %q is not a valid CIDR", c.VpcCidr)
		}
	}
	// An IPAM-drawn CIDR and a literal one are two answers to the same question.
	if c.IpamPoolID != "" && c.VpcCidr != "" && c.VpcCidr != defaultVpcCidr {
		return fmt.Errorf("network.create.ipam_pool_id and a non-default network.create.vpc_cidr are mutually exclusive: with a pool the CIDR is drawn from it")
	}
	// Subnets are carved 8 bits smaller than the VPC block, so a base longer
	// than /20 would put them below AWS's /28 minimum.
	if c.IpamNetmaskLength != nil && *c.IpamNetmaskLength != 0 && (*c.IpamNetmaskLength < 16 || *c.IpamNetmaskLength > 20) {
		return fmt.Errorf("network.create.ipam_netmask_length %d must be between 16 and 20 (0 means literal allocation)", *c.IpamNetmaskLength)
	}
	if c.CentralizedEgress != nil && *c.CentralizedEgress && c.TransitGatewayID == "" {
		return fmt.Errorf("network.create.centralized_egress requires network.create.transit_gateway_id: egress routes through the gateway instead of a local NAT")
	}
	return nil
}

// ValidName reports whether s is a valid RFC-1123 label — the shape Kubernetes
// requires for names that become resource names / namespaces. Exported so the
// tenant write-path shares this one rule instead of re-declaring the regex.
func ValidName(s string) bool { return k8sName.MatchString(s) }

// EffectiveEnvironment is the environment after defaulting — also the GitOps
// path segment (clusters/<environment>/<name>.yaml).
func (in Input) EffectiveEnvironment() string {
	if in.Environment != "" {
		return in.Environment
	}
	return defaultEnvironment
}

// WithCrossAccountBootstrap stamps the hub's Crossplane role ARN onto
// BootstrapAccessRoleArn for cross-account vends, returning the (possibly
// updated) Input. A vend is cross-account when VendRoleArn is set; for those the
// spoke must trust the hub role directly — cluster-stack grants it a cluster-admin
// EKS access entry so the bootstrap Workspace's ambient get-token can reach the
// spoke API (get-token can't present the fleet-vend external_id). Same-account
// vends, an empty hubRoleArn, and an already-set BootstrapAccessRoleArn (an
// explicit override) are all left untouched.
func (in Input) WithCrossAccountBootstrap(hubRoleArn string) Input {
	if hubRoleArn == "" || in.VendRoleArn == "" || in.BootstrapAccessRoleArn != "" {
		return in
	}
	in.BootstrapAccessRoleArn = hubRoleArn
	return in
}

// WithPortalWiring stamps the two fields that describe portal to the cluster it
// is vending, returning the (possibly updated) Input. Neither is an operator
// choice — portal is the only party that knows either value — and leaving them
// empty produces a cluster portal cannot fully drive:
//
//   - spokeRoleArn is the ordering account's portal-spoke role. cluster-stack
//     grants it a read EKS access entry only when set; without one portal can
//     call eks:DescribeCluster (an IAM-only control-plane API) but cannot
//     authenticate to the kube API, so token minting and the tenant watcher
//     both fail on a cluster portal vended itself.
//   - tenantsRepoURL is the tenants GitOps repo portal commits tenant manifests
//     to. The bootstrap registers a read-only deploy key + ArgoCD repo
//     credential for it; without one the cluster's ArgoCD cannot pull the
//     manifests portal writes for it.
//
// Empty arguments and already-set values are left untouched, so an explicit
// order-level override still wins and a portal deployment with no tenants repo
// configured stamps nothing.
func (in Input) WithPortalWiring(spokeRoleArn, tenantsRepoURL string) Input {
	if spokeRoleArn != "" && in.PortalAccessRoleArn == "" {
		in.PortalAccessRoleArn = spokeRoleArn
	}
	if tenantsRepoURL != "" && in.TenantsRepoURL == "" {
		in.TenantsRepoURL = tenantsRepoURL
	}
	return in
}

// the camelCase CR shape (matches apis/cluster/definition.yaml).
type clusterCR struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Metadata   crMetadata `json:"metadata"`
	Spec       crSpec     `json:"spec"`
}

type crMetadata struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type crSpec struct {
	Account                        string         `json:"account"`
	Region                         string         `json:"region"`
	Team                           string         `json:"team"`
	Environment                    string         `json:"environment"`
	ClusterName                    string         `json:"clusterName"`
	ClusterVersion                 string         `json:"clusterVersion"`
	SystemNodes                    *crSystemNodes `json:"systemNodes,omitempty"`
	Network                        *crNetwork     `json:"network,omitempty"`
	EndpointPublicAccess           *bool          `json:"endpointPublicAccess,omitempty"`
	EndpointPublicAccessCidrs      []string       `json:"endpointPublicAccessCidrs,omitempty"`
	ObservabilityTier              string         `json:"observabilityTier,omitempty"`
	TTLDays                        *int           `json:"ttlDays,omitempty"`
	VendRoleArn                    string         `json:"vendRoleArn,omitempty"`
	DataKmsKeyArn                  string         `json:"dataKmsKeyArn,omitempty"`
	ClusterPermissionsBoundaryArn  string         `json:"clusterPermissionsBoundaryArn,omitempty"`
	OperatorPermissionsBoundaryArn string         `json:"operatorPermissionsBoundaryArn,omitempty"`
	BootstrapAccessRoleArn         string         `json:"bootstrapAccessRoleArn,omitempty"`
	PortalAccessRoleArn            string         `json:"portalAccessRoleArn,omitempty"`
	TenantsRepoURL                 string         `json:"tenantsRepoUrl,omitempty"`
}

type crSystemNodes struct {
	InstanceTypes []string `json:"instanceTypes,omitempty"`
	MinSize       *int     `json:"minSize,omitempty"`
	MaxSize       *int     `json:"maxSize,omitempty"`
	DesiredSize   *int     `json:"desiredSize,omitempty"`
	DiskSize      *int     `json:"diskSize,omitempty"`
}

type crNetwork struct {
	Mode   string           `json:"mode,omitempty"`
	Create *crNetworkCreate `json:"create,omitempty"`
	Adopt  *crNetworkAdopt  `json:"adopt,omitempty"`
}

type crNetworkCreate struct {
	VpcCidr           string `json:"vpcCidr,omitempty"`
	IpamPoolID        string `json:"ipamPoolId,omitempty"`
	IpamNetmaskLength *int   `json:"ipamNetmaskLength,omitempty"`
	TransitGatewayID  string `json:"transitGatewayId,omitempty"`
	CentralizedEgress *bool  `json:"centralizedEgress,omitempty"`
	MaxAzs            *int   `json:"maxAzs,omitempty"`
	NatGateways       *int   `json:"natGateways,omitempty"`
}

type crNetworkAdopt struct {
	VpcID     string            `json:"vpcId,omitempty"`
	SubnetIDs *crNetworkSubnets `json:"subnetIds,omitempty"`
}

type crNetworkSubnets struct {
	Private []string `json:"private,omitempty"`
	Public  []string `json:"public,omitempty"`
}

// Render validates the input and returns a complete namespaced Cluster CR as
// YAML, ready to commit to the clusters repo.
func (in Input) Render() (string, error) {
	if err := in.Validate(); err != nil {
		return "", err
	}
	clusterVersion := in.ClusterVersion
	if clusterVersion == "" {
		clusterVersion = defaultClusterVersion
	}
	cr := clusterCR{
		APIVersion: apiVersion,
		Kind:       kind,
		Metadata:   crMetadata{Name: in.Name, Namespace: in.Team},
		Spec: crSpec{
			Account:                        in.Account,
			Region:                         in.Region,
			Team:                           in.Team,
			Environment:                    in.EffectiveEnvironment(),
			ClusterName:                    in.Name,
			ClusterVersion:                 clusterVersion,
			EndpointPublicAccess:           in.EndpointPublicAccess,
			EndpointPublicAccessCidrs:      in.EndpointPublicAccessCidrs,
			ObservabilityTier:              in.ObservabilityTier,
			TTLDays:                        in.TTLDays,
			VendRoleArn:                    in.VendRoleArn,
			DataKmsKeyArn:                  in.DataKmsKeyArn,
			ClusterPermissionsBoundaryArn:  in.ClusterPermissionsBoundaryArn,
			OperatorPermissionsBoundaryArn: in.OperatorPermissionsBoundaryArn,
			BootstrapAccessRoleArn:         in.BootstrapAccessRoleArn,
			PortalAccessRoleArn:            in.PortalAccessRoleArn,
			TenantsRepoURL:                 in.TenantsRepoURL,
		},
	}
	if in.SystemNodes != nil {
		cr.Spec.SystemNodes = &crSystemNodes{
			InstanceTypes: in.SystemNodes.InstanceTypes,
			MinSize:       in.SystemNodes.MinSize,
			MaxSize:       in.SystemNodes.MaxSize,
			DesiredSize:   in.SystemNodes.DesiredSize,
			DiskSize:      in.SystemNodes.DiskSize,
		}
	}
	if in.Network != nil {
		cr.Spec.Network = &crNetwork{Mode: in.Network.Mode}
		if c := in.Network.Create; c != nil {
			cr.Spec.Network.Create = &crNetworkCreate{
				VpcCidr:           c.VpcCidr,
				IpamPoolID:        c.IpamPoolID,
				IpamNetmaskLength: c.IpamNetmaskLength,
				TransitGatewayID:  c.TransitGatewayID,
				CentralizedEgress: c.CentralizedEgress,
				MaxAzs:            c.MaxAzs,
				NatGateways:       c.NatGateways,
			}
		}
		if a := in.Network.Adopt; a != nil {
			cr.Spec.Network.Adopt = &crNetworkAdopt{VpcID: a.VpcID}
			if s := a.SubnetIDs; s != nil {
				cr.Spec.Network.Adopt.SubnetIDs = &crNetworkSubnets{Private: s.Private, Public: s.Public}
			}
		}
	}
	out, err := yaml.Marshal(cr)
	if err != nil {
		return "", fmt.Errorf("marshal Cluster CR: %w", err)
	}
	header := fmt.Sprintf("# Generated by portal — eks-fleet Cluster vend for %q (%s).\n# Do not edit by hand; manage via the portal order desk.\n",
		in.Name, in.EffectiveEnvironment())
	return header + string(out), nil
}
