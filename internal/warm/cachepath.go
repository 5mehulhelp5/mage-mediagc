// Package warm refills Magento's derived thumbnail cache by requesting the
// cached image URLs through the web server.
//
// Magento writes every resized variant under
//
//	<mediaRoot>/cache/<hash>/<path of the original>
//
// where <hash> is an md5 of the size parameters, and it generates a variant on
// the first request for it. Asking the web server for those URLs therefore
// fills the cache through exactly the code path a visitor would take, which is
// why warming needs no PHP runtime, no bin/magento and no Composer on the host.
//
// The hash is an implementation detail of Magento's PHP code and cannot be
// reproduced from Go with any confidence, so it is discovered from the cache
// directory (or supplied explicitly) rather than computed. A wrong hash would
// mean issuing hundreds of thousands of requests against a URL space that does
// not exist, and every one of them would look like a success.
package warm

import (
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

// CachePath returns the absolute path Magento serves a cached variant from.
//
// The cache tree mirrors the layout of the originals below the size-set
// directory, which is what makes the mapping a prefix substitution and nothing
// more.
func CachePath(mediaRoot, hash, relPath string) string {
	return filepath.Join(mediaRoot, media.CacheDirName, hash, filepath.FromSlash(relPath))
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
// afterwards nothing on the host says which size sets the theme asks for.
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

// WarmableFiles counts the originals that can be paired with a size set.
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

// warmableFile reports whether an original is worth pairing with a size set.
//
// Three exclusions matter. Files inside the cache tree are the output, not the
// input. Placeholder images never take the cache path. And the extension is
// restricted to the formats Magento resizes reliably, so that stray files a
// catalog collects (an .htaccess, importer staging debris) do not turn into
// 404s that then look like a broken run.
func warmableFile(rel string) bool {
	if rel == "" || media.IsCachePath(rel) {
		return false
	}
	if rel == placeholderDir || strings.HasPrefix(rel, placeholderDir+"/") {
		return false
	}
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	}
	return false
}
