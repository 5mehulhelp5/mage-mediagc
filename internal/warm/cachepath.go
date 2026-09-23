// Package warm refills Magento's derived thumbnail cache by requesting the
// cached image URLs through the web server.
//
// Magento writes every resized variant under
//
//	<mediaRoot>/cache/<hash>/<dir>/<dir>/<file>
//
// where <hash> identifies a size set and the trailing path mirrors the
// original. Asking the web server for such a URL fills the cache through
// exactly the code path a visitor takes, which is why warming needs no PHP
// runtime, no bin/magento and no Composer on the host.
//
// # One request fills the whole size-set family
//
// Since Magento 2.3 the request is handled by
// Magento\MediaStorage\App\Media::launch, which calls
// ImageResize::resizeFromImageName() on the original behind the requested cache
// path. That regenerates the derived image for every size set the theme
// defines, not only the one that was asked for, so one request per original is
// enough. Requesting once per (original, size set) pair — the obvious reading
// of the URLs — multiplies the job by the number of size sets, typically
// twenty-five, for no gain.
//
// # The hash segment is a route, not a request
//
// Two properties of the same code path shape everything else here, and both are
// cheap to confirm by hand on a test install:
//
//   - Magento never validates the hash. Any 32 hex characters reach the resize
//     service and produce the full family of sets the theme asks for. The value
//     is the md5 of PHP-side parameters (Magento\Catalog\Model\Product\
//     Image\ParamsBuilder builds them, View\Asset\Image::getMiscPath hashes
//     them), so it cannot be reproduced here with any confidence. It does not
//     need to be.
//   - The requested path is what the caller stats to decide whether a variant
//     exists, and it is only created when the hash names a set the theme really
//     asks for. A made-up hash therefore warms the whole family while leaving
//     the URL that was asked for absent, which reads as failure.
//
// So a hash has to be live — see LiveHash — but which live one is used does not
// matter, and only one is needed.
//
// # The path depth is fixed
//
// Media::getOriginalImage() recovers the original from the request with
//
//	return preg_replace('|^.*((?:/[^/]+){3})$|', '$1', $resizedImagePath);
//
// that is, it keeps exactly the last three segments. A cache URL must therefore
// end in two directories and a file name. Magento's own layout always does,
// which is why the rule costs nothing in practice, but a tree that does not
// would have its requests resolved against the wrong original — silently
// generating a variant of some other image. See warmableFile.
//
// # Magento 2.2 and earlier cannot do this at all
//
// Those releases have no resize service. Media::launch only copies the file out
// of the media storage backend, so a request for a missing cache URL fails and
// generates nothing. CheckOnDemandSupport refuses to start rather than
// launching a run whose every request would be wasted.
package warm

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// hashLen is the length of a Magento size-set directory name, which is the
// hex form of an md5 digest.
const hashLen = 32

// SeedHash is a size set that is syntactically valid and deliberately
// meaningless.
//
// It exists for the one case nothing on the host can answer: when the cache
// tree is empty and no hash was supplied, there is no live size set to read off
// the disk, and Magento leaves no other record of one. Because the hash segment
// is not validated, a request carrying this value still regenerates the entire
// family — and the sets that appear afterwards are the live ones, which
// LiveHash then reads back.
//
// It is the all-zero digest rather than something derived, so that nobody
// mistakes it for a computed value: it carries no information at all, which is
// exactly its role.
const SeedHash = "00000000000000000000000000000000"

// placeholderDir is the placeholder directory, relative to the media root.
//
// Magento serves those images straight from their own path and never through
// the cache tree, so pairing them with a size set would only produce a 404.
// The analyzer protects the same directory for a different reason; the
// constant is repeated here rather than exported because the two meanings are
// not the same thing.
const placeholderDir = "placeholder"

// ValidHash reports whether name has the shape of a size-set directory.
//
// Magento writes the digest in lowercase hex, so anything else in the cache
// directory — a stray directory, a backup copy, a future layout — is ignored
// rather than turned into requests.
func ValidHash(name string) bool {
	if len(name) != hashLen {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// DiscoverHashes returns the size-set hashes present under the cache
// directory, sorted. A missing cache directory is not an error: it is exactly
// the state reached after `cache clean`, and callers report the empty result
// themselves.
//
// Sorted order is what makes the choice of a hash to try first deterministic,
// and nothing more: a directory that exists is not necessarily one the theme
// still asks for. LiveHash is what settles that.
func DiscoverHashes(cacheDir string) ([]string, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache dir %s: %w", cacheDir, err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ValidHash(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// CacheDir returns the derived-cache directory under a media root.
func CacheDir(mediaRoot string) string {
	return filepath.Join(mediaRoot, media.CacheDirName)
}

// ErrNoLiveHash reports that no size set under the cache directory holds the
// variant asked about, which means none of them is one the theme currently
// asks for.
var ErrNoLiveHash = errors.New("no live size set: the cache directory holds no " +
	"variant for that image, so none of the size sets present is one the theme asks for")

// LiveHash returns a size set the theme really asks for, proven rather than
// guessed.
//
// A size set is live when the variant at <hash>/<relPath> exists. That is the
// same test the warm run applies to each item, so a hash accepted here is one
// whose URL the run will see materialize — no separate liveness probe, and no
// reliance on timestamps, which say when a directory was written but not
// whether anything still asks for it.
//
// Stale sets are the norm rather than the exception. The hash is derived from
// the theme's view.xml and from store configuration, so editing either orphans
// every existing directory forever: Magento writes into the sets it wants and
// never sweeps the ones it has stopped wanting. Picking by recency would pick a
// stale set whenever a rebuild touched it last.
//
// Callers that find nothing here can issue one request with SeedHash and try
// again — the family it generates is exactly the set of live ones.
func LiveHash(cacheDir, relPath string) (string, error) {
	hashes, err := DiscoverHashes(cacheDir)
	if err != nil {
		return "", err
	}
	for _, h := range hashes {
		if _, err := os.Stat(VariantPath(cacheDir, h, relPath)); err == nil {
			return h, nil
		}
	}
	return "", ErrNoLiveHash
}

// VariantPath returns the path of a cached variant inside a cache directory.
//
// CachePath is the same value derived from a media root; this form exists so
// that the live-hash search, which is handed a cache directory, does not have
// to reconstruct the root only to take it apart again.
func VariantPath(cacheDir, hash, relPath string) string {
	return filepath.Join(cacheDir, hash, filepath.FromSlash(relPath))
}

// CachePath returns the absolute path Magento serves a cached variant from.
//
// The cache tree mirrors the layout of the originals below the size-set
// directory, which is what makes the mapping a prefix substitution and nothing
// more.
func CachePath(mediaRoot, hash, relPath string) string {
	return VariantPath(CacheDir(mediaRoot), hash, relPath)
}

// CacheURL returns the public URL of a cached variant.
//
// The path is assembled through net/url rather than by concatenation so that
// file names containing spaces or non-ASCII characters are escaped correctly.
// Magento catalogs routinely hold both.
func CacheURL(base *url.URL, hash, relPath string) string {
	u := *base
	u.Path = path.Join("/", base.Path, "media", "catalog", "product",
		media.CacheDirName, hash, relPath)
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// ParseBaseURL validates and normalizes a storefront base URL.
func ParseBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("empty base URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse base URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("base URL %q must use http or https, got %q", raw, u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("base URL %q has no host", raw)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("base URL %q must not carry a query or fragment", raw)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

// ReadHashFile loads a hash snapshot written by WriteHashFile. Blank lines and
// lines starting with # are ignored, so the file stays editable by hand.
//
// The file is a hint, not an authority: a snapshot taken before the theme or the
// store configuration changed names sets that no longer exist. LiveHash decides.
func ReadHashFile(p string) ([]string, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !ValidHash(line) {
			return nil, fmt.Errorf("%s: %q is not a cache hash (32 lowercase hex characters)", p, line)
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		out = append(out, line)
	}
	sort.Strings(out)
	return out, nil
}

// WriteHashFile records a hash snapshot.
//
// The point of the file is the gap between the two halves of a cache rebuild:
// the hashes have to be read off the disk before the cache is emptied, because
// afterwards nothing on the host says which size sets the theme asks for. It is
// a convenience rather than a requirement — a run that starts with an empty
// tree can recover a live set on its own, at the cost of one request — but a
// run given one does not have to.
func WriteHashFile(p string, hashes []string) error {
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	var b strings.Builder
	b.WriteString("# mage-mediagc thumbnail cache hashes\n")
	b.WriteString("# One size-set hash per line, as found under media/catalog/product/cache/.\n")
	b.WriteString("# Written before the cache is emptied; read by `mage-mediagc cache warm`.\n")
	for _, h := range hashes {
		b.WriteString(h)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", p, err)
	}
	return nil
}

// WarmableFiles counts the originals the plan can be built from.
//
// It is exported so a caller can report a number that multiplies out: only
// these files produce variants, so a total taken over every file on disk would
// not. Reporting a count that disagrees with the plan is a small thing, but it
// is exactly the kind of small thing that makes an operator distrust the run.
func WarmableFiles(files []media.File) int {
	n := 0
	for _, f := range files {
		if warmableFile(f.RelPath) {
			n++
		}
	}
	return n
}

// warmableFile reports whether an original can be warmed through a cache URL.
//
// Four exclusions matter. Files inside the cache tree are the output, not the
// input. Placeholder images never take the cache path. The extension is
// restricted to the formats Magento resizes reliably, so that stray files a
// catalog collects (an .htaccess, importer staging debris) do not turn into
// 404s that then look like a broken run.
//
// The last one is the path depth, and it is the reason this check is not merely
// a filter. Magento recovers the original by keeping the last three segments of
// the request path (see the package comment), so a file at any other depth is
// requested against a different original and would quietly generate the wrong
// image — or, more often, fail and be reported as a transport problem. Magento
// stores catalog images exactly two directories deep, so the rule admits every
// real original and nothing else. Excluded files are counted, not dropped
// silently.
func warmableFile(rel string) bool {
	if rel == "" || media.IsCachePath(rel) {
		return false
	}
	if rel == placeholderDir || strings.HasPrefix(rel, placeholderDir+"/") {
		return false
	}
	if !isImageFile(rel) {
		return false
	}
	// Two directories and a file name: exactly what the last-three-segments
	// rule expects to find.
	segments := 0
	for _, s := range strings.Split(rel, "/") {
		if s != "" {
			segments++
		}
	}
	return segments == originalDepth
}

// originalDepth is the number of path segments Media::getOriginalImage keeps.
const originalDepth = 3
