package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Run a fresh process because t.Setenv in this process is too late to catch
// configuration reads performed by package initialization.
func TestSettingsImportDoesNotLoadConfig(t *testing.T) {
	const childEnv = "ORCA_SETTINGS_IMPORT_TEST"
	if os.Getenv(childEnv) == "1" {
		if settings != (Settings{}) {
			t.Fatalf("import initialized settings = %+v, want zero value", settings)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"missing", "malformed", "configured"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if name != "missing" {
				configDir := filepath.Join(root, ".orca")
				if err := os.MkdirAll(configDir, 0700); err != nil {
					t.Fatal(err)
				}
				data := "{invalid JSON"
				if name == "configured" {
					data = `{"defaultProvider":"must-not-load","defaultModel":"must-not-load","debug":"true"}`
				}
				if err := os.WriteFile(filepath.Join(configDir, setting), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(executable, "-test.run=^TestSettingsImportDoesNotLoadConfig$")
			cmd.Dir = root
			cmd.Env = []string{"HOME=" + root, "WORKSPACE=" + root, childEnv + "=1"}
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("import with %s config: %v\n%s", name, err, output)
			}
		})
	}
}

func TestLoadSettingsRemainsExplicit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("WORKSPACE", root)
	t.Chdir(root)
	configDir := filepath.Join(root, ".orca")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	data := `{"defaultProvider":"fixture-provider","defaultModel":"fixture-model","debug":"true"}`
	if err := os.WriteFile(filepath.Join(configDir, setting), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	want := Settings{DefaultProvider: "fixture-provider", DefaultModel: "fixture-model", Debug: "true"}
	if got := LoadSettings(); got != want {
		t.Errorf("LoadSettings() = %+v, want %+v", got, want)
	}
	for _, key := range []string{KeyDefaultProvider, KeyDefaultModel, KeyDebug} {
		if got := Get(key); got != "" {
			t.Errorf("Get(%q) = %q, want unchanged zero setting", key, got)
		}
	}
}
