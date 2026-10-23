package oidc

import (
	"context"
	"crypto"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	keyOnce               sync.Once
	signingKey, rotateKey *rsa.PrivateKey
	weakKey               *rsa.PrivateKey
)

func testKeys(t *testing.T) {
	t.Helper()
	keyOnce.Do(func() {
		signingKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		rotateKey, _ = rsa.GenerateKey(rand.Reader, 2048)
		weakKey, _ = rsa.GenerateKey(rand.Reader, 1024)
	})
}

// fakeIssuer serves discovery, a key set, and a token endpoint.
type fakeIssuer struct {
	server     *httptest.Server
	issuer     string
	keys       map[string]*rsa.PublicKey
	mu         sync.Mutex
	jwksHits   atomic.Int32
	idToken    string
	tokenForm  url.Values
	basicUser  string
	basicPass  string
	discovered string // overrides the issuer in discovery when set
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	testKeys(t)
	fake := &fakeIssuer{keys: map[string]*rsa.PublicKey{"k1": &signingKey.PublicKey, "weak": &weakKey.PublicKey}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := fake.issuer
		if fake.discovered != "" {
			issuer = fake.discovered
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"issuer":                 issuer,
			"authorization_endpoint": fake.issuer + "/oauth2/auth?existing=1",
			"token_endpoint":         fake.issuer + "/oauth2/token",
			"jwks_uri":               fake.issuer + "/.well-known/jwks.json",
			"end_session_endpoint":   fake.issuer + "/oauth2/sessions/logout",
		})
	})
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		fake.jwksHits.Add(1)
		fake.mu.Lock()
		defer fake.mu.Unlock()
		var keys []map[string]string
		for kid, key := range fake.keys {
			keys = append(keys, map[string]string{
				"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		fake.tokenForm = r.PostForm
		fake.basicUser, fake.basicPass, _ = r.BasicAuth()
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at", "id_token": fake.idToken})
	})
	fake.server = httptest.NewServer(mux)
	fake.issuer = fake.server.URL
	t.Cleanup(fake.server.Close)
	return fake
}

const (
	testClientID = "app-client"
	testSecret   = "s3cret:with/odd chars"
)

var testNow = time.Unix(1_800_000_000, 0)

func (f *fakeIssuer) client(t *testing.T) *Client {
	t.Helper()
	client, err := New(Config{Issuer: f.issuer, ClientID: testClientID, ClientSecret: testSecret, RedirectURL: "http://127.0.0.1:8081/en/oauth/sso/callback", Now: func() time.Time { return testNow }})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func sign(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// testVerifier is the PKCE verifier of the sign-in under test. The client
// sends its S256 challenge as the nonce, so valid ID tokens echo it.
const testVerifier = "the-verifier"

func testNonce() string {
	digest := sha256.Sum256([]byte(testVerifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (f *fakeIssuer) idClaims() map[string]any {
	return map[string]any{
		"iss": f.issuer, "sub": "42", "aud": []string{testClientID}, "exp": testNow.Add(time.Hour).Unix(), "iat": testNow.Unix(), "nonce": testNonce(),
		"sid": "sid-1", "email": "ada@example.test", "email_verified": true, "name": "Ada", "locale": "hu",
	}
}

func (f *fakeIssuer) logoutClaims() map[string]any {
	return map[string]any{
		"iss": f.issuer, "aud": []string{testClientID}, "iat": testNow.Unix(), "jti": "jti-1", "sid": "sid-1",
		"events": map[string]any{backChannelEvent: map[string]any{}},
	}
}

var rs256 = map[string]any{"alg": "RS256", "kid": "k1", "typ": "JWT"}

func TestExchangeVerifiesTheIDTokenAndAuthenticatesTheClient(t *testing.T) {
	fake := newFakeIssuer(t)
	fake.idToken = sign(t, signingKey, rs256, fake.idClaims())
	identity, err := fake.client(t).Exchange(context.Background(), "the-code", testVerifier)
	if err != nil {
		t.Fatalf("Exchange() error = %v", err)
	}
	if identity.Subject != "42" || identity.SessionID != "sid-1" || identity.Email != "ada@example.test" || !identity.EmailVerified || identity.Name != "Ada" || identity.Locale != "hu" || identity.IDToken != fake.idToken {
		t.Fatalf("identity = %+v", identity)
	}
	// RFC 6749 2.3.1: credentials are form-encoded before Basic auth, so a
	// server that decodes them recovers the exact values.
	user, _ := url.QueryUnescape(fake.basicUser)
	pass, _ := url.QueryUnescape(fake.basicPass)
	if user != testClientID || pass != testSecret {
		t.Fatalf("basic auth = %q/%q, want the form-encoded client credentials", fake.basicUser, fake.basicPass)
	}
	if fake.tokenForm.Get("code") != "the-code" || fake.tokenForm.Get("code_verifier") != "the-verifier" || fake.tokenForm.Get("grant_type") != "authorization_code" || fake.tokenForm.Get("redirect_uri") != "http://127.0.0.1:8081/en/oauth/sso/callback" {
		t.Fatalf("token form = %v", fake.tokenForm)
	}
}

func TestIDTokenRejections(t *testing.T) {
	fake := newFakeIssuer(t)
	with := func(edit func(map[string]any)) map[string]any {
		claims := fake.idClaims()
		edit(claims)
		return claims
	}
	publicDER, _ := x509.MarshalPKIXPublicKey(&signingKey.PublicKey)
	hs256 := func() string {
		h, _ := json.Marshal(map[string]any{"alg": "HS256", "kid": "k1"})
		c, _ := json.Marshal(fake.idClaims())
		input := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
		mac := hmac.New(sha256.New, publicDER)
		mac.Write([]byte(input))
		return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	unsigned := func() string {
		h, _ := json.Marshal(map[string]any{"alg": "none", "kid": "k1"})
		c, _ := json.Marshal(fake.idClaims())
		return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c) + "."
	}
	tampered := func() string {
		parts := strings.Split(sign(t, signingKey, rs256, fake.idClaims()), ".")
		c, _ := json.Marshal(with(func(c map[string]any) { c["sub"] = "1" }))
		return parts[0] + "." + base64.RawURLEncoding.EncodeToString(c) + "." + parts[2]
	}
	tests := map[string]string{
		"alg none":                  unsigned(),
		"HS256 with the public key": hs256(),
		"tampered claims":           tampered(),
		"unknown kid":               sign(t, rotateKey, map[string]any{"alg": "RS256", "kid": "k-unknown"}, fake.idClaims()),
		"weak key":                  sign(t, weakKey, map[string]any{"alg": "RS256", "kid": "weak"}, fake.idClaims()),
		"crit header":               sign(t, signingKey, map[string]any{"alg": "RS256", "kid": "k1", "crit": []string{"exp"}}, fake.idClaims()),
		"wrong issuer":              sign(t, signingKey, rs256, with(func(c map[string]any) { c["iss"] = "http://evil.example.test" })),
		"wrong audience":            sign(t, signingKey, rs256, with(func(c map[string]any) { c["aud"] = "other-client" })),
		"expired":                   sign(t, signingKey, rs256, with(func(c map[string]any) { c["exp"] = testNow.Add(-2 * time.Minute).Unix() })),
		"issued in the future":      sign(t, signingKey, rs256, with(func(c map[string]any) { c["iat"] = testNow.Add(5 * time.Minute).Unix() })),
		"no nonce":                  sign(t, signingKey, rs256, with(func(c map[string]any) { delete(c, "nonce") })),
		"wrong nonce":               sign(t, signingKey, rs256, with(func(c map[string]any) { c["nonce"] = "another-request" })),
		"no subject":                sign(t, signingKey, rs256, with(func(c map[string]any) { delete(c, "sub") })),
		"several audiences no azp":  sign(t, signingKey, rs256, with(func(c map[string]any) { c["aud"] = []string{testClientID, "api"} })),
		"azp of another client":     sign(t, signingKey, rs256, with(func(c map[string]any) { c["azp"] = "other-client" })),
		"logout token as ID token":  sign(t, signingKey, rs256, with(func(c map[string]any) { c["events"] = map[string]any{backChannelEvent: map[string]any{}} })),
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			fake.idToken = token
			if _, err := fake.client(t).Exchange(context.Background(), "code", testVerifier); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Exchange() error = %v, want %v", err, ErrInvalidToken)
			}
		})
	}
	// Within the allowed clock skew the token is still accepted.
	fake.idToken = sign(t, signingKey, rs256, with(func(c map[string]any) {
		c["exp"] = testNow.Add(-30 * time.Second).Unix()
		c["aud"] = []string{testClientID, "api"}
		c["azp"] = testClientID
	}))
	if _, err := fake.client(t).Exchange(context.Background(), "code", testVerifier); err != nil {
		t.Fatalf("Exchange(within skew, azp matches) error = %v", err)
	}
}

func TestIDTokenWithoutKidNeedsASingleKeyInTheSet(t *testing.T) {
	noKid := map[string]any{"alg": "RS256", "typ": "JWT"}
	fake := newFakeIssuer(t)
	fake.idToken = sign(t, signingKey, noKid, fake.idClaims())
	// The default set holds one usable key (the weak key is ignored).
	if _, err := fake.client(t).Exchange(context.Background(), "code", testVerifier); err != nil {
		t.Fatalf("single-key set, no kid: error = %v", err)
	}
	// The one key still has to match the signature.
	fake.idToken = sign(t, rotateKey, noKid, fake.idClaims())
	if _, err := fake.client(t).Exchange(context.Background(), "code", testVerifier); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("single-key set, no kid, other signer: error = %v, want %v", err, ErrInvalidToken)
	}
	// With several keys the token cannot say which one signed it.
	fake.mu.Lock()
	fake.keys["k2"] = &rotateKey.PublicKey
	fake.mu.Unlock()
	fake.idToken = sign(t, signingKey, noKid, fake.idClaims())
	if _, err := fake.client(t).Exchange(context.Background(), "code", testVerifier); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("two-key set, no kid: error = %v, want %v", err, ErrInvalidToken)
	}
}

func TestVerifyLogoutToken(t *testing.T) {
	fake := newFakeIssuer(t)
	client := fake.client(t)
	valid := sign(t, signingKey, rs256, fake.logoutClaims())
	sid, err := client.VerifyLogoutToken(context.Background(), valid)
	if err != nil || sid != "sid-1" {
		t.Fatalf("VerifyLogoutToken(valid) = %q, %v", sid, err)
	}
	with := func(edit func(map[string]any)) string {
		claims := fake.logoutClaims()
		edit(claims)
		return sign(t, signingKey, rs256, claims)
	}
	tests := map[string]string{
		"ID token as logout token": sign(t, signingKey, rs256, fake.idClaims()),
		"event is not an object":   with(func(c map[string]any) { c["events"] = map[string]any{backChannelEvent: "yes"} }),
		"other event only":         with(func(c map[string]any) { c["events"] = map[string]any{"urn:other": map[string]any{}} }),
		"nonce present":            with(func(c map[string]any) { c["nonce"] = "n" }),
		"missing sid":              with(func(c map[string]any) { delete(c, "sid") }),
		"missing jti":              with(func(c map[string]any) { delete(c, "jti") }),
		"too old":                  with(func(c map[string]any) { c["iat"] = testNow.Add(-10 * time.Minute).Unix() }),
		"from the future":          with(func(c map[string]any) { c["iat"] = testNow.Add(5 * time.Minute).Unix() }),
		"expired":                  with(func(c map[string]any) { c["exp"] = testNow.Add(-2 * time.Minute).Unix() }),
		"wrong audience":           with(func(c map[string]any) { c["aud"] = "other-client" }),
		"wrong issuer":             with(func(c map[string]any) { c["iss"] = "http://evil.example.test" }),
		"garbage":                  "not.a.token",
	}
	for name, token := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := client.VerifyLogoutToken(context.Background(), token); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("VerifyLogoutToken() error = %v, want %v", err, ErrInvalidToken)
			}
		})
	}
}

func TestKeyRotationRefreshesAtMostOncePerPause(t *testing.T) {
	fake := newFakeIssuer(t)
	clock := testNow
	client, _ := New(Config{Issuer: fake.issuer, ClientID: testClientID, ClientSecret: testSecret, RedirectURL: "http://127.0.0.1:8081/cb", Now: func() time.Time { return clock }})
	if _, err := client.VerifyLogoutToken(context.Background(), sign(t, signingKey, rs256, fake.logoutClaims())); err != nil {
		t.Fatal(err)
	}
	rotated := sign(t, rotateKey, map[string]any{"alg": "RS256", "kid": "k2"}, fake.logoutClaims())
	if _, err := client.VerifyLogoutToken(context.Background(), rotated); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unpublished key error = %v, want rejection", err)
	}
	fake.mu.Lock()
	fake.keys["k2"] = &rotateKey.PublicKey
	fake.mu.Unlock()
	if _, err := client.VerifyLogoutToken(context.Background(), rotated); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("refresh within the pause error = %v, want rejection without refetching", err)
	}
	if hits := fake.jwksHits.Load(); hits != 1 {
		t.Fatalf("key set fetched %d times within the pause, want 1", hits)
	}
	clock = clock.Add(keyRefreshPause + time.Second)
	claims := fake.logoutClaims()
	claims["iat"] = clock.Unix()
	if _, err := client.VerifyLogoutToken(context.Background(), sign(t, rotateKey, map[string]any{"alg": "RS256", "kid": "k2"}, claims)); err != nil {
		t.Fatalf("rotated key after the pause error = %v", err)
	}
}

func TestDiscoveryRejectsAnotherIssuer(t *testing.T) {
	fake := newFakeIssuer(t)
	fake.discovered = "http://evil.example.test"
	if _, err := fake.client(t).AuthorizationURL(context.Background(), "state", "challenge"); !errors.Is(err, ErrDiscovery) {
		t.Fatalf("AuthorizationURL() error = %v, want %v", err, ErrDiscovery)
	}
}

func TestAuthorizationAndEndSessionURLs(t *testing.T) {
	fake := newFakeIssuer(t)
	client := fake.client(t)
	raw, err := client.AuthorizationURL(context.Background(), "st", "ch")
	if err != nil {
		t.Fatal(err)
	}
	authorization, _ := url.Parse(raw)
	query := authorization.Query()
	for key, want := range map[string]string{"existing": "1", "response_type": "code", "client_id": testClientID, "scope": "openid email profile", "state": "st", "nonce": "ch", "code_challenge": "ch", "code_challenge_method": "S256", "redirect_uri": "http://127.0.0.1:8081/en/oauth/sso/callback"} {
		if query.Get(key) != want {
			t.Errorf("authorization %s = %q, want %q", key, query.Get(key), want)
		}
	}
	raw, err = client.EndSessionURL(context.Background(), "the.id.token", "http://127.0.0.1:8081/en/sign-in")
	if err != nil {
		t.Fatal(err)
	}
	end, _ := url.Parse(raw)
	if end.Path != "/oauth2/sessions/logout" || end.Query().Get("id_token_hint") != "the.id.token" || end.Query().Get("post_logout_redirect_uri") != "http://127.0.0.1:8081/en/sign-in" {
		t.Fatalf("end session URL = %s", raw)
	}
}

func TestNewValidatesConfigAndRedactsTheSecret(t *testing.T) {
	for _, config := range []Config{
		{Issuer: "", ClientID: "c", ClientSecret: "s", RedirectURL: "http://a/cb"},
		{Issuer: "http://i", ClientID: "", ClientSecret: "s", RedirectURL: "http://a/cb"},
		{Issuer: "http://i", ClientID: "c", ClientSecret: "", RedirectURL: "http://a/cb"},
		{Issuer: "http://i", ClientID: "c", ClientSecret: "s", RedirectURL: "/cb"},
	} {
		if _, err := New(config); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("New(%+v) error = %v, want %v", config, err, ErrInvalidConfig)
		}
	}
	client, _ := New(Config{Issuer: "http://i", ClientID: "c", ClientSecret: "top-secret-value", RedirectURL: "http://a/cb"})
	for _, formatted := range []string{fmt.Sprint(client), fmt.Sprintf("%#v", client), fmt.Sprintf("%+v", client)} {
		if strings.Contains(formatted, "top-secret-value") {
			t.Fatalf("formatted client leaked the secret: %s", formatted)
		}
	}
}
