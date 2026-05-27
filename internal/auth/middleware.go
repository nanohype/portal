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
		// Check Authorization header
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// Also check query param for WebSocket connections
			authHeader = "Bearer " + r.URL.Query().Get("token")
		}

		if !strings.HasPrefix(authHeader, "Bearer ") {
			respond.Error(w, http.StatusUnauthorized, "missing or invalid authorization header")
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		if tokenString == "" {
			respond.Error(w, http.StatusUnauthorized, "missing token")
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

func GetUser(ctx context.Context) *UserContext {
	u, _ := ctx.Value(UserContextKey).(*UserContext)
	return u
}
