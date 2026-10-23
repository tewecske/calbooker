package locale

import (
	"errors"
	"testing"
)

func TestParsePath(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		expectedCode  Code
		expectedRoute string
		expectedOK    bool
	}{
		{name: "english home", path: "/en/", expectedCode: English, expectedRoute: "/", expectedOK: true},
		{name: "hungarian deep link", path: "/hu/sign-in", expectedCode: Hungarian, expectedRoute: "/sign-in", expectedOK: true},
		{name: "locale without slash", path: "/en", expectedCode: English, expectedRoute: "/", expectedOK: true},
		{name: "root", path: "/", expectedOK: false},
		{name: "unsupported", path: "/fr/", expectedOK: false},
		{name: "duplicate slash", path: "/en//", expectedOK: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, route, ok := ParsePath(test.path)
			if code != test.expectedCode || route != test.expectedRoute || ok != test.expectedOK {
				t.Errorf("ParsePath() = %q, %q, %t; want %q, %q, %t", code, route, ok, test.expectedCode, test.expectedRoute, test.expectedOK)
			}
		})
	}
}

func TestPath(t *testing.T) {
	tests := []struct {
		name  string
		code  Code
		route string
		want  string
		err   error
	}{
		{name: "home", code: English, route: "/", want: "/en/"},
		{name: "deep link", code: Hungarian, route: "/settings?tab=profile", want: "/hu/settings?tab=profile"},
		{name: "unsupported code", code: "fr", route: "/", err: ErrUnsupported},
		{name: "absolute url", code: English, route: "https://example.com", err: ErrInvalidPath},
		{name: "protocol relative url", code: English, route: "//example.com", err: ErrInvalidPath},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Path(test.code, test.route)
			if !errors.Is(err, test.err) {
				t.Fatalf("Path() error = %v, want errors.Is(_, %v)", err, test.err)
			}
			if got != test.want {
				t.Errorf("Path() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name        string
		preferences Preferences
		wantCode    Code
		wantSource  Source
	}{
		{
			name:        "url wins",
			preferences: Preferences{URL: Hungarian, Account: English, Browser: English, AcceptLanguage: "en"},
			wantCode:    Hungarian,
			wantSource:  SourceURL,
		},
		{
			name:        "account wins over browser",
			preferences: Preferences{Account: Hungarian, Browser: English, AcceptLanguage: "en"},
			wantCode:    Hungarian,
			wantSource:  SourceAccount,
		},
		{
			name:        "browser wins over header",
			preferences: Preferences{Browser: Hungarian, AcceptLanguage: "en"},
			wantCode:    Hungarian,
			wantSource:  SourceBrowser,
		},
		{
			name:        "header quality and region",
			preferences: Preferences{AcceptLanguage: "en;q=0.4, hu-HU;q=0.9"},
			wantCode:    Hungarian,
			wantSource:  SourceBrowserLanguage,
		},
		{
			name:        "invalid values fall back",
			preferences: Preferences{URL: "fr", Account: "de", Browser: "es", AcceptLanguage: "fr,de"},
			wantCode:    Default,
			wantSource:  SourceDefault,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, source := Resolve(test.preferences)
			if code != test.wantCode || source != test.wantSource {
				t.Errorf("Resolve() = %q, %q; want %q, %q", code, source, test.wantCode, test.wantSource)
			}
		})
	}
}
