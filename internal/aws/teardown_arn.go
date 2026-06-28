package aws

import "strings"

// parseResourceARN turns a tagged-resource ARN (as the Resource Groups Tagging
// API returns it) into the Resource the teardown engine orders and the Delete
// switch dispatches on. It returns ok=false for ARN shapes the teardown doesn't
// handle, so discovery can skip them rather than guess.
//
// ARN grammar: arn:partition:service:region:account:resource, where the resource
// tail is one of `type/id`, `type:id`, or a bare `id`. Which one — and what the
// delete call needs out of it — varies by service, so each is handled explicitly:
//
//   - ec2          type/id     ID = the id (vpc-…, subnet-…, sg-…)
//   - eks cluster  cluster/name        ID = name
//   - eks child    nodegroup/cluster/name/uuid  ID = "cluster/name" (delete needs both)
//   - iam          role/<path>/name    ID = name (the delete takes the name, path implied)
//   - iam oidc     oidc-provider/host/id  ID = the full ARN (DeleteOpenIDConnectProvider takes it)
//   - logs         log-group:/path     ID = the group name (colon-separated tail)
//   - kms key      key/id      ID = id ;  alias  alias/name  ID = "alias/name"
//   - sqs          queue-name (bare)   ID = name (the URL is resolved at delete time)
//   - events       rule/name or rule/bus/name  ID = the tail after "rule/"
//   - autoscaling  autoScalingGroup:uuid:autoScalingGroupName/name  ID = name
//   - elbv2        loadbalancer/… or targetgroup/…  ID = the full ARN (delete takes it)
func parseResourceARN(arn string) (Resource, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[0] != "arn" {
		return Resource{}, false
	}
	service, region, tail := parts[2], parts[3], parts[5]
	r := Resource{ARN: arn, Service: service, Region: region}

	switch service {
	case "ec2":
		typ, id, ok := splitSlash(tail)
		if !ok {
			return Resource{}, false
		}
		r.Type, r.ID = typ, id
		return r, isKnownEC2Type(typ)

	case "eks":
		typ, rest, ok := splitSlash(tail)
		if !ok {
			return Resource{}, false
		}
		r.Type = typ
		switch typ {
		case "cluster":
			r.ID = rest
		case "nodegroup", "fargateprofile", "addon":
			// nodegroup/<cluster>/<name>/<uuid> — the delete needs cluster + name.
			seg := strings.Split(rest, "/")
			if len(seg) < 2 {
				return Resource{}, false
			}
			r.ID = seg[0] + "/" + seg[1]
		default:
			return Resource{}, false
		}
		return r, true

	case "iam":
		typ, rest, ok := splitSlash(tail)
		if !ok {
			return Resource{}, false
		}
		r.Type = typ
		switch typ {
		case "role", "policy", "instance-profile":
			// strip any path: role/eks-fleet/name -> name
			seg := strings.Split(rest, "/")
			r.ID = seg[len(seg)-1]
		case "oidc-provider":
			r.ID = arn // DeleteOpenIDConnectProvider takes the ARN
		default:
			return Resource{}, false
		}
		return r, true

	case "logs":
		// log-group:/aws/eks/... — colon-separated tail.
		typ, id, ok := splitColon(tail)
		if !ok || typ != "log-group" {
			return Resource{}, false
		}
		r.Type, r.ID = "log-group", id
		return r, true

	case "kms":
		typ, id, ok := splitSlash(tail)
		if !ok {
			return Resource{}, false
		}
		switch typ {
		case "key":
			r.Type, r.ID = "key", id
		case "alias":
			r.Type, r.ID = "alias", "alias/"+id // DeleteAlias takes alias/<name>
		default:
			return Resource{}, false
		}
		return r, true

	case "sqs":
		r.Type, r.ID = "queue", tail // bare queue name
		return r, true

	case "events":
		typ, rest, ok := splitSlash(tail)
		if !ok || typ != "rule" {
			return Resource{}, false
		}
		r.Type, r.ID = "rule", rest // name, or bus/name for a custom bus
		return r, true

	case "autoscaling":
		// autoScalingGroup:<uuid>:autoScalingGroupName/<name>
		i := strings.LastIndex(tail, "/")
		if !strings.HasPrefix(tail, "autoScalingGroup") || i < 0 {
			return Resource{}, false
		}
		r.Type, r.ID = "autoScalingGroup", tail[i+1:]
		return r, true

	case "elasticloadbalancing":
		typ, _, ok := splitSlash(tail)
		if !ok || (typ != "loadbalancer" && typ != "targetgroup") {
			return Resource{}, false
		}
		r.Type, r.ID = typ, arn // delete takes the ARN
		return r, true

	default:
		return Resource{}, false
	}
}

func splitSlash(s string) (head, rest string, ok bool) {
	i := strings.Index(s, "/")
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func splitColon(s string) (head, rest string, ok bool) {
	i := strings.Index(s, ":")
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

func isKnownEC2Type(t string) bool {
	switch t {
	case "vpc", "subnet", "security-group", "route-table", "natgateway",
		"internet-gateway", "egress-only-internet-gateway", "elastic-ip",
		"vpc-endpoint", "launch-template", "network-acl", "network-interface":
		return true
	default:
		return false
	}
}
