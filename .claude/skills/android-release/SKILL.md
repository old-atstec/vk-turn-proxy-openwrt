---
name: android-release
description: Сборка, проверка и выпуск Android-приложения VK TURN (APK), установка или обновление на телефоне либо эмуляторе. Использовать, когда просят «собрать APK», «выпустить приложение», «обновить приложение на телефоне».
---

# Android-приложение: сборка и выпуск

## Версия

Каждый выпуск, который ставится поверх старого, — `vkturnVersionCode+1` в `android/gradle.properties`
(Android не обновит приложение с тем же или меньшим кодом). `vkturnVersionName` — человеческая
версия, менять по смыслу.

## Сборка

```sh
android/build.sh             # демон (Go хоста) под 3 ABI + Gradle в контейнере; тесты + release APK
LOCAL=1 android/build.sh     # Gradle и ANDROID_HOME хоста вместо контейнера
ls android/out/              # vkturn-<версия>-{arm64-v8a,armeabi-v7a,x86_64,universal}.apk + SHA256SUMS
```

`build.sh` прогоняет unit-тесты (`testReleaseUnitTest`): `CidrTest`, `ProfileImportTest`. Красные —
не выпускать.

**Ключ подписи** — `android/keystore/release.jks` и `keystore.properties`. В git не класть, не
терять, не пересоздавать: APK с другой подписью не встанет поверх установленного (только через
удаление приложения вместе с настройками). Если `keystore/` нет — `build.sh` создаст **новый** ключ;
на чужой машине это почти всегда ошибка — спросите пользователя.

Если упало на `packageRelease` (бывает под эмуляцией amd64 при инкрементальной сборке):
`rm -rf android/app/build/intermediates/incremental/packageRelease android/app/build/outputs/apk/release`
и повторить.

## Установка и обновление

```sh
adb devices
adb shell getprop ro.product.cpu.abi                   # какой APK ставить
adb install -r android/out/vkturn-<версия>-arm64-v8a.apk   # -r — обновление с сохранением настроек
```

Пользователю без adb — `vkturn-<версия>-universal.apk` (подходит любому телефону), передать файлом.
Поверх старой версии ставится, если подпись та же и `versionCode` больше.

## Проверка импорта (без порчи настроек)

```sh
adb shell am start -a android.intent.action.VIEW -d '"vkturn://import?c=…"'
```

Должен открыться диалог «Импорт …» с сервером и адресом. На чужом или рабочем устройстве жмите
«Отмена»: «Подключить» и «Только сохранить» **заменяют** сервер, ключи и ссылки. Эталонная ссылка с
выдуманными ключами — в `ProfileImportTest.kt`.

Полный ручной прогон — [docs/android-testing.md](../../../docs/android-testing.md).
