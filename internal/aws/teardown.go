package aws

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Resource is one tagged AWS resource the unwedge teardown discovered, parsed
// from its ARN into the pieces the delete call needs.
type Resource struct {
	ARN     string
	Service string // ec2, eks, iam, logs, kms, sqs, events, autoscaling, elasticloadbalancing
	Type    string // e.g. vpc, subnet, security-group, cluster, nodegroup, role, log-group
	ID      string // the id/name the delete call takes
	Region  string
}

// TeardownAPI is the narrow AWS surface the wedged-cluster teardown drives.
// Discover finds the cluster's resources by tag; Delete ENSURES one is gone and
// is IDEMPOTENT (nil if already gone). For async deletes (NAT gateway, EKS
// cluster/nodegroup) Delete polls to completion before returning, so a nil from
// Delete means truly gone — the engine's retry exists only to resolve ordering
// (a resource the rank tried to delete before its dependency was gone), not to
// wait out async. When a resource can't be deleted yet because something still
// depends on it, Delete returns a *DependencyError so the engine retries it once
// the blocker is gone.
type TeardownAPI interface {
	Discover(ctx context.Context, clusterTag string) ([]Resource, error)
	Delete(ctx context.Context, r Resource) error
}

// DependencyError marks a delete that failed because the resource is still
// referenced (a subnet with a live NAT gateway, a VPC with subnets, an EKS
// cluster with node groups). The engine treats these as retryable; any other
// error is fatal and aborts the teardown for the operator + runbook.
type DependencyError struct{ Err error }

func (e *DependencyError) Error() string {
	if e.Err == nil {
		return "resource still has dependencies"
	}
	return e.Err.Error()
}

func (e *DependencyError) Unwrap() error { return e.Err }

// deletionRank orders resources so dependents are torn down before what they
// depend on (lower = first). The retry loop handles whatever the rank can't
// (async deletes that take minutes, cross-referencing security groups), so this
// is a strong hint that minimizes passes, not a hard contract.
func deletionRank(r Resource) int {
	switch r.Service + ":" + r.Type {
	case "eks:nodegroup", "eks:fargateprofile":
		return 0
	case "eks:addon":
		return 1
	case "eks:cluster":
		return 2
	case "elasticloadbalancing:loadbalancer":
		return 3
	case "elasticloadbalancing:targetgroup":
		return 4
	case "autoscaling:autoScalingGroup":
		return 5
	case "ec2:network-interface":
		return 6
	case "ec2:natgateway":
		return 7
	case "ec2:vpc-endpoint":
		return 8
	case "ec2:subnet":
		return 9
	case "ec2:security-group":
		return 10
	case "ec2:route-table":
		return 11
	case "ec2:internet-gateway", "ec2:egress-only-internet-gateway":
		return 12
	case "ec2:network-acl":
		return 13
	case "ec2:elastic-ip": // released after the NAT gateway that used it
		return 14
	case "ec2:vpc":
		return 15
	case "kms:key", "kms:alias":
		return 16
	case "logs:log-group":
		return 17
	case "sqs:queue":
		return 18
	case "events:rule":
		return 19
	case "iam:instance-profile":
		return 20
	case "iam:policy":
		return 21
	case "iam:role":
		return 22
	case "iam:oidc-provider":
		return 23
	default:
		return 50
	}
}

// TeardownResult summarizes a run.
type TeardownResult struct {
	Deleted   int
	Remaining []Resource // anything still standing when the run gave up
}

type teardownConfig struct {
	maxPasses int
	passDelay time.Duration
}

// TeardownOption tunes the teardown loop (tests set passDelay to 0).
type TeardownOption func(*teardownConfig)

// WithPassDelay sets the wait between retry passes (async deletes need time to
// settle before their dependents free up).
func WithPassDelay(d time.Duration) TeardownOption {
	return func(c *teardownConfig) { c.passDelay = d }
}

// WithMaxPasses caps the retry passes.
func WithMaxPasses(n int) TeardownOption { return func(c *teardownConfig) { c.maxPasses = n } }

// Teardown deletes every resource tagged for the cluster, in dependency order,
// retrying across passes so async deletes and cross-references resolve. It is
// idempotent: a resource already gone counts as deleted, so a re-run after a
// partial teardown is safe. It returns an error if it stalls (no progress with
// resources still standing) or runs out of passes — the caller surfaces that and
// the operator falls back to the runbook rather than the engine pretending success.
//
// Safety: clusterTag scopes Discover to exactly this cluster (ProvisionedBy +
// Cluster), and the assumed unwedge role can only touch ProvisionedBy=eks-fleet
// resources — two independent limits against ever reaching another spoke.
func Teardown(ctx context.Context, api TeardownAPI, clusterTag string, opts ...TeardownOption) (TeardownResult, error) {
	cfg := teardownConfig{maxPasses: 40, passDelay: 15 * time.Second}
	for _, o := range opts {
		o(&cfg)
	}

	remaining, err := api.Discover(ctx, clusterTag)
	if err != nil {
		return TeardownResult{}, fmt.Errorf("discover cluster resources: %w", err)
	}
	sort.SliceStable(remaining, func(i, j int) bool {
		return deletionRank(remaining[i]) < deletionRank(remaining[j])
	})

	deleted := 0
	for pass := 0; pass < cfg.maxPasses && len(remaining) > 0; pass++ {
		var blocked []Resource
		progress := false
		for _, r := range remaining {
			if err := ctx.Err(); err != nil {
				return TeardownResult{Deleted: deleted, Remaining: remaining}, err
			}
			err := api.Delete(ctx, r)
			switch {
			case err == nil:
				deleted++
				progress = true
			case isDependencyError(err):
				blocked = append(blocked, r)
			default:
				return TeardownResult{Deleted: deleted, Remaining: remaining},
					fmt.Errorf("delete %s: %w", r.ARN, err)
			}
		}
		remaining = blocked
		if len(remaining) == 0 {
			break
		}
		if !progress {
			return TeardownResult{Deleted: deleted, Remaining: remaining},
				fmt.Errorf("teardown stalled: %d resource(s) still blocked after a pass made no progress (e.g. %s)",
					len(remaining), remaining[0].ARN)
		}
		if err := wait(ctx, cfg.passDelay); err != nil {
			return TeardownResult{Deleted: deleted, Remaining: remaining}, err
		}
	}

	if len(remaining) > 0 {
		return TeardownResult{Deleted: deleted, Remaining: remaining},
			fmt.Errorf("teardown incomplete: %d resource(s) remain after %d passes", len(remaining), cfg.maxPasses)
	}
	return TeardownResult{Deleted: deleted}, nil
}

func isDependencyError(err error) bool {
	var dep *DependencyError
	return errors.As(err, &dep)
}

func wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
