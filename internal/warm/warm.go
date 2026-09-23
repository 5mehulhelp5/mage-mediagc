package warm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

// Item is one cached variant that has to be requested.
type Item struct {
	RelPath   string `json:"relPath"`
	Hash      string `json:"hash"`
	CachePath string `json:"cachePath"`
	URL       string `json:"url"`
}

// Plan is the work a warm run has ahead of it.
type Plan struct {
	MediaRoot string   `json:"mediaRoot"`
	BaseURL   string   `json:"baseUrl"`
	Hashes    []string `json:"hashes"`
	// Items holds the variants that are missing on disk, in the order the
	// scan returned the originals.
	Items []Item `json:"-"`
	// Skipped counts the variants that already exist and therefore need no
	// request at all.
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
	// NoProbe skips the pre-flight check described on Run.
	NoProbe bool
	// DryRun stops after the plan is costed, issuing nothing.
	DryRun   bool
	Progress func(done, total int64, extra string)
}

// Result summarizes a warm run.
type Result struct {
	Candidates      int64         `json:"candidates"`
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

// ErrNoHashes is returned when the size sets could not be determined.
var ErrNoHashes = errors.New("no thumbnail cache hashes: " +
	"the cache directory is empty and no hash was supplied. " +
	"Load one product page and retry, pass --cache-hash, " +
	"or restore a snapshot written with --hash-file")

// ErrProbe reports that the pre-flight check showed requests are not producing
// cache files, which makes the rest of the run pointless.
type ErrProbe struct {
	URL       string
	CachePath string
	Status    int
	Err       error
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
	return fmt.Sprintf(
		"probe request to %s returned HTTP %d but %s was not created. "+
			"Either this installation lays the cache out differently, or something in front of "+
			"the web server answered without reaching PHP (a CDN or reverse proxy). "+
			"Point --base-url at the origin, or pass --no-probe once you have confirmed the mapping by hand",
		e.URL, e.Status, e.CachePath)
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

// BuildPlan pairs every original with every size set and keeps the pairs whose
// cache file is missing.
//
// The existence check is a local stat, not a request. That is what makes an
// interrupted run cheap to pick up again: re-running simply finds less work,
// with no probing and no wasted traffic.
func BuildPlan(mediaRoot string, files []media.File, hashes []string, base *url.URL) (*Plan, error) {
	if len(hashes) == 0 {
		return nil, ErrNoHashes
	}
	for _, h := range hashes {
		if !ValidHash(h) {
			return nil, fmt.Errorf("invalid cache hash %q: expected 32 lowercase hex characters", h)
		}
	}
	p := &Plan{
		MediaRoot: mediaRoot,
		BaseURL:   base.String(),
		Hashes:    append([]string(nil), hashes...),
	}
	for _, f := range files {
		if !warmableFile(f.RelPath) {
			continue
		}
		for _, h := range hashes {
			cachePath := CachePath(mediaRoot, h, f.RelPath)
			if _, err := os.Stat(cachePath); err == nil {
				p.Skipped++
				continue
			}
			p.Items = append(p.Items, Item{
				RelPath:   f.RelPath,
				Hash:      h,
				CachePath: cachePath,
				URL:       CacheURL(base, h, f.RelPath),
			})
		}
	}
	return p, nil
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
		Candidates: plan.Skipped + int64(len(plan.Items)),
		Pending:    int64(len(plan.Items)),
		Skipped:    plan.Skipped,
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
			return res, r.finish(started, opts, &ErrProbe{URL: first.URL, CachePath: first.CachePath, Err: reqErr})
		}
		if code < 200 || code >= 300 {
			return res, r.finish(started, opts, &ErrProbe{URL: first.URL, CachePath: first.CachePath, Status: code})
		}
		if _, statErr := os.Stat(first.CachePath); statErr != nil {
			return res, r.finish(started, opts, &ErrProbe{URL: first.URL, CachePath: first.CachePath, Status: code})
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
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        concurrency * 2,
			MaxIdleConnsPerHost: concurrency,
			MaxConnsPerHost:     concurrency,
			IdleConnTimeout:     30 * time.Second,
			ForceAttemptHTTP2:   true,
			// The bodies are discarded, so asking for gzip would only spend
			// CPU on both ends to inflate nothing.
			DisableCompression: true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
	}
}
