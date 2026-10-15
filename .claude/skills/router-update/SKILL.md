---
name: router-update
description: Обновление пакетов vk-turn-proxy / luci-proto-vkturn на роутере OpenWrt после изменений в коде, переустановка после sysupgrade, удаление. Использовать, когда просят «обновить роутер», «выкатить на роутер», «после прошивки пропал vkturn».
---

# Обновление на роутере

## 1. Версия

Любое изменение содержимого пакета — `PKG_RELEASE+1` в его Makefile, иначе `apk add` сочтёт версию
той же и ничего не обновит:

| Что менялось | Makefile |
|---|---|
| `cmd/vk-turn-proxyd`, `pkg/…`, `openwrt/vk-turn-proxy/files/…` | `openwrt/vk-turn-proxy/Makefile` |
| `openwrt/luci-proto-vkturn/…` | `openwrt/luci-proto-vkturn/Makefile` |

```sh
go test -count=1 -timeout=10m ./cmd/... ./pkg/... ./tools/hygiene/   # красное не выкатывать
ARCH=<arch> openwrt/package-native.sh
```

## 2. Выкатить

SSH-сессию на роутер поднимает человек (`ssh -MNf root@<router>`).

```sh
scp openwrt/out/native-$ARCH/*.apk root@<router>:/tmp/
ssh root@<router> 'apk add --allow-untrusted /tmp/vk-turn-proxy-*.apk /tmp/luci-proto-vkturn-*.apk; rm -f /tmp/vk-turn-proxy-*.apk /tmp/luci-proto-vkturn-*.apk'
```

`post-upgrade` сам перезапускает работающие интерфейсы `vkturn` (~2 с, TURN-аллокации старого
демона освобождаются). `service network restart` при обновлении **не нужен**. LuCI: после
обновления `luci-proto-vkturn` — обновить страницу с очисткой кэша (Ctrl+Shift+R).

## 3. Проверить

```sh
ssh root@<router> 'vk-turn-proxyd version; ubus call vkturn status | grep -E "version|commit|state|conns_active"'
```

Версия и коммит — новые, `state up`.

## После sysupgrade

Прошивка сохраняет конфиги (секция `network.vkturn` на месте), но не доустановленные пакеты.
Поставить оба apk заново (шаг 2) — интерфейс поднимется сам. Если `apk add` ругается на
зависимости — сначала `apk update`.

## Удаление

```sh
ssh root@<router> 'apk del vk-turn-proxy luci-proto-vkturn'
```

Пакеты сами опускают интерфейсы, удаляют `/var/run/vkturn` и кэш кредов `/etc/vkturn`. Секцию
`network.vkturn` удалить вручную (`uci delete network.vkturn; uci commit network`) — только с
согласия пользователя.
