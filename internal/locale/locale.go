// Package locale defines supported language codes and canonical URL paths.
package locale

import (
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrUnsupported identifies a language code outside the application catalog.
	ErrUnsupported = errors.New("locale: unsupported language")
	// ErrInvalidPath identifies a route that cannot be localized safely.
	ErrInvalidPath = errors.New("locale: invalid path")
)

// Code identifies one supported language.
type Code string

const (
	// English is the default language.
	English Code = "en"
	// Hungarian is the second supported language.
	Hungarian Code = "hu"
)

// Default is used for URLs without an explicit language prefix.
const Default = English

// Source identifies which preference selected a language.
type Source string

const (
	SourceURL             Source = "url"
	SourceAccount         Source = "account"
	SourceBrowser         Source = "browser"
	SourceBrowserLanguage Source = "browser-language"
	SourceDefault         Source = "default"
)

// Preferences contains already separated language preference inputs. Zero
// values mean that a preference was not supplied or was invalid.
type Preferences struct {
	URL            Code
	Account        Code
	Browser        Code
	AcceptLanguage string
}

// Supported reports whether code has a catalog and can appear in a URL.
func Supported(code Code) bool {
	return code == English || code == Hungarian
}

// Codes returns supported language codes in stable display order.
func Codes() []Code {
	return []Code{English, Hungarian}
}

// Resolve applies language preference precedence and returns its source.
func Resolve(preferences Preferences) (Code, Source) {
	for _, candidate := range []struct {
		code   Code
		source Source
	}{
		{code: preferences.URL, source: SourceURL},
		{code: preferences.Account, source: SourceAccount},
		{code: preferences.Browser, source: SourceBrowser},
	} {
		if Supported(candidate.code) {
			return candidate.code, candidate.source
		}
	}
	if code, ok := acceptLanguage(preferences.AcceptLanguage); ok {
		return code, SourceBrowserLanguage
	}
	return Default, SourceDefault
}

// ParsePath extracts a language prefix and route from a URL path.
func ParsePath(path string) (Code, string, bool) {
	if path == "" || !strings.HasPrefix(path, "/") {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "/", 2)
	code := Code(parts[0])
	if !Supported(code) {
		return "", "", false
	}
	if len(parts) == 1 {
		return code, "/", true
	}
	if parts[1] == "" {
		return code, "/", true
	}
	if strings.HasPrefix(parts[1], "/") {
		return "", "", false
	}
	return code, "/" + parts[1], true
}

// Path returns the canonical internal URL for route under code.
func Path(code Code, route string) (string, error) {
	if !Supported(code) {
		return "", ErrUnsupported
	}
	parsed, err := url.Parse(route)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", ErrInvalidPath
	}
	if strings.HasPrefix(parsed.Path, "//") {
		return "", ErrInvalidPath
	}
	path := strings.TrimPrefix(parsed.Path, "/")
	if path == "" {
		path = "/"
	} else {
		path = "/" + path
	}
	parsed.Path = "/" + string(code) + path
	return parsed.String(), nil
}

type languageCandidate struct {
	code    Code
	quality float64
	order   int
}

func acceptLanguage(header string) (Code, bool) {
	if strings.TrimSpace(header) == "" {
		return "", false
	}
	candidates := make([]languageCandidate, 0)
	for order, raw := range strings.Split(header, ",") {
		parts := strings.Split(raw, ";")
		tag := strings.ToLower(strings.TrimSpace(parts[0]))
		if tag == "" || tag == "*" {
			continue
		}
		quality := 1.0
		valid := true
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) != 2 || strings.ToLower(strings.TrimSpace(keyValue[0])) != "q" {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(keyValue[1]), 64)
			if err != nil || parsed < 0 || parsed > 1 {
				valid = false
				break
			}
			quality = parsed
		}
		if !valid || quality == 0 {
			continue
		}
		code := Code(tag)
		if !Supported(code) {
			primary := Code(strings.SplitN(tag, "-", 2)[0])
			if !Supported(primary) {
				continue
			}
			code = primary
		}
		candidates = append(candidates, languageCandidate{code: code, quality: quality, order: order})
	}
	if len(candidates) == 0 {
		return "", false
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].quality == candidates[right].quality {
			return candidates[left].order < candidates[right].order
		}
		return candidates[left].quality > candidates[right].quality
	})
	return candidates[0].code, true
}
