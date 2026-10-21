# Helper API

Версионированный HTTP API доступен только через Unix socket. Это справочник групп операций; точные структуры запросов и ответов находятся в `internal/api`, маршруты — в `internal/httpapi`. CLI и web используют тот же API.

## Транспорт и ограничения

Путь по умолчанию: `/run/xray-core-helper/helper.sock`; фактический путь берётся из настроек установленного helper. Пример чтения:

```bash
sudo curl --silent --show-error \
  --unix-socket /run/xray-core-helper/helper.sock \
  http://helper/v1/ready | jq
```

Внешнего TCP listener нет. Доступ к socket означает доверенный доступ к API; полноценное разграничение клиентов по ролям пока не реализовано. Ответы не содержат исходный конфиг, пароли или ключи.

JSON-запросы имеют ограниченный размер, неизвестные поля отклоняются. Ошибка возвращается как `{"error":{"code":"...","message":"..."}}`; клиент должен проверять HTTP status и машинный `code`.

## Чтение и первичная настройка

| Endpoint | Назначение |
| --- | --- |
| `GET /v1/health` | Доступность процесса и версия |
| `GET /v1/ready` | Готовность компонентов; HTTP 503 при degraded |
| `GET /v1/setup` | Долговременное состояние первичной настройки |
| `GET /v1/setup/interfaces` | Реальные интерфейсы для выбора оператором |
| `POST /v1/setup/plan` | Подготовка плана без включения сети |
| `POST /v1/setup/apply` | Применение выбранного плана |
| `POST /v1/setup/adopt` | Явное принятие существующей установки |
| `POST /v1/setup/recover` | Восстановление прерванной настройки |
| `GET /v1/web/settings` | Параметры web listener/control и `exporterURL` |

Во время первичной настройки работает ограниченный набор setup/web API. Нельзя считать обычные конфигурационные endpoints доступными до завершения настройки. Web bootstrap использует временный приватный token; после передачи управления он отзывается. Standalone setup (`xray-core-helper setup`) требует root, останавливает helper/web, берёт runtime lock и выполняет настройку в процессе без запросов к этому socket; `setup --recover` работает в том же автономном режиме, а `setup --status` остаётся обычным чтением через socket.

## Конфигурация Xray и операции

Чтение: `/runtime`, `/status`, `/config/summary`, `/inbounds`, `/outbounds`, `/routing`, `/geodata`, `/exporter-metadata` под общим префиксом `/v1/xray`.

Типизированные изменения охватывают logging, поддерживаемые outbounds и правила маршрутизации. Также предоставляются операции проверки конфига, диагностики, чтения журналов, connectivity probe и разрешённых действий над сервисами. Смотрите реализации `mutations.go`, `operations.go` и соответствующие структуры API перед написанием клиента.

Изменения JSONC используют `expectedGeneration` и контроль content hash. Конфликт поколения или внешнее редактирование — повод перечитать состояние и пересмотреть действие. Нельзя автоматически повторять запись с новым поколением. Явное принятие внешнего конфига выполняется через `POST /v1/xray/config/adopt` после проверки оператором.

Не все операции используют поколение конфигурации: setup, probe, refresh, сервисные действия и safe-apply имеют собственные контракты. Нельзя добавлять `expectedGeneration` ко всем запросам механически.

### Управление службами и диагностика

Allowlisted start/stop/restart/status выполняют соответствующее действие systemd
без предварительного gateway gate. Отдельное действие `enable` удалено:
routing всегда запускается hook службы Xray, собственного автозапуска у него нет. Ошибки native-команд передаются
через ограниченные ошибки service-control; детали этапа доступны в журнале unit.

Явный CLI `network-check` проверяет service/config/bindings/socket/API один раз,
возвращая `xray_gateway_not_ready: <stage>` (например, `config_validation`,
`tproxy_tcp_listener`, `api_unavailable`). Исходный конфиг и вывод Xray не
попадают в ошибку. Проверки mtime после запуска нет; диагностика не является
условием старта и не проверяет LAN forwarding.

Явный safe-apply TProxy-unit перезапускает Xray после daemon-reload и применения
sysctl/nft; строгий post-start Xray запускает routing. Это изменение работы сети,
о котором CLI предупреждает перед активацией. Перед переходом оператор вручную
отключает прежний независимый autostart routing без её остановки; см.
[эксплуатацию](../../docs/OPERATIONS.md#переход-с-прежнего-helper-зависимого-профиля).

### Настройки журналов

`PUT /v1/xray/logging` согласует четыре поля Xray с `[logging]` helper TOML.
Режим `file` сохраняет путь из TOML; при предыдущем journal/disabled использует
`/var/log/xray/access.log` или `/var/log/xray/error.log`. Произвольные пути в
HTTP-запросе не принимаются. Последующий template apply использует сохранённые
значения. Частичная запись TOML/Xray восстанавливается по приватному журналу
после штатного восстановления конфигурационной транзакции.

Перед записью helper проверяет основные ограничения systemd обеих служб.
Каталог вне доступной области или невозможность определить ограничения дают
HTTP 409 `log_path_confirmation_required`; состояние при этом не меняется.
После показа предупреждения клиент может повторить тот же запрос с тем же
`expectedGeneration` и `confirmRestrictedPaths: true`. Отмена не повторяет запись.
Это согласие не меняет unit/drop-in, права службы и sandbox.

При подтверждённом ограничении helper не пытается подготовить файлы,
недоступные ему по проверенным ограничениям; их подготовка остаётся ручной.
Остальные файловые назначения подготавливаются для effective UID/GID Xray;
существующим родительским каталогам права не расширяются. Валидация проверяет
реальный кандидат, без подмены путей. Её отказ, конфликт поколения и сбой записи
сохраняют обычное поведение транзакции.

Если после такого подтверждения запрошенный restart Xray завершился ошибкой,
настройки TOML и Xray остаются сохранёнными. Ответ: HTTP 503
`logging_saved_restart_failed`. Это **не откат**: клиент перечитывает состояние
и сообщает о необходимости ручного исправления доступа. При обычном изменении
без выявленного ограничения сохраняется прежний откат при отказе restart.
Проверка systemd даёт предупреждение, а не гарантию записи в любой конфигурации
системы. Ротация управляет существующей установкой logrotate Xray и не создаёт
новую политику для произвольных каталогов.

### Динамические домены и IPv4

Proxy-домены и geodata имеют отдельные группы endpoints. Конфигурация geodata и
разрешённые источники принадлежат helper.

| Endpoint | Назначение |
| --- | --- |
| `GET /v1/proxy-domains` | Снимок `desired`, `baseline`, `consolidationActive` |
| `POST /v1/proxy-domains` | `{"domain":"example.com"}`; домен или IPv4 |
| `DELETE /v1/proxy-domains/{value}` | Удалить/исключить домен или IPv4 |
| `POST /v1/proxy-domains/apply` | Явно применить текущий desired к runtime |
| `POST /v1/proxy-domains/reconcile` | Восстановить overlay после нового запуска Xray |

Имена endpoint, поле `domain` и массивы `desired`/`baseline` сохранены. `domain`
— имя единственного поля запроса, но значение принимается как домен или IPv4;
отдельного выбора типа нет. Оба типа находятся в одном списке и одной
SQLite-хранилище `desired`/`baseline`; второго хранилища, миграции схемы и импорта
старых списков нет. Массивы снимка содержат нормализованные значения в
лексикографическом порядке.

Helper нормализует домен (пробелы, регистр, конечная точка) и IPv4
(каноническая запись), поэтому `" Example.COM. "` возвращается как `example.com`,
а `192.0.2.1` — как `192.0.2.1`. POST и DELETE используют один классификатор;
повторные add/delete идемпотентны. Пустой ввод, IPv6, CIDR, URL, IPv4 с портом,
некорректный IPv4 и строки только из цифр и точек, не являющиеся корректным
IPv4, отклоняются до записи и обращения к Xray: HTTP 400 `invalid_domain`. IPv4
никогда не получает префикс `domain:`.

Успешная мутация возвращает `{"domain":"<каноническое значение>","applied":true}`.
`applied` относится к runtime overlay, а не к USERLIST: `true` подтверждает
применение overlay; если клиент получил `false`, это сохранённый desired без
применения, и успех runtime не утверждается. Клиент не подменяет отсутствующие
массивы пустым списком и проверяет структуру ответа. `consolidationActive`
показывает замороженный снимок пары.

Runtime additions — `desired - baseline`. Каждый домен/IPv4 имеет отдельный
стабильный тег `helper-proxy-resource-<sha256>` и добавляется в конец через
`adrules --append`; лишние owned tags удаляются через `rmrules`. Статические
правила имеют приоритет и не пересобираются. Непоменявшиеся ресурсы не удаляются
и не добавляются заново. Для IP additions требуется единственное постоянное
`proxy-ip-permanent` после `proxy-domains-permanent`; JSONC не исправляется
автоматически. Удаление baseline-записи сохраняется в desired, но продолжает
действовать через USERLIST до следующего обновления баз: direct exclusions нет.
Ошибки частичной синхронизации не откатывают desired; следующий apply сравнивает
фактические теги и завершает работу без дубликатов. Повтор append после потерянного
ответа не выполняется вслепую: сначала проверяется наличие новых тегов.

Повтор apply строит тот же overlay без дубликатов. `POST
/v1/proxy-domains/reconcile` с пустым body или `{}` проверяет текущий systemd
InvocationID Xray и восстанавливает overlay только после смены запуска / первого
вызова после запуска helper. Ответ `{"applied":true}` означает успешное
восстановление; `false` — текущий запуск уже обработан. Ошибка не запоминается
как успех. Внутренний планировщик helper вызывает reconcile раз в 30 с через тот
же API, поэтому после restart Xray overlay восстанавливается и без ручного
запроса. CLI: `proxy-domain reconcile`. Операция проходит общий coordinator и
recovery gate; во время consolidation overlay не применяется. Сбой
восстановления возвращает HTTP 503 `domain_runtime_reconcile_failed` и
повторяется следующим тиком. Неудачный runtime apply также повторяется
при прежнем InvocationID.

`geodata update` собирает домены в `[geodata].user_geosite` (default
`geosite_user.dat`, USERLIST), IPv4 — в `[geodata].user_geoip` (default
`geoip_user.dat`, USERLIST, /32). Сборка и скачивание внешних баз независимы;
ошибка файла не отменяет допустимый результат остальных. У каждого процесса
свой общий Xray test. После замен — один restart; без замен restart отсутствует.
После нового runtime активируется baseline успешно загруженных файлов и
восстанавливается overlay. Desired остаётся доступным; API-сбой повторяет reconciler.
Неуспешный restart — ошибка шлюза без отката файлов. Ошибки desired/runtime
возвращают прежние `domain_runtime_sync_failed`/`domain_runtime_apply_failed`;
клиент перечитывает список вместо автоматического повторного add/delete.

CLI сохраняет команду `proxy-domain`; `list` печатает JSON снимка, `add`/`delete`
принимают оба типа. Активация нового заводского Xray-шаблона в release остаётся
в S7 [плана](../../docs/ROADMAP.md); серверная приёмка выполняется в T8, а не
локальными контрактными тестами.

## Самообновление комплекта

| Endpoint | Назначение |
| --- | --- |
| `GET /v1/update` | Текущая версия/commit и публичная запись операции (`operation` может быть `null`) |
| `POST /v1/update/check` | Проверить последнюю версию в публичном GitHub; тело `{}` |
| `POST /v1/update/prepare` | `{"version":"helper-vX.Y.Z"}`; пустая версия — последняя |
| `POST /v1/update/apply` | `{"operationId":"...","actionId":"..."}` |
| `POST /v1/update/retry` | То же; повторить сохранённую операцию |
| `POST /v1/update/rollback` | То же; восстановить согласованный снимок |

ID операции и ID попытки — 32 lowercase hex. Состояния: `preparing`, `prepared`,
`launching`, `applying`, `verifying`, `succeeded`, `failed`, `interrupted`,
`rolled_back`. Запросы декодируются строго, с ограничением 4 KiB; неизвестные
поля и лишнее содержимое отклоняются.
`prepare` принимает только `version`; `apply`/`retry`/`rollback` — только
`operationId` и `actionId`. `check` возвращает `currentVersion`, `currentCommit`,
`latestVersion`, `updateAvailable` и `comparison` (`known`/`unknown`); ошибка
GitHub не превращается в «обновлений нет». Draft исключаются, выбирается
максимальная SemVer с prerelease; явная версия может быть новее, старше или
совпадать с установленной, если это допускает `minimum_installable_version`.
Нижняя граница — необязательное поле schema1
`/usr/share/xray-core-helper/release.json`; без поля ограничения нет. Проверка
выполняется до замены файлов и без daemon. Отсутствующая metadata не заменяется
выдуманной версией.

`prepare` скачивает и проверяет архив, manifest, SHA256, ELF architecture и
web-зависимости, не останавливая службы. API-путь сохраняет проверенную копию
текущего installer, подготавливает (stage) целевой архив и использует эту копию
для worker. Набор установленных компонентов не меняется: наличие web
определяется `helper.env`. Bootstrap, наоборот, требует raw CLI целевой версии.
`apply` запускает worker через временную `systemd-run`-службу; worker
останавливает helper/web, сохраняет снимок, заменяет managed files, запускает
новую версию и проверяет health/панель. Обновление не запускает setup, не меняет
Xray config/сеть и не перезапускает Xray/TProxy.

`apply` допустим только из `prepared`; `retry`/`rollback` — после `failed` или
`interrupted`, rollback требует готового снимка. Автоматического повтора и
автоматического отката нет. Повтор с тем же `actionId` идемпотентен; новый ID
создаёт новую попытку. `retry` фиксирован на target version, использует
сохранённый кандидат/снимок и не выбирает более новый release; первоначальный
снимок не затирается. Rollback возвращает файлы и согласованные данные как пару
и не является обычным downgrade. Журнал операции, installer, кандидат и снимок
хранятся в `/var/lib/xray-core-helper-update`, вне восстанавливаемых каталогов;
публичная запись операции не содержит fingerprints, digest и пути конфигурации.

Обновление сохраняет настройки и данные в `/etc/xray-core-helper` и
`/var/lib/xray-core-helper` (TOML, SQLite, backups, дополнительные пользовательские
файлы, admin drop-in) и наличие установленной
панели; заменяет четыре поставляемых рабочих шаблона в `/etc/xray-core-helper/templConf`,
заводской `/usr/share/xray-core-helper/templConf`, managed
units и, если панель установлена, `/opt/xrayui-web`. Собственный drop-in
`20-installation-paths.conf` пересоздаётся. Обновлённые шаблоны не применяются к
системным файлам автоматически: пользователь рассматривает расхождения и выбирает
apply/accept-system. Откат обновления возвращает предыдущие рабочие шаблоны. При `preparing`/`launching`/
`applying`/`verifying`, а также после `failed`/`interrupted` обычные изменяющие
запросы получают 409 `update_pending`; чтение разрешено, состояние `prepared`
само по себе изменений не блокирует. После прерывания daemon обслуживает
минимальный recovery API: `/v1/health` (`recovery_required`), `/v1/web/settings`,
`/v1/update`; остальные запросы получают 503 `update_recovery_required`.

CLI: `xray-core-helper update check|status|prepare [VERSION]`,
`update apply|retry|rollback OPERATION_ID ACTION_ID`. Если daemon недоступен,
bootstrap `--recover` использует сохранённый installer офлайн.

## История ошибок

- `GET /v1/errors?limit=5` — последние отдельные ошибки, newest first. По умолчанию
  100, допустимо 1–100; другие query-параметры отклоняются.
- `POST /v1/errors/collect` с `{}` — один ограниченный сбор настроенного источника;
  возвращает последние пять. Query-параметры запрещены. Операция наблюдения
  доступна и при блокировке изменений до reboot; Xray/сеть не меняет.

Ответ содержит `errors`, `capacity`, `source`, `collectedAt`, `collectionError`,
`truncated`. Запись содержит `id`, `time` (если известно), `observedAt`, `source`,
`message`. Одинаковый текст разных событий не агрегируется. Буфер в SQLite:
100 записей без TTL, только вытеснение новыми; checkpoint не дублирует повторное
чтение. URI и распознаваемые секретные записи скрываются до сохранения.
Сбой сбора не удаляет историю. Нормальный сбор запускает внутренний планировщик helper
каждые 20 с, а не systemd timer; это ограниченное окно источника, а не полный
архив журнала.

CLI: `xray-core-helper errors` и `xray-core-helper errors collect`.
Во время незавершённого setup обычный API истории ещё недоступен.

## Native templates

| Endpoint | Назначение |
| --- | --- |
| `GET /v1/templates` | Сохранённая проекция статуса четырёх шаблонов |
| `POST /v1/templates/refresh` | Явно перечитать файлы; пустое тело |
| `GET /v1/templates/{id}/backups` | История и поколения шаблона |
| `POST /v1/templates/{id}/accept-system` | Принять системный файл как рабочий шаблон |
| `POST /v1/templates/{id}/apply` | Применить рабочий шаблон |
| `POST /v1/templates/{id}/backups/{backupId}/restore` | Восстановить разрешённую сторону backup |

`accept-system` и `apply` требуют `expectedGeneration`; актуальное значение берите из `completedGeneration` истории. Поле `generation` включает также rollback-записи и не является заменой этому значению. `expectedRevision` защищает от изменения файлов после просмотра: оно обязательно для accept-system и инициализации блока, в остальных apply опционально. `initializeBlock` у apply допускает только явно подтверждённую и просмотренную инициализацию nft-блока.

Поле `force: true` в apply отключает таймер и немедленно подтверждает успешное
применение. Проверки, backup, журнал и запрет другой pending-транзакции сохраняются.
CLI передаёт его через `-force`/`--force`; без флага CLI сам запрашивает подтверждение
с обратным отсчётом в той же команде. Web/API по умолчанию сохраняют два шага.

Restore имеет отдельную проверку поколения истории. Восстановление системной стороны backup через обычный restore отклоняется: оно требует безопасного применения. Не переносите правила поколений между операциями без чтения их схемы.

`synchronized` означает совпадение управляемых файлов/блоков, а не проверку реального трафика. После внешнего редактирования выполните refresh. Подробнее — [native contract](NATIVE_TEMPLATES.md).

## Safe-apply

- `GET /v1/safe-apply` — pending-транзакция.
- `POST /v1/safe-apply/{id}/confirm` — подтвердить конкретную транзакцию.
- `POST /v1/safe-apply/{id}/rollback` — запросить откат.
- `POST /v1/safe-apply/{id}/cancel-reboot` — явное решение оператора завершить
  `awaiting_reboot` без перезагрузки; другие pending-фазы возвращают HTTP 409
  `safe_apply_phase_conflict`. История сохраняется со state `reboot_cancelled`,
  `rebootRequired` и `recoveryRequired` становятся false. Файлы и runtime не меняются.
  Повтор с тем же ID после завершения возвращает `changed: false`.
  Для остановленного daemon CLI поддерживает `safe-apply cancel-reboot ID --offline`
  с root и общим runtime lock.

Подтверждение требует актуального ID. Для native system rollback восстанавливается группа nftables/sysctl/routing unit и одного вспомогательного Xray drop-in, после чего требуется ручная перезагрузка.
Ранее записанная группа из трёх файлов восстанавливается в её прежнем составе. Фаза `awaiting_reboot` сохраняется после перезапуска helper. При неполном восстановлении изменения блокируются; статус и диагностические чтения остаются доступны.

Не удаляйте pending/backup и не исправляйте audit вручную ради прохождения проверки. [Восстановление](../../docs/OPERATIONS.md) и [T8](../packaging/T8_MANUAL_SERVER_TEST.md) описывают действия оператора.

## Проверка сети сервера

`GET /v1/network/probe` возвращает последние `before`, `after`, `latest` результаты.
`POST /v1/network/probe` принимает только `{ "stage": "before|after|manual",
"transactionId": "..." }`. ID обязателен только для `after` и должен совпадать
с текущей applied safe-apply транзакцией; baseline недоступен при pending.
URL из запроса не принимается. Проверка доступна во время pending/recovery,
не меняет маршруты и не подтверждает настройки.

Результат: `checkedAt`, `url`, `expectedStatus`, `httpStatus`, `remoteIP`, `gateway`,
`interface`, проверки `dns`, `gatewayPing`, `https` (status/code/durationMs), общая
`durationMs`, `reachable`, `lanForwarding: requires_client_test`. `reachable` означает
успех HTTPS с ожидаемым кодом; ping шлюза носит дополнительный характер.
Ошибки DNS/TCP/TLS/HTTP редактируются до стабильных кодов без исходного текста.
Неуспех сети — HTTP 200 с `reachable:false`, не HTTP-ошибка самого API.

`GET/PUT /v1/settings` включает `connectivity: {url, expectedStatus, timeoutSeconds}`.
Настройки применяются к следующему запросу и сохраняются в helper TOML.
Разрешён HTTPS/443 без credentials/query/fragment; адрес назначения проверяется
как публичный IPv4, соединение привязано к проверенному IP, TLS проверяется,
redirect и proxy environment не используются. Таймаут 5..30 с, код 200..299.
После включения таймаут дополнительно ограничен deadline safe-apply с запасом 3 с.
Одновременный probe отклоняется `network_probe_busy` (409); устаревший ID —
`probe_transaction_conflict` (409). Ошибка сохранения — 503, без автоматического повтора.

### Настройки и результаты обновления геобаз (G10)

`GET/PUT /v1/xray/geodata/settings` читает и сохраняет TOML, не SQLite.
`expectedGeneration` обязателен; generation — отпечаток эффективного снимка
геонастроек и расписания, а не счётчик. Одинаковые настройки имеют одинаковую
версию. Конкурирующая запись возвращает HTTP 409 `generation_conflict`.

Settings содержит `origins`, `geosite`/`geoip` с `url` и `checksum:{url,algorithm}`,
`maxAssetSizeMB`, `builder`, `userGeosite`, `userGeoip`,
`schedule:{enabled,time}`. Builder доступен для чтения; смена исполняемой программы
разрешена только ручной правкой TOML, API отклоняет другое значение. При отсутствии TOML-ключа применяется default helper.
Явно пустые origins запрещают внешние запросы; пользовательская сборка независима.
Для частичного API PUT пропущенные дополнительные поля сохраняют текущие
значения. Источники передаются полностью. Предел файла default 256 МБ
(1 МБ = 1024 × 1024 байт); изменение действует для следующей операции без restart helper.

`POST /v1/xray/geodata/update` использует один снимок и прежний обязательный
Idempotency-Key. После сохранения операции возвращает HTTP 202 с `operation`
в состоянии `running`; скачивание, сборка, тесты и перезапуск продолжаются в daemon
независимо от HTTP-запроса. Повтор ключа возвращает ту же операцию без нового запуска;
для завершённой операции — HTTP 200 с её итогом. Другой ключ при активной операции —
HTTP 409. Ошибки принятия возвращаются сразу; ошибки принятой операции доступны через
status и журнал с ID операции. Остановка helper отменяет и дожидается worker до
закрытия хранилищ; после аварийного завершения прежняя операция помечается прерванной,
автоматического повтора нет. CLI ожидает завершения через polling; закрытие CLI или
панели не отменяет принятую задачу. `GET .../update/status` возвращает settings,
operation, `lastSuccessfulAt`, `nextScheduledAt` (Unix ms; отсутствует при выключенном
расписании), `maxAssetSizeMB`. Старая конфигурация БД не импортируется.

`operation.report` содержит `files:[{process,file,state}]`, `replaced`, `restarted`,
`issues:[{process,file,stage,code,message,at,warning}]` и отдельную `activation`
для критической ошибки шлюза. Partial failure — failed operation с сохранёнными
успешными файлами. Сбой restart/routing не делает успешную замену баз failed.
Ошибка cleanup — warning. История операций хранится в SQLite; journal связывается
с панелью тем же ID и кодом. После прерывания сохраняется этап и
`geodata_interrupted`; новая попытка доступна без общего recovery/rollback.
