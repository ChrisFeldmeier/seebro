#!/bin/bash
set -e

echo "🪼 Seebro Auto-Start Uninstaller"
echo "==================================="

# Detect OS
if [[ "$OSTYPE" == "darwin"* ]]; then
    # macOS
    echo "Detected macOS - removing launchd configuration"
    
    PLIST="$HOME/Library/LaunchAgents/com.seebro.bridge.plist"
    
    # Unload if running
    echo "Stopping Seebro..."
    launchctl unload "$PLIST" 2>/dev/null || true
    
    # Remove plist
    if [ -f "$PLIST" ]; then
        echo "Removing LaunchAgent..."
        rm "$PLIST"
    fi
    
    # Optionally remove binary
    read -p "Remove /usr/local/bin/seebro? (y/N): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        sudo rm -f /usr/local/bin/seebro
    fi
    
elif [[ "$OSTYPE" == "linux-gnu"* ]]; then
    # Linux
    echo "Detected Linux - removing systemd configuration"
    
    # Stop and disable service
    echo "Stopping Seebro..."
    sudo systemctl stop "seebro@$USER" 2>/dev/null || true
    sudo systemctl disable "seebro@$USER" 2>/dev/null || true
    
    # Remove service file
    if [ -f "/etc/systemd/system/seebro@.service" ]; then
        echo "Removing systemd service..."
        sudo rm "/etc/systemd/system/seebro@.service"
        sudo systemctl daemon-reload
    fi
    
    # Optionally remove binary
    read -p "Remove /usr/local/bin/seebro? (y/N): " -n 1 -r
    echo
    if [[ $REPLY =~ ^[Yy]$ ]]; then
        sudo rm -f /usr/local/bin/seebro
    fi
    
else
    echo "❌ Unsupported OS: $OSTYPE"
    exit 1
fi

echo ""
echo "✅ Seebro auto-start has been removed!"