# Проверки и приёмка

Unit/fixture PASS не означает успешную установку, forwarding или reboot
на реальном хосте. Команды разработки — [CONTRIBUTING](../CONTRIBUTING.md).

## Матрица

| Проверка | Что подтверждает | Среда |
| --- | --- | --- |
| Go/Node unit | Контракты, edits, redaction, recovery с fakes | Development |
| Python packaging | Архивы, загрузчик, T8 runner | Development |
| T8 check | Services, ready, socket permissions, templates, confinement, explicit network diagnostics | Disposable server |
| T8 smoke | Sysctl confirm, grouped rollback, recovery | Disposable с консолью |
| T8 post-reboot | Новый boot ID, baseline/metadata, clear recovery | Тот же сервер/audit |
| CLI/Web clean install | Пользовательский процесс установки | Отдельные свежие экземпляры |
| LAN/packet | Перехват, bypass, forwarding, отсутствие loops | Тестовая топология |
| Browser acceptance | Навигация, отображение, подтверждения, mobile | Целевой bundle |

Основная процедура — [T8](../xray-core-helper/packaging/T8_MANUAL_SERVER_TEST.md).
Переустановка ради повторного check/smoke не нужна.

## Зафиксированные результаты

Сведения перенесены из операторской приёмки и прежних отчётов, а не заново
проверены на текущем хосте при редактировании документации.

Имена хостов в таблице условные; `TEST-GATEWAY` обозначает тестовый WSL-сервер.

| Дата / хост | Результат | Граница |
| --- | --- | --- |
| 2026-10-09, beta gateway | Read-path и mutation smoke | До новых native/setup изменений |
| 2026-10-14, beta gateway | Failure fixtures; HTTP 204 с остановленными web/Helper; reboot | Прежние diagnostics/bundle |
| 2026-10-15, WSL disposable | Начальный setup и namespace packet fixture | Предыдущий сетевой профиль |
| 2026-10-17, TEST-GATEWAY, fixes до 3bd5144 | T8 smoke и post-reboot: все автоматические проверки PASS | Не полная GUI/LAN/crash/nft/unit матрица |
| 2026-10-17, тот же сервер | HTTPS 204 с сервера; панель открывается из Windows | Не отдельный LAN-клиент |
| 2026-10-18, development | 92 web unit и 39 packaging tests PASS; helper gofmt, go test ./..., race, build, vet PASS. Проверены отдельные повторяющиеся ошибки, FIFO/reopen/redaction, journal checkpoint и exporter rates/reset/stale/schema | Текущая рабочая копия, без browser/server acceptance |
| 2026-10-18, development / Chromium fixtures | Девять разделов на 1440×1000 и 390×844; домен, URL вкладок, единый журнал с ошибками по умолчанию, pending во всех разделах, offline/recovery; без JS errors/overflow | Локальные синтетические данные, не реальный exporter и не серверная приёмка |
| 2026-10-18, development | 41 web unit и 22 T8 fixture tests PASS перед переносом в main | Новая панель без операторской приёмки |
| 2026-10-18, TEST-GATEWAY, bundle 9d1cf9b | После сброса установки: setup, HTTPS 204 до/после включения, T8 check/smoke/post-reboot PASS. Динамическое правило восстановлено timer после restart Xray и удалено из runtime после удаления домена | Не новая ОС; ping шлюза неуспешен до/после. Без LAN-клиента, проверки restart helper/retry/consolidation и обновления геобаз |
| 2026-10-18, development, исправление shared SQLite | Воспроизведён отказ открытия БД после создания helper_schedule; после исправления gofmt, go test ./..., race, build, vet PASS. Три открытия всех хранилищ сохраняют домены, ошибки, pending и дедлайн | Временные SQLite и fake reader; без запуска установленных служб и серверного T8 нового bundle |
| 2026-10-19, development, D6 | 153 web tests; helper gofmt/test/race/build/vet PASS; Bruno 5 requests / 10 tests PASS. Chromium: общий ввод domain/IPv4 на главной и routing, Enter, нормализация, удаление/отмена, stale, 1440×1100 и 390×844; без JS exceptions/overflow | Временные API/socket и синтетические данные браузера; без изменения установленных служб и без нового серверного T8 |
| 2026-10-19, development, S7 | gofmt/test/race/build/vet PASS; 153 web и 67 Python packaging tests PASS. Полный новый Xray-шаблон принят изолированным Xray 26.3.27 с синтетическими основными assets и пустыми/заполненными USERLIST; проверены DNS-условия, порядок правил и mixed runtime overlay | Без служб хоста, сетевого DNS-fallback теста, LAN и серверного T8 нового bundle |

Последний успешный серверный audit: /var/backups/xray-T8.q6vcqk4y
(check перед smoke: /var/backups/xray-T8.ybqwonui), на тестовом сервере.
Прежний успешный audit: /var/backups/xray-T8.pcumxg6c.
Содержимое приватного audit не публикуется. После установки helper-v0.2.0-beta.1
отдельно переносились исправления; это не означает обновления старых release assets.

Испытания выявили/исправили root:root 0600 рабочих шаблонов после reboot
и completedGeneration после rollback. Неуспешные старые audits сохраняются
как evidence; fingerprints и права не исправляют для искусственного PASS.

## G10: обновление геобаз и TOML (21 октября 2026)

Локально проверены:

- Единые defaults для отсутствующих TOML-ключей; явные `[]`/`false` сохраняются.
  Ручная правка видна следующему циклу, конкурирующий PUT получает 409;
  builder можно изменить только вручную в TOML. Сохранение не стирает чужие
  разделы/комментарии и поддерживает quoted headers, inline tables и multiline.
- Независимые внешние и пользовательские файлы, SHA256, HTTP/redirect/timeout,
  размеры до и выше 64 МБ, независимый `xray run -test` каждого процесса.
  При частичной записи тестируется реальная смесь; failed test восстанавливает
  только изменённые файлы своего процесса. Неудачное восстановление сохраняет
  evidence и не запускает Xray с заведомо отвергнутым набором.
- Ноль restart без замен/при полном отказе; один при заменах. Failed restart
  не откатывает базы. Повтор после перезапуска helper активирует прежние замены,
  даже если файлы уже совпали; baseline не меняется до нового runtime Xray.
  Ошибка routing не теряет desired и допускает штатный reconcile.
- Прерванная операция сохраняет этап и допускает новую попытку. Ошибка
  финального сохранения SQLite возвращает известные результаты и понятный
  `save_state` с ID операции; cleanup остаётся предупреждением.
- Web: частичный успех/точная ошибка, отдельная критическая ошибка Xray,
  даты выполнения/расписания, снятие текущей ошибки успешным повтором.
  Чтение настроек для карточки и результатов использует один status snapshot.

Команды: `gofmt -d ./cmd ./internal`, `go test ./...`, `go test -race ./...`,
`go build ./cmd/...`, `go vet ./...`; web `npm test` (156 тестов);
Python packaging fixtures (67 тестов); `bash -n` изменённого host harness;
`git diff --check`: PASS. Последовательная финальная Go-матрица пройдена;
web — 156/156, packaging — 67/67.

Все проверки используют временные файлы/SQLite, локальные fake HTTP/socket и
fake Xray/builder. Установленные службы и production-сеть не менялись.
Остаётся отдельная установленная приёмка нового комплекта: ежедневный запуск,
обновление реальных источников, отображение ошибки и успешного повтора в браузере,
загрузка USERLIST и восстановление правил после единственного restart.
Уже выполненные T8 старых версий не считаются приёмкой G10.
Проверена смена имени USERLIST до применения binding: новый файл собран,
динамические правила сохранены, повторного restart нет. После подключения
постоянного proxy-правила и нового запуска Xray baseline активируется.
Отдельно проверено, что DNS-ссылка не подменяет постоянное routing-правило.

## Отдельные оставшиеся сценарии

- LAN TCP/UDP и bypass текущего профиля отдельно от серверного HTTP.
- Native nft/unit confirm/timeout, helper crash и partial recovery.
- CLI/browser setup для existing/empty config на независимых исходных состояниях.
- Нестандартные service paths/assets/socket на отдельном свежем экземпляре.
- Операторская приёмка согласованной новой панели.
- Авторизация/browser security и hardened acceptance после реализации.

Успешный staged T8 закрывает свой прогон, а не автоматически всю матрицу.

## Приёмка пересмотренного N9

Предыдущий локальный `helper-v0.2.0-beta.4` на `0eb9d59` отозван. Его результаты
относятся к удалённой сложной схеме и не подтверждают текущий вариант. Для новой
схемы 20 октября 2026 года выполнены gofmt, `go test ./...`,
`go test -race ./...`, `go build ./cmd/...`, `go vet ./...`: PASS.
67 Python packaging tests и 153 web tests: PASS. Проверка unit-графа через
`systemd-analyze --root ... verify` выполнена на временном дереве;
проверены остановка explicit apply на каждом отказавшем шаге и grouped recovery.
На этапе локальной сборки реальные boot/stop/crash и серверная приёмка ещё не выполнялись;
последующие результаты приведены ниже.
Продуктивные службы и сеть при пересмотре не менялись.

Серверная проверка на WSL `LAPTOP-DNA`, beta.4 `35a4309e1d56`, 20 октября:
установка из публичного GitHub завершена; версии CLI/daemon совпадают.
Ручной переход со старой схемы выполнен с добавлением `[network]` и заменой
рабочего unit-шаблона, safe-apply подтверждён, pending отсутствует. Проверены
stop/start Xray (снятие/возврат rule и local route), restart при остановленном
helper, reboot со сменой Boot ID и crash через SIGKILL с автоматическим restart.
После восстановления службы active, HTTPS generate_204 возвращает 204.
Это не подтверждает LAN forwarding, failure injection add route/rule и полный
автоматический переход между версиями. Доставка шаблонов исправлена отдельно
после этой опубликованной сборки; данный серверный результат её не проверяет.

Новая матрица:

1. Boot без helper: native nft/sysctl загружены, Xray и routing активны;
   серверный HTTPS и реальный LAN TCP/UDP/DNS проверены отдельно.
2. Start/stop/restart/crash Xray: hook запускает/останавливает routing без
   отдельного enable. Нет встречного ordering или собственного boot routing.
3. Ошибка add route/add rule: Xray получает failed start, журнал сохраняет
   причину, ExecStopPost удаляет rule и точный local route после частичного старта.
4. Постоянные nft-правила и forwarding/sysctl сохраняются при stop Xray;
   чужие маршруты и правила не очищаются. Новых marker/lock/цепочек нет.
5. До перевода явно отключён старый независимый autostart routing без `--now`.
   Явный safe-apply устанавливает единственный drop-in и перезапускает Xray;
   routing запускается его hook. Rollback восстанавливает
   четыре файла/их исходное отсутствие; legacy3 evidence остаётся читаемым.
6. Upgrade/uninstall helper сохраняет native-сеть и пользовательские шаблоны.

Нужны новая последовательная локальная регрессия, временные systemd fixtures и
операторская приёмка на disposable сервере с независимой консолью. Fault injection
и production reboot не выполняются автоматически. Успех HTTP сам по себе не
доказывает LAN. Текущие проксируемые соединения при остановке могут оборваться.
Результаты фиксируются здесь после фактического выполнения.

## Дополнительные harness

Исполняемые focused harness остаются в xray-core-helper/packaging. Часть старых
assertions привязана к прежним diagnostics: сначала сверяйте их с текущим кодом,
не используйте вместо T8.

| Файл | Назначение |
| --- | --- |
| 12h1-clean-host-smoke.sh | Явные cli/web и existing/create, только чистый host |
| 12d-host-failure-recovery.sh | Live stale-generation плюс isolated failures |
| 12e-availability-reboot.sh | Две фазы, остановка control services, ручной reboot |
| r6-netns-fixture.sh | Isolated mount/network namespaces, opt-in packet tests |

```bash
cd xray-core-helper
sudo env XCH_DISPOSABLE_HOST=YES XCH_NETNS_TEST=YES \
  go test ./internal/setup -run '^TestNativeGatewayNetworkNamespace$' -count=1 -v
```

Нужны Xray, nftables, unshare, ip, jq, ss, mount, Bash, setpriv и Python.
Обычный go test может пропустить fixture; skip не равен PASS.

Исторические подробности удалённых фазовых отчётов доступны в Git до
реорганизации документации. Новые результаты добавляйте сюда с датой,
bundle/commit, хостом, областью проверки и расположением приватного audit.

## Development-проверка сети, 18 октября 2026

Добавлены hermetic проверки IPv4 DNS/HTTPS, обязательного TLS, точного HTTP-кода,
запрета redirect/proxy/non-public IP, сохранения before/after и связи с pending ID.
Ping не определяет результат HTTPS. Проверены классификация transport timeout,
ошибка directory sync после rename, busy probe и ручное подтверждение при неуспехе.
Web-тесты покрывают одну активацию, baseline/after probe, повтор только probe,
потерю ответа apply без повторного apply и сохранение настроек проверки.
Это проверки development checkout с fake runners и локальным TLS fixture;
они не подтверждают прохождение трафика на установленном тестовом сервере.

### Приёмка пользовательского geosite и динамического runtime

На disposable сервере после чистого setup проверить `geodata.user_geosite`,
наличие USERLIST и правило `proxy-domains-permanent` после системных правил.
Добавить тестовый домен и проверить runtime rules через Xray API; перезапустить
Xray, дождаться следующей проверки внутреннего планировщика helper (30 секунд) и
повторить проверку. Повторный `proxy-domain reconcile` должен вернуть applied=false.
Проверить восстановление после restart helper, временно недоступного API,
а также отсутствие запуска намеренно остановленного Xray. Отдельно проверить
исключение домена из baseline и обновление геобаз. Эти проверки на целевом
сервере пока не выполнены.

Локальная проверка сборки начального asset без системных сервисов:
`XRAY_HELPER_TEST_GEODATA_BUILDER=/absolute/path/domain-list-community go test ./internal/setup -run TestSetupGeositeWithRealBuilder -count=1`
из `xray-core-helper`. Используются только временные файлы; Xray validator в этом
тесте заменён fixture, поэтому это не проверка настоящего Xray.

### Внутренний планировщик helper

Локальная регрессия `TestAllSharedStoresReopenWithScheduler` открывает общую SQLite
в порядке запуска daemon три раза: на чистом состоянии и с pending safe-apply.
Проверяются desired/baseline доменов, evidence отката, история ошибок без повторного
сбора одного события и сохранённый дедлайн геобаз. Неизвестные таблицы, индексы,
views и triggers по-прежнему отклоняются. Это не заменяет рестарт установленного
daemon и проверку его confinement на сервере.

После обновления bundle проверить отсутствие активных прежних таймеров
`xray-core-helper-errors.timer`, `xray-core-helper-routing.timer`,
`update-xray-geodata.timer`; rollback timer должен оставаться установленным.
Через `errors` проверить изменение `collectedAt`, через Xray API — восстановление
динамического правила после restart Xray и helper, включая повтор после отказа API.
В журнале не должно быть сообщений об успешных периодических проверках;
одинаковый длительный отказ должен дать одно сообщение до восстановления.

Дважды перезапустить helper и после каждого запуска проверить `health`, `ready`,
`safe-apply status`, `proxy-domain list`, историю ошибок и отсутствие ошибок
открытия БД. Используйте только disposable сервер для отдельной проверки рестарта
с pending-транзакцией; не очищайте её вручную. После рестартов повторите T8
check/smoke/post-reboot с новым audit.

Для проверки ежедневного обновления на disposable сервере назначить в
`[scheduler]` ближайшее время HH:MM и перезапустить helper. Обновление перезапускает
Xray. Проверить результат через `geodata status`. Отдельно остановить helper до
назначенного запуска и запустить после него: выполняется один пропущенный запуск.
Повторный restart не должен повторять обновление. Проверить `geodata_update_enabled = false`
с рестартом helper и вернуть обычное время 04:00 после проверки.

## S7: тестовый комплект

Версия подготовленной локальной сборки — `helper-v0.2.0-beta.3`, Linux amd64
и arm64. Оба архива и raw CLI проверены по SHA256, ELF и manifest; templates
совпадают с committed snapshot. Raw CLI и сборщик USERLIST выполнены на amd64;
arm64 проверен статически, без эмуляции исполнения. Публикация в GitHub и серверная приёмка выполняются отдельно. Для
чистого setup нужен исходный snapshot disposable сервера; обновление существующей
установки не проверяет новый setup. Историческая beta.3 сохраняла рабочие шаблоны;
текущий контракт доставки заменяет четыре поставляемых шаблона вместе с helper,
не применяя их к системным файлам.

После копирования подходящего архива и SHA256SUMS на сервер, для amd64:

```bash
cd "$HOME"
sha256sum --check --ignore-missing SHA256SUMS
T8_DIR=$(mktemp -d "$HOME/xray-T8-beta3.XXXXXXXX")
tar -xzf "$HOME/xray-core-helper-v0.2.0-beta.3-linux-amd64.tar.gz" -C "$T8_DIR"
python3 "$T8_DIR/xray-core-helper/packaging/verify-release.py" "$T8_DIR" &&
  sudo bash "$T8_DIR/xray-core-helper/packaging/install-bundle.sh" --prebuilt
```

Установщик сам подготовит web dependencies и запустит standalone setup при
интерактивной чистой установке. Отдельный `npm ci` не нужен. При ошибке остановитесь,
сохраните состояние `update status`; повторное действие — только осознанные
`install --recover`/retry/rollback по [инструкции установки](INSTALLATION.md).
Не очищайте БД/журнал установки ради повторного прогона.

```bash
sudo xray-core-helper health
sudo xray-core-helper setup --status
sudo xray-core-helper safe-apply status
sudo python3 "$T8_DIR/xray-core-helper/packaging/t8-server-test.py" check
sudo python3 "$T8_DIR/xray-core-helper/packaging/t8-server-test.py" smoke --reviewed-sysctl
```

Ожидается версия `helper-v0.2.0-beta.3+<commit>`, отсутствие pending и PASS
всех автоматических проверок. Smoke содержит grouped rollback: следуйте
[T8](../xray-core-helper/packaging/T8_MANUAL_SERVER_TEST.md), сохраните напечатанный
`audit` и абсолютный путь к runner до выхода из shell. После ручной перезагрузки
с новым boot ID запустите `post-reboot --audit <тот же audit>` по абсолютному пути.
В WSL смена входа пользователя или restart systemd не гарантирует новый boot ID.

Дополнительно проверить новый функционал:

- Домен и IPv4 через CLI и web: нормализация, добавление/удаление, раздельные
  runtime-правила перед permanent и после system; повтор и restart без дублей.
- Явное обновление геобаз с обоими типами ресурсов: оба USERLIST файла,
  согласованный baseline и работа Xray. Повторить с пустым списком.
- Logging из TOML/API: настройки переживают следующий apply. Предупреждение
  systemd требует явного решения пользователя и не меняет sandbox скрытно.
- Новый setup сохраняет исходные шаблоны с placeholders; системные кандидаты
  не содержат неразрешённых bindings. Повторный setup стабилен.
- Сетевая проба до/после включения, журнал ошибок, графики и все разделы панели;
  отдельный LAN-клиент нужен для проверки forwarding.

U6.12 выполняется на отдельном восстановимом состоянии: две совместимые версии,
сохранение настроек/БД/рабочих шаблонов, upgrade, разрешённый downgrade и запрет
ниже minimum_installable_version, явные retry/rollback после отказа и прерывания.
Fault injection и подмена manifest на работающем сервере не входят в обычный T8.

### CLI safe-apply и отмена reboot, 20.10.2026

Локально пройдены `gofmt -d ./cmd ./internal` (без вывода), `go test ./...`,
`go test -race ./...`, `go build ./cmd/...`, `go vet ./...` и `git diff --check`.
Проверены подтверждение/откат в одной команде, обратный отсчёт, потеря связи,
истечение deadline без ввода, EOF, повтор probe без повторного apply,
подтверждение при неуспехе probe; force без timer и с сохранением recovery при
ошибке; отмена reboot, неверная фаза/ID, повтор, ошибка metadata и сохранение истории.
Offline-команда использует тот же проверенный recovery-код с общим runtime lock;
её запуск через systemd на сервере ещё не проверен. Production-службы и маршруты
при этих локальных проверках не менялись.

Дополнительно после исправления cursor redraw повторно пройдены Go test/race/
build/vet, gofmt и diff check. Собранный CLI проверен через Linux PTY и fake
Unix API: набор ответа во время countdown, исправление Backspace, пункт `3`,
повтор probe, подтверждение — PASS; один apply, два probe, один confirm.
Скрипт проверки находится рядом с локальным CLI в
`/home/suridler/builds/helper-safeapply-cursor-fix-orvdh6km/verify-pty.py`.
Нужна визуальная проверка в операторском терминале; службы сервера не менялись.

### Проверка G10 на рабочем сервере, 21 октября 2026

Beta.5 `89860dd`: helper/web/Xray/TProxy активны; внешние источники сохранены
в TOML через API. Обновление заменило geoip.dat и geosite_user.dat; остальные
файлы не изменились. Xray перезапущен успешно. Активация runtime routing
завершилась ошибкой. Read-only сборка действующего routing через Xray test
прошла за 11,1 с; `xray api adrules` начинает API deadline до сборки и по
умолчанию ограничивает его тремя секундами. Исправление явно передаёт 15 с,
согласованные с текущим command timeout helper; CLI сохраняет код ошибки
из расширенного ответа с operation. Повторная установленная проверка нужна
после доставки исправления; сетевые файлы и Xray config не менялись.
