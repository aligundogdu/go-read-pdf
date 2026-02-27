#!/bin/bash
set -e

PORT="${1:-8090}"
INSTALL_DIR="/opt/pdf-read-service"
SERVICE_NAME="pdf-read-service"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Guncelleme mi, ilk kurulum mu?
IS_UPDATE=false
if systemctl is-active --quiet ${SERVICE_NAME} 2>/dev/null; then
    IS_UPDATE=true
fi
if [ -f "$HOME/Library/LaunchAgents/com.local.pdf-read-service.plist" ] 2>/dev/null; then
    IS_UPDATE=true
fi

if [ "$IS_UPDATE" = true ]; then
    echo "=== pdf-read-service guncelleme ==="
else
    echo "=== pdf-read-service ilk kurulum ==="
fi
echo "[*] Port: $PORT"

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
        sudo apt-get install -y poppler-utils tesseract-ocr tesseract-ocr-tur tesseract-ocr-eng
    elif [ -f /etc/redhat-release ]; then
        echo "[*] RHEL/CentOS tespit edildi, paketler kuruluyor..."
        sudo yum install -y poppler-utils tesseract tesseract-langpack-tur tesseract-langpack-eng
    elif command -v brew &>/dev/null; then
        echo "[*] macOS tespit edildi, paketler kuruluyor..."
        brew install poppler tesseract tesseract-lang
    else
        echo "[!] OS tespit edilemedi. poppler-utils ve tesseract-ocr manuel kurun."
        exit 1
    fi
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
echo "[*] ${INSTALL_DIR} altina kuruluyor..."
sudo mkdir -p "$INSTALL_DIR"
sudo cp pdf-read-service "$INSTALL_DIR/"

if [ "$IS_UPDATE" = false ]; then
    echo "[*] systemd servisi olusturuluyor..."
    sudo tee /etc/systemd/system/${SERVICE_NAME}.service > /dev/null <<EOF
[Unit]
Description=PDF/Image to Text Extraction Service
After=network.target

[Service]
Type=simple
ExecStart=${INSTALL_DIR}/pdf-read-service -port ${PORT}
Restart=always
RestartSec=5
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF
    sudo systemctl daemon-reload
    sudo systemctl enable ${SERVICE_NAME}
fi

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
