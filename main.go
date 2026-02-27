package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	workerSem    chan struct{}
	queueCount   int64
	fileCache    *FileCache
	ocrEngine    string // "paddle" or "tesseract"
	wrapperPath  string // path to paddleocr_wrapper.py
	ocrThreads   int    // threads per PaddleOCR call
)


func cacheKey(prefix, lang, mode string) string {
	return fmt.Sprintf("%s|%s|%s", prefix, lang, mode)
}

func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// --- File Cache (disk-based) ---

type FileCacheEntry struct {
	URL       string
	Path      string
	Filename  string
	Size      int64
	CreatedAt time.Time
}

type FileCache struct {
	mu       sync.RWMutex
	items    map[string]*FileCacheEntry // key: SHA256(URL)
	dir      string
	ttl      time.Duration
	maxItems int
}

func NewFileCache(dir string, ttl time.Duration, maxItems int) *FileCache {
	os.MkdirAll(dir, 0755)
	fc := &FileCache{
		items:    make(map[string]*FileCacheEntry),
		dir:      dir,
		ttl:      ttl,
		maxItems: maxItems,
	}
	go fc.cleanup()
	return fc
}

func (fc *FileCache) urlKey(url string) string {
	h := sha256.Sum256([]byte(url))
	return fmt.Sprintf("%x", h)
}

func (fc *FileCache) Get(url string) (*FileCacheEntry, bool) {
	key := fc.urlKey(url)
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	entry, ok := fc.items[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.CreatedAt) > fc.ttl {
		return nil, false
	}
	// Verify file still exists on disk
	if _, err := os.Stat(entry.Path); err != nil {
		return nil, false
	}
	return entry, true
}

// Store downloads and caches a URL, returning the entry. If already cached, returns existing.
func (fc *FileCache) Store(url string) (*FileCacheEntry, bool, error) {
	// Check if already cached
	if entry, ok := fc.Get(url); ok {
		return entry, true, nil
	}

	// Download to a temp dir first, then move to cache dir
	tmpDir, err := os.MkdirTemp("", "pdfread-dl-")
	if err != nil {
		return nil, false, fmt.Errorf("temp dir error: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	filename, err := downloadFile(url, tmpDir)
	if err != nil {
		return nil, false, fmt.Errorf("download failed: %w", err)
	}

	srcPath := filepath.Join(tmpDir, filename)
	info, err := os.Stat(srcPath)
	if err != nil {
		return nil, false, err
	}

	key := fc.urlKey(url)
	ext := filepath.Ext(filename)
	destPath := filepath.Join(fc.dir, key+ext)

	// Copy file to cache dir
	src, err := os.Open(srcPath)
	if err != nil {
		return nil, false, err
	}
	defer src.Close()

	dst, err := os.Create(destPath)
	if err != nil {
		return nil, false, err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(destPath)
		return nil, false, err
	}
	dst.Close()

	entry := &FileCacheEntry{
		URL:       url,
		Path:      destPath,
		Filename:  filename,
		Size:      info.Size(),
		CreatedAt: time.Now(),
	}

	fc.mu.Lock()
	// Evict if at capacity
	if len(fc.items) >= fc.maxItems {
		fc.evictOldest(fc.maxItems / 2)
	}
	fc.items[key] = entry
	fc.mu.Unlock()

	return entry, false, nil
}

func (fc *FileCache) evictOldest(count int) {
	type kv struct {
		key string
		ts  time.Time
	}
	list := make([]kv, 0, len(fc.items))
	for k, v := range fc.items {
		list = append(list, kv{k, v.CreatedAt})
	}
	for i := 0; i < count && i < len(list); i++ {
		oldest := i
		for j := i + 1; j < len(list); j++ {
			if list[j].ts.Before(list[oldest].ts) {
				oldest = j
			}
		}
		list[i], list[oldest] = list[oldest], list[i]
		// Remove file and associated text files from disk
		if entry, ok := fc.items[list[i].key]; ok {
			os.Remove(entry.Path)
			// Remove all text cache files for this URL
			txtPattern := filepath.Join(fc.dir, list[i].key+"_*_*.txt")
			txtFiles, _ := filepath.Glob(txtPattern)
			for _, tf := range txtFiles {
				os.Remove(tf)
			}
		}
		delete(fc.items, list[i].key)
	}
}

func (fc *FileCache) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	for range ticker.C {
		fc.mu.Lock()
		now := time.Now()
		expired := 0
		for k, v := range fc.items {
			if now.Sub(v.CreatedAt) > fc.ttl {
				os.Remove(v.Path)
				// Remove associated text cache files
				txtPattern := filepath.Join(fc.dir, k+"_*_*.txt")
				txtFiles, _ := filepath.Glob(txtPattern)
				for _, tf := range txtFiles {
					os.Remove(tf)
				}
				delete(fc.items, k)
				expired++
			}
		}
		fc.mu.Unlock()
		if expired > 0 {
			log.Printf("[file-cache] expired %d entries, %d remaining", expired, fc.Len())
		}
		// Also clean orphaned text files past TTL
		allTxt, _ := filepath.Glob(filepath.Join(fc.dir, "*_*_*.txt"))
		for _, tf := range allTxt {
			if info, err := os.Stat(tf); err == nil && now.Sub(info.ModTime()) > fc.ttl {
				os.Remove(tf)
			}
		}
	}
}

func (fc *FileCache) Len() int {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	return len(fc.items)
}

func (fc *FileCache) TotalSize() int64 {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	var total int64
	for _, v := range fc.items {
		total += v.Size
	}
	return total
}

// textPath returns the disk path for a cached text result.
func (fc *FileCache) textPath(url, lang, mode string) string {
	key := fc.urlKey(url)
	return filepath.Join(fc.dir, fmt.Sprintf("%s_%s_%s.txt", key, lang, mode))
}

// GetText returns cached OCR/extract text for a URL+lang+mode combo.
func (fc *FileCache) GetText(url, lang, mode string) (string, bool) {
	path := fc.textPath(url, lang, mode)
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	// TTL check using file mod time
	if time.Since(info.ModTime()) > fc.ttl {
		os.Remove(path)
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// SetText stores OCR/extract text result to disk.
func (fc *FileCache) SetText(url, lang, mode, text string) {
	path := fc.textPath(url, lang, mode)
	os.WriteFile(path, []byte(text), 0644)
}

// TextEntries returns the count of cached text files.
func (fc *FileCache) TextEntries() int {
	matches, _ := filepath.Glob(filepath.Join(fc.dir, "*_*_*.txt"))
	return len(matches)
}

// --- HTTP ---

type ExtractResponse struct {
	Success bool   `json:"success"`
	Text    string `json:"text,omitempty"`
	Error   string `json:"error,omitempty"`
	Cached  bool   `json:"cached"`
}

func main() {
	port := flag.Int("port", 8090, "port to listen on")
	workers := flag.Int("workers", 2, "max concurrent extract jobs")
	cacheTTL := flag.Int("cache-ttl", 2880, "cache TTL in minutes")
	fileCacheDir := flag.String("file-cache-dir", "/tmp/pdfread-cache", "directory for file cache")
	fileCacheMax := flag.Int("file-cache-max", 100, "max cached files")
	engine := flag.String("ocr-engine", "paddle", "OCR engine: paddle or tesseract")
	threads := flag.Int("ocr-threads", 4, "threads per PaddleOCR call")
	flag.Parse()

	ocrEngine = *engine
	ocrThreads = *threads
	workerSem = make(chan struct{}, *workers)
	fileCache = NewFileCache(*fileCacheDir, time.Duration(*cacheTTL)*time.Minute, *fileCacheMax)

	// Resolve wrapper path (next to the binary)
	exe, _ := os.Executable()
	wrapperPath = filepath.Join(filepath.Dir(exe), "paddleocr_wrapper.py")
	// Fallback: check script dir
	if _, err := os.Stat(wrapperPath); err != nil {
		// Try current working directory
		if wd, err := os.Getwd(); err == nil {
			alt := filepath.Join(wd, "paddleocr_wrapper.py")
			if _, err := os.Stat(alt); err == nil {
				wrapperPath = alt
			}
		}
	}

	if ocrEngine == "paddle" {
		if _, err := os.Stat(wrapperPath); err != nil {
			log.Printf("[WARN] paddleocr_wrapper.py not found at %s, falling back to tesseract", wrapperPath)
			ocrEngine = "tesseract"
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/extract", handleExtract)
	mux.HandleFunc("/download", handleDownload)
	mux.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("pdf-read-service starting on %s (workers: %d, ttl: %dm, ocr: %s, threads: %d, file-cache: %s/%d files)",
		addr, *workers, *cacheTTL, ocrEngine, ocrThreads, *fileCacheDir, *fileCacheMax)
	log.Fatal(http.ListenAndServe(addr, corsMiddleware(mux)))
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	active := len(workerSem)
	waiting := atomic.LoadInt64(&queueCount) - int64(active)
	if waiting < 0 {
		waiting = 0
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":            true,
		"ocr_engine":         ocrEngine,
		"active_jobs":        active,
		"waiting_jobs":       waiting,
		"worker_limit":       cap(workerSem),
		"text_cache_entries": fileCache.TextEntries(),
		"file_cache_entries": fileCache.Len(),
		"file_cache_size":    fileCache.TotalSize(),
	})
}

// --- Download endpoint ---

type DownloadFileResult struct {
	URL    string `json:"url"`
	Cached bool   `json:"cached"`
	Size   int64  `json:"size"`
	Error  string `json:"error,omitempty"`
}

type DownloadResponse struct {
	Success bool                 `json:"success"`
	Files   []DownloadFileResult `json:"files"`
	Error   string               `json:"error,omitempty"`
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, DownloadResponse{Success: false, Error: "POST required"})
		return
	}

	// Parse flexible JSON body:
	//   {"url": "..."}
	//   {"urls": ["...", "..."]}
	//   [{"url": "..."}, {"url": "..."}]
	var urls []string

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, DownloadResponse{Success: false, Error: "read body error: " + err.Error()})
		return
	}

	// Trim whitespace to detect format
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) == 0 {
		writeJSON(w, http.StatusBadRequest, DownloadResponse{Success: false, Error: "empty body"})
		return
	}

	if trimmed[0] == '[' {
		// Array format: [{"url": "..."}, ...]
		var arr []struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(body, &arr); err != nil {
			writeJSON(w, http.StatusBadRequest, DownloadResponse{Success: false, Error: "invalid JSON array: " + err.Error()})
			return
		}
		for _, item := range arr {
			if item.URL != "" {
				urls = append(urls, item.URL)
			}
		}
	} else {
		// Object format — url can be string or []string
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(body, &raw); err != nil {
			writeJSON(w, http.StatusBadRequest, DownloadResponse{Success: false, Error: "invalid JSON: " + err.Error()})
			return
		}

		// Parse "url" field (string or []string)
		if urlRaw, ok := raw["url"]; ok {
			var single string
			if err := json.Unmarshal(urlRaw, &single); err == nil {
				urls = append(urls, single)
			} else {
				var multi []string
				if err := json.Unmarshal(urlRaw, &multi); err == nil {
					urls = append(urls, multi...)
				}
			}
		}

		// Parse "urls" field ([]string)
		if urlsRaw, ok := raw["urls"]; ok {
			var multi []string
			if err := json.Unmarshal(urlsRaw, &multi); err == nil {
				urls = append(urls, multi...)
			}
		}
	}

	if len(urls) == 0 {
		writeJSON(w, http.StatusBadRequest, DownloadResponse{Success: false, Error: "no URLs provided"})
		return
	}

	// Paralel indirme — max 5 eşzamanlı
	results := make([]DownloadFileResult, len(urls))
	var wg sync.WaitGroup
	dlSem := make(chan struct{}, 5)

	for i, u := range urls {
		wg.Add(1)
		go func(idx int, url string) {
			defer wg.Done()
			dlSem <- struct{}{}
			defer func() { <-dlSem }()

			entry, cached, err := fileCache.Store(url)
			if err != nil {
				log.Printf("[download] failed: %s — %v", url, err)
				results[idx] = DownloadFileResult{URL: url, Error: err.Error()}
			} else {
				if cached {
					log.Printf("[download] already cached: %s", url)
				} else {
					log.Printf("[download] cached: %s (%d bytes)", url, entry.Size)
				}
				results[idx] = DownloadFileResult{URL: url, Cached: cached, Size: entry.Size}
			}
		}(i, u)
	}
	wg.Wait()

	writeJSON(w, http.StatusOK, DownloadResponse{Success: true, Files: results})
}

func handleExtract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ExtractResponse{Success: false, Error: "POST required"})
		return
	}

	lang := ""
	mode := ""
	var tmpDir string
	var tmpFile string
	var filename string
	var cacheID string // URL veya dosya hash — text cache key

	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "application/json") {
		var req struct {
			URL  string `json:"url"`
			Lang string `json:"lang"`
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "invalid JSON: " + err.Error()})
			return
		}
		if req.URL == "" {
			writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "url field required"})
			return
		}
		lang = req.Lang
		mode = req.Mode
		if lang == "" {
			lang = "eng"
		}
		if mode == "" {
			mode = "ocr"
		}

		// Text cache kontrolu — worker slot beklemeden hemen dön
		if text, ok := fileCache.GetText(req.URL, lang, mode); ok {
			log.Printf("[text-cache] hit: %s", req.URL)
			writeJSON(w, http.StatusOK, ExtractResponse{Success: true, Text: text, Cached: true})
			return
		}
		cacheID = req.URL

		// Dosya indirme — worker slot beklemeden yap (I/O bound, CPU değil)
		if entry, ok := fileCache.Get(req.URL); ok {
			log.Printf("[file-cache] hit: %s → %s", req.URL, entry.Path)
			var err error
			tmpDir, err = os.MkdirTemp("", "pdfread-")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp dir error"})
				return
			}
			filename = entry.Filename
			tmpFile = filepath.Join(tmpDir, filename)
			if err := copyFile(entry.Path, tmpFile); err != nil {
				os.RemoveAll(tmpDir)
				writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "cache read error: " + err.Error()})
				return
			}
		} else {
			entry, _, err := fileCache.Store(req.URL)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "download failed: " + err.Error()})
				return
			}
			log.Printf("[file-cache] stored: %s (%d bytes)", req.URL, entry.Size)

			tmpDir, err = os.MkdirTemp("", "pdfread-")
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp dir error"})
				return
			}
			filename = entry.Filename
			tmpFile = filepath.Join(tmpDir, filename)
			if err := copyFile(entry.Path, tmpFile); err != nil {
				os.RemoveAll(tmpDir)
				writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "cache read error: " + err.Error()})
				return
			}
		}

		// Worker slot sadece OCR/extract için bekle
		current := atomic.AddInt64(&queueCount, 1)
		defer atomic.AddInt64(&queueCount, -1)
		log.Printf("[queue] job queued (queue: %d)", current)
		workerSem <- struct{}{}
		defer func() { <-workerSem }()
		log.Printf("[queue] job started (queue: %d)", atomic.LoadInt64(&queueCount))

	} else {
		if err := r.ParseMultipartForm(50 << 20); err != nil {
			writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "invalid multipart form: " + err.Error()})
			return
		}
		lang = r.FormValue("lang")
		mode = r.FormValue("mode")

		file, header, err := r.FormFile("file")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "file field required"})
			return
		}
		defer file.Close()

		tmpDir, err = os.MkdirTemp("", "pdfread-")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp dir error"})
			return
		}

		filename = header.Filename
		tmpFile = filepath.Join(tmpDir, filename)
		out, err := os.Create(tmpFile)
		if err != nil {
			os.RemoveAll(tmpDir)
			writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp file error"})
			return
		}
		if _, err := io.Copy(out, file); err != nil {
			out.Close()
			os.RemoveAll(tmpDir)
			writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "file write error"})
			return
		}
		out.Close()

		if lang == "" {
			lang = "eng"
		}
		if mode == "" {
			mode = "ocr"
		}

		// Dosya hash ile text cache kontrolu (disk tabanlı)
		hash := fileHash(tmpFile)
		if hash != "" {
			cacheID = cacheKey(hash, lang, mode)
			if text, ok := fileCache.GetText(hash, lang, mode); ok {
				os.RemoveAll(tmpDir)
				log.Printf("[text-cache] hit: %s (%s)", header.Filename, hash[:20])
				writeJSON(w, http.StatusOK, ExtractResponse{Success: true, Text: text, Cached: true})
				return
			}
		}

		// Worker slot sadece OCR/extract için bekle
		current := atomic.AddInt64(&queueCount, 1)
		defer atomic.AddInt64(&queueCount, -1)
		log.Printf("[queue] job queued (queue: %d)", current)
		workerSem <- struct{}{}
		defer func() { <-workerSem }()
		log.Printf("[queue] job started (queue: %d)", atomic.LoadInt64(&queueCount))
	}

	defer os.RemoveAll(tmpDir)

	// Extract
	ext := strings.ToLower(filepath.Ext(filename))
	var text string
	var err error

	switch ext {
	case ".pdf":
		text, err = extractPDF(tmpFile, lang, mode)
	case ".png", ".jpg", ".jpeg", ".tiff", ".tif", ".bmp", ".gif", ".webp":
		text, err = extractImage(tmpFile, lang)
	default:
		writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "unsupported file type: " + ext})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: err.Error()})
		return
	}

	result := strings.TrimSpace(text)

	// Sonucu disk text cache'e yaz
	if cacheID != "" && result != "" {
		fileCache.SetText(cacheID, lang, mode, result)
		log.Printf("[text-cache] stored: %s (%d bytes)", cacheID[:min(50, len(cacheID))], len(result))
	}

	writeJSON(w, http.StatusOK, ExtractResponse{Success: true, Text: result})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func downloadFile(fileURL string, dir string) (string, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	filename := filepath.Base(resp.Request.URL.Path)
	if filename == "" || filename == "." || filename == "/" {
		ct := resp.Header.Get("Content-Type")
		switch {
		case strings.Contains(ct, "pdf"):
			filename = "download.pdf"
		case strings.Contains(ct, "png"):
			filename = "download.png"
		case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"):
			filename = "download.jpg"
		case strings.Contains(ct, "tiff"):
			filename = "download.tiff"
		case strings.Contains(ct, "webp"):
			filename = "download.webp"
		default:
			filename = "download.pdf"
		}
	}

	out, err := os.Create(filepath.Join(dir, filename))
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}

	return filename, nil
}

func extractPDF(path string, lang string, mode string) (string, error) {
	if mode == "text" {
		return extractPDFText(path)
	}
	if mode == "auto" {
		text, err := extractPDFText(path)
		if err == nil && len(text) > 100 {
			return text, nil
		}
	}
	return extractPDFOCR(path, lang)
}

func runCmd(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%s timeout (%s)", name, timeout)
	}
	return out, err
}

func extractPDFText(path string) (string, error) {
	outFile := path + ".txt"
	if _, err := runCmd(60*time.Second, "pdftotext", "-layout", path, outFile); err != nil {
		return "", fmt.Errorf("pdftotext failed: %w", err)
	}
	data, err := os.ReadFile(outFile)
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(data))
	if len(text) == 0 {
		return "", fmt.Errorf("pdftotext returned empty")
	}
	return text, nil
}

type pageResult struct {
	index int
	text  string
}

func extractPDFOCR(path string, lang string) (string, error) {
	imgPrefix := filepath.Join(filepath.Dir(path), "page")
	if _, err := runCmd(60*time.Second, "pdftoppm", "-png", "-r", "300", path, imgPrefix); err != nil {
		return "", fmt.Errorf("pdf conversion failed: %w (is poppler-utils installed?)", err)
	}

	matches, _ := filepath.Glob(imgPrefix + "-*.png")
	if len(matches) == 0 {
		matches, _ = filepath.Glob(imgPrefix + "*.png")
	}
	sort.Strings(matches) // sayfa sırasını koru

	// Paralel OCR — engine'e göre eşzamanlılık ayarla
	// PaddleOCR kendi içinde multi-thread, fazla paralel sayfa CPU'yu boğar
	// Tesseract single-thread, çekirdek sayısı kadar paralel güvenli
	maxParallel := runtime.NumCPU()
	if ocrEngine == "paddle" {
		maxParallel = max(1, runtime.NumCPU()/ocrThreads)
	}
	if maxParallel > len(matches) {
		maxParallel = len(matches)
	}
	ocrSem := make(chan struct{}, maxParallel)

	results := make([]pageResult, len(matches))
	var wg sync.WaitGroup
	var cachedCount, ocrdCount int64

	for i, img := range matches {
		wg.Add(1)
		go func(idx int, imgPath string) {
			defer wg.Done()

			// Sayfa image hash'i ile page-level cache kontrolu
			pageHash := fileHash(imgPath)
			if pageHash != "" {
				if text, ok := fileCache.GetText(pageHash, lang, "page"); ok {
					results[idx] = pageResult{index: idx, text: text}
					atomic.AddInt64(&cachedCount, 1)
					return
				}
			}

			// Tesseract için slot al
			ocrSem <- struct{}{}
			defer func() { <-ocrSem }()

			text, err := extractImage(imgPath, lang)
			if err != nil {
				return
			}
			results[idx] = pageResult{index: idx, text: text}
			atomic.AddInt64(&ocrdCount, 1)

			// Sayfa sonucunu cache'le
			if pageHash != "" {
				fileCache.SetText(pageHash, lang, "page", text)
			}
		}(i, img)
	}
	wg.Wait()

	// Sonuçları sıralı birleştir
	var parts []string
	for _, r := range results {
		if r.text != "" {
			parts = append(parts, r.text)
		}
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("no text extracted from PDF")
	}

	cached := atomic.LoadInt64(&cachedCount)
	ocrd := atomic.LoadInt64(&ocrdCount)
	log.Printf("[ocr] %d pages total, %d from cache, %d OCR'd (%d parallel)", len(matches), cached, ocrd, maxParallel)

	return strings.Join(parts, "\n\n--- page break ---\n\n"), nil
}

func extractImage(path string, lang string) (string, error) {
	if ocrEngine == "paddle" {
		return extractImagePaddle(path, lang)
	}
	return extractImageTesseract(path, lang)
}

func extractImagePaddle(path string, lang string) (string, error) {
	out, err := runCmd(120*time.Second, "python3", wrapperPath, path, lang, fmt.Sprintf("%d", ocrThreads))
	if err != nil {
		return "", fmt.Errorf("paddleocr failed: %w (is paddleocr installed?)", err)
	}
	return string(out), nil
}

func extractImageTesseract(path string, lang string) (string, error) {
	out, err := runCmd(60*time.Second, "tesseract", path, "stdout", "-l", lang)
	if err != nil {
		return "", fmt.Errorf("tesseract failed: %w (is tesseract-ocr installed?)", err)
	}
	return string(out), nil
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
