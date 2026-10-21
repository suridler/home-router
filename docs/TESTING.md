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

## Beta.6: G10.8/G10.9 и публикация (21 октября 2026)

Commit `9cc0e0529421`, релиз `helper-v0.2.0-beta.6`:
[release assets](https://github.com/suridler/home-router/releases/tag/helper-v0.2.0-beta.6),
[GitHub Actions PASS](https://github.com/suridler/home-router/actions/runs/39568020001).
Подтверждены публикация шести assets и соответствие тега этому commit.

- Локально: gofmt/test/race/build/vet PASS; 162 web tests PASS. При подготовке
  релиза также прошли 67 Python packaging tests. Архивы amd64/arm64, manifest,
  SHA256 бинарников и ELF проверены; amd64 CLI сообщает
  `helper-v0.2.0-beta.6+9cc0e0529421`. Исполнение arm64 на arm64-хосте не проверялось.
- G10.8: HTTP 202 без ожидания updater; disconnect запроса не отменяет worker;
  повтор ключа, конфликт параллельного запуска, status/checkpoint, остановка и
  прерывание worker, итоговая ошибка и CLI polling проверены на временных
  SQLite/socket/fake updater. Web fixtures проверяют принятый запуск, восстановление
  отображения, этап ошибки и завершение с обновлением списка ресурсов.
- G10.9: точечный Go-клиент проверен на отдельном Xray 26.10.17: append,
  повтор без дубликатов, remove и сохранение статических правил. Unit/race
  покрывают частичные сбои, потерянный ответ и повтор синхронизации.
- **Серверная проверка выполнена:** 21 октября 2026 года пользователь подтвердил
  обновление рабочего комплекта до beta.6 через web, проверку обновления геобаз
  и динамических правил на установленной версии. Основание — подтверждение
  оператора в диалоге; дополнительные журналы, audit ID и результаты отдельных
  сценариев отказов в этом сообщении не передавались.

Остальная матрица U6/T8 остаётся открытой: это подтверждение не закрывает
downgrade/retry/rollback, принудительные сбои, LAN forwarding или новый reboot.

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

## R12: упрощение и унификация (локально, 21 октября 2026)

Изменения не установлены на сервер и не опубликованы. R2 завершён по
согласованной границе зависимостей конкретной операции.

Проверены:

- Helper: gofmt, `go test ./...`, `go test -race ./...`,
  `go build ./cmd/...`, `go vet ./...` PASS.
- Web: `npm test` — 165 PASS. Две одновременные сводки и отдельное чтение
  геобаз выполняют 8 запросов к fake helper вместо прежних 28; `/api/config`
  использует два адресных чтения и совпадает с соответствующей частью сводки.
  Завершённые ответы не кешируются, отказ допускает новое чтение на следующем poll.
- Snapshot JSONC: замена файла во время запроса метаданных не смешивает
  проекции; следующий overview видит новый документ. Проверены общие
  generation/hash, отсутствие secret-bearing outbound settings, сохранение
  видимости invalid config и отказ записи через read-only endpoint.
- Общий SQLite: сохранение модульных PRAGMA, повторное открытие всех stores,
  сохранность чужих таблиц/индексов/views/triggers и legacy данных без импорта.
- Atomic publication: ошибки подготовки, rename, sync каталога после rename;
  сохранение metadata и уборка временных файлов. Подготовка журналов использует
  общую файловую функцию с прежними каталогами и undo records.
- API: неизвестные поля ответа приняты CLI/exporter; неверные типы и trailing
  JSON отклонены. Входящие команды отклоняют неизвестные/повторённые поля;
  null проверяется предметным контрактом. Сбор ошибок остаётся доступен при
  path drift. Native apply/accept-system/restore и confirm/rollback проверены
  при изменённых и недоступных путях Xray, включая завершённые транзакции.
  Xray config и его recovery сохраняют guard; неизвестная транзакция не
  получает исключения. Ошибка фазы native recovery по-прежнему возвращается.
  Routing/geodata mutations сохраняют прежние gates.
- Exporter: `go test ./...` и сборка с `-trimpath -ldflags='-s -w'` PASS;
  бинарник собран во временный каталог.
- Packaging: 67 Python fixture tests PASS; Go packaging tests входят в полную
  матрицу helper; `bash -n packaging/install-bundle.sh` и `git diff --check` PASS.

Проверки используют временные файлы, Unix sockets и fake services. Работающие
службы, сеть, Xray config и установленный комплект хоста не изменялись.

## Уточнение серверной приёмки — 21 октября 2026

По подтверждению пользователя прерывание установки уже воспроизводилось
на этом рабочем сервере; после этого менялся таймер отката. Не учитывать
прерывание как никогда не проверявшийся сценарий. Повтор после изменения
механизма отмечать отдельно как регрессию с указанием проверенной версии.
Это подтверждение не закрывает всю матрицу downgrade/retry/rollback.

## Beta.7: обновление рабочего сервера через web — 21 октября 2026

Релиз `helper-v0.2.0-beta.7`, commit `8f615e972cdae8a3c7141d54ac1c04e2e323da7c`.
GitHub Actions run `39595783387` завершился успешно; опубликованы все шесть
assets, SHA256SUMS содержит четыре записи для архивов и raw CLI.
Пользователь подтвердил обновление этого сервера через web.

После обновления непосредственно проверены: health `ok`, версия
`helper-v0.2.0-beta.7+8f615e972cda`, active у Xray, TProxy routing, helper и web,
`safe-apply status` возвращает `pending: null`. Проверки только читают состояние.
Пользователь отдельно подтвердил через web-панель на beta.7: проверку панели,
обновление геобаз и добавление/удаление динамических ресурсов — выполнено.
Основание — подтверждение оператора; отдельные audit ID не передавались.
Native safe-apply при изменении путей Xray и оставшаяся матрица отказов
этим подтверждением не закрываются.

## Beta.7: проверки чтением на рабочем сервере — 21 октября 2026

Evidence: `/tmp/helper-beta7-readonly-2t0abvoa` (приватный каталог; временный,
не включать его содержимое в репозиторий). Конфигурация, службы, маршруты,
динамический список и геобазы в этой проверке не изменялись.

- Health beta.7 `ok`, четыре службы active, ExecMainStatus=0, pending отсутствует.
- Helper overview, exporter metadata, templates, errors, network probe status,
  geodata status, settings и proxy domains: HTTP 200. Overview configState=parsed.
- Web index и семь проверенных GET API: HTTP 200; данные трафика доступны,
  trafficSource=xray-routing-exporter, trafficError=null.
- Exporter healthz/readyz/metrics и `/api/v1/xray/stats`: HTTP 200, up=true,
  12 счётчиков. Первоначальный пробный `/api/stats` дал ожидаемый 404:
  это неверный путь проверки, рабочий endpoint доступен.
- Три native-шаблона synchronized. Xray-шаблон different, как ранее оставлено
  оператором; только это вызывает templates_different и HTTP 503 у ready.
  Config/database/Xray API/recovery checks — ready.
- Последнее обновление геобаз succeeded: 21.10.2026 17:08:00 +05,
  issues пуст, recovery clean. Следующий запуск 22.10.2026 04:00 +05;
  ежедневное обновление включено.
- Desired/baseline пусты, consolidationActive=false. Xray API возвращает
  12 правил, включая обе permanent USERLIST; динамический overlay отсутствует,
  что соответствует пустому desired. Данные не добавлялись и не удалялись.
- Policy rule fwmark=1/table=100 и local default dev lo присутствуют.
  HTTPS generate_204 вернул HTTP 204 за 0.165 с; LAN forwarding не проверялся.
- SHA256 обоих архивов и raw CLI совпадают с опубликованным SHA256SUMS.
  Metadata обеих архитектур указывает beta.7. Установленные CLI,
  `/usr/local/sbin/xray-core-helperd` и domain-list-community совпадают
  с amd64 manifest. Ошибка первоначального поиска metadata исправлена
  в локальном проверочном скрипте: release.json лежит в корне архива.
- Изолированные TestNativeOperationsDoNotDependOnXrayPaths и
  TestNativeRecoveryStillChecksTransactionPhase — PASS (временная SQLite,
  изменённые/недоступные пути, без обращения к рабочим службам).
- В доступном journal с момента запуска beta.7 нет сообщений с приоритетом
  warning/error или текстовых признаков ошибок. В сохранённой helper истории
  все 100 записей — Xray `proxy/dns: can only hijack A/AAAA records`,
  с 16:29:12 до 17:10:51 +05. Это отдельное замечание к DNS-поведению;
  причина в этой проверке не устанавливалась, конфигурация не исправлялась.

### M13: defaults и пользовательские переопределения (21.10.2026)

Изолированные проверки реализации в рабочем дереве:

- `gofmt -d ./cmd ./internal`, `go test ./...`, `go test -race ./...`,
  `go build ./cmd/...`, `go vet ./...` — PASS.
- `python3 -B -m unittest discover -s packaging -p 'test_*.py'` — PASS, 67 тестов.
- `bash -n packaging/install-bundle.sh`, `sh -n packaging/install-templconf.sh`
  и `git diff --check` — PASS.
- Покрыты все разделы TOML: наложение вложенных ключей и списков, явные
  false/empty, сохранение отличий и удаление равных defaults значений,
  сохранение остальных разделов и комментариев, смена заводских defaults,
  ошибка отсутствующего/неполного шаблона без Go fallback.
- Установочные fixtures проверяют defaults из комплекта до появления файла
  в `/usr/share/xray-core-helper/templConf/config.toml`, сохранение пользовательского
  TOML, доставку только заводской копии и чтение настроек при откате старого комплекта.

Рабочие службы, пользовательский TOML и сеть этим прогоном не менялись.
Установка M13 на сервере остаётся отдельной приёмкой.


### O14.1: изолированная оценка импорта outbound (21.10.2026)

- Рабочий каталог испытания: `/tmp/helper-o14-kli8hikg`; копии helper и
  libXray `75ccb623c807a1113a064278e3e544b0049c16a5`, MIT; core
  `92ccc939aaf4`. Основные go.mod/go.sum не менялись, библиотека не подключена.
- Обе сборки CLI: `GOTOOLCHAIN=go1.27.2 CGO_ENABLED=0 go build -trimpath
  -ldflags='-s -w' -o <отдельный-output> ./cmd/xray-core-helper` — PASS.
  Baseline: 13 975 712 байт; с прямым импортом share: 38 039 712 байт.
  Детерминированный gzip: 5 867 490 / 14 626 513 байт соответственно.
- `go list -deps` для CLI: нестандартных пакетов 79 / 499; модулей 11 / 56
  (с собственным модулем). Это граф сборки, не оценка памяти работающего daemon.
- В копии helper `GOTOOLCHAIN=go1.27.2 CGO_ENABLED=0 go test
  ./cmd/xray-core-helper -run '^TestO14' -count=1 -v` — PASS:
  три группы проверок, 24 протокольных/транспортных подслучая и отдельная
  проверка неизвестного параметра. Использованы только искусственные значения
  и `proxy.example`; реальные соединения не выполнялись.
- Проверен импорт VLESS, VMess AEAD, Trojan, SS, SOCKS и Hysteria2;
  отклонение HTTP/WireGuard URI и legacy VMess Base64(JSON). Первоначальный
  fixture SOCKS с plain user:pass отклонён: исходники требуют Base64(user:pass).
  Добавлены отдельные positive Base64 и negative plain случаи; повтор — PASS.
- Проверены RAW/TCP, WS/WebSocket, gRPC, HTTPUpgrade, XHTTP/SplitHTTP,
  KCP/mKCP; HTTP/H2/QUIC/gun отклоняются. Неизвестный URI-параметр
  библиотека молча отбрасывает — это ограничение upstream, не принятая
  семантика helper. Для адаптера нужны явные сообщения о неподдержанном.
- Upstream `GOTOOLCHAIN=go1.27.2 CGO_ENABLED=0 go test ./share` — PASS (6.978 с).
- `git diff --check` — PASS.
- Основной код O14 и серверные службы не менялись. После оценки альтернатив
  пользователь согласовал прямой pinned `libXray/share`, Go 1.27.2 и увеличение
  бинарника примерно на 23 МБ. Решение зафиксировано в O14.1 ROADMAP;
  реализация адаптера и подключение зависимости ещё не выполнены.


### O14: native outbound editor, API/web/CLI (21.10.2026)

Локальная реализация использует `github.com/xtls/libxray/share` v1.261017.0
(commit `75ccb623c807`) и Xray-core `92ccc939aaf4`, Go 1.27.2.
В отличие от изолированной оценки O14.1 зависимость теперь включена в основной
модуль; собственные VLESS-парсеры Go/JS и VLESS-only endpoints удалены.

Проверки из `xray-core-helper`, последовательно:

- `gofmt -d ./cmd ./internal` — PASS, без вывода;
- `GOTOOLCHAIN=go1.27.2 go test ./...` — PASS, 57 пакетов с тестами;
- `GOTOOLCHAIN=go1.27.2 go test -race ./...` — PASS;
- `GOTOOLCHAIN=go1.27.2 go build ./cmd/...` — PASS;
- `GOTOOLCHAIN=go1.27.2 go vet ./...` — PASS.

Покрытие: импорт VLESS/VMess/Trojan/SS/SOCKS/Hysteria2, безопасные ошибки,
запрет подписок/нескольких ссылок, черновик без записи; native Build fixtures
всех 13 ручных типов после привязки марки; create/edit/replace/delete,
защита служебных подключений, ссылки routing/balancer/dialerProxy,
generation, отказ валидатора без изменения файла, JSONC-комментарии и неизвестные
поля, сохранение только tag/марки при замене. CLI использует временный Unix
socket, передаёт credentials из stdin и требует явную generation/замену.
TCP endpoint diagnostics понимает плоские settings и прежние servers/vnext.

Из `xrayui-web`: `npm test` — PASS, 174 теста. Проверены формы 13 типов,
сохранение неизвестных значений, очистка credentials, выбор legacy server/user,
коллекции peers/правил, явная замена, поколение открытого черновика, блокировка
невалидной формы и transport API к fake helper.

Дополнительный прогон в установленном Chromium headless с настоящим Alpine,
временным HTTP server и искусственными API-ответами — PASS: отрисовка всех
13 форм, соответствие значений input/select/checkbox native-черновику,
чтение credentials и сохранение. Исправлены обнаруженные старый вызов
VLESS-selector и потеря selected value при создании динамических options.
Этот прогон проверяет браузерную механику, а не соединение с реальным proxy.

`git diff --check` — PASS. Службы, Xray config, TOML оператора и сеть рабочего
сервера не менялись. Изменения M13 сохранены. Серверная O14.8 остаётся открытой:
реальные подключения, GUI-приёмка и обновление установленного комплекта.
