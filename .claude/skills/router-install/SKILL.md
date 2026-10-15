---
name: router-install
description: Первая установка пакетов vk-turn-proxy и luci-proto-vkturn на роутер OpenWrt и настройка интерфейса vkturn (по ссылке из панели сервера или вручную). Использовать, когда просят «поставить на роутер», «настроить OpenWrt», «подключить роутер к серверу».
---

# Установка на роутер OpenWrt

Нужно: OpenWrt 25.12+ (apk), SSH на роутер, сервер с `-srtp` и, желательно, панель `vkturn-panel`
(репозиторий vk-turn-proxy, скил `issue-connection`).

SSH на роутер — по паролю через master-сессию, её поднимает **человек**:
`ssh -MNf root@<router>` (живёт ~час). Без неё не начинайте — попросите пользователя.

## 1. Архитектура и сборка

```sh
ssh root@<router> 'cat /etc/openwrt_release | grep -E "ARCH|RELEASE"'   # DISTRIB_ARCH='aarch64_generic'
ARCH=aarch64_generic openwrt/package-native.sh                           # секунды; Go ≥ go.mod и Docker
ls openwrt/out/native-aarch64_generic/                                   # 2 apk + SHA256SUMS
```

Частые `ARCH`: `aarch64_generic` (NanoPi R4S/R5S, RPi 4/5), `x86_64`, `arm_cortex-a7_neon-vfpv4`,
`mipsel_24kc` (MT7621). Эталонная сборка в SDK — `openwrt/build.sh` (на Apple silicon — часы).

## 2. Установка

```sh
scp openwrt/out/native-$ARCH/*.apk root@<router>:/tmp/
ssh root@<router> 'apk add --allow-untrusted /tmp/vk-turn-proxy-*.apk /tmp/luci-proto-vkturn-*.apk && rm /tmp/*vkturn*.apk /tmp/vk-turn-proxy-*.apk'
```

**Первая установка:** netifd узнаёт протокол только при старте. Нужен `service network restart`
(обрыв всех интерфейсов на несколько секунд) или ребут — **спросите пользователя**, когда можно.
Если на роутере NetShift — после рестарта сети:
`rm -f /tmp/sing-box/rulesets/*-user-domains-ruleset.json && netshift restart`.

## 3. Настройка интерфейса

Лучший путь — профиль из панели сервера (`vkturn://import?c=…`):

- **LuCI:** Network → Interfaces → Add new interface → имя `vkturn`, протокол VK TURN → поле
  **Import** → вставить ссылку → Import → Save & Apply;
- **SSH:** скрипт со страницы подключения в панели (кнопка «Скопировать скрипт») — вставить в
  консоль роутера. Он пересоздаёт `network.vkturn` и делает `ifup vkturn`.

Вручную — UCI из [openwrt/README.md](../../../openwrt/README.md#настройка); тогда публичный ключ
роутера (`vk-turn-proxyd pubkey`) надо добавить peer'ом на сервер.

Куда пускать трафик: через NetShift/sing-box (`interface=vkturn`) или в зону wan
(`openwrt/README.md`, раздел Firewall) — уточните у пользователя.

## 4. Проверка

```sh
ssh root@<router> 'ifstatus vkturn | grep -E "\"up\"|address"; ubus call vkturn status | grep -E "state|conns_active|last_error"; logread -e vkturn | tail -20'
```

Ожидаемо: `"up": true`, `state up`, `conns_active` ≈ `conns`; в журнале `proxy: first connection up`,
`wireguard: handshake in`. На сервере — свежий handshake у peer'а роутера в `wg show wg0`.

Больше сценариев — [docs/router-testing.md](../../../docs/router-testing.md).
