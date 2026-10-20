# Xray Core Helper

Привилегированный локальный control plane для Xray: анализ и настройка установленного Xray, типизированное редактирование JSONC, динамический список доменов и IPv4, geodata и безопасное применение native-шаблонов.

`xray-core-helperd` владеет обычными изменениями; CLI `xray-core-helper` и web-панель обращаются к нему через Unix socket. Исключение — самостоятельный режим установки/setup/recovery. Остановка helper не останавливает передачу трафика.

## Начало работы

- [Установка и первичная настройка](../docs/INSTALLATION.md).
- [Эксплуатация и восстановление](../docs/OPERATIONS.md).
- [Архитектура и границы компонентов](../docs/ARCHITECTURE.md).
- [Проверки и результаты T8](../docs/TESTING.md).
- [Сборка и выпуск релиза](../docs/RELEASING.md).

Установщик определяет бинарник, единственный конфиг и сервисную идентичность по эффективным настройкам `xray.service`. Каталог assets задаётся явным `XRAY_LOCATION_ASSET`. При неподдерживаемой конфигурации установка останавливается; отдельного параметра пути Xray в helper TOML нет.

Наличие конфига Xray не означает, что helper настроен. Первичная настройка запускается отдельно:

```bash
sudo /usr/local/bin/xray-core-helper setup
sudo /usr/local/bin/xray-core-helper setup --status
sudo /usr/local/bin/xray-core-helper health
sudo /usr/local/bin/xray-core-helper safe-apply status
```

`setup` — самостоятельный режим CLI без обращений к daemon: останавливает helper/web, берёт runtime lock и выполняет настройку в процессе. `setup --status` читает состояние через обычный socket; `setup --recover` восстанавливает состояние после прерванной настройки в том же автономном режиме. Диалог анализирует существующий конфиг или подготавливает начальный, предлагает подготовить сеть и отдельно включить TProxy с подтверждением. Начальный outbound `proxy` выходит напрямую, пока оператор не задаст реальный прокси.

## Динамические домены и IPv4

```bash
sudo /usr/local/bin/xray-core-helper proxy-domain add example.com
sudo /usr/local/bin/xray-core-helper proxy-domain add 192.0.2.1
sudo /usr/local/bin/xray-core-helper proxy-domain list
sudo /usr/local/bin/xray-core-helper proxy-domain delete example.com
sudo /usr/local/bin/xray-core-helper proxy-domain delete 192.0.2.1
```

Команда `proxy-domain`, endpoint `/v1/proxy-domains` и поле `domain` сохранены;
значение может быть доменом или IPv4, тип не выбирается вручную. Нормализацию,
классификацию и отклонение IPv6/CIDR/URL/некорректного IPv4 выполняет helper, а
не CLI. Список живёт в той же SQLite-хранилище `desired`/`baseline`; второго
хранилища и офлайн-записи нет.

Add/delete меняют desired и синхронизируют runtime overlay без рестарта Xray.
`applied`/`applied:true` подтверждает применение overlay, но не запись в
`geosite_user.dat`/`geoip_user.dat`: файлы пары USERLIST обновляются
консолидацией при `geodata update` после согласования runtime. При
`domain_runtime_sync_failed` или `domain_runtime_apply_failed` desired мог
сохраниться — перечитайте `proxy-domain list` и выполните явный
`proxy-domain apply` после исправления; мутацию автоматически не повторяйте.
Подробный контракт — в [API](docs/API.md) и [эксплуатации](../docs/OPERATIONS.md).

## Конфигурация

Настройки helper: `/etc/xray-core-helper/config.toml`; доступные поля и значения по умолчанию — в [config.example.toml](config.example.toml). SQLite хранит служебное состояние, а не вторую копию конфига Xray.

Web слушает loopback; параметры listener и разрешение изменений принадлежат секции `[web]` helper. Runtime-файл `/run/xray-core-helper/web.env` передаёт web путь socket и временные данные первичной настройки. Он не предназначен для ручного редактирования.

## Контракты разработчика

- [API](docs/API.md): проекции, изменения, поколения и safe-apply.
- [Native templates](docs/NATIVE_TEMPLATES.md): аннотации, границы блока и восстановление.
- [Каталог шаблонов](templConf/README.md): рабочие и системные файлы.
- [Contributing](../CONTRIBUTING.md): проверки и правила изменений.

Нельзя добавлять произвольные пути, shell-команды или raw JSON API. JSONC изменяется узко с сохранением комментариев и неизвестных полей. Нативные правила принадлежат шаблонам; helper не генерирует за них сетевую политику.

Beta не реализует разграничение всех API по отдельным ролям клиентов socket. Доступ к socket доверенный; web не имеет HTTP-аутентификации.
