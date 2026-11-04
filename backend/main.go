package main

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var web embed.FS

const model = "dm" + modelNumber
const modelName = "DM" + modelNumber
const nandImage = model == "dm520"
const bankedKernel = model == "dm7080" || model == "dm820"
const catalogBase = "https://raw.githubusercontent.com/oe-alliance/bootmenu/master/" + model
const catalogURL = catalogBase + "/images"

// A write request waits for inventory and firmware reads that already started.
// A second write request must never queue behind the first and run afterwards.
type operationGate struct {
	writer  sync.Mutex
	storage sync.RWMutex
}

func (g *operationGate) begin() bool {
	if !g.writer.TryLock() {
		return false
	}
	g.storage.Lock()
	return true
}

func (g *operationGate) end() {
	g.storage.Unlock()
	g.writer.Unlock()
}

var op operationGate
var jobMu sync.Mutex
var staged *Plan
var busyJob = Job{State: "idle"}

type Job struct {
	State, Message              string
	Bytes, Total                int64
	Action, Image, ImageID      string
	Slot                        int
	Transfer                    bool
	Details, BackupID, Filename string
	streamKind, streamDate      string
	DecisionID                  string `json:",omitempty"`
	DestinationAtRisk           bool   `json:",omitempty"`
	DownloadIssue               string `json:",omitempty"`
	DownloadKey                 string `json:",omitempty"`
}

var kernelDecision chan bool // Protected by jobMu, scoped to the current slot-1 install.
var errKernelCancelled = errors.New("Slot 1 installation cancelled; the loaded image is still available")

func confirmSingleImage(reason string) error {
	decision := make(chan bool, 1)
	jobMu.Lock()
	kernelDecision = decision
	busyJob.DecisionID = rand.Text()
	busyJob.State = "kernel-choice"
	busyJob.Message = reason
	jobMu.Unlock()
	defer func() {
		jobMu.Lock()
		kernelDecision = nil
		busyJob.DecisionID = ""
		jobMu.Unlock()
	}()
	select {
	case yes := <-decision:
		if yes {
			job("working", "Single-image installation confirmed")
			return nil
		}
	case <-time.After(15 * time.Minute):
	}
	return errKernelCancelled
}

func answerKernelDecision(w http.ResponseWriter, r *http.Request) {
	var a struct{ DecisionID, Confirm string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&a); err != nil {
		failure(w, err)
		return
	}
	jobMu.Lock()
	defer jobMu.Unlock()
	if kernelDecision == nil || busyJob.State != "kernel-choice" || a.DecisionID != busyJob.DecisionID ||
		(a.Confirm != "disable-multiboot" && a.Confirm != "cancel") {
		http.Error(w, "There is no matching slot 1 decision", http.StatusConflict)
		return
	}
	kernelDecision <- a.Confirm == "disable-multiboot"
	kernelDecision = nil
	reply(w, true)
}

func beginJob(action, image, message string, slot int) {
	jobMu.Lock()
	defer jobMu.Unlock()
	os.Remove(filepath.Join(runDir, "last-job.json"))
	os.Remove(filepath.Join(runDir, "backup"))
	busyJob = Job{State: "working", Action: action, Image: image, Message: message, Slot: slot, Details: message}
}

func job(state, msg string) {
	jobMu.Lock()
	defer jobMu.Unlock()
	if msg != busyJob.Message {
		busyJob.Details += "\n" + msg
	}
	busyJob.State, busyJob.Message = state, msg
	busyJob.Bytes, busyJob.Total, busyJob.Transfer = 0, 0, false
	if state == "done" || state == "error" {
		data, err := json.Marshal(busyJob)
		path := filepath.Join(runDir, "last-job.json")
		if err == nil && os.WriteFile(path+".tmp", data, 0600) == nil {
			os.Rename(path+".tmp", path)
		}
	}
}

func displayName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" || len(name) > 255 || strings.ContainsAny(name, "\r\n\t") {
		return modelName + " image"
	}
	return name
}
func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, e error) { http.Error(w, e.Error(), http.StatusBadRequest) }
func validURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

var client = &http.Client{Timeout: 15 * time.Minute, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, ResponseHeaderTimeout: 30 * time.Second}, CheckRedirect: func(r *http.Request, prior []*http.Request) error {
	if len(prior) > 8 || !validURL(r.URL.String()) {
		return errImageRedirect
	}
	return nil
}}

type Entry struct{ Name, URL, Key string }

func catalog() ([]Entry, error) {
	res, e := client.Get(catalogURL)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf(modelName+" catalogue: HTTP %d", res.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 65537))
	if e != nil || len(b) > 65536 {
		return nil, errors.New("Invalid catalogue")
	}
	list := []Entry{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		if s == "" || strings.HasPrefix(s, "#") {
			continue
		}
		var n, u string
		if strings.HasPrefix(s, "\"") {
			row, e := csv.NewReader(strings.NewReader(s)).Read()
			if e == nil && len(row) == 2 {
				n, u = row[0], row[1]
			}
		} else {
			fields := strings.Fields(s)
			if len(fields) > 1 {
				n, u = fields[0], fields[1]
			}
		}
		if n == "" || !validURL(u) {
			return nil, errors.New("Invalid catalogue row")
		}
		list = append(list, Entry{n, u, downloadKey(u)})
	}
	return list, sc.Err()
}

type counter struct{}

func (counter) Write(p []byte) (int, error) {
	jobMu.Lock()
	busyJob.Bytes += int64(len(p))
	jobMu.Unlock()
	return len(p), nil
}
func discardStaged() {
	if staged != nil {
		os.RemoveAll(staged.Directory)
		staged = nil
	}
}
func stage(src io.Reader, total int64) error {
	discardStaged()
	dir, e := os.MkdirTemp(runDir, "image-")
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dir)
		}
	}()
	free, e := available(dir)
	if e != nil {
		return e
	}
	limit := min(maxUpload, free-(32<<20))
	if limit <= 0 || total > limit {
		return errors.New("Insufficient RAM for image")
	}
	job("working", "Receiving image")
	jobMu.Lock()
	busyJob.Total = total
	busyJob.Transfer = true
	jobMu.Unlock()
	file := filepath.Join(dir, "input")
	if e = save(file, io.TeeReader(src, counter{}), limit); e != nil {
		return e
	}
	job("working", "Reading image package")
	p, e := openImage(dir, file)
	if e != nil {
		return e
	}
	staged = &p
	ok = true
	jobMu.Lock()
	busyJob.ImageID = filepath.Base(dir)
	jobMu.Unlock()
	job("ready", "Image loaded. Choose the target.")
	return nil
}
func info(w http.ResponseWriter) {
	m := map[string]any{"Model": readText("/proc/stb/info/model"), "Kernel": readText("/proc/sys/kernel/osrelease"), "Memory": strings.Split(readText("/proc/meminfo"), "\n")[0], "MAC": readText("/sys/class/net/eth0/address"), "StorageSectors": readText("/sys/class/block/mmcblk0/size"), "Version": "1.0"}
	if eth, e := net.InterfaceByName("eth0"); e == nil {
		if aa, e := eth.Addrs(); e == nil {
			for _, a := range aa {
				if strings.Contains(a.String(), ".") {
					m["IP"] = strings.Split(a.String(), "/")[0]
					break
				}
			}
		}
	}
	l, warning, e := imageInventory()
	if e != nil {
		m["LayoutError"] = e.Error()
	} else {
		m["Layout"] = l
		if warning != "" {
			m["ImageWarning"] = warning
		}
	}
	reply(w, m)
}

func imageInventory() (Layout, string, error) {
	var warning string
	l, e := layout()
	if e == nil {
		for _, d := range []string{rootDev, dataDev} {
			needed := false
			for _, s := range l.Slots {
				if s.Device == d {
					needed = true
				}
			}
			if !needed {
				continue
			}
			er := mounted(d, rootFS, false, func(b string) error {
				for i, s := range l.Slots {
					if s.Device == d {
						l.Slots[i].Incomplete = incompleteSlot(b, s)
						l.Slots[i].Installed = slotReady(b, s)
						if l.Slots[i].Installed {
							l.Slots[i].Label = imageLabel(slotRoot(b, s))
						}
					}
				}
				return nil
			})
			if er != nil {
				warning = er.Error()
			}
		}
	}
	return l, warning, e
}

func imageLabel(dir string) string {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ""
	}
	defer root.Close()
	// Open the image's release metadata beneath this slot. Some images
	// link etc/os-release to usr/lib/os-release.
	for _, path := range []string{"etc/os-release", "usr/lib/os-release"} {
		f, err := root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			continue
		}
		var label string
		if st, e := f.Stat(); e == nil && st.Mode().IsRegular() {
			sc := bufio.NewScanner(io.LimitReader(f, 8192))
			for sc.Scan() {
				if value, ok := strings.CutPrefix(sc.Text(), "PRETTY_NAME="); ok {
					label = strings.Trim(strings.TrimSpace(value), "\"'")
					break
				}
			}
		}
		f.Close()
		if len(label) > 0 && len(label) <= 100 {
			return label
		}
	}
	return ""
}
func api(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	action := strings.TrimPrefix(r.URL.Path, "/api/")
	if r.Method == "GET" {
		switch action {
		case "firmware":
			rel, err := rescueRelease(r.URL.Query().Get("date"))
			if err != nil {
				failure(w, err)
				return
			}
			installed := false
			if rel.Version != "" {
				if !op.storage.TryRLock() {
					http.Error(w, "Operation in progress", 409)
					return
				}
				defer op.storage.RUnlock()
				if err = rescueTarget(); err != nil {
					failure(w, err)
					return
				}
				sum, e := rescueDigest(rescueReadDev, rescueOffset, rel.Bytes)
				if e != nil {
					failure(w, e)
					return
				}
				installed = sum == rel.SHA256
			}
			reply(w, struct {
				Release   RescueRelease
				Installed bool
			}{rel, installed})
		case "backup-file":
			serveBackup(w, r)
		case "device":
			reply(w, deviceInfo())
		case "job":
			jobMu.Lock()
			v := busyJob
			jobMu.Unlock()
			reply(w, v)
		case "info":
			if !op.storage.TryRLock() {
				http.Error(w, "Operation in progress", 409)
				return
			}
			defer op.storage.RUnlock()
			info(w)
		case "catalog":
			v, e := catalog()
			if e != nil {
				failure(w, e)
			} else {
				reply(w, v)
			}
		default:
			http.NotFound(w, r)
		}
		return
	}
	if r.Method != "POST" || r.Header.Get("X-ODR-Request") != "1" {
		http.Error(w, "Invalid request", 403)
		return
	}
	if o := r.Header.Get("Origin"); o != "" {
		u, e := url.Parse(o)
		if e != nil || u.Host != r.Host {
			http.Error(w, "Invalid origin", 403)
			return
		}
	}
	if action == "kernel-decision" {
		answerKernelDecision(w, r)
		return
	}
	if !op.begin() {
		http.Error(w, "Operation in progress", 409)
		return
	}
	if action == "upload" {
		defer op.end()
		name, _ := url.PathUnescape(r.Header.Get("X-ODR-Image"))
		beginJob("upload", displayName(name), "Receiving image", 0)
		e := stage(http.MaxBytesReader(w, r.Body, maxUpload), r.ContentLength)
		if e != nil {
			job("error", e.Error())
			failure(w, e)
		} else {
			reply(w, true)
		}
		return
	}
	var arg struct {
		Slot                  int
		URL, Confirm, ImageID string
		Kind, Date, Name      string
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&arg); e != nil {
		op.end()
		failure(w, e)
		return
	}
	var work func() error
	var imageName string
	switch action {
	case "rescue-update":
		if arg.Confirm != action || len(arg.ImageID) != 64 {
			op.end()
			failure(w, errors.New("Select and confirm a published "+modelName+" rescue update"))
			return
		}
		work = func() error { return updateRescue(arg.ImageID, arg.Date) }
	case "backup":
		if arg.Kind != "image" && arg.Kind != "settings" {
			op.end()
			failure(w, errors.New("Choose an image or settings backup"))
			return
		}
		if nandImage {
			if arg.Slot != 1 {
				op.end()
				failure(w, errors.New("Select the internal NAND image"))
				return
			}
			discardStaged()
			beginJob("backup", "", "Choose Download backup to save the archive directly to your device.", arg.Slot)
			id := rand.Text()
			jobMu.Lock()
			busyJob.State, busyJob.BackupID = "backup-download", id
			busyJob.streamKind, busyJob.streamDate = arg.Kind, arg.Date
			jobMu.Unlock()
			op.end()
			reply(w, map[string]string{"BackupID": id})
			return
		}
		work = func() error {
			discardStaged()
			return backupSlot(arg.Slot, arg.Kind, arg.Date)
		}
	case "download":
		if !validURL(arg.URL) {
			op.end()
			failure(w, errors.New("HTTPS image URL required"))
			return
		}
		u, _ := url.Parse(arg.URL)
		imageName = displayName(u.Path)
		if arg.Name != "" {
			imageName = displayName(arg.Name)
		}
		work = func() error {
			discardStaged()
			return downloadImage(arg.URL)
		}
	case "install", "start", "remove", "restart":
		if arg.Confirm != action {
			op.end()
			failure(w, errors.New("Confirmation required"))
			return
		}
		if action == "install" && arg.ImageID != "" && (staged == nil || filepath.Base(staged.Directory) != arg.ImageID) {
			op.end()
			http.Error(w, "The prepared image changed. Choose the destination again.", http.StatusConflict)
			return
		}
		if action == "install" {
			jobMu.Lock()
			imageName = busyJob.Image
			jobMu.Unlock()
		}
		work = func() error {
			if action == "restart" {
				return reboot()
			}
			l, e := layout()
			if e != nil {
				return e
			}
			if action == "remove" {
				if e = removeMulti(l); e == nil {
					job("done", "Multiboot removed. Slot 1 is the internal image.")
				}
				return e
			}
			s, e := lookup(l, arg.Slot)
			if e != nil {
				return e
			}
			if action == "start" {
				if e = startSlot(l, s); e != nil {
					return e
				}
				return reboot()
			}
			if staged == nil {
				return errors.New("Upload or download an image first")
			}
			if e = install(*staged, l, s); errors.Is(e, errKernelCancelled) {
				jobMu.Lock()
				busyJob.ImageID = filepath.Base(staged.Directory)
				jobMu.Unlock()
				job("ready", e.Error())
				return nil
			} else if e == nil {
				os.RemoveAll(staged.Directory)
				staged = nil
				job("done", "Image installed. You can start it now.")
			} else if nandImage && !staged.UBI {
				// The final TAR read consumes its tmpfs pages. A retry needs a new upload.
				discardStaged()
			}
			return e
		}
	default:
		op.end()
		http.NotFound(w, r)
		return
	}
	beginJob(action, imageName, map[string]string{"download": "Contacting image server", "install": "Installing image", "remove": "Removing guest slots and STARTUP files", "start": "Selecting image", "restart": "Restarting", "backup": "Reading the backup source", "rescue-update": "Checking the published rescue update"}[action], arg.Slot)
	go func() {
		defer op.end()
		if e := work(); e != nil {
			var issue *downloadError
			if action == "download" && errors.As(e, &issue) {
				jobMu.Lock()
				busyJob.DownloadIssue, busyJob.DownloadKey = issue.title, downloadKey(arg.URL)
				jobMu.Unlock()
			}
			job("error", e.Error())
		}
	}()
	reply(w, true)
}
func reboot() error {
	job("reboot", "Dreambox is restarting.")
	time.Sleep(800 * time.Millisecond)
	return command("/sbin/reboot")
}
func matchesReceiver() bool {
	actual := readText("/proc/stb/info/model")
	if nandImage {
		return (actual == "dm520" || actual == "dm525" || actual == "dm520mini") && readText("/proc/stb/info/mid") == "21"
	}
	return actual == model
}
func main() {
	if len(os.Args) == 3 && os.Args[1] == "extract" {
		if e := extractChild(os.Args[2]); e != nil {
			fmt.Fprintln(os.Stderr, e)
			os.Exit(1)
		}
		return
	}
	if !matchesReceiver() || readText("/etc/odr-"+model) != "1.0" {
		fmt.Fprintln(os.Stderr, modelName+" ODR initramfs required")
		os.Exit(1)
	}
	port := 80
	if len(os.Args) == 2 {
		p, e := strconv.Atoi(os.Args[1])
		if e != nil || p < 1 || p > 65535 {
			os.Exit(1)
		}
		port = p
	}
	if e := os.MkdirAll(runDir, 0700); e != nil {
		panic(e)
	}
	if err := prepareWorkspace(); err != nil {
		panic(err)
	}
	if data, err := os.ReadFile(filepath.Join(runDir, "last-job.json")); err == nil {
		var last Job
		if json.Unmarshal(data, &last) == nil && (last.State == "done" || last.State == "error") {
			busyJob = last
		}
	}
	if port == 80 {
		go superviseHDMI()
	}
	assets, _ := fs.Sub(web, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", api)
	files := http.FileServer(http.FS(assets))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	srv := http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux, ReadHeaderTimeout: 8 * time.Second, IdleTimeout: 60 * time.Second}
	if e := srv.ListenAndServe(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
