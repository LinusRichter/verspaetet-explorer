// Package dataset downloads published months of the verspaetet dataset from
// HuggingFace, verifies them against their manifest and splits each month
// into one Parquet file per day, so the explorer never has to scan a whole
// month for a single day.
package dataset

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// Manifest is the subset of a published month's manifest.json the explorer uses.
type Manifest struct {
	Year          int    `json:"year"`
	Month         int    `json:"month"`
	SchemaVersion string `json:"schema_version"`
	StopEvents    struct {
		Rows       int64  `json:"rows"`
		SizeBytes  int64  `json:"size_bytes"`
		Sha256     string `json:"sha256"`
		MinScraped string `json:"min_scraped_at"`
		MaxScraped string `json:"max_scraped_at"`
	} `json:"stop_events"`
	Stations struct {
		Sha256 string `json:"sha256"`
	} `json:"stations"`
	ScrapeRuns struct {
		Rows      int64  `json:"rows"`
		SizeBytes int64  `json:"size_bytes"`
		Sha256    string `json:"sha256"`
	} `json:"scrape_runs"`
	Coverage struct {
		DaysWithEvents []string `json:"days_with_events"`
		Downtimes      []string `json:"downtimes"`
	} `json:"coverage"`
}

// Source points at the published dataset.
type Source struct {
	Endpoint string // e.g. https://huggingface.co
	Repo     string // e.g. LinusRichter404/verspaetet
	Client   *http.Client
}

func (s Source) fileURL(path string) string {
	return fmt.Sprintf("%s/datasets/%s/resolve/main/%s", s.Endpoint, s.Repo, path)
}

var monthRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}$`)

// Months lists the published month folders (YYYY-MM), oldest first.
func (s Source) Months(ctx context.Context) ([]string, error) {
	url := fmt.Sprintf("%s/api/datasets/%s/tree/main", s.Endpoint, s.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list dataset: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list dataset: HTTP %d", resp.StatusCode)
	}
	var tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		return nil, fmt.Errorf("list dataset: %w", err)
	}
	var months []string
	for _, e := range tree {
		if e.Type == "directory" && monthRe.MatchString(e.Path) {
			months = append(months, e.Path)
		}
	}
	sort.Strings(months)
	return months, nil
}

// Manifest downloads and parses <month>/manifest.json.
func (s Source) Manifest(ctx context.Context, month string) (*Manifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.fileURL(month+"/manifest.json"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("manifest %s: %w", month, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest %s: HTTP %d", month, resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", month, err)
	}
	return &m, nil
}

// Download fetches path into dest (atomically via a .part file). If wantSha
// is set, the SHA-256 of the download must match or the file is discarded.
func (s Source) Download(ctx context.Context, path, dest, wantSha string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.fileURL(path), nil)
	if err != nil {
		return err
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: HTTP %d", path, resp.StatusCode)
	}
	if size <= 0 {
		size = resp.ContentLength
	}
	part := dest + ".part"
	f, err := os.Create(part)
	if err != nil {
		return err
	}
	h := sha256.New()
	pr := &progressReader{r: resp.Body, total: size, label: path, next: time.Now().Add(10 * time.Second)}
	_, err = io.Copy(io.MultiWriter(f, h), pr)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return fmt.Errorf("download %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); wantSha != "" && got != wantSha {
		os.Remove(part)
		return fmt.Errorf("download %s: checksum mismatch (got %s, manifest says %s)", path, got, wantSha)
	}
	return os.Rename(part, dest)
}

type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	label string
	next  time.Time
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if time.Now().After(p.next) && p.total > 0 {
		log.Printf("  downloading %s: %d%% (%d of %d MB)", p.label, p.done*100/p.total, p.done>>20, p.total>>20)
		p.next = time.Now().Add(10 * time.Second)
	}
	return n, err
}
