package executor

import "fmt"

// unsupportedOperation is what an executor returns rather than running nothing.
//
// Both executors return it, so an operation one of them cannot render is a
// failure and not a difference in behaviour between them. A run that executed
// nothing must not be recorded as having succeeded, which is what a dispatch
// with a silent fall-through produced: an import rendered no commands, the pod
// exited 0, and the worker wrote the run back as applied.
//
// The vocabulary itself is the Postgres `run_operation` enum, declared and grown
// under migrations/, which CLAUDE.md makes its one source of truth. The tests
// read it from there; nothing here restates it.
func unsupportedOperation(operation string) error {
	return fmt.Errorf("unknown operation %q: this executor renders no commands for it. "+
		"Check the operation on the run, and add a case for it to both executors if it is one portal should run", operation)
}
