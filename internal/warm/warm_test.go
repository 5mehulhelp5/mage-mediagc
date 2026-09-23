package warm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shuaiZend/mage-mediagc/internal/media"
)

const cachePrefix = "/media/catalog/product/cache/"

// fakeMagento stands in for the storefront. It records every request, and when
// write is set it materializes the cache file the way Magento would, which is
// what lets a test assert that the tool asked for the right thing.
type fakeMagento struct {
	root   string
	write  bool
	status int
	delay  time.Duration

	server *httptest.Server

	mu        sync.Mutex
	seen      map[string]int
	inFlight  int
	maxFlight int
}

func newFakeMagento(t *testing.T, root string) *fakeMagento {
	t.Helper()
	f := &fakeMagento{root: root, write: true, seen: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMagento) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen[r.URL.Path]++
	f.inFlight++
	if f.inFlight > f.maxFlight {
		f.maxFlight = f.inFlight
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.write {
		if rel, ok := strings.CutPrefix(r.URL.Path, cachePrefix); ok {
			if h, rest, found := strings.Cut(rel, "/"); found {
				p := CachePath(f.root, h, rest)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
					_ = os.WriteFile(p, []byte("variant"), 0o644)
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("variant"))
}

func (f *fakeMagento) requests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.seen {
		n += c
	}
	return n
}

func (f *fakeMagento) requested(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seen[path] > 0
}

func (f *fakeMagento) peakInFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxFlight
}

func (f *fakeMagento) base(t *testing.T) *url.URL {
	t.Helper()
	u, err := ParseBaseURL(f.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// makeMedia creates a media tree and returns its root and index.
func makeMedia(t *testing.T, rels ...string) (string, []media.File) {
	t.Helper()
	root := t.TempDir()
	files := make([]media.File, 0, len(rels))
	for _, rel := range rels {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
		files = append(files, media.File{RelPath: rel, Size: 8})
	}
	return root, files
}

func seedCache(t *testing.T, root, hash, rel string) {
	t.Helper()
	p := CachePath(root, hash, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPlanSkipsExistingWithoutRequesting(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg")
	seedCache(t, root, hashA, "a/b/c.jpg")

	base := mustParseURL(t, "https://shop.example.com")
	plan, err := BuildPlan(root, files, []string{hashA}, base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if plan.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", plan.Skipped)
	}
	if len(plan.Items) != 1 || plan.Items[0].RelPath != "a/b/d.jpg" {
		t.Fatalf("Items = %+v, want only a/b/d.jpg", plan.Items)
	}
	if plan.Items[0].CachePath != CachePath(root, hashA, "a/b/d.jpg") {
		t.Errorf("CachePath = %q", plan.Items[0].CachePath)
	}
	if !strings.HasSuffix(plan.Items[0].URL, cachePrefix+hashA+"/a/b/d.jpg") {
		t.Errorf("URL = %q", plan.Items[0].URL)
	}
}

func TestBuildPlanExcludesWhatHasNoCachedForm(t *testing.T) {
	root, files := makeMedia(t,
		"a/b/c.jpg",
		"a/b/c.webp",
		"a/b/notes.txt",
		"placeholder/placeholder.jpg",
		// A planted cache file under a *different* size set, so it cannot
		// double as the cached form of a/b/c.jpg below hashA.
		"cache/"+hashB+"/a/b/c.jpg",
		".htaccess",
	)
	plan, err := BuildPlan(root, files, []string{hashA}, mustParseURL(t, "https://shop.example.com"))
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].RelPath != "a/b/c.jpg" {
		t.Fatalf("Items = %+v, want only a/b/c.jpg", plan.Items)
	}
}

func TestBuildPlanNeedsHashes(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	if _, err := BuildPlan(root, files, nil, mustParseURL(t, "https://shop.example.com")); !errors.Is(err, ErrNoHashes) {
		t.Fatalf("err = %v, want ErrNoHashes", err)
	}
	if _, err := BuildPlan(root, files, []string{"not-a-hash"}, mustParseURL(t, "https://shop.example.com")); err == nil {
		t.Fatal("expected an error for a malformed hash")
	}
}

func TestRunDryRunIssuesNothing(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg")
	fake := newFakeMagento(t, root)

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Pending != 2 || res.Candidates != 2 {
		t.Errorf("Pending/Candidates = %d/%d, want 2/2", res.Pending, res.Candidates)
	}
	if res.Requests != 0 {
		t.Errorf("Requests = %d, want 0", res.Requests)
	}
	if n := fake.requests(); n != 0 {
		t.Fatalf("the server saw %d requests during a dry run, want 0", n)
	}
}

func TestRunAsksOnlyForMissingVariants(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg")
	seedCache(t, root, hashA, "a/b/c.jpg")

	fake := newFakeMagento(t, root)
	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{Concurrency: 2, Timeout: 5 * time.Second, UserAgent: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK != 1 || res.Skipped != 1 || res.Candidates != 2 {
		t.Errorf("OK/Skipped/Candidates = %d/%d/%d, want 1/1/2", res.OK, res.Skipped, res.Candidates)
	}
	if fake.requested(cachePrefix + hashA + "/a/b/c.jpg") {
		t.Error("the already-cached variant was requested; the stat check did not short-circuit it")
	}
	if !fake.requested(cachePrefix + hashA + "/a/b/d.jpg") {
		t.Error("the missing variant was never requested")
	}
	if _, err := os.Stat(CachePath(root, hashA, "a/b/d.jpg")); err != nil {
		t.Errorf("variant was not materialized: %v", err)
	}
}

func TestRunStopsWhenNoFileAppears(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg", "a/b/e.jpg")
	fake := newFakeMagento(t, root)
	fake.write = false

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{Concurrency: 2, Timeout: 5 * time.Second})
	var probe *ErrProbe
	if !errors.As(err, &probe) {
		t.Fatalf("err = %v, want *ErrProbe", err)
	}
	if probe.Status != http.StatusOK {
		t.Errorf("probe Status = %d, want 200", probe.Status)
	}
	if n := fake.requests(); n != 1 {
		t.Fatalf("the server saw %d requests, want exactly the 1 probe", n)
	}
	if res.Requests != 1 {
		t.Errorf("Requests = %d, want 1", res.Requests)
	}
}

func TestRunStopsWhenProbeStatusIsAnError(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	fake := newFakeMagento(t, root)
	fake.status = http.StatusNotFound

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	var probe *ErrProbe
	if _, err := Run(context.Background(), plan, Options{Timeout: 5 * time.Second}); !errors.As(err, &probe) {
		t.Fatalf("err = %v, want *ErrProbe", err)
	} else if probe.Status != http.StatusNotFound {
		t.Errorf("probe Status = %d, want 404", probe.Status)
	}
	if n := fake.requests(); n != 1 {
		t.Fatalf("the server saw %d requests, want 1", n)
	}
	if !strings.Contains(probe.Error(), "404") {
		t.Errorf("probe error should name the status: %v", probe.Error())
	}
}

func TestRunNoProbeSkipsTheCheck(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg")
	fake := newFakeMagento(t, root)
	fake.write = false

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{NoProbe: true, Timeout: 5 * time.Second, MaxErrorFraction: 1})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Probed {
		t.Error("Probed = true with --no-probe")
	}
	if res.Requests != 2 {
		t.Errorf("Requests = %d, want 2", res.Requests)
	}
}

func TestRunRespectsMaxRequests(t *testing.T) {
	root, files := makeMedia(t, "a/b/c1.jpg", "a/b/c2.jpg", "a/b/c3.jpg", "a/b/c4.jpg", "a/b/c5.jpg", "a/b/c6.jpg")
	fake := newFakeMagento(t, root)

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{Concurrency: 2, Timeout: 5 * time.Second, MaxRequests: 4})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
	if res.Requests != 4 {
		t.Errorf("Requests = %d, want 4 (budget is honored exactly)", res.Requests)
	}
	if n := fake.requests(); n != 4 {
		t.Errorf("the server saw %d requests, want 4", n)
	}
}

func TestRunAbortsOnFailureRate(t *testing.T) {
	root, files := makeMedia(t, "a/b/c1.jpg", "a/b/c2.jpg", "a/b/c3.jpg", "a/b/c4.jpg")
	fake := newFakeMagento(t, root)
	fake.status = http.StatusInternalServerError

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	// The probe would stop the run first, so this exercises the rate gate past
	// it — the case where requests work but the responses say something is
	// wrong with the host, the user agent or the pool size.
	res, err := Run(context.Background(), plan, Options{NoProbe: true, Concurrency: 2, Timeout: 5 * time.Second, MaxErrorFraction: 0.05})
	var rate *ErrFailureRate
	if !errors.As(err, &rate) {
		t.Fatalf("err = %v, want *ErrFailureRate", err)
	}
	if rate.Requests != 4 || rate.Failures != 4 {
		t.Errorf("rate = %+v, want 4 failures of 4 requests", rate)
	}
	if res.ServerErrors != 4 {
		t.Errorf("ServerErrors = %d, want 4", res.ServerErrors)
	}
	if res.OK != 0 {
		t.Errorf("OK = %d, want 0", res.OK)
	}
}

func TestRunHonoursConcurrency(t *testing.T) {
	rels := make([]string, 0, 24)
	for i := 0; i < 24; i++ {
		rels = append(rels, "a/b/c"+string(rune('a'+i))+".jpg")
	}
	root, files := makeMedia(t, rels...)
	fake := newFakeMagento(t, root)
	fake.delay = 10 * time.Millisecond

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), plan, Options{
		NoProbe: true, Concurrency: 4, Timeout: 10 * time.Second, MaxErrorFraction: 1,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	peak := fake.peakInFlight()
	if peak > 4 {
		t.Errorf("peak in flight = %d, want at most the configured 4", peak)
	}
	if peak < 2 {
		t.Errorf("peak in flight = %d, want the work to be parallel", peak)
	}
}

func TestRunStopsOnCancellation(t *testing.T) {
	rels := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		rels = append(rels, "a/b/c"+string(rune('a'+i%26))+string(rune('0'+i/26))+".jpg")
	}
	root, files := makeMedia(t, rels...)
	fake := newFakeMagento(t, root)
	fake.delay = 15 * time.Millisecond

	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	res, err := Run(ctx, plan, Options{NoProbe: true, Concurrency: 2, Timeout: 10 * time.Second, MaxErrorFraction: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if res.Requests >= int64(len(plan.Items)) {
		t.Errorf("Requests = %d, want fewer than the %d planned", res.Requests, len(plan.Items))
	}
	if res.Duration <= 0 {
		t.Error("Duration was not stamped on the canceled run")
	}
}

func TestRunWithNothingToDo(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	seedCache(t, root, hashA, "a/b/c.jpg")

	fake := newFakeMagento(t, root)
	plan, err := BuildPlan(root, files, []string{hashA}, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Requests != 0 || res.Probed {
		t.Errorf("res = %+v, want no requests and no probe", res)
	}
	if n := fake.requests(); n != 0 {
		t.Errorf("the server saw %d requests, want 0", n)
	}
}

func TestResultFailureRate(t *testing.T) {
	cases := []struct {
		res  Result
		want float64
	}{
		{Result{}, 0},
		{Result{Requests: 10, OK: 10}, 0},
		{Result{Requests: 10, OK: 7}, 0.3},
		{Result{Requests: 10, TransportErrors: 10}, 1},
	}
	for _, c := range cases {
		if got := c.res.FailureRate(); got != c.want {
			t.Errorf("FailureRate(%+v) = %v, want %v", c.res, got, c.want)
		}
	}
}
