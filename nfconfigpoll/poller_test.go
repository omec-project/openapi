// Copyright (C) 2026 Intel Corporation
// SPDX-FileCopyrightText: 2025 Canonical Ltd
// SPDX-License-Identifier: Apache-2.0

package nfconfigpoll

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omec-project/openapi/v2/models"
	"github.com/omec-project/openapi/v2/nfConfigApi"
)

func TestFetchJSON(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		status      int
		body        string
		want        []models.PlmnId
		wantErr     bool
	}{
		{"ok", contentTypeJSON, 200, `[{"mcc":"208","mnc":"93"}]`, []models.PlmnId{{Mcc: "208", Mnc: "93"}}, false},
		{"ok with charset", "application/json; charset=utf-8", 200, `[]`, []models.PlmnId{}, false},
		{"wrong content type", "text/plain", 200, `[]`, nil, true},
		{"media type is case-insensitive", "Application/JSON", 200, `[]`, []models.PlmnId{}, false},
		{"similar media type", "application/jsonp", 200, `[]`, nil, true},
		{"media type only in parameter", "text/plain; note=application/json", 200, `[]`, nil, true},
		{"missing content type", "", 200, `[]`, nil, true},
		{"bad request", contentTypeJSON, 400, ``, nil, true},
		{"internal error", contentTypeJSON, 500, ``, nil, true},
		{"unexpected status", contentTypeJSON, 404, ``, nil, true},
		{"invalid json", contentTypeJSON, 200, `{`, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Accept"); got != contentTypeJSON {
					t.Errorf("Accept = %q", got)
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Errorf("write response: %v", err)
				}
			}))
			defer srv.Close()

			got, err := FetchJSON[[]models.PlmnId](context.Background(), srv.Client(), srv.URL)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && len(got) != len(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFetchJSON_UnreachableServer(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if _, err := FetchJSON[[]models.PlmnId](context.Background(), http.DefaultClient, url); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchJSON_DecodesNfConfigApiTypes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSON)
		if _, err := w.Write([]byte(`[{"plmnId":{"mcc":"208","mnc":"93"},"snssai":{"sst":1}}]`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	got, err := FetchJSON[[]nfConfigApi.AccessAndMobility](context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PlmnId.GetMcc() != "208" {
		t.Errorf("unexpected decode: %+v", got)
	}
}

func TestNew_ValidatesRequiredFields(t *testing.T) {
	full := Config[[]string]{Name: "x", Endpoint: "http://x", OnChange: func(context.Context, []string) error { return nil }}
	for name, mutate := range map[string]func(*Config[[]string]){
		"Name":     func(c *Config[[]string]) { c.Name = "" },
		"Endpoint": func(c *Config[[]string]) { c.Endpoint = "" },
		"OnChange": func(c *Config[[]string]) { c.OnChange = nil },
	} {
		c := full
		mutate(&c)
		if _, err := New(c); err == nil {
			t.Errorf("expected error when %s is missing", name)
		}
	}
	if _, err := New(full); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// fetcher serves queued results, then repeats the last one.
type fetcher struct {
	mu      sync.Mutex
	results []fetchResult
	calls   atomic.Int32
	times   []time.Time
}

type fetchResult struct {
	cfg []string
	err error
}

func (f *fetcher) fetch(ctx context.Context, url string) ([]string, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.times = append(f.times, time.Now())
	r := f.results[0]
	if len(f.results) > 1 {
		f.results = f.results[1:]
	}
	return r.cfg, r.err
}

type recorder struct {
	mu   sync.Mutex
	got  [][]string
	errs []error // consumed one per call
}

func (r *recorder) onChange(ctx context.Context, cfg []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, cfg)
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		return err
	}
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(msg)
}

func run(t *testing.T, cfg Config[[]string]) (stop func()) {
	t.Helper()
	cfg.Name, cfg.Endpoint = "TEST", "http://test"
	if cfg.Interval == 0 {
		cfg.Interval = 2 * time.Millisecond
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Run did not return after cancel")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestRun_CallsOnChangeOnlyWhenConfigChanged(t *testing.T) {
	f := &fetcher{results: []fetchResult{
		{cfg: []string{"a"}}, {cfg: []string{"a"}}, {cfg: []string{"b"}}, {cfg: []string{"b"}},
	}}
	rec := &recorder{}
	run(t, Config[[]string]{Fetch: f.fetch, OnChange: rec.onChange, Initial: []string{}})

	waitFor(t, func() bool { return rec.count() == 2 && f.calls.Load() >= 4 }, "expected exactly a and b to be delivered")
	time.Sleep(10 * time.Millisecond)
	if rec.count() != 2 {
		t.Errorf("OnChange calls = %d, want 2: %v", rec.count(), rec.got)
	}
}

func TestRun_FirstEmptyPollMatchesInitial(t *testing.T) {
	f := &fetcher{results: []fetchResult{{cfg: []string{}}}}
	rec := &recorder{}
	run(t, Config[[]string]{Fetch: f.fetch, OnChange: rec.onChange, Initial: []string{}})

	waitFor(t, func() bool { return f.calls.Load() >= 3 }, "no polls")
	if rec.count() != 0 {
		t.Errorf("OnChange called for config equal to Initial: %v", rec.got)
	}
}

func TestRun_FetchErrorBacksOffThenRecovers(t *testing.T) {
	boom := errors.New("boom")
	f := &fetcher{results: []fetchResult{{err: boom}, {err: boom}, {err: boom}, {cfg: []string{"a"}}}}
	rec := &recorder{}
	run(t, Config[[]string]{
		Interval: 4 * time.Millisecond, MaxBackoff: 16 * time.Millisecond,
		Fetch: f.fetch, OnChange: rec.onChange, Initial: []string{},
	})

	waitFor(t, func() bool { return rec.count() == 1 }, "config was not delivered after errors")
	f.mu.Lock()
	defer f.mu.Unlock()
	// intervals before polls 2,3,4 are 8ms, 16ms, 16ms (capped); allow generous scheduling slack
	for i, want := range []time.Duration{8, 16, 16} {
		got := f.times[i+1].Sub(f.times[i])
		if got < want*time.Millisecond*9/10 {
			t.Errorf("gap before poll %d = %v, want at least ~%dms", i+2, got, want)
		}
	}
}

func TestRun_OnChangeError_RetriesSameConfigNextPoll(t *testing.T) {
	f := &fetcher{results: []fetchResult{{cfg: []string{"a"}}}}
	rec := &recorder{errs: []error{errors.New("channel full")}}
	run(t, Config[[]string]{Fetch: f.fetch, OnChange: rec.onChange, Initial: []string{}})

	waitFor(t, func() bool { return rec.count() == 2 }, "config was not redelivered after OnChange failure")
	time.Sleep(10 * time.Millisecond)
	if rec.count() != 2 {
		t.Errorf("OnChange calls = %d, want 2 (failure then success, then deduped)", rec.count())
	}
}

func TestRun_Normalize_AppliedBeforeCompareAndDelivery(t *testing.T) {
	f := &fetcher{results: []fetchResult{{cfg: []string{"A"}}, {cfg: []string{"a"}}}}
	rec := &recorder{}
	run(t, Config[[]string]{
		Fetch: f.fetch, OnChange: rec.onChange, Initial: []string{},
		Normalize: func(c []string) ([]string, error) {
			out := make([]string, len(c))
			for i := range c {
				out[i] = strings.ToLower(c[i])
			}
			return out, nil
		},
	})

	waitFor(t, func() bool { return f.calls.Load() >= 4 }, "no polls")
	if rec.count() != 1 || rec.got[0][0] != "a" {
		t.Errorf("got %v, want a single normalized delivery", rec.got)
	}
}

func TestRun_NormalizeError_SkipsPollWithoutBackoff(t *testing.T) {
	f := &fetcher{results: []fetchResult{{cfg: []string{"a"}}}}
	rec := &recorder{}
	var calls atomic.Int32
	run(t, Config[[]string]{
		Interval: 3 * time.Millisecond, Fetch: f.fetch, OnChange: rec.onChange, Initial: []string{},
		Normalize: func(c []string) ([]string, error) {
			if calls.Add(1) == 1 {
				return nil, errors.New("bad")
			}
			return c, nil
		},
	})

	waitFor(t, func() bool { return rec.count() == 1 }, "config was not delivered after normalize recovered")
}

func TestRun_ContextCancelDuringOnChange_StopsRun(t *testing.T) {
	f := &fetcher{results: []fetchResult{{cfg: []string{"a"}}}}
	ch := make(chan []string) // never read: Send blocks until ctx is done
	cfg := Config[[]string]{
		Fetch: f.fetch, Initial: []string{},
		OnChange: func(ctx context.Context, c []string) error { return Send(ctx, ch, c) },
	}
	stop := run(t, cfg)
	waitFor(t, func() bool { return f.calls.Load() >= 1 }, "no poll")
	stop() // fails the test if Run does not return
}

func TestSend(t *testing.T) {
	ch := make(chan int, 1)
	if err := Send(context.Background(), ch, 1); err != nil || <-ch != 1 {
		t.Error("Send on ready channel failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Send(ctx, make(chan int), 1); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestJSONRoundTrip_IsIdempotentOnNfConfigApiTypes(t *testing.T) {
	in := `[{"plmnId":{"mcc":"208","mnc":"93"},"snssai":{"sst":1}}]`
	decoded, err := FetchJSON[[]nfConfigApi.AccessAndMobility](context.Background(), serve(t, in).Client(), serve(t, in).URL)
	if err != nil {
		t.Fatal(err)
	}
	once, err := JSONRoundTrip(decoded)
	if err != nil {
		t.Fatal(err)
	}
	twice, err := JSONRoundTrip(once)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(once, twice) {
		t.Errorf("round trip not idempotent:\n%+v\n%+v", once, twice)
	}
	if len(once) != 1 || once[0].PlmnId.GetMcc() != "208" {
		t.Errorf("round trip lost data: %+v", once)
	}
}

func serve(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentTypeJSON)
		if _, err := w.Write([]byte(body)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNew_MaxBackoffNeverBelowInterval(t *testing.T) {
	for _, tc := range []struct{ interval, maxBackoff, want time.Duration }{
		{time.Minute, 0, time.Minute},
		{time.Minute, 10 * time.Second, time.Minute},
		{time.Second, 10 * time.Second, 10 * time.Second},
	} {
		p, err := New(Config[[]string]{
			Name: "x", Endpoint: "http://x", Interval: tc.interval, MaxBackoff: tc.maxBackoff,
			OnChange: func(context.Context, []string) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		if p.cfg.MaxBackoff != tc.want {
			t.Errorf("interval=%v MaxBackoff=%v: got %v, want %v", tc.interval, tc.maxBackoff, p.cfg.MaxBackoff, tc.want)
		}
	}
}

// An error response from a proxy or a server in front of the endpoint is usually not JSON. It has to be reported
// by its status, not as a Content-Type problem that hides it.
func TestFetchJSON_ReportsTheStatusOfANonJSONErrorResponse(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusNotFound, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(status)
			if _, err := w.Write([]byte("<html>error</html>")); err != nil {
				t.Errorf("write response: %v", err)
			}
		}))
		_, err := FetchJSON[[]models.PlmnId](context.Background(), srv.Client(), srv.URL)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), strconv.Itoa(status)) || strings.Contains(err.Error(), "Content-Type") {
			t.Errorf("status %d: err = %v, want it to name the status and not the Content-Type", status, err)
		}
	}
}

// A configuration is a short list. A response above the limit is refused, and what is read of it is bounded: the
// client stops reading, so a server that keeps sending is cut off long before it has sent everything.
func TestFetchJSON_RefusesAnOversizedBody(t *testing.T) {
	const serverWouldSend = 4 * maxResponseBytes
	var sent atomic.Int64
	finished := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "application/json")
		chunk := bytes.Repeat([]byte(" "), 1<<20)
		for sent.Load() < serverWouldSend {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	_, err := FetchJSON[[]models.PlmnId](context.Background(), srv.Client(), srv.URL)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want the body refused as too large", err)
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the server was still sending long after the client gave up")
	}
	if got := sent.Load(); got >= serverWouldSend {
		t.Errorf("the server sent all %d bytes: the client read them although the limit is %d", got, maxResponseBytes)
	}

	// a body at the limit is still read (and here is not JSON, which is a different error)
	exact := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(append([]byte("[]"), bytes.Repeat([]byte(" "), maxResponseBytes-2)...)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer exact.Close()
	if got, err := FetchJSON[[]models.PlmnId](context.Background(), exact.Client(), exact.URL); err != nil || got == nil {
		t.Errorf("a body of exactly the limit was refused: %v", err)
	}
}
