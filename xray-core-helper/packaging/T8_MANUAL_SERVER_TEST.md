# T8: проверка установленного сервера

Процедура предназначена для отдельного тестового сервера. Она проверяет установленную версию; не переустанавливает helper, не принимает чужую конфигурацию автоматически и не перезагружает систему.

Сначала выполните [установку и setup](../../docs/INSTALLATION.md). Общая матрица, история результатов и незакрытые сценарии — в [TESTING](../../docs/TESTING.md). Успешный T8 не заменяет проверку с LAN-клиента.

## Условия

- Linux с systemd, Xray и настроенным helper; для текущего общего runner нужна также установленная web-панель.
- Python 3.9+, curl, jq и системные инструменты установленного bundle.
- Независимая консоль и возможность вручную перезагрузить сервер.
- Нет pending safe-apply, recovery или непросмотренных изменений шаблонов.
- Используется runner из того же проверяемого bundle/commit, с актуальными исправлениями метаданных и `completedGeneration`.
- Установка должна оставаться неизменной между smoke и post-reboot.

Runner получает эффективные пути Xray и socket через CLI. Нельзя подменять его discovery временным копированием файлов в стандартные каталоги.

Для отдельной приёмки чистой установки нужны независимые свежие состояния для CLI/web и существующего/нового конфига. Наличие Xray или его конфига допустимо; наличие прежнего состояния helper — уже не чистая установка. `uninstall-bundle.sh` сохраняет данные и не является сбросом сервера.

## 1. Preflight

В каталоге `xray-core-helper` из bundle:

```bash
sudo /usr/local/bin/xray-core-helper safe-apply status
sudo /usr/local/bin/xray-core-helper setup --status
sudo python3 packaging/t8-server-test.py check
```

Ожидаются `pending: null`, завершённый setup и PASS всех проверок. Runner печатает приватный каталог `audit=/var/backups/xray-T8.…` и путь JSON-отчёта. `--start-services` позволяет явно запустить установленные helper/web перед check или smoke; по умолчанию runner их не запускает.

Проверяются services, health, socket permissions, recovery, ready, история ошибок,
сохранённые результаты сетевых проб, согласованность Xray для подготовленной сети,
templates, confinement, sysctl dry-run и web HTTP. Проверки истории используют
только GET API: пустая история допустима, сбор ошибок и сетевые пробы не запускаются.
Набор проверок развивается вместе с runner; не привязывайте клиент к фиксированному числу строк PASS.

## 2. Smoke

Сначала локально просмотрите рабочий sysctl-шаблон и его системный файл. Следующая команда **изменяет тестовый сервер**:

```bash
sudo python3 packaging/t8-server-test.py smoke --reviewed-sysctl
```

Runner выполняет sysctl apply/confirm и apply/rollback, сохраняет baseline всей группы nftables/sysctl/unit и метаданные файлов, проверяет сохранность recovery после перезапуска helper. Откат system-файлов требует последующей ручной перезагрузки. PASS на этом этапе означает готовность продолжить процедуру.

Сохраните напечатанный audit path; для post-reboot нужен именно audit успешного smoke. Не используйте каталог неуспешного preflight.

## 3. Ручная перезагрузка

```bash
sudo reboot
```

После возвращения на сервер проверьте новый boot ID:

```bash
cat /proc/sys/kernel/random/boot_id
uptime -s
```

В WSL перезапуск systemd/дистрибутива может сохранить ядро и boot ID. Если runner сообщает `Boot ID unchanged`, сравните текущий ID с `bootId` в audit/state.json. При необходимости полностью остановите WSL из Windows и запустите снова. Способ перезапуска не заменяет проверку boot ID; нельзя переписывать сохранённый ID, чтобы пройти gate.

## 4. Post-reboot

Восстановите каталог bundle после входа или укажите **полный фактический путь** к тому же runner. Подставьте audit из smoke:

```bash
cd /path/to/bundle/xray-core-helper
sudo python3 packaging/t8-server-test.py post-reboot \
  --audit /var/backups/xray-T8.REPLACE_WITH_SMOKE_AUDIT
```

Ожидается PASS всех проверок и `report-post-reboot.json`. Runner проверяет baseline, отсутствие pending recovery и fingerprint рабочего шаблона, возвращает исходный рабочий sysctl-шаблон и повторяет check. Fingerprint включает содержимое, mode, uid и gid; одинакового SHA256 недостаточно.

## Остановка и диагностика

Любой STOP требует разобраться до следующего изменения. Сохраняйте audit приватным: в нём есть резервные копии и состояние операций. Не публикуйте весь каталог.

Полезные безопасные чтения; замените путь audit:

```bash
sudo /usr/local/bin/xray-core-helper safe-apply status
sudo jq '{httpStatus, checks: .body.checks}' \
  /var/backups/xray-T8.REPLACE/ready.json
sudo jq '{templates: [.body.templates[]? | {templateId, state, summary}]}' \
  /var/backups/xray-T8.REPLACE/templates.json
```

- `templates_different`: проверьте реальные различия, включая метаданные. После внешнего редактирования выполните явный refresh, как описано в [эксплуатации](../../docs/OPERATIONS.md).
- `invalid_block_markers`: остановитесь и просмотрите native-блоки. Не заменяйте системный nftables заводским примером поверх действующей сети.
- HTTP 409: прочитайте JSON соответствующего шага. `template_generation_conflict` требует проверки версии runner и `completedGeneration`, а не ручной правки базы.
- `Working template changed`: сравните sha256/mode/uid/gid с fixture из state.json. Не исправляйте audit или права вслепую. Установите причину изменения.
- Pending/partial restore: выполняйте [процедуру восстановления](../../docs/OPERATIONS.md). Не удаляйте backup, journal или recovery state.

## Отдельная ручная матрица

Для полной приёмки дополнительно фиксируются результаты:

1. TCP/UDP с реального LAN-клиента, серверный HTTPS, bypass и отсутствие зацикливания.
2. Preview, confirm и timeout для nftables и unit на disposable host.
3. Crash helper, повторное восстановление, частичный отказ записи и сохранность baseline.
4. CLI и browser setup, прямой и настроенный proxy outbound.
5. Работа data plane при остановленных helper/web.
6. Открытие всех разделов панели, stale/conflict/error состояния и отображение recovery.
7. Внутренний планировщик сбора ошибок и сохранение последних 100 записей после перезапуска/reboot:
   одинаковые сообщения остаются отдельными записями с собственным временем.
8. Обновление геобаз с установленным сборщиком, пустым и непустым динамическим
   списком; завершённая консолидация и работоспособность Xray после обновления.

В CLI и web setup проверить пробы шлюза/HTTPS до и после включения TProxy,
сохранение результатов с transaction ID, повтор только пробы, ручной confirm
при неуспешной проверке и восстановление по timeout. Успех HTTPS не доказывает
LAN forwarding; автоматического подтверждения нет. Отдельная
[команда проверки сайта](../../docs/OPERATIONS.md) доступна для диагностики.
Записывайте дату, bundle/commit, область теста и audit в общий журнал результатов
TESTING, не создавая отдельный отчёт на каждый этап.
