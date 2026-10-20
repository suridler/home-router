# home-router

Linux-шлюз на базе Xray-core с прозрачной маршрутизацией, web-панелью,
управлением конфигурацией и отдельным сбором телеметрии.

Проект находится в beta. Web по умолчанию доступен на `127.0.0.1:3000`;
HTTP-аутентификация и детальная авторизация Helper ещё не реализованы.

## Документация

| Задача | Документ |
| --- | --- |
| Установка Helper и панели | [Установка](docs/INSTALLATION.md) |
| Настройки, диагностика и восстановление | [Эксплуатация](docs/OPERATIONS.md) |
| Компоненты и границы ответственности | [Архитектура](docs/ARCHITECTURE.md) |
| Разработка и проверки | [CONTRIBUTING.md](CONTRIBUTING.md) |
| Серверные испытания и результаты | [Тестирование](docs/TESTING.md) |
| Сборка и публикация | [Выпуск релизов](docs/RELEASING.md) |
| Оставшиеся работы | [План развития](docs/ROADMAP.md) |

## Компоненты

| Каталог | Назначение |
| --- | --- |
| [xray-core-helper](xray-core-helper/README.md) | Привилегированный daemon, CLI и Unix-socket API |
| [xrayui-web](xrayui-web/README.md) | Непривилегированная web-панель |
| [xray-routing-exporter](xray-routing-exporter/README.md) | Routing Webhook, StatsService, история и метрики |
| [xray-monitoring-docker](xray-monitoring-docker/README.md) | Prometheus и Grafana |
| [home-router](home-router/README.md) | Примеры системных конфигураций |

Xray, nftables и policy routing обрабатывают трафик независимо от Helper,
панели и мониторинга. Динамическое добавление домена в proxy выполняется
без перезапуска Xray. Начальный `proxy` может использовать прямой выход,
пока оператор не настроит настоящий прокси-сервер.

Рекомендуемая установка — готовый GitHub Release для Linux amd64/arm64.
Go на целевом сервере не требуется; для web нужны Node.js и npm.
Изменения `main` не попадают в уже выпущенные release assets.

## Лицензия

[BSD Zero Clause License (0BSD)](LICENSE). Сторонние зависимости сохраняют
собственные лицензии.
