# Разработка, сборка, релизы

## Что получается на выходе

Два пакета apk (формат apk-tools 3, OpenWrt 25.12+):

| Пакет | Архитектура | Содержимое |
|---|---|---|
| `vk-turn-proxy-<ver>-r<rel>.apk` | своя для каждой (`aarch64_generic`, `x86_64`, `arm_cortex-a7_neon-vfpv4`, `mipsel_24kc`, …) | `/usr/sbin/vk-turn-proxyd`, `/lib/netifd/proto/vkturn.sh`, `/lib/netifd/vkturn-notify.sh`, `/usr/share/rpcd/ucode/vkturn` |
| `luci-proto-vkturn-<ver>-r<rel>.apk` | `noarch` | форма протокола, страница статуса, ACL, пункт меню |

Демон — статический Go-бинарник (`CGO_ENABLED=0`): от libc и ядра роутера не зависит, модулей ядра
не содержит (`kmod-tun` — зависимость из официального репозитория). Поэтому пакет **не нужно
пересобирать** при обновлении OpenWrt внутри ветки (25.12.x); пересборка нужна при изменении кода,
для новой архитектуры и, возможно, при переходе на новую ветку OpenWrt (netifd, rpcd, формат apk).

## Две сборки

### Быстрая: `openwrt/package-native.sh`

```sh
openwrt/package-native.sh                          # aarch64_generic (NanoPi R4S/R5S, RPi 4/5 и т. п.)
ARCH=x86_64 openwrt/package-native.sh
ARCH=arm_cortex-a7_neon-vfpv4 openwrt/package-native.sh
ARCH=mipsel_24kc openwrt/package-native.sh         # MT7621 и т. п.
```

- Демон собирает Go хоста: `CGO_ENABLED=0 GOOS=linux GOARCH=… -trimpath -mod=readonly`,
  в версию вшиваются `PKG_VERSION-PKG_RELEASE` и коммит (`-dirty`, если дерево не чистое).
- Упаковка — `apk mkpkg` из Alpine edge (apk-tools 3) в Docker, нативно на arm64/x86_64.
  Раскладка, метаданные, обёртки скриптов (`default_postinst`/`default_prerm`) и список файлов
  `/lib/apk/packages/<pkg>.list` — как в OpenWrt `include/package-pack.mk`.
- Скрипты пакета (postinst/prerm/postrm) берутся **из его Makefile** — один источник для обеих сборок.
- Результат: `openwrt/out/native-<arch>/*.apk` + `SHA256SUMS`. Время — секунды.

Нужно: Go ≥ 1.26 (из `go.mod`), Docker.

### Эталонная: `openwrt/build.sh`

```sh
openwrt/build.sh                       # rockchip/armv8, OpenWrt 25.12.4
TARGET=x86/64 openwrt/build.sh
RELEASE=25.12.4 TARGET=ramips/mt7621 openwrt/build.sh
```

- Официальный SDK релиза в Docker (`debian:bookworm`, `--platform linux/amd64`); sha256 SDK сверяется
  с `sha256sums` релиза.
- Go собирается **из исходников** фидом `packages/lang/golang` (bootstrap-цепочка), пакет — через
  `golang-package.mk`. Модули сверяются с `go.sum` и checksum database.
- Наше дерево подключается локальным фидом (`src-link vkturn`), исходник пакета — копия дерева
  (`VKTURN_SRC`). Переменная своя: `USE_SOURCE_DIR` в командной строке make достаётся **всем**
  пакетам (zlib, lua… пытались собраться из нашего дерева).
- В `.config` выключены `ALL_KMODS`/`ALL_NONSHARED` (иначе SDK пакует сотни модулей).
- Параллельная сборка зависимостей (lua) иногда теряет jobserver make — повтор с `-j1` встроен.
- SDK, фиды и собранный Go живут в Docker-томе `vkturn-sdk-<target>-<release>`: первая сборка долгая
  (на Apple silicon под эмуляцией — часы), повторные быстрее. Таймауты: `BUILD_TIMEOUT` (3 ч).
- Результат: `openwrt/out/<target>/*.apk`, `SHA256SUMS`, полный лог `build.log`.

На x86_64-машине или в CI эталонная сборка идёт нативно — так её и стоит запускать для релизов.

### Для фида OpenWrt

`openwrt/vk-turn-proxy/Makefile` без `VKTURN_SRC` берёт исходник из git по `PKG_SOURCE_VERSION`
(сейчас `COMMIT_PLACEHOLDER` — при оформлении фида поставить SHA релизного коммита и
`PKG_MIRROR_HASH`).

## Версии

- `PKG_VERSION` — версия проекта, `PKG_RELEASE` — номер сборки пакета. **Любое изменение
  содержимого пакета — `PKG_RELEASE+1`**, иначе `apk add` сочтёт версию той же и ничего не обновит.
- `luci-proto-vkturn` версионируется отдельно (свой Makefile).
- `vk-turn-proxyd version` печатает версию и коммит; то же — поля `version`/`commit` в статусе.

## Установка, обновление, удаление

```sh
scp openwrt/out/native-aarch64_generic/*.apk root@<router>:/tmp/
ssh root@<router> 'apk add --allow-untrusted /tmp/vk-turn-proxy-*.apk /tmp/luci-proto-vkturn-*.apk'
```

- `--allow-untrusted`: пакеты не подписаны ключом OpenWrt (своя подпись — вместе со своим фидом,
  см. ниже).
- **Первая установка**: netifd узнаёт о протоколе `vkturn` только при старте —
  `service network restart` (обрыв всех интерфейсов на несколько секунд) или ребут; с NetShift —
  после этого перезапустить NetShift (см. [CLAUDE.md](../CLAUDE.md#грабли-роутера-openwrt-2512)).
- **Обновление**: тот же `apk add` с новым `PKG_RELEASE`; `post-upgrade` сам перезапускает
  работающие интерфейсы `vkturn` (старый демон отдаёт TURN-аллокации, новый стартует за ~2 с).
- **Удаление**: `apk del vk-turn-proxy luci-proto-vkturn` — `pre-deinstall` опускает интерфейсы,
  `post-deinstall` удаляет `/var/run/vkturn` и кэш кредов `/etc/vkturn`; секцию интерфейса в
  `/etc/config/network` удалить руками.
- **sysupgrade** (прошивка новой версии OpenWrt) сохраняет конфиги, но не доустановленные пакеты:
  после него поставить оба apk заново (интерфейс `vkturn` в конфиге уже есть).

## Подтянуть апстрим

Форк ответвлён от `anton48/vk-turn-proxy-ios` @ `a13e19e`. Наши правки ядра:

- `pkg/proxy/sockopt*.go` (новые), `dialhook.go` (`dialControl` применяет опции),
  `proxy.go` — два сайта: `net.DialUDP` → `Dialer{Control: sockoptControl}`,
  `net.ListenUDP` → `ListenConfig{Control: sockoptControl}`, **без контекста** (как у оригинала —
  иначе на Linux падает `relayjoin_test`);
- `pkg/proxy/redact.go` (новый) и вызовы в `creds.go`, `creds_vkcalls.go`, `creds_vkcookie.go`,
  `captcha_pow.go`, `proxy.go`;
- тесты, читавшие исходники удалённого iOS-моста (`credpool_lease_test.go`, `quotabreaker_test.go`,
  `seatcool_test.go`, `writepacket_test.go`) — убраны только проверки моста.

```sh
git remote add upstream https://github.com/anton48/vk-turn-proxy-ios.git
git fetch upstream
git merge upstream/main     # конфликты — в iOS/консоли (удалены у нас: git rm) и в перечисленных файлах
go test ./cmd/... ./pkg/... ./tools/hygiene/
```

## Распространение (не сделано, варианты)

1. **CI в GitHub Actions**: тесты на push; на тег `v*` — `openwrt/build.sh` под нужные таргеты,
   артефакты и `SHA256SUMS` в Release. Actions — закреплённые по SHA.
2. **Свой apk-фид**: CI публикует подписанный индекс (GitHub Pages и т. п.); на роутере — ключ и
   строка в `/etc/apk/repositories.d/`; дальше `apk update && apk upgrade`, после sysupgrade —
   `apk add vk-turn-proxy luci-proto-vkturn`.
