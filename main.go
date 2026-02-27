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

	// Parse multipart form (max 50MB)
	if err := r.ParseMultipartForm(50 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "invalid multipart form: " + err.Error()})
		return
	}

	// Get language parameter (default: eng)
	lang := r.FormValue("lang")
	if lang == "" {
		lang = "eng"
	}

	// Get uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ExtractResponse{Success: false, Error: "file field required"})
		return
	}
	defer file.Close()

	// Create temp file
	tmpDir, err := os.MkdirTemp("", "pdfread-")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp dir error"})
		return
	}
	defer os.RemoveAll(tmpDir)

	tmpFile := filepath.Join(tmpDir, header.Filename)
	out, err := os.Create(tmpFile)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "temp file error"})
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		writeJSON(w, http.StatusInternalServerError, ExtractResponse{Success: false, Error: "file write error"})
		return
	}
	out.Close()

	// Detect file type and extract
	ext := strings.ToLower(filepath.Ext(header.Filename))
	var text string

	switch ext {
	case ".pdf":
		text, err = extractPDF(tmpFile, lang)
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

func extractPDF(path string, lang string) (string, error) {
	// First try pdftotext (fast, works for text-based PDFs)
	outFile := path + ".txt"
	cmd := exec.Command("pdftotext", "-layout", path, outFile)
	if err := cmd.Run(); err == nil {
		data, err := os.ReadFile(outFile)
		if err == nil {
			text := strings.TrimSpace(string(data))
			if len(text) > 0 {
				return text, nil
			}
		}
	}

	// Fallback: scanned PDF -> convert pages to images, then OCR
	// Convert PDF to images using pdftoppm
	imgPrefix := filepath.Join(filepath.Dir(path), "page")
	cmd = exec.Command("pdftoppm", "-png", "-r", "300", path, imgPrefix)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdf conversion failed: %w (is poppler-utils installed?)", err)
	}

	// OCR each page image
	matches, _ := filepath.Glob(imgPrefix + "-*.png")
	if len(matches) == 0 {
		// pdftoppm might produce single page without number
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
