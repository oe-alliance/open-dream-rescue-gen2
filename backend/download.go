package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var errImageRedirect = errors.New("Invalid HTTPS redirect")

type downloadError struct{ title, reason string }

func (e *downloadError) Error() string { return e.reason }

// Match a failed job to its catalogue link without persisting URL query tokens.
func downloadKey(link string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(link))) }

type remoteBody struct{ io.Reader }

func (b remoteBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	if err != nil && err != io.EOF {
		return n, &downloadError{"Download interrupted", "The connection ended before the image was fully received. Please try the download again."}
	}
	return n, err
}

func imageDownloadBody(res *http.Response) (*bufio.Reader, error) {
	host := res.Request.URL.Hostname()
	status := fmt.Sprintf("%s returned HTTP %d.", host, res.StatusCode)
	switch {
	case res.StatusCode == http.StatusNoContent || res.StatusCode == http.StatusResetContent:
		return nil, &downloadError{"Image not available", status + " No image was supplied at this link."}
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return nil, &downloadError{"Image not available", status + " The requested image is missing at this link."}
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return nil, &downloadError{"Download access denied", status + " This link does not allow the image to be downloaded."}
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		return nil, &downloadError{"Image server unavailable", status + " Try again later or choose another image."}
	case res.StatusCode != http.StatusOK:
		return nil, &downloadError{"Image link unavailable", status + " Check the download link or choose another image."}
	}

	r := bufio.NewReader(remoteBody{res.Body})
	header, err := r.Peek(512)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(header) == 0 {
		return nil, &downloadError{"Image not available", host + " returned an empty response. No image is available at this link."}
	}
	// Some PHP mirrors serve valid archives with an HTML content type.
	// Inspect the payload first and pass the same buffered bytes to staging.
	if !bytes.HasPrefix(header, []byte("PK\x03\x04")) && rootHeader(header) != nil {
		media, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
		sniffed := http.DetectContentType(header)
		if media == "text/html" || media == "application/xhtml+xml" || strings.HasPrefix(sniffed, "text/html") {
			return nil, &downloadError{"Image link unavailable", host + " returned a web page instead of an image. Check the download link or choose another image."}
		}
		if strings.HasPrefix(sniffed, "text/") || strings.HasPrefix(media, "text/") || media == "application/json" || media == "application/xml" {
			return nil, &downloadError{"Image link unavailable", host + " returned a text response instead of an image. Check the download link or choose another image."}
		}
	}
	return r, nil
}

func downloadImage(link string) error {
	res, err := client.Get(link)
	if err != nil {
		host := "the image server"
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			if u, e := url.Parse(requestErr.URL); e == nil && u.Hostname() != "" {
				host = u.Hostname()
			}
		}
		if errors.Is(err, errImageRedirect) {
			return &downloadError{"Image link unavailable", "The download link has an invalid HTTPS redirect."}
		}
		var networkErr net.Error
		if errors.As(err, &networkErr) && networkErr.Timeout() {
			return &downloadError{"Image server unreachable", "The connection to " + host + " timed out. Try again or choose another image."}
		}
		return &downloadError{"Image server unreachable", "ODR could not connect to " + host + ". Check the network connection or try again later."}
	}
	defer res.Body.Close()
	r, err := imageDownloadBody(res)
	if err != nil {
		return err
	}
	name := ""
	if res.Request.URL.String() != link {
		name = res.Request.URL.Path
	}
	if _, p, err := mime.ParseMediaType(res.Header.Get("Content-Disposition")); err == nil && p["filename"] != "" {
		name = p["filename"]
	}
	if name != "" {
		jobMu.Lock()
		busyJob.Image = displayName(name)
		jobMu.Unlock()
	}
	return stage(r, res.ContentLength)
}
