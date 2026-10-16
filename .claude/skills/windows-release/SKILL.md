---
name: windows-release
description: Сборка, проверка и выпуск Windows-приложения VK TURN (zip для x64), установка или обновление на ПК, разработка Windows-части на Mac. Использовать, когда просят «собрать для Windows», «выпустить Windows-версию», «обновить на ПК», «проверить на Windows».
---

# Windows-приложение: сборка, выпуск, разработка

Устройство и решения — [windows/README.md](../../../windows/README.md); ручной прогон на ПК —
[docs/windows-testing.md](../../../docs/windows-testing.md). Решения владельца: **только x64**, UX
«запустил — нажал Включить» (как Clash Verge / Happ), TUN через **wintun** (альтернативы — системный
прокси, gVisor — обсуждались и отклонены), живая проверка — на отдельном ПК владельца (Windows-VM
на Mac нет).

## Версия

`windows/VERSION` — поднимать при каждом выпуске: `vkturn.exe` из нового архива сравнивает себя с
установленным по sha256 и, если отличается, переустанавливается через UAC (это и есть обновление).
Изменили `cmd/vk-turn-proxyd` — ещё и `PKG_RELEASE` пакета роутера (правило CLAUDE.md).

## Сборка

```sh
windows/build.sh     # windows/out/vkturn-<ver>-windows-amd64.zip + SHA256SUMS
```

- Exe — Go хоста (`CGO_ENABLED=0 GOOS=windows GOARCH=amd64 -trimpath -mod=readonly`), `vkturn.exe` с
  `-H windowsgui`.
- `wintun.dll` — единственный чужой бинарник: официальный `wintun-0.14.1.zip` с wintun.net, sha256
  zip закреплён в скрипте (`07c25618…ef51`, опубликован на wintun.net), dll amd64 —
  `e5da8447…dafce`. Кэш — `windows/.cache/` (в .gitignore). **Грабли:** из этой сети соединение с
  wintun.net замирает после ~16 КБ — скрипт качает 15-секундными соединениями с докачкой (`-C -`);
  никогда не запускать две загрузки в один файл (так однажды получился битый zip — его поймал хэш).
- Перед выпуском: `go test -count=1 ./cmd/... ./pkg/... ./tools/hygiene/` и
  `for p in ./cmd/vkturn ./cmd/vk-turn-proxyd ./pkg/proxy; do GOOS=windows go vet $p; done`
  (то же делает `tools/test.sh`).

## Разработка на Mac

- Логика — в платформонезависимых файлах `cmd/vkturn/*.go` с тестами на `fakePlatform`
  (`fakeplatform.go`); полный цикл туннеля в `tunnel_test.go` гоняет **сам тестовый бинарник как
  демон** (`VKTURN_FAKE_DAEMON`). Windows-only — `*_windows.go` (сервис, установка, окно, трей,
  iphlpapi/NRPT): на Mac они только компилируются, поэтому любая правка там — `GOOS=windows go vet`.
- Раскладки структур iphlpapi проверяются на этапе компиляции (`netcfg_windows.go`,
  `tray_windows.go`): неверный размер или смещение — сборка падает. Не убирать.
- Окно без Windows: `go build -o /tmp/vkd ./cmd/vk-turn-proxyd && go run ./cmd/vkturn -demo -daemon /tmp/vkd`
  → `http://127.0.0.1:47447/login?t=demo` (сеть фальшивая; демон без root не стартует — видна ошибка).
- Формат `.srs` — `pkg/srs` (комментарий пакета — спецификация по исходникам sing-box); фикстуры
  тесты пишут сами (`pkg/srs/srstest`), настоящие списки MetaCubeX в репозиторий не класть.

## Установка и проверка на ПК

1. Перенести zip на ПК, распаковать, запустить `vkturn.exe` → UAC один раз → окно.
2. Подключение: ссылка `vkturn://` из панели (`issue-connection` в репозитории vk-turn-proxy) или
   временный пир на VPS: «Новый ключ» в окне → `wg set wg-turn peer <pub> allowed-ips 10.66.66.<свободный>/32`
   (только рантайм; master-сессию `ssh -MNf vps` поднимает пользователь), после проверки удалить
   пира. Пир, добавленный при уже крутящихся сессиях, — переподключить туннель. Ключ роутера не брать.
3. Пройти [docs/windows-testing.md](../../../docs/windows-testing.md); что нашлось — записать туда же
   (раздел «Что нашлось на ПК», по образцу android/README.md) с датой, а исправления — коммитами.
4. Журнал — вкладка «Журнал» в окне; сервис — «Просмотр событий → Система». Диагностика DNS:
   `Get-DnsClientNrptPolicy`, `Resolve-DnsName <имя> -Server 127.0.0.1`, `route print -4`.

## Состояние (2026-10-16)

Собрано и покрыто тестами на Mac; **на Windows ещё не запускалось**. Риски первого прогона — в конце
docs/windows-testing.md (NRPT на 127.0.0.1, порт 53, трей, замена status.json, PARAMCHANGE Dnscache).
