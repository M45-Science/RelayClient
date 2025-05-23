package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Masterminds/semver"
)

const (
	baseURL     = "https://m45sci.xyz/relayClient/"
	UpdateJSON  = "https://m45sci.xyz/relayClient/relayClient.json"
	updateDebug = false
)

type downloadInfo struct {
	Link     string `json:"link"`
	Checksum string `json:"sha256"`
}

type Entry struct {
	Version string         `json:"version"`
	Date    int64          `json:"utc-unixnano"`
	Links   []downloadInfo `json:"links"`
}

func OSString() (string, error) {
	switch runtime.GOOS {
	case "windows":
		return "win", nil
	case "darwin":
		return "mac", nil
	case "linux":
		return "linux", nil
	default:
		return "", fmt.Errorf("did not detect a valid host OS")
	}
}

func CheckUpdate() (bool, error) {
	ourVersion, err := semver.NewVersion(version)
	if err != nil {
		return false, fmt.Errorf("This is not published build, not checking for update.")
	}
	doLog("Checking for relayClient updates.")
	jsonBytes, fileName, err := httpGet(UpdateJSON)
	if err != nil {
		return false, err
	}

	if len(jsonBytes) == 0 {
		return false, fmt.Errorf("empty response")
	}
	if updateDebug {
		doLog("len: %v, name: %v\n", len(jsonBytes), fileName)
	}

	jsonReader := bytes.NewReader(jsonBytes)
	decoder := json.NewDecoder(jsonReader)
	entries := []Entry{}
	if err := decoder.Decode(&entries); err != nil && err != io.EOF {
		doLog("error decoding json: %v\n", err)
		os.Exit(1)
	}

	remoteNewest, err := NewestEntry(entries)
	if err != nil {
		return false, fmt.Errorf("NewestEntry: %v", err)
	}

	remoteVersion, err := semver.NewVersion(remoteNewest.Version)
	if err != nil {
		return false, fmt.Errorf("NewVersion: %v", err)
	}
	if !ourVersion.LessThan(remoteVersion) {
		doLog("clientRelay is update to date.")
		return false, nil
	}

	doLog("Found new version: %v\n", remoteNewest.Version)

	goos, err := OSString()
	if err != nil {
		return false, fmt.Errorf("OSString: %v", err)
	}
	var updateLink *downloadInfo
	for _, link := range remoteNewest.Links {
		if strings.Contains(
			strings.ToLower(link.Link),
			strings.ToLower("-"+goos+"-")) {
			updateLink = &link
			break
		}
	}
	if updateLink == nil {
		return false, fmt.Errorf("No valid download link found")
	} else {
		//Mac version can not update without being signed.
		if strings.EqualFold(goos, "mac") {
			openInBrowser(downloadURL)
			return false, nil
		}
		doLog("Downloading: %v\n", path.Base(updateLink.Link))
		data, fileName, err := httpGet(baseURL + updateLink.Link)
		if err != nil {
			return false, fmt.Errorf("httpGet: %v", err)
		}
		if updateDebug {
			doLog("Filename: %v, Size: %vb\n", fileName, len(data))
		}
		checksum, err := computeChecksum(data)
		if checksum != updateLink.Checksum {
			return false, fmt.Errorf("file: %v - checksum is invalid.", fileName)
		} else {
			doLog("Download complete, updating.")
			if err := UnzipToExeDir(data); err != nil {
				doLog("Extraction failed: %v\n", err)
				os.Exit(1)
			}
			doLog("Update complete, restarting.")
			relaunch()
			return true, nil
		}
	}
}

// relaunch replaces the current process with update_binary (or update_binary.exe).
// It never returns on success; on failure it returns an error.
func relaunch() error {
	// 1) Find the path to the currently running executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable path: %w", err)
	}

	// 2) Compute the new binary name in the same dir
	dir := filepath.Dir(exePath)
	ext := filepath.Ext(exePath) // ".exe" on Windows, "" elsewhere
	newName := "update_binary" + ext
	newPath := filepath.Join(dir, newName)

	// 3) Verify the file exists
	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("update binary not found at %q: %w", newPath, err)
	}

	// 4) Grab the original args (including os.Args[0])
	args := os.Args
	//    On Windows exec.Command wants args[1:], on Unix Exec wants the full slice
	argsForSpawn := args[1:]

	// 5) Inherit the current environment
	env := os.Environ()

	if runtime.GOOS == "windows" {
		// Windows: spawn a new process and exit this one
		cmd := exec.Command(newPath, argsForSpawn...)
		cmd.Env = env
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start updater: %w", err)
		}
		// Kill ourselves so that only the updater remains
		os.Exit(0)
		// unreachable
		return nil
	}

	// Unix (Linux, macOS, etc.): replace the current process
	return syscall.Exec(newPath, args, env)
}

func computeChecksum(data []byte) (string, error) {
	dataReader := bytes.NewReader(data)
	h := sha256.New()
	if _, err := io.Copy(h, dataReader); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func httpGet(URL string) ([]byte, string, error) {
	// Set timeout
	hClient := http.Client{
		Timeout: time.Second * 30,
	}

	//HTTP GET
	req, err := http.NewRequest(http.MethodGet, URL, nil)
	if err != nil {
		return nil, "", errors.New("get failed: " + err.Error())
	}

	//Get response
	res, err := hClient.Do(req)
	if err != nil {
		return nil, "", errors.New("failed to get response: " + err.Error())
	}

	//Check status code
	if res.StatusCode != 200 {
		return nil, "", fmt.Errorf("http status error: %v", res.StatusCode)
	}

	//Close once complete, if valid
	if res.Body != nil {
		defer res.Body.Close()
	}

	//Read all
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, "", errors.New("unable to read response body: " + err.Error())
	}

	//Check data length
	if res.ContentLength > 0 {
		if len(body) != int(res.ContentLength) {
			return nil, "", errors.New("data ended early")
		}
	} else if res.ContentLength != -1 {
		return nil, "", errors.New("content length did not match")
	}

	realurl := res.Request.URL.String()
	parts := strings.Split(realurl, "/")
	query := parts[len(parts)-1]
	parts = strings.Split(query, "?")
	return body, parts[0], nil
}

func NewestEntry(entries []Entry) (*Entry, error) {
	// pair up each Entry with its parsed semver.Version
	type pair struct {
		e   *Entry
		ver *semver.Version
	}
	var pairs []pair
	for i := range entries {
		e := &entries[i]
		v, err := semver.NewVersion(e.Version)
		if err != nil {
			// skip unparsable versions
			continue
		}
		pairs = append(pairs, pair{e: e, ver: v})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("semutil: no valid versions found in entries")
	}

	// sort ascending by version (lowest → highest)
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].ver.LessThan(pairs[j].ver)
	})

	// the last element has the highest version
	return pairs[len(pairs)-1].e, nil
}

// UnzipToExeDir unpacks the zip from data into the directory of the running binary.
func UnzipToExeDir(data []byte) error {
	// figure out where the binary lives
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	exeDir := filepath.Dir(exePath)
	return UnzipToDir(data, exeDir)
}

// UnzipToDir unpacks the zip archive in data into destDir, preserving folders and file modes.
// Any entry whose base name is "M45-Relay-Client" or "M45-Relay-Client.exe" will be
// written as "update_binary" plus the same extension (e.g. ".exe") if present.
func UnzipToDir(data []byte, destDir string) error {
	// open zip reader
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("zip.NewReader: %w", err)
	}

	for _, f := range r.File {
		// original path components in the zip
		origDir, origName := filepath.Split(f.Name)

		// determine new filename (rename special cases)
		newName := origName
		if origName == "M45-Relay-Client" || origName == "M45-Relay-Client.exe" {
			ext := filepath.Ext(origName)   // e.g. ".exe" or ""
			newName = "update_binary" + ext // preserve extension
		}

		targetPath := filepath.Join(destDir, origDir, newName)

		if f.FileInfo().IsDir() {
			// create sub‑directory
			if err := os.MkdirAll(targetPath, os.ModePerm); err != nil {
				return fmt.Errorf("mkdir %q: %w", targetPath, err)
			}
			continue
		}

		// make sure parent dir exists
		parentDir := filepath.Dir(targetPath)
		if err := os.MkdirAll(parentDir, os.ModePerm); err != nil {
			return fmt.Errorf("mkdirall %q: %w", parentDir, err)
		}

		// open file inside zip
		inFile, err := f.Open()
		if err != nil {
			return fmt.Errorf("open %q in zip: %w", f.Name, err)
		}
		defer inFile.Close()

		// create destination file
		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, f.Mode())
		if err != nil {
			return fmt.Errorf("open file %q: %w", targetPath, err)
		}
		defer outFile.Close()

		// copy contents
		if _, err := io.Copy(outFile, inFile); err != nil {
			return fmt.Errorf("copy to %q: %w", targetPath, err)
		}
	}

	return nil
}
