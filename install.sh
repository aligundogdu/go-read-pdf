#!/bin/bash
set -e

PORT="${1:-8090}"
INSTALL_DIR="/opt/pdf-read-service"
SERVICE_NAME="pdf-read-service"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "=== pdf-read-service kurulum ==="
echo "[*] Port: $PORT"

# OS tespiti ve paket kurulumu
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
    echo ""
    echo "=== macOS'ta systemd yok, launchd plist olusturuluyor ==="

    # Go kontrolu
    if ! command -v go &>/dev/null; then
        echo "[!] Go bulunamadi. https://go.dev/dl/ adresinden kurun."
        exit 1
    fi

    echo "[*] Derleniyor..."
    go build -o pdf-read-service .

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
    echo "=== Kurulum tamam ==="
    echo "Servis calisiyor: http://localhost:${PORT}/health"
    echo "Log: /tmp/pdf-read-service.log"
    echo ""
    echo "Yonetim:"
    echo "  launchctl unload ${PLIST_PATH}   # durdur"
    echo "  launchctl load ${PLIST_PATH}     # baslat"
    exit 0
else
    echo "[!] OS tespit edilemedi. poppler-utils ve tesseract-ocr manuel kurun."
    exit 1
fi

# Go kontrolu
if ! command -v go &>/dev/null; then
    echo "[!] Go bulunamadi. https://go.dev/dl/ adresinden kurun."
    exit 1
fi

echo "[*] Derleniyor..."
go build -o pdf-read-service .

# Binary'yi /opt altina kopyala
echo "[*] ${INSTALL_DIR} altina kuruluyor..."
sudo mkdir -p "$INSTALL_DIR"
sudo cp pdf-read-service "$INSTALL_DIR/"

# systemd servis dosyasi olustur
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
sudo systemctl restart ${SERVICE_NAME}

echo ""
echo "=== Kurulum tamam ==="
echo "Servis calisiyor: http://localhost:${PORT}/health"
echo "Log: sudo journalctl -u ${SERVICE_NAME} -f"
echo ""
echo "Yonetim:"
echo "  sudo systemctl status ${SERVICE_NAME}"
echo "  sudo systemctl stop ${SERVICE_NAME}"
echo "  sudo systemctl start ${SERVICE_NAME}"
echo "  sudo systemctl restart ${SERVICE_NAME}"
