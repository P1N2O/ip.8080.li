package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSerializeText(t *testing.T) {
	tests := []struct {
		name string
		r    geoResponse
		want string
	}{
		{
			name: "ip only",
			r:    geoResponse{IP: "1.2.3.4"},
			want: "1.2.3.4\n",
		},
		{
			name: "full geo",
			r: geoResponse{
				IP:            "1.2.3.4",
				Flag:          "🇺🇸",
				CountryCode:   "US",
				Country:       "United States",
				City:          "Mountain View",
				ASN:           15169,
				ASOrganization: "Google LLC",
			},
			want: "ip: 1.2.3.4\nflag: 🇺🇸\ncountryCode: US\ncountry: United States\ncity: Mountain View\nasn: 15169\nasOrganization: Google LLC\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			serializeText(&b, tt.r)
			if b.String() != tt.want {
				t.Errorf("got %q, want %q", b.String(), tt.want)
			}
		})
	}
}

func TestCountryFlag(t *testing.T) {
	tests := []struct {
		code string
		want string
	}{
		{"US", "🇺🇸"},
		{"PT", "🇵🇹"},
		{"", ""},
		{"USA", ""},
	}
	for _, tt := range tests {
		if got := countryFlag(tt.code); got != tt.want {
			t.Errorf("countryFlag(%q) = %q, want %q", tt.code, got, tt.want)
		}
	}
}

func TestServerRoutes(t *testing.T) {
	s := &server{cfg: &config{debug: false}}
	ts := httptest.NewServer(http.HandlerFunc(s.handleRequest))
	defer ts.Close()

	t.Run("root returns ip", func(t *testing.T) {
		resp, err := http.Get(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if v := resp.Header.Get("Content-Type"); v != "text/plain; charset=utf-8" {
			t.Errorf("Content-Type = %q", v)
		}
	})

	t.Run("json format", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/?format=json")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
		}
		var m map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
			t.Fatal(err)
		}
		if m["ip"] == nil {
			t.Error("expected ip field")
		}
	})

	t.Run("jsonp format", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/?format=jsonp&callback=myFn")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.Header.Get("Content-Type") != "application/javascript" {
			t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
		}
	})

	t.Run("xml format", func(t *testing.T) {
		resp, err := http.Get(ts.URL + "/?format=xml")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.Header.Get("Content-Type") != "application/xml" {
			t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
		}
	})
}
