package demoseed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAccountPlan(t *testing.T) {
	accounts := Accounts()
	seen := map[string]bool{}
	for _, a := range accounts {
		if seen[a.Email] {
			t.Fatalf("duplicate account %s", a.Email)
		}
		seen[a.Email] = true
		if a.Email != strings.ToLower(a.Email) || strings.ContainsAny(a.Email, "áéíóöőúüű") {
			t.Fatalf("email %q is not lowercase ASCII", a.Email)
		}
	}
	// bvsc and kise: admin + 2 managers + 4 coaches + 10 players + 10 parents.
	// fuzy: admin + manager/coach + 10 players + 10 parents. Plus the site admin.
	if want := 27*2 + 22 + 1; len(accounts) != want {
		t.Fatalf("accounts = %d, want %d", len(accounts), want)
	}
	for _, email := range []string{"bvsc@example.com", "kise-coach-4@example.com", "fuzy-manager-1@example.com", "fuzy-parent-10@example.com", SiteAdminEmail} {
		if !seen[email] {
			t.Errorf("missing account %s", email)
		}
	}
	if accounts[len(accounts)-1].Email != SiteAdminEmail || !accounts[len(accounts)-1].SiteAdmin {
		t.Error("the site admin must be the last account")
	}
}

func TestLayoutTeams(t *testing.T) {
	groups := Layout()
	if len(groups) != 3 {
		t.Fatalf("groups = %d", len(groups))
	}
	for _, g := range groups {
		roles := map[string]Member{}
		for _, m := range g.Members {
			roles[m.Email] = m
		}
		for _, team := range g.Teams {
			if len(team.Players) != len(team.Parents) {
				t.Errorf("%s: players and parents differ", team.Name)
			}
			if !contains(team.Coaches, team.Head) {
				t.Errorf("%s: head %s is not a coach of the team", team.Name, team.Head)
			}
			for _, c := range team.Coaches {
				if !hasRole(roles[c], RoleCoach) {
					t.Errorf("%s: %s lacks the coach role", team.Name, c)
				}
			}
		}
	}
	fuzy := groups[2]
	if len(fuzy.Teams) != 1 || len(fuzy.Teams[0].Players) != 10 {
		t.Fatalf("fuzy teams = %+v", fuzy.Teams)
	}
	manager := fuzy.Members[1]
	if !hasRole(manager, RoleManager) || !hasRole(manager, RoleCoach) {
		t.Errorf("fuzy manager roles = %v", manager.Roles)
	}
	if bvsc := groups[0]; len(bvsc.Teams) != 2 || bvsc.Teams[1].Players[0] != "bvsc-player-6@example.com" || bvsc.Teams[1].Coaches[0] != "bvsc-coach-3@example.com" {
		t.Errorf("bvsc teams = %+v", bvsc.Teams)
	}
}

func hasRole(m Member, role string) bool { return contains(m.Roles, role) }

func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}

func TestSeasonCoversToday(t *testing.T) {
	for _, today := range []time.Time{
		time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC),
		time.Date(2027, 3, 3, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC),
	} {
		s := Season(today)
		day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
		if day.Before(s.Starts) || day.After(s.Ends) {
			t.Errorf("%v outside season %v..%v", today, s.Starts, s.Ends)
		}
		if !s.Part1Ends.Before(s.Part2From) || s.Part2From.Sub(s.Part1Ends) != 24*time.Hour {
			t.Errorf("parts are not adjacent: %v %v", s.Part1Ends, s.Part2From)
		}
	}
	if got := Season(time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)).Name; got != "2026/27" {
		t.Errorf("name = %q", got)
	}
}

func TestEventSpecsHavePastAndUpcoming(t *testing.T) {
	past, upcoming := 0, 0
	for _, e := range EventSpecs() {
		if e.DayOffset < 0 {
			past++
		} else {
			upcoming++
		}
	}
	if past < 3 || upcoming < 3 {
		t.Errorf("past=%d upcoming=%d", past, upcoming)
	}
}

func TestAnswer(t *testing.T) {
	status, attended := Answer(EventSpec{DayOffset: -4, Kind: "training"}, 0, 0)
	if status != "signed_up" || attended == nil || !*attended {
		t.Errorf("past answer = %q %v", status, attended)
	}
	status, attended = Answer(EventSpec{DayOffset: 5, Nominated: true}, 5, 1)
	if status != "nominated" || attended != nil {
		t.Errorf("nominated answer = %q %v", status, attended)
	}
	if status, attended = Answer(EventSpec{DayOffset: 5, Nominated: true}, 5, 8); status != "" || attended != nil {
		t.Errorf("unnominated answer = %q %v", status, attended)
	}
}

func TestCheckTarget(t *testing.T) {
	tests := []struct {
		url, env string
		name     string
		refused  bool
	}{
		{"postgres://u:p@localhost:55432/calbooker_a?sslmode=disable", "development", "calbooker_a", false},
		{"postgres://u:p@127.0.0.1/calbooker", "test", "calbooker", false},
		{"postgres://u:p@localhost/calbooker", "production", "", true},
		{"postgres://u:p@db.example.com/calbooker", "development", "", true},
		{"postgres://u:p@localhost/goweb", "development", "", true},
		{"postgres://u:p@localhost", "development", "", true},
		{"not a url", "development", "", true},
	}
	for _, tt := range tests {
		name, err := CheckTarget(tt.url, tt.env)
		if tt.refused != errors.Is(err, ErrRefused) || name != tt.name {
			t.Errorf("CheckTarget(%q, %q) = %q, %v", tt.url, tt.env, name, err)
		}
	}
	got, err := GowebDatabaseURL("postgres://u:p@localhost:55432/calbooker_a?sslmode=disable")
	if err != nil || got != "postgres://u:p@localhost:55432/goweb?sslmode=disable" {
		t.Errorf("GowebDatabaseURL = %q, %v", got, err)
	}
}

func TestEnsureAccountsSkipsExisting(t *testing.T) {
	accounts := []Account{{Email: "a@example.com"}, {Email: "b@example.com"}, {Email: SiteAdminEmail, SiteAdmin: true}}
	existing := map[string]string{"a@example.com": "1"}
	var signedUp []string
	lookup := func(context.Context, []string) (map[string]string, error) {
		out := map[string]string{}
		for k, v := range existing {
			out[k] = v
		}
		return out, nil
	}
	signUp := func(_ context.Context, email string) error {
		signedUp = append(signedUp, email)
		existing[email] = "2"
		return nil
	}
	report := func(string, ...any) {}
	subjects, err := EnsureAccounts(context.Background(), accounts, lookup, signUp, report)
	if err != nil || len(signedUp) != 1 || signedUp[0] != "b@example.com" {
		t.Fatalf("first run: %v, signed up %v", err, signedUp)
	}
	if subjects["a@example.com"] != "1" || subjects["b@example.com"] != "2" || subjects[SiteAdminEmail] != "" {
		t.Errorf("subjects = %v", subjects)
	}
	signedUp = nil
	if _, err := EnsureAccounts(context.Background(), accounts, lookup, signUp, report); err != nil || len(signedUp) != 0 {
		t.Errorf("second run signed up %v, err %v", signedUp, err)
	}
}

func TestProviderSignUpRetriesWhenLimited(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`<input type="hidden" name="_csrf" value="tok">`))
			return
		}
		_ = r.ParseForm()
		if r.Form.Get("_csrf") != "tok" || r.Form.Get("email") != "a@example.com" || r.Form.Get("password") != "pw" {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		posts++
		if posts == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		http.Redirect(w, r, "/en/home", http.StatusSeeOther)
	}))
	defer server.Close()
	var waits []time.Duration
	p := &Provider{BaseURL: server.URL, Backoff: time.Second, Sleep: func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}}
	if err := p.SignUp(context.Background(), "a@example.com", "pw"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if posts != 2 || len(waits) != 1 || waits[0] != time.Second {
		t.Errorf("posts=%d waits=%v", posts, waits)
	}
}

func TestProviderSignUpRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`<input type="hidden" name="_csrf" value="tok">`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer server.Close()
	p := &Provider{BaseURL: server.URL}
	if err := p.SignUp(context.Background(), "a@example.com", "pw"); !errors.Is(err, ErrSignUpRejected) {
		t.Errorf("SignUp() error = %v, want ErrSignUpRejected", err)
	}
}

func TestProviderConfirmFollowsMailLink(t *testing.T) {
	confirmed := false
	var provider *httptest.Server
	mail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/search"):
			_, _ = w.Write([]byte(`{"messages":[{"ID":"m1"}]}`))
		case r.URL.Path == "/api/v1/message/m1":
			_, _ = w.Write([]byte(`{"Text":"Confirm: ` + provider.URL + `/en/confirm-email?token=abc thanks"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer mail.Close()
	provider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		confirmed = r.URL.Path == "/en/confirm-email" && r.URL.Query().Get("token") == "abc"
	}))
	defer provider.Close()
	p := &Provider{BaseURL: provider.URL, MailpitURL: mail.URL}
	ok, err := p.Confirm(context.Background(), "a@example.com")
	if err != nil || !ok || !confirmed {
		t.Errorf("Confirm() = %v, %v, confirmed=%v", ok, err, confirmed)
	}
}
