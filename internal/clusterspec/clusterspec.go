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
	apiVersion            = "fleet.nanohype.dev/v1alpha1"
	kind                  = "Cluster"
	defaultEnvironment    = "development"
	defaultClusterVersion = "1.36"
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
	VendRoleArn               string       `json:"vend_role_arn,omitempty"`
	// BootstrapAccessRoleArn is normally left unset and stamped by the worker for
	// cross-account vends (see WithCrossAccountBootstrap); an explicit value here
	// overrides that.
	BootstrapAccessRoleArn string `json:"bootstrap_access_role_arn,omitempty"`
}

// SystemNodes is the system node group sizing.
type SystemNodes struct {
	InstanceTypes []string `json:"instance_types,omitempty"`
	MinSize       *int     `json:"min_size,omitempty"`
	MaxSize       *int     `json:"max_size,omitempty"`
	DesiredSize   *int     `json:"desired_size,omitempty"`
	DiskSize      *int     `json:"disk_size,omitempty"`
}

// Network is the VPC the cluster lands in.
type Network struct {
	VpcCidr     string `json:"vpc_cidr,omitempty"`
	MaxAzs      *int   `json:"max_azs,omitempty"`
	NatGateways *int   `json:"nat_gateways,omitempty"`
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
	Account                   string         `json:"account"`
	Region                    string         `json:"region"`
	Team                      string         `json:"team"`
	Environment               string         `json:"environment"`
	ClusterName               string         `json:"clusterName"`
	ClusterVersion            string         `json:"clusterVersion"`
	SystemNodes               *crSystemNodes `json:"systemNodes,omitempty"`
	Network                   *crNetwork     `json:"network,omitempty"`
	EndpointPublicAccess      *bool          `json:"endpointPublicAccess,omitempty"`
	EndpointPublicAccessCidrs []string       `json:"endpointPublicAccessCidrs,omitempty"`
	VendRoleArn               string         `json:"vendRoleArn,omitempty"`
	BootstrapAccessRoleArn    string         `json:"bootstrapAccessRoleArn,omitempty"`
}

type crSystemNodes struct {
	InstanceTypes []string `json:"instanceTypes,omitempty"`
	MinSize       *int     `json:"minSize,omitempty"`
	MaxSize       *int     `json:"maxSize,omitempty"`
	DesiredSize   *int     `json:"desiredSize,omitempty"`
	DiskSize      *int     `json:"diskSize,omitempty"`
}

type crNetwork struct {
	VpcCidr     string `json:"vpcCidr,omitempty"`
	MaxAzs      *int   `json:"maxAzs,omitempty"`
	NatGateways *int   `json:"natGateways,omitempty"`
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
			Account:                   in.Account,
			Region:                    in.Region,
			Team:                      in.Team,
			Environment:               in.EffectiveEnvironment(),
			ClusterName:               in.Name,
			ClusterVersion:            clusterVersion,
			EndpointPublicAccess:      in.EndpointPublicAccess,
			EndpointPublicAccessCidrs: in.EndpointPublicAccessCidrs,
			VendRoleArn:               in.VendRoleArn,
			BootstrapAccessRoleArn:    in.BootstrapAccessRoleArn,
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
		cr.Spec.Network = &crNetwork{
			VpcCidr:     in.Network.VpcCidr,
			MaxAzs:      in.Network.MaxAzs,
			NatGateways: in.Network.NatGateways,
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
