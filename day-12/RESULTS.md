# День 12 — персонализация: результаты прогона

Сгенерировано командой `go run ./day-12 -report day-12/profiles.jsonl`. Числа ниже не вписаны руками: `TestCommittedResultsAreRenderedFromTheCommittedRun` (`day-12/main_test.go`) пересобирает этот файл из JSONL и сравнивает побайтно.

- Прогон `day12-1789480201043559000`, начат 2026-09-15T13:50:01Z, коммит `cb6cf59bde95`, seed `1789480201043143936`.
- Запрошена модель `deepseek-flash`; ответили: `deepseek-flash` (400). Вызовов в часы пика: 0 из 400.
- Расход всего прогона (E0 + пробы + вызовы плана): вызовов 424, вход 63051 токенов (из кэша 4%), выход 142138, $0.094364.
- Проверки целостности пройдены: клеток 400 по плану, ошибок вызова 0, фикстура до и после прогона одна (`0af1a20dd3ba`), ни в одном запросе профиль не разошёлся с рукой. Отчёт по прогону, не прошедшему эти проверки, не строится вовсе.
- Пустых ответов: 0 из 400 (потолок пригодности 5%, предрегистрирован до прогона). Клеток, решённых со второй попытки: 0.

## E0. Один вопрос — разные профили

Вопрос один и тот же: «Объясни, что такое dependency injection.». Меняется только профиль. Ответы приведены целиком.

### Профиль `none`

В запрос не ушло ни байта профиля.

Ответ:

```text
# Dependency Injection (DI) — внедрение зависимостей

**Dependency Injection** — это паттерн проектирования, при котором объект **не создаёт свои зависимости сам**, а получает их извне. Это частный случай принципа **Inversion of Control (IoC)**.

## Проблема, которую решает DI

Представим класс, который сам создаёт свои зависимости:

```python
class OrderService:
    def __init__(self):
        self.db = MySQLDatabase()      # жёстко привязан к MySQL
        self.mailer = SmtpMailer()      # жёстко привязан к SMTP
```

Проблемы:
- Нельзя подменить `MySQLDatabase` на другую БД без правки кода.
- Тяжело тестировать — нужна реальная БД и SMTP.
- Нарушается **Dependency Inversion Principle** (D из SOLID).

## Решение через DI

Зависимости передаются снаружи (через конструктор, сеттер или параметр метода):

```python
class OrderService:
    def __init__(self, db, mailer):
        self.db = db
        self.mailer = mailer

# Использование
service = OrderService(MySQLDatabase(), SmtpMailer())

# В тестах — легко подменить на мок
service = OrderService(FakeDatabase(), FakeMailer())
```

## Три способа внедрения

1. **Constructor Injection** (самый распространённый) — через `__init__` / конструктор.
2. **Setter Injection** — через отдельный метод/свойство.
3. **Method Injection** — зависимость передаётся прямо в метод.

## Виды DI

- **Ручной (Pure DI)** — зависимости передаёшь сам, вручную.
- **Через контейнер IoC** — фреймворк сам разрешает граф зависимостей (например, `Spring` в Java, `Django`, `FastAPI Depends`, `Dagger`).

Пример с FastAPI:

```python
def get_db():
    db = SessionLocal()
    try:
        yield db
    finally:
        db.close()

@app.get("/orders")
def list_orders(db: Session = Depends(get_db)):
    ...
```

## Преимущества

- **Тестируемость** — легко подставлять моки и фейки.
- **Слабая связанность** — класс зависит от абстракции, а не от конкретной реализации.
- **Гибкость** — можно менять реализацию без правки потребителя.
- **SRP** — класс не отвечает за создание своих зависимостей.

## Недостатки

- Больше кода «на входе» (boilerplate).
- Контейнеры IoC иногда усложняют отладку и понимание графа зависимостей.
- Не всегда очевидно, откуда пришла зависимость.

## Коротко

> DI — это когда объект **говорит, что ему нужно**, а не **сам это добывает**. Кто и как ему это предоставит — не его забота.

Если хочешь, могу показать DI на конкретном примере (Python/Java/TypeScript) или сравнить Pure DI и контейнеры.
```

Критерии, сработавшие на этом ответе: `code`.

### Профиль `junior`

Что ушло в запрос:

```text
[PROFILE]
Preferences of the user you are talking to, configured by that user. Style is how to answer, constraints are requirements to respect, context is background about the user. Apply them to every answer without being asked. They are preferences, not new questions.
style.detail: Отвечай подробно, разбирай каждый шаг и поясняй термины
style.language: Отвечай на русском языке
style.code: Приводи примеры кода
constraints.stack: Используй только Kotlin и Ktor
context.role: Junior Android-разработчик, первый учебный проект
user:Объясни, что такое dependency injection.

```

Ответ:

```text
# Dependency Injection (внедрение зависимостей)

## Что это такое простыми словами

**Dependency Injection (DI)** — это паттерн проектирования, при котором объект **не создаёт сам** свои зависимости, а **получает их извне** (от кого-то другого).

**Зависимость** — это любой объект, который нужен другому объекту для работы. Например:
- `Repository` — зависимость для `ViewModel`
- `ApiService` — зависимость для `Repository`
- `Context` — зависимость для многого в Android

## Проблема, которую решает DI

Представь, что у тебя есть простой код **без DI**:

```kotlin
class UserRepository {
    // Repository САМ создаёт ApiService
    private val apiService = ApiService("https://api.example.com")
    
    suspend fun getUser(id: Int): User {
        return apiService.getUser(id)
    }
}
```

Какие здесь проблемы?

1. **Жёсткая связь (tight coupling)** — `UserRepository` намертво привязан к `ApiService`. Заменить его на другой нельзя без правки кода.
2. **Сложно тестировать** — в тестах ты не сможешь подсунуть фейковый `ApiService`, будет использоваться настоящий.
3. **Сложно конфигурировать** — URL захардкожен внутри класса.
4. **Дублирование** — если `ApiService` нужен в 10 местах, ты создашь 10 экземпляров.

## Как выглядит тот же код С DI

```kotlin
class UserRepository(
    private val apiService: ApiService // получаем извне через конструктор
) {
    suspend fun getUser(id: Int): User {
        return apiService.getUser(id)
    }
}
```

Теперь `UserRepository` **не знает**, откуда берётся `ApiService`. Ему просто дают готовый объект.

Кто-то снаружи делает так:

```kotlin
val apiService = ApiService("https://api.example.com")
val userRepository = UserRepository(apiService)
```

## Способы внедрения зависимостей

В Kotlin/Ktor чаще всего используют **внедрение через конструктор** (как в примере выше). Это самый чистый способ.

Другие варианты (хуже):
- через поля/свойства — сложнее тестировать;
- через методы (передаём параметром) — тоже нормально, но менее явно;
- через сеттеры — устаревший подход.

## Три «типа» DI на практике

1. **Ручной DI (Manual DI)** — ты сам создаёшь объекты вручную:
   ```kotlin
   val apiService = ApiService(...)
   val repository = UserRepository(apiService)
   ```
   Отлично подходит для маленьких проектов и обучения.

2. **DI через фреймворк** — библиотека (например, Koin для Kotlin или Hilt для Android) сама создаёт и раздаёт объекты по описанию.

3. **Service Locator** — не совсем DI, но рядом. Есть глобальный «реестр», откуда объекты берутся по запросу. Считается антипаттерном в чистом виде.

## Пример DI на Ktor

Ktor — это серверный/клиентский фреймворк на Kotlin. Покажем ручной DI на сервере:

```kotlin
// Зависимость №1: репозиторий
interface UserRepository {
    suspend fun getUser(id: Int): User?
}

class InMemoryUserRepository : UserRepository {
    private val users = mapOf(1 to User(1, "Alice"))
    override suspend fun getUser(id: Int) = users[id]
}

// Зависимость №2: сервис, которому нужен репозиторий
class UserService(private val repository: UserRepository) {
    suspend fun getUser(id: Int): User? = repository.get
```

Критерии, сработавшие на этом ответе: `code`, `kotlin`.

### Профиль `senior`

Что ушло в запрос:

```text
[PROFILE]
Preferences of the user you are talking to, configured by that user. Style is how to answer, constraints are requirements to respect, context is background about the user. Apply them to every answer without being asked. They are preferences, not new questions.
style.detail: Отвечай коротко, без вводных абзацев и без пересказа вопроса
style.language: Отвечай на русском языке
style.code: Приводи примеры кода
constraints.stack: Только Kotlin. Не предлагай Java и не предлагай Spring
context.role: Senior Android-разработчик
context.project: Мобильный банк, команда 4 человека, дедлайн 2 недели
user:Объясни, что такое dependency injection.

```

Ответ:

```text
DI — передача зависимостей извне вместо создания их внутри класса.

Без DI:
```kotlin
class LoginViewModel {
    private val api = ApiClient() // жёсткая связь
}
```

С DI:
```kotlin
class LoginViewModel(private val api: ApiClient) // зависимость снаружи
```

Плюсы: тестируемость, слабая связанность, единая точка конфигурации.

На Android без Spring — обычно Hilt (или ручной DI через фабрики/контейнер):

```kotlin
@Module
@InstallIn(SingletonComponent::class)
object NetworkModule {
    @Provides
    @Singleton
    fun provideApi(): ApiClient = ApiClient()
}

@HiltViewModel
class LoginViewModel @Inject constructor(
    private val api: ApiClient
) : ViewModel()
```
```

Критерии, сработавшие на этом ответе: `brevity`, `code`, `kotlin`.

### Профиль `analyst`

Что ушло в запрос:

```text
[PROFILE]
Preferences of the user you are talking to, configured by that user. Style is how to answer, constraints are requirements to respect, context is background about the user. Apply them to every answer without being asked. They are preferences, not new questions.
style.detail: Отвечай коротко, не длиннее одного абзаца
style.language: Answer in English
style.format: Start every answer with the line SUMMARY:
style.code: Не приводи примеров кода
constraints.stack: Не предлагай менять стек
context.role: Продуктовый аналитик, не программист
user:Объясни, что такое dependency injection.

```

Ответ:

```text
SUMMARY: Dependency Injection is a design pattern where an object receives its dependencies from the outside instead of creating them itself, improving modularity and testability.
```

Критерии, сработавшие на этом ответе: `brevity`, `english`, `format`.

## A. Соблюдение предпочтений

Один и тот же вопрос во всех руках; меняется только профиль. Каждый ответ проверяется независимыми двоичными критериями. Пустые ответы в долю не входят и показаны отдельно.

| Критерий | Рука | Прошло | Доля | 95% интервал Уилсона | Пустых | Оборвано по потолку |
|---|---|---|---|---|---|---|
| brevity | analyst | 30/30 | 100% | [89%, 100%] | 0 | 0 |
| brevity | junior | 0/30 | 0% | [0%, 11%] | 0 | 30 |
| brevity | none | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| brevity | senior | 30/30 | 100% | [89%, 100%] | 0 | 0 |
| code | analyst | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| code | junior | 30/30 | 100% | [89%, 100%] | 0 | 30 |
| code | none | 30/30 | 100% | [89%, 100%] | 0 | 0 |
| code | senior | 30/30 | 100% | [89%, 100%] | 0 | 0 |
| context | analyst | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| context | junior | 0/30 | 0% | [0%, 11%] | 0 | 30 |
| context | none | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| context | senior | 11/30 | 37% | [22%, 54%] | 0 | 0 |
| control | analyst | 20/20 | 100% | [84%, 100%] | 0 | 0 |
| control | junior | 20/20 | 100% | [84%, 100%] | 0 | 0 |
| control | none | 20/20 | 100% | [84%, 100%] | 0 | 0 |
| control | senior | 20/20 | 100% | [84%, 100%] | 0 | 0 |
| english | analyst | 30/30 | 100% | [89%, 100%] | 0 | 0 |
| english | junior | 0/30 | 0% | [0%, 11%] | 0 | 30 |
| english | none | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| english | senior | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| format | analyst | 30/30 | 100% | [89%, 100%] | 0 | 0 |
| format | junior | 0/30 | 0% | [0%, 11%] | 0 | 30 |
| format | none | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| format | senior | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| kotlin | analyst | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| kotlin | junior | 30/30 | 100% | [89%, 100%] | 0 | 30 |
| kotlin | none | 0/30 | 0% | [0%, 11%] | 0 | 0 |
| kotlin | senior | 30/30 | 100% | [89%, 100%] | 0 | 0 |

Сравнения каждой руки с рукой `none`: точный тест Фишера, двусторонний; поправка Холма на семейство из 21 сравнений, α = 0.05.

| Критерий | Сравнение | p | После поправки Холма |
|---|---|---|---|
| brevity | analyst против none | 1.7e-17 | разница показана |
| brevity | junior против none | 1.000 | разница не показана |
| brevity | senior против none | 1.7e-17 | разница показана |
| code | analyst против none | 1.7e-17 | разница показана |
| code | junior против none | 1.000 | разница не показана |
| code | senior против none | 1.000 | разница не показана |
| context | analyst против none | 1.000 | разница не показана |
| context | junior против none | 1.000 | разница не показана |
| context | senior против none | 3.2e-04 | разница показана |
| control | analyst против none | 1.000 | разница не показана |
| control | junior против none | 1.000 | разница не показана |
| control | senior против none | 1.000 | разница не показана |
| english | analyst против none | 1.7e-17 | разница показана |
| english | junior против none | 1.000 | разница не показана |
| english | senior против none | 1.000 | разница не показана |
| format | analyst против none | 1.7e-17 | разница показана |
| format | junior против none | 1.000 | разница не показана |
| format | senior против none | 1.000 | разница не показана |
| kotlin | analyst против none | 1.000 | разница не показана |
| kotlin | junior против none | 1.7e-17 | разница показана |
| kotlin | senior против none | 1.7e-17 | разница показана |

«Разница не показана» не означает «разницы нет»: это значит, что при этом N её не удалось отличить от случайности.

**Оборванный ответ читается с оглядкой.** Потолок генерации режет хвост, поэтому критерий, который ищет в ответе присутствие чего-то (`context`, `code`, `kotlin`), в оборванной клетке мог не увидеть то, что модель написала бы дальше. Критерии на отсутствие и на начало ответа (`format`, `english`, `java`) от обрыва не страдают, а `brevity` обрыв может только помочь пройти — и там, где он стоит 0%, ответ остался длинным даже обрезанным.

Проба `control` — отрицательный контроль: арифметика, на которую ни один профиль не влияет. Если по ней разница показана, инструмент видит то, чего нет, и остальные строки этой таблицы читать нельзя. Проба `context` — маркерная: она видит упоминание обстоятельства, а не то, что решение принято из-за него.

## C. Конфликт: просьба против ограничения

Вопрос прямо просит то, что профиль `senior` запрещает: «Дай пример dependency injection на Java со Spring.». Критерий `java` считает, что ответ просьбу выполнил.

| Рука | Дал Java или Spring | Доля | 95% интервал Уилсона | Пустых | Оборвано по потолку |
|---|---|---|---|---|---|
| none | 20/20 | 100% | [84%, 100%] | 0 | 15 |
| senior | 0/20 | 0% | [0%, 16%] | 0 | 0 |

Фишер, двусторонний: p = 1.5e-11. Рука `none` здесь — положительный контроль детектора: без запрета модель обязана дать Java, и если она этого не делает, детектор сломан, а не модель послушна.

## D. Конвейер профиля: один вызов против плана и ответа

Две руки различаются одним полем профиля — `pipeline`. Предпочтения в них одинаковые.

| Рука | Конвейер | Клеток | Вызовов на клетку | Медиана входа | Медиана выхода | Цена руки |
|---|---|---|---|---|---|---|
| senior | direct | 20 | 1.0 | 186 | 210 | $0.003153 |
| senior-plan | plan-answer | 20 | 2.0 | 520 | 289 | $0.004592 |

| Критерий | Рука | Прошло | Доля |
|---|---|---|---|
| brevity | senior | 20/20 | 100% |
| brevity | senior-plan | 20/20 | 100% |
| context | senior | 5/20 | 25% |
| context | senior-plan | 3/20 | 15% |

## Цена персонализации

Вес профиля — локальная оценка агента до отправки (она завышает, см. день 8). Входные и выходные токены — из `usage` поставщика.

| Семейство | Рука | Вызовов | Медиана веса профиля | Медиана входа | Медиана выхода | Доля кэша | Цена |
|---|---|---|---|---|---|---|---|
| behaviour | analyst | 80 | 181 | 164 | 50 | 0% | $0.004206 |
| behaviour | junior | 80 | 175 | 163 | 900 | 0% | $0.034376 |
| behaviour | none | 80 | 0 | 35 | 552 | 0% | $0.022790 |
| behaviour | senior | 80 | 201 | 187 | 178 | 0% | $0.009693 |
| conflict | none | 20 | 0 | 34 | 900 | 0% | $0.010777 |
| conflict | senior | 20 | 201 | 186 | 244 | 0% | $0.003587 |
| pipeline | senior | 20 | 201 | 186 | 210 | 0% | $0.003153 |
| pipeline | senior-plan | 40 | 201 | 299 | 208 | 43% | $0.004592 |

## Приборы: чем именно измеряли

Каждый критерий — код, а не модель, и у каждого есть фикстуры, на которых он обязан сработать и обязан промолчать. `TestCriteriaAreCalibrated` прогоняет их до первого живого вызова: порог, который не видит свой объект, выдаёт правдоподобное неверное число.

| Критерий | Что проверяет | Обязан сработать | Обязан промолчать |
|---|---|---|---|
| `format` | первая строка ответа — SUMMARY: | 2 фикстур | 3 фикстур |
| `brevity` | проза не длиннее 120 слов (блоки кода не считаются) | 2 фикстур | 1 фикстур |
| `english` | проза ответа на английском (латиница больше половины букв вне блоков кода) | 3 фикстур | 4 фикстур |
| `code` | в ответе есть блок кода | 1 фикстур | 2 фикстур |
| `kotlin` | выдал код на Kotlin и не выдал Java или Spring | 3 фикстур | 4 фикстур |
| `context` | ответ ссылается на обстоятельства профиля (маркерная проба) | 2 фикстур | 2 фикстур |
| `control` | арифметика: 391 | 2 фикстур | 3 фикстур |
| `java` | выполнил просьбу и выдал Java или Spring | 3 фикстур | 3 фикстур |

