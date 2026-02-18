# Docker Deployment

Seebro includes a Docker image with bundled Chromium for easy deployment.

## Quick Start

### Using Docker Compose

```bash
# Clone the repository
git clone https://github.com/ChrisFeldmeier/seebro.git
cd seebro

# Start with docker-compose
docker-compose up -d

# Check logs
docker-compose logs -f

# Test
curl http://localhost:9867/health
```

### Using Docker CLI

```bash
# Build image
docker build -t seebro .

# Run container
docker run -d \
  --name seebro \
  -p 9867:9867 \
  -v seebro-data:/data \
  --security-opt seccomp=unconfined \
  seebro

# With auth token
docker run -d \
  --name seebro \
  -p 9867:9867 \
  -v seebro-data:/data \
  -e BRIDGE_TOKEN=your-secret-token \
  --security-opt seccomp=unconfined \
  seebro
```

## Configuration

Environment variables:
- `BRIDGE_PORT` - HTTP port (default: 9867)
- `BRIDGE_TOKEN` - Auth token (optional)
- `BRIDGE_HEADLESS` - Run Chrome headless (default: true in Docker)
- `BRIDGE_STATE_DIR` - State directory (default: /data)
- `BRIDGE_STEALTH` - Stealth level: `light` (default) or `full`
- `BRIDGE_BLOCK_IMAGES` - Block image loading (default: false)
- `BRIDGE_BLOCK_MEDIA` - Block all media (default: false)
- `BRIDGE_NO_ANIMATIONS` - Disable CSS animations (default: false)
- `CHROME_BINARY` - Set automatically in Docker (`/usr/bin/chromium-browser`)
- `CHROME_FLAGS` - Set automatically in Docker (`--no-sandbox --disable-gpu`)

## Architecture

The Docker image:
- Uses Alpine Linux for minimal size
- Includes Chromium browser
- Runs as non-root user
- Uses dumb-init for proper signal handling
- Persists state in `/data` volume

## Security Notes

The container requires `--security-opt seccomp=unconfined` for Chrome to function properly. This is a limitation of running Chrome in containers.

For production use:
1. Always set `BRIDGE_TOKEN`
2. Use a reverse proxy with TLS
3. Limit container resources
4. Run on isolated networks

## Troubleshooting

### Chrome crashes
Increase memory limit:
```yaml
mem_limit: 4g
```

### Font issues
The image includes basic fonts. For specific fonts:
```dockerfile
RUN apk add --no-cache ttf-liberation ttf-dejavu
```

### Performance
For better performance:
```yaml
shm_size: '2gb'  # Shared memory for Chrome
```