#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SEEBRO_BIN="$(cd "$SCRIPT_DIR/.." && pwd)/seebro"

echo "🪼 Seebro Auto-Start Installer"
echo "================================"

# Detect OS
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    echo "Detected macOS - using launchd"
    
    # Build if needed
    if [ ! -f "$SEEBRO_BIN" ]; then
        echo "Building seebro..."
        (cd "$SCRIPT_DIR/.." && go build -o seebro .)
    fi
    
    # Copy binary to /usr/local/bin
    echo "Installing seebro to /usr/local/bin..."
    sudo cp "$SEEBRO_BIN" /usr/local/bin/seebro
    
    # Install LaunchAgent
    PLIST_SRC="$SCRIPT_DIR/launchd/com.seebro.bridge.plist"
    PLIST_DST="$HOME/Library/LaunchAgents/com.seebro.bridge.plist"
    
    echo "Installing LaunchAgent..."
    mkdir -p "$HOME/Library/LaunchAgents"
    cp "$PLIST_SRC" "$PLIST_DST"
    
    # Load the agent
    echo "Loading LaunchAgent..."
    launchctl load -w "$PLIST_DST" 2>/dev/null || true
    
    echo ""
    echo "✅ Seebro installed and set to auto-start!"
    echo ""
    echo "Commands:"
    echo "  Start:   launchctl start com.seebro.bridge"
    echo "  Stop:    launchctl stop com.seebro.bridge"
    echo "  Status:  launchctl list | grep seebro"
    echo "  Logs:    tail -f /tmp/seebro.*.log"
    echo "  Disable: launchctl unload ~/Library/LaunchAgents/com.seebro.bridge.plist"
    
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    # Linux
    echo "Detected Linux - using systemd"
    
    # Build if needed
    if [ ! -f "$SEEBRO_BIN" ]; then
        echo "Building seebro..."
        (cd "$SCRIPT_DIR/.." && go build -o seebro .)
    fi
    
    # Copy binary to /usr/local/bin
    echo "Installing seebro to /usr/local/bin..."
    sudo cp "$SEEBRO_BIN" /usr/local/bin/seebro
    
    # Install systemd service
    SERVICE_SRC="$SCRIPT_DIR/systemd/seebro.service"
    SERVICE_DST="/etc/systemd/system/seebro@.service"
    
    echo "Installing systemd service..."
    sudo cp "$SERVICE_SRC" "$SERVICE_DST"
    
    # Enable and start service
    echo "Enabling service for user $USER..."
    sudo systemctl daemon-reload
    sudo systemctl enable "seebro@$USER.service"
    sudo systemctl start "seebro@$USER.service"
    
    echo ""
    echo "✅ Seebro installed and set to auto-start!"
    echo ""
    echo "Commands:"
    echo "  Start:   sudo systemctl start seebro@$USER"
    echo "  Stop:    sudo systemctl stop seebro@$USER"
    echo "  Status:  sudo systemctl status seebro@$USER"
    echo "  Logs:    sudo journalctl -u seebro@$USER -f"
    echo "  Disable: sudo systemctl disable seebro@$USER"
    
else
    echo "❌ Unsupported OS: $OSTYPE"
    echo "Please manually configure auto-start for your system."
    exit 1
fi

echo ""
echo "Test with: curl http://localhost:9867/health"