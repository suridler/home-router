# Разработка

Обзор — [README](README.md), границы — [архитектура](docs/ARCHITECTURE.md),
следующие работы — [roadmap](docs/ROADMAP.md). Проверяйте текущий код:
старый номер задачи не доказывает, что возможность ещё отсутствует.

## Проверки

Из корня repo, последовательно для Go с постоянными caches:

```bash
cd xray-core-helper
gofmt -d ./cmd ./internal
go test ./...
go test -race ./...
go build ./cmd/...
go vet ./...
cd ../xray-routing-exporter
go test ./...
go build ./cmd/...
cd ../xrayui-web
npm ci
npm test
```

Версии Go задаются go.mod; CI web использует Node 22.
Npm ci нужен при первой установке dependencies или изменении lockfile.
Не запускайте одновременно дублирующиеся full/race/build/vet с холодным cache.

Для packaging/harness без обращения к host services:

```bash
cd xray-core-helper
python3 -B -m unittest discover -s packaging -p 'test_*.py'
go test ./packaging
bash -n packaging/install-from-github.sh
bash -n packaging/install-bundle.sh
```

Для документации: git diff --check, относительные ссылки и синтаксис примеров.
Изменение release builder также требует проверки упаковки docs в обоих архивах.

Host integration — отдельная разрешённая оператором работа:

```bash
cd xrayui-web
sudo npm run test:integration
```

Permission/sandbox skip не равен app failure и не равен PASS.
[Серверная приёмка](docs/TESTING.md) не заменяется unit tests.

## Правила реализации

- Helper — единственный прикладной writer Xray config и managed assets.
- Web — typed socket client, без чтения config и privileged commands.
- Exporter сохраняет независимые webhook/StatsService и telemetry storage.
- JSONC edits сохраняют comments/unknown fields; protected DNS/TProxy objects,
  generation/hash guards остаются на стороне Helper.
- Restart и опасные сетевые действия явно отражаются в интерфейсе.
- Native template semantics принадлежат файлам, не Go rule generator.
- Тесты используют temp files/fakes; не меняют host Xray/systemd/DB.
- Runtime DB, raw config, credentials, VLESS URI, tokens и audits не коммитятся.

## Документация и изменения

README компонентов содержит назначение, запуск и ссылки. Общие процедуры —
docs/, контракты Helper — xray-core-helper/docs/, подробный T8 остаётся рядом
с bundle. Новые планы и промежуточные отчёты на каждую подзадачу не создаются:
roadmap обновляется в одном месте, результаты приёмки добавляются в TESTING.
Старые решения доступны в истории Git.

В PR укажите изменение поведения, проверки и ограничения. Реализованное
описывайте отдельно от запланированного. Не запускайте установку/рестарт
сервисов ради проверки документации.

[Выпуск релиза](docs/RELEASING.md)
