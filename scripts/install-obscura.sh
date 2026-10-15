#!/usr/bin/env bash
# Install the obscura engine (https://github.com/h4ckf0r0day/obscura, Apache-2.0)
# into ~/.seebro/bin, where Seebro finds it automatically.
#
#   scripts/install-obscura.sh                 # download the release for this OS/arch
#   scripts/install-obscura.sh --from-source   # build from source (needs Rust; ~10 min)
#
# OBSCURA_VERSION selects the release (default below), SEEBRO_BIN the target dir.
set -euo pipefail

VERSION="${OBSCURA_VERSION:-v0.2.4}"
DEST="${SEEBRO_BIN:-$HOME/.seebro/bin}"
REPO="h4ckf0r0day/obscura"
mkdir -p "$DEST"

if [[ "${1:-}" == "--from-source" ]]; then
  work="$(mktemp -d)"
  git clone --depth 1 --branch "$VERSION" "https://github.com/$REPO.git" "$work/obscura"
  (cd "$work/obscura" && cargo build --release -p obscura-cli --features stealth,render)
  cp "$work/obscura/target/release/obscura" "$work/obscura/target/release/obscura-worker" "$DEST/"
  rm -rf "$work"
else
  case "$(uname -s)" in
    Darwin) os=macos ;;
    Linux) os=linux ;;
    MINGW*|MSYS*|CYGWIN*) os=windows ;;
    *) echo "unsupported OS $(uname -s); use --from-source" >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) arch=aarch64 ;;
    x86_64|amd64) arch=x86_64 ;;
    *) echo "unsupported arch $(uname -m); use --from-source" >&2; exit 1 ;;
  esac
  ext=tar.gz; [[ $os == windows ]] && ext=zip
  asset="obscura-$arch-$os-stealth.$ext"
  url="https://github.com/$REPO/releases/download/$VERSION/$asset"
  work="$(mktemp -d)"
  echo "downloading $url"
  curl -fL --progress-bar -o "$work/$asset" "$url"
  echo "sha256: $(shasum -a 256 "$work/$asset" | cut -d' ' -f1)  (compare with the release page if you want to verify)"
  if [[ $ext == zip ]]; then unzip -q "$work/$asset" -d "$work/x"; else mkdir -p "$work/x" && tar -xzf "$work/$asset" -C "$work/x"; fi
  find "$work/x" -type f \( -name 'obscura' -o -name 'obscura-worker' -o -name 'obscura.exe' -o -name 'obscura-worker.exe' \) -exec cp {} "$DEST/" \;
  rm -rf "$work"
fi

chmod +x "$DEST"/obscura* 2>/dev/null || true
"$DEST/obscura" --version
echo "installed into $DEST — use it with BRIDGE_ENGINE=obscura, or open tabs with {\"engine\":\"obscura\"}"
