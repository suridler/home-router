# Мониторинг: Prometheus и Grafana

Compose запускает Prometheus и Grafana. Xray и [экспортёр](../xray-routing-exporter/README.md) остаются на хосте. Контейнеры используют host networking для доступа к экспортёру на `127.0.0.1:9101`.

## Перед запуском

Проверьте адреса в [docker-compose.yml](https://github.com/suridler/home-router/blob/main/xray-monitoring-docker/docker-compose.yml) исходного репозитория. Helper bundle не устанавливает мониторинг; для этих команд нужен каталог `xray-monitoring-docker` из checkout:

| Компонент | Адрес в поставляемом compose |
| --- | --- |
| Prometheus | `127.0.0.1:9090` |
| Grafana | `192.0.2.10:3000` |
| Exporter на хосте | `127.0.0.1:9101` |

Адрес Grafana `192.0.2.10` зарезервирован для документационных примеров. Перед запуском замените `GF_SERVER_HTTP_ADDR` в compose на адрес своего сервера. Убедитесь, что выбранный адрес/порт свободен и не конфликтует с web-панелью helper. Не расширяйте listener экспортёра ради Docker.

Версии образов закреплены в compose; источник правды для них — сам файл. Dashboard `Xray Gateway` и datasource provisioning загружаются из репозитория; Infinity datasource устанавливается при старте Grafana.

## Установка

Нужны Docker Engine, Compose plugin и работающий exporter. Следующие команды запускают службы мониторинга:

```bash
docker --version
docker compose version
umask 077
cp .env.example .env
```

Задайте в `.env` уникальный `GRAFANA_ADMIN_PASSWORD`; не печатайте файл в журнал и не добавляйте его в Git. Затем:

```bash
docker compose config --quiet
docker compose pull
docker compose up -d
docker compose ps
curl --fail --silent --show-error http://127.0.0.1:9090/-/ready
```

Откройте Grafana на настроенном адресе. Учётная запись — `admin`, пароль из приватного `.env`. Данные Prometheus и Grafana сохраняются в named volumes; `docker compose down -v` удаляет их.

## Изменение dashboard

JSON dashboards находятся в `grafana/dashboards`, provisioning — в `grafana/provisioning`. Сохраняйте datasource UID и проверяйте JSON перед обновлением. Переменная Client применяется к Prometheus и Infinity API; routing events и байты — разные метрики.

Общие [архитектура](../docs/ARCHITECTURE.md) и [проверки](../docs/TESTING.md).
