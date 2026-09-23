package warm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tree builds a directory structure from a list of relative file paths.
func tree(t *testing.T, rels ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestCheckOnDemandSupport(t *testing.T) {
	service := resizeServiceRel

	t.Run("magento 2.3 and later pass", func(t *testing.T) {
		root := tree(t, "vendor/magento/module-media-storage/App/Media.php", service)
		if err := CheckOnDemandSupport(root); err != nil {
			t.Fatalf("CheckOnDemandSupport = %v, want nil", err)
		}
	})

	t.Run("magento 2.2 fails with an actionable error", func(t *testing.T) {
		// The 2.2 layout: the media front controller exists, the resize
		// service does not.
		root := tree(t, "vendor/magento/module-media-storage/App/Media.php",
			"vendor/magento/framework/App/Bootstrap.php")
		err := CheckOnDemandSupport(root)
		if !errors.Is(err, ErrNoResizeService) {
			t.Fatalf("err = %v, want ErrNoResizeService", err)
		}
		msg := err.Error()
		for _, want := range []string{"catalog:images:resize", "--skip-support-check", "2.3 or later"} {
			if !strings.Contains(msg, want) {
				t.Errorf("the error should mention %q to be actionable; got:\n%s", want, msg)
			}
		}
	})

	t.Run("a media only host passes", func(t *testing.T) {
		// No vendor directory at all: the tool supports mounting just
		// pub/media, and refusing there would be a regression.
		root := tree(t, "catalog/product/a/b/c.jpg", "cache/"+hashA+"/a/b/c.jpg")
		if err := CheckOnDemandSupport(root); err != nil {
			t.Fatalf("CheckOnDemandSupport = %v, want nil on a tree with no vendor/", err)
		}
	})

	t.Run("an unset or relative root passes", func(t *testing.T) {
		// Nothing was configured, so there is nothing to read and no claim to
		// make. "." is what an unset root cleans to.
		for _, root := range []string{"", ".", "./"} {
			if err := CheckOnDemandSupport(root); err != nil {
				t.Errorf("CheckOnDemandSupport(%q) = %v, want nil", root, err)
			}
		}
	})
}

// The service path is the version boundary stated in code. Asserting the
// literal keeps a rename from silently turning the check into a no-op that
// passes every install, including the ones it exists to refuse.
func TestResizeServicePathIsTheOneMagentoUses(t *testing.T) {
	const want = "vendor/magento/module-media-storage/Service/ImageResize.php"
	if resizeServiceRel != want {
		t.Fatalf("resizeServiceRel = %q, want %q", resizeServiceRel, want)
	}
}
