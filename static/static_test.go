package static

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestFilesContainsVendoredSortable(t *testing.T) {
	data, err := fs.ReadFile(Files(), "sortable.min.js")
	if err != nil {
		t.Fatalf("read sortable.min.js: %v", err)
	}
	// docs/dependency-policy.md records the version and SHA-256.
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != "bf4241bc73fef7f11c59a283a69fe8051cdd31c6d8ff5a2b9ba219e7831fcf76" {
		t.Errorf("sortable.min.js SHA-256 = %s, want the recorded SortableJS 1.15.7 checksum", got)
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
