package postgres

import (
	"context"
	"errors"
	"testing"
)

func TestOpenRejectsMissingOrInvalidURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want error
	}{
		{name: "missing", want: ErrEmptyURL},
		{name: "unsupported scheme", url: "mysql://user:pass@example.test/db", want: ErrInvalidURL},
		{name: "missing user", url: "postgres://example.test/db", want: ErrInvalidURL},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Open(context.Background(), test.url)
			if !errors.Is(err, test.want) {
				t.Fatalf("Open() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestQuoteIdentifierEscapesQuotes(t *testing.T) {
	if got, want := quoteIdentifier(`schema"name`), `"schema""name"`; got != want {
		t.Fatalf("quoteIdentifier() = %q, want %q", got, want)
	}
}
