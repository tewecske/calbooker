// Package oidc implements the OpenID Connect relying party used by client apps
// to sign in through the single sign-on issuer. It verifies RS256 tokens with
// the standard library only and accepts no other algorithm.
package oidc

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tewecske/calbooker/internal/service"
)

const (
	maxResponseBytes  = 1 << 20
	maxTokenBytes     = 16 << 10
	requestTimeout    = 10 * time.Second
	clockSkew         = time.Minute
	maxLogoutTokenAge = 5 * time.Minute
	minRSAKeyBits     = 2048
	keyRefreshPause   = 30 * time.Second
	scopes            = "openid email profile"
	backChannelEvent  = "http://schemas.openid.net/event/backchannel-logout"
)

var (
	// ErrInvalidConfig identifies unusable client configuration.
	ErrInvalidConfig = errors.New("oidc: invalid configuration")
	// ErrDiscovery identifies an unusable issuer discovery document.
	ErrDiscovery = errors.New("oidc: discovery failed")
	// ErrTokenExchange identifies a failed authorization code redemption.
	ErrTokenExchange = errors.New("oidc: token exchange failed")
	// ErrInvalidToken identifies a token that failed verification.
	ErrInvalidToken = errors.New("oidc: invalid token")
)

// Config configures one relying party registered with the issuer.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// HTTPClient and Now are optional; tests inject them.
	HTTPClient *http.Client
	Now        func() time.Time
}

// Client is the relying party. Discovery and keys are fetched on first use and
// cached; an unknown key ID triggers a rate-limited key refresh.
type Client struct {
	issuer       string
	clientID     string
	clientSecret string
	redirectURL  string
	httpClient   *http.Client
	now          func() time.Time

	mu              sync.Mutex
	metadata        *metadata
	keys            map[string]*rsa.PublicKey
	onlyKey         *rsa.PublicKey // the key set's single usable key, if it has exactly one
	keysRefreshedAt time.Time
}

type metadata struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
	EndSessionEndpoint    string `json:"end_session_endpoint"`
}

var _ service.SSORelyingParty = (*Client)(nil)

// New validates configuration without network access.
func New(config Config) (*Client, error) {
	issuer := strings.TrimSpace(config.Issuer)
	if !absoluteHTTPURL(issuer) || strings.TrimSpace(config.ClientID) == "" || config.ClientSecret == "" || !absoluteHTTPURL(config.RedirectURL) {
		return nil, ErrInvalidConfig
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		issuer:       issuer,
		clientID:     strings.TrimSpace(config.ClientID),
		clientSecret: config.ClientSecret,
		redirectURL:  config.RedirectURL,
		httpClient:   httpClient,
		now:          now,
	}, nil
}

// AuthorizationURL builds the issuer authorization request with PKCE S256. The
// nonce is the code challenge: it is unique to this request, and Exchange can
// recompute it from the stored code verifier, so no extra state is stored.
func (c *Client) AuthorizationURL(ctx context.Context, state, codeChallenge string) (string, error) {
	meta, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	endpoint, _ := url.Parse(meta.AuthorizationEndpoint)
	query := endpoint.Query()
	query.Set("response_type", "code")
	query.Set("client_id", c.clientID)
	query.Set("redirect_uri", c.redirectURL)
	query.Set("scope", scopes)
	query.Set("state", state)
	query.Set("nonce", codeChallenge)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// Exchange redeems an authorization code and returns the identity from the
// verified ID token.
func (c *Client) Exchange(ctx context.Context, code, codeVerifier string) (service.SSOIdentity, error) {
	meta, err := c.discover(ctx)
	if err != nil {
		return service.SSOIdentity{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.redirectURL},
		"code_verifier": {codeVerifier},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, meta.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return service.SSOIdentity{}, ErrTokenExchange
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	// RFC 6749 section 2.3.1: form-encode the credentials before Basic auth.
	request.SetBasicAuth(url.QueryEscape(c.clientID), url.QueryEscape(c.clientSecret))
	var payload struct {
		IDToken string `json:"id_token"`
	}
	if err := c.doJSON(request, &payload); err != nil {
		return service.SSOIdentity{}, fmt.Errorf("%w: %w", ErrTokenExchange, err)
	}
	claims, err := c.verifyIDToken(ctx, payload.IDToken)
	if err != nil {
		return service.SSOIdentity{}, err
	}
	// OpenID Connect Core 3.1.3.7: the nonce sent in the request must come back.
	challenge := sha256.Sum256([]byte(codeVerifier))
	if claims.Nonce == nil || *claims.Nonce != base64.RawURLEncoding.EncodeToString(challenge[:]) {
		return service.SSOIdentity{}, fmt.Errorf("%w: nonce mismatch", ErrInvalidToken)
	}
	return service.SSOIdentity{
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
		Locale:        claims.Locale,
		SessionID:     claims.SessionID,
		IDToken:       payload.IDToken,
	}, nil
}

// VerifyLogoutToken applies OpenID Connect Back-Channel Logout 1.0 section 2.6
// and returns the session ID to log out.
func (c *Client) VerifyLogoutToken(ctx context.Context, raw string) (string, error) {
	claims, err := c.verify(ctx, raw)
	if err != nil {
		return "", err
	}
	now := c.now()
	issuedAt := time.Unix(claims.IssuedAt, 0)
	event, hasEvent := claims.Events[backChannelEvent]
	switch {
	case claims.IssuedAt == 0 || issuedAt.After(now.Add(clockSkew)) || issuedAt.Before(now.Add(-maxLogoutTokenAge-clockSkew)):
		return "", fmt.Errorf("%w: logout token issued at an unacceptable time", ErrInvalidToken)
	case claims.Expiry != 0 && !now.Before(time.Unix(claims.Expiry, 0).Add(clockSkew)):
		return "", fmt.Errorf("%w: logout token expired", ErrInvalidToken)
	case strings.TrimSpace(claims.JWTID) == "":
		return "", fmt.Errorf("%w: logout token without jti", ErrInvalidToken)
	case !hasEvent || !jsonObject(event):
		return "", fmt.Errorf("%w: logout token without the back-channel event", ErrInvalidToken)
	case claims.Nonce != nil:
		return "", fmt.Errorf("%w: logout token with a nonce", ErrInvalidToken)
	case strings.TrimSpace(claims.SessionID) == "":
		return "", fmt.Errorf("%w: logout token without sid", ErrInvalidToken)
	}
	return claims.SessionID, nil
}

// EndSessionURL builds the issuer logout URL. Hydra requires the ID token hint
// whenever a return URL is given.
func (c *Client) EndSessionURL(ctx context.Context, idTokenHint, postLogoutRedirect string) (string, error) {
	meta, err := c.discover(ctx)
	if err != nil {
		return "", err
	}
	if meta.EndSessionEndpoint == "" {
		return "", fmt.Errorf("%w: issuer has no end_session_endpoint", ErrDiscovery)
	}
	endpoint, _ := url.Parse(meta.EndSessionEndpoint)
	query := endpoint.Query()
	query.Set("id_token_hint", idTokenHint)
	query.Set("post_logout_redirect_uri", postLogoutRedirect)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

// String keeps the client secret out of formatted diagnostics.
func (c *Client) String() string {
	if c == nil {
		return "oidc.Client{}"
	}
	return fmt.Sprintf("oidc.Client{issuer:%q client_id_configured:%t}", c.issuer, c.clientID != "")
}

// GoString keeps %#v diagnostics redacted too.
func (c *Client) GoString() string { return c.String() }

type claims struct {
	Issuer          string                     `json:"iss"`
	Subject         string                     `json:"sub"`
	Audience        audience                   `json:"aud"`
	AuthorizedParty string                     `json:"azp"`
	Expiry          int64                      `json:"exp"`
	IssuedAt        int64                      `json:"iat"`
	JWTID           string                     `json:"jti"`
	SessionID       string                     `json:"sid"`
	Nonce           *string                    `json:"nonce"`
	Events          map[string]json.RawMessage `json:"events"`
	Email           string                     `json:"email"`
	EmailVerified   bool                       `json:"email_verified"`
	Name            string                     `json:"name"`
	Locale          string                     `json:"locale"`
}

func (c *Client) verifyIDToken(ctx context.Context, raw string) (claims, error) {
	verified, err := c.verify(ctx, raw)
	if err != nil {
		return claims{}, err
	}
	now := c.now()
	switch {
	case strings.TrimSpace(verified.Subject) == "":
		return claims{}, fmt.Errorf("%w: ID token without sub", ErrInvalidToken)
	case verified.Expiry == 0 || !now.Before(time.Unix(verified.Expiry, 0).Add(clockSkew)):
		return claims{}, fmt.Errorf("%w: ID token expired", ErrInvalidToken)
	case verified.IssuedAt == 0 || time.Unix(verified.IssuedAt, 0).After(now.Add(clockSkew)):
		return claims{}, fmt.Errorf("%w: ID token issued in the future", ErrInvalidToken)
	case len(verified.Audience) > 1 && verified.AuthorizedParty != c.clientID:
		return claims{}, fmt.Errorf("%w: ID token for several audiences without matching azp", ErrInvalidToken)
	case verified.AuthorizedParty != "" && verified.AuthorizedParty != c.clientID:
		return claims{}, fmt.Errorf("%w: ID token azp is another client", ErrInvalidToken)
	case verified.Events != nil:
		// A logout token must never be accepted as an ID token.
		return claims{}, fmt.Errorf("%w: token carries events", ErrInvalidToken)
	}
	return verified, nil
}

// verify checks the signature, issuer, and audience shared by both token types.
func (c *Client) verify(ctx context.Context, raw string) (claims, error) {
	if raw == "" || len(raw) > maxTokenBytes {
		return claims{}, fmt.Errorf("%w: missing or oversized token", ErrInvalidToken)
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return claims{}, fmt.Errorf("%w: malformed token", ErrInvalidToken)
	}
	var header struct {
		Algorithm string          `json:"alg"`
		KeyID     string          `json:"kid"`
		Critical  json.RawMessage `json:"crit"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return claims{}, fmt.Errorf("%w: malformed header", ErrInvalidToken)
	}
	if header.Algorithm != "RS256" || header.Critical != nil {
		return claims{}, fmt.Errorf("%w: unsupported header", ErrInvalidToken)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims{}, fmt.Errorf("%w: malformed signature", ErrInvalidToken)
	}
	key, err := c.key(ctx, header.KeyID)
	if err != nil {
		return claims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return claims{}, fmt.Errorf("%w: bad signature", ErrInvalidToken)
	}
	var verified claims
	if err := decodeSegment(parts[1], &verified); err != nil {
		return claims{}, fmt.Errorf("%w: malformed claims", ErrInvalidToken)
	}
	if verified.Issuer != c.issuer {
		return claims{}, fmt.Errorf("%w: wrong issuer", ErrInvalidToken)
	}
	if !verified.Audience.contains(c.clientID) {
		return claims{}, fmt.Errorf("%w: wrong audience", ErrInvalidToken)
	}
	return verified, nil
}

func (c *Client) discover(ctx context.Context) (metadata, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.metadata != nil {
		return *c.metadata, nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return metadata{}, ErrDiscovery
	}
	var meta metadata
	if err := c.doJSON(request, &meta); err != nil {
		return metadata{}, fmt.Errorf("%w: %w", ErrDiscovery, err)
	}
	// The issuer must match exactly so tokens from another issuer can never
	// pass the iss check.
	if meta.Issuer != c.issuer || !absoluteHTTPURL(meta.AuthorizationEndpoint) || !absoluteHTTPURL(meta.TokenEndpoint) || !absoluteHTTPURL(meta.JWKSURI) || (meta.EndSessionEndpoint != "" && !absoluteHTTPURL(meta.EndSessionEndpoint)) {
		return metadata{}, fmt.Errorf("%w: unusable document", ErrDiscovery)
	}
	c.metadata = &meta
	return meta, nil
}

// key returns the signing key for keyID, refreshing the key set when the ID
// is unknown, at most once per keyRefreshPause. A token without a kid header
// is verified with the issuer's only key; a set with several keys cannot say
// which one signed it, so such a token is rejected.
func (c *Client) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	meta, err := c.discover(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if key, ok := c.lookup(keyID); ok {
		return key, nil
	}
	if !c.keysRefreshedAt.IsZero() && c.now().Sub(c.keysRefreshedAt) < keyRefreshPause {
		return nil, fmt.Errorf("%w: unknown key", ErrInvalidToken)
	}
	c.keysRefreshedAt = c.now()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, meta.JWKSURI, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: key set request", ErrInvalidToken)
	}
	var set struct {
		Keys []struct {
			KeyType   string `json:"kty"`
			KeyID     string `json:"kid"`
			Use       string `json:"use"`
			Algorithm string `json:"alg"`
			Modulus   string `json:"n"`
			Exponent  string `json:"e"`
		} `json:"keys"`
	}
	if err := c.doJSON(request, &set); err != nil {
		return nil, fmt.Errorf("%w: key set unavailable", ErrInvalidToken)
	}
	keys := map[string]*rsa.PublicKey{}
	usable := 0
	var last *rsa.PublicKey
	for _, jwk := range set.Keys {
		if jwk.KeyType != "RSA" || (jwk.Use != "" && jwk.Use != "sig") || (jwk.Algorithm != "" && jwk.Algorithm != "RS256") {
			continue
		}
		key, err := rsaKey(jwk.Modulus, jwk.Exponent)
		if err != nil {
			continue
		}
		usable++
		last = key
		if jwk.KeyID != "" {
			keys[jwk.KeyID] = key
		}
	}
	c.keys = keys
	c.onlyKey = nil
	if usable == 1 {
		c.onlyKey = last
	}
	if key, ok := c.lookup(keyID); ok {
		return key, nil
	}
	return nil, fmt.Errorf("%w: unknown key", ErrInvalidToken)
}

// lookup finds a key by kid. An empty kid matches only a key set with exactly
// one usable key. Call with c.mu held.
func (c *Client) lookup(keyID string) (*rsa.PublicKey, bool) {
	if keyID == "" {
		return c.onlyKey, c.onlyKey != nil
	}
	key, ok := c.keys[keyID]
	return key, ok
}

func rsaKey(modulus, exponent string) (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, err
	}
	e, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("bad exponent")
	}
	key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	if key.N.BitLen() < minRSAKeyBits || key.E < 3 || key.E%2 == 0 {
		return nil, errors.New("weak key")
	}
	return key, nil
}

func (c *Client) doJSON(request *http.Request, out any) error {
	response, err := c.httpClient.Do(request)
	if err != nil {
		return errors.New("request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("status %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(out); err != nil {
		return errors.New("malformed response")
	}
	return nil
}

func decodeSegment(segment string, out any) error {
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// audience accepts the single-string and array forms of "aud".
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audience{single}
		return nil
	}
	var many []string
	if err := json.Unmarshal(data, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

func (a audience) contains(clientID string) bool {
	for _, value := range a {
		if value == clientID {
			return true
		}
	}
	return false
}

func jsonObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) >= 2 && trimmed[0] == '{'
}

func absoluteHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && parsed.Host != "" && parsed.User == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
