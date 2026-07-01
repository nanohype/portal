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

	"github.com/oklog/ulid/v2"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"

	"github.com/nanohype/portal/internal/auth"
	"github.com/nanohype/portal/internal/config"
	"github.com/nanohype/portal/internal/handler/respond"
	"github.com/nanohype/portal/internal/service"
)

type AuthHandler struct {
	cfg         *config.Config
	userSvc     *service.UserService
	jwt         *auth.JWTAuth
	oauthConfig *oauth2.Config
}

func NewAuthHandler(cfg *config.Config, userSvc *service.UserService, jwt *auth.JWTAuth) *AuthHandler {
	return &AuthHandler{
		cfg:     cfg,
		userSvc: userSvc,
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
	// Clear the state cookie. Mirror the security attributes of the cookie set
	// in GitHubLogin (Secure outside dev, SameSite=Lax) so the clearing cookie
	// carries the same flags the browser expects.
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.Environment != "development",
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
	userReq, err := http.NewRequestWithContext(ghCtx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to build user request")
		return
	}
	resp, err := client.Do(userReq)
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

	// Enforce GitHub org membership when configured. Without this, a GitHub
	// OAuth App admits any GitHub account that completes the flow — the read:org
	// scope is requested but unused. With ALLOWED_GITHUB_ORG set, only active
	// members of that org may log in (and thus only a member can become the
	// bootstrap owner).
	if h.cfg.AllowedGitHubOrg != "" {
		member, err := h.isActiveOrgMember(ghCtx, client, h.cfg.AllowedGitHubOrg)
		if err != nil {
			slog.Error("failed to verify github org membership", "error", err, "org", h.cfg.AllowedGitHubOrg)
			respond.Error(w, http.StatusInternalServerError, "failed to verify organization membership")
			return
		}
		if !member {
			respond.Error(w, http.StatusForbidden, "access is restricted to members of the allowed GitHub organization")
			return
		}
	}

	// Provision the verified identity (default org bootstrap, role assignment,
	// upsert keyed on the GitHub account).
	user, err := h.userSvc.Provision(ctx, service.ProvisionUserParams{
		Email:     ghUser.Email,
		Name:      ghUser.Name,
		AvatarURL: ghUser.AvatarURL,
		GitHubID:  &ghUser.ID,
	})
	if err != nil {
		slog.Error("failed to provision user", "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to setup user")
		return
	}

	// Generate JWT
	jwtToken, err := h.jwt.GenerateToken(user.ID, user.OrgID, user.Email, user.Role)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	h.redirectWithToken(w, r, jwtToken)
}

// redirectWithToken hands the freshly minted JWT to the SPA callback page via
// a short-lived, path-scoped cookie and redirects there. The token never
// appears in a URL, so it can't land in browser history, proxy logs, or
// Referer headers. The cookie is readable by the SPA (not HttpOnly), scoped
// to the callback route, and expires in 60 seconds; the callback page reads
// it once, stores it, and deletes it.
func (h *AuthHandler) redirectWithToken(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/auth/callback",
		MaxAge:   60,
		SameSite: http.SameSiteLaxMode,
		Secure:   h.cfg.Environment != "development",
	})
	http.Redirect(w, r, h.cfg.WebURL+"/auth/callback", http.StatusTemporaryRedirect)
}

func (h *AuthHandler) DevLogin(w http.ResponseWriter, r *http.Request) {
	if h.cfg.Environment != "development" {
		respond.Error(w, http.StatusNotFound, "not found")
		return
	}

	user, err := h.userSvc.Provision(r.Context(), service.ProvisionUserParams{
		Email: "dev@portal.local",
		Name:  "Dev User",
	})
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to setup user")
		return
	}

	token, err := h.jwt.GenerateToken(user.ID, user.OrgID, user.Email, user.Role)
	if err != nil {
		respond.Error(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	h.redirectWithToken(w, r, token)
}

func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	userCtx := auth.GetUser(r.Context())
	if userCtx == nil {
		respond.Error(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	user, err := h.userSvc.Get(r.Context(), userCtx.UserID)
	if err != nil {
		respond.FromError(w, r, err)
		return
	}

	respond.JSON(w, http.StatusOK, user)
}

type githubEmail struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// isActiveOrgMember reports whether the authenticated user (carried by client's
// read:org-scoped token) is an active member of org. It calls
// GET /user/memberships/orgs/{org}: 200 + state "active" means a confirmed
// member; 403/404 means not a member; anything else is an error so a transient
// GitHub failure fails closed (login is denied, not silently granted).
func (h *AuthHandler) isActiveOrgMember(ctx context.Context, client *http.Client, org string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("https://api.github.com/user/memberships/orgs/%s", org), nil)
	if err != nil {
		return false, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var m struct {
			State string `json:"state"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			return false, err
		}
		return m.State == "active", nil
	case http.StatusForbidden, http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("github membership check returned status %d", resp.StatusCode)
	}
}

func (h *AuthHandler) fetchPrimaryEmail(ctx context.Context, client *http.Client) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user/emails", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
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
