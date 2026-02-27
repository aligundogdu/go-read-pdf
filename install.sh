#!/bin/bash
set -e

INSTALL_DIR="/opt/pdf-read-service"
SERVICE_NAME="pdf-read-service"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

# Guncelleme mi, ilk kurulum mu?
IS_UPDATE=false
if systemctl is-active --quiet ${SERVICE_NAME} 2>/dev/null; then
    IS_UPDATE=true
fi
if [ -f "$HOME/Library/LaunchAgents/com.local.pdf-read-service.plist" ] 2>/dev/null; then
    IS_UPDATE=true
fi

# Mevcut service dosyasindan parametreleri oku (guncelleme icin varsayilan)
_existing_param() {
    # ExecStart satirindan -flag value ciftini parse et
    if [ -f "$SERVICE_FILE" ]; then
        grep -oP "(?<=$1 )\S+" "$SERVICE_FILE" 2>/dev/null || echo ""
    fi
}

if [ "$IS_UPDATE" = true ] && [ -f "$SERVICE_FILE" ]; then
    DEF_PORT=$(_existing_param "-port")
    DEF_WORKERS=$(_existing_param "-workers")
    DEF_CACHE_TTL=$(_existing_param "-cache-ttl")
    DEF_FILE_CACHE_DIR=$(_existing_param "-file-cache-dir")
    DEF_FILE_CACHE_MAX=$(_existing_param "-file-cache-max")
    DEF_OCR_ENGINE=$(_existing_param "-ocr-engine")
    DEF_OCR_THREADS=$(_existing_param "-ocr-threads")
fi

PORT="${1:-${DEF_PORT:-8090}}"
WORKERS="${2:-${DEF_WORKERS:-2}}"
CACHE_TTL="${3:-${DEF_CACHE_TTL:-2880}}"
FILE_CACHE_DIR="${4:-${DEF_FILE_CACHE_DIR:-/tmp/pdfread-cache}}"
FILE_CACHE_MAX="${5:-${DEF_FILE_CACHE_MAX:-100}}"
OCR_ENGINE="${6:-${DEF_OCR_ENGINE:-paddle}}"
OCR_THREADS="${7:-${DEF_OCR_THREADS:-4}}"

if [ "$IS_UPDATE" = true ]; then
    echo "=== pdf-read-service guncelleme ==="
else
    echo "=== pdf-read-service ilk kurulum ==="
fi
echo "[*] Port: $PORT, Workers: $WORKERS, Cache TTL: ${CACHE_TTL}m, File Cache: ${FILE_CACHE_DIR} (max ${FILE_CACHE_MAX}), OCR: ${OCR_ENGINE} (threads: ${OCR_THREADS})"

# git pull (eger git reposu icindeyse)
if [ -d "${SCRIPT_DIR}/.git" ]; then
    echo "[*] git pull yapiliyor..."
    git -C "$SCRIPT_DIR" pull
fi

# Ilk kurulumda paketleri kur, guncellemede atla
if [ "$IS_UPDATE" = false ]; then
    if [ -f /etc/debian_version ]; then
        echo "[*] Debian/Ubuntu tespit edildi, paketler kuruluyor..."
        sudo apt-get update -qq
        sudo apt-get install -y poppler-utils python3 python3-pip
        # Tesseract (fallback)
        sudo apt-get install -y tesseract-ocr tesseract-ocr-tur tesseract-ocr-eng
    elif [ -f /etc/redhat-release ]; then
        echo "[*] RHEL/CentOS tespit edildi, paketler kuruluyor..."
        sudo yum install -y poppler-utils python3 python3-pip
        sudo yum install -y tesseract tesseract-langpack-tur tesseract-langpack-eng
    elif command -v brew &>/dev/null; then
        echo "[*] macOS tespit edildi, paketler kuruluyor..."
        brew install poppler python3
        brew install tesseract tesseract-lang
    else
        echo "[!] OS tespit edilemedi. poppler-utils ve python3 manuel kurun."
        exit 1
    fi

    # EasyOCR kurulumu (venv icinde)
    echo "[*] EasyOCR kuruluyor (venv)..."
    VENV_DIR="${INSTALL_DIR}/venv"
    if [ -f /etc/debian_version ]; then
        sudo apt-get install -y python3-venv python3-full
    fi
    sudo mkdir -p "$INSTALL_DIR"
    sudo python3 -m venv "$VENV_DIR"
    sudo "$VENV_DIR/bin/pip" install --upgrade pip
    sudo "$VENV_DIR/bin/pip" install easyocr
fi

# Go kontrolu
if ! command -v go &>/dev/null; then
    echo "[!] Go bulunamadi. https://go.dev/dl/ adresinden kurun."
    exit 1
fi

echo "[*] Derleniyor..."
cd "$SCRIPT_DIR"
go build -o pdf-read-service .

# --- macOS ---
if command -v brew &>/dev/null && [ "$(uname)" = "Darwin" ]; then
    PLIST_PATH="$HOME/Library/LaunchAgents/com.local.pdf-read-service.plist"
    cat > "$PLIST_PATH" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.local.pdf-read-service</string>
    <key>ProgramArguments</key>
    <array>
        <string>${SCRIPT_DIR}/pdf-read-service</string>
        <string>-port</string>
        <string>${PORT}</string>
        <string>-workers</string>
        <string>${WORKERS}</string>
        <string>-cache-ttl</string>
        <string>${CACHE_TTL}</string>
        <string>-file-cache-dir</string>
        <string>${FILE_CACHE_DIR}</string>
        <string>-file-cache-max</string>
        <string>${FILE_CACHE_MAX}</string>
        <string>-ocr-engine</string>
        <string>${OCR_ENGINE}</string>
        <string>-ocr-threads</string>
        <string>${OCR_THREADS}</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/tmp/pdf-read-service.log</string>
    <key>StandardErrorPath</key>
    <string>/tmp/pdf-read-service.log</string>
</dict>
</plist>
PLIST

    launchctl unload "$PLIST_PATH" 2>/dev/null || true
    launchctl load "$PLIST_PATH"

    echo ""
    if [ "$IS_UPDATE" = true ]; then
        echo "=== Guncelleme tamam ==="
    else
        echo "=== Kurulum tamam ==="
    fi
    echo "Servis calisiyor: http://localhost:${PORT}/health"
    echo "Log: /tmp/pdf-read-service.log"
    exit 0
fi

# --- Linux (systemd) ---
if [ "$IS_UPDATE" = true ]; then
    echo "[*] Servis durduruluyor..."
    sudo systemctl stop ${SERVICE_NAME}
fi

echo "[*] ${INSTALL_DIR} altina kuruluyor..."
sudo mkdir -p "$INSTALL_DIR"
sudo cp pdf-read-service "$INSTALL_DIR/"
sudo cp paddleocr_wrapper.py "$INSTALL_DIR/"

    echo "[*] systemd servisi olusturuluyor..."
    sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<EOF
[Unit]
Description=PDF/Image to Text Extraction Service
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/pdf-read-service -port ${PORT} -workers ${WORKERS} -cache-ttl ${CACHE_TTL} -file-cache-dir ${FILE_CACHE_DIR} -file-cache-max ${FILE_CACHE_MAX} -ocr-engine ${OCR_ENGINE} -ocr-threads ${OCR_THREADS}
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
    sudo systemctl daemon-reload
    sudo systemctl enable ${SERVICE_NAME}

sudo systemctl restart ${SERVICE_NAME}

echo ""
if [ "$IS_UPDATE" = true ]; then
    echo "=== Guncelleme tamam ==="
else
    echo "=== Kurulum tamam ==="
fi
echo "Servis calisiyor: http://localhost:${PORT}/health"
echo "Log: sudo journalctl -u ${SERVICE_NAME} -f"
echo ""
echo "Yonetim:"
echo "  sudo systemctl status ${SERVICE_NAME}"
echo "  sudo systemctl stop ${SERVICE_NAME}"
echo "  sudo systemctl start ${SERVICE_NAME}"
echo "  sudo systemctl restart ${SERVICE_NAME}"
