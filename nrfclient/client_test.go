// Copyright (C) 2026 Intel Corporation
// SPDX-License-Identifier: Apache-2.0

package nrfclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/omec-project/openapi/v2/models"
)

const (
	nfStatusPath     = "/nfStatus"
	testNfInstanceID = "abc"
	locationHeader   = "Location"
)

type seenRequest struct {
	method, path string
	body         []byte
}

// fakeNrf answers every request with the configured status, headers and body, and records requests.
type fakeNrf struct {
	srv *httptest.Server

	mu       sync.Mutex
	status   int
	header   map[string]string
	respBody any
	seen     []seenRequest
}

func newFakeNrf(t *testing.T, status int, respBody any, header map[string]string) *fakeNrf {
	t.Helper()
	f := &fakeNrf{status: status, respBody: respBody, header: header}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		f.mu.Lock()
		f.seen = append(f.seen, seenRequest{r.Method, r.URL.Path, body})
		status, respBody, header := f.status, f.respBody, f.header
		f.mu.Unlock()
		for k, v := range header {
			w.Header().Set(k, v)
		}
		if respBody != nil {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		if respBody != nil {
			if err := json.NewEncoder(w).Encode(respBody); err != nil {
				t.Errorf("encode response: %v", err)
			}
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeNrf) last(t *testing.T) seenRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) == 0 {
		t.Fatal("no request reached the NRF")
	}
	return f.seen[len(f.seen)-1]
}

func testProfile() models.NFProfile {
	p := *models.NewNFProfileWithDefaults()
	p.SetNfInstanceId(testNfInstanceID)
	p.SetNfType(models.NFTYPE_UDM)
	p.SetNfStatus(models.NFSTATUS_REGISTERED)
	return p
}

func problem(status int32, title string) models.ProblemDetails {
	p := models.NewProblemDetails()
	p.SetStatus(status)
	p.SetTitle(title)
	return *p
}

func TestRegister_Created_ReturnsLocationDerivedValues(t *testing.T) {
	prof := testProfile()
	prof.SetHeartBeatTimer(30)
	f := newFakeNrf(t, http.StatusCreated, prof, map[string]string{
		locationHeader: "http://nrf:8000/nnrf-nfm/v1/nf-instances/assigned-id",
	})

	res, err := New(f.srv.URL).Register(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || res.NrfURI != "http://nrf:8000" || res.NfInstanceID != "assigned-id" {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.Profile.GetHeartBeatTimer() != 30 {
		t.Errorf("profile not returned: %+v", res.Profile)
	}
	if got := f.last(t); got.method != http.MethodPut || !strings.HasSuffix(got.path, "/nf-instances/abc") {
		t.Errorf("request = %s %s", got.method, got.path)
	}
}

func TestRegister_Created_WithoutLocation_DoesNotPanic(t *testing.T) {
	// the per-NF code this replaces sliced with strings.Index(...) == -1 and panicked here
	for name, loc := range map[string]string{"missing": "", "trailing slash": "http://nrf/nnrf-nfm/v1/nf-instances/"} {
		t.Run(name, func(t *testing.T) {
			prof := testProfile()
			f := newFakeNrf(t, http.StatusCreated, prof, map[string]string{locationHeader: loc})
			res, err := New(f.srv.URL).Register(context.Background(), prof)
			if err != nil {
				t.Fatal(err)
			}
			if !res.Created || res.NfInstanceID != "" {
				t.Errorf("expected created without an instance ID, got %+v", res)
			}
		})
	}
}

func TestRegister_Created_LocationWithoutNfmPath_UsesLastSegmentAsInstanceID(t *testing.T) {
	prof := testProfile()
	f := newFakeNrf(t, http.StatusCreated, prof, map[string]string{locationHeader: "http://nrf:8000/other/assigned-id"})
	res, err := New(f.srv.URL).Register(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if res.NfInstanceID != "assigned-id" || res.NrfURI != "" {
		t.Errorf("got %+v, want instance ID from the last segment and no NRF URI", res)
	}
}

func TestRegister_OK_IsReplacement(t *testing.T) {
	prof := testProfile()
	f := newFakeNrf(t, http.StatusOK, prof, nil)
	res, err := New(f.srv.URL).Register(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || res.NfInstanceID != "" {
		t.Errorf("unexpected result: %+v", res)
	}
}

func TestRegister_Errors(t *testing.T) {
	prof := testProfile()
	for name, status := range map[string]int{"bad request": 400, "server error": 500, "unexpected 2xx": 202} {
		t.Run(name, func(t *testing.T) {
			f := newFakeNrf(t, status, problem(int32(status), "nope"), nil)
			res, err := New(f.srv.URL).Register(context.Background(), prof)
			if err == nil || res != nil {
				t.Errorf("res=%+v err=%v, want nil result and an error", res, err)
			}
		})
	}
}

func TestRegister_UnreachableNrf(t *testing.T) {
	f := newFakeNrf(t, 200, nil, nil)
	url := f.srv.URL
	f.srv.Close()
	if _, err := New(url).Register(context.Background(), testProfile()); err == nil {
		t.Fatal("expected error")
	}
}

func TestRegister_ContextCancelled(t *testing.T) {
	f := newFakeNrf(t, 200, testProfile(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(f.srv.URL).Register(ctx, testProfile()); err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestUpdate(t *testing.T) {
	patch := []models.PatchItem{{Op: models.PATCHOPERATION_REPLACE, Path: nfStatusPath, Value: models.NFSTATUS_REGISTERED}}

	t.Run("200 returns profile", func(t *testing.T) {
		prof := testProfile()
		prof.SetHeartBeatTimer(45)
		f := newFakeNrf(t, http.StatusOK, prof, nil)
		got, pd, err := New(f.srv.URL).Update(context.Background(), testNfInstanceID, patch)
		if err != nil || pd != nil || got.GetHeartBeatTimer() != 45 {
			t.Fatalf("got=%+v pd=%v err=%v", got, pd, err)
		}
		req := f.last(t)
		if req.method != http.MethodPatch || !strings.HasSuffix(req.path, "/nf-instances/abc") {
			t.Errorf("request = %s %s", req.method, req.path)
		}
	})

	t.Run("204 succeeds with nil profile", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusNoContent, nil, nil)
		got, pd, err := New(f.srv.URL).Update(context.Background(), testNfInstanceID, patch)
		if err != nil || pd != nil {
			t.Fatalf("pd=%v err=%v", pd, err)
		}
		if got.GetHeartBeatTimer() != 0 { // nil-safe getter: callers rely on it
			t.Error("unexpected heartbeat timer")
		}
	})

	t.Run("404 problem details returned with error", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusNotFound, problem(404, "not registered"), nil)
		got, pd, err := New(f.srv.URL).Update(context.Background(), testNfInstanceID, patch)
		if err == nil || got != nil {
			t.Fatalf("got=%+v err=%v", got, err)
		}
		if pd == nil || pd.GetTitle() != "not registered" {
			t.Errorf("problem details = %+v", pd)
		}
	})

	t.Run("500 without a body is an error", func(t *testing.T) {
		// the generated client decodes any error body, even an empty one, into ProblemDetails
		f := newFakeNrf(t, http.StatusInternalServerError, nil, nil)
		got, _, err := New(f.srv.URL).Update(context.Background(), testNfInstanceID, patch)
		if err == nil || got != nil {
			t.Errorf("got=%+v err=%v", got, err)
		}
	})

	t.Run("empty patch is rejected before any request", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusNoContent, nil, nil)
		if _, _, err := New(f.srv.URL).Update(context.Background(), testNfInstanceID, nil); err == nil {
			t.Error("expected error")
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.seen) != 0 {
			t.Error("request was sent")
		}
	})
}

func TestDeregister(t *testing.T) {
	t.Run("204", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusNoContent, nil, nil)
		if err := New(f.srv.URL).Deregister(context.Background(), testNfInstanceID); err != nil {
			t.Fatal(err)
		}
		if got := f.last(t); got.method != http.MethodDelete || !strings.HasSuffix(got.path, "/nf-instances/abc") {
			t.Errorf("request = %s %s", got.method, got.path)
		}
	})
	for name, status := range map[string]int{"404": 404, "200 is not success": 200, "500": 500} {
		t.Run(name, func(t *testing.T) {
			f := newFakeNrf(t, status, nil, nil)
			if err := New(f.srv.URL).Deregister(context.Background(), testNfInstanceID); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestCreateSubscription(t *testing.T) {
	data := *models.NewSubscriptionData("http://nf/notify")

	t.Run("created", func(t *testing.T) {
		resp := *models.NewSubscriptionData("http://nf/notify")
		resp.SetSubscriptionId("sub-1")
		f := newFakeNrf(t, http.StatusCreated, resp, nil)
		got, pd, err := New(f.srv.URL).CreateSubscription(context.Background(), data)
		if err != nil || pd != nil || got.GetSubscriptionId() != "sub-1" {
			t.Fatalf("got=%+v pd=%v err=%v", got, pd, err)
		}
		if req := f.last(t); req.method != http.MethodPost || !strings.HasSuffix(req.path, "/subscriptions") {
			t.Errorf("request = %s %s", req.method, req.path)
		}
	})

	t.Run("problem details", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusBadRequest, problem(400, "bad subscription"), nil)
		got, pd, err := New(f.srv.URL).CreateSubscription(context.Background(), data)
		if err == nil || got != nil || pd == nil || pd.GetTitle() != "bad subscription" {
			t.Fatalf("got=%+v pd=%+v err=%v", got, pd, err)
		}
	})

	t.Run("no response wraps the cause", func(t *testing.T) {
		f := newFakeNrf(t, 200, nil, nil)
		url := f.srv.URL
		f.srv.Close()
		_, pd, err := New(url).CreateSubscription(context.Background(), data)
		if err == nil || pd != nil || !strings.Contains(err.Error(), "no response from server") {
			t.Fatalf("pd=%v err=%v", pd, err)
		}
	})
}

func TestRemoveSubscription(t *testing.T) {
	t.Run("204", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusNoContent, nil, nil)
		pd, err := New(f.srv.URL).RemoveSubscription(context.Background(), "sub-1")
		if err != nil || pd != nil {
			t.Fatalf("pd=%v err=%v", pd, err)
		}
		if req := f.last(t); req.method != http.MethodDelete || !strings.HasSuffix(req.path, "/subscriptions/sub-1") {
			t.Errorf("request = %s %s", req.method, req.path)
		}
	})
	t.Run("problem details", func(t *testing.T) {
		f := newFakeNrf(t, http.StatusNotFound, problem(404, "unknown subscription"), nil)
		pd, err := New(f.srv.URL).RemoveSubscription(context.Background(), "sub-1")
		if err == nil || pd == nil || pd.GetTitle() != "unknown subscription" {
			t.Fatalf("pd=%+v err=%v", pd, err)
		}
	})
}

func TestObserver_SeesEveryOperationWithResponse(t *testing.T) {
	f := newFakeNrf(t, http.StatusNoContent, nil, nil)
	var ops []Operation
	var statuses []int
	c := New(f.srv.URL, WithObserver(func(op Operation, res *http.Response, err error) {
		ops = append(ops, op)
		if res != nil {
			statuses = append(statuses, res.StatusCode)
		}
	}))

	if err := c.Deregister(context.Background(), testNfInstanceID); err != nil {
		t.Errorf("Deregister: %v", err)
	}
	patch := []models.PatchItem{{Op: models.PATCHOPERATION_REPLACE, Path: nfStatusPath, Value: models.NFSTATUS_REGISTERED}}
	if _, _, err := c.Update(context.Background(), testNfInstanceID, patch); err != nil {
		t.Errorf("Update: %v", err)
	}
	if _, err := c.RemoveSubscription(context.Background(), "s"); err != nil {
		t.Errorf("RemoveSubscription: %v", err)
	}

	want := []Operation{OpDeregisterNFInstance, OpUpdateNFInstance, OpRemoveSubscription}
	if len(ops) != len(want) {
		t.Fatalf("ops = %v, want %v", ops, want)
	}
	for i := range want {
		if ops[i] != want[i] || statuses[i] != http.StatusNoContent {
			t.Errorf("op %d = %s/%d", i, ops[i], statuses[i])
		}
	}
}

func TestObserver_NoResponseIsNilResponse(t *testing.T) {
	f := newFakeNrf(t, 200, nil, nil)
	url := f.srv.URL
	f.srv.Close()
	var gotRes *http.Response
	var gotErr error
	c := New(url, WithObserver(func(_ Operation, res *http.Response, err error) { gotRes, gotErr = res, err }))
	if err := c.Deregister(context.Background(), testNfInstanceID); err == nil {
		t.Error("Deregister to a closed server returned no error")
	}
	if gotRes != nil || gotErr == nil {
		t.Errorf("res=%v err=%v", gotRes, gotErr)
	}
}

func TestWithHTTPClient_IsUsed(t *testing.T) {
	f := newFakeNrf(t, http.StatusNoContent, nil, nil)
	var used bool
	hc := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		used = true
		return http.DefaultTransport.RoundTrip(r)
	})}
	if err := New(f.srv.URL, WithHTTPClient(hc)).Deregister(context.Background(), testNfInstanceID); err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Error("custom HTTP client was not used")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNewClients_PointAtNrfURI(t *testing.T) {
	mgmt := NewNFManagementClient("http://nrf.example:9000", nil)
	if got := mgmt.GetConfig().Servers[0].Variables["apiRoot"].DefaultValue; got != "http://nrf.example:9000" {
		t.Errorf("management apiRoot = %q", got)
	}
	disc := NewNFDiscoveryClient("http://nrf.example:9000")
	if got := disc.GetConfig().Servers[0].Variables["apiRoot"].DefaultValue; got != "http://nrf.example:9000" {
		t.Errorf("discovery apiRoot = %q", got)
	}
}

func TestParseLocation(t *testing.T) {
	cases := []struct{ in, uri, id string }{
		{"https://nrf:8000/nnrf-nfm/v1/nf-instances/abc", "https://nrf:8000", testNfInstanceID},
		{"", "", ""},
		{"https://nrf/other/abc", "", testNfInstanceID}, // no /nnrf-nfm/ marker: ID still from the last segment
		{testNfInstanceID, "", testNfInstanceID},
		{"https://nrf/nnrf-nfm/v1/nf-instances/", "https://nrf", ""},
	}
	for _, c := range cases {
		uri, id := parseLocation(c.in)
		if uri != c.uri || id != c.id {
			t.Errorf("parseLocation(%q) = %q, %q; want %q, %q", c.in, uri, id, c.uri, c.id)
		}
	}
}

// The generated client takes every status below 300 as success and decodes an empty body without complaint, so
// the NRF's answer has to be checked here: only 201 with the data of the subscription creates one.
func TestCreateSubscription_OnlyCreatedWithDataIsSuccess(t *testing.T) {
	data := *models.NewSubscriptionData("http://nf/notify")
	withData := *models.NewSubscriptionData("http://nf/notify")
	withData.SetSubscriptionId("sub-1")

	cases := []struct {
		name    string
		status  int
		body    any
		wantErr string // empty for success
	}{
		{"201 with the subscription", http.StatusCreated, withData, ""},
		{"201 without a body", http.StatusCreated, nil, "without the data"},
		{"200 with the subscription", http.StatusOK, withData, "unexpected response code 200"},
		{"202 without a body", http.StatusAccepted, nil, "unexpected response code 202"},
		{"202 with a body", http.StatusAccepted, withData, "unexpected response code 202"},
		{"204", http.StatusNoContent, nil, "unexpected response code 204"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeNrf(t, tc.status, tc.body, nil)
			got, pd, err := New(f.srv.URL).CreateSubscription(context.Background(), data)
			if tc.wantErr == "" {
				if err != nil || pd != nil || got == nil || got.GetSubscriptionId() != "sub-1" {
					t.Fatalf("got=%+v pd=%v err=%v", got, pd, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
			}
			if got != nil || pd != nil {
				t.Errorf("a failed creation returned data: got=%+v pd=%v", got, pd)
			}
		})
	}
}

// Only 204 confirms that a subscription is gone: a 202 says the removal was accepted, not that it happened.
func TestRemoveSubscription_OnlyNoContentIsSuccess(t *testing.T) {
	for name, tc := range map[string]struct {
		status  int
		wantErr bool
	}{
		"204": {http.StatusNoContent, false},
		"200": {http.StatusOK, true},
		"202": {http.StatusAccepted, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeNrf(t, tc.status, nil, nil)
			pd, err := New(f.srv.URL).RemoveSubscription(context.Background(), "sub-1")
			if (err != nil) != tc.wantErr || pd != nil {
				t.Fatalf("pd=%v err=%v, wantErr %v", pd, err, tc.wantErr)
			}
			if tc.wantErr && !strings.Contains(err.Error(), "unexpected response code") {
				t.Errorf("err = %v", err)
			}
		})
	}
}

// An empty identifier leaves a path parameter empty, which addresses the collection ("/nf-instances/") instead
// of the resource, so it is refused before any request is sent.
func TestEmptyIdentifiersAreRefusedWithoutARequest(t *testing.T) {
	patch := []models.PatchItem{{Op: models.PATCHOPERATION_REPLACE, Path: nfStatusPath, Value: models.NFSTATUS_REGISTERED}}
	noID := testProfile()
	noID.SetNfInstanceId("")

	cases := map[string]func(c *Client) error{
		"Register without an instance ID in the profile": func(c *Client) error {
			_, err := c.Register(context.Background(), noID)
			return err
		},
		"Update without an instance ID": func(c *Client) error {
			_, _, err := c.Update(context.Background(), "", patch)
			return err
		},
		"Deregister without an instance ID": func(c *Client) error {
			return c.Deregister(context.Background(), "")
		},
		"RemoveSubscription without a subscription ID": func(c *Client) error {
			_, err := c.RemoveSubscription(context.Background(), "")
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFakeNrf(t, http.StatusNoContent, nil, nil)
			err := call(New(f.srv.URL))
			if err == nil || !strings.Contains(err.Error(), "is required") {
				t.Fatalf("err = %v, want it to say the identifier is required", err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.seen) != 0 {
				t.Errorf("%d request(s) reached the NRF: %+v", len(f.seen), f.seen)
			}
		})
	}
}

// A nil logger selects the default one: it must not be left nil, which panics on the first request.
func TestWithLoggerNilFallsBackToTheDefault(t *testing.T) {
	f := newFakeNrf(t, http.StatusNoContent, nil, nil)
	c := New(f.srv.URL, WithLogger(nil))
	if err := c.Deregister(context.Background(), testNfInstanceID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Register(context.Background(), testProfile()); err == nil {
		t.Fatal("a 204 is not a registration, expected an error")
	}
}
