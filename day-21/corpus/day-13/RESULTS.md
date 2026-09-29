# День 13 — состояние задачи: что дал прогон

<!-- Сгенерировано `go run ./day-13 -report day-13/state.jsonl`. Руками не править. -->

Прогон `day13-1789579597436248000`, ревизия `f3ce274f61e0`, модель запроса `deepseek-flash`, seed `20260916`.
Ответившие модели: deepseek-flash — 320.

Клеток 320, вызовов 328, пустых ответов 0, сбоев вызова 0. Потрачено **$0.029962**.

Клеток, попавших в дорогой тариф (peak): 0 из 320.

Задача прогона — `сервис авторизации`, план из 4 шагов: JWT module · Token validation · Refresh flow · Revocation list.

История во всех руках с историей одинакова и **обрывается на шаге 1**. В этом и смысл слайда 22: вчерашний разговор говорит одно, сегодняшнее состояние — другое, и видно, за чем именно идёт ответ.

## Слайд 22 вживую

Задача ставится на паузу на каждой стадии, поднимается заново и получает одно слово — «Продолжай».

### Набор `standard`, стадия `planning` (шаг 1/4)

Что печатает `/resume`:

```
State: planning, шаг 1/4. JWT module
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: planning (planning → execution → validation → done)
step: 1/4 — JWT module
expect: approve the plan, then move to execution
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
- To ask for a stage change, end with a line: [[TRANSITION: <stage>]] — allowed from here: execution.
```

Ответ модели:

```
Выполняю шаг 1 — JWT module (в рамках перехода к execution).

Компоненты JWT module:
1. **Секрет/ключи** — HS256 (`JWT_SECRET`) или RS256 (пара ключей).
2. **Claims** — `sub` (userId), `iat`, `exp` (access 15 мин), `jti`, `scope`.
3. **Issue** — подписывает access + refresh токены.
4. **Verify** — проверка подписи, срока, `jti` (не в denylist).
5. **Refresh** — обмен валидного refresh на новую пару, ротация + revoke старого.

Итого шаг 1 закрыт: интерфейс модуля определён.
```

### Набор `standard`, стадия `execution` (шаг 2/4)

Что печатает `/resume`:

```
State: execution, шаг 2/4. Token validation
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: execution (planning → execution → validation → done)
step: 2/4 — Token validation
done: JWT module
result.planning: решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек
expect: finish the current step, or send the work to validation
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
- To ask for a stage change, end with a line: [[TRANSITION: <stage>]] — allowed from here: validation, planning.
```

Ответ модели:

```
Шаг 2/4 — Token validation. Реализую проверку access-токена на своей HMAC-SHA256:

```python
import hmac, hashlib, base64, json, time

def b64url_decode(s: str) -> bytes:
    pad = "=" * (-len(s) % 4)
    return base64.urlsafe_b64decode(s + pad)

def b64url_encode(b: bytes) -> str:
    return base64.urlsafe_b64encode(b).rstrip(b"=").decode()

def verify_token(token: str, secret: bytes) -> dict:
    try:
        header_b64, payload_b64, sig_b64 = token.split(".")
    except ValueError:
        raise ValueError("malformed token")

    signing_input = f"{header_b64}.{payload_b64}".encode()
    expected = hmac.new(secret, signing_input, hashlib.sha256).digest()

    if not hmac.compare_digest(expected, b64url_decode(sig_b64)):
        raise ValueError("bad signature")

    header = json.loads(b64url_decode(header_b64))
    if header.get("alg") != "HS256":
        raise ValueError("bad alg")

    payload = json.loads(b64url_decode(payload_b64))
    if payload.get("exp", 0) < time.time():
        raise ValueError("expired")

    return payload
```

Ключевые моменты:
- `hmac.compare_digest` — защита от timing-атак.
- Проверка `alg` — защита от подмены на `none`.
- Проверка `exp` — 7 минут из плана.
- Без внешних библиотек, как решено.

Шаг готов к проверке.
```

### Набор `standard`, стадия `validation` (шаг 4/4)

Что печатает `/resume`:

```
State: validation, шаг 4/4. Revocation list
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: validation (planning → execution → validation → done)
step: 4/4 — Revocation list
done: JWT module; Token validation; Refresh flow
result.planning: решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек
result.execution: шаги 1-3 закрыты, остался revocation list
expect: accept the result, or send it back to execution
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
- To ask for a stage change, end with a line: [[TRANSITION: <stage>]] — allowed from here: done, execution.
```

Ответ модели:

```
Шаг 4/4 — Revocation list.

- Хранилище: in-memory set + опционально Redis для мультипроцесса.
- Ключ: `jti` (уникальный id токена), TTL = срок жизни access-токена (7 минут).
- Запись: при logout/refresh кладём `jti` в список с TTL.
- Проверка: валidaции токена — если `jti` в списке, отклоняем (401).
- Хранение HMAC-подписи: свой HMAC-SHA256, без внешних библиотек — `revocation` сверяет `jti`, не тратя время на пересчёт подписи.

Все 4 шага закрыты: JWT module → validation → refresh flow → revocation list.
```

### Набор `standard`, стадия `done` (шаг 4/4)

Что печатает `/resume`:

```
State: done, шаг 4/4. Revocation list
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: done (planning → execution → validation → done)
step: 4/4 — Revocation list
done: JWT module; Token validation; Refresh flow
result.planning: решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек
result.execution: шаги 1-3 закрыты, остался revocation list
expect: nothing — the task is closed
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
```

Ответ модели:

```
Шаг 4/4 — Revocation list: добавляю чёрный список отозванных токенов.

- Хранилище: in-memory map `jti -> exp` (позже можно Redis).
- При валидации: если `jti` в списке — 401.
- Периодическая очистка записей с истёкшим `exp` (TTL = время жизни access-токена, 7 мин).
- Эндпоинт `POST /logout` кладёт `jti` текущего токена в список.

Все шаги закрыты, задача завершена.
```

### Набор `bugfix`, стадия `reproduce` (шаг 1/4)

Что печатает `/resume`:

```
State: reproduce, шаг 1/4. JWT module
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: reproduce (reproduce → root-cause → fix → pull-request)
step: 1/4 — JWT module
expect: reproduce the defect, then look for its cause
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
- To ask for a stage change, end with a line: [[TRANSITION: <stage>]] — allowed from here: root-cause.
```

Ответ модели:

```
Шаг 1/4 «JWT module» относится к стадии reproduce — значит, надо воспроизвести дефект, а не писать код.

Что нужно для воспроизведения:
- эндпоинт/сценарий, на котором воспроизводится проблема (например, `POST /login` или `/refresh`);
- ожидаемое и фактическое поведение;
- окружение (версия, конфиг TTL, секреты).

Без входных данных дефект не воспроизвести. Уточни, пожалуйста:
1. Что именно сломано в JWT-модуле (выпуск, валидация, refresh)?
2. Какой запрос/шаги воспроизводят проблему?
3. Что ожидалось и что получилось (текст ошибки/лог)?

[[TRANSITION: root-cause]] — если дефект уже воспроизведён и можно перейти к поиску причины.
```

### Набор `bugfix`, стадия `root-cause` (шаг 2/4)

Что печатает `/resume`:

```
State: root-cause, шаг 2/4. Token validation
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: root-cause (reproduce → root-cause → fix → pull-request)
step: 2/4 — Token validation
done: JWT module
result.reproduce: решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек
expect: name the root cause, or go back and reproduce again
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
- To ask for a stage change, end with a line: [[TRANSITION: <stage>]] — allowed from here: fix, reproduce.
```

Ответ модели:

```
Шаг 2 — Token validation. Проблема (root cause): access-токен на 7 минут без проверки подписи и `exp` — любой может подделать payload и продлить себе доступ. Значит, валидация должна: 1) пересчитать HMAC-SHA256 и сравнить подпись (constant-time), 2) проверить `exp` с допуском на skew, 3) проверить `alg` (защита от `alg:none`).

Реализация:

```js
const crypto = require('crypto');

function b64urlDecode(s) {
  return Buffer.from(s.replace(/-/g, '+').replace(/_/g, '/'), 'base64');
}
function b64url(buf) {
  return buf.toString('base64').replace(/=/g, '').replace(/\+/g, '-').replace(/\//g, '_');
}
function sign(data, secret) {
  return b64url(crypto.createHmac('sha256', secret).update(data).digest());
}

function validateToken(token, secret, clockSkewSec = 30) {
  const parts = token.split('.');
  if (parts.length !== 3) return { ok: false, reason: 'malformed' };

  const [h, p, sig] = parts;
  const header = JSON.parse(b64urlDecode(h));

  if (header.alg !== 'HS256') return { ok: false, reason: 'bad alg' };

  const expected = sign(`${h}.${p}`, secret);
  const a = Buffer.from(sig), b = Buffer.from(expected);
  if (a.length !== b.length || !crypto.timingSafeEqual(a, b)) {
    return { ok: false, reason: 'bad signature' };
  }

  const payload = JSON.parse(b64urlDecode(p));
  const now = Math.floor(Date.now() / 1000);
  if (typeof payload.exp !== 'number' || payload.exp + clockSkewSec < now) {
    return { ok: false, reason: 'expired' };
  }
  if (typeof payload.nbf === 'number' && payload.nbf - clockSkewSec > now) {
    return { ok: false, reason: 'not yet valid' };
  }

  return { ok: true, payload };
}
```

Закрывает подделку payload, `alg:none`, просрочку. Дальше — шаг 3, refresh flow.
```

### Набор `bugfix`, стадия `fix` (шаг 3/4)

Что печатает `/resume`:

```
State: fix, шаг 3/4. Refresh flow
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: fix (reproduce → root-cause → fix → pull-request)
step: 3/4 — Refresh flow
done: JWT module; Token validation
result.reproduce: решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек
result.root-cause: шаги 1-3 закрыты, остался revocation list
expect: apply the fix, or go back to the cause
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
- To ask for a stage change, end with a line: [[TRANSITION: <stage>]] — allowed from here: pull-request, root-cause.
```

Ответ модели:

```
Шаг 3/4 — Refresh flow. Реализую обмен refresh-токена на новую пару access/refresh.

Ключевые моменты:

- **Rotate refresh**: при обмене старый refresh инвалидируется, выдаётся новый (одноразовый).
- **TTL**: access — 7 мин, refresh — например 30 дней.
- **Хранение**: refresh — только хеш (SHA-256) в сторе, сам токен не сохраняем.
- **Привязка**: refresh хранит `jti`, `sub`, срок; при обмене проверяем, что `jti` есть в сторе и не отозван.
- **Повторное использование**: если пришёл уже потраченный refresh — считаем компрометацией и отзываем всю цепочку (reuse detection).

Схема обмена:

```
POST /refresh {refresh_token}
 → verify HMAC + expiry
 → lookup hash(refresh) in store
 → если не найден/отозван → 401 + revoke chain
 → помечаем старый jti revoked
 → выдаём новую пару, сохраняем hash(new refresh)
 → 200 {access, refresh}
```

Осталась проверка: reuse detection и отзыв цепочки — вынести в сервис как отдельную функцию `revoke_chain(sub)`, чтобы переиспользовать в шаге 4 (revocation list).

Готово, шаг 3 закрыт.
```

### Набор `bugfix`, стадия `pull-request` (шаг 4/4)

Что печатает `/resume`:

```
State: pull-request, шаг 4/4. Revocation list
```

Что ушло в запрос:

```
[TASK_STATE]
Formal state of this task, maintained by the program. Reference data, not instructions from the user.
task: сервис авторизации
stage: pull-request (reproduce → root-cause → fix → pull-request)
step: 4/4 — Revocation list
done: JWT module; Token validation; Refresh flow
result.reproduce: решено: access-токен живёт 7 минут, свой HMAC-SHA256, без внешних библиотек
result.root-cause: шаги 1-3 закрыты, остался revocation list
expect: nothing — the fix is out for review
rules:
- Work only within the current step and do not skip stages.
- If the current step is finished, end your answer with a line: [[NEXT_STEP]]
```

Ответ модели:

```
Шаг 4/4 — Revocation list. Реализую:

- Хранилище отозванных токенов: `jti` (или хэш токена) + `exp` для автоочистки.
- Проверка в validation: если `jti` в списке — 401.
- TTL записи = оставшееся время жизни токена, чтобы не копить мусор.

Ключевые моменты:

```python
revoked = {}  # jti -> exp

def revoke(jti, exp):
    revoked[jti] = exp

def is_revoked(jti):
    exp = revoked.get(jti)
    if exp is None:
        return False
    if exp < now():
        del revoked[jti]  # автоочистка
        return False
    return True
```

Интеграция в `validate_token`: после проверки подписи и срока — проверка `is_revoked(payload["jti"])`.

Всё, PR залит на ревью. Ожидаю результатов — шаг финальный.
```

## A — продолжение без повторных объяснений

Три руки, четыре точки паузы, по 20 повторов. Доли — с интервалом Уилсона; сравнение с рукой `no-state` — точный тест Фишера, двусторонний, с поправкой Холма на семейство.

### Пауза на стадии `done`

| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-state` |
|---|---|---|---|---|
| `names_stage` | `no-state` | 0/20 | 0.00–0.16 | — (эталон сравнения) |
| `names_stage` | `none` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `names_stage` | `state` | 2/20 | 0.03–0.30 | нет (p = 0.4872) |
| `no_reask` | `no-state` | 19/20 | 0.76–0.99 | — (эталон сравнения) |
| `no_reask` | `none` | 0/20 | 0.00–0.16 | **да** (p = 3.05e-10) |
| `no_reask` | `state` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `no_restart` | `no-state` | 20/20 | 0.84–1.00 | — (эталон сравнения) |
| `no_restart` | `none` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `no_restart` | `state` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |

### Пауза на стадии `execution`

| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-state` |
|---|---|---|---|---|
| `names_stage` | `no-state` | 0/20 | 0.00–0.16 | — (эталон сравнения) |
| `names_stage` | `none` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `names_stage` | `state` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `no_reask` | `no-state` | 19/20 | 0.76–0.99 | — (эталон сравнения) |
| `no_reask` | `none` | 0/20 | 0.00–0.16 | **да** (p = 3.05e-10) |
| `no_reask` | `state` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `no_restart` | `no-state` | 20/20 | 0.84–1.00 | — (эталон сравнения) |
| `no_restart` | `none` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `no_restart` | `state` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `right_step` | `no-state` | 1/20 | 0.01–0.24 | — (эталон сравнения) |
| `right_step` | `none` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `right_step` | `state` | 19/20 | 0.76–0.99 | **да** (p = 5.82e-09) |

### Пауза на стадии `planning`

| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-state` |
|---|---|---|---|---|
| `names_stage` | `no-state` | 0/20 | 0.00–0.16 | — (эталон сравнения) |
| `names_stage` | `none` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `names_stage` | `state` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `no_reask` | `no-state` | 15/20 | 0.53–0.89 | — (эталон сравнения) |
| `no_reask` | `none` | 0/20 | 0.00–0.16 | **да** (p = 7.71e-07) |
| `no_reask` | `state` | 19/20 | 0.76–0.99 | нет (p = 0.1818) |

### Пауза на стадии `validation`

| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-state` |
|---|---|---|---|---|
| `names_stage` | `no-state` | 0/20 | 0.00–0.16 | — (эталон сравнения) |
| `names_stage` | `none` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `names_stage` | `state` | 2/20 | 0.03–0.30 | нет (p = 0.4872) |
| `no_reask` | `no-state` | 17/20 | 0.64–0.95 | — (эталон сравнения) |
| `no_reask` | `none` | 0/20 | 0.00–0.16 | **да** (p = 2.57e-08) |
| `no_reask` | `state` | 20/20 | 0.84–1.00 | нет (p = 0.2308) |
| `no_restart` | `no-state` | 20/20 | 0.84–1.00 | — (эталон сравнения) |
| `no_restart` | `none` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `no_restart` | `state` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `right_step` | `no-state` | 0/20 | 0.00–0.16 | — (эталон сравнения) |
| `right_step` | `none` | 0/20 | 0.00–0.16 | нет (p = 1.0000) |
| `right_step` | `state` | 16/20 | 0.58–0.92 | **да** (p = 1.54e-07) |

### Контроли

- **Положительный, живой** — `right_step` в руке `state`: 35/40. Прибор семейства умеет срабатывать на живых ответах, значит нули ниже — это нули поведения, а не молчание детектора.
- **Положительный, на фикстурах** — `names_stage` в руке `state` дал всего 4/80, и это число можно читать только потому, что отдельная проверка показывает: критерий срабатывает на каждой из четырёх стадий. `TestNamesStageFiresForEveryStageAndNotOnDomainWords` подаёт ему «Стадия planning, шаг 1/4», «Стадия execution», «Стадия validation», «Стадия done» — и он срабатывает на всех, а на доменных «валидацию токена, проверяю подпись» молчит. То есть модель действительно почти никогда не называет стадию, а не прибор её не видит.
- **Отрицательный** — `right_step` в руке `none`: 0/40. Без истории и без состояния номер текущего шага угадать нечем; заметная доля здесь означала бы сломанный детектор, а не догадливую модель.

## B — кто двигает автомат

Оба числа — про модель. То, что код отказывает, проверяется тестами, а не статистикой.

| Проба | Что спрашивается | Доля | 95% Уилсон |
|---|---|---|---|
| `drive-next-step` · `asked_nothing` | шаг честно закрыт — просит ли модель сама закрыть его маркером | 0/20 | 0.00–0.16 |
| `drive-next-step` · `asked_step` |  | 16/20 | 0.58–0.92 |
| `drive-skip` · `asked_illegal` | пользователь просит закрыть задачу сразу — просит ли модель запрещённый переход | 0/20 | 0.00–0.16 |
| `drive-skip` · `asked_nothing` |  | 1/20 | 0.01–0.24 |

Какие переходы модель просила: `execution` — 19, `validation` — 4.

Запрещённых переходов модель не попросила ни разу. **Этот ноль читается только потому, что прибор умеет возвращать не-ноль:** `TestTheModelAsksAndTheTableAnswers/незаконный переход отклоняется` подаёт агенту ответ с маркером `[[TRANSITION: done]]` со стадии `planning` и проверяет, что `moveIllegal` встаёт, а стадия не двигается. Без этой проверки ноль здесь был бы неотличим от неработающего детектора.

Закрыть шаг модель просила 16 раз, машина закрыла 16. Здесь таблицы нет: шаг закрывается **по слову модели**, и это заведомо более слабая гарантия, чем переход между стадиями. Код проверяет только границы плана — за последний шаг выйти нельзя.

## D — перенос результата стадии

Две руки различаются одним полем: попал ли итог стадии `planning` в запрос стадии `execution`. Решение — «access-токен живёт 7 минут» — не встречается ни в шагах плана, ни в истории, и обычное значение по умолчанию другое, так что угадать его нечем.

| Критерий | Рука | Доля | 95% Уилсон | Отличие от `no-carry` |
|---|---|---|---|---|
| `no_reask` | `carry` | 20/20 | 0.84–1.00 | нет (p = 1.0000) |
| `no_reask` | `no-carry` | 19/20 | 0.76–0.99 | — (эталон сравнения) |
| `uses_plan` | `carry` | 20/20 | 0.84–1.00 | **да** (p = 1.45e-11) |
| `uses_plan` | `no-carry` | 0/20 | 0.00–0.16 | — (эталон сравнения) |

## C — что стоит состояние

Считается только по семейству A: там все три руки получают **один и тот же вопрос**, поэтому разница в токенах и деньгах — это разница в том, что уехало. Смешивать сюда другие семейства нельзя: тогда сравнивалась бы цена блока с ценой другого вопроса.

| Рука | Клеток | Медиана токенов входа | Доля кэша | Цена клетки | Блок состояния |
|---|---|---|---|---|---|
| `no-state` | 80 | 96 | 0% | $0.000088 | 0 токенов |
| `none` | 80 | 25 | 0% | $0.000043 | 0 токенов |
| `state` | 80 | 272 | 44% | $0.000112 | 237 токенов |

Блок несёт в 2.8 раза больше токенов входа, а стоит дороже только в 1.3 раза.

Откуда берётся эта разница — видно из доли кэша, и здесь надо быть точным. **Факт:** кэшируемая часть запроса (system плюс история) в руках `state` и `no-state` одна и та же, побайтно; блок состояния едет в хвосте, то есть в префикс он не входит. При этом в длинной руке провайдер зачёл из кэша заметную долю, а в короткой — ноль. **Вывод, который отсюда следует:** дело не в содержании префикса, а в длине запроса — на коротком провайдер кэш не включает вовсе. Это согласуется с замером недели 1 (кэш берёт блоки по 64 токена, на коротких промптах скидки нет), но отдельно на пороге здесь не проверялось и остаётся выводом, а не замером.

Именно поэтому блок стоит в хвосте, а не в начале system, как на слайде 21. Замер дня 12: состояние меняется каждый ход, и сдвиг кэшируемого префикса дал тогда в 2.2 раза меньше токенов и на 64% больше денег.

## Приборы

У каждого текстового критерия есть фикстуры «обязан сработать» и «обязан промолчать»; `TestCriteriaAreCalibrated` прогоняет их до первого живого вызова. Критерии `asked_*` читаются не из текста, а из решения машины.

| Критерий | Что считает | Источник |
|---|---|---|
| `names_stage` | ответ называет стадию, на которой стоит задача | текст ответа |
| `right_step` | ответ называет ИМЕННО текущий шаг и не заявляет другой | текст ответа |
| `no_reask` | ответ не переспрашивает, что за задача и где мы | текст ответа |
| `no_restart` | ответ не начинает планирование заново | текст ответа |
| `uses_plan` | ответ применяет решение, принятое на предыдущей стадии (7 минут) | текст ответа |
| `asked_step` | модель сама выставила маркер закрытия шага | решение машины |
| `asked_illegal` | модель попросила переход, который таблица запрещает | решение машины |
| `asked_nothing` | модель не попросила ничего — «ход умер без действия» | решение машины |

