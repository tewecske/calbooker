package static

import (
	"io/fs"
	"testing"
)

func TestFilesContainsGeneratedStylesheet(t *testing.T) {
	info, err := fs.Stat(Files(), "app.css")
	if err != nil {
		t.Fatalf("stat app.css: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("app.css is empty")
	}
}

func TestFilesContainsPinnedHTMXRuntime(t *testing.T) {
	info, err := fs.Stat(Files(), "htmx.min.js")
	if err != nil {
		t.Fatalf("stat htmx.min.js: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("htmx.min.js is empty")
	}
}
