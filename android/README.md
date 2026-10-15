# VK TURN для Android

Тот же туннель, что и на роутере (WireGuard → DTLS-SRTP → TURN-релеи звонков VK → сервер
`anton48/vk-turn-proxy -srtp`), как VPN-приложение Android с **раздельным туннелированием**:
в туннель идут все приложения, только выбранные или все, кроме выбранных.

## Как устроено

Движок — сам демон роутера `cmd/vk-turn-proxyd`, собранный под `GOOS=linux` для трёх ABI
(arm64-v8a, armeabi-v7a, x86_64) и упакованный в APK как `lib/<abi>/libvkturnd.so`: Android даёт
приложению запускать файлы только из `nativeLibraryDir`. Приложение (Kotlin, без AndroidX) запускает
его как дочерний процесс в «режиме приложения» (`cmd/vk-turn-proxyd/app.go`):

| Что | Роутер | Android |
|---|---|---|
| tun | демон создаёт сам | `VpnService` создаёт и передаёт fd через unix-сокет (`tun_fd_socket`) |
| когда поднимается | хук `up` → netifd ставит адреса | демон просит tun после первой TURN-сессии — до этого трафик идёт как шёл |
| смена сети | `/proc/net/route` | `ConnectivityManager` → `network <id>` / `network none` в stdin (`network_events=stdin`) |
| DNS для запросов к VK | dnsmasq / `dns_server` | DNS текущей сети → `dns <ip>` в stdin; запасной — в настройках |
| свои сокеты мимо туннеля | `fwmark` / `bind_device` | uid приложения всегда вне VPN — `protect()` не нужен |
| остановка | SIGTERM | закрытие stdin (демон отдаёт TURN-аллокации) |
| статус | `/var/run/vkturn/<iface>/status.json` | `files/run/status.json` — экран и уведомление |

### Раздельное туннелирование

Режим и список — `VpnService.Builder.addAllowedApplication` / `addDisallowedApplication`:

- **Все приложения** — в туннеле всё, кроме самого VK TURN;
- **Только выбранные** — в туннеле только отмеченные приложения;
- **Все, кроме выбранных** — отмеченные идут напрямую.

Дополнительно по адресам: «Маршруты в туннель» (по умолчанию `0.0.0.0/0`) и «Локальные сети —
мимо туннеля» (10/8, 172.16/12, 192.168/16, 169.254/16, 224/4 вычитаются из маршрутов).

IPv6 приложениям в туннеле Android блокирует (у VPN нет IPv6-адреса и маршрута) — утечки мимо
туннеля нет, приложения уходят на IPv4.

## Настройка

На сервере — пир WireGuard с публичным ключом телефона (кнопка «Новый ключ» создаёт пару, «Копировать
публичный» — ключ для сервера). В приложении:

- адрес сервера `host:port` (листенер `-srtp`) и его публичный ключ;
- адрес в туннеле (как `Address` пира, например `10.66.66.2/32`) и DNS в туннеле;
- одна или несколько ссылок на звонки VK — как `list vk_link` на роутере;
- число соединений (20 по умолчанию; замеры — [openwrt/TODO.md](../openwrt/TODO.md)).

Ключи и ссылки хранятся в приватных настройках приложения (`allowBackup=false`); демону они
передаются файлом `files/vkturn.conf` с правами 0600.

Капча: как и на роутере, интерфейса для неё нет — при `captcha_needed` экран показывает ссылку.

Always-on VPN поддерживается (Настройки → Сеть → VPN → VK TURN).

## Сборка

```sh
android/build.sh            # APK в android/out/: по одному на ABI и универсальный, SHA256SUMS
LOCAL=1 android/build.sh    # gradle и ANDROID_HOME хоста вместо контейнера
```

Демон собирается Go хоста (`CGO_ENABLED=0`, `-trimpath`, `-mod=readonly` — как пакеты роутера),
APK — Gradle в контейнере `android/Dockerfile` (JDK 17, Gradle 8.11.1, Android SDK 35; загрузки
проверяются по sha256; при сборке образа принимаются лицензии Android SDK). Образ — `linux/amd64`:
у `aapt2` нет сборки под Linux arm64, на Apple silicon он идёт через эмуляцию Docker Desktop.

Подпись: при первом запуске создаётся `android/keystore/release.jks` и пароль к нему
(`keystore.properties`). **Сохраните оба**: APK с другой подписью не встанет поверх установленного.
В git они не попадают.

Версия приложения — `vkturnVersionName` / `vkturnVersionCode` в `android/gradle.properties`;
поднимайте `vkturnVersionCode` при каждом выпуске.

## Ограничения

- `GOOS=android` без cgo не собирается (внешняя линковка, `wlynxg/anet`), поэтому демон — статический
  Linux-бинарь: сертификаты он берёт из `/system/etc/security/cacerts` (`SSL_CERT_DIR`), резолвер —
  из того, что передаёт приложение.
- Только IPv4 внутри туннеля (как и на роутере: `allowed_ip=0.0.0.0/0`).
- Проверено сборкой и тестами демона; на устройстве — см. чек-лист в
  [docs/android-testing.md](../docs/android-testing.md).
