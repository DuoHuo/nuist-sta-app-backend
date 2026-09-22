package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelStorageConfig(t *testing.T) {
	t.Setenv("CAMPUS_MODEL_STORAGE_DIR", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.StorageDir != "data/models" {
		t.Fatalf("default %q", cfg.Models.StorageDir)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err = os.WriteFile(path, []byte("models:\n  storage_dir: custom/models\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.StorageDir != "custom/models" {
		t.Fatalf("yaml %q", cfg.Models.StorageDir)
	}
	t.Setenv("CAMPUS_MODEL_STORAGE_DIR", "override/models")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models.StorageDir != "override/models" {
		t.Fatalf("env %q", cfg.Models.StorageDir)
	}
}
