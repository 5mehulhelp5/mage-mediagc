package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/shuaiZend/mage-mediagc/internal/config"
)

// The two files a user is told to copy must actually work.
//
// Both shipped with `workers: 0` and `parallel: 0` under comments saying zero
// means "auto", which is how the tool reads them everywhere else — but
// validation rejected anything below one. Copying either file therefore
// produced a configuration that every command refused to start from, and the
// failure arrived before the user had changed a single line. This test exists
// so that a config we recommend cannot silently stop validating.
func TestShippedConfigurationExamplesValidate(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "mage-mediagc.yaml"))
	if err != nil {
		t.Fatalf("read the shipped example config: %v", err)
	}

	cases := map[string]string{
		"config template":            configTemplate,
		"examples/mage-mediagc.yaml": string(example),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mage-mediagc.yaml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			cfg, err := config.Load(config.Overrides{ConfigFile: path})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			// The examples point at a shop this test machine does not have, so
			// supply the two things validation would otherwise miss. Everything
			// else has to come from the file as written.
			cfg.Magento.MediaPath = t.TempDir()
			cfg.DB.Name, cfg.DB.User = "shop", "shopuser"

			if err := cfg.Validate(config.ModeAnalyze); err != nil {
				t.Fatalf("a configuration we tell people to start from must validate: %v", err)
			}
			if err := cfg.Validate(config.ModeWrite); err != nil {
				t.Fatalf("the same file must also validate for write operations: %v", err)
			}

			// A zero that reaches validation means the auto-resolution was
			// skipped, which is the bug this test was added for.
			if cfg.Scan.Workers < 1 || cfg.Cleanup.Parallel < 1 {
				t.Fatalf("worker counts were left unresolved: scan=%d cleanup=%d",
					cfg.Scan.Workers, cfg.Cleanup.Parallel)
			}
		})
	}
}
