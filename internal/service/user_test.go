package service

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeEmail(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  error
	}{
		{name: "normalizes case and surrounding whitespace", input: "  User@Example.COM ", expected: "user@example.com"},
		{name: "allows optional empty value", input: " \t", expected: ""},
		{name: "rejects malformed address", input: "not-an-email", wantErr: ErrInvalidEmail},
		{name: "rejects display name", input: "User <user@example.com>", wantErr: ErrInvalidEmail},
		{name: "rejects oversized address", input: strings.Repeat("a", 250) + "@x.test", wantErr: ErrInvalidEmail},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeEmail(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeEmail() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if got != test.expected {
				t.Errorf("NormalizeEmail() = %q, want %q", got, test.expected)
			}
			if err != nil && strings.Contains(err.Error(), test.input) {
				t.Errorf("NormalizeEmail() error exposes input")
			}
		})
	}
}

func TestNormalizeUsername(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  error
	}{
		{name: "normalizes case and surrounding whitespace", input: "  Alice_42 ", expected: "alice_42"},
		{name: "allows optional empty value", input: " \t", expected: ""},
		{name: "rejects at sign", input: "alice@example", wantErr: ErrInvalidUsername},
		{name: "rejects internal whitespace", input: "alice smith", wantErr: ErrInvalidUsername},
		{name: "rejects oversized username", input: strings.Repeat("a", 33), wantErr: ErrInvalidUsername},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeUsername(test.input)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("NormalizeUsername() error = %v, want errors.Is(_, %v)", err, test.wantErr)
			}
			if got != test.expected {
				t.Errorf("NormalizeUsername() = %q, want %q", got, test.expected)
			}
			if err != nil && strings.Contains(err.Error(), test.input) {
				t.Errorf("NormalizeUsername() error exposes input")
			}
		})
	}
}

func stringPointer(value string) *string {
	return &value
}

func int64Pointer(value int64) *int64 {
	return &value
}
