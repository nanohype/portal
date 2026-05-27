package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/domain"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/repository"
)

type AuthHandler struct {
	cfg         *domain.Config
	queries     *repository.Queries
	db          *pgxpool.Pool
	jwt         *auth.JWTAuth
	oauthConfig *oauth2.Config
}

func NewAuthHandler(cfg *domain.Config, queries *repository.Queries, db *pgxpool.Pool, jwt *auth.JWTAuth) *AuthHandler {
	return &AuthHandler{
		cfg:     cfg,
		queries: queries,
		db:      db,
		jwt:     jwt,
		oauthConfig: &oauth2.Config{
			ClientID:     cfg.GitHubClientID,
			ClientSecret: cfg.GitHubClientSecret,
			Scopes:       []string{"user:email", "read:org"},
			Endpoint:     github.Endpoint,
		},
	}
}

func (h *AuthHandler) GitHubLogin(w http.ResponseWriter, r *http.Request) {
	state := ulid.Make().String()

	// Store state in a signed, short-lived cookie for CSRF protection
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state + "." + h.signState(state),
		Path:     "/",
		MaxAge:   600, // 10 minutes
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.Environment != "development",
	})

	url := h.oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOnline)
	http.Redirect(w, r, url, http.StatusTemporaryRedirect)
}

// signState produces an HMAC-SHA256 signature of the state value using the JWT secret.
func (h *AuthHandler) signState(state string) string {
	mac := hmac.New(sha256.New, []byte(h.cfg.JWTSecret))
	mac.Write([]byte(state))
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyState checks the state parameter against the signed cookie.
func (h *AuthHandler) verifyState(r *http.Request, state string) bool {
	cookie, err := r.Cookie("oauth_state")
	if err != nil {
		return false
	}
	// Cookie value is "state.signature"
	parts := splitStateCookie(cookie.Value)
	if len(parts) != 2 {
		return false
	}
	cookieState, sig := parts[0], parts[1]
	if cookieState != state {
		return false
	}
	return hmac.Equal([]byte(sig), []byte(h.signState(cookieState)))
}

func splitStateCookie(val string) []string {
	// Split on last dot (ULID doesn't contain dots)
	for i := len(val) - 1; i >= 0; i-- {
		if val[i] == '.' {
			return []string{val[:i], val[i+1:]}
		}
	}
	return nil
}

type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

func (h *AuthHandler) GitHubCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	code := r.URL.Query().Get("code")
	if code == "" {
		respond.Error(w, http.StatusBadRequest, "missing code parameter")
		return
	}

	// Validate state parameter against the signed cookie (CSRF protection)
	state := r.URL.Query().Get("state")
	if !h.verifyState(r, state) {
		respond.Error(w, http.StatusBadRequest, "invalid or missing state parameter")
		return
	}
	// Clear the state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	token, err := h.oauthConfig.Exchange(ctx, code)
	if err != nil {
		slog.Error("oauth exchange failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "OAuth exchange failed")
		return
	}

	// Fetch GitHub user info (with timeout)
	ghCtx, ghCancel := context.WithTimeout(ctx, 10*time.Second)
	defer ghCancel()
	client := h.oauthConfig.Client(ghCtx, token)
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		slog.Error("failed to fetch github user", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to fetch user info")
		return
	}
	defer resp.Body.Close()

	var ghUser githubUser
	if err := json.NewDecoder(resp.Body).Decode(&ghUser); err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to decode user info")
		return
	}

	if ghUser.Email == "" {
		// Fetch primary email (use the same timeout context)
		email, err := h.fetchPrimaryEmail(ghCtx, client)
		if err != nil {
			slog.Error("failed to fetch github email", "error", err)
			respond.Error(w, http.StatusInternalServerError, "failed to fetch user email")
			return
		}
		ghUser.Email = email
	}

	if ghUser.Name == "" {
		ghUser.Name = ghUser.Login
	}

	// Get or create default org (single-org mode for Phase 1)
	org, err := h.ensureDefaultOrg(ctx)
	if err != nil {
		slog.Error("failed to ensure default org", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to setup organization")
		return
	}

	// Determine role: first user in org gets owner, subsequent get viewer
	userCount, err := h.queries.CountUsersByOrg(ctx, org.ID)
	if err != nil {
		slog.Error("failed to count users", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to setup user")
		return
	}
	role := assignRole(userCount)

	// Upsert user
	user, err := h.queries.UpsertUserByGitHubID(ctx, repository.UpsertUserByGitHubIDParams{
		ID:        ulid.Make().String(),
		OrgID:     org.ID,
		Email:     ghUser.Email,
		Name:      ghUser.Name,
		AvatarURL: ghUser.AvatarURL,
		GithubID:  &ghUser.ID,
		Role:      role,
	})
	if err != nil {
		slog.Error("failed to upsert user", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to create user")
		return
	}

	// Generate JWT
	jwtToken, err := h.jwt.GenerateToken(user.ID, user.OrgID, user.Email, user.Role)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	// Redirect to frontend with token
	redirectURL := fmt.Sprintf("%s/auth/callback?token=%s", h.cfg.WebURL, jwtToken)
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) DevLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment != "development" {
		respond.Error(w, http.StatusNotFound, "not found")
		return
	}

	ctx := r.Context()

	org, err := h.ensureDefaultOrg(ctx)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to setup organization")
		return
	}

	userCount, err := h.queries.CountUsersByOrg(ctx, org.ID)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to setup user")
		return
	}
	role := assignRole(userCount)

	user, err := h.queries.UpsertUserByEmail(ctx, repository.UpsertUserByEmailParams{
		ID:        ulid.Make().String(),
		OrgID:     org.ID,
		Email:     "dev@portal.local",
		Name:      "Dev User",
		AvatarURL: "",
		Role:      role,
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to create dev user")
		return
	}

	token, err := h.jwt.GenerateToken(user.ID, user.OrgID, user.Email, user.Role)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	redirectURL := fmt.Sprintf("%s/auth/callback?token=%s", h.cfg.WebURL, token)
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	if userCtx == nil {
		respond.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	user, err := h.queries.GetUser(r.Context(), userCtx.UserID)
	if err != nil {
		respond.Error(w, http.StatusNotFound, "user not found")
		return
	}

	respond.JSON(w, http.StatusOK, user)
}

func (h *AuthHandler) ensureDefaultOrg(ctx context.Context) (repository.Organization, error) {
	org, err := h.queries.GetDefaultOrganization(ctx)
	if err == nil {
		return org, nil
	}

	return h.queries.CreateOrganization(ctx, repository.CreateOrganizationParams{
		ID:   ulid.Make().String(),
		Name: "Default Organization",
		Slug: "default",
	})
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// assignRole returns "owner" for the first user in an org, "viewer" otherwise.
func assignRole(userCount int64) string {
	if userCount == 0 {
		return "owner"
	}
	return "viewer"
}

func (h *AuthHandler) fetchPrimaryEmail(ctx context.Context, client *http.Client) (string, error) {
	resp, err := client.Get("https://api.github.com/user/emails")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var emails []githubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", err
	}

	for _, e := range emails {
		if e.Primary && e.Verified {
			return e.Email, nil
		}
	}

	if len(emails) > 0 {
		return emails[0].Email, nil
	}

	return "", fmt.Errorf("no email found")
}
