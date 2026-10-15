# Проверки на роутере

Сценарии, которыми пакет проверялся на NanoPi R4S (OpenWrt 25.12.4, NetShift 0.9.7), — с командами,
ожиданием и замером. Повторять после значимых правок демона, обработчика или ядра.

Обозначения: `<router>` — роутер, `<server>` — VPS с `vk-turn-proxy -srtp` и WireGuard за ним.
На роутере нет `timeout`, `diff`, `nohup` — долгое запускать со сторожем
(`cmd & p=$!; (sleep 70; kill $p) &`).

## 0. Окружение

- `kmod-tun` и `/dev/net/tun` есть; `ip-full` не нужен.
- На сервере пир роутера: `wg set <wg-if> peer <router-pub> allowed-ips <tunnel-ip>/32` + `[Peer]` в конфиг.
- Публичный ключ роутера: LuCI показывает его в форме; или `vk-turn-proxyd pubkey < keyfile`.

## 1. Ручной прогон без netifd

Демон в отдельном каталоге 0700, конфиг 0600 (ключи `iface`, `peer`, `private_key`, `public_key`,
`vk_link`, `status_file`, `cred_cache` — см. `cmd/vk-turn-proxyd/config.go`):

```sh
vk-turn-proxyd -config ./config -check
start-stop-daemon -S -b -m -p ./pid -x /bin/sh -- -c "exec ./vk-turn-proxyd -config ./config > ./log 2>&1"
ip addr add <tunnel-ip>/24 dev <iface>             # вручную: без netifd адрес ставить некому
curl --interface <iface> https://ifconfig.me       # → IP сервера
start-stop-daemon -K -p ./pid -s TERM              # tun исчезает, статус удаляется
```

Замер: 20/20 соединений за ~2 с, handshake 0,2 с; ~21 Мбит/с вниз и вверх (`curl --interface`).
Повторный старт сразу после остановки — без «Allocation Quota Reached» (486).

## 2. Как интерфейс netifd

```sh
apk add --allow-untrusted vk-turn-proxy-*.apk luci-proto-vkturn-*.apk
service network restart                            # только после первой установки
# с NetShift — затем: rm -f /tmp/sing-box/rulesets/*-user-domains-ruleset.json && netshift restart
ubus call network get_proto_handlers | jsonfilter -e '@.vkturn.available'   # true
```

Секция `network.<iface>` (`proto vkturn`, адрес, ключи, ссылка, `defaultroute 0`, `peerdns 0`),
`ubus call network reload`. Ожидание:

- `ifstatus <iface>`: `up`, адрес из UCI; `ubus call vkturn status` — `state up`, `conns_active 20`;
- `logread -e vkturn` — строки демона, TURN-логин `<redacted>`;
- снимок `ip -4 route`, `ip -4 rule`, `ip route show table 105`, `/etc/resolv.conf`, `uci show dhcp`
  до и после: отличие — только подсеть туннеля (`… dev <iface> proto kernel`), `/32` нет;
- `ifdown` → демон пишет `stopped`, tun и `/var/run/vkturn/<iface>/*` исчезают; немедленный
  `ifup` → `up` за ~2 с, без 486;
- rpcd: `getPublicKey` верен, `status '{"interface":"../etc"}'` → отказ, мусор вместо ключа → отказ.

## 3. NetShift

Новая секция `connection_type=vpn`, `interface=<iface>`, свои домены (не входящие в списки других
секций — проверить `nslookup` на роутере: не fakeip до добавления). Ожидание:

- в `/etc/sing-box/config.json` outbound `{type: direct, bind_interface: <iface>}` и правило по
  `<section>-user-domains-ruleset`;
- с роутера (`curl https://<домен>`) и с LAN-клиента **без своего TUN-прокси** — выход с IP сервера,
  счётчик `/sys/class/net/<iface>/statistics/rx_bytes` растёт на объём загрузки; прочие домены — как
  раньше. Если сервер общий с другим выходом (AmneziaWG и т. п.), IP выхода ничего не доказывает —
  смотреть счётчик.

Замер: LAN-клиент, 100 МБ — ~31 Мбит/с; с роутера через sing-box — 15 и 32 Мбит/с (разброс по релею).

## 4. Отказы

| Сценарий | Как | Ожидание | Замер |
|---|---|---|---|
| Перезапуск WAN | `ifup wan` (со `setsid`, чтобы ssh не оборвал) | короткий провал, интерфейс не падает | 20/20 через 10 с |
| Мёртвый путь | своя nft-таблица: `table inet vkturn_test { chain out { type filter hook output priority -10; ip daddr { <relay-ips> } drop; meta l4proto { tcp, udp } th dport 19302 drop } }` | сессии вымирают, `down` (адрес снят), домены секции не открываются, мимо туннеля не утекают; после `nft delete table inet vkturn_test` — `up` | `down` на 174-й с; `up` через 6 с, 20/20 через ~50 с |
| `ifdown` при работающем NetShift | `ifdown <iface>`, `curl https://<домен секции>` | ошибка соединения (не домашний IP), sing-box жив | curl rc=35 |
| Ребут роутера | `reboot` | интерфейс сам поднимается, NetShift-правило на месте | `up` через ~1 мин |
| Рестарт сервера | `systemctl restart <server unit>` | трафик возвращается сам | ~2 мин (цикл rekey WireGuard) |
| Обновление пакета | `apk add` с новым `PKG_RELEASE` | `post-upgrade` перезапускает интерфейс | `up` через 2 с |
| Простой | 6 мин без трафика | ни одного ложного переподключения | макс. возраст handshake 125 с |

Blackhole-маршрут вместо nft-дропа не годится: у подключённых UDP-сокетов маршрут закеширован,
сессии почти не замечают.

## 5. Капча

`option simulate_captcha '1'` → статус `captcha_needed: true` со ссылкой, на странице Status → VK TURN
— предупреждение. В бою флаг ставит ядро, когда автосолвер не прошёл капчу; демон продолжает попытки.
