package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// makeTarGz builds an in-memory .tar.gz from a slice of (name, typeflag, content) tuples.
// Pass typeflag = tar.TypeDir for directories, tar.TypeReg for regular files.
func makeTarGz(t *testing.T, entries []tarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: e.typeflag,
			Size:     int64(len(e.content)),
			Mode:     0o644,
		}
		if e.typeflag == tar.TypeDir {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader %q: %v", e.name, err)
		}
		if len(e.content) > 0 {
			if _, err := tw.Write([]byte(e.content)); err != nil {
				t.Fatalf("Write %q: %v", e.name, err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	return buf.Bytes()
}

type tarEntry struct {
	name     string
	typeflag byte
	content  string
}

const (
	validServiceMetadata   = "id: my-service\ntype: service\nversion: 1.0.0\n"
	validComponentMetadata = "id: my-provider\ntype: component\ncomponent_type: llm\nversion: 1.0.0\n"
)

// TestPeekMetadata_HappyPath verifies that a well-formed service archive returns the correct metadata.
// The archive top-level directory name is irrelevant — identity comes from metadata.yaml.
func TestPeekMetadata_HappyPath(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"anything/", tar.TypeDir, ""},
		{"anything/metadata.yaml", tar.TypeReg, validServiceMetadata},
	})

	meta, err := peekMetadata(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.CatalogID() != "my-service" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "my-service")
	}
	if meta.CatalogType() != "service" {
		t.Errorf("CatalogType = %q, want %q", meta.CatalogType(), "service")
	}
	if meta.Version() != "1.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version(), "1.0.0")
	}
}

// TestPeekMetadata_ComponentHappyPath verifies that a component archive with component_type
// returns valid metadata regardless of the top-level directory name.
func TestPeekMetadata_ComponentHappyPath(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"irrelevant-dir-name/", tar.TypeDir, ""},
		{"irrelevant-dir-name/metadata.yaml", tar.TypeReg, validComponentMetadata},
	})

	meta, err := peekMetadata(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Component catalog_id is composite: "<component_type>--<id>"
	if meta.CatalogID() != "llm--my-provider" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "llm--my-provider")
	}
	if meta.CatalogType() != "component" {
		t.Errorf("CatalogType = %q, want %q", meta.CatalogType(), "component")
	}
	if meta.Version() != "1.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version(), "1.0.0")
	}
}

// TestPeekMetadata_RootLevelLooseFiles verifies that root-level loose files appearing
// before the bundle directory (e.g. macOS ._* sidecars) do not corrupt topDir inference.
func TestPeekMetadata_RootLevelLooseFiles(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"._my-service", tar.TypeReg, "junk"},               // macOS Apple Double at root
		{"my-service/", tar.TypeDir, ""},
		{"my-service/._metadata.yaml", tar.TypeReg, "junk"}, // sidecar for metadata.yaml
		{"my-service/metadata.yaml", tar.TypeReg, validServiceMetadata},
	})

	meta, err := peekMetadata(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.CatalogID() != "my-service" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "my-service")
	}
}

// TestPeekMetadata_RootLevelLooseFilesVariants verifies that any root-level file —
// regardless of name — cannot corrupt topDir when it appears before the bundle dir.
func TestPeekMetadata_RootLevelLooseFilesVariants(t *testing.T) {
	rootFiles := []tarEntry{
		// macOS Apple Double sidecars
		{"._my-service", tar.TypeReg, "junk"},
		// macOS .DS_Store
		{".DS_Store", tar.TypeReg, "junk"},
		// Windows artifacts
		{"Thumbs.db", tar.TypeReg, "junk"},
		{"desktop.ini", tar.TypeReg, "junk"},
		// any arbitrary root-level file
		{"README.txt", tar.TypeReg, "junk"},
	}

	entries := append(rootFiles,
		tarEntry{"my-service/", tar.TypeDir, ""},
		tarEntry{"my-service/metadata.yaml", tar.TypeReg, validServiceMetadata},
	)

	archive := makeTarGz(t, entries)

	meta, err := peekMetadata(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.CatalogID() != "my-service" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "my-service")
	}
}

// TestPeekMetadata_DotSlashPrefix verifies archives packed with leading "./" are handled.
// The dir name ("my-service") still does not need to match the id in metadata.yaml.
func TestPeekMetadata_DotSlashPrefix(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"./my-service/", tar.TypeDir, ""},
		{"./my-service/metadata.yaml", tar.TypeReg, validServiceMetadata},
	})

	meta, err := peekMetadata(archive)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if meta.CatalogID() != "my-service" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "my-service")
	}
}

// TestPeekMetadata_DirNameMismatch verifies that a top-level directory whose name
// differs from the id in metadata.yaml is now ACCEPTED — the dir name is irrelevant.
func TestPeekMetadata_DirNameMismatch(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"completely-different-name/", tar.TypeDir, ""},
		{"completely-different-name/metadata.yaml", tar.TypeReg, validServiceMetadata},
	})

	meta, err := peekMetadata(archive)
	if err != nil {
		t.Fatalf("dir name mismatch should now be accepted, got error: %v", err)
	}
	if meta.CatalogID() != "my-service" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "my-service")
	}
}

// TestPeekMetadata_MissingMetadataYAML verifies the error when metadata.yaml is absent.
func TestPeekMetadata_MissingMetadataYAML(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"my-service/", tar.TypeDir, ""},
		{"my-service/values.yaml", tar.TypeReg, "key: val\n"},
	})

	_, err := peekMetadata(archive)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	want := "metadata.yaml not found in archive"
	if !bytes.Contains([]byte(err.Error()), []byte(want)) {
		t.Errorf("error %q does not contain %q", err.Error(), want)
	}
}

// TestPeekMetadata_MissingType verifies the error when type is absent from metadata.yaml.
func TestPeekMetadata_MissingType(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"any-dir/", tar.TypeDir, ""},
		{"any-dir/metadata.yaml", tar.TypeReg, "id: my-service\nversion: 1.0.0\n"},
	})

	_, err := peekMetadata(archive)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("type is missing")) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPeekMetadata_MissingVersion verifies the error when version is absent.
func TestPeekMetadata_MissingVersion(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"any-dir/", tar.TypeDir, ""},
		{"any-dir/metadata.yaml", tar.TypeReg, "id: my-service\ntype: service\n"},
	})

	_, err := peekMetadata(archive)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("version is missing")) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPeekMetadata_PathTraversal verifies that archives with ".." entries are rejected.
func TestPeekMetadata_PathTraversal(t *testing.T) {
	archive := makeTarGz(t, []tarEntry{
		{"my-service/../etc/passwd", tar.TypeReg, "root:x:0:0\n"},
	})

	_, err := peekMetadata(archive)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("unsafe path")) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPeekMetadata_InvalidGzip verifies that a non-gzip payload is rejected cleanly.
func TestPeekMetadata_InvalidGzip(t *testing.T) {
	_, err := peekMetadata([]byte("this is not gzip"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("invalid gzip archive")) {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPeekMetadata_RealArchive runs peekMetadata against the actual mayuka-service.tar.gz
// fixture that was created on macOS (contains ._* Apple Double entries). This is the
// original repro case for the bug.
func TestPeekMetadata_RealArchive(t *testing.T) {
	// Locate the fixture relative to this test file's directory.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// The fixture lives at <repo-root>/ai-services/mayuka-service.tar.gz.
	// This test file is at internal/pkg/catalog/apiserver/services/bundle/,
	// so walk up 6 levels to reach ai-services/.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "..", "..")
	fixturePath := filepath.Join(repoRoot, "mayuka-service.tar.gz")

	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Skipf("fixture not found at %s, skipping: %v", fixturePath, err)
	}

	meta, err := peekMetadata(data)
	if err != nil {
		t.Fatalf("peekMetadata on real archive failed: %v", err)
	}

	if meta.CatalogID() != "mayuka-service" {
		t.Errorf("CatalogID = %q, want %q", meta.CatalogID(), "mayuka-service")
	}
	if meta.CatalogType() != "service" {
		t.Errorf("CatalogType = %q, want %q", meta.CatalogType(), "service")
	}
	if meta.Version() != "1.0.0" {
		t.Errorf("Version = %q, want %q", meta.Version(), "1.0.0")
	}
}
