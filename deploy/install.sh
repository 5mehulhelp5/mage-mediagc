#!/bin/sh
# Install mage-mediagc from a local binary or from source.
#
# Usage:
#   ./deploy/install.sh --binary ./mage-mediagc
#   ./deploy/install.sh --source            # needs a Go toolchain
#   ./deploy/install.sh --binary ./mage-mediagc --prefix /opt/mage-mediagc
#
# The script installs the binary, a commented config file, and the optional
# systemd units. It never starts anything.
set -eu

PREFIX=/usr/local
BINDIR=""
MODE=""
BINARY=""
WITH_SYSTEMD=auto

usage() {
    sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'
    cat <<'EOF'

Options:
  --binary PATH    install this prebuilt binary
  --source         build from the current checkout with `go build`
  --prefix DIR     installation prefix (default /usr/local)
  --bindir DIR     where to put the binary (default <prefix>/bin)
  --no-systemd     do not install systemd units
  --help           show this help
EOF
}

while [ $# -gt 0 ]; do
    case "$1" in
        --binary) MODE=binary; BINARY="${2:-}"; shift 2 ;;
        --source) MODE=source; shift ;;
        --prefix) PREFIX="${2:-}"; shift 2 ;;
        --bindir) BINDIR="${2:-}"; shift 2 ;;
        --no-systemd) WITH_SYSTEMD=no; shift ;;
        -h|--help) usage; exit 0 ;;
        *) echo "unknown option: $1" >&2; exit 2 ;;
    esac
done

[ -n "$BINDIR" ] || BINDIR="$PREFIX/bin"

if [ "$(id -u)" -ne 0 ]; then
    echo "error: this script writes to $PREFIX and /etc, so it needs root" >&2
    exit 1
fi

case "$MODE" in
    binary)
        [ -n "$BINARY" ] || { echo "error: --binary needs a path" >&2; exit 2; }
        [ -x "$BINARY" ] || { echo "error: $BINARY is not executable" >&2; exit 2; }
        ;;
    source)
        command -v go >/dev/null 2>&1 || { echo "error: go is not installed" >&2; exit 1; }
        echo "building mage-mediagc from source..."
        CGO_ENABLED=0 go build -trimpath -o /tmp/mage-mediagc ./cmd/mage-mediagc
        BINARY=/tmp/mage-mediagc
        ;;
    *)
        echo "error: choose --binary or --source" >&2
        echo >&2
        usage >&2
        exit 2
        ;;
esac

echo "installing binary to $BINDIR/mage-mediagc"
install -d -m 0755 "$BINDIR"
install -m 0755 "$BINARY" "$BINDIR/mage-mediagc"

install -d -m 0755 /etc/mage-mediagc
install -d -m 0750 /var/lib/mage-mediagc
install -d -m 0755 /var/log/mage-mediagc

for f in mage-mediagc.yaml mage-mediagc.env; do
    src="examples/$f"
    dst="/etc/mage-mediagc/$f"
    if [ -f "$src" ]; then
        if [ -f "$dst" ]; then
            echo "keeping existing $dst"
        else
            install -m 0640 "$src" "$dst"
            echo "wrote $dst"
        fi
    fi
done

if [ "$WITH_SYSTEMD" = auto ] && ! command -v systemctl >/dev/null 2>&1; then
    WITH_SYSTEMD=no
fi

if [ "$WITH_SYSTEMD" = auto ]; then
    for unit in mage-mediagc-scan.service mage-mediagc-scan.timer \
                mage-mediagc-cache.service mage-mediagc-cache.timer; do
        if [ -f "deploy/systemd/$unit" ]; then
            install -m 0644 "deploy/systemd/$unit" "/etc/systemd/system/$unit"
            echo "installed /etc/systemd/system/$unit"
        fi
    done
    systemctl daemon-reload || true
fi

cat <<EOF

Done. Verify with:

  $BINDIR/mage-mediagc version
  $BINDIR/mage-mediagc config show --config /etc/mage-mediagc/mage-mediagc.yaml

Then set MAGEGC_MAGENTO_ROOT in /etc/mage-mediagc/mage-mediagc.env and run:

  $BINDIR/mage-mediagc scan --config /etc/mage-mediagc/mage-mediagc.yaml

EOF
