package aws

import (
	"errors"
	"testing"

	"github.com/aws/smithy-go"
)

// apiError is a stand-in for an AWS smithy.APIError with a given code + message.
type apiError struct{ code, msg string }

func (e apiError) Error() string                 { return e.code + ": " + e.msg }
func (e apiError) ErrorCode() string             { return e.code }
func (e apiError) ErrorMessage() string          { return e.msg }
func (e apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultUnknown }

func TestClassifyDeleteError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string // "nil" | "dependency" | "fatal"
	}{
		{"nil is success", nil, "nil"},

		{"eks not found", apiError{"ResourceNotFoundException", "no cluster"}, "nil"},
		{"iam not found", apiError{"NoSuchEntity", "no role"}, "nil"},
		{"sqs not found", apiError{"AWS.SimpleQueueService.NonExistentQueue", ""}, "nil"},
		{"elbv2 not found", apiError{"LoadBalancerNotFound", ""}, "nil"},
		{"ec2 vpc not found (suffix)", apiError{"InvalidVpcID.NotFound", ""}, "nil"},
		{"ec2 group not found (suffix)", apiError{"InvalidGroup.NotFound", ""}, "nil"},

		{"ec2 dependency violation", apiError{"DependencyViolation", "has dependencies"}, "dependency"},
		{"eks in use", apiError{"ResourceInUseException", "nodegroups exist"}, "dependency"},
		{"iam delete conflict", apiError{"DeleteConflict", "attached policies"}, "dependency"},
		{"asg has instances", apiError{"ResourceInUseFault", "group has instances"}, "dependency"},
		{"eks has children", apiError{"InvalidParameterException", "cluster has nodegroups"}, "dependency"},

		{"asg validationerror not found → gone", apiError{"ValidationError", "AutoScalingGroup name not found"}, "nil"},
		{"asg validationerror does not exist → gone", apiError{"ValidationError", "Group does not exist"}, "nil"},
		{"validationerror, real problem → fatal", apiError{"ValidationError", "invalid min size"}, "fatal"},

		{"access denied is fatal", apiError{"AccessDenied", "no perms"}, "fatal"},
		{"access denied is not masked by a not-found message", apiError{"AccessDenied", "role not found"}, "fatal"},
		{"a plain error is fatal", errors.New("connection reset"), "fatal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyDeleteError(tt.err)
			var kind string
			switch {
			case got == nil:
				kind = "nil"
			case isDependencyError(got):
				kind = "dependency"
			default:
				kind = "fatal"
			}
			if kind != tt.want {
				t.Errorf("classify(%v) = %s, want %s", tt.err, kind, tt.want)
			}
			// A fatal classification must return the original error, not a copy.
			if tt.want == "fatal" && !errors.Is(got, tt.err) {
				t.Errorf("fatal must pass the original error through, got %v", got)
			}
		})
	}
}
