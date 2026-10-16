# Работа с репозиторием

Памятка для людей и ИИ-ассистентов. Что это за проект — [README.md](README.md); как пользоваться
пакетами — [openwrt/README.md](openwrt/README.md).

Пошаговые сценарии — скилы в [.claude/skills/](.claude/skills/) (обычный Markdown, годится и для
человека):

| Скил | Когда |
|---|---|
| [router-install](.claude/skills/router-install/SKILL.md) | первая установка на роутер и настройка интерфейса |
| [router-update](.claude/skills/router-update/SKILL.md) | обновление пакетов, после sysupgrade, удаление |
| [android-release](.claude/skills/android-release/SKILL.md) | сборка APK, версия, установка на телефон/эмулятор |
| [windows-release](.claude/skills/windows-release/SKILL.md) | zip для Windows x64, версия, wintun, разработка на Mac, проверка на ПК |
| [connect-device](.claude/skills/connect-device/SKILL.md) | импорт профиля из панели сервера, «нет связи» |

Сервер и панель выдачи подключений (`vkturn-panel`) — в репозитории **vk-turn-proxy**
(anton48/vk-turn-proxy и форк old-atstec/vk-turn-proxy): там свои скилы `vps-install`, `vps-update`,
`issue-connection`, `release`.

## Где что

| Путь | Что |
|---|---|
| `cmd/vk-turn-proxyd/` | демон для роутера (наш код) |
| `pkg/proxy`, `pkg/turnbind`, `pkg/proxy/srtpwrap` | ядро апстрима; правки — минимальные и изолированные (`sockopt*.go`, `redact.go`) |
| `pkg/csqtt`, `pkg/speedtest`, `third_party/` | апстрим, не трогаем |
| `openwrt/vk-turn-proxy/` | пакет: Makefile, `files/` — netifd-обработчик, хук up/down, rpcd-плагин |
| `openwrt/luci-proto-vkturn/` | пакет LuCI |
| `openwrt/package-native.sh`, `openwrt/build.sh` | две сборки одних и тех же пакетов |
| `android/` | Android-приложение (Kotlin, без AndroidX); демон внутри — `cmd/vk-turn-proxyd` в режиме приложения (`app.go`) |
| `android/.../ProfileImport.kt`, `ScanActivity.kt` | импорт профиля панели: ссылка `vkturn://`, файл `.vkturn`, QR (ZXing core) |
| `cmd/vkturn/`, `windows/` | Windows-приложение: сервис SYSTEM + окно (Edge `--app`) и трей; демон — `cmd/vk-turn-proxyd` в режиме приложения, wintun; [windows/README.md](windows/README.md) |
| `pkg/srs` | разбор sing-box rule-set (`.srs`) и сопоставление доменов (доменный режим Windows) |
| `docs/` | [разработка и релизы](docs/development.md), [устройство и решения](docs/design.md), [проверки на роутере](docs/router-testing.md) |
| `openwrt/TODO.md` | отложенные доработки |

## Команды

```sh
go test -count=1 -timeout=10m ./cmd/... ./pkg/... ./tools/hygiene/   # перед каждым коммитом
tools/test.sh                                                       # полный прогон (race, slow-тесты, сборка под 4 архитектуры)
openwrt/package-native.sh                                           # apk для aarch64_generic за секунды
ARCH=mipsel_24kc openwrt/package-native.sh                          # другая архитектура
openwrt/build.sh                                                    # эталон: OpenWrt SDK (на Apple silicon — часы, эмуляция)
android/build.sh                                                    # APK (демон — Go хоста, Gradle — в контейнере)
windows/build.sh                                                    # zip для Windows x64 (Go хоста + официальный wintun.dll по sha256)
go run ./cmd/vkturn -demo -daemon <vk-turn-proxyd>                  # окно Windows-приложения на Mac, сеть фальшивая
```

Linux-специфичное (SO_MARK, `/proc/net/route`) проверяется в контейнере:
`docker run --rm -v "$PWD:/src:ro" golang:1.26-bookworm sh -c 'cp -a /src /w && cd /w && go test ./cmd/... ./pkg/...'`.

## Правила

- **Никаких реальных адресов, ключей и ссылок на звонки в файлах репозитория** — он публичный.
  Примеры — документационные адреса (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24), выдуманные
  ключи и ссылки. `tools/hygiene` ловит IP-адреса (не hex из `/proc/net/route`, не ссылки и не ключи —
  их проверять глазами). Однажды реальная ссылка и адрес роутера уже утекли в тест.
- Коммиты — от `old-atstec <old-atstec@users.noreply.github.com>` (`git config --local`), с
  `Co-Authored-By` ассистента. Push делает владелец.
- Изменили что-то в пакете — поднимите `PKG_RELEASE` в его Makefile (иначе `apk add` не обновит).
  Выпуск Android — `vkturnVersionCode` в `android/gradle.properties`; Windows — `windows/VERSION`.
- Windows-only код (`*_windows.go`: сервис, iphlpapi, NRPT, трей) на Mac только компилируется
  (`GOOS=windows go vet ./cmd/vkturn ./cmd/vk-turn-proxyd`); логика — в платформонезависимых файлах
  с тестами на фальшивой платформе. Живая проверка — на ПК по [docs/windows-testing.md](docs/windows-testing.md).
- `android/keystore/` — ключ подписи APK; в git не класть, терять нельзя (иначе обновление не встанет).
- Сборка воспроизводимая: `GOFLAGS=-mod=readonly`, `CGO_ENABLED=0`, `-trimpath`; `GOSUMDB`/`GONOSUMDB`
  не трогать; ничего не ставить из чужих бинарных релизов.
- `gofmt -l pkg/` показывает файлы апстрима (`creds.go`, `captcha_pow.go`, …) — расхождения его, не наши;
  не форматировать их целиком, чтобы не раздувать дифф с апстримом.
- Скрипт внутри `docker run … -c '…'` в `build.sh` — в одинарных кавычках: **никаких апострофов в
  комментариях** (`make's` уже ломал сборку). `bash -n openwrt/build.sh` перед запуском.
- Новый сокет в `pkg/proxy` — только через `dialControl`/`sockoptControl` (иначе fwmark мимо);
  `dialhook_test.go` и `sockopt_test.go` это ловят.
- Новый `log.Printf` с ответом VK — через `redactedJSON`/`redactBody`/`redactURL` (`redact_test.go`).

## Профиль из панели сервера

`vkturn://import?c=<base64url(JSON)>`, тот же JSON — файл `.vkturn`. Источник формата — тип
`Profile` в `panel/profile.go` репозитория vk-turn-proxy; здесь его читают `ProfileImport.kt`
(Android), `parseProfile` в `luci-proto-vkturn/.../protocol/vkturn.js` (LuCI) и `importProfile` в
`cmd/vkturn/profile.go` (Windows). Новые **необязательные** поля — можно; переименование или новое
обязательное поле — только с `"v": 2` и одновременной правкой всех трёх читателей и панели. В
`ProfileImportTest.kt` и `cmd/vkturn/app_test.go` лежит эталонная ссылка, сгенерированная панелью:
изменили формат — перегенерируйте её в vk-turn-proxy:
`VKTURN_VECTOR=1 go test -run TestPrintVector -v ./panel/`.

## Грабли роутера (OpenWrt 25.12)

- Нет `timeout`, `diff`, `nohup`; `sleep` только целые секунды. Долгое — со сторожем
  `cmd & p=$!; (sleep 70; kill $p) &`. Фоновый запуск — `start-stop-daemon -S -b -m -p … -x …`.
- netifd читает обработчики протоколов при старте: после первой установки — `service network restart`
  или ребут. **`network restart` сносит правило NetShift** (`fwmark 0x100000 → table netshift`) —
  после него `rm -f /tmp/sing-box/rulesets/*-user-domains-ruleset.json && netshift restart`.
- Проверять маршрутизацию NetShift с машины, где работает свой TUN-прокси (clash/sing-box), бесполезно.
- SSH на роутер — по паролю через master-сессию (`ssh -MNf <host>`), её поднимает человек; живёт ~час.
