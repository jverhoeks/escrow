package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNpmConfigLocal_CreatesNpmrc(t *testing.T) {
	dir := t.TempDir()
	if err := writeNpmConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil {
		t.Fatalf("expected .npmrc to exist: %v", err)
	}
	if !strings.Contains(string(data), "registry=http://127.0.0.1:7888/") {
		t.Errorf("unexpected .npmrc content: %s", data)
	}
}

func TestWriteNpmConfigLocal_UpdatesExistingRegistry(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".npmrc"), []byte("registry=https://registry.npmjs.org/\nother=val\n"), 0644)
	writeNpmConfigLocal(dir, "http://127.0.0.1:7888")
	data, _ := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if strings.Count(string(data), "registry=") != 1 {
		t.Error("expected exactly one registry= line after update")
	}
	if !strings.Contains(string(data), "registry=http://127.0.0.1:7888/") {
		t.Errorf("registry not updated: %s", data)
	}
	if !strings.Contains(string(data), "other=val") {
		t.Error("other keys should be preserved")
	}
}

func TestWriteCargoConfigLocal_CreatesCargoDir(t *testing.T) {
	dir := t.TempDir()
	if err := writeCargoConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".cargo", "config.toml"))
	if err != nil {
		t.Fatalf("expected .cargo/config.toml: %v", err)
	}
	if !strings.Contains(string(data), `replace-with = "escrow"`) {
		t.Errorf("unexpected cargo config: %s", data)
	}
	if !strings.Contains(string(data), "http://127.0.0.1:7888/cargo/") {
		t.Errorf("expected cargo registry URL: %s", data)
	}
}

func TestWriteNugetConfigLocal_CreatesNugetConfig(t *testing.T) {
	dir := t.TempDir()
	if err := writeNugetConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "nuget.config"))
	if err != nil {
		t.Fatalf("expected nuget.config: %v", err)
	}
	if !strings.Contains(string(data), `key="escrow"`) {
		t.Errorf("unexpected nuget config: %s", data)
	}
	if !strings.Contains(string(data), "http://127.0.0.1:7888/nuget/v3/index.json") {
		t.Errorf("expected nuget URL in config: %s", data)
	}
}

func TestWritePypiConfigLocal_CreatesUvToml(t *testing.T) {
	dir := t.TempDir()
	if err := writePypiConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "uv.toml"))
	if err != nil {
		t.Fatalf("expected uv.toml: %v", err)
	}
	if !strings.Contains(string(data), "index-url") {
		t.Errorf("expected index-url in uv.toml: %s", data)
	}
	if !strings.Contains(string(data), "http://127.0.0.1:7888/pypi/simple/") {
		t.Errorf("expected pypi URL: %s", data)
	}
}

func TestWriteComposerConfigLocal_CreatesComposerJson(t *testing.T) {
	dir := t.TempDir()
	if err := writeComposerConfigLocal(dir, "http://127.0.0.1:7888"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		t.Fatalf("expected composer.json: %v", err)
	}
	if !strings.Contains(string(data), `"type": "composer"`) {
		t.Errorf("unexpected composer.json: %s", data)
	}
}

func TestWriteComposerConfigLocal_MergesExisting(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "composer.json"), []byte(`{"name":"my/pkg","require":{}}`), 0644)
	writeComposerConfigLocal(dir, "http://127.0.0.1:7888")
	data, _ := os.ReadFile(filepath.Join(dir, "composer.json"))
	if !strings.Contains(string(data), `"name"`) {
		t.Error("existing composer.json keys should be preserved")
	}
	if !strings.Contains(string(data), `"repositories"`) {
		t.Error("expected repositories key to be added")
	}
}

func TestConfigRestoreLocal_RestoresBackup(t *testing.T) {
	dir := t.TempDir()
	original := filepath.Join(dir, ".npmrc")
	backup := original + ".escrow-backup"
	os.WriteFile(original, []byte("registry=http://127.0.0.1:7888/\n"), 0644)
	os.WriteFile(backup, []byte("registry=https://registry.npmjs.org/\n"), 0644)

	restored := restoreLocalBackups(dir)
	if restored == 0 {
		t.Error("expected at least one file to be restored")
	}
	data, _ := os.ReadFile(original)
	if !strings.Contains(string(data), "registry.npmjs.org") {
		t.Errorf("expected original content restored, got: %s", data)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Error("backup file should be removed after restore")
	}
}
