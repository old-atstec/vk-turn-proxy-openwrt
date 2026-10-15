#!/usr/bin/env bash
# The Android app: the daemon cross-compiled by the host's Go for each ABI
# (CGO_ENABLED=0, GOOS=linux — a static binary runs on Android's kernel as it
# does on a router's; GOOS=android would want cgo and the NDK), packed as
# lib/<abi>/libvkturnd.so, and the APKs built by Gradle in a container
# (android/Dockerfile). LOCAL=1 uses the host's gradle and ANDROID_HOME.
#
#   android/build.sh            # release APKs into android/out/
#   LOCAL=1 android/build.sh
#
# Signing: android/keystore/release.jks, made on the first run with a random
# password (android/keystore/keystore.properties). Keep both: an APK signed by
# another key does not install over this one. Neither goes into git.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
out="$here/out"
jni="$here/app/src/main/jniLibs"

commit=$(git -C "$root" rev-parse --short=12 HEAD)
[ -z "$(git -C "$root" status --porcelain)" ] || commit="$commit-dirty"
version=$(sed -n 's/^vkturnVersionName=//p' "$here/gradle.properties")

rm -rf "$jni"
for abi in arm64-v8a armeabi-v7a x86_64; do
	case $abi in
	arm64-v8a) goenv=(GOARCH=arm64) ;;
	armeabi-v7a) goenv=(GOARCH=arm GOARM=7) ;;
	x86_64) goenv=(GOARCH=amd64) ;;
	esac
	echo "== vk-turn-proxyd ($abi)"
	mkdir -p "$jni/$abi"
	(cd "$root" && env CGO_ENABLED=0 GOOS=linux "${goenv[@]}" GOFLAGS=-mod=readonly go build -trimpath \
		-ldflags "-s -w -X main.version=android-$version -X main.commit=$commit" \
		-o "$jni/$abi/libvkturnd.so" ./cmd/vk-turn-proxyd)
done

ks="$here/keystore"
if [ ! -f "$ks/release.jks" ]; then
	echo "== a new signing key: $ks/release.jks"
	mkdir -p "$ks"
	chmod 700 "$ks"
	pass=$(openssl rand -hex 16)
	keytool -genkeypair -keystore "$ks/release.jks" -storetype PKCS12 -alias vkturn \
		-keyalg EC -groupname secp256r1 -validity 10000 -dname "CN=VK TURN" \
		-storepass "$pass" -keypass "$pass" >/dev/null
	printf 'password=%s\n' "$pass" >"$ks/keystore.properties"
	chmod 600 "$ks/release.jks" "$ks/keystore.properties"
fi
pass=$(sed -n 's/^password=//p' "$ks/keystore.properties")

# The password goes in by the environment, never on a command line.
gradle_args=(--no-daemon --console=plain testReleaseUnitTest assembleRelease
	-PvkturnKeystore=keystore/release.jks -PvkturnKeyAlias=vkturn)
export ORG_GRADLE_PROJECT_vkturnKeystorePassword="$pass" ORG_GRADLE_PROJECT_vkturnKeyPassword="$pass"

if [ "${LOCAL:-}" = 1 ]; then
	echo "== gradle (local)"
	(cd "$here" && gradle "${gradle_args[@]}")
else
	echo "== the build image"
	docker build --platform linux/amd64 -t vkturn-android-build "$here"
	echo "== gradle (container)"
	docker run --rm --platform linux/amd64 \
		-e ORG_GRADLE_PROJECT_vkturnKeystorePassword -e ORG_GRADLE_PROJECT_vkturnKeyPassword \
		-v vkturn-android-gradle:/gradle -v "$here:/src" -w /src \
		vkturn-android-build gradle "${gradle_args[@]}"
fi

rm -rf "$out"
mkdir -p "$out"
for f in "$here"/app/build/outputs/apk/release/*.apk; do
	n=$(basename "$f" .apk)
	n=${n#app-}
	n=${n%-release}
	cp "$f" "$out/vkturn-$version-$n.apk"
done
(cd "$out" && shasum -a 256 ./*.apk | sed 's| \./| |' >SHA256SUMS)
ls -l "$out"
