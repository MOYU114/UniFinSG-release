package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	ModeLocal   = "local"
	ModeCognito = "cognito"
)

type HTTPHandler struct {
	service *Service
	mode    string
	account *AccountHTTPHandler
}

func NewHTTPHandler(service *Service, mode string) (*HTTPHandler, error) {
	if service == nil || service.db == nil {
		return nil, errors.New("authentication service is required")
	}
	if mode == "" {
		mode = ModeLocal
	}
	if mode != ModeLocal && mode != ModeCognito {
		return nil, errors.New("AUTH_MODE must be local or cognito")
	}
	return &HTTPHandler{service: service, mode: mode}, nil
}

// SetAccountHandler binds the application account lifecycle before routes are
// registered. Legacy routers can omit it and retain their existing DELETE.
func (h *HTTPHandler) SetAccountHandler(account *AccountHTTPHandler) {
	h.account = account
}

// RegisterAccountRoutes reports whether it installed the application DELETE,
// allowing the legacy router to avoid registering the same path twice.
func (h *HTTPHandler) RegisterAccountRoutes(api *gin.RouterGroup, afterDelete func(int64)) bool {
	if h.account == nil {
		return false
	}
	h.account.SetAfterLocalDelete(afterDelete)
	h.account.RegisterRoutes(api)
	return true
}

func (h *HTTPHandler) RegisterRoutes(api *gin.RouterGroup) {
	if h.mode == ModeLocal {
		api.POST("/users/register", h.register)
		api.POST("/users/login", h.login)
	} else {
		api.POST("/users/register", closedAuthRoute)
		api.POST("/users/login", closedAuthRoute)
	}
	if h.mode == ModeLocal {
		api.GET("/auth/login", closedAuthRoute)
		api.GET("/auth/callback", closedAuthRoute)
	}
	api.POST("/auth/logout", h.service.APIMiddleware(), h.service.APICSRFMiddleware(), h.logout)
}

func closedAuthRoute(c *gin.Context) {
	WriteAPIError(c, http.StatusNotFound, "not_found", "The requested endpoint is not available.", nil)
}

type localCredentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func decodeCredentials(c *gin.Context) (localCredentials, error) {
	var credentials localCredentials
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&credentials); err != nil {
		return credentials, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return credentials, errors.New("only one JSON object is allowed")
	}
	return credentials, nil
}

func (h *HTTPHandler) register(c *gin.Context) {
	credentials, err := decodeCredentials(c)
	if err != nil {
		WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be valid JSON.", nil)
		return
	}
	user, err := h.service.RegisterLocalUser(c.Request.Context(), credentials.Username, credentials.Password)
	if err != nil {
		var validation *CredentialsValidationError
		switch {
		case errors.As(err, &validation):
			WriteAPIError(c, http.StatusBadRequest, "validation_failed", "The username or password is invalid.", validation.Fields)
		case errors.Is(err, ErrUsernameExists):
			WriteAPIError(c, http.StatusConflict, "conflict", "The username is already registered.", nil)
		default:
			WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Registration is temporarily unavailable.", nil)
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"id": user.ID, "username": user.Username, "created_at": user.CreatedAt,
	})
}

func (h *HTTPHandler) login(c *gin.Context) {
	credentials, err := decodeCredentials(c)
	if err != nil {
		WriteAPIError(c, http.StatusBadRequest, "bad_request", "The request body must be valid JSON.", nil)
		return
	}
	user, err := h.service.AuthenticateLocalUser(c.Request.Context(), credentials.Username, credentials.Password)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			WriteAPIError(c, http.StatusUnauthorized, "unauthenticated", "The username or password is incorrect.", nil)
		} else {
			WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Login is temporarily unavailable.", nil)
		}
		return
	}
	csrfToken, err := h.service.NewCSRFToken()
	if err != nil {
		WriteAPIError(c, http.StatusInternalServerError, "internal", "Unable to create a session.", nil)
		return
	}
	token, err := h.service.IssueToken(c.Request.Context(), user.ID)
	if err != nil {
		WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Login is temporarily unavailable.", nil)
		return
	}
	h.setCookies(c, token, csrfToken, int(h.service.TokenTTL()/time.Second))
	c.JSON(http.StatusOK, gin.H{
		"id": user.ID, "username": user.Username, "created_at": user.CreatedAt, "auth_token": token,
	})
}

func (h *HTTPHandler) logout(c *gin.Context) {
	var logoutURL *string
	if h.mode == ModeCognito {
		value, err := cognitoLogoutURL()
		if err != nil {
			WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Logout is temporarily unavailable.", nil)
			return
		}
		logoutURL = &value
	}
	token, _ := AuthTokenFromContext(c)
	if err := h.service.RevokeToken(c.Request.Context(), token); err != nil {
		WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Logout is temporarily unavailable.", nil)
		return
	}
	h.setCookies(c, "", "", -1)
	c.JSON(http.StatusOK, gin.H{"logout_url": logoutURL})
}

func (h *HTTPHandler) setCookies(c *gin.Context, token, csrf string, maxAge int) {
	secure := gin.Mode() == gin.ReleaseMode
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(h.service.AuthCookieName(), token, maxAge, "/", "", secure, true)
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(h.service.CSRFCookieName(), csrf, maxAge, "/", "", secure, false)
}

func cognitoLogoutURL() (string, error) {
	domainValue := os.Getenv("COGNITO_DOMAIN")
	if domainValue != "" && !strings.Contains(domainValue, "://") {
		domainValue = "https://" + domainValue
	}
	domain, err := url.Parse(domainValue)
	if err != nil || domain.Scheme != "https" || domain.Host == "" || domain.User != nil ||
		(domain.Path != "" && domain.Path != "/") || domain.RawQuery != "" || domain.Fragment != "" {
		return "", errors.New("invalid Cognito domain")
	}
	frontend, err := url.Parse(os.Getenv("FRONTEND_URL"))
	if err != nil || (frontend.Scheme != "http" && frontend.Scheme != "https") ||
		frontend.Host == "" || frontend.User != nil || frontend.RawQuery != "" || frontend.Fragment != "" {
		return "", errors.New("invalid frontend URL")
	}
	clientID := os.Getenv("COGNITO_CLIENT_ID")
	if strings.TrimSpace(clientID) == "" {
		return "", errors.New("Cognito client ID is required")
	}
	frontend.Path = fmt.Sprintf("%s/signin", strings.TrimRight(frontend.Path, "/"))
	domain.Path = "/logout"
	query := url.Values{"client_id": {clientID}, "logout_uri": {frontend.String()}}
	domain.RawQuery = query.Encode()
	return domain.String(), nil
}
