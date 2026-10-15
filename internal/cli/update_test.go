package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateHelpers(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho forge\n")
	tw.WriteHeader(&tar.Header{Name: "README.md", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: "forge", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write(body)
	tw.Close()
	gz.Close()
	archive := buf.Bytes()

	sum := sha256.Sum256(archive)
	sums := []byte("deadbeef  other.tar.gz\n" + hex.EncodeToString(sum[:]) + "  forge_linux_amd64.tar.gz\n")
	if err := verifyChecksum(archive, sums, "forge_linux_amd64.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if err := verifyChecksum(append(archive, 0), sums, "forge_linux_amd64.tar.gz"); err == nil {
		t.Fatal("tampered archive accepted")
	}
	bin, err := extractBinary(archive, "forge")
	if err != nil || !bytes.Equal(bin, body) {
		t.Fatalf("extract: %v", err)
	}
	exe := filepath.Join(t.TempDir(), "forge")
	os.WriteFile(exe, []byte("old"), 0o755)
	if err := replaceExecutable(exe, bin); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); !bytes.Equal(got, body) {
		t.Fatal("not replaced")
	}
}
