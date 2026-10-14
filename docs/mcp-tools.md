# MCP-инструменты ассистента разработчика (API)

Сервер `day31-git` версии 1.0.0 (`internal/gitmcp`, запуск — `day-31/mcp-server`, транспорт stdio).
Все инструменты только читают репозиторий: модель передаёт в лучшем случае префикс пути,
а не команду, поэтому записать, удалить или запушить через сервер нельзя. Пустой путь и `.`
означают весь репозиторий; путь, начинающийся с `-`, `:` или `/`, и путь с выходом за корень (`..`)
отклоняются.

| Инструмент | Аргументы | Что возвращает | git под капотом |
|---|---|---|---|
| `git_branch` | — | `branch`, `commit` (короткий хеш), `subject` последнего коммита, `detached` | `git branch --show-current`, `git log -1` |
| `git_status` | — | `branch`, `changed` (строки `git status --porcelain`), `clean` | `git status --porcelain=v1 --untracked-files=all` |
| `list_files` | `prefix` (необязательный) | `files` (до 400 путей: под контролем git и новые неигнорируемые; удалённые с диска не показываются), `total`, `truncated` | `git ls-files --cached --others --exclude-standard [-- prefix]` |
| `git_diff` | `path` (необязательный) | `stat`, `diff` (до 12 000 символов), `truncated` | `git diff HEAD [--stat] [-- path]` |

## Как ассистент их использует

- `git_branch` вызывает **код** ассистента перед каждым ответом `/help` — текущая ветка всегда
  в контексте модели.
- Остальные инструменты модель вызывает **сама** через function calling, когда вопрос касается
  файлов, папок или незакоммиченных изменений.

## Пример ответа `git_branch`

```json
{"branch": "main", "commit": "51d482e", "subject": "Add day 27 local LLM web chat with captured two-turn run", "detached": false}
```
