// Command zipdist packages and verifies CPA plugin release artifacts.
//
// CPA installs a plugin from a release archive with a fixed shape:
//
//	<id>_<version>_<goos>_<goarch>.zip
//	  <id>.so | <id>.dylib | <id>.dll      (exactly one, at the zip root)
//	checksums.txt                          (sha256sum format)
//
// Anything else - a nested library, a renamed file, two libraries - is refused
// by the host installer, so the packaging step fails loudly here instead of
// shipping an archive nobody can install. The tool only uses the standard
// library so `make dist` works the same on Linux, macOS and Windows.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// archiveEpoch keeps archives byte-identical across machines and rebuilds.
var archiveEpoch = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

func main() {
	in := flag.String("in", "", "path to the built plugin library")
	name := flag.String("name", "", "entry name inside the archive, for example model-meta-sync.so")
	out := flag.String("out", "", "archive path to write")
	verify := flag.String("verify", "", "archive path to verify")
	checksums := flag.String("checksums", "", "directory whose *.zip files are hashed into checksums.txt")
	hash := flag.String("sha256", "", "file to hash into a sha256sum line")
	check := flag.String("check-sha256", "", "sha256sum file to verify")
	flag.Parse()

	switch {
	case *verify != "":
		if err := verifyArchive(*verify, *name); err != nil {
			fail(err)
		}
	case *checksums != "":
		if err := writeChecksums(*checksums); err != nil {
			fail(err)
		}
	case *hash != "":
		if err := writeSHA256Line(*hash, *out); err != nil {
			fail(err)
		}
	case *check != "":
		if err := checkSHA256File(*check); err != nil {
			fail(err)
		}
	case *in != "" && *out != "" && *name != "":
		if err := createArchive(*in, *out, *name); err != nil {
			fail(err)
		}
		fmt.Printf("wrote %s\n", *out)
	default:
		fail(fmt.Errorf("want -in/-name/-out, -verify, -checksums, -sha256 or -check-sha256"))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "zipdist:", err)
	os.Exit(1)
}

func createArchive(libraryPath, archivePath, entryName string) error {
	if err := validateEntryName(entryName); err != nil {
		return err
	}
	data, err := os.ReadFile(libraryPath)
	if err != nil {
		return fmt.Errorf("read library: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("library %s is empty", libraryPath)
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(archivePath), ".zipdist-*.zip")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		_ = os.Remove(tempPath)
	}()

	writer := zip.NewWriter(temp)
	header := &zip.FileHeader{Name: entryName, Method: zip.Deflate, Modified: archiveEpoch}
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		_ = temp.Close()
		return fmt.Errorf("create archive entry: %w", err)
	}
	if _, err := entry.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write archive entry: %w", err)
	}
	if err := writer.Close(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("finalize archive: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync archive: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close archive: %w", err)
	}
	if err := os.Rename(tempPath, archivePath); err != nil {
		return fmt.Errorf("publish archive: %w", err)
	}
	return nil
}

func verifyArchive(archivePath, entryName string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()

	var libraries []string
	for _, file := range reader.File {
		if file.FileInfo().IsDir() {
			return fmt.Errorf("archive entry %s is a directory; the library must sit at the zip root", file.Name)
		}
		if strings.Contains(file.Name, `\`) {
			return fmt.Errorf("archive entry %s uses backslash separators", file.Name)
		}
		if filepath.IsAbs(file.Name) || strings.HasPrefix(filepath.Clean(file.Name), "..") {
			return fmt.Errorf("archive entry %s escapes the archive root", file.Name)
		}
		if !hasLibraryExtension(file.Name) {
			continue
		}
		if filepath.Dir(file.Name) != "." {
			return fmt.Errorf("archive entry %s is nested; the library must sit at the zip root", file.Name)
		}
		libraries = append(libraries, file.Name)
	}
	if len(libraries) != 1 {
		return fmt.Errorf("archive must contain exactly one dynamic library, found %d: %v", len(libraries), libraries)
	}
	if entryName != "" && libraries[0] != entryName {
		return fmt.Errorf("archive library is %s, want %s", libraries[0], entryName)
	}
	if err := readEntry(reader.File[0]); err != nil {
		return err
	}
	info, err := os.Stat(archivePath)
	if err != nil {
		return fmt.Errorf("stat archive: %w", err)
	}
	fmt.Printf("zip layout ok: %s (%s, %d bytes)\n", archivePath, libraries[0], info.Size())
	return nil
}

func readEntry(file *zip.File) error {
	handle, err := file.Open()
	if err != nil {
		return fmt.Errorf("open archive entry %s: %w", file.Name, err)
	}
	defer func() {
		_ = handle.Close()
	}()
	if _, err := io.Copy(io.Discard, handle); err != nil {
		return fmt.Errorf("read archive entry %s: %w", file.Name, err)
	}
	return nil
}

func writeChecksums(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read release directory: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".zip") {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return fmt.Errorf("no *.zip in %s", directory)
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		builder.WriteString(hex.EncodeToString(sum[:]))
		builder.WriteString("  ")
		builder.WriteString(name)
		builder.WriteString("\n")
	}
	target := filepath.Join(directory, "checksums.txt")
	if err := os.WriteFile(target, []byte(builder.String()), 0o644); err != nil {
		return fmt.Errorf("write checksums: %w", err)
	}
	fmt.Print(builder.String())
	return nil
}

// writeSHA256Line records the hash of one artifact next to it, in the
// sha256sum format the store publishes. It exists so `make build` works on hosts
// without coreutils (macOS ships shasum, not sha256sum).
func writeSHA256Line(target, output string) error {
	data, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read %s: %w", target, err)
	}
	sum := sha256.Sum256(data)
	line := hex.EncodeToString(sum[:]) + "  " + filepath.Base(target) + "\n"
	if strings.TrimSpace(output) == "" {
		output = target + ".sha256"
	}
	if err := os.WriteFile(output, []byte(line), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", output, err)
	}
	fmt.Print(line)
	return nil
}

// checkSHA256File verifies a sha256sum file against the files beside it.
func checkSHA256File(listPath string) error {
	raw, err := os.ReadFile(listPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", listPath, err)
	}
	directory := filepath.Dir(listPath)
	checked := 0
	for lineNumber, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != 64 {
			return fmt.Errorf("%s:%d: not a sha256sum line: %q", listPath, lineNumber+1, line)
		}
		data, err := os.ReadFile(filepath.Join(directory, fields[1]))
		if err != nil {
			return fmt.Errorf("%s:%d: %w", listPath, lineNumber+1, err)
		}
		sum := sha256.Sum256(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), fields[0]) {
			return fmt.Errorf("%s: %s does not match", fields[1], listPath)
		}
		fmt.Printf("%s: OK\n", fields[1])
		checked++
	}
	if checked == 0 {
		return fmt.Errorf("%s lists no checksums", listPath)
	}
	return nil
}

func validateEntryName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("entry name is empty")
	}
	if filepath.Base(name) != name {
		return fmt.Errorf("entry name %q must not contain a path", name)
	}
	if !hasLibraryExtension(name) {
		return fmt.Errorf("entry name %q must end in .so, .dylib or .dll", name)
	}
	return nil
}

func hasLibraryExtension(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".so") || strings.HasSuffix(lower, ".dylib") || strings.HasSuffix(lower, ".dll")
}
