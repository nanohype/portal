package worker

import (
	"context"
	"time"
)

// durableTimeout is how long a fail/cleanup path may take after the job
// context has already been cancelled (SIGTERM, River timeout). Long enough
// for a single Postgres write or a K8s Delete; short enough not to hang
// shutdown.
const durableTimeout = 15 * time.Second

// durableContext returns a short-lived context that survives parent
// cancellation. Fail paths and deferred resource cleanup must use this so a
// cancelled job still records "failed" on the operation row and still
// deletes its executor pod/ConfigMap — otherwise pending ops strand forever
// and pods keep applying infrastructure unobserved.
func durableContext(parent context.Context) (context.Context, context.CancelFunc) {
	base := parent
	if base == nil {
		base = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(base), durableTimeout)
}
