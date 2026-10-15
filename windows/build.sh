#!/usr/bin/env bash
# The Windows app (x64): vkturn.exe (the window, the tray, the service) and
# vk-turn-proxyd.exe (the daemon of the router and of the Android app, the
# same code) cross-compiled by the host's Go — CGO_ENABLED=0, -trimpath,
# -mod=readonly, as every other build here — and wintun.dll, the one binary
# not built from this tree: the official, signed build of WireGuard LLC from
# wintun.net, its zip checked against the sha256 that wintun.net publishes.
#
#   windows/build.sh            # windows/out/vkturn-<ver>-windows-amd64.zip + SHA256SUMS
#
# The version: windows/VERSION. wintun's zip is kept in windows/.cache/.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
out="$here/out"
cache="$here/.cache"

WINTUN_VERSION=0.14.1
# https://www.wintun.net/ — "wintun-0.14.1.zip … SHA2-256: 07c2561…"
WINTUN_SHA256=07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51

version=$(tr -d ' \n' <"$here/VERSION")
commit=$(git -C "$root" rev-parse --short=12 HEAD)
[ -z "$(git -C "$root" status --porcelain)" ] || commit="$commit-dirty"
name="vkturn-$version-windows-amd64"

echo "== wintun $WINTUN_VERSION"
zip="$cache/wintun-$WINTUN_VERSION.zip"
mkdir -p "$cache"
if [ ! -f "$zip" ] || [ "$(shasum -a 256 "$zip" | cut -d' ' -f1)" != "$WINTUN_SHA256" ]; then
	rm -f "$zip.part"
	# wintun.net can be slow from here: resumed until whole.
	for _ in $(seq 1 60); do
		curl -fsSL --retry 3 -C - --max-time 300 -o "$zip.part" "https://www.wintun.net/builds/wintun-$WINTUN_VERSION.zip" && break
		sleep 2
	done
	mv "$zip.part" "$zip"
fi
got=$(shasum -a 256 "$zip" | cut -d' ' -f1)
if [ "$got" != "$WINTUN_SHA256" ]; then
	echo "wintun-$WINTUN_VERSION.zip: sha256 $got, want $WINTUN_SHA256 — not the official build" >&2
	exit 1
fi

stage="$out/$name"
rm -rf "$out"
mkdir -p "$stage"
unzip -q -o -j "$zip" "wintun/bin/amd64/wintun.dll" -d "$stage"
unzip -q -o -p "$zip" "wintun/LICENSE.txt" >"$stage/wintun-LICENSE.txt"
# Its Authenticode signature, where a tool to read it is at hand (the zip's
# hash above is what binds it either way).
if command -v osslsigncode >/dev/null; then
	osslsigncode verify -in "$stage/wintun.dll" | grep -E "Signer's certificate|Subject:.*WireGuard" | head -3 || true
fi

common=(CGO_ENABLED=0 GOOS=windows GOARCH=amd64 GOFLAGS=-mod=readonly)
echo "== vk-turn-proxyd.exe"
(cd "$root" && env "${common[@]}" go build -trimpath \
	-ldflags "-s -w -X main.version=windows-$version -X main.commit=$commit" \
	-o "$stage/vk-turn-proxyd.exe" ./cmd/vk-turn-proxyd)
echo "== vkturn.exe"
(cd "$root" && env "${common[@]}" go build -trimpath \
	-ldflags "-s -w -H windowsgui -X main.version=$version -X main.commit=$commit" \
	-o "$stage/vkturn.exe" ./cmd/vkturn)

cat >"$stage/README.txt" <<TXT
VK TURN $version для Windows 10/11 (x64)

1. Распакуйте архив куда угодно и запустите vkturn.exe.
2. Windows один раз спросит права администратора: VK TURN устанавливается
   в "Program Files\\VK TURN" и запускает свой сервис. Дальше — без вопросов,
   из меню Пуск или значка в трее.
3. Вставьте ссылку vkturn://... из панели сервера (или откройте файл
   .vkturn) и нажмите "Импортировать и подключить".

Подробно: windows/README.md в репозитории.
wintun.dll — официальная подписанная сборка WireGuard LLC (wintun.net),
лицензия — wintun-LICENSE.txt.
TXT
# The zip's entries carry the commit's time: the same tree, the same archive.
stamp=$(git -C "$root" log -1 --format=%cd --date=format:%Y%m%d%H%M.%S)
find "$stage" -exec touch -t "$stamp" {} +
(cd "$out" && rm -f "$name.zip" && zip -q -X -r "$name.zip" "$name")
(cd "$out" && shasum -a 256 "$name.zip" "$name"/* | sed 's| \./| |' >SHA256SUMS)
cat "$out/SHA256SUMS"
ls -l "$out"
