package locale

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrInvalidCatalog identifies malformed catalog structure or messages.
	ErrInvalidCatalog = errors.New("locale: invalid catalog")
	// ErrMissingMessage identifies an ID absent from requested and fallback catalogs.
	ErrMissingMessage = errors.New("locale: missing message")
	// ErrMissingValue identifies a placeholder without a render value.
	ErrMissingValue = errors.New("locale: missing placeholder value")
	// ErrIncompleteCatalog identifies missing IDs or cross-language shape differences.
	ErrIncompleteCatalog = errors.New("locale: incomplete catalog")
)

// Message contains either one plain translation or one/other plural forms.
type Message struct {
	Text  string `json:"text,omitempty"`
	One   string `json:"one,omitempty"`
	Other string `json:"other,omitempty"`
}

// Catalogs stores validated language catalogs and their fallback language.
type Catalogs struct {
	defaultLanguage Code
	messages        map[Code]map[string]Message
}

//go:embed catalogs/*.json
var embeddedCatalogs embed.FS

// LoadEmbeddedCatalogs loads the catalogs shipped with the application.
func LoadEmbeddedCatalogs() (*Catalogs, error) {
	return LoadCatalogs(embeddedCatalogs)
}

// LoadCatalogs reads and validates JSON catalogs from fileSystem.
func LoadCatalogs(fileSystem fs.FS) (*Catalogs, error) {
	if fileSystem == nil {
		return nil, fmt.Errorf("%w: nil filesystem", ErrInvalidCatalog)
	}
	catalogs := &Catalogs{
		defaultLanguage: Default,
		messages:        map[Code]map[string]Message{},
	}
	entries, err := fs.ReadDir(fileSystem, "catalogs")
	if err != nil {
		entries, err = fs.ReadDir(fileSystem, ".")
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read catalog directory: %v", ErrInvalidCatalog, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		language := Code(strings.TrimSuffix(entry.Name(), ".json"))
		if !Supported(language) {
			return nil, fmt.Errorf("%w: unsupported language %q", ErrInvalidCatalog, language)
		}
		path := entry.Name()
		if _, err := fs.Stat(fileSystem, "catalogs/"+entry.Name()); err == nil {
			path = "catalogs/" + entry.Name()
		}
		catalog, err := readCatalog(fileSystem, path)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", language, err)
		}
		if _, exists := catalogs.messages[language]; exists {
			return nil, fmt.Errorf("%w: duplicate language %q", ErrInvalidCatalog, language)
		}
		catalogs.messages[language] = catalog
	}
	if len(catalogs.messages) == 0 {
		return nil, fmt.Errorf("%w: no json catalogs", ErrInvalidCatalog)
	}
	if _, ok := catalogs.messages[Default]; !ok {
		return nil, fmt.Errorf("%w: default language %q is missing", ErrInvalidCatalog, Default)
	}
	return catalogs, nil
}

// Translate returns a rendered message, falling back to the default language
// when language or message ID is unavailable.
func (c *Catalogs) Translate(language Code, id string, values map[string]string) (string, error) {
	if c == nil || c.messages == nil {
		return "", fmt.Errorf("%w: nil catalogs", ErrInvalidCatalog)
	}
	message, ok := c.message(language, id)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrMissingMessage, id)
	}
	text, err := message.form(values)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", id, err)
	}
	return text, nil
}

// Keys returns sorted message IDs for language, or the default catalog when
// language is unavailable.
func (c *Catalogs) Keys(language Code) []string {
	if c == nil || c.messages == nil {
		return []string{}
	}
	messages, ok := c.messages[language]
	if !ok {
		messages = c.messages[c.defaultLanguage]
	}
	keys := make([]string, 0, len(messages))
	for id := range messages {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	return keys
}

// ValidateCompleteness verifies that every supported language has the same
// message IDs, plural shape, and placeholder names as the default catalog.
func (c *Catalogs) ValidateCompleteness() error {
	if c == nil || c.messages == nil {
		return fmt.Errorf("%w: nil catalogs", ErrIncompleteCatalog)
	}
	defaultMessages, ok := c.messages[c.defaultLanguage]
	if !ok {
		return fmt.Errorf("%w: default language %q is missing", ErrIncompleteCatalog, c.defaultLanguage)
	}
	for _, language := range Codes() {
		messages, ok := c.messages[language]
		if !ok {
			return fmt.Errorf("%w: language %q is missing", ErrIncompleteCatalog, language)
		}
		if len(messages) != len(defaultMessages) {
			return fmt.Errorf("%w: language %q has different message count", ErrIncompleteCatalog, language)
		}
		for id, expected := range defaultMessages {
			actual, ok := messages[id]
			if !ok {
				return fmt.Errorf("%w: language %q is missing message %q", ErrIncompleteCatalog, language, id)
			}
			if err := matchingMessageShape(expected, actual); err != nil {
				return fmt.Errorf("%w: language %q message %q: %w", ErrIncompleteCatalog, language, id, err)
			}
		}
	}
	return nil
}

func readCatalog(fileSystem fs.FS, path string) (map[string]Message, error) {
	file, err := fileSystem.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open file: %v", ErrInvalidCatalog, err)
	}
	defer file.Close()
	var document struct {
		Messages map[string]Message `json:"messages"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: decode json: %v", ErrInvalidCatalog, err)
	}
	if len(document.Messages) == 0 {
		return nil, fmt.Errorf("%w: messages are empty", ErrInvalidCatalog)
	}
	for id, message := range document.Messages {
		if err := validateMessage(id, message); err != nil {
			return nil, err
		}
	}
	return document.Messages, nil
}

func validateMessage(id string, message Message) error {
	if !validMessageID(id) {
		return fmt.Errorf("%w: invalid message ID %q", ErrInvalidCatalog, id)
	}
	plain := message.Text != ""
	plural := message.One != "" || message.Other != ""
	if plain == plural || (plural && (message.One == "" || message.Other == "")) {
		return fmt.Errorf("%w: message %q must contain text or one and other", ErrInvalidCatalog, id)
	}
	if _, err := placeholders(message.Text); err != nil {
		return fmt.Errorf("%w: message %q: %w", ErrInvalidCatalog, id, err)
	}
	if plural {
		one, err := placeholders(message.One)
		if err != nil {
			return fmt.Errorf("%w: message %q: %w", ErrInvalidCatalog, id, err)
		}
		other, err := placeholders(message.Other)
		if err != nil {
			return fmt.Errorf("%w: message %q: %w", ErrInvalidCatalog, id, err)
		}
		if !sameStrings(one, other) {
			return fmt.Errorf("%w: plural placeholders differ for %q", ErrInvalidCatalog, id)
		}
	}
	return nil
}

func matchingMessageShape(expected, actual Message) error {
	if (expected.Text == "") != (actual.Text == "") {
		return errors.New("plain and plural forms differ")
	}
	if expected.Text != "" {
		expectedPlaceholders, err := placeholders(expected.Text)
		if err != nil {
			return err
		}
		actualPlaceholders, err := placeholders(actual.Text)
		if err != nil {
			return err
		}
		if !sameStrings(expectedPlaceholders, actualPlaceholders) {
			return errors.New("placeholders differ")
		}
		return nil
	}
	expectedOne, err := placeholders(expected.One)
	if err != nil {
		return err
	}
	actualOne, err := placeholders(actual.One)
	if err != nil {
		return err
	}
	if !sameStrings(expectedOne, actualOne) {
		return errors.New("one-form placeholders differ")
	}
	expectedOther, err := placeholders(expected.Other)
	if err != nil {
		return err
	}
	actualOther, err := placeholders(actual.Other)
	if err != nil {
		return err
	}
	if !sameStrings(expectedOther, actualOther) {
		return errors.New("other-form placeholders differ")
	}
	return nil
}

func (c *Catalogs) message(language Code, id string) (Message, bool) {
	if messages, ok := c.messages[language]; ok {
		if message, exists := messages[id]; exists {
			return message, true
		}
	}
	message, ok := c.messages[c.defaultLanguage][id]
	return message, ok
}

func (m Message) form(values map[string]string) (string, error) {
	text := m.Text
	if text == "" {
		count, ok := values["count"]
		if !ok {
			return "", ErrMissingValue
		}
		parsed, err := strconv.ParseFloat(count, 64)
		if err != nil {
			return "", fmt.Errorf("count is not numeric: %w", err)
		}
		if parsed == 1 {
			text = m.One
		} else {
			text = m.Other
		}
	}
	return interpolate(text, values)
}

func interpolate(text string, values map[string]string) (string, error) {
	var output strings.Builder
	for len(text) > 0 {
		start := strings.IndexByte(text, '{')
		if start < 0 {
			output.WriteString(text)
			break
		}
		output.WriteString(text[:start])
		end := strings.IndexByte(text[start+1:], '}')
		if end < 0 {
			return "", fmt.Errorf("%w: malformed placeholder", ErrInvalidCatalog)
		}
		end += start + 1
		name := text[start+1 : end]
		value, ok := values[name]
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrMissingValue, name)
		}
		output.WriteString(value)
		text = text[end+1:]
	}
	return output.String(), nil
}

func placeholders(text string) (map[string]struct{}, error) {
	values := map[string]struct{}{}
	for len(text) > 0 {
		start := strings.IndexByte(text, '{')
		if start < 0 {
			break
		}
		end := strings.IndexByte(text[start+1:], '}')
		if end < 0 {
			return nil, fmt.Errorf("%w: malformed placeholder", ErrInvalidCatalog)
		}
		end += start + 1
		if text[start+1:end] == "" {
			return nil, fmt.Errorf("%w: empty placeholder", ErrInvalidCatalog)
		}
		values[text[start+1:end]] = struct{}{}
		text = text[end+1:]
	}
	return values, nil
}

func sameStrings(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, ok := right[value]; !ok {
			return false
		}
	}
	return true
}

func validMessageID(id string) bool {
	if id == "" {
		return false
	}
	for index, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || (character == '.' && index > 0) {
			continue
		}
		return false
	}
	return id[0] >= 'a' && id[0] <= 'z'
}
