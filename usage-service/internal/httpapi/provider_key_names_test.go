package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProviderKeyNamesSaveLoadAndDelete(t *testing.T) {
	handler := newTestHandler(t, "http://example.test", true)
	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	do := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer management-key")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	rr := do(http.MethodPut, "/v0/management/provider-key-names",
		`{"items":[{"keyHash":"`+hash+`","provider":"claude","name":"Acc Canh"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", rr.Code, rr.Body.String())
	}

	rr = do(http.MethodGet, "/v0/management/provider-key-names", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("load status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Items []struct {
			KeyHash  string `json:"keyHash"`
			Provider string `json:"provider"`
			Name     string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].KeyHash != hash ||
		response.Items[0].Provider != "claude" || response.Items[0].Name != "Acc Canh" {
		t.Fatalf("items = %#v", response.Items)
	}

	rr = do(http.MethodPut, "/v0/management/provider-key-names",
		`{"items":[{"keyHash":"`+hash+`","provider":"unknown","name":"x"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid provider status = %d, body = %s", rr.Code, rr.Body.String())
	}

	rr = do(http.MethodDelete, "/v0/management/provider-key-names/"+hash, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", rr.Code, rr.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/provider-key-names", nil)
	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, want 401", rr.Code)
	}
}
