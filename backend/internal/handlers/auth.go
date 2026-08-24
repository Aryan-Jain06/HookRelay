package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aryan-jain06/hookrelay/backend/internal/httpx"
	"github.com/aryan-jain06/hookrelay/backend/internal/repos"
	"github.com/aryan-jain06/hookrelay/backend/internal/services"
)

// AuthHandler serves tenant registration and dashboard login.
type AuthHandler struct {
	auth *services.AuthService
}

// NewAuthHandler builds an AuthHandler.
func NewAuthHandler(auth *services.AuthService) *AuthHandler {
	return &AuthHandler{auth: auth}
}

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Register creates a tenant and returns its one-time API key plus a JWT.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	res, err := h.auth.Register(r.Context(), req.Name, req.Email, req.Password)
	if err != nil {
		if errors.Is(err, repos.ErrConflict) {
			httpx.Error(w, r, httpx.Conflict("a tenant with that email already exists"))
			return
		}
		if errors.Is(err, services.ErrValidation) || isUserInputError(err) {
			httpx.Error(w, r, httpx.BadRequest(err.Error()))
			return
		}
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.JSON(w, http.StatusCreated, res)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// Login exchanges email and password for a dashboard JWT.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(w, r, &req, 8<<10); err != nil {
		httpx.Error(w, r, err)
		return
	}
	tenant, token, err := h.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		if errors.Is(err, services.ErrInvalidCredentials) {
			httpx.Error(w, r, httpx.Unauthorized("invalid email or password"))
			return
		}
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"tenant": tenant, "token": token})
}

// Me returns the authenticated tenant.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{"tenant": TenantFrom(r.Context())})
}

// isUserInputError recognises the plain validation errors the auth service
// returns for a bad email or a short password.
func isUserInputError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "invalid email address") ||
		strings.Contains(msg, "password must be at least")
}

// RotateAPIKey issues a new API key and invalidates the old one.
//
// It deliberately requires the dashboard JWT rather than accepting the API key
// itself: if a key leaks, whoever holds it must not be able to rotate it and
// lock the real owner out. Requiring the password-derived credential means
// rotation always needs something the attacker does not have.
func (h *AuthHandler) RotateAPIKey(w http.ResponseWriter, r *http.Request) {
	if CredentialFrom(r.Context()) != CredentialToken {
		httpx.Error(w, r, httpx.Forbidden("rotating an API key requires a dashboard token; sign in at /auth/login and use that token"))
		return
	}
	tenant := TenantFrom(r.Context())
	key, err := h.auth.RotateAPIKey(r.Context(), tenant.ID)
	if err != nil {
		if errors.Is(err, repos.ErrNotFound) {
			httpx.Error(w, r, httpx.NotFound("tenant not found"))
			return
		}
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"api_key":        key,
		"api_key_prefix": services.APIKeyDisplayPrefix(key),
		"warning":        "the previous API key stopped working immediately; this value is shown only once",
	})
}
