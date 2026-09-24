package warm

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

// HTTP methods the warm run can use.
const (
	// MethodGet downloads the variant. It is the default because it exercises
	// the same path a browser takes, so whatever sits in front of the web
	// server cannot satisfy it from a metadata cache alone.
	MethodGet = "get"
	// MethodHead asks for the headers only. It saves the transfer but a
	// caching proxy or CDN may answer it from the edge without waking the
	// origin, in which case no file is written.
	MethodHead = "head"
)

// ParseMethod normalizes an HTTP method name for the warm run.
func ParseMethod(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", MethodGet:
		return MethodGet, nil
	case MethodHead:
		return MethodHead, nil
	}
	return "", fmt.Errorf("unsupported method %q (want %s or %s)", s, MethodGet, MethodHead)
}

// Item is one original whose derived image has to be requested.
//
// There is deliberately no per-size-set dimension. One request regenerates the
// whole family on Magento 2.3 and later (see the package comment), so the Hash
// below is a route into the resize service rather than a statement about which
// variant is wanted.
type Item struct {
	RelPath string `json:"relPath"`
	Hash    string `json:"hash"`
	// CachePath is the variant the request has to create, and the file whose
	// existence decides whether this item is still outstanding.
	CachePath string `json:"cachePath"`
	// SourcePath is the original on this host, used to diagnose a request that
	// returned 2xx without producing a file.
	SourcePath string `json:"sourcePath"`
	URL        string `json:"url"`
}

// Plan is the work a warm run has ahead of it.
type Plan struct {
	MediaRoot string `json:"mediaRoot"`
	BaseURL   string `json:"baseUrl"`
	// Hash is the single live size set every request is routed through.
	Hash string `json:"hash"`
	// Eligible counts the originals that can be warmed by this route: the ones
	// that Ineligible does not account for.
	Eligible int64 `json:"eligible"`
	// Ineligible counts the image files this route cannot serve, which today
	// means those whose path is not two directories and a file name. They are
	// reported rather than dropped: a plan that quietly covers less than the
	// catalog is worse than one that says so.
	Ineligible int64 `json:"ineligible"`
	// Missing counts the warmable images listed by the caller whose file is
	// not on disk after all.
	//
	// They are excluded rather than requested, because the request cannot
	// succeed: Magento answers a URL whose original it cannot find with the
	// placeholder image and a 200, writing nothing. Asking anyway would book a
	// success for a variant that was never generated, and the gap would only
	// surface later as products showing placeholders.
	//
	// The count is normally zero, since the index comes from a scan of this
	// same tree. It is non-zero when the tree and the index disagree — a copy
	// that did not finish, a mount that is not the one the web server serves,
	// a file removed while the run was starting — and those are exactly the
	// cases worth saying out loud.
	Missing int64 `json:"missing"`
	// Items holds the originals whose variant is missing, in the order the
	// scan returned them.
	Items []Item `json:"-"`
	// Skipped counts the originals that already have a variant under Hash and
	// therefore need no request at all.
	Skipped int64 `json:"skipped"`
}

// Options tunes a warm run.
type Options struct {
	Concurrency int
	Timeout     time.Duration
	Method      string
	UserAgent   string
	// Rate caps request starts per second across all workers. Zero means the
	// concurrency setting is the only limit.
	Rate int
	// MaxRequests bounds one run so a large catalog can be warmed in slices.
	MaxRequests int
	// MaxErrorFraction aborts the run's verdict when the share of failed
	// requests exceeds it.
	MaxErrorFraction float64
	// Resolve replaces the address dialed for a given "host:port", leaving the
	// URL — and therefore the Host header, the TLS server name and the
	// certificate check — untouched. It is what lets a run on the server itself
	// talk to 127.0.0.1 while the shop still sees its own domain, which is the
	// only way one client can serve several vhosts on one host.
	Resolve map[string]string
	// InsecureSkipVerify accepts any TLS certificate.
	//
	// It exists for one situation, and it is a common one: warming a shop on the
	// server it runs on, where the certificate is self-signed for the internal
	// name and no public CA ever validated it. Go rejects a certificate that
	// carries only a Common Name and no subject alternative name outright, so
	// without this such a shop cannot be warmed over https at all — and
	// https is what its own base URL says, so a plain http request is answered
	// with a redirect straight back into the same problem.
	//
	// It is still a real loss of protection: the run can no longer tell the shop
	// from anything else answering on that address. That is why the run reports
	// it, and why it is opt-in rather than implied by Resolve.
	InsecureSkipVerify bool
	// NoProbe skips the pre-flight check described on Run.
	NoProbe bool
	// DryRun stops after the plan is costed, issuing nothing.
	DryRun   bool
	Progress func(done, total int64, extra string)
}

// Result summarizes a warm run.
type Result struct {
	Planned         int64         `json:"planned"`
	Pending         int64         `json:"pending"`
	Skipped         int64         `json:"skipped"`
	Requests        int64         `json:"requests"`
	OK              int64         `json:"ok"`
	ClientErrors    int64         `json:"clientErrors"`
	ServerErrors    int64         `json:"serverErrors"`
	TransportErrors int64         `json:"transportErrors"`
	Other           int64         `json:"other"`
	Truncated       bool          `json:"truncated"`
	Probed          bool          `json:"probed"`
	Duration        time.Duration `json:"duration"`
}

// Failures is the number of requests that did not come back 2xx.
func (r Result) Failures() int64 { return r.Requests - r.OK }

// FailureRate is the share of requests that did not come back 2xx.
func (r Result) FailureRate() float64 {
	if r.Requests == 0 {
		return 0
	}
	return float64(r.Failures()) / float64(r.Requests)
}

// ErrNoHashes is returned when no size set was supplied and none could be read
// off the disk.
var ErrNoHashes = errors.New("no thumbnail cache hash: " +
	"the cache directory holds no size sets and no hash was supplied. " +
	"Load one product page and retry, pass --cache-hash, " +
	"or restore a snapshot written with --hash-file")

// ErrProbe reports that the pre-flight check showed requests are not producing
// cache files, which makes the rest of the run pointless.
type ErrProbe struct {
	URL        string
	CachePath  string
	SourcePath string
	Status     int
	// SourceMissing says the original is not on this host at all, which is a
	// different problem from the storefront declining to generate it. The
	// distinction is the whole reason SourcePath is carried: Magento answers a
	// request whose original it cannot read with the placeholder image and a
	// 200, so the failure looks identical to a caching layer answering from the
	// edge — and the diagnosis for the two is not remotely the same.
	SourceMissing bool
	Err           error
}

func (e *ErrProbe) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("probe request to %s failed: %v", e.URL, e.Err)
	}
	if e.Status < 200 || e.Status >= 300 {
		return fmt.Sprintf(
			"probe request to %s returned HTTP %d. Check --base-url and that the storefront "+
				"is reachable from this host", e.URL, e.Status)
	}
	if e.SourceMissing {
		return fmt.Sprintf(
			"probe request to %s returned HTTP %d but %s was not created, and its original %s "+
				"is not on this host. Magento answers a request for a missing original with "+
				"the placeholder image and HTTP 200, writing nothing, so this response says "+
				"nothing about the storefront. Put the originals in place — or point the tool "+
				"at the media tree the web server actually serves — and run again",
			e.URL, e.Status, e.CachePath, e.SourcePath)
	}
	return fmt.Sprintf(
		"probe request to %s returned HTTP %d but %s was not created. The original is %s. "+
			"Either this installation lays the cache out differently, or something in front of "+
			"the web server answered without reaching PHP (a CDN or reverse proxy). "+
			"Point --base-url at the origin, or pass --no-probe once you have confirmed "+
			"the mapping by hand",
		e.URL, e.Status, e.CachePath, e.SourcePath)
}

// Unwrap exposes the underlying transport error, if any.
func (e *ErrProbe) Unwrap() error { return e.Err }

// ErrFailureRate reports that too many requests failed for the run to count.
type ErrFailureRate struct {
	Failures int64
	Requests int64
	Limit    float64
}

func (e *ErrFailureRate) Error() string {
	return fmt.Sprintf(
		"%d of %d requests failed (%.1f%%), above the %.1f%% threshold. "+
			"This usually means the tool is talking to the wrong host, a WAF is rejecting the "+
			"user agent, or the concurrency is high enough that the pool is refusing connections",
		e.Failures, e.Requests, float64(e.Failures)/float64(e.Requests)*100, e.Limit*100)
}

// BuildPlan pairs every warmable original with the one live size set and keeps
// the ones whose variant is missing.
//
// The existence check is a local stat, not a request. That is what makes an
// interrupted run cheap to pick up again: re-running simply finds less work,
// with no probing and no wasted traffic.
//
// Two things are stat'd, not one. The variant being absent is what makes an
// item outstanding; the original being absent is what makes it impossible, and
// it is counted in Missing rather than requested — see the field comment for
// why answering that request would look like success.
//
// hash must be a set the theme asks for. Anything else still fills the cache
// when requested, but leaves every path in this plan absent, so the run would
// look like it failed at every step — and, worse, would re-request everything
// on the next attempt. LiveHash is what establishes the difference.
func BuildPlan(mediaRoot string, files []media.File, hash string, base *url.URL) (*Plan, error) {
	if hash == "" {
		return nil, ErrNoHashes
	}
	if !ValidHash(hash) {
		return nil, fmt.Errorf("invalid cache hash %q: expected 32 lowercase hex characters", hash)
	}
	p := &Plan{
		MediaRoot: mediaRoot,
		BaseURL:   base.String(),
		Hash:      hash,
	}
	cacheDir := CacheDir(mediaRoot)
	for _, f := range files {
		if !isImageFile(f.RelPath) {
			continue
		}
		if !warmableFile(f.RelPath) {
			p.Ineligible++
			continue
		}
		p.Eligible++
		cachePath := VariantPath(cacheDir, hash, f.RelPath)
		if _, err := os.Stat(cachePath); err == nil {
			p.Skipped++
			continue
		}
		sourcePath := filepath.Join(mediaRoot, filepath.FromSlash(f.RelPath))
		// A request only produces a variant when the resize service can find
		// the original. When it cannot, Magento answers with the placeholder
		// image and a 200 and writes nothing, so requesting this item would
		// book a success for a file that never appeared. Counting it instead
		// turns a silent hole in the cache into a number in the report.
		if !fileExists(sourcePath) {
			p.Missing++
			continue
		}
		p.Items = append(p.Items, Item{
			RelPath:    f.RelPath,
			Hash:       hash,
			CachePath:  cachePath,
			SourcePath: sourcePath,
			URL:        CacheURL(base, hash, f.RelPath),
		})
	}
	return p, nil
}

// isImageFile reports whether a path carries one of the extensions Magento
// resizes. It is the half of warmableFile that says nothing about where the
// file sits, so that a caller can tell "not an image at all" — which is not the
// operator's problem — from "an image this route cannot serve", which is.
func isImageFile(rel string) bool {
	switch strings.ToLower(path.Ext(rel)) {
	case ".jpg", ".jpeg", ".png", ".gif":
		return true
	}
	return false
}

// Seed issues the single request that establishes which size sets the theme
// asks for, and returns one of them.
//
// It is the cold-start path. With an empty cache tree nothing on the host says
// which sets are current, and Magento keeps no such record elsewhere — the hash
// is computed from PHP-side parameters on each request. What the host will
// reveal, though, is the family of sets that one request generates, so asking
// once is enough to find out. See SeedHash.
//
// The request goes through the same client as the run itself, so it honors the
// method, the timeout and any address overrides; it is not a side channel with
// different behavior from the work that follows.
func Seed(ctx context.Context, mediaRoot, relPath string, base *url.URL, opts Options) (string, error) {
	method, err := ParseMethod(opts.Method)
	if err != nil {
		return "", err
	}
	item := Item{
		RelPath:    relPath,
		Hash:       SeedHash,
		CachePath:  CachePath(mediaRoot, SeedHash, relPath),
		SourcePath: filepath.Join(mediaRoot, filepath.FromSlash(relPath)),
		URL:        CacheURL(base, SeedHash, relPath),
	}
	// Same reasoning as the pre-flight probe: a missing original answers 200
	// with the placeholder and writes nothing, so a seed request against one
	// would be blamed on the storefront rather than on the file that is not
	// there.
	if !fileExists(item.SourcePath) {
		return "", fmt.Errorf("cannot ask the shop which size sets it uses: the original %s "+
			"is not on this host", item.SourcePath)
	}
	r := &runner{client: newClient(opts), method: method, ua: opts.UserAgent, res: &Result{}}
	code, reqErr := r.fetch(ctx, item)
	if reqErr != nil {
		return "", fmt.Errorf("seed request to %s failed: %w", item.URL, reqErr)
	}
	if code < 200 || code >= 300 {
		return "", fmt.Errorf("seed request to %s returned HTTP %d; "+
			"check --base-url and that the storefront is reachable from this host", item.URL, code)
	}
	live, liveErr := LiveHash(CacheDir(mediaRoot), relPath)
	if liveErr != nil {
		return "", fmt.Errorf("the seed request to %s returned HTTP %d but no variant appeared "+
			"under %s, so this storefront did not generate one. %w",
			item.URL, code, CacheDir(mediaRoot), liveErr)
	}
	return live, nil
}

// Run issues the planned requests.
//
// Before the pool starts, one request is made on its own and the expected cache
// file is checked for. A run that produces no file is stopped right there,
// because the alternative is a few hundred thousand requests that all return
// 200 and produce nothing — the failure mode a caching layer in front of the
// web server creates, and the one that looks most like success.
func Run(ctx context.Context, plan *Plan, opts Options) (*Result, error) {
	started := time.Now()
	res := &Result{
		Planned: plan.Skipped + int64(len(plan.Items)),
		Pending: int64(len(plan.Items)),
		Skipped: plan.Skipped,
	}
	// The method is resolved before the dry-run short circuit on purpose. A
	// dry run exists to surface exactly this kind of mistake while it still
	// costs nothing; deferring the check to the applying run would mean the
	// rehearsal passes and the real run fails.
	method, err := ParseMethod(opts.Method)
	if err != nil {
		return res, err
	}

	if opts.DryRun || len(plan.Items) == 0 {
		res.Duration = time.Since(started)
		return res, nil
	}

	r := &runner{
		client: newClient(opts),
		method: method,
		ua:     opts.UserAgent,
		res:    res,
	}

	start := 0
	// issued counts the requests behind us at this point, which is the probe
	// and nothing else: the pool has not started. It is an int rather than a
	// read of res.Requests so that the budget arithmetic below stays in int,
	// with no narrowing conversion of a counter for a reader — or an analyzer —
	// to have to bound by hand.
	issued := 0
	if !opts.NoProbe {
		first := plan.Items[0]
		code, reqErr := r.fetch(ctx, first)
		atomic.AddInt64(&res.Requests, 1)
		issued = 1
		res.Probed = true
		r.tally(code, reqErr)
		if reqErr != nil {
			return res, r.finish(started, opts, &ErrProbe{
				URL: first.URL, CachePath: first.CachePath, SourcePath: first.SourcePath, Err: reqErr})
		}
		if code < 200 || code >= 300 {
			return res, r.finish(started, opts, &ErrProbe{
				URL: first.URL, CachePath: first.CachePath, SourcePath: first.SourcePath, Status: code})
		}
		if _, statErr := os.Stat(first.CachePath); statErr != nil {
			// The original was present when the plan was built, so its
			// absence here means it went away in between. Stat'ing it is one
			// call, and it turns "something answered without reaching PHP"
			// into the right answer when that is not what happened.
			return res, r.finish(started, opts, &ErrProbe{
				URL: first.URL, CachePath: first.CachePath, SourcePath: first.SourcePath,
				Status: code, SourceMissing: !fileExists(first.SourcePath)})
		}
		start = 1
	}

	allowed := len(plan.Items) - start
	if opts.MaxRequests > 0 {
		// The budget counts every request the run makes, the probe included,
		// and it can only ever shrink the prefix length below.
		if budget := opts.MaxRequests - issued; budget < allowed {
			allowed = budget
			res.Truncated = true
		}
	}
	if allowed < 0 {
		allowed = 0
	}

	var tick <-chan time.Time
	if opts.Rate > 0 {
		t := time.NewTicker(time.Second / time.Duration(opts.Rate))
		defer t.Stop()
		tick = t.C
	}

	items := plan.Items[start : start+allowed]
	total := int64(len(plan.Items))

	var wg sync.WaitGroup
	jobs := make(chan Item)
	workers := opts.Concurrency
	if workers < 1 {
		workers = 1
	}
	if workers > len(items) && len(items) > 0 {
		workers = len(items)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if ctx.Err() != nil {
					return
				}
				if tick != nil {
					select {
					case <-ctx.Done():
						return
					case <-tick:
					}
				}
				code, reqErr := r.fetch(ctx, item)
				n := atomic.AddInt64(&res.Requests, 1)
				r.tally(code, reqErr)
				if p := opts.Progress; p != nil && n%200 == 0 {
					p(n, total, "")
				}
			}
		}()
	}

feed:
	for _, item := range items {
		select {
		case <-ctx.Done():
			break feed
		case jobs <- item:
		}
	}
	close(jobs)
	wg.Wait()

	if p := opts.Progress; p != nil {
		p(atomic.LoadInt64(&res.Requests), total, "")
	}

	if err := ctx.Err(); err != nil {
		res.Duration = time.Since(started)
		return res, err
	}
	return res, r.finish(started, opts, nil)
}

// finish stamps the duration and applies the failure-rate threshold.
func (r *runner) finish(started time.Time, opts Options, err error) error {
	r.res.Duration = time.Since(started)
	if err != nil {
		return err
	}
	limit := opts.MaxErrorFraction
	if limit <= 0 {
		limit = 0.05
	}
	if r.res.Requests > 0 && r.res.FailureRate() > limit {
		return &ErrFailureRate{
			Failures: r.res.Failures(),
			Requests: r.res.Requests,
			Limit:    limit,
		}
	}
	return nil
}

// runner carries the per-run state that the workers share.
type runner struct {
	client *http.Client
	method string
	ua     string
	res    *Result
}

// fetch issues one request. A status of 0 means the request never completed.
func (r *runner) fetch(ctx context.Context, item Item) (int, error) {
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(r.method), item.URL, nil)
	if err != nil {
		return 0, err
	}
	if r.ua != "" {
		req.Header.Set("User-Agent", r.ua)
	}
	if r.method == MethodGet {
		req.Header.Set("Accept", "image/*")
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() {
		if r.method == MethodGet {
			// Reading the body out lets the connection be reused, which
			// matters far more than the bytes: a catalog rebuild is hundreds
			// of thousands of requests, and a fresh TLS handshake for each of
			// them would cost more than the transfers.
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		_ = resp.Body.Close()
	}()
	return resp.StatusCode, nil
}

// tally books one attempt. Requests counts every attempt, so that transport
// failures are part of the failure rate rather than invisible.
func (r *runner) tally(code int, err error) {
	if err != nil {
		atomic.AddInt64(&r.res.TransportErrors, 1)
		return
	}
	switch {
	case code >= 200 && code < 300:
		atomic.AddInt64(&r.res.OK, 1)
	case code >= 400 && code < 500:
		atomic.AddInt64(&r.res.ClientErrors, 1)
	case code >= 500:
		atomic.AddInt64(&r.res.ServerErrors, 1)
	default:
		atomic.AddInt64(&r.res.Other, 1)
	}
}

// newClient builds the connection pool the workers share.
func newClient(opts Options) *http.Client {
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	tr := &http.Transport{
		MaxIdleConns:        concurrency * 2,
		MaxIdleConnsPerHost: concurrency,
		MaxConnsPerHost:     concurrency,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
		// The bodies are discarded, so asking for gzip would only spend
		// CPU on both ends to inflate nothing.
		DisableCompression: true,
	}
	if opts.InsecureSkipVerify {
		// The name is the point: this is not "trust more", it is "stop
		// checking". See the field comment for when that is the only way
		// forward, and why the run says so in its output.
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in, reported, documented
	}
	if len(opts.Resolve) > 0 {
		// An address override is a statement about where the connection goes,
		// and a proxy would put a third party in the middle of it. Worse, it
		// would do so invisibly: the environment's HTTP_PROXY is consulted
		// before the dialer, so a --loopback run on a host with a proxy
		// configured would still send its traffic off the machine, which is the
		// single thing the flag exists to prevent. So the override wins.
		tr.Proxy = nil
		tr.DialContext = dialResolver(opts.Resolve)
	} else {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
}

// dialResolver returns a dialer that substitutes the address for the ones it
// was given, the way curl's --resolve does.
//
// The substitution happens at the socket, so the request keeps the URL's host:
// the Host header, the TLS server name and the certificate check all still
// speak the shop's own domain. That is the whole point. Rewriting the URL or
// pointing the name at loopback in /etc/hosts would send the request to
// whichever vhost the server treats as default, which is wrong the moment a host
// serves more than one site — and it is wrong silently, because the default
// vhost answers perfectly well.
//
// The lookup key is the literal "host:port" from the URL, which is also what
// http.Transport passes here, so no port defaulting is needed on this side.
func dialResolver(resolve map[string]string) func(context.Context, string, string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if target, ok := resolve[addr]; ok {
			addr = target
		}
		return dialer.DialContext(ctx, network, addr)
	}
}

// ResolveEntry parses one "host:port:address" mapping.
//
// The shape is the one curl uses for --resolve, minus the separate port on the
// right-hand side: the replacement is an address, and the port is kept from the
// left. Formatting it that way makes the common case — this host, but on the
// loopback interface — a single readable value, which is what an operator
// typing it into a runbook wants to see. An IPv6 address is written in brackets,
// as it is anywhere else it appears next to a port.
func ResolveEntry(raw string) (hostPort, addr string, err error) {
	host, port, ip, err := splitResolve(raw)
	if err != nil {
		return "", "", err
	}
	if host == "" {
		return "", "", fmt.Errorf("--resolve %q: host is empty", raw)
	}
	n, convErr := strconv.Atoi(port)
	if convErr != nil || n < 1 || n > 65535 {
		return "", "", fmt.Errorf("--resolve %q: %q is not a port", raw, port)
	}
	if ip == "" {
		return "", "", fmt.Errorf("--resolve %q: address is empty", raw)
	}
	if net.ParseIP(ip) == nil {
		return "", "", fmt.Errorf("--resolve %q: %q is not an IP address", raw, ip)
	}
	return net.JoinHostPort(host, port), net.JoinHostPort(ip, port), nil
}

// splitResolve splits "host:port:address" on its colons, allowing the address
// to be a bracketed IPv6 literal.
func splitResolve(raw string) (host, port, ip string, err error) {
	s := strings.TrimSpace(raw)
	if end := strings.LastIndex(s, "]"); end >= 0 {
		start := strings.LastIndex(s[:end], "[")
		if start < 0 {
			return "", "", "", fmt.Errorf("--resolve %q: unmatched ]", raw)
		}
		ip = s[start+1 : end]
		head := strings.TrimSuffix(strings.TrimSpace(s[:start]), ":")
		parts := strings.Split(head, ":")
		if len(parts) != 2 {
			return "", "", "", fmt.Errorf("--resolve %q: want host:port:[address]", raw)
		}
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), ip, nil
	}
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("--resolve %q: want host:port:address", raw)
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2]), nil
}

// ParseResolveEntries builds the address map from repeated --resolve values.
func ParseResolveEntries(raw []string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(raw))
	for _, entry := range raw {
		if strings.TrimSpace(entry) == "" {
			continue
		}
		hostPort, addr, err := ResolveEntry(entry)
		if err != nil {
			return nil, err
		}
		out[hostPort] = addr
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// LoopbackResolve returns the mapping that sends a storefront's traffic to the
// loopback interface while leaving the request's identity alone.
//
// Both default ports are mapped, not only the one the base URL uses, because a
// shop configured with an https base URL answers a plain http request with a
// redirect to its own name — and a mapping that covered only 443 would send that
// redirect straight back out to the internet, where the same host answers over
// whatever address it has, defeating the point of the exercise.
func LoopbackResolve(base *url.URL) map[string]string {
	host := base.Hostname()
	if host == "" {
		return nil
	}
	port := base.Port()
	if port == "" {
		if base.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	out := map[string]string{
		net.JoinHostPort(host, port): net.JoinHostPort("127.0.0.1", port),
	}
	for _, p := range []string{"80", "443"} {
		out[net.JoinHostPort(host, p)] = net.JoinHostPort("127.0.0.1", p)
	}
	return out
}
