package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

type APIError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func WriteAPIError(c *gin.Context, status int, code, message string, fields map[string]string) {
	if code != "validation_failed" {
		fields = nil
	}
	c.AbortWithStatusJSON(status, gin.H{
		"error": APIError{Code: code, Message: message, Fields: fields},
	})
}

// APIMiddleware keeps structured errors separate from the legacy endpoints.
func (s *Service) APIMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := s.extractToken(c)
		if token == "" {
			WriteAPIError(c, http.StatusUnauthorized, "unauthenticated", "Authentication is required.", nil)
			return
		}
		userID, err := s.ValidateToken(c.Request.Context(), token)
		if err != nil {
			if errors.Is(err, ErrInvalidToken) {
				WriteAPIError(c, http.StatusUnauthorized, "unauthenticated", "The session is invalid or expired.", nil)
			} else {
				WriteAPIError(c, http.StatusServiceUnavailable, "unavailable", "Authentication is temporarily unavailable.", nil)
			}
			return
		}
		c.Set(userIDContextKey, userID)
		c.Set(authTokenContextKey, token)
		c.Next()
	}
}

func (s *Service) APICSRFMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !requiresCSRFCheck(c.Request.Method) {
			c.Next()
			return
		}
		header := c.GetHeader(s.headerName)
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			token := strings.TrimSpace(header[7:])
			authenticatedToken, ok := AuthTokenFromContext(c)
			if ok && token != "" && token == authenticatedToken {
				c.Next()
				return
			}
		}
		headerToken := c.GetHeader(s.csrfHeaderName)
		cookieToken, err := c.Cookie(s.csrfCookieName)
		if err != nil || headerToken == "" || cookieToken == "" ||
			subtle.ConstantTimeCompare([]byte(headerToken), []byte(cookieToken)) != 1 {
			WriteAPIError(c, http.StatusForbidden, "csrf_invalid", "The CSRF token is missing or invalid.", nil)
			return
		}
		c.Next()
	}
}

func RequirePathUser() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := UserIDFromContext(c)
		if !ok || userID <= 0 {
			WriteAPIError(c, http.StatusUnauthorized, "unauthenticated", "Authentication is required.", nil)
			return
		}
		pathID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || pathID <= 0 {
			WriteAPIError(c, http.StatusBadRequest, "bad_request", "The user ID is invalid.", nil)
			return
		}
		if pathID != userID {
			WriteAPIError(c, http.StatusForbidden, "forbidden", "Access to another user's data is not allowed.", nil)
			return
		}
		c.Next()
	}
}
