package locale

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestLoadEmbeddedCatalogs(t *testing.T) {
	catalogs, err := LoadEmbeddedCatalogs()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalogs() error = %v, want nil", err)
	}

	tests := []struct {
		name     string
		language Code
		id       string
		values   map[string]string
		want     string
	}{
		{name: "english message", language: English, id: "home.heading", want: "Welcome to Calbooker"},
		{name: "hungarian message", language: Hungarian, id: "home.heading", want: "Üdvözli a Calbooker"},
		{name: "fallback message", language: Hungarian, id: "missing.from.hungarian", want: ""},
		{name: "singular plural", language: English, id: "items.count", values: map[string]string{"count": "1"}, want: "1 item"},
		{name: "other plural", language: English, id: "items.count", values: map[string]string{"count": "2"}, want: "2 items"},
	}

	for _, test := range tests[:4] {
		t.Run(test.name, func(t *testing.T) {
			got, err := catalogs.Translate(test.language, test.id, test.values)
			if test.want == "" {
				if !errors.Is(err, ErrMissingMessage) {
					t.Fatalf("Translate() error = %v, want errors.Is(_, %v)", err, ErrMissingMessage)
				}
				return
			}
			if err != nil {
				t.Fatalf("Translate() error = %v, want nil", err)
			}
			if got != test.want {
				t.Errorf("Translate() = %q, want %q", got, test.want)
			}
		})
	}

	got, err := catalogs.Translate(English, "items.count", map[string]string{"count": "2"})
	if err != nil || got != "2 items" {
		t.Errorf("plural translation = %q, %v; want 2 items, nil", got, err)
	}
}

func TestEmbeddedCatalogsAreComplete(t *testing.T) {
	catalogs, err := LoadEmbeddedCatalogs()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalogs() error = %v, want nil", err)
	}
	if err := catalogs.ValidateCompleteness(); err != nil {
		t.Fatalf("ValidateCompleteness() error = %v, want nil", err)
	}
}

func TestCatalogsRejectMissingPlaceholderValue(t *testing.T) {
	catalogs, err := LoadEmbeddedCatalogs()
	if err != nil {
		t.Fatalf("LoadEmbeddedCatalogs() error = %v, want nil", err)
	}
	_, err = catalogs.Translate(English, "items.count", map[string]string{})
	if !errors.Is(err, ErrMissingValue) {
		t.Fatalf("Translate() error = %v, want errors.Is(_, %v)", err, ErrMissingValue)
	}
}

func TestLoadCatalogsRejectsInvalidCatalog(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "malformed json",
			data: `{"messages":`,
		},
		{
			name: "malformed placeholder",
			data: `{"messages":{"home.title":{"text":"Hello {name"}}}`,
		},
		{
			name: "mismatched plural placeholders",
			data: `{"messages":{"items.count":{"one":"{count} item","other":"{total} items"}}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadCatalogs(fstest.MapFS{
				"en.json": &fstest.MapFile{Data: []byte(test.data)},
			})
			if !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("LoadCatalogs() error = %v, want errors.Is(_, %v)", err, ErrInvalidCatalog)
			}
		})
	}
}

func TestValidateCompleteness(t *testing.T) {
	tests := []struct {
		name     string
		messages map[Code]map[string]Message
	}{
		{
			name: "missing language",
			messages: map[Code]map[string]Message{
				English: {"home.title": {Text: "Title"}},
			},
		},
		{
			name: "missing message",
			messages: map[Code]map[string]Message{
				English:   {"home.title": {Text: "Title"}, "home.body": {Text: "Body"}},
				Hungarian: {"home.title": {Text: "Cím"}},
			},
		},
		{
			name: "placeholder mismatch",
			messages: map[Code]map[string]Message{
				English:   {"home.title": {Text: "Hello {name}"}},
				Hungarian: {"home.title": {Text: "Szia {user}"}},
			},
		},
		{
			name: "plural shape mismatch",
			messages: map[Code]map[string]Message{
				English:   {"items.count": {One: "{count} item", Other: "{count} items"}},
				Hungarian: {"items.count": {Text: "{count} elem"}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalogs := &Catalogs{defaultLanguage: Default, messages: test.messages}
			if err := catalogs.ValidateCompleteness(); !errors.Is(err, ErrIncompleteCatalog) {
				t.Fatalf("ValidateCompleteness() error = %v, want errors.Is(_, %v)", err, ErrIncompleteCatalog)
			}
		})
	}
}
