package executor

import "fmt"

// Operations is every operation an executor can be asked to run.
//
// It mirrors the Postgres `run_operation` enum, which is the vocabulary's one
// source of truth (migrations/000001_initial_schema.up.sql). A value added
// there has to be added here, and TestOperationsMatchTheRunOperationEnum fails
// until it is.
//
// An executor is a strategy, and the list exists because a strategy that
// silently handles a subset of its inputs is worse than one that handles none:
// the operation it skipped is reported as having run. The Kubernetes executor
// rendered a script with no operation in it for an `import`, exited 0, and the
// worker wrote the run back as applied — a resource the operator asked portal to
// adopt was never adopted, and nothing in the run said so.
//
// So both executors dispatch over this set and neither may fall through.
var Operations = []string{"plan", "apply", "destroy", "import", "test"}

// unsupportedOperation is what an executor returns rather than running nothing.
// Every executor produces the same error for the same input, so an operation
// missing from one is a failure and not a difference in behaviour between them.
func unsupportedOperation(operation string) error {
	return fmt.Errorf("unknown operation %q: this executor renders no commands for it, and a run that executed nothing must not be recorded as having succeeded", operation)
}
