# Плейсхолдеры, Content Controls и OnlyOffice

Плейсхолдер DocJet — настройка вычисляемого текста для **элемента управления содержимым DOCX** (content control / SDT). Код настройки сопоставляется со свойством **Tag** элемента (`w:sdtPr/w:tag/@w:val`). Имя элемента (Alias) и показанный в нём текст не используются для поиска настройки.

## Настроить Tag

1. Откройте **Администрирование → Плейсхолдеры → Новый**.
2. Задайте код Tag, например `contract_subject`, и понятное название.
3. Выберите область: все типы или конкретный тип документа. Активная настройка конкретного типа переопределяет общий Tag с тем же кодом.
4. В дереве полей нажмите «Выбрать». DocJet создаст JavaScript с готовым `return`. Кнопка «＋» вставляет выражение в текущую позицию кода.
5. Выберите документ для проверки, нажмите «Вычислить» и сохраните настройку.

Примеры тела функции:

```js
return document.title;
```

```js
return helpers.text(document.fields["contract_subject"]);
```

```js
return document.assignee ? document.assignee.full_name : "Не назначен";
```

```js
return Number(document.fields["amount"] || 0).toFixed(2) + " ₸";
```

```js
return helpers.date(document.due_at);
```

Контекст `document`: id, title, summary, status, registration_number, project_number, created_at, updated_at, due_at, priority, tags; `type` (id, code, name); `author` и nullable `assignee` (id, full_name, email); nullable `department` (id, name); `fields` — исходные пользовательские поля; `resolved_fields` — объекты сотрудников для полей user/users, с ФИО, email, подразделением и должностью. Поле `users` в resolved_fields является массивом. Пустые одиночные связи дают null; используйте `?.` или условие.

`helpers.text` преобразует пустое значение в пустую строку, массив — в строки через запятую, объект — в JSON. `helpers.date` форматирует дату как ДД.ММ.ГГГГ в UTC+5. Скрипт должен синхронно вернуть строку, число или boolean. Для массивов/объектов используйте helpers.text. Promise не поддерживается.

Скрипт запускается в отдельном процессе Goja без Go-объектов, файлов, сети, require, process, console и секретов сервера. Ограничения: 16 КБ кода, 200 мс выполнения JavaScript, 3 секунды на процесс, 64 КБ результата, до двух параллельных процессов. На Windows применяется Job Object с лимитом памяти 192 МиБ; на Linux — RLIMIT_DATA 256 МиБ. Неподдерживаемые платформы не запускают вычисление. Данные документа доступны только для чтения. Код настраивает администратор; обычные пользователи получают значения только доступных им документов.

## Создать шаблон

Можно загрузить оформленный DOCX или нажать **Шаблоны → Новый → Создать через OnlyOffice**. Конструктор создаёт настоящие текстовые inline content controls: каждая строка — абзац, поле «Tag блока» добавляет элемент управления после текста. Созданный DOCX можно скачать, оформить в Word / OnlyOffice Desktop и загрузить новой версией.

В Word добавьте текстовый элемент управления на вкладке «Разработчик», откройте его свойства и задайте **Tag**, например `contract_subject`. В OnlyOffice используется соответствующий параметр Tag элемента управления содержимым. Вставленный в обычный текст код `contract_subject` не является content control.

На вкладке «Элементы управления» показываются найденные Tag, Alias, часть DOCX и настройка, которая их заполняет. Поддерживаются текстовые inline и block SDT, в том числе в ячейках таблиц и колонтитулах. Одинаковый Tag заполняется во всех вхождениях.

Генерация заменяет `sdtContent`, сохраняет сам SDT, Tag, Alias, ID, блокировки и начальные свойства абзаца/текста. Переносы и табуляция поддерживаются. Флаг показа placeholder-текста и прежняя XML data binding удаляются из заполненного элемента, чтобы Word не восстановил старое значение. Вложенные SDT, элементы таблицы целиком, изображения, checkbox, date picker, списки и другие нетекстовые controls не заполняются текстом: генерация возвращает объяснение. Помещайте текстовый control внутри нужной ячейки или контейнера. Для заполнения всех tagged controls шаблона нужны активные настройки; неизвестный Tag возвращает ошибку до сохранения файла.

Для прежних шаблонов сохранена подстановка простых `{{title}}`, `{{summary}}`, `{{registration_number}}`, `{{created_at}}` и кодов пользовательских полей в обычных текстовых элементах Word вне SDT. Новые вычисляемые настройки предназначены для content controls.

## Сформировать документ и просмотреть

В карточке черновика выберите шаблон и нажмите «Сформировать». DocJet вычислит нужные Tag, заполнит content controls и обработает результат в OnlyOffice. Готовый DOCX сохранится новой версией вложения. В аудите сохраняются версия шаблона и версии настроек Tag. При изменении карточки, шаблона или используемых настроек во время генерации файл не прикрепляется, возвращается конфликт. Генерация разрешена автору/администратору до согласования.

PDF и DOCX открываются на вкладке «Предпросмотр». Для DOCX OnlyOffice создаёт временный PDF. PDF.js рисует страницы внутри DocJet: доступны переключение страниц, масштаб, скачивание и текст страницы. Подписи и оригинальные вложения при просмотре не изменяются.

## Локальный Document Builder

В проверенной Windows-установке переносимый **ONLYOFFICE Document Builder 10.0.0** установлен на E:

```text
E:\DocJetRuntime\onlyoffice-builder-10.0.0\docbuilder.exe
```

Путь задаётся в **Администрирование → OnlyOffice** и хранится в базе. Изменение действует без перезапуска. Пакет загружен из официального релиза ONLYOFFICE; цифровая подпись exe проверена. В архиве около 81 МиБ, распакованные файлы — около 183 МиБ. Docker, WSL и службы Windows для этого режима не нужны.

**Бесплатная сборка Document Builder добавляет водяной знак.** Его удаление требует соответствующей лицензии OnlyOffice. DocJet не обходит лицензионные ограничения. На Windows Document Builder проверен созданием DOCX с SDT, вычислением Tag, генерацией нового вложения и PDF-просмотром. Версию и лицензию самого OnlyOffice обслуживает администратор.

Для повторяемой установки используйте `scripts/Install-OnlyOfficeBuilder.ps1`; по умолчанию скачивается версия 10.0.0 на диск E. Скрипт не меняет пути/секреты существующей базы автоматически.

## Встроенный веб-редактор OnlyOffice Docs

Это отдельный сервис. На текущем компьютере он **не установлен и не запущен**: Windows 11 Home, Docker/WSL отсутствуют, виртуализация отключена; для серверной Windows-установки нужны системные службы и права администратора. Работающие сейчас генерация и PDF-предпросмотр используют переносимый Builder.

Интеграция Docs реализована: встроенный просмотр вложений без редактирования, редактирование шаблонов администратором, JWT, callback с сохранением новой версии шаблона и генерация через Document Builder API Docs 10+. Протокол проверен автоматическими тестами с отдельным HTTP-сервером; живой веб-редактор в текущей Windows-среде не проверен.

Для существующего Docs укажите в настройках:

- адрес для браузера, например `http://localhost:8085`;
- адрес для backend, например `http://onlyoffice:80`;
- адрес API DocJet, доступный из Docs, например `http://api:8080`;
- JWT-секрет минимум 32 символа, совпадающий с секретом Docs.

Для Docker-установки полного стека подготовлен `docker-compose.onlyoffice.yml`:

```sh
docker compose -f docker-compose.yml -f docker-compose.onlyoffice.yml up --build -d
```

Сначала задайте `ONLYOFFICE_JWT_SECRET` в приватном `.env`. Манифест включает разрешение доступа Docs к частным адресам для внутренней сети Compose и публикует порт Docs только на loopback. Локальные файлы, журнал и очереди OnlyOffice хранятся в отдельных volumes. Это не переносит существующую native-базу Windows в Docker. Не запускайте второй стек на занятых портах без плана переноса данных.

Для внешних адресов используйте HTTPS. Нельзя отключать проверку JWT. Секрет зашифрован AES-GCM с ключом, производным от APP_SECRET, и не возвращается в API. APP_SECRET должен сохраняться при перезапусках. При его замене потребуется переустановить JWT-секрет. Переменные `ONLYOFFICE_BUILDER_PATH`, `ONLYOFFICE_URL`, `ONLYOFFICE_INTERNAL_URL`, `ONLYOFFICE_STORAGE_URL`, `ONLYOFFICE_JWT_SECRET` переопределяют настройки базы.

Docs получает только короткоживущие подписанные ссылки на конкретный ресурс с проверкой текущего пользователя и прав. Callback требует отдельный билет сессии и JWT OnlyOffice. URL сохранённого результата принимается только от настроенного Docs, перенаправления запрещены. Одновременное сохранение несовместимых версий возвращает конфликт. Вложения документов открываются только для чтения; редактировать можно шаблон с правами администратора.

## API

Префикс `/api/v1`, обычная авторизация DocJet, кроме подписанных входящих запросов Docs:

- `GET /placeholder-tags?type_id=...`; `?all=true` — администратор. Обычным пользователям исходный JavaScript не возвращается.
- `POST /admin/placeholder-tags`, `PUT /admin/placeholder-tags/{id}`: code, name, type_id, script, active; для обновления обязателен version.
- `POST /admin/placeholder-tags/test`: document_id, script; вычисление без записи.
- `GET /documents/{id}/placeholder-values`: значения активных Tag доступного документа.
- `GET/PUT /admin/office`, `GET /office/status`.
- `POST /admin/templates/create`: name, type_id, blocks [{text, tag, label}].
- `PUT /admin/templates/{id}/file`: multipart file и version.
- `GET /templates/{id}/controls`, `/content`, `/preview`, `/office?edit=true`.
- `GET /files/{id}/preview`, `/office`.
- Существующий `POST /documents/{id}/generate`: template_id.

Ограничения: исходный DOCX до 20 МиБ, до 1000 частей ZIP и 80 МиБ распакованного содержимого; до 100 разных Tag; два параллельных задания OnlyOffice; до 75 секунд на OnlyOffice. Генерация целиком ограничена 100 секундами. Неподдерживаемый или повреждённый документ не сохраняется как готовое вложение. Старые файлы шаблонов не удаляются при замене, текущая версия задаётся в templates.

## Официальные источники

- [Content controls и Tag](https://api.onlyoffice.com/docs/office-api/usage-api/document-api/ApiInlineLvlSdt/Methods/GetTag/).
- [Список элементов управления](https://api.onlyoffice.com/docs/office-api/usage-api/document-api/ApiDocument/Methods/GetAllContentControls/).
- [Установка Document Builder и лицензия бесплатной сборки](https://api.onlyoffice.com/docs/document-builder/get-started/installing).
- [CLI Document Builder](https://api.onlyoffice.com/docs/document-builder/using-cli/overview).
- [Веб-API Document Builder](https://api.onlyoffice.com/docs/docs-api/additional-api/document-builder-api).
- [JWT и безопасность Docs](https://api.onlyoffice.com/docs/docs-api/get-started/how-it-works/security/).
- [Установка Docs в Docker](https://helpcenter.onlyoffice.com/docs/installation/docs-community-install-docker.aspx).
- [PDF.js](https://mozilla.github.io/pdf.js/examples/).
