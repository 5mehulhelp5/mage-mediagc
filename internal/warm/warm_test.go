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
	// family, when set, makes the fake behave the way Magento 2.3 and later
	// do: one request writes the variant under every size set in family,
	// ignoring the hash that was asked for. That is the property the whole
	// design rests on, so it is worth being able to reproduce it exactly.
	family []string

	server *httptest.Server

	mu        sync.Mutex
	seen      map[string]int
	hosts     map[string]int
	inFlight  int
	maxFlight int
}

func newFakeMagento(t *testing.T, root string) *fakeMagento {
	t.Helper()
	f := &fakeMagento{root: root, write: true, seen: map[string]int{}, hosts: map[string]int{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeMagento) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.seen[r.URL.Path]++
	f.hosts[r.Host]++
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
			if asked, rest, found := strings.Cut(rel, "/"); found {
				targets := []string{asked}
				if len(f.family) > 0 {
					targets = f.family
				}
				for _, h := range targets {
					p := CachePath(f.root, h, rest)
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
						_ = os.WriteFile(p, []byte("variant"), 0o644)
					}
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("variant"))
}

// hostsSeen returns the Host header values the server received, which is how a
// test checks that an address override left the request's identity alone.
func (f *fakeMagento) hostsSeen() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]int, len(f.hosts))
	for k, v := range f.hosts {
		out[k] = v
	}
	return out
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
	plan, err := BuildPlan(root, files, hashA, base)
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
	plan, err := BuildPlan(root, files, hashA, mustParseURL(t, "https://shop.example.com"))
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if len(plan.Items) != 1 || plan.Items[0].RelPath != "a/b/c.jpg" {
		t.Fatalf("Items = %+v, want only a/b/c.jpg", plan.Items)
	}
}

func TestBuildPlanNeedsHashes(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	if _, err := BuildPlan(root, files, "", mustParseURL(t, "https://shop.example.com")); !errors.Is(err, ErrNoHashes) {
		t.Fatalf("err = %v, want ErrNoHashes", err)
	}
	if _, err := BuildPlan(root, files, "not-a-hash", mustParseURL(t, "https://shop.example.com")); err == nil {
		t.Fatal("expected an error for a malformed hash")
	}
}

// One request regenerates every size set, so the plan holds one item per
// original regardless of how many sets the theme defines. Getting this wrong
// inflates the job by the number of sets, which is the whole reason the
// command is practical.
func TestBuildPlanHasOneItemPerOriginal(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg", "a/b/e.jpg")
	plan, err := BuildPlan(root, files, hashA, mustParseURL(t, "https://shop.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 3 {
		t.Fatalf("Items = %d, want 3, one per original", len(plan.Items))
	}
	seen := map[string]bool{}
	for _, it := range plan.Items {
		if seen[it.RelPath] {
			t.Errorf("%s appears twice in the plan", it.RelPath)
		}
		seen[it.RelPath] = true
		if it.Hash != hashA {
			t.Errorf("Hash = %q, want %q", it.Hash, hashA)
		}
		if it.SourcePath == "" {
			t.Errorf("%s has no source path to diagnose with", it.RelPath)
		}
		if !strings.HasSuffix(it.SourcePath, filepath.FromSlash(it.RelPath)) {
			t.Errorf("SourcePath = %q, does not end in %q", it.SourcePath, it.RelPath)
		}
	}
	if plan.Eligible != 3 || plan.Ineligible != 0 {
		t.Errorf("Eligible/Ineligible = %d/%d, want 3/0", plan.Eligible, plan.Ineligible)
	}
}

// Magento resolves the original from the last three segments of the request
// path, so an image at any other depth would be requested against a different
// image. Those files are counted rather than planned.
func TestBuildPlanExcludesImagesAtTheWrongDepth(t *testing.T) {
	root, files := makeMedia(t,
		"a/b/c.jpg",   // two directories and a file: the layout Magento writes
		"a/c.jpg",     // too shallow
		"a/b/c/d.jpg", // too deep
		"c.jpg",       // no directory at all
		"notes.txt",   // not an image, and not the plan's problem either
	)
	plan, err := BuildPlan(root, files, hashA, mustParseURL(t, "https://shop.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 1 || plan.Items[0].RelPath != "a/b/c.jpg" {
		t.Fatalf("Items = %+v, want only a/b/c.jpg", plan.Items)
	}
	if plan.Eligible != 1 {
		t.Errorf("Eligible = %d, want 1", plan.Eligible)
	}
	if plan.Ineligible != 3 {
		t.Errorf("Ineligible = %d, want 3 (the three images at the wrong depth; "+
			"notes.txt is not an image and is not counted)", plan.Ineligible)
	}
}

func TestRunDryRunIssuesNothing(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg", "a/b/d.jpg")
	fake := newFakeMagento(t, root)

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Pending != 2 || res.Planned != 2 {
		t.Errorf("Pending/Planned = %d/%d, want 2/2", res.Pending, res.Planned)
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
	plan, err := BuildPlan(root, files, hashA, fake.base(t))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{Concurrency: 2, Timeout: 5 * time.Second, UserAgent: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK != 1 || res.Skipped != 1 || res.Planned != 2 {
		t.Errorf("OK/Skipped/Planned = %d/%d/%d, want 1/1/2", res.OK, res.Skipped, res.Planned)
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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
	plan, err := BuildPlan(root, files, hashA, fake.base(t))
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

// The hash in the URL is not validated by Magento: any value reaches the resize
// service and produces the whole family of sets. The corollary is what makes
// Seed necessary — the path that was actually asked for does not appear, so a
// made-up hash cannot be used to build a plan.
func TestSeedFindsTheLiveSetWhenTheRequestedHashIsIgnored(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	fake := newFakeMagento(t, root)
	fake.family = []string{hashB, hashA}

	live, err := Seed(context.Background(), root, files[0].RelPath, fake.base(t), Options{
		Timeout: 5 * time.Second, UserAgent: "test",
	})
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	if live != hashA {
		t.Fatalf("Seed = %q, want %q (the first live set in sorted order)", live, hashA)
	}
	if n := fake.requests(); n != 1 {
		t.Errorf("Seed issued %d requests, want exactly 1", n)
	}
	// The variant exists under the live set, and the plan built from it will
	// therefore see its own stat check succeed.
	if _, err := os.Stat(CachePath(root, hashA, "a/b/c.jpg")); err != nil {
		t.Errorf("the seeded variant is not where the plan will look for it: %v", err)
	}
}

// A request for a set the shop does not use produces nothing at the path asked
// for, which is exactly the state that must not be mistaken for a working run.
func TestSeedReportsWhenNothingAppears(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	fake := newFakeMagento(t, root)
	fake.write = false

	_, err := Seed(context.Background(), root, files[0].RelPath, fake.base(t), Options{Timeout: 5 * time.Second})
	if !errors.Is(err, ErrNoLiveHash) {
		t.Fatalf("err = %v, want ErrNoLiveHash", err)
	}
	if !strings.Contains(err.Error(), "did not generate") {
		t.Errorf("the error should say the shop generated nothing: %v", err)
	}
}

func TestSeedReportsATransportFailure(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	base := mustParseURL(t, "http://127.0.0.1:1")
	if _, err := Seed(context.Background(), root, files[0].RelPath, base, Options{Timeout: 2 * time.Second}); err == nil {
		t.Fatal("expected an error when the storefront is unreachable")
	}
}

// The address override has to change where the connection goes and nothing
// else. If it also changed the Host header, a host serving several shops would
// answer for the wrong one — the failure mode that makes the /etc/hosts
// workaround unsafe in the first place.
func TestRunDialsTheResolvedAddressAndKeepsTheHost(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")
	fake := newFakeMagento(t, root)

	// A name that cannot resolve is the point: reaching the server at all
	// proves the dial target was substituted.
	port := strings.TrimPrefix(fake.server.URL, "http://127.0.0.1:")
	base := mustParseURL(t, "http://shop.example.invalid")
	plan, err := BuildPlan(root, files, hashA, base)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(context.Background(), plan, Options{
		Timeout:          5 * time.Second,
		MaxErrorFraction: 1,
		Resolve:          map[string]string{"shop.example.invalid:80": "127.0.0.1:" + port},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.OK != 1 {
		t.Fatalf("OK = %d, want 1: the request never reached the substituted address", res.OK)
	}
	hosts := fake.hostsSeen()
	if len(hosts) != 1 {
		t.Fatalf("Host headers seen = %v, want exactly shop.example.invalid", hosts)
	}
	if _, ok := hosts["shop.example.invalid"]; !ok {
		t.Errorf("Host = %v, want shop.example.invalid: the override leaked into the request's identity", hosts)
	}
}

func TestResolveEntry(t *testing.T) {
	good := []struct {
		in       string
		hostPort string
		addr     string
	}{
		{"shop.example.com:443:127.0.0.1", "shop.example.com:443", "127.0.0.1:443"},
		{"shop.example.com:8080:10.0.0.5", "shop.example.com:8080", "10.0.0.5:8080"},
		{" shop.example.com : 443 : 127.0.0.1 ", "shop.example.com:443", "127.0.0.1:443"},
		{"shop.example.com:443:[::1]", "shop.example.com:443", "[::1]:443"},
	}
	for _, c := range good {
		hostPort, addr, err := ResolveEntry(c.in)
		if err != nil {
			t.Errorf("ResolveEntry(%q): %v", c.in, err)
			continue
		}
		if hostPort != c.hostPort || addr != c.addr {
			t.Errorf("ResolveEntry(%q) = %q, %q; want %q, %q", c.in, hostPort, addr, c.hostPort, c.addr)
		}
	}
	for _, bad := range []string{"", "shop.example.com:443", ":443:127.0.0.1", "shop.example.com::127.0.0.1",
		"shop.example.com:0:127.0.0.1", "shop.example.com:70000:127.0.0.1", "shop.example.com:443:localhost",
		"shop.example.com:443:127.0.0.1:extra", "shop.example.com:443:[::1", "shop.example.com:443:::1"} {
		if _, _, err := ResolveEntry(bad); err == nil {
			t.Errorf("ResolveEntry(%q) should fail", bad)
		}
	}
}

func TestParseResolveEntries(t *testing.T) {
	got, err := ParseResolveEntries([]string{"a.example:443:127.0.0.1", "", "  ", "b.example:80:10.0.0.2"})
	if err != nil {
		t.Fatalf("ParseResolveEntries: %v", err)
	}
	if len(got) != 2 || got["a.example:443"] != "127.0.0.1:443" || got["b.example:80"] != "10.0.0.2:80" {
		t.Fatalf("got %v", got)
	}
	if out, err := ParseResolveEntries(nil); err != nil || out != nil {
		t.Errorf("ParseResolveEntries(nil) = %v, %v; want nil, nil", out, err)
	}
	if _, err := ParseResolveEntries([]string{"broken"}); err == nil {
		t.Error("a malformed entry should fail rather than be dropped silently")
	}
}

// Both default ports are mapped, not only the one the base URL names: a shop
// with an https base URL answers a plain http request with a redirect to its
// own name, and a mapping that covered only 443 would send that redirect back
// out to the internet.
func TestLoopbackResolve(t *testing.T) {
	cases := []struct {
		raw  string
		want map[string]string
	}{
		{
			"https://shop.example.com",
			map[string]string{
				"shop.example.com:443": "127.0.0.1:443",
				"shop.example.com:80":  "127.0.0.1:80",
			},
		},
		{
			"http://shop.example.com",
			map[string]string{
				"shop.example.com:80":  "127.0.0.1:80",
				"shop.example.com:443": "127.0.0.1:443",
			},
		},
		{
			"https://shop.example.com:8443/sub",
			map[string]string{
				"shop.example.com:8443": "127.0.0.1:8443",
				"shop.example.com:80":   "127.0.0.1:80",
				"shop.example.com:443":  "127.0.0.1:443",
			},
		},
	}
	for _, c := range cases {
		got := LoopbackResolve(mustParseURL(t, c.raw))
		if len(got) != len(c.want) {
			t.Errorf("LoopbackResolve(%q) = %v, want %v", c.raw, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("LoopbackResolve(%q)[%s] = %q, want %q", c.raw, k, got[k], v)
			}
		}
	}
}

// A shop reached over https with a certificate that no public CA signed cannot
// be warmed at all without an escape hatch: Go rejects a self-signed
// certificate outright. The option has to be opt-in, and it has to actually
// work, because "run this on your own server" is the primary use case.
func TestInsecureSkipVerifyAllowsASelfSignedStorefront(t *testing.T) {
	root, files := makeMedia(t, "a/b/c.jpg")

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rel, ok := strings.CutPrefix(r.URL.Path, cachePrefix); ok {
			if h, rest, found := strings.Cut(rel, "/"); found {
				p := CachePath(root, h, rest)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
					_ = os.WriteFile(p, []byte("variant"), 0o644)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	base, err := ParseBaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	plan, err := BuildPlan(root, files, hashA, base)
	if err != nil {
		t.Fatal(err)
	}
	// Without the option the pre-flight request fails on the certificate, so
	// the run stops before issuing anything else.
	var probe *ErrProbe
	if _, strictErr := Run(context.Background(), plan, Options{
		Timeout: 5 * time.Second, MaxErrorFraction: 1,
	}); !errors.As(strictErr, &probe) {
		t.Fatalf("err = %v, want *ErrProbe from the rejected certificate", strictErr)
	}

	plan, err = BuildPlan(root, files, hashA, base)
	if err != nil {
		t.Fatal(err)
	}
	relaxed, err := Run(context.Background(), plan, Options{
		Timeout: 5 * time.Second, MaxErrorFraction: 1, InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("Run with InsecureSkipVerify: %v", err)
	}
	if relaxed.OK != 1 {
		t.Fatalf("OK = %d, want 1: the option did not take effect", relaxed.OK)
	}
}
