# Packaging

Этот каталог содержит установщик, systemd/sysusers/tmpfiles assets, сборщик release и проверочные сценарии. Запуск установки или host harness меняет сервер; чтение документации таких действий не выполняет.

- [Установка](../../docs/INSTALLATION.md): GitHub release, исходники, первичная настройка и обновление.
- [Выпуск релиза](../../docs/RELEASING.md): CI, assets и сборка.
- [T8 на сервере](T8_MANUAL_SERVER_TEST.md): check, smoke, post-reboot и ручные этапы.
- [Проверки проекта](../../docs/TESTING.md): область действия результатов.
- [Native contract](../docs/NATIVE_TEMPLATES.md): валидация и восстановление.

`install-bundle.sh` принимает `--prebuilt`, `--with-web`, `--without-web` и `--web-port PORT` для первой установки. Сеть настраивается отдельным диалогом `xray-core-helper setup`. Флага выбора LAN-интерфейса у установщика нет. В режиме `--prebuilt` скрипт передаёт управление `bin/xray-core-helper install --bundle ROOT`: состояние операции сохраняется, setup не перезапускается, ручной `npm ci` не нужен. Node/npm всё равно должны быть установлены для web-панели. Исходная shell-сборка остаётся инструментом разработки и не является U6 updater.

`install-from-github.sh` — тонкий bootstrap для публичного GitHub: определяет Linux amd64/arm64, скачивает raw CLI выбранной версии и общий `SHA256SUMS` (два архива и два raw CLI), проверяет SHA256 и запускает `install`. `gh`, токен, Go и распаковка архива не требуются; Node/npm нужны только для web-панели. `--version` фиксирует точную версию (новее, старше или текущую); `--recover` не использует сеть, проверяет сохранённый root-private installer в `/var/lib/xray-core-helper-update` и запускает `install --recover`. API-путь сохраняет проверенную копию текущего installer, подготавливает (stage) целевой архив и использует эту копию для worker; bootstrap требует raw CLI целевой версии.

Обновление: `xray-core-helper update check|status|prepare [VERSION]`, затем явные `apply|retry|rollback OPERATION_ID ACTION_ID`. Автоматического повтора/отката нет. Основная часть U6 реализована локально; реальные два release, upgrade/downgrade и восстановление после сбоя остаются U6.12/T8 и требуют серверной приёмки.

`build-release.py` собирает только committed snapshot. Не используйте незакоммиченный рабочий каталог как источник опубликованной версии. Он создаёт два архива, два raw CLI и `SHA256SUMS` из четырёх строк. `release.json` schema1 содержит необязательное `minimum_installable_version` (из `release-policy.json`); без него нижней границы нет.

`sudo python3 packaging/collect-server-audit.py` дополняет аудит сервера
привилегированными read-only данными: загруженный nft ruleset, policy routing,
состояние служб/таймеров, расписания cron root и системы. Ничего не применяет,
не перезапускает и не запускает задания cron. Содержимое Xray config, БД и логов
не копирует; команды cron представлены расписанием, executable и SHA256.
Отчёт сохраняется в новом `/var/tmp/xray-server-audit-*` с правами 0700/0600;
при запуске через sudo передаётся вызывающему пользователю. Это приватные данные
сети, а не готовый публичный отчёт. Недоступные команды отмечаются отдельно.
