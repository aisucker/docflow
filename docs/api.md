# REST API v1

Префикс `/api/v1`, Content-Type `application/json`, защищённые операции требуют заголовок `Authorization: Bearer <access_token>`.

## Авторизация

| Метод | Ресурс | Назначение |
|---|---|---|
| POST | `/auth/login` | `{ "email":"...", "password":"..." }`, возвращает access и refresh tokens |
| POST | `/auth/refresh` | Ротация refresh token, тело `{ "refresh_token":"..." }` |
| POST | `/auth/logout` | Отзыв переданного refresh token |
| GET | `/me` | Текущая учётная запись |

Access token действует 15 минут, refresh token — 14 дней с ротацией. Сервер хранит SHA-256 хеш refresh token и инвалидирует предыдущий при обновлении.

## Документы

| Метод | Ресурс | Назначение |
|---|---|---|
| GET | `/documents` | Журнал; `q`, `status`, `type_id`, `journal=mine\|inbox`, `limit` |
| POST | `/documents` | Создание карточки |
| GET | `/documents/{id}` | Карточка |
| PATCH | `/documents/{id}` | Правка черновика/возвращённого документа |
| DELETE | `/documents/{id}` | Мягкое удаление, только admin |
| GET | `/documents/{id}/history` | История событий |
| GET | `/documents/{id}/route` | Задачи, этапы и решения |
| POST | `/documents/{id}/start` | `{ "workflow_id":"uuid" }` |
| GET | `/tasks?status=pending` | Мои задачи |
| POST | `/tasks/{id}/decision` | `{ "decision":"approve\|reject\|return", "comment":"..." }` |

Пример создания документа:

```json
{
  "type_id": "uuid типа",
  "title": "Письмо о согласовании",
  "summary": "Согласовать условия договора",
  "registration_number": "И-001",
  "assignee_id": "",
  "department_id": "",
  "due_at": null,
  "fields": {"amount": 125000, "urgent": true}
}
```

Свойства `fields` должны соответствовать полям из выбранного типа. Сами примеры UUID с подписями нужно заменить реальными значениями.

## Справочники и администрирование

| Метод | Ресурс | Назначение |
|---|---|---|
| GET | `/document-types`, `/users`, `/departments`, `/positions`, `/workflows`, `/templates` | Списки |
| GET | `/workflows/{id}` | Определение маршрута |
| POST, PUT | `/admin/document-types` / `/admin/document-types/{id}` | Управление схемой полей |
| POST, PATCH | `/admin/users` / `/admin/users/{id}` | Создание и изменение учётных записей |
| POST | `/admin/departments`, `/admin/positions` | Добавление подразделений и должностей |
| POST, PUT | `/admin/workflows` / `/admin/workflows/{id}` | Создание и версионирование схем |
| GET | `/admin/trash` | Удалённые документы |
| POST | `/admin/documents/{id}/restore` | Восстановление документа |
| GET, POST | `/admin/webhooks` | Список и создание HTTPS подписок |
| POST | `/admin/integration-tokens` | Создание интеграционного токена (значение выдаётся один раз) |

## Файлы и шаблоны

| Метод | Ресурс | Назначение |
|---|---|---|
| GET, POST | `/documents/{id}/files` | Список и загрузка `multipart/form-data`, поле `file` |
| GET | `/files/{id}/content` | Оригинал (PDF показывается inline, DOCX скачивается) |
| GET | `/templates` | Список шаблонов |
| POST | `/admin/templates` | Загрузка DOCX, поля формы `file`, `name`, `type_id` |
| POST | `/documents/{id}/generate` | `{ "template_id":"uuid" }` — создаёт DOCX вложение |

## Интеграции

Webhooks получают JSON:

```json
{
  "id": 42,
  "event": "workflow.approved",
  "data": {"document_id":"uuid"}
}
```

Заголовок `X-Docflow-Signature: sha256=<HMAC_SHA256_hex(body, secret)>`. Секрет генерируется при создании webhook и показывается только один раз. При ошибках доставки события повторяются с увеличивающейся задержкой. Обработка должна быть идемпотентной.

Токены API выдаются под ролью `registrar` и допускаются только на `/documents`, `/document-types`, `/files` (в соответствии с дополнительными проверками серверной части). Административные endpoints и задачи по таким токенам недоступны.
