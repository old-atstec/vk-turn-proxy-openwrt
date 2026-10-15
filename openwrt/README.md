# vk-turn-proxy для OpenWrt

`proto vkturn` — WireGuard через TURN-релеи звонков VK как обычный интерфейс netifd.
Проверено на OpenWrt 25.12 (apk), NanoPi R4S (`aarch64_generic`).

## Как устроено

```
LAN-клиент → NetShift / sing-box (outbound direct, bind_interface=vkturn)
           → tun vkturn (wireguard-go внутри vk-turn-proxyd)
           → DTLS-SRTP → TURN-релеи VK (через WAN роутера, мимо NetShift)
           → сервер vk-turn-proxy -srtp → WireGuard сервера → интернет
```

| Кто | Что делает |
|---|---|
| `/lib/netifd/proto/vkturn.sh` | из UCI-секции пишет `/var/run/vkturn/<iface>/config` (0600, каталог 0700), проверяет его (`vk-turn-proxyd -check`) и запускает демон через `proto_run_command` |
| `vk-turn-proxyd` | создаёт tun с именем интерфейса, поднимает прокси и WireGuard, пишет `status.json`, при первом handshake вызывает хук `up`, после `down_after` секунд без живой сессии — хук `down`, при восстановлении — снова `up` |
| `/lib/netifd/vkturn-notify.sh` | `up`: `proto_init_update <dev> 1` + адреса из UCI (+ маршрут по умолчанию, только если `defaultroute 1`) → `proto_send_update`; `down`: `proto_init_update <dev> 0` — как `ppp-up` / `ppp-down` |
| netifd | ставит и снимает адреса и маршруты, перезапускает демон, если тот упал |
| rpcd-объект `vkturn` (ucode) | `status`, `reconnect`, `generateKeyPair`, `getPublicKey` для LuCI и консоли |

Чего демон **не делает никогда**: не трогает маршрут по умолчанию, `/etc/resolv.conf`, dnsmasq,
таблицы и правила маршрутизации, не создаёт маршрутов `/32`, не вызывает `ip`, не ведёт журнал
отката и ничего не пишет в текущий каталог. Пока туннель не несёт трафик, интерфейс не `up` —
NetShift и sing-box не шлют пакеты в мёртвый туннель.

## Установка

```sh
apk add --allow-untrusted ./vk-turn-proxy-*.apk ./luci-proto-vkturn-*.apk
```

Зависимости: `kmod-tun`, `ca-bundle`, `rpcd`, `rpcd-mod-ucode`. `ip-full` и `wireguard-tools`
не нужны — ключи генерирует сам демон (`vk-turn-proxyd genkey`, `vk-turn-proxyd pubkey`).
`apk del vk-turn-proxy luci-proto-vkturn` останавливает интерфейсы (TURN-аллокации
освобождаются) и удаляет `/var/run/vkturn` и кэш кредов `/etc/vkturn`. Секцию интерфейса в
`/etc/config/network` удалите сами.

## Настройка

LuCI → Network → Interfaces → Add new interface → протокол **VK TURN (WireGuard)**. Или UCI:

```
config interface 'vkturn'
	option proto 'vkturn'
	option peer '203.0.113.10:56004'          # SRTP-слушатель сервера
	option private_key '<base64>'              # ключ роутера: vk-turn-proxyd genkey
	option public_key '<base64>'               # ключ сервера
	option preshared_key ''                    # опционально
	list addresses '10.66.66.2/24'
	list vk_link 'https://vk.com/call/join/…'  # анонимный режим берёт первую
	option conns '20'                          # 1…120
	option turn_transport 'udp'                # udp | tcp
	option mtu '1280'                          # 1000…1400
	option keepalive '25'
	option down_after '60'                     # секунд без сессий → link down
	option fwmark '0x200000'                   # опционально, см. ниже
	option bind_device ''                      # опционально: wan / eth0
	option dns_server ''                       # опционально: резолвер для имён VK
	option cred_cache ''                       # по умолчанию /etc/vkturn/<iface>/creds.json
	option turn_server ''                      # опционально: свой TURN host:port
	option pool_reserve '1'
	option uplink_pace '0'                     # KiB/s на соединение, 0 = выкл
	option vk_cookie_file ''                   # опционально: cookies.txt входа в VK
	option stats_every '0'                     # секунд между строками stats в логе
	option log_level 'info'                    # info | debug
```

Имя интерфейса — это и имя tun-устройства: не длиннее 15 символов.

### Смена ссылки на звонок

LuCI → Network → Interfaces → vkturn → Edit → «VK call links» → Save & Apply (или
`uci set network.vkturn.vk_link='https://vk.ru/call/join/…'; uci commit network; ifup vkturn`).
netifd перезапускает интерфейс с новой ссылкой; демон замечает, что ссылка сменилась (рядом с
кэшем хранится её sha256 — `creds.json.link`, саму ссылку туда не пишет), и выбрасывает креды
старого звонка. Ссылки `vk.com` и `vk.ru` равноценны.

Публичный ключ роутера (LuCI показывает его в форме) добавьте на сервере:

```sh
wg set wg-turn peer <pubkey> allowed-ips 10.66.66.2/32   # и [Peer] в конфиг wg-turn
```

### Статус

LuCI → Status → VK TURN, или

```sh
ubus call vkturn status
ubus call vkturn reconnect '{"interface":"vkturn"}'
logread -e vkturn
```

Если VK требует капчу, которую не прошёл автосолвер, статус показывает флаг и ссылку, а демон
повторяет попытки (раз в 2 мин, под rate limit — раз в 10). Ручное решение из LuCI пока не
сделано ([TODO.md](TODO.md)). Для проверки страницы статуса: `option simulate_captcha '1'`.

## NetShift

NetShift 0.9.7 строит для секции `connection_type=vpn` outbound sing-box
`{type: direct, bind_interface: <interface>}`; `interface` — имя **устройства**, у `proto vkturn`
оно совпадает с именем интерфейса:

```sh
uci set netshift.vk=section
uci set netshift.vk.connection_type='vpn'
uci set netshift.vk.interface='vkturn'
uci set netshift.vk.user_domain_list_type='text'
uci set netshift.vk.user_domains_text='ifconfig.me'
uci commit netshift
rm -f /tmp/sing-box/rulesets/*-user-domains-ruleset.json && netshift restart
```

- **Порядок секций**: sing-box берёт первое совпадение. Секция должна стоять раньше той, чьи
  списки покрывают те же домены (`uci reorder netshift.vk=<n>`).
- **DNS**: домены секции получают fakeip от sing-box; `domain_resolver_enabled=1` пускает
  резолв этих доменов (DoH) через сам туннель.
- **QUIC**: `disable_quic=1` в NetShift — хорошая идея и здесь: QUIC внутри WireGuard работает,
  но HTTP/3 часто медленнее TCP на таком пути.
- **Свой трафик клиента**: NetShift метит и **локальный** трафик роутера к fakeip и своим
  подсетям (`chain mangle_output`). Пока имена VK резолвятся в реальные адреса, это клиента не
  касается; `option fwmark '0x200000'` (метка исходящих sing-box, которую NetShift пропускает)
  гарантирует, что TURN-сокеты и запросы к API VK никогда не попадут в sing-box.
- **Интерфейс down**: outbound с `bind_interface` на интерфейс без адреса просто не
  устанавливает соединения; sing-box не падает. Резервирование `awg0` ↔ `vkturn` — средствами
  sing-box (`urltest`), см. [TODO.md](TODO.md).

## Firewall

Если трафик в туннель шлёт только sing-box (NetShift), зона не нужна: это исходящий трафик самого
роутера, адрес источника — адрес туннеля, `defaults.input=REJECT` закрывает вход. Если хотите
маршрутизировать LAN через интерфейс напрямую (`defaultroute 1` / `ip4table`), добавьте его в зону с
masquerade:

```sh
uci add_list firewall.@zone[1].network='vkturn'    # зона wan: masq=1, input REJECT
uci commit firewall && service firewall reload
```

## Безопасность

- Приватный ключ хранится в `/etc/config/network` (как у `proto wireguard`) и передаётся демону
  файлом 0600 в каталоге 0700; в argv и окружении демона его нет. netifd передаёт
  конфиг интерфейса обработчику аргументом — так же, как для `wireguard`/`amneziawg`.
- Кэш TURN-кредов: `/etc/vkturn/<iface>/creds.json` (0600, каталог 0700) — переживает ребут.
  Это логины и пароли TURN открытым текстом; `sysupgrade` их не сохраняет (каталог не в
  `keep.d`). Вариант без записи на флеш: `option cred_cache '/var/run/vkturn/<iface>/creds.json'`
  — после ребута креды перевыпускаются.
- В логах замаскированы токены VK, TURN-логин и пароль, токен сессии капчи.
- Переменные окружения `VK_*` (отладочные переключатели ядра) демон сбрасывает при старте.
- Слушающих сокетов нет.

## Сборка

```sh
openwrt/build.sh                      # rockchip/armv8, OpenWrt 25.12.4
TARGET=x86/64 openwrt/build.sh
```

Официальный SDK в Docker (sha256 сверяется с `sha256sums` релиза), Go собирается из исходников
фидом `packages/lang/golang`, модули сверяются с `go.sum` и checksum database. Пакеты и
`SHA256SUMS` — в `openwrt/out/<target>/`. Для фида OpenWrt Makefile пакета ссылается на
репозиторий по закреплённому коммиту (`PKG_SOURCE_VERSION`).

Тесты: `tools/test.sh` (или `go test ./cmd/... ./pkg/...`).
