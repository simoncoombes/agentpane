#!/bin/sh
# Fixture-driven test for get.sh.
#
# A fake release is built in a temp directory and served over file:// URLs, so
# the download, the checksum verification and the install are exercised without
# a network, a GitHub account, or a published release. Run with:
#   /bin/sh tests/get_test.sh

set -eu

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
GET="$REPO_DIR/get.sh"
[ -f "$GET" ] || { echo "FAIL: $GET not found" >&2; exit 1; }

ROOT="$(mktemp -d "${TMPDIR:-/tmp}/agentpane-get.XXXXXX")"
trap 'rm -rf "$ROOT"' EXIT

PASS=0
FAIL=0
ok()   { PASS=$((PASS+1)); echo "PASS: $*"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL: $*"; }

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

# --- build a fake release ----------------------------------------------------

os="$(uname -s)"; case "$os" in Darwin) os=darwin ;; Linux) os=linux ;; esac
arch="$(uname -m)"; case "$arch" in arm64|aarch64) arch=arm64 ;; x86_64|amd64) arch=amd64 ;; esac

REL="$ROOT/release"
mkdir -p "$REL" "$ROOT/build"
cat > "$ROOT/build/agentpane" <<'BIN'
#!/bin/sh
[ "$1" = "version" ] && echo "v9.9.9-fixture"
exit 0
BIN
chmod +x "$ROOT/build/agentpane"
ARCHIVE="agentpane_v9.9.9_${os}_${arch}.tar.gz"
tar -czf "$REL/$ARCHIVE" -C "$ROOT/build" agentpane
( cd "$REL" && echo "$(sha256 "$ARCHIVE")  $ARCHIVE" > checksums.txt )

run_get() {
  AGENTPANE_VERSION=v9.9.9 \
  AGENTPANE_BASE_URL="file://$REL" \
  AGENTPANE_BIN_DIR="$1" \
  /bin/sh "$GET" 2>&1
}

# --- 1 happy path ------------------------------------------------------------

BIN_DIR="$ROOT/bin1"
if out="$(run_get "$BIN_DIR")"; then
  if [ -x "$BIN_DIR/agentpane" ]; then
    ok "1 installs the binary and marks it executable"
  else
    bad "1 no executable at $BIN_DIR/agentpane"
  fi
  case "$out" in
    *"agentpane install"*) ok "2 points at the hook installer as the next step" ;;
    *) bad "2 output never mentions 'agentpane install': $out" ;;
  esac
  case "$out" in
    *v9.9.9-fixture*) ok "3 reports the installed version by running it" ;;
    *) bad "3 output does not report the version: $out" ;;
  esac
else
  bad "1-3 get.sh failed: $out"
fi

# --- 2 a tampered archive is refused -----------------------------------------

BAD="$ROOT/bad-release"
mkdir -p "$BAD"
cp "$REL/checksums.txt" "$BAD/checksums.txt"
printf 'tampered' > "$ROOT/build/agentpane"
tar -czf "$BAD/$ARCHIVE" -C "$ROOT/build" agentpane

BIN_DIR="$ROOT/bin2"
if out="$(AGENTPANE_VERSION=v9.9.9 AGENTPANE_BASE_URL="file://$BAD" \
          AGENTPANE_BIN_DIR="$BIN_DIR" /bin/sh "$GET" 2>&1)"; then
  bad "4 a tampered archive was installed anyway"
else
  case "$out" in
    *"checksum mismatch"*) ok "4 refuses an archive that does not match checksums.txt" ;;
    *) bad "4 wrong failure for a tampered archive: $out" ;;
  esac
  [ -e "$BIN_DIR/agentpane" ] && bad "5 a refused install left a binary behind" || ok "5 nothing installed after a refusal"
fi

# --- 3 a missing asset fails loudly ------------------------------------------

BIN_DIR="$ROOT/bin3"
if out="$(AGENTPANE_VERSION=v0.0.0 AGENTPANE_BASE_URL="file://$REL" \
          AGENTPANE_BIN_DIR="$BIN_DIR" /bin/sh "$GET" 2>&1)"; then
  bad "6 a missing release asset reported success"
else
  case "$out" in
    *"no such release asset"*|*"error"*) ok "6 a missing release asset is an error" ;;
    *) bad "6 wrong failure for a missing asset: $out" ;;
  esac
fi

echo
echo "$PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
