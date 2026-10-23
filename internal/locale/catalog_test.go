package locale

import (
	"errors"
	"io/fs"
	"strings"
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
				"en/common.json": &fstest.MapFile{Data: []byte(test.data)},
			})
			if !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("LoadCatalogs() error = %v, want errors.Is(_, %v)", err, ErrInvalidCatalog)
			}
		})
	}
}

func catalogFile(data string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(data)}
}

func TestLoadCatalogsMergesAreaFiles(t *testing.T) {
	for _, root := range []string{"", "catalogs/"} {
		t.Run("root "+root, func(t *testing.T) {
			catalogs, err := LoadCatalogs(fstest.MapFS{
				root + "en/common.json": catalogFile(`{"messages":{"home.title":{"text":"Home"}}}`),
				root + "en/groups.json": catalogFile(`{"messages":{"groups.title":{"text":"Groups"}}}`),
				root + "hu/common.json": catalogFile(`{"messages":{"home.title":{"text":"Kezdőlap"}}}`),
				root + "hu/groups.json": catalogFile(`{"messages":{"groups.title":{"text":"Csoportok"}}}`),
				root + "hu/README.txt":  catalogFile(`ignored`),
			})
			if err != nil {
				t.Fatalf("LoadCatalogs() error = %v, want nil", err)
			}
			if err := catalogs.ValidateCompleteness(); err != nil {
				t.Fatalf("ValidateCompleteness() error = %v, want nil", err)
			}
			got, err := catalogs.Translate(Hungarian, "groups.title", nil)
			if err != nil || got != "Csoportok" {
				t.Errorf("Translate(hu, groups.title) = %q, %v; want Csoportok, nil", got, err)
			}
			if keys := catalogs.Keys(English); len(keys) != 2 {
				t.Errorf("Keys(en) = %v, want 2 merged keys", keys)
			}
		})
	}
}

func TestLoadCatalogsRejectsInvalidLayout(t *testing.T) {
	tests := []struct {
		name  string
		files fstest.MapFS
		want  string
	}{
		{
			name: "duplicate key across area files",
			want: "defined in both common.json and groups.json",
			files: fstest.MapFS{
				"en/common.json": catalogFile(`{"messages":{"home.title":{"text":"Home"}}}`),
				"en/groups.json": catalogFile(`{"messages":{"home.title":{"text":"Again"}}}`),
			},
		},
		{
			name: "flat language file",
			want: "must be inside a language directory",
			files: fstest.MapFS{
				"en.json":        catalogFile(`{"messages":{"home.title":{"text":"Home"}}}`),
				"en/common.json": catalogFile(`{"messages":{"home.body":{"text":"Body"}}}`),
			},
		},
		{
			name: "unsupported language directory",
			want: "unsupported language",
			files: fstest.MapFS{
				"en/common.json": catalogFile(`{"messages":{"home.title":{"text":"Home"}}}`),
				"de/common.json": catalogFile(`{"messages":{"home.title":{"text":"Start"}}}`),
			},
		},
		{
			name: "language directory without area files",
			want: "no json area files",
			files: fstest.MapFS{
				"en/common.json": catalogFile(`{"messages":{"home.title":{"text":"Home"}}}`),
				"hu/notes.txt":   catalogFile(`not a catalog`),
			},
		},
		{
			name: "empty area file",
			want: "messages are empty",
			files: fstest.MapFS{
				"en/common.json": catalogFile(`{"messages":{"home.title":{"text":"Home"}}}`),
				"en/groups.json": catalogFile(`{"messages":{}}`),
			},
		},
		{
			name: "missing default language",
			want: "default language",
			files: fstest.MapFS{
				"hu/common.json": catalogFile(`{"messages":{"home.title":{"text":"Kezdőlap"}}}`),
			},
		},
		{
			name:  "no catalogs",
			files: fstest.MapFS{},
			want:  "no json catalogs",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadCatalogs(test.files)
			if !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("LoadCatalogs() error = %v, want errors.Is(_, %v)", err, ErrInvalidCatalog)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Errorf("LoadCatalogs() error = %q, want it to mention %q", err, test.want)
			}
		})
	}
}

// TestEmbeddedCatalogAreasMatch keeps each message in the same area file in
// every language, so a feature's translations stay together.
func TestEmbeddedCatalogAreasMatch(t *testing.T) {
	areas := func(language Code) map[string]map[string]Message {
		t.Helper()
		directory := "catalogs/" + string(language)
		entries, err := fs.ReadDir(embeddedCatalogs, directory)
		if err != nil {
			t.Fatalf("ReadDir(%s) error = %v", directory, err)
		}
		result := map[string]map[string]Message{}
		for _, entry := range entries {
			messages, err := readCatalog(embeddedCatalogs, directory+"/"+entry.Name())
			if err != nil {
				t.Fatalf("readCatalog(%s) error = %v", entry.Name(), err)
			}
			result[entry.Name()] = messages
		}
		return result
	}

	want := areas(Default)
	for _, language := range Codes() {
		got := areas(language)
		if len(got) != len(want) {
			t.Errorf("%s has %d area files, want %d", language, len(got), len(want))
		}
		for area, messages := range want {
			other, ok := got[area]
			if !ok {
				t.Errorf("%s is missing area file %s", language, area)
				continue
			}
			for id := range messages {
				if _, ok := other[id]; !ok {
					t.Errorf("%s/%s is missing message %q", language, area, id)
				}
			}
		}
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
