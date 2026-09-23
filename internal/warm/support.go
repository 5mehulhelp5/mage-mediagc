package warm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// resizeServiceRel is the file that has to exist for /media/ requests to
// generate a derived image, relative to the Magento root.
//
// Magento 2.3 moved image resizing into module-media-storage and made the media
// front controller call it, so the presence of this class is the version
// boundary stated in code rather than inferred from a version string. Reading a
// version would mean trusting composer.json (absent in a composer-managed
// project) or a constant that a patch release might not have moved; the file
// layout is what the request path actually depends on.
const resizeServiceRel = "vendor/magento/module-media-storage/Service/ImageResize.php"

// vendorRel is the directory whose absence means this is not a Magento tree we
// can judge at all.
const vendorRel = "vendor/magento"

// ErrNoResizeService reports that this Magento install cannot generate derived
// images on demand, so an HTTP warm run would issue requests that all fail.
var ErrNoResizeService = errors.New("this Magento install does not generate derived images on demand")

// CheckOnDemandSupport reports whether a warm run against this Magento install
// can work at all.
//
// Magento 2.2 and earlier have no resize service: Magento\MediaStorage\App\
// Media::launch copies the requested file out of the media storage backend only,
// so a request for a missing variant fails and generates nothing. Requesting a
// missing cache URL on such an install therefore fails for every URL, including
// the one the pre-flight probe uses, which is why this is checked before the
// media tree is walked rather than left to the probe. The probe would stop the
// run, but only after reporting a symptom (404, or 200 with nothing written)
// that points at the wrong cause — a CDN or a wrong base URL rather than an
// unsupported version.
//
// A root that does not look like a Magento tree at all passes. The check exists
// to name a specific, actionable condition, not to police configuration: a host
// that mounts only pub/media has no vendor directory and is a supported way to
// run this tool, and refusing there would be a regression. Callers that know
// the difference can act on ErrNoResizeService.
func CheckOnDemandSupport(magentoRoot string) error {
	root := filepath.Clean(magentoRoot)
	if magentoRoot == "" || root == "." {
		// No root configured, so there is nothing to read and no claim to make.
		return nil
	}
	if !fileExists(filepath.Join(root, vendorRel)) {
		return nil
	}
	if fileExists(filepath.Join(root, resizeServiceRel)) {
		return nil
	}
	return fmt.Errorf("%w: %s is missing, which is the layout of Magento 2.2 and earlier. "+
		"Requesting a missing thumbnail there returns 404 and generates nothing, so an HTTP "+
		"warm run cannot work and every request would be wasted. Either run the resize inside "+
		"the shop instead (bin/magento catalog:images:resize — single threaded, and it has to "+
		"look at every variant even the ones that already exist), or upgrade to Magento 2.3 or "+
		"later. Pass --skip-support-check to warm anyway once you have confirmed by hand that "+
		"this install does generate derived images on request",
		ErrNoResizeService, filepath.Join(root, resizeServiceRel))
}

// fileExists reports whether a path is present, without caring whether it is a
// file or a directory.
//
// It exists so the checks above read as the question they are asking. Spelling
// them as `if _, err := os.Stat(...); err != nil { return nil }` invites a
// reader — and a linter — to see a swallowed error where the nil is in fact the
// answer.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
