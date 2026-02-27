package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type ExtractRequest struct {
	Lang string `json:"lang"`
}

type ExtractResponse struct {
	Success bool   `json:"success"`
	Text    string `json:"text,omitempty"`
	Error   string `json:"error,omitempty"`
	Pages   int    `json:"pages,omitempty"`
}

func main() {
	port := flag.Int("port", 8090, "port to listen on")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/extract", handleExtract)
	mux.HandleFunc("/health", handleHealth)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("pdf-read-service starting on %s", addr)
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
	writeJSON(w, http.StatusOK, ExtractResponse{Success: true, Text: "ok"})
}

func handleExtract(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, ExtractResponse{Success: false, Error: "POST required"})
		return
	}

	lang := ""
	mode := "" // text, ocr, auto
	var tmpDir string
	var tmpFile string
	var filename string

	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "application/json") {
		// JSON body with URL
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

		var err error
		tmpDir, err = os.MkdirTemp("", "pdfread-")
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp dir error"})
			return
		}

		filename, err = downloadFile(req.URL, tmpDir)
		if err != nil {
			os.RemoveAll(tmpDir)
			writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "download failed: " + err.Error()})
			return
		}
		tmpFile = filepath.Join(tmpDir, filename)
	} else {
		// Multipart file upload
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
	}

	defer os.RemoveAll(tmpDir)

	if lang == "" {
		lang = "eng"
	}
	if mode == "" {
		mode = "ocr"
	}

	// Detect file type and extract
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

	writeJSON(w, http.StatusOK, ExtractResponse{Success: true, Text: strings.TrimSpace(text)})
}

func downloadFile(url string, dir string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Dosya adini URL'den al
	filename := filepath.Base(resp.Request.URL.Path)
	if filename == "" || filename == "." || filename == "/" {
		// Content-Type'dan uzanti tahmin et
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
	// mode=text: sadece pdftotext (hizli, metin tabanli PDF'ler icin)
	// mode=ocr:  her sayfayi gorsele cevirip OCR (varsayilan, karisik icerik icin)
	// mode=auto: once pdftotext dene, sonuc kisaysa OCR'a dus

	if mode == "text" {
		return extractPDFText(path)
	}

	if mode == "auto" {
		text, err := extractPDFText(path)
		if err == nil && len(text) > 100 {
			return text, nil
		}
	}

	// OCR: sayfalari gorsele cevirip tesseract ile oku
	return extractPDFOCR(path, lang)
}

func extractPDFText(path string) (string, error) {
	outFile := path + ".txt"
	cmd := exec.Command("pdftotext", "-layout", path, outFile)
	if err := cmd.Run(); err != nil {
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

func extractPDFOCR(path string, lang string) (string, error) {
	imgPrefix := filepath.Join(filepath.Dir(path), "page")
	cmd := exec.Command("pdftoppm", "-png", "-r", "300", path, imgPrefix)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdf conversion failed: %w (is poppler-utils installed?)", err)
	}

	matches, _ := filepath.Glob(imgPrefix + "-*.png")
	if len(matches) == 0 {
		matches, _ = filepath.Glob(imgPrefix + "*.png")
	}

	var parts []string
	for _, img := range matches {
		text, err := extractImage(img, lang)
		if err != nil {
			continue
		}
		parts = append(parts, text)
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("no text extracted from PDF")
	}

	return strings.Join(parts, "\n\n--- page break ---\n\n"), nil
}

func extractImage(path string, lang string) (string, error) {
	cmd := exec.Command("tesseract", path, "stdout", "-l", lang)
	out, err := cmd.Output()
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
