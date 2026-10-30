package service

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const maxSlugLength = 64

// slugBaseLength leaves room for a numeric suffix on a generated slug.
const slugBaseLength = 48

var (
	// ErrInvalidSlug identifies a slug that is empty, malformed, numeric
	// only, or reserved for a route.
	ErrInvalidSlug = errors.New("service: invalid slug")
	// ErrSlugTaken identifies a slug another group (or another team of the
	// group) already uses.
	ErrSlugTaken = errors.New("service: slug taken")
)

// ReservedSlugs are words a slug may not be, because they name routes below
// a group or team. Keep it in step with migration 000013.
var ReservedSlugs = []string{
	"new", "edit", "delete", "create", "join", "leave", "settings", "members", "teams", "invites",
	"children", "resources", "seasons", "events", "calendar", "coaches", "plan", "statistics",
	"training-plan", "event-generation", "archive", "roster", "admin",
}

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	slugNumeric   = regexp.MustCompile(`^[0-9]+$`)
	slugNonAlnum  = regexp.MustCompile(`[^a-z0-9]+`)
	slugFoldLatin = strings.NewReplacer(foldPairs()...)
)

// foldPairs maps accented Latin letters to ASCII.
func foldPairs() []string {
	table := map[string]string{
		"áàâäãåāăą": "a", "ćčç": "c", "ďđ": "d", "éèêëēěę": "e", "ğ": "g", "íìîïīı": "i",
		"ł": "l", "ńňñ": "n", "óòôöõőøō": "o", "ř": "r", "śšş": "s", "ť": "t",
		"úùûüűūů": "u", "ýÿ": "y", "žźż": "z",
	}
	pairs := make([]string, 0, 128)
	for letters, ascii := range table {
		for _, letter := range letters {
			pairs = append(pairs, string(letter), ascii)
		}
	}
	return append(pairs, "ß", "ss", "æ", "ae", "œ", "oe")
}

// Slugify turns a name into a URL slug: lowercase ASCII, accents folded,
// runs of other characters turned into one hyphen. The result may be empty,
// numeric, or reserved; uniqueSlug turns it into a usable one.
func Slugify(name string) string {
	folded := slugFoldLatin.Replace(strings.ToLower(strings.TrimSpace(name)))
	slug := strings.Trim(slugNonAlnum.ReplaceAllString(folded, "-"), "-")
	if len(slug) > slugBaseLength {
		slug = strings.Trim(slug[:slugBaseLength], "-")
	}
	return slug
}

// ValidateSlug reports whether slug may address a group or team: lowercase
// letters, digits, and single hyphens; not only digits (those are the old
// numeric URLs); not a reserved word.
func ValidateSlug(slug string) error {
	if len(slug) == 0 || len(slug) > maxSlugLength || !slugPattern.MatchString(slug) ||
		slugNumeric.MatchString(slug) || slices.Contains(ReservedSlugs, slug) {
		return ErrInvalidSlug
	}
	return nil
}

// usableSlugBase turns a name into a valid slug base, falling back to the
// label ("group", "team") for names that slugify to nothing, to digits only,
// or to a reserved word.
func usableSlugBase(name, label string) string {
	base := Slugify(name)
	switch {
	case base == "":
		return label
	case slugNumeric.MatchString(base) || slices.Contains(ReservedSlugs, base):
		return label + "-" + base
	}
	return base
}

// uniqueSlug derives a free slug from name: the base, then base-2, base-3, and
// so on. taken reports whether a slug is in use.
func uniqueSlug(name, label string, taken func(string) (bool, error)) (string, error) {
	base := usableSlugBase(name, label)
	candidate := base
	for suffix := 2; ; suffix++ {
		used, err := taken(candidate)
		if err != nil {
			return "", err
		}
		if !used {
			return candidate, nil
		}
		candidate = base + "-" + strconv.Itoa(suffix)
	}
}

// normalizeSlugInput trims and lowercases an edited slug and validates it.
func normalizeSlugInput(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if err := ValidateSlug(slug); err != nil {
		return "", err
	}
	return slug, nil
}
