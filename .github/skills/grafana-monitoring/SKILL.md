---
name: grafana-monitoring
description: 'Настройка и диагностика мониторинга Xray Gateway в Grafana, Prometheus и xray-routing-exporter. Use when deploying the monitoring stack, configuring data sources or dashboards, verifying metrics, or troubleshooting empty Grafana panels.'
argument-hint: '[цель мониторинга или симптом]'
user-invocable: true
disable-model-invocation: false
---

# Мониторинг Xray Gateway в Grafana

## Когда использовать

Используй этот skill, когда нужно:

- развернуть или обновить стек `xray-monitoring-docker`;
- подключить Prometheus и Grafana к `xray-routing-exporter`;
- проверить provisioning datasource и dashboard `Xray Gateway`;
- разобраться с пустыми панелями, недоступными метриками или ошибками Docker;
- добавить метрику или панель, сохраняя текущую модель доступа и приватность endpoint-ов.

## Контекст проекта

- `xray-routing-exporter` работает на хосте и отдает Prometheus на `127.0.0.1:9101/metrics`.
- Prometheus работает в host network mode на `127.0.0.1:9090`.
- Grafana работает в host network mode и доступна только на `192.0.2.10:3000`.
- Адреса `192.0.2.0/24` здесь условные: перед запуском compose и диагностических команд подставь адреса своего сервера и клиента.
- Основной dashboard хранится в `xray-monitoring-docker/grafana/dashboards/xray-gateway.json`.
- Datasources и dashboard подключаются через provisioning из `xray-monitoring-docker/grafana/provisioning/`.
- Grafana использует Prometheus для метрик и Infinity datasource для JSON API exporter-а.
- Не меняй адрес exporter-а на `0.0.0.0` только ради Docker: host networking намеренно сохраняет endpoint приватным.

## Процедура

### 1. Уточни цель и текущий режим

Определи, что требуется: первый запуск, обновление существующего стека, изменение dashboard или диагностика.

Перед изменениями проверь:

```bash
cd xray-monitoring-docker
docker --version
docker compose version
```

Если задача касается только dashboard, не перезапускай Xray и не меняй systemd unit exporter-а.

### 2. Проверь источник данных до Grafana

Проверяй слои снизу вверх:

```bash
curl -fsS http://127.0.0.1:9101/metrics | head
curl -fsS http://127.0.0.1:9090/-/ready
curl -fsS http://127.0.0.1:9090/api/v1/targets
```

Интерпретация:

- exporter недоступен: проверь `systemctl status xray-routing-exporter` и `journalctl -u xray-routing-exporter`;
- `/metrics` доступен, но target Prometheus не `up`: проверь `prometheus/prometheus.yml`, host network mode и порт `9101`;
- метрики есть, но нет событий: проверь Xray webhook, StatsService и что тестовый клиент действительно генерирует трафик;
- не подменяй отсутствие routing events отсутствием traffic counters: это разные метрики.

Для проверки конкретных метрик используй Prometheus API:

```bash
curl -sG http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=xray_stats_api_up'
curl -sG http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=xray_traffic_bytes_total'
```

### 3. Подготовь секреты и запусти стек

Если `.env` еще не создан, создай его из примера и задай случайный пароль администратора:

```bash
cp .env.example .env
printf 'GRAFANA_ADMIN_PASSWORD=%s\n' "$(openssl rand -hex 24)" > .env
```

Не выводи пароль в чат, логи или commit.

Запусти или обнови только monitoring stack:

```bash
docker compose pull
docker compose up -d
docker compose ps
```

Если контейнер не стартует, сначала исправь причину из `docker compose logs --tail=100 prometheus grafana`, затем повтори проверку. Не удаляй named volumes без явного требования: там находятся исторические данные Prometheus и Grafana.

### 4. Проверь provisioning и доступность Grafana

Проверь health endpoints:

```bash
curl -fsS http://127.0.0.1:9090/-/ready
curl -fsS http://192.0.2.10:3000/api/health
```

Проверь в UI или через API, что присутствуют:

- datasource `Prometheus` с UID `prometheus`;
- datasource `Xray-API` с UID `xray-api`;
- dashboard `Xray Gateway` в папке `Xray Gateway`;
- variable `Client`, если dashboard использует фильтрацию по клиенту.

Если datasource отсутствует, проверь файлы `grafana/provisioning/datasources/datasources.yml` и логи Grafana. Если dashboard не появляется, проверь `grafana/provisioning/dashboards/dashboards.yml`, путь монтирования и валидность JSON.

### 5. Проверь dashboard по типам данных

Ожидаемая проверка:

- traffic panels используют `xray_traffic_bytes_total` и `rate(...[5m])`;
- health panels показывают `xray_stats_api_up`, `xray_uptime_seconds` и `xray_restart_total`;
- routing panels используют Infinity datasource и API exporter-а;
- variable `Client` строится из `label_values(xray_routing_events_total, client)`.

После генерации тестового трафика обнови dashboard и проверь выбранного клиента. Сверяй значения с exporter API, например:

```bash
curl -fsS 'http://127.0.0.1:9101/api/v1/stats/hour/summary?client=192.0.2.51'
curl -fsS 'http://127.0.0.1:9101/api/v1/xray/stats'
```

Не интерпретируй `events` как количество HTTP-запросов: routing webhook может генерировать повторные события, особенно для UDP.

### 6. Диагностируй `No data` по ветвлению

1. Если Grafana недоступна, проверь bind address `192.0.2.10:3000`, контейнер и firewall.
2. Если Prometheus datasource не работает, проверь его URL `http://127.0.0.1:9090` из host network контейнера Grafana.
3. Если Prometheus target down, вернись к exporter и `prometheus/prometheus.yml`.
4. Если health metrics есть, а traffic нулевой, проверь Xray StatsService и наличие трафика.
5. Если traffic есть, а routing panels пусты, проверь webhook socket, права systemd unit и JSON API на `127.0.0.1:9101`.
6. Если только один клиент пуст, проверь значение переменной `Client`, адрес клиента и временной диапазон.
7. Если данные устарели после изменения JSON, помни, что provisioning обновляет dashboard с интервалом 30 секунд, а UI updates отключены.

### 7. Заверши проверкой и зафиксируй результат

После изменений выполни минимальный smoke test:

```bash
docker compose ps
curl -fsS http://127.0.0.1:9090/-/ready
curl -fsS http://192.0.2.10:3000/api/health
curl -fsS http://127.0.0.1:9101/metrics >/dev/null
```

Для изменений dashboard дополнительно проверь, что JSON парсится, datasource UID не изменился, а панели открываются после обновления provisioning. Зафиксируй в `docs/TESTING.md` только подтвержденные результаты и оставшиеся ограничения.

## Ограничения и безопасность

- Сохраняй Prometheus и exporter на loopback.
- Не включай anonymous access и signup без отдельного решения.
- Не публикуй `.env` и пароль Grafana.
- Не удаляй volumes при обычном обновлении.
- Не расширяй мониторинг на IPv6, OUTPUT interception или UDP без отдельной проверки существующей архитектуры.
- Для новых метрик сначала проверь их cardinality: labels должны оставаться ограниченными и пригодными для Prometheus.

## Критерии готовности

Задача считается выполненной, если:

- exporter отвечает на `127.0.0.1:9101`;
- Prometheus ready и target exporter имеет состояние `up`;
- Grafana health endpoint отвечает на `192.0.2.10:3000`;
- provisioned datasources и dashboard доступны;
- после тестового трафика health/traffic panels показывают согласованные данные;
- секреты не попали в tracked-файлы или вывод результата.
