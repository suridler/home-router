# Сборка и выпуск helper

Workflow: `.github/workflows/helper-release.yml` в корне workspace. Триггер —
публикация тега `helper-v*`; ручной `workflow_dispatch` принимает уже существующий
тег. Checkout, тесты, бинарники и все файлы комплекта относятся к одному commit.
Наличие workflow в feature-ветке не обновляет `main` и не создаёт релиз само по себе.

## Что публикуется

- `xray-core-helper-vVERSION-linux-amd64.tar.gz`;
- `xray-core-helper-vVERSION-linux-arm64.tar.gz`;
- raw CLI `xray-core-helper-vVERSION-linux-amd64` и `...-arm64`;
- `SHA256SUMS` с SHA256 двух архивов и двух raw CLI (четыре строки);
- `install-from-github.sh` из того же committed snapshot.

В каждом архиве — CLI/daemon, сборщик `domain-list-community` для той же архитектуры,
native templates (единственный Xray-шаблон `templConf/xray/config.json` с bindings),
systemd/sysusers/tmpfiles assets,
скрипты установки/проверок, runtime-исходники web, документация и `release.json`.
Runtime configs, базы, node_modules и локальная незакоммиченная работа не пакуются.
Web dependencies устанавливаются через npm по вложенному lockfile. Для helper
используется `CGO_ENABLED=0`, `-trimpath`, `-mod=readonly`; amd64 имеет базовый GOAMD64=v1.
Daemon сообщает release version и сокращённый commit через health.

Репозиторий релизов публичный: API, raw CLI и `SHA256SUMS` доступны без `gh` и
токена. Bootstrap скачивает только raw CLI и контрольные суммы; API может
использовать сохранённую проверенную копию текущего installer для staging архива.
`release.json` имеет schema1 и необязательное поле `minimum_installable_version`;
без него нижней границы нет. При наличии оно берётся из `packaging/release-policy.json`
и должно быть не выше версии релиза. Это ограничение downgrade, а не замена
проверкам целостности, архитектуры и совместимости.
Начиная с beta.9 минимальная устанавливаемая версия — beta.9: схема
транзакционной БД обновлена до 2 для истории принятых конфигураций.
Откат незавершённого обновления использует сохранённый комплект и данные,
отдельно от обычного downgrade.

Версия сборщика закреплена в `packaging/dlc_builder.py` на upstream commit
`6f3acc3ba95299031cf408232e2e65e2c892fd2d`; Go module и go.mod проверяются по
закреплённым h1-суммам. Эта версия сохраняет имя выходного файла, выбранное через
`geodata.user_geosite` (по умолчанию `geosite_user.dat`).
Версия/commit/суммы исходника включены в `release.json`,
SHA256 бинарника — в `binaries`; MIT license включена в архив и устанавливается
в `/usr/local/share/licenses/xray-core-helper/`. Смена pin требует проверки
аргументов helper, USERLIST и точного имени выходного файла на временных данных.

CI сначала выполняет Go test/race/build/vet, web unit tests и Python packaging
fixtures. Потом последовательно собирает Linux amd64/arm64. Обе сборки должны
завершиться до создания release. Assets загружаются в draft, после чего release
публикуется. При ошибке загрузки остаётся draft для разбора. Существующие релизы
не перезаписываются; повторный выпуск требует новой версии. Версия с суффиксом
(например `helper-v0.2.0-beta.1`) публикуется как prerelease.

## Выпуск

В примере замените FULL_PUBLISHED_COMMIT_SHA и helper-vX.Y.Z на проверенный
commit и новую незанятую версию. Не используйте существующий tag повторно.

Сначала опубликуйте проверенный commit с workflow и всеми изменениями. На рабочей
машине с GitHub-доступом создайте тег именно на нём:

```bash
RELEASE_COMMIT=FULL_PUBLISHED_COMMIT_SHA
VERSION=helper-vX.Y.Z
git tag "$VERSION" "$RELEASE_COMMIT"
git push origin "refs/tags/$VERSION"
```

Дождитесь успешного workflow и проверьте полный набор assets, включая оба raw CLI
и четыре строки `SHA256SUMS`. Публикация prerelease не означает успешного T8 на
реальном сервере. Основная реализация U6 и локальные тесты есть, но U6.12 — два
реальных release, upgrade/downgrade, retry/rollback и восстановление после
прерывания — остаётся операторской приёмкой. Следующий шаг — чистый snapshot,
[релизный installer](INSTALLATION.md) и [T8](../xray-core-helper/packaging/T8_MANUAL_SERVER_TEST.md).
Пользователю сервера не нужны `gh auth login` или токен: репозиторий публичный.

## Локальная сборка

```bash
python3 xray-core-helper/packaging/build-release.py helper-v0.2.0-beta.3 /tmp/helper-dist
```

Команда берёт **committed HEAD**, а не изменённые файлы рабочего дерева. Нужны
Go согласно go.mod, Git и Python; сборка не касается служб или `/etc`. Каталог
вывода должен быть новым: архивы, raw CLI и контрольные суммы не перезаписываются.
SHA256 выявляет повреждение скачанного asset; доверие к издателю обеспечивается
публикацией GitHub release из проверенного commit. Криптографические attestations
в этот этап не входят. Репозиторий публичный, поэтому `gh` и токен не требуются.
Локальная сборка и публикация не запускают обновление текущего хоста.
