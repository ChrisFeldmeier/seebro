# Seebro Scripts

## Auto-Start Setup

### Quick Install

```bash
./scripts/install-autostart.sh
```

This will:
- Build and install seebro to `/usr/local/bin`
- Configure auto-start on boot
- Start the service immediately

### macOS (launchd)

The installer creates a LaunchAgent that:
- Runs seebro on login
- Restarts automatically if it crashes
- Logs to `/tmp/seebro.*.log`

Manual control:
```bash
# Start/stop
launchctl start com.seebro.bridge
launchctl stop com.seebro.bridge

# Check status
launchctl list | grep seebro

# View logs
tail -f /tmp/seebro.*.log
```

### Linux (systemd)

The installer creates a systemd user service that:
- Runs seebro on boot
- Restarts automatically if it crashes
- Logs to systemd journal

Manual control:
```bash
# Start/stop
sudo systemctl start seebro@$USER
sudo systemctl stop seebro@$USER

# Check status
sudo systemctl status seebro@$USER

# View logs
sudo journalctl -u seebro@$USER -f
```

### Uninstall

```bash
./scripts/uninstall-autostart.sh
```

### Custom Configuration

Edit environment variables in:
- **macOS**: `~/Library/LaunchAgents/com.seebro.bridge.plist`
- **Linux**: `/etc/systemd/system/seebro@.service`

Common environment variables:
- `BRIDGE_PORT` - HTTP port (default: 9867)
- `BRIDGE_TOKEN` - Auth token (optional)
- `BRIDGE_HEADLESS` - Run Chrome headless (default: true)
- `BRIDGE_PROFILE` - Chrome profile directory

## Other Scripts

- `check.sh` - Run all pre-push checks (format, vet, build, test)