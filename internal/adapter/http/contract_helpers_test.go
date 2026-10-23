package httpadapter

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tewecske/calbooker/internal/locale"
)

// contractRequest builds an HTTP request with explicit request encoding, so
// contract tests exercise the same form and header boundary the browser uses.
type contractRequest struct {
	method  string
	target  string
	form    url.Values
	headers http.Header
	cookies []*http.Cookie
}

// newContractRequest starts a request with a method and target path.
func newContractRequest(method, target string) *contractRequest {
	return &contractRequest{method: method, target: target, headers: make(http.Header)}
}

// withForm replaces the encoded form body.
func (r *contractRequest) withForm(values url.Values) *contractRequest {
	r.form = values
	return r
}

// withFormValue adds one encoded form field.
func (r *contractRequest) withFormValue(key, value string) *contractRequest {
	if r.form == nil {
		r.form = url.Values{}
	}
	r.form.Set(key, value)
	return r
}

// withHTMX marks the request as an in-page HTMX request.
func (r *contractRequest) withHTMX() *contractRequest {
	return r.withHeader("HX-Request", "true")
}

// withHeader sets one request header.
func (r *contractRequest) withHeader(key, value string) *contractRequest {
	r.headers.Set(key, value)
	return r
}

// withCookie attaches a browser cookie.
func (r *contractRequest) withCookie(cookie *http.Cookie) *contractRequest {
	r.cookies = append(r.cookies, cookie)
	return r
}

// build encodes the request, including application/x-www-form-urlencoded bodies.
func (r *contractRequest) build() *http.Request {
	var body io.Reader
	if r.form != nil {
		body = strings.NewReader(r.form.Encode())
	}
	request := httptest.NewRequest(r.method, r.target, body)
	if r.form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for key, values := range r.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	for _, cookie := range r.cookies {
		request.AddCookie(cookie)
	}
	return request
}

// contractResponse wraps a recorded response with contract assertions.
type contractResponse struct {
	recorder *httptest.ResponseRecorder
}

// serveContract runs one encoded request through handler and records it.
func serveContract(t *testing.T, handler http.Handler, request *contractRequest) *contractResponse {
	t.Helper()
	if handler == nil {
		t.Fatal("serveContract: nil handler")
	}
	if request == nil {
		t.Fatal("serveContract: nil request")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request.build())
	return &contractResponse{recorder: recorder}
}

func (r *contractResponse) status() int              { return r.recorder.Code }
func (r *contractResponse) body() string             { return r.recorder.Body.String() }
func (r *contractResponse) header(key string) string { return r.recorder.Header().Get(key) }

// checkStatus verifies an exact HTTP status code.
func checkStatus(response *httptest.ResponseRecorder, want int) error {
	if response.Code != want {
		return fmt.Errorf("status = %d, want %d", response.Code, want)
	}
	return nil
}

// checkEncoding verifies the response Content-Type encoding.
func checkEncoding(response *httptest.ResponseRecorder, want string) error {
	if got := response.Header().Get("Content-Type"); got != want {
		return fmt.Errorf("content type = %q, want %q", got, want)
	}
	return nil
}

// checkRedirect verifies a browser redirect through status and Location.
func checkRedirect(response *httptest.ResponseRecorder, wantStatus int, wantLocation string) error {
	if response.Code != wantStatus {
		return fmt.Errorf("status = %d, want %d", response.Code, wantStatus)
	}
	if got := response.Header().Get("Location"); got != wantLocation {
		return fmt.Errorf("Location = %q, want %q", got, wantLocation)
	}
	return nil
}

// checkHTMXRedirect verifies an HTMX full-page redirect instruction.
func checkHTMXRedirect(response *httptest.ResponseRecorder, wantLocation string) error {
	if response.Code != http.StatusOK {
		return fmt.Errorf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("HX-Redirect"); got != wantLocation {
		return fmt.Errorf("HX-Redirect = %q, want %q", got, wantLocation)
	}
	if response.Header().Get("Location") != "" {
		return fmt.Errorf("HTMX redirect also set Location = %q", response.Header().Get("Location"))
	}
	return nil
}

// checkEmptyBody verifies an empty response payload.
func checkEmptyBody(response *httptest.ResponseRecorder) error {
	if response.Body.Len() != 0 {
		return fmt.Errorf("body = %q, want empty", response.Body.String())
	}
	return nil
}

// checkFullDocument verifies a complete HTML document with one visible heading.
func checkFullDocument(response *httptest.ResponseRecorder) error {
	if err := checkEncoding(response, "text/html; charset=utf-8"); err != nil {
		return err
	}
	body := response.Body.String()
	if !strings.Contains(body, "<!doctype html>") {
		return fmt.Errorf("full document missing doctype")
	}
	if !strings.Contains(body, `id="page-heading"`) {
		return fmt.Errorf("full document missing accessible heading")
	}
	return nil
}

// checkFragment verifies an accessible HTMX fragment without a document shell.
func checkFragment(response *httptest.ResponseRecorder) error {
	if err := checkEncoding(response, "text/html; charset=utf-8"); err != nil {
		return err
	}
	if response.Header().Get("Vary") != "HX-Request" {
		return fmt.Errorf("Vary = %q, want HX-Request", response.Header().Get("Vary"))
	}
	body := response.Body.String()
	if strings.Contains(body, "<!doctype html>") {
		return fmt.Errorf("fragment contains a full document shell")
	}
	if !strings.Contains(body, `id="page-heading"`) {
		return fmt.Errorf("fragment missing accessible heading")
	}
	if !strings.Contains(body, `id="alerts"`) {
		return fmt.Errorf("fragment missing out-of-band alerts region")
	}
	return nil
}

// checkCatalogMessage resolves a stable catalog identifier and verifies its
// rendered translation appears, keeping tests pinned to message ids rather than
// duplicated copy. It also fails when the raw identifier leaks into the page.
func checkCatalogMessage(catalogs *locale.Catalogs, language locale.Code, id, body string) error {
	if catalogs == nil {
		return fmt.Errorf("nil catalogs")
	}
	want, err := catalogs.Translate(language, id, nil)
	if err != nil {
		return fmt.Errorf("translate %s/%s: %w", language, id, err)
	}
	if !strings.Contains(body, want) {
		return fmt.Errorf("body missing %s/%s message %q", language, id, want)
	}
	if strings.Contains(body, id) {
		return fmt.Errorf("body leaked raw message identifier %q", id)
	}
	return nil
}

var contractCatalogsCache *locale.Catalogs

func contractCatalogs(t *testing.T) *locale.Catalogs {
	t.Helper()
	if contractCatalogsCache != nil {
		return contractCatalogsCache
	}
	catalogs, err := locale.LoadEmbeddedCatalogs()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalogs() error = %v, want nil", err)
	}
	contractCatalogsCache = catalogs
	return catalogs
}

// assertStatus fails the test unless the response status matches.
func (r *contractResponse) assertStatus(t *testing.T, want int) *contractResponse {
	t.Helper()
	if err := checkStatus(r.recorder, want); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertHTML fails unless the response is UTF-8 HTML.
func (r *contractResponse) assertHTML(t *testing.T) *contractResponse {
	t.Helper()
	if err := checkEncoding(r.recorder, "text/html; charset=utf-8"); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertRedirect fails unless the response is the expected browser redirect.
func (r *contractResponse) assertRedirect(t *testing.T, wantStatus int, wantLocation string) *contractResponse {
	t.Helper()
	if err := checkRedirect(r.recorder, wantStatus, wantLocation); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertSeeOther fails unless the response redirects to wantLocation.
func (r *contractResponse) assertSeeOther(t *testing.T, wantLocation string) *contractResponse {
	t.Helper()
	return r.assertRedirect(t, http.StatusSeeOther, wantLocation)
}

// assertFound fails unless the response redirects with 302 to wantLocation.
func (r *contractResponse) assertFound(t *testing.T, wantLocation string) *contractResponse {
	t.Helper()
	return r.assertRedirect(t, http.StatusFound, wantLocation)
}

// assertHTMXRedirect fails unless the fragment instructs a full-page redirect.
func (r *contractResponse) assertHTMXRedirect(t *testing.T, wantLocation string) *contractResponse {
	t.Helper()
	if err := checkHTMXRedirect(r.recorder, wantLocation); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertEmpty fails unless the response payload is empty.
func (r *contractResponse) assertEmpty(t *testing.T) *contractResponse {
	t.Helper()
	if err := checkEmptyBody(r.recorder); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertFullDocument fails unless the response is a complete HTML document.
func (r *contractResponse) assertFullDocument(t *testing.T) *contractResponse {
	t.Helper()
	if err := checkFullDocument(r.recorder); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertFragment fails unless the response is an accessible HTMX fragment.
func (r *contractResponse) assertFragment(t *testing.T) *contractResponse {
	t.Helper()
	if err := checkFragment(r.recorder); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertCatalogMessage fails unless the stable catalog message is rendered.
func (r *contractResponse) assertCatalogMessage(t *testing.T, language locale.Code, id string) *contractResponse {
	t.Helper()
	if err := checkCatalogMessage(contractCatalogs(t), language, id, r.body()); err != nil {
		t.Fatalf("%v", err)
	}
	return r
}

// assertBodyContains fails unless the body contains fragment.
func (r *contractResponse) assertBodyContains(t *testing.T, fragment string) *contractResponse {
	t.Helper()
	if !strings.Contains(r.body(), fragment) {
		t.Fatalf("body missing %q", fragment)
	}
	return r
}

// assertBodyAbsent fails when the body contains fragment.
func (r *contractResponse) assertBodyAbsent(t *testing.T, fragment string) *contractResponse {
	t.Helper()
	if strings.Contains(r.body(), fragment) {
		t.Fatalf("body unexpectedly contains %q", fragment)
	}
	return r
}
