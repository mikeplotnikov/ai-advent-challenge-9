# День 30 — локальная LLM как приватный сервис

Qwen3 4B Instruct работает в Ollama на MacBook («домашний сервер»). Перед ней Go-сервис
с HTTP API ([main.go](main.go)). Он добавляет то, что нужно модели, которой пользуются другие:

- **токен доступа** — `Authorization: Bearer …`, без него 401;
- **rate limit** — 8 запросов в минуту с одного IP (берётся из `CF-Connecting-IP`), сверх лимита 429 с `Retry-After`;
- **очередь** — модель генерирует по одному запросу, остальные ждут; в ответе видно, сколько запросов было впереди и сколько длилось ожидание;
- **max context** — до 8000 символов на весь диалог, сверх лимита 413 ещё до модели. `num_ctx` 4096, ответ до 512 токенов, `truncate:false` — Ollama не обрезает контекст молча.

Наружу сервис открывает Cloudflare Quick Tunnel: публичный https-адрес без проброса портов.
Адрес живёт, пока запущен процесс.

## Запуск

```sh
brew install cloudflared
brew services start ollama
ollama pull qwen3:4b-instruct-2507-q4_K_M
./day-30/run.sh
```

Скрипт печатает адрес API и ссылку на витрину `https://challeng.mikeproject.dev/day-30/#api=…&token=…`.
Токен генерируется заново при каждом запуске. Ctrl+C останавливает сервис и туннель.

## API

```sh
curl -s https://….trycloudflare.com/api/chat \
  -H "Authorization: Bearer $DAY30_TOKEN" \
  -d '{"messages":[{"role":"user","content":"Привет!"}]}'
```

- `POST /api/chat` — `{messages:[{role, content}]}`, где role — `user` или `assistant`; историю диалога передаёт клиент.
- `GET /api/status` — лимиты, длина очереди, журнал последних запросов (с токеном).
- `GET /health` — без токена.

## Проверка через сеть

`DAY30_TOKEN=… go run ./day-30 -check https://….trycloudflare.com` ([check.go](check.go)) ходит
по публичному адресу. Проверяет: доступность, 401 без токена, 413 на 8001 символ, 200 ровно на
лимите (токенов промпта + ответа ≤ `num_ctx`), два одновременных диалога с памятью и залп
сверх лимита. Результат — [check.json](check.json), его показывает витрина.
