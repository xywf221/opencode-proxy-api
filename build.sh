#!/usr/bin/env sh
# opencode-proxy-api build script
set -eu

ROOT="$(CDPATH= cd -- "$(dirname "$0")" && pwd)"
cd "$ROOT"

usage() {
  cat <<'EOF'
Usage: ./build.sh [options] [-- go build args...]

Builds the opencode-proxy binary (main package at ./cmd/server).

Options:
  --os GOOS        Target OS. Defaults to current go env GOOS.
  --arch GOARCH    Target architecture. Defaults to current go env GOARCH.
  --cgo 0|1        CGO_ENABLED. Defaults to 0.
  --out DIR        Output directory. Defaults to dist.
  --name NAME      Binary base name. Defaults to opencode-proxy.
  --pkg PACKAGE    Package to build. Defaults to ./cmd/server.
  --ldflags VALUE  Extra value appended to release ldflags.
  --local          Also write/replace ./opencode-proxy[.exe] in project root
                   (if locked, writes opencode-proxy_new[.exe] instead).
  -h, --help       Show this help.

Examples:
  ./build.sh
  ./build.sh --local
  ./build.sh --os linux --arch amd64
  ./build.sh --os windows --arch amd64 --local
  ./build.sh --os linux --arch arm64 --out release
EOF
}

target_os="$(go env GOOS 2>/dev/null || echo linux)"
target_arch="$(go env GOARCH 2>/dev/null || echo amd64)"
cgo_enabled="0"
out_dir="dist"
name="opencode-proxy"
pkg="./cmd/server"
ldflags="-s -w -buildid="
also_local=0

while [ "$#" -gt 0 ]; do
  case "$1" in
    --os)
      target_os="$2"
      shift 2
      ;;
    --arch)
      target_arch="$2"
      shift 2
      ;;
    --cgo)
      cgo_enabled="$2"
      shift 2
      ;;
    --out)
      out_dir="$2"
      shift 2
      ;;
    --name)
      name="$2"
      shift 2
      ;;
    --pkg)
      pkg="$2"
      shift 2
      ;;
    --ldflags)
      ldflags="$ldflags $2"
      shift 2
      ;;
    --local)
      also_local=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      break
      ;;
    *)
      echo "unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "go not found" >&2
  exit 1
fi

mkdir -p "$out_dir"

suffix=""
if [ "$target_os" = "windows" ]; then
  suffix=".exe"
fi
output="$out_dir/$name-$target_os-$target_arch$suffix"

echo "==> building go: GOOS=$target_os GOARCH=$target_arch CGO_ENABLED=$cgo_enabled"
echo "go build -trimpath -buildvcs=false -ldflags \"$ldflags\" -o $output $* $pkg"
GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED="$cgo_enabled" \
  go build -trimpath -buildvcs=false -ldflags "$ldflags" -o "$output" "$@" "$pkg"
echo "built: $output"

# optional sample files into dist (never overwrite real secrets)
copy_if() {
  src="$1"
  dst="$2"
  if [ -f "$src" ] && [ ! -f "$dst" ]; then
    cp -f "$src" "$dst"
    echo "copied: $dst"
  fi
}
copy_if ".env.example" "$out_dir/.env.example"
copy_if "config.example.json" "$out_dir/config.example.json"

# also install to project root for local use
if [ "$also_local" -eq 1 ]; then
  host_os="$(go env GOOS)"
  host_arch="$(go env GOARCH)"
  if [ "$target_os" = "$host_os" ] && [ "$target_arch" = "$host_arch" ]; then
    local_bin="./$name$suffix"
    local_new="./${name}_new$suffix"
    if cp -f "$output" "$local_bin" 2>/dev/null; then
      echo "local: $local_bin"
    else
      cp -f "$output" "$local_new"
      echo "local locked, wrote: $local_new"
      echo "  stop the running server, then: mv $local_new $local_bin"
    fi
  else
    echo "skip --local: cross-compile $target_os/$target_arch != host $host_os/$host_arch"
  fi
fi

echo
echo "done."
echo "artifact: $output"
echo "run examples:"
echo "  $output"
echo "  OPCODE_LISTEN=:8080 OPCODE_PROXY=socks5://127.0.0.1:1080 $output"
