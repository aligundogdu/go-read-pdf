# pdf-read-service

PDF ve görsel dosyalardan metin çıkaran basit bir Go HTTP servisi. n8n veya herhangi bir HTTP client ile kullanılmak üzere tasarlanmıştır.

## Hızlı Kurulum

```bash
git clone <repo-url> && cd pdf-read-service
chmod +x install.sh
./install.sh
./pdf-read-service -port 8090
```

`install.sh` scripti OS'u otomatik tespit eder, gerekli paketleri (`poppler-utils`, `tesseract-ocr`) kurar ve Go binary'sini derler.

## Manuel Kurulum

### Gereksinimler

- **Go 1.21+**
- **poppler-utils** — PDF metin çıkarma (`pdftotext`, `pdftoppm`)
- **tesseract-ocr** — Görsel OCR

```bash
# Ubuntu/Debian
sudo apt install -y poppler-utils tesseract-ocr tesseract-ocr-tur tesseract-ocr-eng

# macOS
brew install poppler tesseract tesseract-lang
```

### Derleme ve Çalıştırma

```bash
go build -o pdf-read-service .
./pdf-read-service -port 8090
```

## API

### `GET /health`

Servis durumunu kontrol eder.

```bash
curl http://localhost:8090/health
```

```json
{"success": true, "text": "ok"}
```

### `POST /extract`

Dosya yükleyerek metin çıkarır. Multipart form-data olarak gönderilir.

**Parametreler:**

| Alan   | Tip    | Zorunlu | Açıklama                                      |
|--------|--------|---------|-----------------------------------------------|
| `file` | file   | Evet    | PDF veya görsel dosya                         |
| `lang` | string | Hayır   | Tesseract dil kodu (varsayılan: `eng`)        |

**Desteklenen formatlar:** `.pdf`, `.png`, `.jpg`, `.jpeg`, `.tiff`, `.tif`, `.bmp`, `.gif`, `.webp`

**Dil kodları:** `eng` (İngilizce), `tur` (Türkçe), `deu` (Almanca), `fra` (Fransızca), `tur+eng` (çoklu)

#### Örnek: PDF

```bash
curl -X POST http://localhost:8090/extract \
  -F "file=@belge.pdf" \
  -F "lang=tur"
```

#### Örnek: Görsel

```bash
curl -X POST http://localhost:8090/extract \
  -F "file=@screenshot.png" \
  -F "lang=tur+eng"
```

#### Başarılı Yanıt

```json
{
  "success": true,
  "text": "Dosyadan çıkarılan metin burada görünür..."
}
```

#### Hata Yanıtı

```json
{
  "success": false,
  "error": "tesseract failed: exit status 1 (is tesseract-ocr installed?)"
}
```

## n8n ile Kullanım

1. **HTTP Request** node ekleyin
2. Ayarlar:
   - **Method:** POST
   - **URL:** `http://localhost:8090/extract`
   - **Body Content Type:** Form-Data/Multipart
   - **Body Parameters:**
     - `file` → Binary data (PDF veya görsel)
     - `lang` → `tur` (veya ihtiyacınıza göre)
3. Yanıttaki `text` alanını sonraki node'larda kullanın

## Notlar

- PDF dosyaları önce `pdftotext` ile metin çıkarılır (hızlı). Eğer sonuç boşsa (taranmış PDF), sayfalar 300 DPI görüntüye dönüştürülüp Tesseract ile OCR yapılır.
- Tüm origin'lerden gelen istekler kabul edilir (CORS: `*`).
- Maksimum dosya boyutu: 50 MB.
