package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestImageServerResponse(t *testing.T) {
	for _, tc := range []struct {
		name, media, data, issue string
		status                   int
	}{
		{"empty current.php", "text/html; charset=UTF-8", "", "Image not available", 200},
		{"missing ZIP", "", "", "Image not available", 404},
		{"gone", "", "", "Image not available", 410},
		{"no content", "", "", "Image not available", 204},
		{"maintenance", "text/html", "<html>Service unavailable", "Image server unavailable", 503},
		{"forbidden", "", "", "Download access denied", 403},
		{"webpage labelled binary", "application/octet-stream", "<!DOCTYPE html><html>Error</html>", "Image link unavailable", 200},
		{"text error labelled binary", "application/octet-stream", "No build available", "Image link unavailable", 200},
		{"zip labelled HTML", "text/html", "PK\x03\x04" + strings.Repeat("z", 1800), "", 200},
		{"xz labelled text", "text/plain", "\xfd7zXZ\x00test", "", 200},
		{"bzip image", "", "BZh9test", "", 200},
		{"gzip image", "", "\x1f\x8b\x08test", "", 200},
		{"binary for archive validator", "application/octet-stream", "\x01\x00broken", "", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := &http.Response{Request: httptest.NewRequest("GET", "https://mirror.example/current.php", nil), Header: make(http.Header), StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.data))}
			res.Header.Set("Content-Type", tc.media)
			r, err := imageDownloadBody(res)
			if tc.issue != "" {
				var e *downloadError
				if !errors.As(err, &e) || e.title != tc.issue {
					t.Fatalf("got %v, want %s", err, tc.issue)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(r)
			if err != nil || !bytes.Equal(body, []byte(tc.data)) {
				t.Fatal("response bytes changed", err)
			}
		})
	}
}

func TestUnavailableImageLink(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/current.php":
			http.Redirect(w, r, "/empty", http.StatusFound)
		case "/empty":
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		default:
			http.Redirect(w, r, "http://invalid.example/", http.StatusFound)
		}
	}))
	defer srv.Close()
	check := func(path, title string) {
		t.Helper()
		var issue *downloadError
		if err := downloadImage(srv.URL + path); !errors.As(err, &issue) || issue.title != title {
			t.Fatalf("%s: %v", path, err)
		}
	}
	check("/current.php", "Image not available")
	check("/bad-redirect", "Image link unavailable")
	srv.Close()
	check("/current.php", "Image server unreachable")
}

func TestDownloadInterrupted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.Write([]byte("PK\x03\x04" + strings.Repeat("z", 1024)))
	}))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	r, err := imageDownloadBody(res)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	var issue *downloadError
	if !errors.As(err, &issue) || issue.title != "Download interrupted" {
		t.Fatal(err)
	}
}
