package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestClientIP(t *testing.T) {
	tests := []struct {
		name         string
		remoteAddr   string
		headerName   string
		headerValue  string
		expected     string
	}{
		{
			name:       "no header, with port",
			remoteAddr: "10.0.1.5:12345",
			expected:   "10.0.1.5",
		},
		{
			name:       "no header, without port",
			remoteAddr: "10.0.1.5",
			expected:   "10.0.1.5",
		},
		{
			name:         "with header, single IP",
			headerName:   "X-Forwarded-For",
			headerValue:  "10.0.2.10",
			expected:     "10.0.2.10",
		},
		{
			name:         "with header, multiple IPs",
			headerName:   "X-Forwarded-For",
			headerValue:  "10.0.2.10, 10.0.3.20, 10.0.4.30",
			expected:     "10.0.2.10",
		},
		{
			name:         "with header, extra whitespace",
			headerName:   "X-Real-IP",
			headerValue:  "  10.0.5.5  ",
			expected:     "10.0.5.5",
		},
		{
			name:         "header not present falls back to RemoteAddr",
			headerName:   "X-Forwarded-For",
			headerValue:  "",
			remoteAddr:   "10.0.6.6:9999",
			expected:     "10.0.6.6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &http.Request{RemoteAddr: tt.remoteAddr}
			if tt.headerName != "" && tt.headerValue != "" {
				req.Header = http.Header{}
				req.Header.Set(tt.headerName, tt.headerValue)
			}
			got := clientIP(req, tt.headerName)
			if got != tt.expected {
				t.Errorf("clientIP(%q, %q) = %q; want %q", tt.remoteAddr, tt.headerName, got, tt.expected)
			}
		})
	}
}

func TestLoadIPMapping(t *testing.T) {
	tmpDir := t.TempDir()

	validYAML := `10.0.1.5:
  h: web-01
  m: aa-bb-cc-dd-ee-ff
10.0.2.3:
  u: 550e8400-e29b-41d4-a716-446655440000`

	mapping, err := loadIPMapping(filepath.Join(tmpDir, "valid.yaml"))
	if mapping != nil {
		t.Fatalf("expected error for non-existent file")
	}
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}

	validPath := filepath.Join(tmpDir, "valid.yaml")
	if err := os.WriteFile(validPath, []byte(validYAML), 0644); err != nil {
		t.Fatal(err)
	}

	mapping, err = loadIPMapping(validPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mapping) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(mapping))
	}
	if mapping["10.0.1.5"]["h"] != "web-01" {
		t.Errorf("expected h=web-01 for 10.0.1.5, got %q", mapping["10.0.1.5"]["h"])
	}

	malformedYAML := "10.0.1.5:\n  h: [invalid yaml"
	malformedPath := filepath.Join(tmpDir, "malformed.yaml")
	if err := os.WriteFile(malformedPath, []byte(malformedYAML), 0644); err != nil {
		t.Fatal(err)
	}

	_, err = loadIPMapping(malformedPath)
	if err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestValidateAuth(t *testing.T) {
	mapping := map[string]map[string]string{
		"10.0.1.5": {
			"h": "web-01",
			"m": "aa-bb-cc-dd-ee-ff",
			"s": "SN001",
		},
	}

	tests := []struct {
		name     string
		query    url.Values
		ip       string
		wantOK   bool
		wantInfo string
	}{
		{
			name:   "unknown IP",
			query:  url.Values{"h": {"web-01"}},
			ip:     "10.0.9.9",
			wantOK: false,
			wantInfo: "unknown IP 10.0.9.9",
		},
		{
			name:   "all params match",
			query:  url.Values{"h": {"web-01"}, "m": {"aa-bb-cc-dd-ee-ff"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
		{
			name:   "param value mismatch",
			query:  url.Values{"h": {"web-01"}, "s": {"WRONG"}},
			ip:     "10.0.1.5",
			wantOK: false,
			wantInfo: "param 's' mismatch for IP 10.0.1.5",
		},
		{
			name:   "known param matches with unknown param present - unknown skipped",
			query:  url.Values{"h": {"web-01"}, "secret": {"key"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
		{
			name:   "partial identity - fewer params than mapping",
			query:  url.Values{"m": {"aa-bb-cc-dd-ee-ff"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
		{
			name:   "unknown params skipped, known param validated",
			query:  url.Values{"h": {"web-01"}, "foo": {"bar"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
		{
			name:   "no recognized params - empty query",
			query:  url.Values{},
			ip:     "10.0.1.5",
			wantOK: false,
			wantInfo: "no recognized parameters for IP 10.0.1.5",
		},
		{
			name:   "empty param values skipped",
			query:  url.Values{"h": {""}, "m": {"aa-bb-cc-dd-ee-ff"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
		{
			name:   "MAC with colon format matches normalized",
			query:  url.Values{"m": {"aa:bb:cc:dd:ee:ff"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
		{
			name:   "uppercase hostname matches lowercase mapping",
			query:  url.Values{"h": {"WEB-01"}},
			ip:     "10.0.1.5",
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, info := validateAuth(tt.query, mapping, tt.ip)
			if ok != tt.wantOK {
				t.Errorf("validateAuth(%v, %q) = (%v, %q); want (%v, %q)",
					tt.query, tt.ip, ok, info, tt.wantOK, tt.wantInfo)
			}
			if info != tt.wantInfo && tt.wantInfo != "" {
				t.Errorf("info mismatch: got %q; want %q", info, tt.wantInfo)
			}
		})
	}
}

func TestAuthHandlerIntegration(t *testing.T) {
	tmpDir := t.TempDir()

	validYAML := `10.0.1.5:
  h: web-01`
	mappingPath := filepath.Join(tmpDir, "mapping.yaml")
	if err := os.WriteFile(mappingPath, []byte(validYAML), 0644); err != nil {
		t.Fatal(err)
	}

	testDataDir := filepath.Join(tmpDir, "data")
	hDir := filepath.Join(testDataDir, "h")
	if err := os.MkdirAll(hDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(hDir, "web-01.yaml"), []byte("machine:\n  hostname: web-01\n"), 0644)

	defaultConfigPath := filepath.Join(testDataDir, "default.yaml")
	if err := os.WriteFile(defaultConfigPath, []byte("test: value\n"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		enabled    bool
		configPath string
		forwarded  string
		query      string
		headers    map[string]string
		remoteAddr string
		wantStatus int
	}{
		{
			name:       "auth enabled unknown IP returns 401",
			enabled:    true,
			configPath: mappingPath,
			remoteAddr: "10.0.9.9",
			query:      "?h=web-01",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "auth enabled known IP with matching param passes auth and matches config",
			enabled:    true,
			configPath: mappingPath,
			remoteAddr: "10.0.1.5",
			query:      "?h=web-01",
			wantStatus: http.StatusOK,
		},
		{
			name:       "auth enabled known IP with mismatched param returns 403",
			enabled:    true,
			configPath: mappingPath,
			remoteAddr: "10.0.1.5",
			query:      "?h=wrong",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "auth enabled known IP with unknown param only returns 403",
			enabled:    true,
			configPath: mappingPath,
			remoteAddr: "10.0.1.5",
			query:      "?foo=bar",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "auth enabled with forwarded header uses header IP",
			enabled:    true,
			configPath: mappingPath,
			forwarded:  "X-Forwarded-For",
			headers: map[string]string{
				"X-Forwarded-For": "10.0.1.5",
			},
			remoteAddr: "10.0.9.9 (proxy)",
			query:      "?h=web-01",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldACLEnabled := ipACLEnabled
			oldACLConfig := ipACLConfig
			oldACLForwarded := ipACLForwarded
			oldDataVal := *dataDir

			ipACLEnabled = &tt.enabled
			ipACLConfig = &tt.configPath
			ipACLForwarded = &tt.forwarded
			*dataDir = testDataDir

			if tt.enabled {
				mapping, err := loadIPMapping(tt.configPath)
				if err != nil {
					t.Fatalf("failed to load mapping: %v", err)
				}
				ipACLMapping.Store(&mapping)
			} else {
				ipACLMapping.Store(nil)
			}

			req := httptest.NewRequest(http.MethodGet, "/metadata"+tt.query, nil)
			req.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()

			handler := http.HandlerFunc(metadataHandler)
			handler.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("status = %d; want %d", rr.Code, tt.wantStatus)
			}

			ipACLEnabled = oldACLEnabled
			ipACLConfig = oldACLConfig
			ipACLForwarded = oldACLForwarded
			*dataDir = oldDataVal
		})
	}
}
