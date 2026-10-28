package demoseed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var csrfField = regexp.MustCompile(`name="_csrf"\s+value="([^"]+)"`)

// Provider signs accounts up at goweb's provider the way a person does, and
// confirms their email through Mailpit when the provider sent a message.
type Provider struct {
	// BaseURL is the provider origin, for example http://localhost:8080.
	BaseURL string
	// MailpitURL is Mailpit's origin; empty skips email confirmation.
	MailpitURL string
	// Client is used for every request; nil means a default client.
	Client *http.Client
	// Backoff is the first wait after "too many attempts"; it doubles.
	Backoff time.Duration
	// Attempts bounds the retries per account.
	Attempts int
	// Sleep waits between retries; nil uses time.After.
	Sleep func(context.Context, time.Duration) error
}

// ErrSignUpRejected means the provider refused the sign-up form.
var ErrSignUpRejected = errors.New("provider rejected the sign-up")

func (p *Provider) httpClient() (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	base := p.Client
	if base == nil {
		base = &http.Client{Timeout: 20 * time.Second}
	}
	client := *base
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client, nil
}

func (p *Provider) sleep(ctx context.Context, d time.Duration) error {
	if p.Sleep != nil {
		return p.Sleep(ctx, d)
	}
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SignUp creates one provider account. A rate-limited attempt is retried
// with a growing wait.
func (p *Provider) SignUp(ctx context.Context, email, password string) error {
	attempts := p.Attempts
	if attempts <= 0 {
		attempts = 6
	}
	wait := p.Backoff
	if wait <= 0 {
		wait = 5 * time.Second
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		var limited bool
		limited, err = p.signUpOnce(ctx, email, password)
		if err == nil {
			return nil
		}
		if !limited {
			return err
		}
		if sleepErr := p.sleep(ctx, wait); sleepErr != nil {
			return sleepErr
		}
		wait *= 2
	}
	return err
}

func (p *Provider) signUpOnce(ctx context.Context, email, password string) (limited bool, err error) {
	client, err := p.httpClient()
	if err != nil {
		return false, err
	}
	page, status, err := p.do(ctx, client, http.MethodGet, p.BaseURL+"/en/sign-up", nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusTooManyRequests {
		return true, errors.New("too many attempts loading the sign-up page")
	}
	match := csrfField.FindStringSubmatch(page)
	if status != http.StatusOK || match == nil {
		return false, fmt.Errorf("sign-up page: status %d without a CSRF token", status)
	}
	form := url.Values{"_csrf": {match[1]}, "email": {email}, "password": {password}}
	body, status, err := p.do(ctx, client, http.MethodPost, p.BaseURL+"/en/sign-up", strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	switch {
	case status == http.StatusSeeOther || status == http.StatusFound:
		return false, nil
	case status == http.StatusTooManyRequests || strings.Contains(strings.ToLower(body), "too many attempts"):
		return true, fmt.Errorf("%w: too many attempts", ErrSignUpRejected)
	default:
		return false, fmt.Errorf("%w: status %d for %s", ErrSignUpRejected, status, email)
	}
}

func (p *Provider) do(ctx context.Context, client *http.Client, method, target string, body io.Reader) (string, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return "", 0, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := client.Do(request)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return string(data), response.StatusCode, err
}

var linkPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

// Confirm opens the confirmation link of the newest Mailpit message sent to
// email. It reports false when no message with a provider link exists, which
// is normal when the local provider sends no mail.
func (p *Provider) Confirm(ctx context.Context, email string) (bool, error) {
	if p.MailpitURL == "" {
		return false, nil
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	var search struct {
		Messages []struct {
			ID string `json:"ID"`
		} `json:"messages"`
	}
	if err := getJSON(ctx, client, p.MailpitURL+"/api/v1/search?query="+url.QueryEscape("to:"+email), &search); err != nil {
		return false, err
	}
	if len(search.Messages) == 0 {
		return false, nil
	}
	var message struct {
		Text string `json:"Text"`
	}
	if err := getJSON(ctx, client, p.MailpitURL+"/api/v1/message/"+url.PathEscape(search.Messages[0].ID), &message); err != nil {
		return false, err
	}
	for _, link := range linkPattern.FindAllString(message.Text, -1) {
		if !strings.HasPrefix(link, p.BaseURL) || !strings.Contains(link, "confirm") && !strings.Contains(link, "verify") {
			continue
		}
		plain, err := p.httpClient()
		if err != nil {
			return false, err
		}
		if _, status, err := p.do(ctx, plain, http.MethodGet, link, nil); err != nil {
			return false, err
		} else if status >= 500 {
			return false, fmt.Errorf("confirmation link: status %d", status)
		}
		return true, nil
	}
	return false, nil
}

// LookupSubjects reads goweb user IDs (the SSO subject) by email from goweb's
// database. The query is read-only; missing emails are absent from the result.
func LookupSubjects(ctx context.Context, gowebDB *sql.DB, emails []string) (map[string]string, error) {
	out := make(map[string]string, len(emails))
	for _, email := range emails {
		var id int64
		err := gowebDB.QueryRowContext(ctx, `SELECT id FROM users WHERE email = $1`, email).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("look up %s: %w", email, err)
		}
		out[email] = fmt.Sprintf("%d", id)
	}
	return out, nil
}

// EnsureAccounts signs up every account that has no subject yet, then
// returns the subjects of all of them. lookup reads subjects by email
// (LookupSubjects against goweb's database); signUp creates one account.
// Accounts that exist are never touched, the site administrator is never
// signed up, and the report lists what happened.
func EnsureAccounts(
	ctx context.Context,
	accounts []Account,
	lookup func(context.Context, []string) (map[string]string, error),
	signUp func(context.Context, string) error,
	report func(format string, args ...any),
) (map[string]string, error) {
	emails := make([]string, len(accounts))
	for i, a := range accounts {
		emails[i] = a.Email
	}
	subjects, err := lookup(ctx, emails)
	if err != nil {
		return nil, err
	}
	created := 0
	for _, a := range accounts {
		if subjects[a.Email] != "" {
			continue
		}
		if a.SiteAdmin {
			report("skip %s: not at the provider (it is never signed up here)", a.Email)
			continue
		}
		if err := signUp(ctx, a.Email); err != nil {
			return nil, fmt.Errorf("sign up %s: %w", a.Email, err)
		}
		created++
	}
	if created == 0 {
		report("all %d provider accounts already exist", len(accounts))
		return withoutMissing(subjects, accounts), nil
	}
	report("signed up %d provider accounts", created)
	subjects, err = lookup(ctx, emails)
	if err != nil {
		return nil, err
	}
	return withoutMissing(subjects, accounts), nil
}

func withoutMissing(subjects map[string]string, accounts []Account) map[string]string {
	out := make(map[string]string, len(subjects))
	for _, a := range accounts {
		if subjects[a.Email] != "" {
			out[a.Email] = subjects[a.Email]
		}
	}
	return out
}
