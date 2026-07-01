package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/nanohype/portal/internal/handler/respond"
)

type contextKey string

const (
	UserContextKey contextKey = "user"

	// WebSocketBearerProtocol is the subprotocol name WebSocket clients pair
	// with the JWT: the browser requests ["bearer", <jwt>] and the server
	// echoes "bearer" as the selected subprotocol during the upgrade. The
	// browser WebSocket API can't set arbitrary headers, so the
	// Sec-WebSocket-Protocol list is the standard header-based carrier —
	// unlike a query parameter, it never lands in browser history, proxy
	// logs, or Referer headers.
	WebSocketBearerProtocol = "bearer"
)

type UserContext struct {
	UserID string
	OrgID  string
	Email  string
	Role   string
}

type Middleware struct {
	jwt *JWTAuth
}

func NewMiddleware(jwt *JWTAuth) *Middleware {
	return &Middleware{jwt: jwt}
}

func (m *Middleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenString := bearerToken(r)
		if tokenString == "" {
			respond.Error(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}

		claims, err := m.jwt.ValidateToken(tokenString)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "invalid token")
			return
		}

		userCtx := &UserContext{
			UserID: claims.UserID,
			OrgID:  claims.OrgID,
			Email:  claims.Email,
			Role:   claims.Role,
		}

		ctx := context.WithValue(r.Context(), UserContextKey, userCtx)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts the JWT from a request. Regular HTTP requests carry it
// in the Authorization header; WebSocket handshakes carry it as the second
// entry of the Sec-WebSocket-Protocol list (see WebSocketBearerProtocol).
// Tokens never travel in URLs.
func bearerToken(r *http.Request) string {
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		if !strings.HasPrefix(authHeader, "Bearer ") {
			return ""
		}
		return strings.TrimPrefix(authHeader, "Bearer ")
	}
	return websocketBearerToken(r.Header.Get("Sec-WebSocket-Protocol"))
}

// websocketBearerToken parses a Sec-WebSocket-Protocol header of the form
// "bearer, <jwt>" and returns the JWT, or "" when the header doesn't carry
// bearer credentials.
func websocketBearerToken(header string) string {
	protocols := strings.Split(header, ",")
	if len(protocols) < 2 {
		return ""
	}
	if strings.TrimSpace(protocols[0]) != WebSocketBearerProtocol {
		return ""
	}
	return strings.TrimSpace(protocols[1])
}

func GetUser(ctx context.Context) *UserContext {
	u, _ := ctx.Value(UserContextKey).(*UserContext)
	return u
}
