package magento

import (
	"net/url"
	"path"
	"regexp"
	"strings"
)

// mediaDirectiveRe matches the {{media url="..."}} syntax used by Magento's
// page builder and CMS editor.
var mediaDirectiveRe = regexp.MustCompile(`(?is)\{\{\s*media\s+url\s*=\s*["']?\s*([^"'}]+?)\s*["']?\s*\}\}`)

// mediaPathRe matches bare media paths embedded in HTML or Markdown, with or
// without the catalog/product or wysiwyg prefix. It is deliberately greedy on
// the directory part and restrictive on the extension so that it does not
// swallow surrounding markup.
var mediaPathRe = regexp.MustCompile(`(?i)(?:catalog/product|wysiwyg)/[A-Za-z0-9_./%+@~-]*\.(?:jpe?g|png|gif|webp|avif|svg|bmp|tiff?|mp4|webm|glb)`)

// mediaPrefixes are the path fragments stripped when normalizing a reference
// down to a path relative to the media catalog/product directory.
var mediaPrefixes = []string{
	"catalog/product/",
	"catalog/product",
	"/media/catalog/product/",
	"media/catalog/product/",
}

// ExtractMediaPaths pulls every plausible media reference out of a blob of
// HTML, Markdown or plain text. Both Magento directives and bare paths are
// recognized, because product descriptions in the wild use either.
func ExtractMediaPaths(content string) []string {
	if content == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	for _, m := range mediaDirectiveRe.FindAllStringSubmatch(content, -1) {
		if len(m) > 1 {
			add(cleanRef(strings.TrimSpace(m[1])))
		}
	}
	for _, m := range mediaPathRe.FindAllString(content, -1) {
		add(cleanRef(m))
	}
	return out
}

// cleanRef trims query strings and fragments that sometimes ride along in
// rendered HTML (e.g. ?v=2 cache busters).
func cleanRef(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "\"")
}

// NormalizeMediaPath converts a stored reference into the relative path that
// filepath.WalkDir reports under the media catalog/product directory.
//
// Examples:
//
//	/h/-/h-abc-b1.jpg                        -> h/-/h-abc-b1.jpg
//	/catalog/product/h/-/h-abc-b1.jpg        -> h/-/h-abc-b1.jpg
//	https://shop.test/media/catalog/product/h/-/x.jpg -> h/-/x.jpg
func NormalizeMediaPath(raw string) string {
	s := cleanRef(raw)
	if s == "" {
		return ""
	}
	// Absolute URL: keep only the path component.
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil && u.Path != "" {
			s = u.Path
		}
	}
	// Strip a leading /media/ (or /pub/media/) segment before the known prefixes.
	if i := strings.Index(s, "/media/"); i >= 0 {
		s = s[i+len("/media/"):]
	}
	s = strings.TrimPrefix(s, "/")
	for _, p := range mediaPrefixes {
		if strings.HasPrefix(s, p) {
			s = strings.TrimPrefix(s, p)
			break
		}
	}
	s = strings.TrimPrefix(s, "/")
	return path.Clean(s)
}

// CandidatePaths returns every shape a reference could take on disk.
//
// This matters because the database stores decoded paths while CMS content
// often stores percent-encoded ones, and operators should never lose images
// merely because of an encoding mismatch. A reference counts as live when
// *any* candidate resolves to a file on disk.
func CandidatePaths(raw string) []string {
	out := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(s string) {
		if s == "" || s == "." {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}

	norm := NormalizeMediaPath(raw)
	add(norm)

	if decoded, err := url.PathUnescape(norm); err == nil && decoded != norm {
		add(path.Clean(decoded))
	}
	// Some installs store a doubled prefix after a botched import.
	add(strings.TrimPrefix(norm, "pub/"))
	add(strings.TrimPrefix(norm, "media/"))

	return out
}
