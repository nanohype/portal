package tracing

import (
	"context"
	"encoding/json"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// traceCarrierKeys are the W3C trace-context keys carried through a job's
// Metadata so a run reads as one trace from the request that enqueued it.
var traceCarrierKeys = []string{"traceparent", "tracestate"}

// JobInsertMiddleware injects the current span's trace context into each job's
// Metadata at insert time, so a job enqueued inside an HTTP request (or another
// job) continues that trace. It MERGES into Metadata — River keeps its own keys
// there (uniqueness, recorded output) which must be preserved.
func JobInsertMiddleware() rivertype.JobInsertMiddleware {
	return river.JobInsertMiddlewareFunc(func(ctx context.Context, manyParams []*rivertype.JobInsertParams, doInner func(context.Context) ([]*rivertype.JobInsertResult, error)) ([]*rivertype.JobInsertResult, error) {
		carrier := propagation.MapCarrier{}
		otel.GetTextMapPropagator().Inject(ctx, carrier)
		if len(carrier) > 0 {
			for _, p := range manyParams {
				p.Metadata = mergeTraceCarrier(p.Metadata, carrier)
			}
		}
		return doInner(ctx)
	})
}

// WorkerMiddleware runs each job under a span that continues the trace its
// Metadata carries, so HTTP → enqueue → run is one trace. The per-kind manual
// spans on the hot paths (executor.Execute) hang off this root.
func WorkerMiddleware() rivertype.WorkerMiddleware {
	tracer := otel.Tracer("portal/worker")
	return river.WorkerMiddlewareFunc(func(ctx context.Context, job *rivertype.JobRow, doInner func(context.Context) error) error {
		if carrier := traceCarrierFromMetadata(job.Metadata); carrier != nil {
			ctx = otel.GetTextMapPropagator().Extract(ctx, carrier)
		}
		ctx, span := tracer.Start(ctx, "job "+job.Kind, trace.WithSpanKind(trace.SpanKindConsumer))
		defer span.End()
		span.SetAttributes(
			attribute.String("river.job.kind", job.Kind),
			attribute.Int64("river.job.id", job.ID),
			attribute.Int("river.job.attempt", job.Attempt),
		)
		err := doInner(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	})
}

// mergeTraceCarrier adds the W3C keys to a job's Metadata JSON object without
// disturbing River's own keys. A nil/empty Metadata becomes a fresh object.
func mergeTraceCarrier(metadata []byte, carrier propagation.MapCarrier) []byte {
	m := map[string]json.RawMessage{}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &m); err != nil {
			// Don't clobber metadata we can't parse — skip injection.
			return metadata
		}
	}
	for _, k := range traceCarrierKeys {
		if v := carrier.Get(k); v != "" {
			if b, err := json.Marshal(v); err == nil {
				m[k] = b
			}
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return metadata
	}
	return out
}

// traceCarrierFromMetadata reads the W3C keys back out, returning nil when the
// job carries no trace context (so the worker starts a fresh root).
func traceCarrierFromMetadata(metadata []byte) propagation.MapCarrier {
	if len(metadata) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &m); err != nil {
		return nil
	}
	carrier := propagation.MapCarrier{}
	found := false
	for _, k := range traceCarrierKeys {
		if raw, ok := m[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				carrier[k] = s
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	return carrier
}
