package tracing

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// HTTPMiddleware starts a server span per request, joined to any incoming W3C
// trace context, and names it by chi's route pattern. The pattern is read
// AFTER next.ServeHTTP — the same ordering metrics.Middleware relies on, which
// sidesteps the otelhttp+chi footgun where RoutePattern() is empty at span
// creation and span names blow up to per-path cardinality. Mount it inside the
// chi router (so the RouteContext is populated).
func HTTPMiddleware(next http.Handler) http.Handler {
	tracer := otel.Tracer("portal/http")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := tracer.Start(ctx, r.Method, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
		r = r.WithContext(ctx)
		next.ServeHTTP(ww, r)

		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		span.SetName(r.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.request.method", r.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
		)
		if status >= 500 {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
	})
}
