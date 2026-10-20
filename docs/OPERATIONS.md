# Эксплуатация

Read-only проверки не включают TProxy и не применяют шаблоны. Restart, apply,
confirm, rollback и geodata update меняют состояние системы.

## Быстрая проверка

```bash
sudo /usr/local/bin/xray-core-helper health
sudo /usr/local/bin/xray-core-helper setup --status
sudo /usr/local/bin/xray-core-helper safe-apply status
systemctl show --no-pager xray.service xray-tproxy-routing.service \
  xray-core-helper.service xrayui-web.service \
  -p Id -p ActiveState -p SubState -p Result
```

Health сообщает доступность daemon, ready — готовность зависимостей.
Setup required не равен отказу Xray. Active unit/config/API не доказывают LAN.

Для API получите фактический socket:

```bash
HELPER_SOCKET=$(sudo /usr/local/bin/xray-core-helper installation-settings |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["socket"])')
sudo curl --silent --show-error --unix-socket "$HELPER_SOCKET" \
  http://helper/v1/ready | jq
sudo curl --silent --show-error --unix-socket "$HELPER_SOCKET" \
  http://helper/v1/templates | jq
```

Не используйте --fail, когда нужен JSON причины HTTP 503. Socket default —
root:xray-core-helper 0660; доступ только доверенным beta-клиентам.
При отказе проверьте systemctl status и journalctl нужного сервиса.

## Шаблоны и drift

Factory: `/usr/share/xray-core-helper/templConf`.
Working: `/etc/xray-core-helper/templConf`, root:root 0600.
Четыре ID: xray-config, nftables, sysctl, tproxy-unit. Web env — не шаблон.

При templates_different/templates_invalid смотрите сторону и код в /v1/templates.
Не копируйте factory поверх рабочей системы. Raw Xray config/diff может
содержать секреты; для браузера используйте безопасные проекции.

После внешнего изменения перечитайте состояние:

```bash
sudo curl --silent --show-error -X POST --unix-socket "$HELPER_SOCKET" \
  http://helper/v1/templates/refresh | jq
```

Refresh не принимает differences и не применяет файлы. Accept-system явно
принимает system в working; apply меняет систему по working.
Инициализация nft markers требует отдельного preview/revision.
Поколения и request contracts: [Helper API](../xray-core-helper/docs/API.md).

## Safe apply и восстановление

Держите независимую консоль до потенциально разрывающего сеть apply.
Подтвердите или откатите конкретную pending-операцию до deadline:

```bash
sudo /usr/local/bin/xray-core-helper safe-apply status
# Выберите одно действие и подставьте ID текущей операции:
sudo /usr/local/bin/xray-core-helper safe-apply confirm TRANSACTION_ID
# либо
sudo /usr/local/bin/xray-core-helper safe-apply rollback TRANSACTION_ID
```

Timeout запускает systemd rollback timer. Системный rollback восстанавливает
baseline nft/sysctl/unit; runtime может оставаться изменённым до reboot.

- Неполный restore/recoveryRequired: сначала устраните причину и повторите
  восстановление; reboot не заменяет отсутствующие файлы.
- Awaiting reboot: файлы восстановлены, оператор вручную выполняет sudo reboot.
- Новый kernel boot ID и совпадение baseline завершают recovery.

Не удаляйте pending, SQLite, backups, audit и не правьте fingerprints ради PASS.
`setup` — самостоятельный режим CLI: он останавливает helper/web, берёт runtime
lock и работает без запросов к socket; `setup --status` остаётся обычным чтением.
Для interrupted setup предусмотрено `setup --recover`.

## Динамические домены и IPv4

Команда, endpoint и поле `domain` остались прежними; значение принимается как
домен или IPv4. Тип вручную не выбирается:

```bash
sudo /usr/local/bin/xray-core-helper proxy-domain list
# Следующие операции меняют маршруты новых соединений:
sudo /usr/local/bin/xray-core-helper proxy-domain add example.com
sudo /usr/local/bin/xray-core-helper proxy-domain add 192.0.2.1
sudo /usr/local/bin/xray-core-helper proxy-domain delete 192.0.2.1
sudo /usr/local/bin/xray-core-helper proxy-domain delete example.com
```

Helper нормализует домен (пробелы, регистр, конечная точка) и IPv4 (каноническая
запись); add/delete идемпотентны. Пустой ввод, IPv6, CIDR, URL, IPv4 с портом и
некорректный IPv4 отклоняются до записи с `invalid_domain`; CLI не создаёт
собственного хранилища и при недоступном daemon мутацию не выполняет.

`list` печатает JSON `desired`, `baseline`, `consolidationActive`. `desired` —
сохранённое намерение, `baseline` — последняя консолидированная пара USERLIST.
Удаление baseline-записи оставляет её в baseline и добавляет direct exclusion;
из USERLIST она исчезает после следующей консолидации. Add/delete меняют desired
и без рестарта Xray синхронизируют runtime overlay.
`applied:true` в API и сообщение `applied` в CLI означают применение overlay,
а не запись в USERLIST. В `geosite_user.dat` и `geoip_user.dat` записи попадают только при
`geodata update`: консолидация замораживает снимок, передаёт домены в geosite,
IPv4 в geoip как `/32`, согласует runtime и лишь затем одной транзакцией
фиксирует baseline пары. До этого запись существует в desired и runtime.

Порядок правил: существующие системные правила, dynamic direct exclusions,
dynamic proxy additions (отдельно домены и IPv4), затем
`proxy-domains-permanent` с USERLIST и остальные правила. Для IP overlay
единственное `proxy-ip-permanent` должно оставаться после permanent-правила
доменов; helper не добавляет недостающее правило автоматически.

Если add/delete вернул `domain_runtime_sync_failed`, а apply —
`domain_runtime_apply_failed`, desired мог сохраниться. Перечитайте список и
разберитесь с конфигурацией; после исправления выполните явный apply. Не
повторяйте add/delete автоматически и не считайте запись отсутствующей:

```bash
sudo /usr/local/bin/xray-core-helper proxy-domain list
sudo /usr/local/bin/xray-core-helper proxy-domain apply
```

Во время consolidation записи отклоняются с `consolidation_active`, чтение
остаётся доступным. `proxy_domain_unavailable` означает недоступность сервиса
или recovery gate. Повторный apply строит тот же overlay без дубликатов;
внутренний планировщик helper раз в 30 с вызывает reconcile через тот же API
после нового запуска Xray. Proxy-as-freedom не создаёт туннель. Ввод доступен
на первом экране панели и в маршрутизации.

## Логи и geodata

```bash
sudo /usr/local/bin/xray-core-helper logs error --limit 50
sudo /usr/local/bin/xray-core-helper logs access --limit 50
sudo /usr/local/bin/xray-core-helper errors
systemctl status xray-core-helper.service --no-pager
sudo /usr/local/bin/xray-core-helper geodata status
sudo /usr/local/bin/xray-core-helper geodata settings
```

Log reads ограничены configured files/journald. Helper хранит последние 100
отдельных ошибок в SQLite без TTL; внутренний планировщик helper собирает их
каждые 20 с (это не systemd timer). Для явного сбора используйте
`sudo /usr/local/bin/xray-core-helper errors collect`.
Сбой/ограничение окна отражаются в ответе; история не является полным архивом.
Не все connection failures попадают на warning/error.
Accepted direct и webhook подтверждают маршрут, не открытие страницы.

При новой установке настроены GeoSite и GeoIP от `runetfreedom`:
`russia-blocked-geosite` и `russia-blocked-geoip`, ветка `release`.
Оба файла проверяются по SHA-256; разрешён HTTPS origin `raw.githubusercontent.com`.
Установщик сохраняет существующий TOML, а helper — сохранённые в SQLite настройки.
Чтобы отключить источники, очистите origins и URL обоих файлов и их checksum;
структура — [TOML](../xray-core-helper/config.example.toml). После первого запуска
настройки geodata меняются через web/API helper, а не редактированием bootstrap TOML.
Geodata update скачивает/проверяет файлы, выполняет consolidation и restart Xray.
Расписание — `[scheduler]` в `/etc/xray-core-helper/config.toml`:
`geodata_update_enabled = true`, `geodata_update_time = "04:00"`. Обновление ежесуточное,
время сервера, формат HH:MM; для отключения укажите false. После изменения
перезапустите helper. Пропущенный сохранённый запуск выполняется после старта;
первый запуск на новой установке — в ближайшее назначенное время.
Состояние обновления доступно через `geodata status`, сбор ошибок — `errors`.
Отключённый auto-update не доказывает поломку сети.

## Автоматическая проверка сети

Мастер CLI и включение TProxy в панели запускают IPv4 DNS, ping реального
шлюза и HTTPS до/после применения. URL/ожидаемый код/таймаут меняются в
«Настройки → Система» либо `[connectivity]` helper TOML до запуска daemon.
По умолчанию — Google generate_204, HTTP 204, общий таймаут 15 с.
Проверить отдельно и прочитать сохранённые результаты:

```bash
sudo /usr/local/bin/xray-core-helper network-probe
sudo /usr/local/bin/xray-core-helper network-probe status
```

`reachable:true` подтверждает IPv4 HTTPS с сервера. При штатном профиле это
обычный исходящий путь, включая Xray после включения TProxy. ICMP шлюза может
блокироваться независимо от доступа к сайту. LAN forwarding требует клиента.
Результаты хранятся в `network-probes.json` рядом с helper SQLite, только последние
before/after/latest; ID after связывает проверку со штатным safe-apply журналом.
При неуспехе можно повторить только проверку, подтвердить настройки вручную,
откатить либо оставить таймер. Автоматического подтверждения нет.

## Реальный трафик

```bash
curl -4 --noproxy '*' --connect-timeout 5 --max-time 15 \
  --fail --silent --show-error --output /dev/null \
  --write-out 'HTTP %{http_code}; IP %{remote_ip}; время %{time_total} с\n' \
  https://www.google.com/generate_204
```

Ожидается HTTP 204: IPv4 DNS/TCP/TLS/HTTP с места запуска. Shipped TProxy
перехватывает публичный TCP/UDP сервера с учётом bypass. Для LAN нужен
отдельный запрос клиента, реально использующего шлюз.
Setup выполняет DNS/шлюз/HTTPS-пробы до и после включения TProxy; URL и
ожидаемый статус задаются в `[connectivity]` helper. Пользователь подтверждает
сохранение самостоятельно, включая случай неуспешной пробы.

## Если web не открывается

```bash
systemctl status xrayui-web.service --no-pager -l
curl --noproxy '*' --max-time 5 -o /dev/null -w 'HTTP %{http_code}\n' \
  http://127.0.0.1:3000/
sudo ss -ltnp 'sport = :3000'
```

Во время setup проверяйте / и /api/setup. Установщик проверяет listener через /;
/api/health зависит также от доступности normal-mode API Helper и может быть
недоступен во время setup при работающей странице.

Loopback относится к машине браузера. Для удалённого Linux используйте туннель.
В WSL проверьте localhost forwarding и занятые Windows-порты: старый VS Code
tunnel может принять TCP без HTTP-ответа. Не открывайте beta на 0.0.0.0 ради обхода.

WSL иногда перезапускает systemd при sudo reboot, сохраняя kernel boot ID.
Сравните /proc/sys/kernel/random/boot_id с audit. При неизменном ID оператор
может завершить WSL из Windows через wsl --shutdown и запустить дистрибутив.
Это ручное действие затрагивает все WSL-сессии.
