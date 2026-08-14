# Backlog реализации платформы командных творческих джемов

Backlog описывает MVP универсальной платформы: результатом команды может быть текст, игра, музыка, видео, иллюстрация, интерактивная работа или другое медиа. Платформа хранит только карточку продукта и внешние URL, но не файлы результата.

Задачи выполняются по одной и в указанном порядке зависимостей. Завершение задачи не означает автоматического перехода к следующей.

Критерий **«Готово, когда»** каждой задачи дополняет, но не отменяет общие правила в конце документа. Задача с admin mutation не может считаться завершенной без CSRF, обязательной причины и атомарного append-only audit; задача с закрытыми данными — без backend authorization/stage/disclosure checks и тестов; задача с представимым или конкурентным инвариантом — без PostgreSQL constraint/transaction/lock и соответствующей проверки.

## Milestone 1 — Foundation

### TASK-001: Инициализировать приложение и конфигурацию

**Цель:** создать минимальный запускаемый модульный монолит на Go и Gin.

**Требования:**

- конфигурация читается из environment variables и валидируется при старте;
- HTTP-сервер имеет разумные таймауты и graceful shutdown;
- предусмотрены раздельные development и production-настройки без секретов в репозитории;
- структура следует фактическим соглашениям репозитория и не вводит лишних слоев, DI или абстрактных repository-интерфейсов.

**Готово, когда:** приложение корректно запускается и останавливается, ошибки конфигурации понятны, а минимальные тесты и сборка проходят.

### TASK-002: Подключить PostgreSQL через pgx

**Цель:** сделать PostgreSQL единственным источником истины приложения.

**Требования:**

- использовать `pgx`/pool и явный SQL без ORM;
- настраивать DSN и параметры пула через environment variables;
- проверять соединение при старте и корректно закрывать пул при shutdown;
- не включать credentials и чувствительные параметры в логи.

**Готово, когда:** приложение подключается к чистой PostgreSQL, диагностирует недоступную БД и освобождает соединения при остановке.

### TASK-003: Добавить health и доступный noir server-rendered UI foundation

**Цель:** подготовить минимальный серверный web-каркас и общий визуальный фундамент до появления форм и страниц.

**Требования:**

- добавить `GET /health` с минимальной безопасной проверкой состояния приложения и БД;
- рендерить базовую страницу через `html/template` с автоматическим escaping;
- подключить `/static/` для обычных CSS и vanilla JavaScript;
- закрепить сдержанный noir archival-dossier язык: бумага, чернила, красные пометки, dossier/document/stamp/archive number/status label без ущерба читаемости;
- добавить общий layout с фиксированной левой якорной навигацией на desktop и фиксированной нижней навигацией на mobile, а также формы, таблицы и flash/error states;
- предусмотреть компоненты ленты стадии с display-only таймером, горизонтальных folder-карточек команд и секции профиля;
- обеспечить семантическую структуру, labels, keyboard navigation, видимый focus, достаточный контраст и понятные ошибки;
- необязательные CSS-анимации и зерно не мешают чтению, `prefers-reduced-motion` отключает движение; JavaScript не используется для эффектов, решаемых CSS;
- не вводить SPA, frontend-фреймворк или клиентский роутинг.

**Готово, когда:** health endpoint, общий noir layout и статические ресурсы доступны, шаблон не помечает пользовательские данные доверенным HTML, страницы проходимы с клавиатуры и не ломаются на mobile или в reduced-motion режиме.

## Milestone 2 — Миграции и базовая целостность

### TASK-004: Ввести механизм миграций

**Цель:** воспроизводимо управлять схемой PostgreSQL.

**Требования:**

- хранить небольшие упорядоченные миграции в репозитории;
- поддержать применение схемы на чистую БД и документированный production-запуск;
- не смешивать в одной миграции несвязанные домены;
- использовать `NOT NULL`, `FOREIGN KEY`, `UNIQUE`, частичные индексы и `CHECK`, где они выражают инварианты.

**Готово, когда:** чистая база полностью поднимается миграциями, повторный запуск безопасен в рамках выбранного инструмента, а процедура ошибки понятна.

### TASK-005: Подготовить тестовую PostgreSQL-инфраструктуру

**Цель:** проверять SQL-ограничения, транзакции и гонки на реальной PostgreSQL.

**Требования:**

- изолировать данные тестов и применять актуальные миграции;
- не подменять PostgreSQL несовместимой in-memory БД;
- предусмотреть удобные helpers только для реально повторяющейся подготовки данных.

**Готово, когда:** интеграционный тест может создать схему, выполнить запрос и очистить состояние без зависимости от порядка тестов.

## Milestone 3 — Пользователи и аутентификация

### TASK-006: Создать модель пользователей

**Цель:** хранить учетные записи и минимальные роли `user`/`admin`.

**Требования:**

- добавить идентификатор, required username, optional email, password hash, роль и timestamps; отдельного отображаемого имени нет;
- обеспечить case-insensitive уникальность username и case-insensitive уникальность email только когда он указан, а также нужные ограничения длины;
- username публичен; email приватен и доступен только владельцу и admin;
- роль проверяется сервером, а публичное назначение `admin` отсутствует;
- password reset и email verification не входят в MVP.

**Готово, когда:** миграция защищает case-insensitive уникальность username, partial uniqueness optional email и допустимые роли, а SQL-тесты проверяют основные ограничения.

### TASK-007: Добавить общую CSRF-защиту

**Цель:** защитить все state-changing формы и будущие `fetch`-запросы до появления первой мутации.

**Требования:**

- выдавать и проверять CSRF-токены сервером;
- интегрировать токен в HTML-формы и заголовки небольших JSON/fetch endpoints;
- отклонять отсутствующий или неверный токен до мутации;
- `SameSite` не считать заменой CSRF-проверке.

**Готово, когда:** тестовая HTML-форма и fetch-сценарий защищены, а механизм можно повторно применять во всех следующих задачах.

### TASK-008: Реализовать регистрацию

**Цель:** дать гостю безопасно создать обычный аккаунт.

**Требования:**

- ориентировочные маршруты: `GET /register`, `POST /register` с PRG;
- требовать username и пароль, принимать optional email и валидировать их длину, формат и case-insensitive конфликты на backend;
- использовать современное password hashing;
- форма защищена CSRF, пароль и CSRF-токен не логируются.

**Готово, когда:** валидная регистрация создает только роль `user`, ошибки понятны и не раскрывают внутренности, повторные и параллельные регистрации соблюдают уникальность.

### TASK-009: Реализовать server-side sessions и вход

**Цель:** авторизовать пользователя без JWT и клиентского хранения полномочий.

**Требования:**

- ориентировочные маршруты: `GET /login`, `POST /login`, `POST /logout`;
- вход выполняется по username и паролю; email не является логином;
- генерировать криптографически случайный токен и хранить в БД только безопасный hash;
- cookie имеет `HttpOnly`, подходящий `SameSite`, ограниченный срок и `Secure` в production;
- logout инвалидирует серверную сессию, сырые токены не попадают в логи или ошибки;
- все state-changing auth-формы используют уже введенную CSRF-защиту.

**Готово, когда:** case-insensitive вход по username, восстановление current user из cookie, истечение и logout работают, а тесты подтверждают хранение hash вместо сырого токена и CSRF-защиту форм.

### TASK-010: Добавить профиль, auth и role middleware

**Цель:** дать пользователю минимальный профиль и единообразно защищать пользовательские и административные маршруты.

**Требования:**

- реализовать current-user middleware, `RequireAuth` и `RequireAdmin`;
- профиль показывает и редактирует username и optional email с PRG, CSRF и серверной проверкой case-insensitive uniqueness;
- email профиля виден только владельцу и admin; публичные поверхности используют username;
- всегда получать роль из актуальных серверных данных;
- различать понятные ответы для гостя, обычного пользователя и администратора без утечки закрытых сущностей.

**Готово, когда:** профиль и конфликты редактирования покрыты тестами, матрица доступа проверена, а cookie, hidden input или client ID не могут повысить полномочия.

### TASK-011: Добавить CLI создания первого администратора

**Цель:** безопасно загрузить первую admin-учетную запись без публичного self-promotion.

**Требования:**

- CLI принимает необходимые данные безопасным способом и использует тот же password hashing;
- повторный запуск не создает неоднозначных дублей;
- пароль не появляется в логах, audit или shell-friendly диагностике.

**Готово, когда:** на чистой БД можно создать первого администратора, войти через web и нельзя назначить себя admin публичным запросом.

## Milestone 4 — Неизменяемый административный аудит

### TASK-012: Создать append-only audit log

**Цель:** заложить аудит до появления admin CRUD и вмешательств.

**Требования:**

- запись содержит администратора, action, тип и ID сущности, timestamp, обязательную причину, существенные before/after;
- before/after имеют понятный стабильный формат и не содержат паролей, токенов, credentials или лишних персональных данных;
- приложение только добавляет записи и не предоставляет update/delete журнала;
- PostgreSQL запрещает `UPDATE`/`DELETE` audit-записей application DB role через отдельные privileges/ownership, trigger с контролируемыми правами или эквивалентную проверяемую защиту; одного отсутствия UI недостаточно;
- изменение данных и audit insert выполняются в одной транзакции.

**Готово, когда:** helper или прямая транзакционная схема используется тестовым admin-действием, rollback не оставляет ни изменение без аудита, ни аудит без изменения, а интеграционный тест от application DB role подтверждает запрет update/delete журнала.

### TASK-013: Добавить защищенный просмотр аудита

**Цель:** дать администратору читаемый неизменяемый журнал.

**Требования:**

- ориентировочный маршрут `GET /admin/audit` только для `admin`;
- показать actor, действие, сущность, время, причину и before/after;
- поддержать минимальную фильтрацию по сущности/действию без экспорта секретов;
- не предоставлять редактирование или удаление записей.

**Готово, когда:** обычный пользователь не видит журнал, администратор может проследить тестовое изменение, пользовательский текст безопасно экранируется.

## Milestone 5 — Модель джема и стадии

### TASK-014: Создать модель джема

**Цель:** определить джем раньше всех зависящих доменов.

**Требования:**

- поля включают title, description, rules, `visibility` (`draft`/`published`), max team size и timestamps;
- хранить границы для всех пяти стадий: начало `submission`, `evaluation`, `voting`, `finished` и, при необходимости для отображения, начало/публикацию upcoming;
- все моменты хранить как `timestamptz`, расписание должно быть строго упорядочено;
- status override допускает только `upcoming`, `submission`, `evaluation`, `voting`, `finished` или `NULL` для автоматического режима.

**Готово, когда:** миграция отклоняет недопустимые visibility, override, max team size и очевидно некорректное расписание.

### TASK-015: Реализовать канонический расчет effective stage

**Цель:** получить единственный серверный источник стадии без cron.

**Требования:**

- чистая функция сначала применяет override, иначе сравнивает server time с границами;
- точно определить включительность каждой границы и последовательность `upcoming → submission → evaluation → voting → finished`;
- visibility не смешивать со стадией: draft может иметь effective stage, но не становится публичным;
- browser countdown является только отображением.

**Готово, когда:** table-driven unit tests покрывают момент до, ровно на и после каждой границы, override каждой стадией и возврат к автоматическому расчету.

### TASK-016: Реализовать Europe/Moscow conversion

**Цель:** единообразно принимать и показывать admin-даты в Москве, сохраняя абсолютные моменты.

**Требования:**

- парсить локальные admin-поля явно в `Europe/Moscow`;
- сохранять `timestamptz` и форматировать пользователям в московском времени;
- ошибки отсутствующей/некорректной даты показывать у соответствующего поля;
- тестировать conversion независимо от timezone процесса и БД.

**Готово, когда:** round-trip тесты подтверждают ожидаемые instant и московское отображение всех границ.

### TASK-017: Защитить правило одного опубликованного активного джема

**Цель:** не допустить одновременной публикации двух незавершенных джемов.

**Требования:**

- считать активным опубликованный джем с effective stage не `finished`;
- проверять правило транзакционно при publish, изменении расписания и override, включая возврат finished-джема в активную стадию;
- использовать PostgreSQL-ограничение там, где оно практически выражается без зависимости от текущего времени, и серверную блокировку для time-derived части;
- разрешать множество draft и опубликованных finished джемов.

**Готово, когда:** конкурентные и последовательные тесты publish, override и изменения расписания, реактивирующего finished jam, не оставляют два active published джема, а неизмененные архивные джемы не блокируют новый active.

## Milestone 6 — Схема опросника и подготовка draft-джема

### TASK-018: Создать схему единственного опросника, вопросов и ответов

**Цель:** до появления create-handler джема подготовить хранение ровно одного индивидуального eligibility questionnaire на каждый jam.

**Требования:**

- questionnaire имеет обязательный `jam_id` с `UNIQUE`; второй questionnaire для того же jam невозможен;
- будущий handler создания jam обязан в одной транзакции создавать jam и его единственную пустую questionnaire shell; отдельного создания второго опросника не будет;
- типы вопросов ровно `short_text`, `single_choice`, `multiple_choice`;
- вопрос содержит prompt, optional hint, required, применимый text-length limit, применимый multiple-choice selection limit и position;
- варианты выбора имеют стабильные IDs и position;
- ответы принадлежат user, а не team, и сохраняют draft/completed state и историю.

**Готово, когда:** миграции применяются до TASK-019, unique/foreign/check constraints отклоняют второй questionnaire, несовместимые типы/лимиты и недопустимые связи, а SQL-тесты доказывают эти инварианты.

### TASK-019: Реализовать admin create/edit draft-джемов

**Цель:** позволить администратору подготовить draft jam и расписание без опасного CRUD/hard delete.

**Требования:**

- ориентировочные PRG-маршруты: `/admin/jams`, `/admin/jams/new`, `/admin/jams/:id`, `/admin/jams/:id/edit`; delete-route не добавлять;
- создание в одной транзакции записывает jam, ровно одну пустую questionnaire shell и audit entry;
- валидировать title, description/rules, все границы и max team size на backend;
- обычные create/edit, включая расписание, требуют reason и material before/after audit в той же транзакции;
- опубликованный или finished jam не удаляется; история сохраняется; опасные изменения визуально отделены и подтверждаются.

**Готово, когда:** admin создает/редактирует draft вместе с единственным questionnaire, non-admin получает отказ, CSRF действует, rollback не оставляет половинчатое состояние, а каждое изменение полностью отражено в audit.

### TASK-020: Реализовать admin questionnaire builder для draft

**Цель:** настроить уже существующую пустую shell, вопросы и варианты до публикации джема.

**Требования:**

- admin PRG-формы добавляют, редактируют и упорядочивают вопросы/варианты, но не создают второй questionnaire;
- каждое изменение вопроса, варианта или порядка требует reason и material before/after audit в той же транзакции;
- builder валидирует свойства каждого типа на backend и не добавляет rich text;
- до первого ответа допустимы структурные изменения; после первого ответа разрешены только изменения, не меняющие смысл или валидность сохраненных данных;
- UI ясно объясняет заблокированные destructive actions.

**Готово, когда:** draft questionnaire можно полностью настроить, non-admin не имеет доступа, все мутации CSRF-защищены и аудируются, а существующие ответы невозможно тихо повредить.

### TASK-021: Реализовать подтвержденный reset ответов

**Цель:** обеспечить единственный явный путь для необходимых destructive structural changes без удаления истории.

**Требования:**

- отдельное опасное admin-действие требует подтверждения и reason;
- reset и структурное изменение выполняются согласованно и пишут material before/after audit в той же транзакции;
- исторические ответы и факт изменения сохраняются после reset и завершения jam;
- обычное редактирование не запускает reset и hard delete исторических questionnaire data отсутствует.

**Готово, когда:** без reset запрещенное изменение невозможно, после reset история и audit однозначны, а application routes не могут hard-delete ответы или их историю.

## Milestone 7 — Публикация и публичные страницы

### TASK-022: Реализовать publish, unpublish и status override

**Цель:** управлять публичностью отдельно от стадии и поддержать аварийное ручное состояние.

**Требования:**

- publish/unpublish, установка override и возврат к auto — отдельные подтверждаемые CSRF-protected POST-действия;
- каждое действие требует reason и пишет material before/after audit в той же транзакции;
- после публикации TASK-022 расширяет edit-flow TASK-019: изменение расписания опубликованного jam остается доступно admin, но требует отдельного подтверждения, повторной проверки one-active/stage prerequisites и аудита;
- publish требует ровно одну questionnaire shell и минимум один корректно настроенный вопрос, но не требует тем: темы формируются из ответов во время `upcoming`;
- перед commit повторно проверяются расписание и один active published jam; publish, override и schedule edit не могут реактивировать finished jam при наличии другого active;
- до реализации schema тем override непосредственно в `submission`+ и опасные schedule changes, сразу приводящие к этой стадии, безопасно отклоняются как недоступные; TASK-036 заменяет этот временный запрет проверкой наличия минимум одной темы;
- hard delete published/finished jam отсутствует, а unpublish не уничтожает историю.

**Готово, когда:** draft никогда не раскрывается normal users, публикация без настроенного вопроса отклоняется, темы не являются publish prerequisite, ранний переход в submission+ пока безопасно закрыт, one-active покрыт publish/override/reactivating schedule tests, а все мутации атомарно аудируются.

### TASK-023: Добавить публичный текущий джем и архив

**Цель:** дать гостям доступ только к опубликованному и уже раскрытому контенту.

**Требования:**

- ориентировочные страницы: `GET /`, `GET /jams/:id`, `GET /archive`;
- главная остается доступной гостю и один раз за browser session автоматически открывает закрываемое auth-досье через `sessionStorage`, по умолчанию на форме входа; защищенное действие гостя открывает его снова;
- `sessionStorage` хранит только UX-признак показа и не участвует в серверной аутентификации;
- главная показывает не более одного published active jam, архив — только published `finished` jams;
- отображать effective stage, московские даты, правила и серверно определенные доступные действия;
- основной экран использует stage ribbon с display-only таймером и секцию профиля; ссылки на admin UI нет ни для кого, включая admin, а `/admin` доступен только прямым переходом и защищен сервером;
- draft выглядит отсутствующим для normal users и не утекает через списки, IDs или errors;
- published/finished jam и связанная история остаются доступны по disclosure rules без hard delete.

**Готово, когда:** гость видит текущий/архивный jam по правилам, draft недоступен, countdown не влияет на разрешения, а disclosure tests охватывают HTML и ошибки.

## Milestone 8 — Команды, приглашения и управление составом

### TASK-024: Создать схему команд и членства

**Цель:** закрепить принадлежность команды одному jam и членство пользователя.

**Требования:**

- team имеет jam, required name, одного captain, optional description/avatar и timestamps;
- название team уникально в пределах jam без учета регистра;
- membership связывает user/team/jam и обеспечивает максимум одну team пользователя в одном jam;
- captain обязан быть текущим member своей team;
- хранить признак назначенного captain product editor без универсальной ACL.

**Готово, когда:** PostgreSQL constraints не допускают case-insensitive дубли названий в jam, cross-jam membership, двойное членство, двух captains или captain вне состава.

### TASK-025: Реализовать создание и профиль команды

**Цель:** дать пользователю создать team и captain управлять ее профилем через конец `submission`.

**Требования:**

- ориентировочные маршруты: `GET/POST /jams/:id/teams/new`, `GET /teams/:id`, `GET/POST /teams/:id/edit`;
- create form принимает name и optional description; TASK-027 добавляет в эту же форму optional avatar с безопасной обработкой загрузки;
- создание атомарно добавляет team, captain и membership;
- create/edit self-service разрешены в `upcoming` и `submission`, включая всю submission до серверной границы ее окончания;
- только current captain редактирует name, optional description и avatar; case-insensitive конфликт имени, visibility, stage и membership проверяются backend;
- team detail показывает avatar, name, description, captain и состав с публичными username; own-team действия выводятся по текущей роли и стадии, закрытые invite/editor controls не видны outsiders, а theme/product соблюдают stage disclosure;
- на главной команды показаны горизонтальными folder-карточками, при этом собственная team идет первой.

**Готово, когда:** пользователь не может создать вторую team/membership в jam, подменить IDs, действовать после submission или редактировать чужую team.

### TASK-026: Обеспечить concurrency-safe max team size

**Цель:** не превышать установленный для jam размер team при параллельных вступлениях.

**Требования:**

- блокировать подходящую строку team/jam или применять эквивалентную транзакционную гарантию;
- повторно проверять current membership, stage и capacity внутри транзакции;
- не использовать незащищенную пару `SELECT count` → `INSERT`.

**Готово, когда:** конкурентный PostgreSQL-тест при одном свободном месте принимает ровно одного пользователя и сохраняет membership invariants.

### TASK-027: Реализовать безопасные аватары команд

**Цель:** хранить optional avatar на постоянном локальном диске VPS без риска исполнения.

**Требования:**

- optional avatar добавляется в create form команды; captain загружает/заменяет его через CSRF-protected create/edit формы до конца submission;
- ограничить размер/форматы и определять фактический content type по содержимому;
- генерировать безопасное имя, хранить вне executable paths и не доверять extension/header;
- согласовать запись БД и файла, безопасную замену/удаление старого avatar и future backup.

**Готово, когда:** валидное изображение отображается, oversized/поддельный файл и path tampering отклоняются, а DB/file failure не оставляет небезопасного состояния.

### TASK-028: Реализовать отзывные invite links

**Цель:** позволить captain приглашать участников непредсказуемой ссылкой до конца submission.

**Требования:**

- raw token криптографически случаен и показывается только для ссылки, в БД хранится безопасный hash;
- current captain может issue/revoke только в `upcoming` и `submission`; позже revoke доступен лишь admin через audited intervention;
- новая ссылка немедленно отзывает старую; raw token не логируется и не попадает в audit before/after;
- ориентировочные маршруты: captain POST issue/revoke и `GET/POST /invites/:token`.

**Готово, когда:** cutoff, отзыв, hash/rotation, CSRF и отсутствие token leakage покрыты security tests.

### TASK-029: Реализовать вступление и выход

**Цель:** разрешить пользователю создавать/join/leave team через конец `submission` с правилом captain transfer.

**Требования:**

- join по invite транзакционно проверяет published jam, effective stage `upcoming`/`submission`, token, capacity и membership;
- участнику другой team того же jam возвращается понятный отказ без auto-leave/auto-switch;
- обычный member может leave через конец submission; после границы self-service закрыт;
- captain обязан сначала передать captaincy и не может оставить team без captain.

**Готово, когда:** boundary/concurrency tests подтверждают cutoff, unique membership, capacity, foreign-invite отказ и captain leave rule.

### TASK-030: Реализовать передачу captaincy и product editors

**Цель:** дать captain минимальное управление полномочиями team через конец submission.

**Требования:**

- captain передает роль только current member своей team;
- captain назначает/снимает product editor только среди current members;
- self-service действия доступны в `upcoming` и `submission`, полномочия выводятся из session/current membership;
- при leave editor-полномочие перестает действовать.

**Готово, когда:** операции атомарны, сохраняют ровно одного captain и покрыты authorization, stage-boundary и ID-tampering tests.

### TASK-031: Добавить полный admin team control

**Цель:** позволить admin исправлять team-состояние на любой стадии без обхода инвариантов.

**Требования:**

- отдельные подтверждаемые controls покрывают profile name/description, avatar moderation/removal, invite revocation, membership add/remove и captain transfer;
- каждое вмешательство требует reason и material before/after audit в той же транзакции; raw invite token никогда не аудируется;
- сохраняются один captain, одна team пользователя на jam и max team size, иначе возвращается явный безопасный отказ;
- admin может действовать на любой стадии, но не может бесследно потерять captain, auto-switch пользователя или разрушить историю.

**Готово, когда:** все admin team/profile/avatar/invite/membership/captain mutations доступны только admin, атомарно аудируются и не нарушают DB/domain invariants.

## Milestone 9 — Опросник участника и eligibility

### TASK-032: Реализовать страницу и CSRF-autosave опросника

**Цель:** дать current team member заполнить индивидуальный draft только в `upcoming`.

**Требования:**

- `GET /jams/:id/questionnaire` доступен members этого jam и admin;
- небольшой autosave fetch endpoint CSRF-защищен и валидирует user/jam/question IDs, типы и лимиты;
- ответы другого user никогда не возвращаются;
- редактирование completed до submission переводит response обратно в draft, сохраняя историю.

**Готово, когда:** autosave устойчив к повтору, отклонен вне upcoming/чужому member, не раскрывает чужие ответы, а edit→draft покрыт тестами.

### TASK-033: Реализовать Complete и расчет eligibility

**Цель:** завершать response только после полной серверной валидации и вычислять eligibility team.

**Требования:**

- Complete повторно проверяет required answers и все лимиты в транзакции;
- team eligible, если хотя бы один current member имеет completed response;
- response/history вышедшего member сохраняются, но перестают учитываться;
- team detail показывает ее members только draft/completed status, не ответы; outsiders эти статусы не видят.

**Готово, когда:** tests покрывают пустой response, completed member, его leave, повторный Complete после edit и deadline boundary.

### TASK-034: Добавить admin eligibility override

**Цель:** дать admin явное исключение без изменения ответов.

**Требования:**

- override set/remove — отдельные подтверждаемые CSRF-действия с reason;
- изменение и material before/after audit атомарны;
- automatic eligibility и override отображаются раздельно;
- leave member не создает override автоматически.

**Готово, когда:** effective eligibility корректно объединяет current completed member и override, а каждое admin-изменение видно в append-only audit.

### TASK-035: Добавить admin reports и безопасный CSV

**Цель:** дать admin completion summary, individual answers и filter by team без публичного раскрытия.

**Требования:**

- страницы и export доступны только admin, ответы не видны teammates или гостям;
- CSV имеет корректные encoding/quoting;
- untrusted cells с formula-triggering prefix нейтрализуются;
- исторические ответы сохраняются после leave, reset и finished jam.

**Готово, когда:** authorization/disclosure и CSV injection tests покрывают опасные префиксы, кавычки, переносы и Unicode.

## Milestone 10 — Темы

### TASK-036: Создать per-jam темы и admin controls

**Цель:** хранить простые фразы тем конкретного джема.

**Требования:**

- тема содержит только jam, phrase, технические ID/timestamps и при необходимости `withdrawn_at` для сохранения истории экстренного удаления; состояний `draft`/`ready`/`archived` и общего lifecycle нет;
- каждая неотозванная тема, привязанная к jam, входит в его доступный набор; минимум — одна, верхнего лимита нет;
- admin create/edit требует reason и material before/after audit в той же транзакции;
- до начала `submission` admin обязан создать минимум одну тему из ответов upcoming; это не является prerequisite публикации;
- отсутствие тем не изменяет канонический effective stage: в наступившем `submission`+ показывается критичная ошибка конфигурации, а выбор темы и final submission блокируются;
- override напрямую в `submission`+ и опасное schedule edit, приводящее к этой стадии, отклоняются без минимум одной темы.

**Готово, когда:** schema не содержит lifecycle-state, admin может создать от одной до неограниченного числа тем, все мутации аудируются, временный запрет TASK-022 заменен атомарной проверкой theme prerequisite в override/schedule handlers, а автоматическая стадия без тем не подменяется и безопасно блокирует зависимые mutations.

### TASK-037: Реализовать раскрытие и выбор темы командой

**Цель:** автоматически раскрыть все темы jam в `submission` и сохранить выбор команды.

**Требования:**

- до submission темы скрыты от обычных пользователей;
- в submission список раскрыт, только captain выбирает/меняет одну тему до конца стадии;
- несколько команд могут выбрать одну тему;
- выбор конкретной команды скрыт от outsiders до `evaluation` на HTML, JSON, counters и errors.

**Готово, когда:** stage/role/disclosure tests покрывают все пять стадий и попытки подмены team/theme другого jam.

### TASK-038: Защитить историю тем и admin emergencies

**Цель:** не разрушать смысл выбранной темы и поддержать контролируемые вмешательства.

**Требования:**

- выбранную или использованную при final submission тему нельзя тихо удалить/переименовать;
- после публикации физическое удаление темы запрещено: экстренное исключение из набора сохраняет запись и отмечает ее `withdrawn_at`; выбранная тема сначала требует явного безопасного переназначения;
- любое admin create/edit, а post-reveal edit, административная смена выбора и отзыв темы особенно, требуют reason, подтверждения и material before/after audit в одной транзакции;
- reuse выполняется копированием phrase в независимую запись другого jam;
- finished jam сохраняет свои темы и selections.

**Готово, когда:** ограничения и tests не позволяют dangling selection или историческое изменение без audit.

## Milestone 11 — Продукты команды

### TASK-039: Создать схему product card

**Цель:** хранить максимум один продукт команды в джеме без файлов результата.

**Требования:**

- поля ровно: required title, required external result URL, optional description, optional external commentary/review URL, optional notes;
- дополнительно хранить служебный draft/final state и timestamps, не добавляя литературный body/author model;
- `UNIQUE` защищает один product на team/jam, связи подтверждают принадлежность team тому же jam;
- файлы результата не загружаются, не проксируются и не хостятся.

**Готово, когда:** миграция отклоняет второй product и cross-jam связи, а модель не содержит stale submission/one-author assumptions.

### TASK-040: Реализовать draft create/edit и URL validation

**Цель:** дать captain и назначенным current product editors редактировать карточку до конца submission.

**Требования:**

- ориентировочные PRG-маршруты: `GET/POST /jams/:id/product`, `GET/POST /products/:id/edit`;
- team и полномочия выводятся из session/current membership, browser `team_id/user_id/editor` не считается авторитетным;
- result и commentary/review URL должны быть абсолютными `http`/`https`, без credentials и control characters; malformed URL отклоняются, проверка достижимости host не требуется;
- guest, outsider, бывший editor и запрос после submission получают безопасный отказ.

**Готово, когда:** authorization, deadline и URL tests покрывают безопасные/опасные схемы и ID tampering.

### TASK-041: Реализовать final submission

**Цель:** явно зафиксировать готовность карточки к публичному раскрытию.

**Требования:**

- final action доступен до конца `submission` captain/editor;
- внутри транзакции повторно проверить title, result URL, selected theme и effective team eligibility;
- final state не отменяет разрешенное редактирование до конца submission, но каждое изменение снова должно оставлять карточку валидной;
- stage, membership и prerequisites проверяются в момент мутации.

**Готово, когда:** неполная или ineligible карточка не становится final, а граничные тесты deadline используют server time.

### TASK-042: Реализовать публичный список и detail продуктов

**Цель:** раскрывать только final продукты начиная с `evaluation`.

**Требования:**

- ориентировочные маршруты: `GET /jams/:id/products`, `GET /jams/:id/products/:id`;
- гости могут читать disclosed final products опубликованного jam;
- до evaluation чужие products, existence, counts, selected themes и identifiers не раскрываются через HTML/JSON/errors/sorting;
- после evaluation показываются карточка, team и selected theme по установленным правилам.

**Готово, когда:** disclosure tests проверяют guest/user/member/admin на каждой стадии и последовательные скрытые IDs.

## Milestone 12 — Номинации

### TASK-043: Реализовать командную номинацию

**Цель:** позволить команде предложить максимум одну optional nomination вместе с product.

**Требования:**

- nomination принадлежит тому же jam и связана с authoring team/product;
- PostgreSQL гарантирует максимум одну team nomination на product/team в jam;
- редактирование следует полномочиям и срокам product;
- отказ команды от ранее введенной номинации сохраняет историческую запись как withdrawn, а не удаляет ее физически;
- title и список nominations не раскрываются до `voting`, а начиная с voting становятся публичными для published jam;
- authoring team скрыта от outsiders до `finished`, включая errors и metadata.

**Готово, когда:** команда может оставить номинацию пустой или предложить одну, а вторая и cross-jam связь отклоняются.

### TASK-044: Реализовать кураторские номинации

**Цель:** дать admin добавить любое число явно помеченных nominations до voting.

**Требования:**

- admin create/edit/withdraw до начала voting через PRG-формы; withdrawal сохраняет историческую запись и исключает ее из активного списка без hard delete;
- каждая curator nomination получает явную публичную отметку начиная с раскрытия списка в `voting` и не притворяется командной;
- каждое admin create/edit/withdraw требует reason и material before/after audit в той же транзакции;
- после начала voting destructive changes блокируются либо выполняются только как явно спроектированная audited emergency operation.

**Готово, когда:** ограничения стадии/роли протестированы, до voting не раскрываются title/list/existence nominations, в voting видны список и curator mark без team author, а team author раскрывается только в finished.

## Milestone 13 — Бампы

### TASK-045: Создать модель и concurrency-safe cooldown бампов

**Цель:** хранить повторяемые реакции отдельно от голосов и результатов.

**Требования:**

- bump связывает authenticated user и disclosed final product;
- разрешен только в `evaluation` и `voting`;
- cooldown ровно одна минута для пары user-product;
- собственный team product бампать разрешено;
- атомарный SQL, lock или эквивалентная гарантия исключает два успешных параллельных bump в cooldown.

**Готово, когда:** boundary и concurrency tests подтверждают один успешный bump, повтор после минуты и закрытие во всех остальных стадиях.

### TASK-046: Добавить fetch endpoint и актуальный счетчик бампов

**Цель:** обновлять реакцию без полной перезагрузки, сохраняя backend источником истины.

**Требования:**

- небольшой CSRF-protected endpoint проверяет auth, published jam, stage и product-jam relation;
- response возвращает авторитетный count и оставшийся cooldown без скрытых данных;
- страница может периодически обновлять count простым fetch, WebSocket не нужен;
- count публично виден для disclosed product в `evaluation`, `voting` и finished archive, но bump mutation разрешена только в evaluation/voting;
- errors не раскрывают скрытый product до evaluation.

**Готово, когда:** UI корректно обрабатывает success/cooldown/stage close, а HTML и JSON используют одинаковые disclosure rules.

## Milestone 14 — Голосование

### TASK-047: Создать схему выбора голоса

**Цель:** хранить один изменяемый выбор пользователя в каждой номинации.

**Требования:**

- vote связывает user, nomination и product одного jam;
- PostgreSQL uniqueness гарантирует максимум одну активную selection на user-nomination;
- схема поддерживает change selection через upsert/transaction без накопления активных дублей;
- не вводить общий рейтинг, общий голос или overall winner.

**Готово, когда:** constraints и SQL tests отклоняют дубли и cross-jam nomination/product.

### TASK-048: Реализовать mutation голосования

**Цель:** дать каждому зарегистрированному пользователю выбирать и менять один product на nomination во время voting.

**Требования:**

- небольшой CSRF-protected fetch endpoint повторно проверяет effective stage `voting`;
- проверяются published jam, nomination/product same jam, final/disclosed product и current membership;
- запрещен голос за product собственной текущей команды;
- upsert/transaction устойчив к параллельным запросам и возвращает фактический серверный выбор.

**Готово, когда:** authorization, self-vote, change-vote, stage boundary и concurrency tests проходят.

### TASK-049: Реализовать текущие счетчики голосов

**Цель:** показывать authoritative counts в реальном времени только во время voting.

**Требования:**

- небольшой read/fetch endpoint вычисляет counts в PostgreSQL;
- counts доступны гостям во время `voting` вместе с публичным disclosed content, но mutation всегда требует account;
- до voting totals не раскрываются ни прямо, ни sorting/aggregates/errors; после voting используется отдельное представление результатов;
- polling достаточен, WebSocket не вводить.

**Готово, когда:** disclosure tests подтверждают отсутствие ранних totals и корректное обновление during voting.

## Milestone 15 — Результаты и архив

### TASK-050: Рассчитать победителей по номинациям

**Цель:** публиковать результаты без общего победителя джема.

**Требования:**

- результат вычисляется authoritative SQL по каждой nomination;
- все products с одинаковым максимальным count являются winners;
- нет tie-breaker, positions across nominations, overall ranking или overall winner;
- nominations без голосов имеют однозначное нейтральное отображение без ложного победителя.

**Готово, когда:** tests покрывают единственного лидера, ничью, нулевые голоса и независимость nominations.

### TASK-051: Добавить finished results и полное архивное раскрытие

**Цель:** после `finished` закрыть mutations и показать допустимые итоги.

**Требования:**

- results доступны для published finished jam и остаются в archive;
- раскрывается authoring team командной nomination, curator mark сохраняется;
- bump/vote mutations закрыты backend независимо от клиента, но historical bump count остается публичным в finished archive;
- до finished results/winner flags/authorship не утекают через HTML, JSON, ordering, counts, metadata и errors.

**Готово, когда:** end-to-end disclosure matrix для voting→finished подтверждает закрытие мутаций и корректное раскрытие ties/authorship.

## Milestone 16 — Полная административная панель

### TASK-052: Собрать admin dashboard и навигацию

**Цель:** дать ясную точку входа ко всем уже реализованным административным областям.

**Требования:**

- `GET /admin` показывает current jam, visibility, effective stage, auto/override, ближайшие границы и безопасные агрегаты;
- публичный и пользовательский UI не содержит ссылки на admin dashboard даже для admin; вход только прямым переходом на `/admin`;
- навигация ведет к users, jams, teams, questionnaire, eligibility, themes, products, nominations, votes, bumps и audit;
- опасные actions заметно отделены, требуют confirmation, reason и CSRF;
- dashboard не становится вторым источником доменной логики.

**Готово, когда:** admin видит состояние и доступные controls, non-admin не получает admin data, mobile/admin readability проверена.

### TASK-053: Добавить управление users и roles

**Цель:** позволить admin просматривать пользователей и контролируемо менять роли/доступ в пределах MVP.

**Требования:**

- список, поиск и detail не раскрываются обычным пользователям;
- admin видит optional email пользователя, но публичные страницы и другие пользователи видят только username;
- role changes выполняются server-side, требуют reason и before/after audit;
- запретить опасное удаление/понижение последнего администратора, если это оставляет систему без admin;
- password reset, email verification и email service не добавлять.

**Готово, когда:** role authorization и last-admin invariant протестированы, каждое изменение отражено в audit.

### TASK-054: Добавить moderation products и nominations

**Цель:** дать admin явные инструменты вмешательства в пользовательский контент.

**Требования:**

- просмотр и допустимые correction/moderation actions сохраняют связи jam/team/theme;
- post-disclosure вмешательства особо подтверждаются;
- reason и material before/after audit обязательны;
- нельзя молча создать второй product, удалить историческую selected theme или раскрыть hidden nomination author.

**Готово, когда:** admin-сценарии сохраняют constraints и disclosure, а все изменения трассируются.

### TASK-055: Добавить interventions для votes и bumps

**Цель:** исправлять конкретные недействительные записи без скрытого пересчета правил.

**Требования:**

- admin видит необходимые агрегаты и конкретные записи в защищенном интерфейсе;
- недействительная запись помечается invalidated с причиной, actor и временем, но физически не удаляется; при необходимости отдельное аудируемое действие может восстановить ее;
- intervention требует confirmation/reason и транзакционный before/after audit;
- публичные counts после изменения берутся из PostgreSQL и исключают invalidated записи;
- admin pages не меняют правила ties, self-vote или cooldown для обычных запросов.

**Готово, когда:** инвалидация/восстановление отражается в authoritative counts и audit, исходная запись сохраняется, а обычный пользователь не имеет доступа.

## Milestone 17 — Сквозная безопасность и раскрытие данных

### TASK-056: Провести hidden-data disclosure review и tests

**Цель:** доказать отсутствие преждевременных утечек на всех поверхностях.

**Требования:**

- матрица guest/user/member/captain/editor/admin × draft/published × все пять стадий;
- проверить HTML, JSON, counters, sorting, aggregates, IDs, errors, search, export и metadata;
- особо проверить themes, team selections, чужие products, nomination title/list до voting, team nomination author до finished, vote totals/results и questionnaire answers;
- draft всегда скрыт от normal users, guest может читать только уже disclosed public content, account требуется для mutations.

**Готово, когда:** автоматизированные tests покрывают ключевую матрицу, найденные leaks исправлены без ослабления admin-защиты.

### TASK-057: Провести authorization, token и input security review

**Цель:** закрыть сквозные уязвимости перед production.

**Требования:**

- проверить sessions/cookies, password hashing, CSRF всех POST/fetch, role/membership/ownership/stage rechecks;
- проверить SQL parameters, URL schemes, template escaping, avatar upload и CSV formula injection;
- убедиться, что passwords, raw session/invite/CSRF tokens и credentials не попадают в logs/errors/audit/URLs;
- проверить понятные неразглашающие errors и отсутствие доверия hidden inputs/client IDs.

**Готово, когда:** security-sensitive tests проходят, а review checklist не содержит необработанных high-risk пунктов.

### TASK-058: Добавить безопасные error pages

**Цель:** единообразно отвечать на 403, 404, 409/validation и 500 без утечки внутренних данных.

**Требования:**

- noir dossier оформление остается читаемым и доступным;
- production response не содержит stack trace, SQL, path, secret или hidden entity existence;
- логи содержат достаточный correlation/context без чувствительных значений;
- JSON/fetch errors следуют тем же disclosure rules.

**Готово, когда:** handler tests проверяют content/status для HTML и JSON, а production-mode ошибки безопасны.

## Milestone 18 — Production readiness и приемка MVP

### TASK-059: Подготовить production runtime

**Цель:** сделать один VPS deployment предсказуемым без лишней инфраструктуры.

**Требования:**

- проверить HTTP timeouts, graceful shutdown, pgx pool, secure cookie mode, proxy/base URL assumptions и structured logging;
- healthcheck корректно отражает готовность без публикации internals;
- миграции запускаются документированно и не требуют ручного изменения schema;
- не добавлять Kubernetes, Redis, Kafka, object storage, cron для stage, SPA или WebSocket.

**Готово, когда:** production-like запуск, shutdown и migration procedure воспроизводимы, секреты поступают только из environment.

### TASK-060: Настроить backups и проверку восстановления

**Цель:** восстанавливать PostgreSQL и persistent local team avatars как единое состояние продукта.

**Требования:**

- backup включает PostgreSQL и каталог аватаров;
- описать частоту, retention, права доступа и безопасное хранение backup;
- выполнить тестовое восстановление в отдельное окружение;
- внешние файлы результатов не входят в backup, потому что платформа их не хранит.

**Готово, когда:** проверенное восстановление возвращает schema/data/audit и доступные avatar files, а процедура документирована.

### TASK-061: Выполнить финальную сквозную приемку

**Цель:** подтвердить целостный MVP-сценарий и отсутствие устаревших предположений.

**Требования:**

- пройти draft→publish→upcoming→submission→evaluation→voting→finished/archive;
- проверить team create/invite/questionnaire/eligibility/theme/product/team nomination/bumps/votes/ties;
- проверить admin overrides/interventions/audit и возврат stage override в auto;
- проверить скрытие nomination title/list до voting, curator mark с voting, team author с finished, realtime public vote counts в voting и сохранение bump count в finished;
- убедиться, что нет literary body, one-author submission, единой темы jam, общего winner, result hosting, password recovery/email service или иных вне-MVP функций;
- запустить concurrency tests для team capacity, publish invariant, bumps и votes.

**Готово, когда:** приемочный checklist завершен, миграции проверены на чистой БД, известные ограничения явно зафиксированы и нет блокирующих дефектов.

### TASK-062: Выполнить финальную техническую проверку

**Цель:** завершить MVP воспроизводимой проверкой качества.

**Требования:**

- выполнить `gofmt -w .`;
- выполнить `go test ./...`, включая disclosure, boundary, authorization и concurrency tests;
- выполнить `go build ./...`;
- проверить рабочее дерево и не включать несвязанные изменения.

**Готово, когда:** форматирование, все тесты и сборка проходят; каждое невыполнимое действие или failure явно сообщено, а успешность не заявлена при падающей проверке.

# Общие правила выполнения задач

1. Выполнять только явно запрошенную `TASK-xxx`. Не переходить автоматически к следующей задаче или milestone.
2. Перед реализацией прочитать актуальные `context.md`, `AGENTS.md`, затронутый код, routes, migrations и tests. Уточнить зависимости задачи, stage gates, permissions, disclosure boundaries и возможные гонки.
3. Следовать указанному порядку зависимостей. Если prerequisite не завершен, остановиться и явно сообщить блокер, а не реализовывать скрытую альтернативу.
4. Использовать только Go, Gin, PostgreSQL, pgx, `html/template`, vanilla JavaScript и обычный CSS. Основной UI server-rendered; обычные формы используют `POST → validation → transaction → redirect → GET`.
5. Небольшие CSRF-protected fetch endpoints применять только для questionnaire autosave, bumps, voting, live counters и действительно точечных admin actions.
6. Не вводить SPA, React/Vue/Next/HTMX/Alpine, Tailwind, ORM, object storage, result file hosting/proxy, Redis, Kafka, microservices, Kubernetes, CQRS, event sourcing, event bus, generic repositories или DI framework.
7. Не добавлять speculative email service, email verification, password reset/recovery, rich text, WebSocket, общий рейтинг или overall jam winner.
8. Всегда различать `visibility` (`draft`/`published`) и effective stage (`upcoming`, `submission`, `evaluation`, `voting`, `finished`). Draft никогда не публичен; одновременно допустим не более чем один published active jam.
9. Effective stage вычисляет backend по server time и schedule с приоритетом explicit override. Cron и browser timers не авторизуют операции. Даты вводятся/показываются в `Europe/Moscow`, хранятся как `timestamptz`.
10. Гости читают только published и уже disclosed данные. Account обязателен для любой mutation участия, questionnaire, team/theme/product, bump и vote.
11. На каждой mutation повторно проверять auth, role, visibility, stage/deadline, jam relation, membership, ownership, eligibility, limits и input. Не доверять hidden inputs, client IDs, disabled controls или JavaScript state.
12. Закреплять представимые инварианты в PostgreSQL. Для check-then-write races использовать transaction и row/advisory lock, atomic SQL или эквивалент; не оставлять незащищенный `SELECT`→`INSERT`.
13. Каждое изменение schema оформлять отдельной понятной migration. PostgreSQL остается источником истины, SQL пишется явно через pgx.
14. Каждая admin mutation, влияющая на lifecycle, access, membership, user content или results, требует reason и append-only audit с material before/after в той же транзакции. Это включает обычные jam create/edit, questionnaire question/option/order edits, theme create/edit, team profile/avatar/invite interventions и все перечисленные emergency controls; секреты в audit запрещены.
15. Append-only защищается в PostgreSQL от `UPDATE`/`DELETE` application DB role или эквивалентной проверяемой DB-защитой, а не только отсутствием UI. Admin mutation и audit insert либо commit вместе, либо вместе rollback.
16. Применять disclosure rules ко всем HTML, JSON, counters, sorting, aggregates, search, exports, metadata, IDs и errors. Questionnaire answers никогда не публикуются; nomination title/list скрыты до voting, curator mark публичен с voting, team author скрыт до finished.
17. Для каждого jam существует ровно одна questionnaire shell: schema создается раньше jam handler, shell создается атомарно с jam, а публикация требует минимум один настроенный вопрос. Questionnaire responses/history и published/finished jams не hard-delete; отдельный draft-delete не входит в backlog.
18. Theme — независимая per-jam запись phrase без draft/ready lifecycle. Все темы jam входят в набор, требуется минимум одна и нет максимума; нехватка тем не подменяет автоматическую стадию, а дает критическую config error и блокирует selection/final submission. Reuse выполняется копированием.
19. Self-service create/join/leave team, profile management, invite issue/revoke, captain transfer и editor controls разрешены через конец submission в пределах правил конкретного действия; после этого составом и отзывом invite управляет только admin с audit. Captain перед leave обязан передать роль.
20. Product result/commentary URLs допускают только абсолютные `http`/`https` без credentials, malformed syntax и control characters; проверка сетевой достижимости host не требуется. Платформа хранит только карточку и URL, не файлы результата.
21. Bump mutation разрешена только в evaluation/voting с cooldown ровно одна минута для user-product, включая own product; публичный count остается видимым также в finished archive. Public authoritative vote counts доступны в voting; общего рейтинга или overall winner нет.
22. Сохранять модель универсального командного творчества: одна team принадлежит одному jam и имеет максимум один product card. Не возвращать stale модели литературного body, единственного автора или одной общей темы джема.
23. Для каждой значимой business/security задачи добавлять focused tests: stage boundaries/override, deadlines, authorization, one-active publish/override/reactivating schedule, team capacity, token handling, eligibility, hidden data, products, bumps, voting/ties и concurrency.
24. После изменения Go-кода выполнить `gofmt -w .`, `go test ./...` и `go build ./...`; также проверить миграции и затронутые пользовательские сценарии. Все failures и недоступные проверки сообщать явно.
25. Делать минимальное корректное локальное изменение, не перестраивать репозиторий ради идеальной архитектуры и не добавлять backward compatibility без конкретной необходимости.
26. Не изменять и не откатывать несвязанные изменения рабочего дерева. Не создавать commit, amend, push или pull request без отдельного явного запроса.
