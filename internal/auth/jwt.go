package auth

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the signed identity portal issues. It answers "who is calling",
// and nothing else: authority is resolved per request from the users table (see
// Middleware.Authenticate), so a role change takes effect immediately instead
// of waiting out the token's lifetime.
type Claims struct {
	UserID string `json:"user_id"`
	OrgID  string `json:"org_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

type JWTAuth struct {
	secret     []byte
	expiration time.Duration
}

func NewJWTAuth(secret string, expiration time.Duration) *JWTAuth {
	return &JWTAuth{
		secret:     []byte(secret),
		expiration: expiration,
	}
}

func (j *JWTAuth) GenerateToken(userID, orgID, email string) (string, error) {
	claims := &Claims{
		UserID: userID,
		OrgID:  orgID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(j.expiration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(j.secret)
}

func (j *JWTAuth) ValidateToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return j.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}
