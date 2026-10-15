#!/usr/bin/env bash
# Build the apk packages (vk-turn-proxy, luci-proto-vkturn) with the official
# OpenWrt SDK in a Docker container, from THIS working tree:
#
#   openwrt/build.sh                        # rockchip/armv8 (aarch64_generic), 25.12.4
#   TARGET=x86/64 openwrt/build.sh
#   TARGET=ramips/mt7621 openwrt/build.sh   # mipsel_24kc
#
# The SDK is checked against the release's sha256sums. Go itself is built from
# source by the packages feed (lang/golang); modules come through the Go proxy
# and are checked against go.sum and the checksum database (GOSUMDB is left
# alone). The packages land in openwrt/out/<target>/, with SHA256SUMS.
#
# Needs: docker (on Apple silicon the x86_64 SDK runs under emulation — the
# first build, Go's bootstrap included, takes a while). A named volume keeps
# the SDK, the feeds and the Go toolchain between runs.
set -euo pipefail

# Ceilings, applied inside the container (macOS has no timeout(1)): the first
# build compiles Go itself under emulation (~30–60 min on Apple silicon); a
# download or a build step that hangs fails instead of waiting.
BUILD_TIMEOUT=${BUILD_TIMEOUT:-3h}

RELEASE=${RELEASE:-25.12.4}
TARGET=${TARGET:-rockchip/armv8}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
slug=${TARGET//\//-}
out="$here/out/$slug"
mkdir -p "$out"

exec docker run --rm --platform linux/amd64 \
	-e BUILD_TIMEOUT="$BUILD_TIMEOUT" -e RELEASE="$RELEASE" -e TARGET="$TARGET" -e SLUG="$slug" \
	-v "$root:/src:ro" \
	-v "$out:/out" \
	-v "vkturn-sdk-$slug-$RELEASE:/sdk" \
	debian:bookworm bash -euo pipefail -c '
apt-get update -qq
apt-get install -y -qq --no-install-recommends \
	build-essential ca-certificates curl file gawk gettext git libncurses-dev \
	python3 python3-setuptools rsync swig unzip wget xz-utils zstd >/dev/null

base="https://downloads.openwrt.org/releases/$RELEASE/targets/$TARGET"
cd /sdk
if [ ! -f .ready ]; then
	curl -fsSL --connect-timeout 20 --max-time 120 "$base/sha256sums" -o sha256sums
	name=$(grep -o "openwrt-sdk-[^ ]*Linux-x86_64.tar.zst" sha256sums | head -n1)
	echo "== SDK $name"
	curl -fsSL --connect-timeout 20 --max-time 1800 "$base/$name" -o "$name"
	grep " \*$name\$" sha256sums | sha256sum -c -
	tar --zstd -xf "$name" --strip-components=1
	rm -f "$name"
	touch .ready
fi

# Our packages as a local feed; a copy of the tree is the source (the build
# writes into its source dir).
rsync -a --delete --exclude .git --exclude openwrt/out /src/ /build-src/
grep -q "^src-link vkturn " feeds.conf.default || echo "src-link vkturn /build-src/openwrt" >> feeds.conf.default
timeout 15m ./scripts/feeds update -a >/dev/null
./scripts/feeds install vk-turn-proxy luci-proto-vkturn >/dev/null
make defconfig >/dev/null

g="git -c safe.directory=/src -C /src"
commit=$($g rev-parse HEAD 2>/dev/null || echo local)
[ -z "$($g status --porcelain 2>/dev/null)" ] || commit="$commit-dirty"
make package/vk-turn-proxy/clean >/dev/null || true
timeout "$BUILD_TIMEOUT" make -j"$(nproc)" package/vk-turn-proxy/compile USE_SOURCE_DIR=/build-src PKG_SOURCE_VERSION="$commit" V=s > /out/build.log 2>&1 \
	|| { tail -n 60 /out/build.log; exit 1; }
timeout 30m make -j"$(nproc)" package/luci-proto-vkturn/compile V=s >> /out/build.log 2>&1 \
	|| { tail -n 60 /out/build.log; exit 1; }

rm -f /out/*.apk /out/SHA256SUMS
find bin/packages -name "vk-turn-proxy-*.apk" -exec cp {} /out/ \;
find bin/packages -name "luci-proto-vkturn-*.apk" -exec cp {} /out/ \;
cd /out
sha256sum *.apk > SHA256SUMS
cat SHA256SUMS
'
