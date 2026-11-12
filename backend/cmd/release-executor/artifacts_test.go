package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandArtifactGlobPlainAndDoubleStar(t *testing.T) {
	root := t.TempDir()
	must := func(rel string) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("dist/SyntropySetup.exe")
	must("out/nested/deep/app.dmg")
	must("out/readme.txt")

	plain, err := expandArtifactGlob(root, "dist/*.exe")
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 1 || filepath.Base(plain[0]) != "SyntropySetup.exe" {
		t.Fatalf("plain glob = %v", plain)
	}

	deep, err := expandArtifactGlob(root, "out/**/*.dmg")
	if err != nil {
		t.Fatal(err)
	}
	if len(deep) != 1 || filepath.Base(deep[0]) != "app.dmg" {
		t.Fatalf("double star glob = %v", deep)
	}

	none, err := expandArtifactGlob(root, "dist/*.msi")
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no matches, got %v", none)
	}
}
