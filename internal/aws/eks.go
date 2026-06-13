package aws

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/eks"
)

// EKSClusterStatus is the slice of eks:DescribeCluster the cluster-health watcher
// surfaces: the AWS-side control-plane lifecycle (distinct from kube-API
// reachability — a cluster can be UPDATING while the API still answers) and the
// EKS platform version.
type EKSClusterStatus struct {
	Status          string // ACTIVE / CREATING / UPDATING / DELETING / FAILED / PENDING
	PlatformVersion string // e.g. eks.7
	Version         string // Kubernetes version, e.g. 1.30
}

// DescribeCluster reads an EKS cluster's control-plane status via
// eks:DescribeCluster, assuming the same per-account role the EKS token path
// uses (AssumeRoleConfig) — no new identity. The assumed role must hold
// eks:DescribeCluster for the cluster; callers treat an error (commonly
// AccessDenied until that IAM is granted) as "unknown" and degrade gracefully.
func (p *Provider) DescribeCluster(ctx context.Context, roleARN, externalID, region, clusterName string) (EKSClusterStatus, error) {
	cfg, err := p.AssumeRoleConfig(ctx, roleARN, externalID, region)
	if err != nil {
		return EKSClusterStatus{}, err
	}
	out, err := eks.NewFromConfig(cfg).DescribeCluster(ctx, &eks.DescribeClusterInput{Name: &clusterName})
	if err != nil {
		return EKSClusterStatus{}, fmt.Errorf("eks:DescribeCluster %s: %w", clusterName, err)
	}
	if out.Cluster == nil {
		return EKSClusterStatus{}, fmt.Errorf("eks:DescribeCluster %s: empty cluster in response", clusterName)
	}
	st := EKSClusterStatus{Status: string(out.Cluster.Status)}
	if out.Cluster.PlatformVersion != nil {
		st.PlatformVersion = *out.Cluster.PlatformVersion
	}
	if out.Cluster.Version != nil {
		st.Version = *out.Cluster.Version
	}
	return st, nil
}
