package auth

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	cognitoFlowCookie = "cognito_flow"
	cognitoFlowTTL    = 10 * time.Minute
)

type CognitoHandler struct {
	service         *Service
	config          CognitoConfig
	client          *http.Client
	now             func() time.Time
	flowCipher      cipher.AEAD
	verifier        *cognitoJWTVerifier
	resolveIdentity func(context.Context, CognitoIdentity) (int64, error)
}

type cognitoFlow struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce"`
	Redirect string `json:"redirect"`
	Expires  int64  `json:"expires"`
}

func NewCognitoHandler(service *Service, config CognitoConfig, options ...CognitoOption) (*CognitoHandler, error) {
	if service == nil || service.db == nil {
		return nil, errors.New("authentication service is required")
	}
	config, err := validateCognitoConfig(config)
	if err != nil {
		return nil, err
	}
	aead, err := cognitoFlowCipher(config.ClientSecret)
	if err != nil {
		return nil, errors.New("cannot configure Cognito flow protection")
	}
	handler := &CognitoHandler{
		service: service, config: config, client: &http.Client{}, now: time.Now, flowCipher: aead,
	}
	for _, option := range options {
		if option != nil {
			option(handler)
		}
	}
	// Never forward authorization codes, verifier values, or client secrets to
	// a redirect target supplied by a token/JWKS response.
	handler.client.Timeout = 10 * time.Second
	handler.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	handler.verifier = &cognitoJWTVerifier{
		issuer:   "https://cognito-idp." + config.Region + ".amazonaws.com/" + config.UserPoolID,
		clientID: config.ClientID, client: handler.client, now: handler.now,
	}
	handler.resolveIdentity = handler.provisionIdentity
	return handler, nil
}

func (handler *CognitoHandler) RegisterRoutes(api *gin.RouterGroup) {
	api.GET("/auth/login", handler.loginCognito)
	api.GET("/auth/callback", handler.cognitoCallback)
}

func (handler *CognitoHandler) loginCognito(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	redirect, valid := safeCognitoRedirect(c.Query("redirect"))
	if !valid {
		WriteAPIError(c, http.StatusBadRequest, "bad_request", "redirect must be a local path beginning with a single slash.", nil)
		return
	}
	state, err := randomCognitoValue()
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	verifier, err := randomCognitoValue()
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	nonce, err := randomCognitoValue()
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	flow := cognitoFlow{
		State: state, Verifier: verifier, Nonce: nonce, Redirect: redirect,
		Expires: handler.now().Add(cognitoFlowTTL).Unix(),
	}
	encrypted, err := handler.encodeFlow(flow)
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	handler.setFlowCookie(c, encrypted, int(cognitoFlowTTL/time.Second))
	digest := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type": {"code"}, "client_id": {handler.config.ClientID},
		"redirect_uri": {handler.config.RedirectURI}, "scope": {"openid email"},
		"state": {state}, "nonce": {nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])},
	}
	c.Header("Location", handler.config.Domain+"/oauth2/authorize?"+query.Encode())
	c.Status(http.StatusFound)
}

func (handler *CognitoHandler) cognitoCallback(c *gin.Context) {
	// Authorization codes and flow state must not be cached or carried in the
	// Referer header when the browser follows the frontend redirect.
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	cookie, cookieErr := c.Cookie(cognitoFlowCookie)
	handler.setFlowCookie(c, "", -1)
	flow, flowErr := handler.decodeFlow(cookie)
	state := c.Query("state")
	if cookieErr != nil || flowErr != nil || state == "" ||
		subtle.ConstantTimeCompare([]byte(flow.State), []byte(state)) != 1 {
		handler.callbackFailure(c, "state_mismatch")
		return
	}
	code := c.Query("code")
	if code == "" || c.Query("error") != "" {
		handler.callbackFailure(c, "token_invalid")
		return
	}
	idToken, err := handler.exchangeCode(c.Request.Context(), code, flow.Verifier)
	if err != nil {
		handler.callbackError(c, err)
		return
	}
	identity, err := handler.verifier.verify(c.Request.Context(), idToken, flow.Nonce)
	if err != nil {
		handler.callbackError(c, err)
		return
	}
	userID, err := handler.resolveIdentity(c.Request.Context(), identity)
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	csrf, err := handler.service.NewCSRFToken()
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	token, err := handler.service.IssueToken(c.Request.Context(), userID)
	if err != nil {
		handler.callbackFailure(c, "cognito_unavailable")
		return
	}
	(&HTTPHandler{service: handler.service}).setCookies(c, token, csrf, int(handler.service.TokenTTL()/time.Second))
	c.Header("Location", handler.config.FrontendURL+flow.Redirect)
	c.Status(http.StatusFound)
}

func (handler *CognitoHandler) exchangeCode(ctx context.Context, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {handler.config.ClientID},
		"code": {code}, "redirect_uri": {handler.config.RedirectURI}, "code_verifier": {verifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, handler.config.Domain+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", ErrCognitoUnavailable
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.SetBasicAuth(handler.config.ClientID, handler.config.ClientSecret)
	response, err := handler.client.Do(request)
	if err != nil {
		return "", ErrCognitoUnavailable
	}
	defer response.Body.Close()
	body, err := readCognitoBody(response.Body, 1<<20)
	if err != nil || response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
		return "", ErrCognitoUnavailable
	}
	if response.StatusCode != http.StatusOK {
		return "", ErrCognitoTokenInvalid
	}
	var result struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.IDToken == "" {
		return "", ErrCognitoTokenInvalid
	}
	return result.IDToken, nil
}

func randomCognitoValue() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (handler *CognitoHandler) encodeFlow(flow cognitoFlow) (string, error) {
	body, err := json.Marshal(flow)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, handler.flowCipher.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := handler.flowCipher.Seal(nonce, nonce, body, []byte(cognitoFlowCookie))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (handler *CognitoHandler) decodeFlow(encoded string) (cognitoFlow, error) {
	var flow cognitoFlow
	if len(encoded) > 4096 {
		return flow, errors.New("invalid flow")
	}
	sealed, err := base64.RawURLEncoding.DecodeString(encoded)
	nonceSize := handler.flowCipher.NonceSize()
	if err != nil || len(sealed) < nonceSize {
		return flow, errors.New("invalid flow")
	}
	body, err := handler.flowCipher.Open(nil, sealed[:nonceSize], sealed[nonceSize:], []byte(cognitoFlowCookie))
	if err != nil || json.Unmarshal(body, &flow) != nil {
		return flow, errors.New("invalid flow")
	}
	_, valid := safeCognitoRedirect(flow.Redirect)
	if flow.Expires <= handler.now().Unix() || flow.Expires > handler.now().Add(cognitoFlowTTL).Unix() ||
		len(flow.State) != 43 || len(flow.Verifier) != 43 || len(flow.Nonce) != 43 || !valid {
		return cognitoFlow{}, errors.New("invalid flow")
	}
	return flow, nil
}

func (handler *CognitoHandler) setFlowCookie(c *gin.Context, value string, maxAge int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(cognitoFlowCookie, value, maxAge, "/api/auth", "", gin.Mode() == gin.ReleaseMode, true)
}

func (handler *CognitoHandler) callbackError(c *gin.Context, err error) {
	if errors.Is(err, ErrCognitoUnavailable) {
		handler.callbackFailure(c, "cognito_unavailable")
	} else {
		handler.callbackFailure(c, "token_invalid")
	}
}

func (handler *CognitoHandler) callbackFailure(c *gin.Context, code string) {
	c.Header("Location", handler.config.FrontendURL+"/signin?"+url.Values{"error": {code}}.Encode())
	c.Status(http.StatusFound)
}
