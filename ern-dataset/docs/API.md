# HTTP API сервиса данных ЕРН

Базовый адрес: `http://localhost:8091` (меняется флагом `-addr`).
Формат ответов — JSON, кодировка UTF-8. Авторизации нет: сервис рассчитан на
внутренний контур.

Все ошибки отдаются в одном виде:

```json
{ "error": "текст причины" }
```

---

## GET /health

Состояние сервиса вместе с проверкой датасета. Используется для мониторинга и
для быстрой проверки после замены файла.

**200 OK** — датасет читается и разбирается:

```json
{
  "status": "ok",
  "version": "v1.0.0",
  "file": "dataset.json",
  "size": 139424,
  "sha256": "3f5c…",
  "counts": {
    "persons": 230,
    "kinship": 37,
    "civil_registry": 18,
    "divorce": 7,
    "tax_registration": 46
  },
  "checked": "2026-11-20T09:15:00Z"
}
```

**503 Service Unavailable** — файл пропал, пуст или не разбирается:

```json
{ "error": "датасет не разбирается: разбор JSON: unexpected end of JSON input" }
```

---

## GET /api/v1/meta

Метаданные файла. Нужны, чтобы понять, тот ли датасет забрал потребитель:
`sha256` меняется при любой правке файла.

**200 OK**:

```json
{
  "file": "dataset.json",
  "size": 139424,
  "modified": "2026-11-20T09:10:41Z",
  "sha256": "3f5c…",
  "counts": {
    "persons": 230,
    "kinship": 37,
    "civil_registry": 18,
    "divorce": 7,
    "tax_registration": 46
  }
}
```

Поля `modified` и `size` берутся из файловой системы, `sha256` считается по
содержимому, `counts` — по фактическому разбору.

---

## GET /api/v1/bundle

Датасет целиком, в том же виде, в котором он лежит в файле.

**Заголовки ответа**:

| Заголовок | Значение |
|---|---|
| `Content-Type` | `application/json; charset=utf-8` |
| `ETag` | `"3f5c…"` — первые 16 символов SHA-256 |
| `Cache-Control` | `no-cache` |
| `X-Dataset-SHA256` | полная контрольная сумма файла |
| `X-Dataset-Version` | версия из `-version` |

**Условный запрос.** Если передать `If-None-Match` с ранее полученным `ETag`
и файл не менялся, сервис отвечает `304 Not Modified` без тела:

```powershell
curl -H 'If-None-Match: "3f5c1a2b3c4d5e6f"' http://localhost:8091/api/v1/bundle
```

**200 OK** — тело:

```json
{
  "persons": [
    {
      "id": "P-185",
      "full_name": "ЗАЙЦЕВ ЕГОР СЕРГЕЕВИЧ",
      "passport": "6697 478160",
      "address": "г. Нижний Тагил, ул. ГАГАРИНА, д. 100, кв. 61",
      "city": "г. Нижний Тагил",
      "street": "ул. ГАГАРИНА",
      "house": "д. 100",
      "birth_date": "05.12.1964",
      "snils": "…",
      "inn": "…",
      "citizenships": ["RF"]
    }
  ],
  "kinship": [
    { "a": "DL-014", "b": "P-185", "kind": "sibling", "civil_id": "ZAGS-004" }
  ],
  "civil_registry": [
    { "id": "ZAGS-004", "a": "DL-014", "b": "P-185", "kind": "sibling" }
  ],
  "divorce": [],
  "guardianship": [],
  "emancipation": [],
  "parental_rights": [],
  "legal_capacity": [],
  "employment_service": [],
  "tax_registration": [],
  "driver_license": []
}
```

Секции, которых нет в файле, в ответе отсутствуют. Потребитель обязан
отвергать неизвестные поля — так `coi-1` и делает.

---

## Коды ответов

| Код | Когда |
|---|---|
| `200` | успех |
| `304` | `If-None-Match` совпал с текущим `ETag` |
| `404` | неизвестный маршрут, тело: `{"error":"неизвестный маршрут: /…"}` |
| `500` | файл есть, но прочитать не удалось |
| `503` | `/health`: датасет недоступен или не разбирается |

## Примеры

```powershell
# живость
curl http://localhost:8091/health

# метаданные
curl http://localhost:8091/api/v1/meta

# датасет в файл
curl -o dataset.json http://localhost:8091/api/v1/bundle

# сравнить с локальным файлом
(Get-FileHash .\data\dataset.json -Algorithm SHA256).Hash
```
