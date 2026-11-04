package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const rescueCatalogURL = catalogBase + "/rescue"

type RescueRelease struct {
	Version, SHA256, URL string
	Bytes                int64
}

func parseRescue(data []byte) (RescueRelease, error) {
	var r RescueRelease
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 || f[0] != "rescue" || r.Version != "" || len(f[1]) > 64 || !validURL(f[4]) {
			return r, errors.New("Invalid " + modelName + " rescue catalogue")
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		hash, he := hex.DecodeString(f[3])
		if err != nil || n < 48 || n > rescueLimit || he != nil || len(hash) != sha256.Size {
			return r, errors.New("Invalid rescue size or SHA-256 in catalogue")
		}
		r = RescueRelease{f[1], strings.ToLower(f[3]), f[4], n}
	}
	if r.Version == "" {
		return r, errors.New("No rescue release in the " + modelName + " catalogue")
	}
	return r, nil
}

// The digest which authorises an SPI update comes from authenticated GitHub
// metadata. Old receivers can cold-boot with their RTC still set to 2019.
func rescueRelease(browserTime string) (RescueRelease, error) {
	var empty RescueRelease
	at := time.Now()
	if at.Year() < 2026 {
		var err error
		at, err = time.Parse(time.RFC3339, browserTime)
		if err != nil || at.Year() < 2026 {
			return empty, errors.New("Check the date on your computer and reload the firmware page")
		}
	}
	pem, err := os.ReadFile("/etc/odr-firmware-ca.pem")
	if err != nil {
		return empty, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return empty, errors.New("Cannot load rescue catalogue trust store")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12, Time: func() time.Time { return at }}}
	defer transport.CloseIdleConnections()
	c := http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(rescueCatalogURL)
	if err != nil {
		return empty, fmt.Errorf("Reading "+modelName+" rescue catalogue: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return empty, nil
	}
	if res.StatusCode != 200 {
		return empty, fmt.Errorf(modelName+" rescue catalogue: HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 8193))
	if err != nil || len(data) > 8192 {
		return empty, errors.New("Cannot read " + modelName + " rescue catalogue")
	}
	return parseRescue(data)
}

func rescueTarget() error {
	if !matchesReceiver() || readText("/etc/odr-"+model) != "1.0" ||
		readText(rescueSys+"/name") != rescuePart || readText(rescueSys+"/type") != "nor" ||
		readText(rescueSys+"/size") != strconv.Itoa(rescueOffset+rescueLimit) || readText(rescueSys+"/erasesize") != "65536" {
		return errors.New("The " + modelName + " SPI rescue partition could not be identified")
	}
	st, err := os.Stat(rescueReadDev)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeCharDevice == 0 || st.Sys().(*syscall.Stat_t).Rdev != (90<<8|rescueMinor) {
		return errors.New("Unexpected device for " + modelName + " rescue read-back")
	}
	return nil
}

func rescueDigest(path string, offset, n int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.CopyN(h, io.NewSectionReader(f, offset, n), n); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

var odrBanner = regexp.MustCompile(`Linux version [^[:space:]\x00]+-` + model + ` \(odr-` + model + `@rescue\) `)

func checkRescueImage(path string, release RescueRelease) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(b)) != release.Bytes || len(b) > rescueLimit || fmt.Sprintf("%x", sha256.Sum256(b)) != release.SHA256 {
		return errors.New("Rescue image size or checksum does not match")
	}
	offset := 0
	if bankedKernel || nandImage {
		if !bytes.HasPrefix(b, []byte{31, 139, 8}) {
			return errors.New("Expected a gzip-compressed MIPS rescue")
		}
	} else {
		if !armKernel(b) {
			return errors.New("Invalid ARM rescue header")
		}
		offset = bytes.Index(b, []byte{31, 139, 8})
	}
	if offset < 0 {
		return errors.New("Missing compressed rescue kernel")
	}
	g, err := gzip.NewReader(bytes.NewReader(b[offset:]))
	if err != nil {
		return err
	}
	defer g.Close()
	g.Multistream(false)
	const limit = 64 << 20
	k, err := io.ReadAll(io.LimitReader(g, limit+1))
	if err != nil || len(k) > limit || !odrBanner.Match(k) || ((bankedKernel || nandImage) && (len(k) < 52 || !bytes.HasPrefix(k, []byte("\x7fELF\x01\x01\x01")) || k[16] != 2 || k[17] != 0 || k[18] != 8 || k[19] != 0)) {
		return errors.New("This is not an ODR update kernel for the " + modelName)
	}
	return nil
}

// Keep the complete previous rescue in RAM until the independent MTD
// read-back succeeds. writespi's "kernel" argument is the SPI rescue bank.
func replaceRescue(dir, image, dev string, offset int64, r RescueRelease, write func(string) error) error {
	orig, err := os.Open(dev)
	if err != nil {
		return err
	}
	old := filepath.Join(dir, "previous")
	err = save(old, io.NewSectionReader(orig, offset, rescueLimit), rescueLimit)
	orig.Close()
	if err != nil {
		return err
	}
	prev, err := rescueDigest(old, 0, rescueLimit)
	if err != nil {
		return err
	}
	job("working", "Writing the internal SPI rescue. Keep the receiver powered on.")
	err = write(image)
	if err == nil {
		job("working", "Reading back the internal rescue and verifying SHA-256")
		got, e := rescueDigest(dev, offset, r.Bytes)
		if e == nil && got == r.SHA256 {
			return nil
		}
		err = errors.New("The rescue read-back did not match")
	}
	if got, e := rescueDigest(dev, offset, rescueLimit); e == nil && got == prev {
		return fmt.Errorf("%w. Previous rescue verified", err)
	}
	job("working", "Restoring the previous rescue")
	if e := write(old); e == nil {
		if got, e := rescueDigest(dev, offset, rescueLimit); e == nil && got == prev {
			return fmt.Errorf("%w. Previous rescue restored and verified", err)
		}
	}
	return fmt.Errorf("%w. The previous rescue could not be verified. Keep the receiver powered and use the ODR USB stick for recovery", err)
}

func updateRescue(hash, date string) error {
	r, err := rescueRelease(date)
	if err != nil {
		return err
	}
	if r.SHA256 == "" || r.SHA256 != hash {
		return errors.New("The rescue release changed. Check for updates again")
	}
	if err = rescueTarget(); err != nil {
		return err
	}
	installed, err := rescueDigest(rescueReadDev, rescueOffset, r.Bytes)
	if err != nil {
		return err
	}
	if installed == hash {
		job("done", "This rescue build is already installed.")
		return nil
	}
	dir, err := os.MkdirTemp(runDir, "rescue-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	free, err := available(dir)
	if err != nil || free < r.Bytes+rescueLimit+(32<<20) {
		return errors.New("Insufficient RAM to stage the rescue update")
	}
	job("working", "Downloading rescue update")
	jobMu.Lock()
	busyJob.Total, busyJob.Transfer = r.Bytes, true
	jobMu.Unlock()
	res, err := client.Get(r.URL)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 || res.ContentLength >= 0 && res.ContentLength != r.Bytes {
		return errors.New("Unable to download the published rescue image at its expected size")
	}
	image := filepath.Join(dir, "new")
	if err = save(image, io.TeeReader(res.Body, counter{}), r.Bytes); err != nil {
		return err
	}
	job("working", "Checking the "+modelName+" rescue kernel and SHA-256")
	if err = checkRescueImage(image, r); err != nil {
		return err
	}
	if err = replaceRescue(dir, image, rescueReadDev, rescueOffset, r, func(file string) error { return command("/usr/sbin/writespi", file, "kernel") }); err != nil {
		return err
	}
	job("done", "Internal rescue updated and verified. The new version will run the next time you start the internal rescue.")
	return nil
}
