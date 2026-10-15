#!/usr/bin/env bash
# The fast path to the same two apk packages as openwrt/build.sh, without the
# SDK: the daemon is cross-compiled by the host's Go (CGO_ENABLED=0 — it links
# nothing of the target's), and the packages are put together with apk-tools 3
# `apk mkpkg` (Alpine edge, natively on arm64 and x86_64 hosts) the way
# OpenWrt's include/package-pack.mk does: the same metadata, the same wrapped
# scripts (default_postinst / default_prerm), the file list in
# /lib/apk/packages/<pkg>.list. The package scripts are read out of the
# packages' Makefiles: one source for both paths.
#
#   openwrt/package-native.sh                         # aarch64_generic
#   ARCH=x86_64 openwrt/package-native.sh
#   ARCH=arm_cortex-a7_neon-vfpv4 openwrt/package-native.sh
#   ARCH=mipsel_24kc openwrt/package-native.sh
#
# For releases and CI the SDK build (build.sh) is the reference; this one is
# for iterating on a router. Output: openwrt/out/native-<arch>/ with SHA256SUMS.
set -euo pipefail

ARCH=${ARCH:-aarch64_generic}
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
out="$here/out/native-$ARCH"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

goenv=(CGO_ENABLED=0 GOOS=linux)
case "$ARCH" in
aarch64_*) goenv+=(GOARCH=arm64) ;;
x86_64) goenv+=(GOARCH=amd64) ;;
arm_cortex-a7*|arm_cortex-a9*|arm_cortex-a15*) goenv+=(GOARCH=arm GOARM=7) ;;
mipsel_*) goenv+=(GOARCH=mipsle GOMIPS=softfloat) ;;
mips_*) goenv+=(GOARCH=mips GOMIPS=softfloat) ;;
*) echo "unknown ARCH $ARCH" >&2; exit 2 ;;
esac

mkvar() { sed -n "s/^$1:=//p" "$2" | head -n1; }
pkgmk="$here/vk-turn-proxy/Makefile"
lucimk="$here/luci-proto-vkturn/Makefile"
version="$(mkvar PKG_VERSION "$pkgmk")-r$(mkvar PKG_RELEASE "$pkgmk")"
luciversion="$(mkvar PKG_VERSION "$lucimk")-r$(mkvar PKG_RELEASE "$lucimk")"
commit=$(git -C "$root" rev-parse --short=12 HEAD)
[ -z "$(git -C "$root" status --porcelain)" ] || commit="$commit-dirty"

# define Package/<pkg>/<script> … endef out of a Makefile, $$ unescaped.
script() {
	awk -v start="define Package/$2/$3" '$0 == start {on=1; next} on && $0 == "endef" {exit} on' "$1" | sed 's/\$\$/$/g'
}

echo "== vk-turn-proxyd ($ARCH, ${goenv[*]})"
(cd "$root" && env "${goenv[@]}" GOFLAGS=-mod=readonly go build -trimpath \
	-ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
	-o "$work/vk-turn-proxyd" ./cmd/vk-turn-proxyd)

# vk-turn-proxy: the tree, as Package/vk-turn-proxy/install lays it out.
p=$work/vk-turn-proxy
install -d "$p/usr/sbin" "$p/lib/netifd/proto" "$p/usr/share/rpcd/ucode" "$p/lib/apk/packages"
install -m 0755 "$work/vk-turn-proxyd" "$p/usr/sbin/vk-turn-proxyd"
install -m 0755 "$here/vk-turn-proxy/files/vkturn.sh" "$p/lib/netifd/proto/vkturn.sh"
install -m 0755 "$here/vk-turn-proxy/files/vkturn-notify.sh" "$p/lib/netifd/vkturn-notify.sh"
install -m 0644 "$here/vk-turn-proxy/files/vkturn.rpcd.uc" "$p/usr/share/rpcd/ucode/vkturn"

# luci-proto-vkturn: luci.mk's layout — htdocs under /www, root/ at /.
l=$work/luci-proto-vkturn
install -d "$l/www" "$l/lib/apk/packages"
cp -R "$here/luci-proto-vkturn/htdocs/." "$l/www/"
cp -R "$here/luci-proto-vkturn/root/." "$l/"
find "$l" -type d -exec chmod 0755 {} + ; find "$l" -type f -exec chmod 0644 {} +

# The scripts, wrapped as package-pack.mk wraps them.
s=$work/scripts
mkdir -p "$s/vk-turn-proxy" "$s/luci-proto-vkturn"
wrap() { # pkg dir postinst-body prerm-body postrm-body
	local pkg=$1 d=$2
	{
		echo '#!/bin/sh'
		echo '[ "${IPKG_NO_SCRIPT}" = "1" ] && exit 0'
		echo '[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0'
		echo '. ${IPKG_INSTROOT}/lib/functions.sh'
		echo 'export root="${IPKG_INSTROOT}"'
		echo "export pkgname=\"$pkg\""
		echo 'add_group_and_user'
		echo 'default_postinst'
		printf '%s\n' "$3" | sed '/^[[:space:]]*#!/d'
	} > "$d/post-install"
	{ echo '#!/bin/sh'; echo 'export PKG_UPGRADE=1'; sed '/^[[:space:]]*#!/d' "$d/post-install"; } > "$d/post-upgrade"
	{
		echo '#!/bin/sh'
		echo '[ -s "${IPKG_INSTROOT}/lib/functions.sh" ] || exit 0'
		echo '. ${IPKG_INSTROOT}/lib/functions.sh'
		echo 'export root="${IPKG_INSTROOT}"'
		echo "export pkgname=\"$pkg\""
		echo 'default_prerm'
		printf '%s\n' "$4" | sed '/^[[:space:]]*#!/d'
	} > "$d/pre-deinstall"
	if [ -n "$5" ]; then printf '%s\n' "$5" > "$d/post-deinstall"; fi
	chmod 0755 "$d"/*
}
wrap vk-turn-proxy "$s/vk-turn-proxy" \
	"$(script "$pkgmk" vk-turn-proxy postinst)" \
	"$(script "$pkgmk" vk-turn-proxy prerm)" \
	"$(script "$pkgmk" vk-turn-proxy postrm)"
# luci.mk's own postinst: drop LuCI's caches, let rpcd see the ACL.
wrap luci-proto-vkturn "$s/luci-proto-vkturn" \
	"$(printf '%s\n' '[ -n "${IPKG_INSTROOT}" ] || { rm -f /tmp/luci-indexcache.*; rm -rf /tmp/luci-modulecache/; killall -HUP rpcd 2>/dev/null; exit 0; }')" "" ""

for pkg in vk-turn-proxy luci-proto-vkturn; do
	(cd "$work/$pkg" && find . \( -type f -o -type l \) | sed 's|^\.||' | sort > "lib/apk/packages/$pkg.list")
done

desc=$(awk '/^define Package\/vk-turn-proxy\/description$/{on=1; next} on && /^endef$/{exit} on' "$pkgmk" | sed 's/^ *//' | tr '\n' ' ' | sed 's/ *$//')

mkdir -p "$out"
rm -f "$out"/*.apk "$out/SHA256SUMS"
docker run --rm -e DESC="$desc" -v "$work:/w" -v "$out:/out" alpine:edge sh -euc "
chown -R 0:0 /w
apk mkpkg \
	--info 'name:vk-turn-proxy' --info 'version:$version' \
	--info \"description:\$DESC\" --info 'arch:$ARCH' \
	--info 'license:GPL-3.0-only' --info 'origin:vkturn/vk-turn-proxy' \
	--info 'url:https://github.com/old-atstec/vk-turn-proxy-openwrt' \
	--info 'maintainer:old-atstec' \
	--info 'depends:kmod-tun ca-bundle rpcd rpcd-mod-ucode' \
	--script 'post-install:/w/scripts/vk-turn-proxy/post-install' \
	--script 'post-upgrade:/w/scripts/vk-turn-proxy/post-upgrade' \
	--script 'pre-deinstall:/w/scripts/vk-turn-proxy/pre-deinstall' \
	--script 'post-deinstall:/w/scripts/vk-turn-proxy/post-deinstall' \
	--files /w/vk-turn-proxy --output /out/vk-turn-proxy-$version.apk
apk mkpkg \
	--info 'name:luci-proto-vkturn' --info 'version:$luciversion' \
	--info 'description:Support for VK TURN interfaces (vk-turn-proxy)' --info 'arch:noarch' \
	--info 'license:GPL-3.0-only' --info 'origin:vkturn/luci-proto-vkturn' \
	--info 'url:https://github.com/old-atstec/vk-turn-proxy-openwrt' \
	--info 'maintainer:old-atstec' \
	--info 'depends:vk-turn-proxy luci-base' \
	--script 'post-install:/w/scripts/luci-proto-vkturn/post-install' \
	--script 'post-upgrade:/w/scripts/luci-proto-vkturn/post-upgrade' \
	--script 'pre-deinstall:/w/scripts/luci-proto-vkturn/pre-deinstall' \
	--files /w/luci-proto-vkturn --output /out/luci-proto-vkturn-$luciversion.apk
cd /out && sha256sum *.apk > SHA256SUMS && cat SHA256SUMS
"
echo "== $out"
