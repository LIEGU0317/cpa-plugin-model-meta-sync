package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFakeLibrary(t *testing.T, dir, name string, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Repeat("x", size)), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCreateAndVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	library := writeFakeLibrary(t, dir, "model-meta-sync.so", 4096)
	archive := filepath.Join(dir, "model-meta-sync_0.3.1_linux_amd64.zip")

	if err := createArchive(library, archive, "model-meta-sync.so"); err != nil {
		t.Fatalf("createArchive: %v", err)
	}
	if err := verifyArchive(archive, "model-meta-sync.so"); err != nil {
		t.Fatalf("verifyArchive: %v", err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if len(reader.File) != 1 || reader.File[0].Name != "model-meta-sync.so" {
		t.Fatalf("unexpected entries: %v", reader.File)
	}
	if reader.File[0].FileInfo().Mode().Perm()&0o111 == 0 {
		t.Fatalf("library entry is not executable: %v", reader.File[0].FileInfo().Mode())
	}
}

func TestCreateRejectsPathEntryName(t *testing.T) {
	dir := t.TempDir()
	library := writeFakeLibrary(t, dir, "model-meta-sync.so", 16)
	err := createArchive(library, filepath.Join(dir, "a.zip"), "sub/model-meta-sync.so")
	if err == nil {
		t.Fatal("entry names with a path must be rejected")
	}
}

func buildArchive(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	handle, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(handle)
	for name, body := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsLayoutsTheHostRefuses(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		entries map[string]string
		expect  string
	}{
		{
			name:    "nested library",
			entries: map[string]string{"sub/model-meta-sync.so": "x"},
			expect:  "nested",
		},
		{
			name:    "two libraries",
			entries: map[string]string{"model-meta-sync.so": "x", "other.so": "y"},
			expect:  "exactly one",
		},
		{
			name:    "no library",
			entries: map[string]string{"README.md": "x"},
			expect:  "exactly one",
		},
		{
			name:    "wrong library name",
			entries: map[string]string{"other.so": "x"},
			expect:  "want model-meta-sync.so",
		},
	}
	for _, tc := range cases {
		archive := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".zip")
		buildArchive(t, archive, tc.entries)
		err := verifyArchive(archive, "model-meta-sync.so")
		if err == nil {
			t.Fatalf("%s: archive must be rejected", tc.name)
		}
		if !strings.Contains(err.Error(), tc.expect) {
			t.Fatalf("%s: error %q does not mention %q", tc.name, err, tc.expect)
		}
	}
}

func TestWriteChecksumsIsSortedSha256SumFormat(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b_linux_amd64.zip", "a_linux_amd64.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeChecksums(dir); err != nil {
		t.Fatalf("writeChecksums: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "checksums.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("checksums.txt must cover the two zips, got %q", raw)
	}
	if !strings.HasSuffix(lines[0], "  a_linux_amd64.zip") || !strings.HasSuffix(lines[1], "  b_linux_amd64.zip") {
		t.Fatalf("checksums must be sorted by file name: %q", raw)
	}
	for _, line := range lines {
		if len(strings.Fields(line)[0]) != 64 {
			t.Fatalf("not a sha256 line: %q", line)
		}
	}
}

func TestWriteChecksumsWithoutArchivesFails(t *testing.T) {
	if err := writeChecksums(t.TempDir()); err == nil {
		t.Fatal("an empty release directory must fail")
	}
}

func TestSHA256LineAndCheck(t *testing.T) {
	dir := t.TempDir()
	library := writeFakeLibrary(t, dir, "model-meta-sync-v0.3.1.so", 128)
	if err := writeSHA256Line(library, ""); err != nil {
		t.Fatalf("writeSHA256Line: %v", err)
	}
	list := library + ".sha256"
	raw, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSpace(string(raw)), "  model-meta-sync-v0.3.1.so") {
		t.Fatalf("unexpected checksum line: %q", raw)
	}
	if err := checkSHA256File(list); err != nil {
		t.Fatalf("checkSHA256File: %v", err)
	}

	// A modified artifact must fail verification.
	if err := os.WriteFile(library, []byte(strings.Repeat("y", 128)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkSHA256File(list); err == nil {
		t.Fatal("a changed artifact must not verify")
	}
}

func TestCheckSHA256FileRejectsGarbage(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "broken.sha256")
	if err := os.WriteFile(list, []byte("not a checksum line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkSHA256File(list); err == nil {
		t.Fatal("a malformed checksum file must be rejected")
	}
}
