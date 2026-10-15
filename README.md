# VK TURN Proxy — OpenWrt

WireGuard через TURN-релеи звонков VK как **обычный сетевой интерфейс OpenWrt**:
`proto vkturn` в `/etc/config/network`, жизненный цикл через netifd, форма и статус в LuCI,
выход для NetShift / sing-box (`connection_type=vpn`, `interface=vkturn`).

Форк [anton48/vk-turn-proxy-ios](https://github.com/anton48/vk-turn-proxy-ios) @ `a13e19e`.
Транспорт — родной режим приложения **SRTP** (DTLS-SRTP поверх TURN, креды через путь VK Звонков
без капчи) к серверу [anton48/vk-turn-proxy](https://github.com/anton48/vk-turn-proxy)
(ветка `add-server-srtp-layer`, флаг `-srtp`). iOS-приложение и десктопный консольный клиент из
форка удалены, ядро `pkg/proxy` сохранено с минимальными изменениями.

## Состав

| Путь | Что |
|---|---|
| `cmd/vk-turn-proxyd` | демон для роутера (router mode): tun + wireguard-go + прокси, статус, хук netifd |
| `pkg/proxy`, `pkg/turnbind` | ядро апстрима: SRTP, TURN, пул кредов, автосолвер капчи |
| `openwrt/vk-turn-proxy` | пакет: демон, `/lib/netifd/proto/vkturn.sh`, хук up/down, rpcd-объект `vkturn` |
| `openwrt/luci-proto-vkturn` | пакет LuCI: форма протокола и страница Status → VK TURN |
| `android/` | приложение для Android: VPN с раздельным туннелированием по приложениям, движок — тот же демон |

| Документ | О чём |
|---|---|
| [openwrt/README.md](openwrt/README.md) | установка, настройка UCI/LuCI, NetShift, firewall, безопасность |
| [android/README.md](android/README.md) | Android: устройство, раздельное туннелирование, сборка APK |
| [docs/development.md](docs/development.md) | сборка (быстрая и SDK), версии, установка/обновление/удаление, sysupgrade, апстрим |
| [docs/design.md](docs/design.md) | устройство и почему так: netifd-хук, статус, fwmark, живость туннеля, кэш кредов |
| [docs/router-testing.md](docs/router-testing.md) | сценарии проверки на роутере и замеры |
| [openwrt/TODO.md](openwrt/TODO.md) | отложенные доработки |
| [CLAUDE.md](CLAUDE.md) | памятка для работы с репозиторием (люди и ИИ-ассистенты) |

## Изменения ядра относительно апстрима

- `pkg/proxy/sockopt*.go` — `SO_MARK` / `SO_BINDTODEVICE` на всех сокетах прокси (через тот же
  Control-хук, что и dial hook); `net.DialUDP` / `net.ListenUDP` заменены на Dialer / ListenConfig.
- `pkg/proxy/redact.go` — ответы VK (step2, step4, ошибка капчи, методы капчи) и URL капчи
  попадают в лог с замаскированными токенами, TURN-логином и паролем.
- Тесты, читавшие исходники удалённого iOS-моста, сокращены до проверок самого ядра.

## Credits

Based on [vk-turn-proxy-ios](https://github.com/anton48/vk-turn-proxy-ios) by anton48 and
[vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) by [cacggghp](https://github.com/cacggghp).

## License

[GNU General Public License v3.0](LICENSE) (GPL-3.0) — as a derivative of
[vk-turn-proxy](https://github.com/cacggghp/vk-turn-proxy) (GPL-3.0).

Files that carry an `SPDX-License-Identifier` header are additionally available under the license
named there (e.g. MIT); the project as a whole is GPL-3.0.
