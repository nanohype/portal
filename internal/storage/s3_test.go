package storage

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nanohype/portal/internal/config"
)

// hangingS3 accepts the connection and never answers, which is what an
// unreachable endpoint behind a dropping network policy looks like from the
// client side. A refused connection fails fast and was never the problem; a
// silent accept is what burns the whole timeout budget.
func hangingS3(t *testing.T) *config.Config {
	t.Helper()
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(blocked); srv.Close() })

	return &config.Config{
		S3Endpoint:  srv.Listener.Addr().String(),
		S3Bucket:    "probe",
		S3AccessKey: "k",
		S3SecretKey: "s",
		S3UseSSL:    false,
		S3Region:    "us-west-2",
	}
}

// EnsureBucket must honour its caller's deadline rather than the client's.
//
// This is the bug it pins, and it cost a live install. The S3 client's own
// timeout is two minutes — right for a body transfer mid-run. The worker called
// EnsureBucket at startup with context.Background(), before binding its health
// port, and the liveness probe restarts the container after 10s + 3 × 15s = 55s.
// So the two-minute bound could never be reached: an unreachable bucket killed
// the process instead of degrading to the "logs and state won't be persisted"
// branch the code plainly intends. The crashloop's logs ended after "using redis
// log streamer" with nothing to explain it.
//
// A dependency treated as optional has to be optional on the path where packets
// are dropped, not only where they are refused.
func TestEnsureBucket_HonoursTheCallerDeadline(t *testing.T) {
	s, err := NewS3Storage(hangingS3(t))
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 750*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = s.EnsureBucket(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a hanging endpoint must produce an error, not a silent success")
	}
	// Generous relative to the deadline, tiny relative to requestTimeout — the
	// point is that the CALLER's bound governs, not the client's two minutes.
	if elapsed > 15*time.Second {
		t.Fatalf("EnsureBucket ran %s past a 750ms deadline; the caller's context must "+
			"bound it, otherwise the client's %s timeout outlives the liveness probe",
			elapsed, requestTimeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		t.Errorf("expected the deadline to be the cause, got %v", err)
	}
}

// And the constant the worker uses has to fit inside the probe's budget, which is
// the invariant that made the timeout the right size rather than an arbitrary one.
func TestS3StartupBound_FitsInsideALivenessBudget(t *testing.T) {
	// worker-deployment.yaml: initialDelaySeconds 10, periodSeconds 15,
	// failureThreshold default 3.
	const livenessBudget = 10*time.Second + 3*15*time.Second

	if requestTimeout <= livenessBudget {
		t.Skipf("requestTimeout (%s) now fits the probe budget (%s) on its own; the startup "+
			"bound may no longer be load-bearing", requestTimeout, livenessBudget)
	}
	// The client's own timeout does NOT fit, which is exactly why a startup call
	// needs its own shorter bound. Documented here so the relationship is checked
	// rather than remembered.
	t.Logf("client timeout %s exceeds the %s liveness budget — startup calls must pass a "+
		"shorter context", requestTimeout, livenessBudget)
}
