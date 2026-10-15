# Работа с репозиторием

Памятка для людей и ИИ-ассистентов. Что это за проект — [README.md](README.md); как пользоваться
пакетами — [openwrt/README.md](openwrt/README.md).

## Где что

| Путь | Что |
|---|---|
| `cmd/vk-turn-proxyd/` | демон для роутера (наш код) |
| `pkg/proxy`, `pkg/turnbind`, `pkg/proxy/srtpwrap` | ядро апстрима; правки — минимальные и изолированные (`sockopt*.go`, `redact.go`) |
| `pkg/csqtt`, `pkg/speedtest`, `third_party/` | апстрим, не трогаем |
| `openwrt/vk-turn-proxy/` | пакет: Makefile, `files/` — netifd-обработчик, хук up/down, rpcd-плагин |
| `openwrt/luci-proto-vkturn/` | пакет LuCI |
| `openwrt/package-native.sh`, `openwrt/build.sh` | две сборки одних и тех же пакетов |
| `docs/` | [разработка и релизы](docs/development.md), [устройство и решения](docs/design.md), [проверки на роутере](docs/router-testing.md) |
| `openwrt/TODO.md` | отложенные доработки |

## Команды

```sh
go test -count=1 -timeout=10m ./cmd/... ./pkg/... ./tools/hygiene/   # перед каждым коммитом
tools/test.sh                                                       # полный прогон (race, slow-тесты, сборка под 4 архитектуры)
openwrt/package-native.sh                                           # apk для aarch64_generic за секунды
ARCH=mipsel_24kc openwrt/package-native.sh                          # другая архитектура
openwrt/build.sh                                                    # эталон: OpenWrt SDK (на Apple silicon — часы, эмуляция)
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
- Сборка воспроизводимая: `GOFLAGS=-mod=readonly`, `CGO_ENABLED=0`, `-trimpath`; `GOSUMDB`/`GONOSUMDB`
  не трогать; ничего не ставить из чужих бинарных релизов.
- `gofmt -l pkg/` показывает файлы апстрима (`creds.go`, `captcha_pow.go`, …) — расхождения его, не наши;
  не форматировать их целиком, чтобы не раздувать дифф с апстримом.
- Скрипт внутри `docker run … -c '…'` в `build.sh` — в одинарных кавычках: **никаких апострофов в
  комментариях** (`make's` уже ломал сборку). `bash -n openwrt/build.sh` перед запуском.
- Новый сокет в `pkg/proxy` — только через `dialControl`/`sockoptControl` (иначе fwmark мимо);
  `dialhook_test.go` и `sockopt_test.go` это ловят.
- Новый `log.Printf` с ответом VK — через `redactedJSON`/`redactBody`/`redactURL` (`redact_test.go`).

## Грабли роутера (OpenWrt 25.12)

- Нет `timeout`, `diff`, `nohup`; `sleep` только целые секунды. Долгое — со сторожем
  `cmd & p=$!; (sleep 70; kill $p) &`. Фоновый запуск — `start-stop-daemon -S -b -m -p … -x …`.
- netifd читает обработчики протоколов при старте: после первой установки — `service network restart`
  или ребут. **`network restart` сносит правило NetShift** (`fwmark 0x100000 → table netshift`) —
  после него `rm -f /tmp/sing-box/rulesets/*-user-domains-ruleset.json && netshift restart`.
- Проверять маршрутизацию NetShift с машины, где работает свой TUN-прокси (clash/sing-box), бесполезно.
- SSH на роутер — по паролю через master-сессию (`ssh -MNf <host>`), её поднимает человек; живёт ~час.
