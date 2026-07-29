package clusterspec

import (
	"strings"
	"testing"
)

// The order-time mirror of the Cluster XRD's CEL rules.
//
// Every rule here is also enforced at admission, so portal is not the authority
// — it is the only place the answer is cheap. A rejection at admission lands
// after portal has written the manifest, pushed it, and reported the operation
// committed; the vend timeline simply stops advancing, and the reason is in an
// ArgoCD event nobody on the order desk is watching. Rejected here, it is a 400
// on the form with the field named.
//
// Each case therefore asserts the rule AND that the message says which field,
// because "invalid spec" and no rejection at all cost an operator the same
// afternoon.

func validOrder() Input {
	return Input{Name: "analytics", Account: "222222222222", Region: "us-east-1", Team: "platform"}
}

func TestValidate_CrossAccountVendNeedsBothBoundaries(t *testing.T) {
	boundary := "arn:aws:iam::222222222222:policy/vend-boundary"
	vend := "arn:aws:iam::222222222222:role/production-eks-fleet-vend"

	for _, tc := range []struct {
		name             string
		cluster, operatr string
		wantErr          bool
	}{
		{"neither", "", "", true},
		{"cluster only", boundary, "", true},
		{"operator only", "", boundary, true},
		{"both", boundary, boundary, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validOrder()
			in.VendRoleArn = vend
			in.ClusterPermissionsBoundaryArn = tc.cluster
			in.OperatorPermissionsBoundaryArn = tc.operatr

			err := in.Validate()
			if tc.wantErr && err == nil {
				t.Fatal("accepted a cross-account vend the XRD would reject at admission")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("rejected a complete cross-account vend: %v", err)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "permissions_boundary") {
				t.Errorf("message does not name the missing field: %v", err)
			}
		})
	}
}

func TestValidate_BoundariesAreOnlyRequiredForACrossAccountVend(t *testing.T) {
	// A same-account hub vend runs as the hub role and needs no vend role, so
	// requiring the boundaries there would reject every ordinary order.
	if err := validOrder().Validate(); err != nil {
		t.Fatalf("a same-account vend was rejected: %v", err)
	}
}

func TestValidate_ObservabilityTier(t *testing.T) {
	for _, tc := range []struct {
		tier    string
		wantErr bool
	}{
		{"", false}, // unset takes the XRD's floor default
		{"floor", false},
		{"full", false},
		{"FULL", true},
		{"verbose", true},
	} {
		in := validOrder()
		in.ObservabilityTier = tc.tier

		if err := in.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("observability_tier %q: err = %v, wantErr %v", tc.tier, err, tc.wantErr)
		}
	}
}

func TestValidate_TTLDays(t *testing.T) {
	for _, tc := range []struct {
		days    int
		wantErr bool
	}{
		{0, false}, // persistent
		{7, false},
		{-1, true},
	} {
		in := validOrder()
		in.TTLDays = &tc.days

		if err := in.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("ttl_days %d: err = %v, wantErr %v", tc.days, err, tc.wantErr)
		}
	}
}

func TestValidate_NetworkModeEnum(t *testing.T) {
	in := validOrder()
	in.Network = &Network{Mode: "byo"}

	err := in.Validate()
	if err == nil {
		t.Fatal("accepted a network mode the XRD has no branch for")
	}
	if !strings.Contains(err.Error(), "network.mode") {
		t.Errorf("message does not name the field: %v", err)
	}
}

func TestValidate_NilNetworkIsAValidOrder(t *testing.T) {
	// Most orders do not mention the network at all and take a created VPC.
	in := validOrder()
	in.Network = nil

	if err := in.Validate(); err != nil {
		t.Fatalf("an order with no network block was rejected: %v", err)
	}
}

func TestValidate_TheUnusedSideOfTheUnionIsRefused(t *testing.T) {
	// Not pedantry: the XRD reads only the sub-object matching the mode, so
	// adopt IDs on a create-mode order are dropped in silence while the stack
	// builds a fresh VPC. Accepting that reads as "portal understood me".
	t.Run("adopt block on a create order", func(t *testing.T) {
		in := validOrder()
		in.Network = &Network{Mode: "create", Adopt: &NetworkAdopt{VpcID: "vpc-0abc"}}

		if err := in.Validate(); err == nil {
			t.Fatal("a create-mode order carrying adopt IDs was accepted")
		}
	})

	t.Run("create block on an adopt order", func(t *testing.T) {
		in := validOrder()
		in.Network = &Network{
			Mode:   "adopt",
			Create: &NetworkCreate{VpcCidr: "10.4.0.0/16"},
			Adopt: &NetworkAdopt{
				VpcID:     "vpc-0abc",
				SubnetIDs: &NetworkSubnets{Private: []string{"subnet-0priv"}},
			},
		}

		if err := in.Validate(); err == nil {
			t.Fatal("an adopt-mode order carrying create levers was accepted")
		}
	})
}

func TestValidate_AdoptNeedsAVpcAndPrivateSubnets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		adopt *NetworkAdopt
	}{
		{"no adopt block", nil},
		{"no vpc", &NetworkAdopt{SubnetIDs: &NetworkSubnets{Private: []string{"subnet-0priv"}}}},
		{"no subnets object", &NetworkAdopt{VpcID: "vpc-0abc"}},
		{"empty private list", &NetworkAdopt{VpcID: "vpc-0abc", SubnetIDs: &NetworkSubnets{Public: []string{"subnet-0pub"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validOrder()
			in.Network = &Network{Mode: "adopt", Adopt: tc.adopt}

			if err := in.Validate(); err == nil {
				t.Fatal("an adopt-mode order with nowhere to put nodes was accepted")
			}
		})
	}
}

func TestValidate_AdoptWithPrivateSubnetsIsAccepted(t *testing.T) {
	in := validOrder()
	in.Network = &Network{
		Mode: "adopt",
		Adopt: &NetworkAdopt{
			VpcID:     "vpc-0abc",
			SubnetIDs: &NetworkSubnets{Private: []string{"subnet-0priv1", "subnet-0priv2"}},
		},
	}

	if err := in.Validate(); err != nil {
		t.Fatalf("a complete adopt order was rejected: %v", err)
	}
}

func TestValidate_CreateModeLevers(t *testing.T) {
	yes, no := true, false
	n := func(i int) *int { return &i }

	for _, tc := range []struct {
		name    string
		create  NetworkCreate
		wantErr string
	}{
		{"empty is fine", NetworkCreate{}, ""},
		{"a plain CIDR", NetworkCreate{VpcCidr: "10.4.0.0/16"}, ""},
		{"a bad CIDR", NetworkCreate{VpcCidr: "10.4.0.0"}, "vpc_cidr"},
		{"not a CIDR at all", NetworkCreate{VpcCidr: "ten dot four"}, "vpc_cidr"},
		// The exclusion is written against the default: leaving the CIDR alone
		// is how an order says "I did not pick one".
		{"IPAM with the default CIDR", NetworkCreate{IpamPoolID: "ipam-pool-0abc", VpcCidr: defaultVpcCidr}, ""},
		{"IPAM with no CIDR", NetworkCreate{IpamPoolID: "ipam-pool-0abc"}, ""},
		{"IPAM and a chosen CIDR", NetworkCreate{IpamPoolID: "ipam-pool-0abc", VpcCidr: "10.4.0.0/16"}, "mutually exclusive"},
		{"netmask in range", NetworkCreate{IpamPoolID: "ipam-pool-0abc", IpamNetmaskLength: n(18)}, ""},
		{"netmask 0 means literal", NetworkCreate{IpamNetmaskLength: n(0)}, ""},
		{"netmask too short", NetworkCreate{IpamNetmaskLength: n(15)}, "ipam_netmask_length"},
		{"netmask too long", NetworkCreate{IpamNetmaskLength: n(21)}, "ipam_netmask_length"},
		{"centralized egress with a gateway", NetworkCreate{TransitGatewayID: "tgw-0abc", CentralizedEgress: &yes}, ""},
		{"centralized egress with no gateway", NetworkCreate{CentralizedEgress: &yes}, "transit_gateway_id"},
		{"egress explicitly off needs nothing", NetworkCreate{CentralizedEgress: &no}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := validOrder()
			create := tc.create
			in.Network = &Network{Mode: "create", Create: &create}

			err := in.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("rejected a valid create block: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted a create block the XRD would reject")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("message %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidate_CreateModeWithNoCreateBlock(t *testing.T) {
	in := validOrder()
	in.Network = &Network{Mode: "create"}

	if err := in.Validate(); err != nil {
		t.Fatalf("create mode with no levers was rejected: %v", err)
	}
}

func TestWithPortalWiring(t *testing.T) {
	const (
		spoke   = "arn:aws:iam::222222222222:role/production-portal-spoke"
		tenants = "git@github.com:nanohype/tenants.git"
	)

	t.Run("stamps both when unset", func(t *testing.T) {
		got := validOrder().WithPortalWiring(spoke, tenants)

		if got.PortalAccessRoleArn != spoke || got.TenantsRepoURL != tenants {
			t.Errorf("stamped %q / %q", got.PortalAccessRoleArn, got.TenantsRepoURL)
		}
	})

	t.Run("stamps each half independently", func(t *testing.T) {
		// The two are filled at different points on the write path — the spoke
		// role at enqueue, the tenants repo at apply — so each call passes ""
		// for the other half and must leave it alone.
		got := validOrder().WithPortalWiring(spoke, "").WithPortalWiring("", tenants)

		if got.PortalAccessRoleArn != spoke || got.TenantsRepoURL != tenants {
			t.Errorf("a two-stage stamp lost a half: %q / %q", got.PortalAccessRoleArn, got.TenantsRepoURL)
		}
	})

	t.Run("never overwrites an explicit value", func(t *testing.T) {
		in := validOrder()
		in.PortalAccessRoleArn = "arn:aws:iam::222222222222:role/ordered"
		in.TenantsRepoURL = "git@github.com:acme/theirs.git"

		got := in.WithPortalWiring(spoke, tenants)

		if got.PortalAccessRoleArn != in.PortalAccessRoleArn || got.TenantsRepoURL != in.TenantsRepoURL {
			t.Errorf("the stamp overruled the order: %q / %q", got.PortalAccessRoleArn, got.TenantsRepoURL)
		}
	})

	t.Run("stamps nothing when portal has nothing to stamp", func(t *testing.T) {
		got := validOrder().WithPortalWiring("", "")

		if got.PortalAccessRoleArn != "" || got.TenantsRepoURL != "" {
			t.Errorf("invented values from empty inputs: %q / %q", got.PortalAccessRoleArn, got.TenantsRepoURL)
		}
	})
}

func TestValidName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"analytics", true},
		{"a", true},
		{"web-01", true},
		{"Analytics", false},
		{"-leading", false},
		{"trailing-", false},
		{"under_score", false},
		{"", false},
	} {
		if got := ValidName(tc.name); got != tc.want {
			t.Errorf("ValidName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestWithAccountSubstrate(t *testing.T) {
	const (
		vendRole  = "arn:aws:iam::222222222222:role/production-fleet-vend"
		kms       = "arn:aws:kms:us-west-2:222222222222:key/abcd-1234"
		clusterPB = "arn:aws:iam::222222222222:policy/production-cluster-boundary"
		operPB    = "arn:aws:iam::222222222222:policy/production-operator-boundary"
	)

	t.Run("stamps all four when unset", func(t *testing.T) {
		got := validOrder().WithAccountSubstrate(vendRole, kms, clusterPB, operPB)

		if got.VendRoleArn != vendRole || got.DataKmsKeyArn != kms ||
			got.ClusterPermissionsBoundaryArn != clusterPB || got.OperatorPermissionsBoundaryArn != operPB {
			t.Errorf("stamped %q / %q / %q / %q",
				got.VendRoleArn, got.DataKmsKeyArn,
				got.ClusterPermissionsBoundaryArn, got.OperatorPermissionsBoundaryArn)
		}
	})

	// An account registered with only some of these is the ordinary case: an
	// account that vends same-account may still have a data KMS key.
	t.Run("stamps each field independently", func(t *testing.T) {
		got := validOrder().WithAccountSubstrate("", kms, "", "")

		if got.DataKmsKeyArn != kms {
			t.Errorf("did not stamp the one field it was given: %q", got.DataKmsKeyArn)
		}
		if got.VendRoleArn != "" || got.ClusterPermissionsBoundaryArn != "" || got.OperatorPermissionsBoundaryArn != "" {
			t.Errorf("invented a value for an unregistered field: %q / %q / %q",
				got.VendRoleArn, got.ClusterPermissionsBoundaryArn, got.OperatorPermissionsBoundaryArn)
		}
	})

	t.Run("never overwrites an explicit order-level value", func(t *testing.T) {
		in := validOrder()
		in.VendRoleArn = "arn:aws:iam::222222222222:role/ordered"
		in.OperatorPermissionsBoundaryArn = "arn:aws:iam::222222222222:policy/ordered"

		got := in.WithAccountSubstrate(vendRole, kms, clusterPB, operPB)

		if got.VendRoleArn != in.VendRoleArn || got.OperatorPermissionsBoundaryArn != in.OperatorPermissionsBoundaryArn {
			t.Errorf("the account overruled the order: %q / %q", got.VendRoleArn, got.OperatorPermissionsBoundaryArn)
		}
		// The fields the order left alone still take the account's values.
		if got.DataKmsKeyArn != kms || got.ClusterPermissionsBoundaryArn != clusterPB {
			t.Errorf("an override on one field suppressed another: %q / %q", got.DataKmsKeyArn, got.ClusterPermissionsBoundaryArn)
		}
	})

	t.Run("an account with no prerequisites stamps nothing", func(t *testing.T) {
		got := validOrder().WithAccountSubstrate("", "", "", "")

		if got.VendRoleArn != "" || got.DataKmsKeyArn != "" ||
			got.ClusterPermissionsBoundaryArn != "" || got.OperatorPermissionsBoundaryArn != "" {
			t.Error("stamped a value from an account that has none — empty means ungated")
		}
	})

	// The point of the whole change: a cross-account order that would have been
	// refused for missing boundaries now validates from what the account carries.
	t.Run("a cross-account order validates from the account's ARNs alone", func(t *testing.T) {
		bare := validOrder()
		bare.VendRoleArn = vendRole
		if err := bare.Validate(); err == nil {
			t.Fatal("a vend role with no boundaries was accepted; the guard this test relies on is gone")
		}

		stamped := validOrder().WithAccountSubstrate(vendRole, kms, clusterPB, operPB)
		if err := stamped.Validate(); err != nil {
			t.Errorf("a cross-account order still fails after stamping: %v", err)
		}
	})
}
