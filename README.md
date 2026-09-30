# ДЗ-1: архитектура маркетплейса

Проект содержит C4 Container диаграмму целевой архитектуры и минимально запускаемый `catalog-service`.
В C4 контейнер означает приложение или хранилище, а не обязательно Docker-контейнер. Бизнес-логика, клиентское
приложение, остальные сервисы, базы данных и брокер сообщений пока не реализованы.

## C4 Container диаграмма

Текстовый исходник: [`docs/c4-container.mmd`](docs/c4-container.mmd). Готовый рендер: [`docs/diagram.png`](docs/diagram.png).

```mermaid
---
config:
  layout: elk
  flowchart:
    curve: linear
    nodeSpacing: 35
    rankSpacing: 55
  elk:
    preset: modelOrder
    nodePlacementStrategy: LINEAR_SEGMENTS
    considerModelOrder: PREFER_NODES
    straightenEdges: true
    lineHops: arc
    mergeEdges: false
---

flowchart TB

    buyer["Покупатель<br/>[Person]"]
    seller["Продавец<br/>[Person]"]
    psp["Платёжный провайдер<br/>[External system]"]
    notification_provider["Провайдер уведомлений<br/>[External system]<br/>Email / push"]

    buyer ~~~ seller

    subgraph marketplace["Маркетплейс [Software System]"]
        direction TB

        web["Web SPA<br/>[Container: React / TypeScript]<br/>Интерфейс покупателя и продавца"]
        gateway["API Gateway<br/>[Container: Nginx]<br/>Раздача SPA и маршрутизация API"]

        subgraph services["Доменные контейнеры"]
            direction LR

            users["Users service<br/>[Container: Go / HTTP API]<br/>Пользователи, роли, предпочтения"]
            usersdb[("Users DB<br/>[Container: PostgreSQL]")]
            users -->|"Читает и пишет [SQL/TCP]"| usersdb

            catalog["Catalog service<br/>[Container: Go / net/http]<br/>Товары, цены, доступность"]
            catalogdb[("Catalog DB<br/>[Container: PostgreSQL]")]
            catalog -->|"Читает и пишет [SQL/TCP]"| catalogdb

            feed["Feed service<br/>[Container: Go / HTTP API]<br/>Собирает ленту при запросе"]
            feeddb[("Feed projection DB<br/>[Container: PostgreSQL]<br/>Товары и предпочтения")]
            feed -->|"Читает и пишет [SQL/TCP]"| feeddb

            orders["Orders service<br/>[Container: Go / HTTP API]<br/>Заказы, статусы"]
            ordersdb[("Orders DB<br/>[Container: PostgreSQL]")]
            orders -->|"Читает и пишет [SQL/TCP]"| ordersdb

            payments["Payments service<br/>[Container: Go / HTTP API]<br/>Расчёт и учёт платежей"]
            paymentsdb[("Payments DB<br/>[Container: PostgreSQL]")]
            payments -->|"Читает и пишет [SQL/TCP]"| paymentsdb

            notifications["Notifications service<br/>[Container: Go / Kafka consumer]<br/>Отправка уведомлений о заказах"]
        end

        subgraph broker["Брокер событий<br/>[Container: Apache Kafka]"]
            direction LR
            topic_preferences["PreferencesChanged<br/>[Kafka topic]"]
            topic_products["ProductChanged<br/>[Kafka topic]"]
            topic_payments["PaymentStatusChanged<br/>[Kafka topic]"]
            topic_orders["OrderStatusChanged<br/>[Kafka topic]"]
        end
    end

    %% Клиенты и публичный API
    buyer -->|"Просматривает ленту и оформляет заказы [HTTPS]"| web
    seller -->|"Управляет товарами [HTTPS]"| web
    web -->|"Вызывает публичное API [HTTPS/JSON]"| gateway

    %% Синхронные вызовы
    gateway -->|"Запрашивает профиль и предпочтения [HTTP/JSON]"| users
    gateway -->|"Управляет каталогом [HTTP/JSON]"| catalog
    gateway -->|"Получает ленту [HTTP/JSON]"| feed
    gateway -->|"Оформляет заказы [HTTP/JSON]"| orders
    orders -->|"Проверяет цену и доступность [HTTP/JSON]"| catalog
    orders -->|"Создаёт платёж [HTTP/JSON]"| payments
    payments -->|"Проводит оплату [HTTPS/JSON]"| psp
    notifications -->|"Отправляет уведомление [HTTPS/JSON]"| notification_provider

    %% Асинхронные события: публикация и доставка
    users -.->|"Публикует PreferencesChanged [Kafka]"| topic_preferences
    topic_preferences -.->|"Читает PreferencesChanged [Kafka]"| feed
    catalog -.->|"Публикует ProductChanged [Kafka]"| topic_products
    topic_products -.->|"Читает ProductChanged [Kafka]"| feed
    payments -.->|"Публикует PaymentStatusChanged [Kafka]"| topic_payments
    topic_payments -.->|"Читает PaymentStatusChanged [Kafka]"| orders
    orders -.->|"Публикует OrderStatusChanged [Kafka]"| topic_orders
    topic_orders -.->|"Читает OrderStatusChanged [Kafka]"| notifications

    %% Стили
    classDef person fill:#084c61,color:#fff,stroke:#063845
    classDef external fill:#f1f5f9,stroke:#475569,color:#0f172a
    classDef frontend fill:#ccfbf1,stroke:#0f766e,color:#134e4a
    classDef gateway fill:#0f766e,color:#fff,stroke:#115e59
    classDef service fill:#e0f2fe,stroke:#0369a1,color:#082f49
    classDef data fill:#fef3c7,stroke:#b45309,color:#451a03
    classDef broker fill:#f3e8ff,stroke:#7e22ce,color:#3b0764

    class buyer,seller person
    class psp,notification_provider external
    class web frontend
    class gateway gateway
    class users,catalog,feed,orders,payments,notifications service
    class usersdb,catalogdb,feeddb,ordersdb,paymentsdb data
    class topic_preferences,topic_products,topic_payments,topic_orders broker
```

## Клиенты и технологии

Покупатель и продавец работают через одно веб-приложение на React и TypeScript. Отдельное мобильное приложение для
первой версии не требуется. Nginx отдаёт статические файлы SPA и маршрутизирует запросы к HTTP API; клиент обращается к
нему по HTTPS, а сервисы внутри системы обмениваются HTTP/JSON.

Для доменных сервисов выбран Go. `Catalog` уже запускается на стандартной библиотеке `net/http`; технологии остальных
сервисов на диаграмме — проектное решение, а не готовая реализация. Users, Catalog, Feed, Orders и Payments владеют
отдельными базами PostgreSQL. Notifications не хранит постоянных данных. Apache Kafka передаёт изменения между
сервисами без синхронного ожидания получателей.

## Домены и сервисы

| Домен / сервис | Ответственность                                    | Данные, которыми владеет сервис                                 |
|----------------|----------------------------------------------------|-----------------------------------------------------------------|
| Users          | Регистрация и управление покупателями и продавцами | учётные записи, роли, предпочтительные категории                |
| Catalog        | Управление продавцом товарами                      | товары, цены, доступность                                       |
| Feed           | Персональная выдача товаров покупателю             | проекции товаров и предпочтений, но не готовые ленты            |
| Orders         | Оформление и сопровождение заказа                  | заказы, позиции, снимки цен, статусы заказов                    |
| Payments       | Расчёт и учёт оплаты                               | сумма к оплате, попытки платежа, записи учёта, статусы платежей |
| Notifications  | Доставка сообщений о состоянии заказа              | постоянных данных нет                                           |

Каждый сервис с базой читает и изменяет только свою базу; общих баз и прямого доступа к данным другого сервиса нет.
Notifications получает события из Kafka и отправляет сообщения через внешнего провайдера.

`Feed` обновляет собственную проекцию по событиям `ProductChanged` и `PreferencesChanged`, а первичные данные остаются у
`Catalog` и `Users`. Готовая лента для каждого покупателя не хранится: при запросе сервис выдаёт доступные товары из
предпочитаемых категорий перед остальными, внутри группы сортируя по новизне. Для такой первой версии достаточно
PostgreSQL. Redis можно добавить как кэш результатов при подтверждённой нагрузке; отдельного кэша на схеме пока нет.

`Catalog` хранит товары, цены и доступность в PostgreSQL. Для отличающихся по категориям атрибутов подойдёт `jsonb`;
переход на документную БД сам по себе не обещает ускорения. Если понадобится сложный полнотекстовый поиск и фильтры,
можно добавить производный поисковый индекс, обновляемый из `ProductChanged`.

## Взаимодействия

Web SPA обращается по HTTPS/JSON к Nginx, который направляет запрос нужному сервису. Для оформления заказа `Orders`
синхронно по HTTP/JSON запрашивает в `Catalog` актуальные цену и доступность, затем вызывает `Payments` для начала
платежа. `Payments` обращается к внешнему платёжному провайдеру (PSP) по HTTPS/JSON. `Notifications` по HTTPS/JSON
отправляет сообщение внешнему провайдеру уведомлений.

Через Kafka сервисы взаимодействуют асинхронно. На диаграмме внутри Kafka показаны топики, а не дополнительные
сервисы; стрелки отражают публикацию и получение каждого события:

| Событие                | Отправитель → получатель | Назначение                          |
|------------------------|--------------------------|-------------------------------------|
| `ProductChanged`       | Catalog → Feed           | Обновить проекцию товара в ленте    |
| `PreferencesChanged`   | Users → Feed             | Обновить проекцию предпочтений      |
| `PaymentStatusChanged` | Payments → Orders        | Изменить статус заказа после оплаты |
| `OrderStatusChanged`   | Orders → Notifications   | Отправить покупателю уведомление    |

Синхронные вызовы нужны там, где заказ нельзя продолжить без текущих цены, наличия или создания платежа. События
подходят для распространения изменений и уведомлений: получателям не требуется отвечать в рамках исходного запроса.
`Notifications` подтверждает обработку события после успешного вызова провайдера. При сбое между вызовом и
подтверждением событие может прийти повторно. Если появятся требования к защите от дублей или истории доставки,
потребуются идемпотентность провайдера или учёт попыток с ограниченным сроком хранения; Redis с TTL сам по себе не
устраняет риск повторной отправки при таком сбое.

При дальнейшей реализации стоит рассмотреть gRPC для внутренних синхронных вызовов `Orders → Catalog` и
`Orders → Payments`: `.proto`-контракты позволят генерировать типизированных клиентов. Решение можно принять после
появления реальных API и требований к задержке или нагрузке; границы сервисов и асинхронные события от выбора
транспорта не зависят.

## Варианты декомпозиции

### Вариант 1: модульный монолит

Все домены находятся в одном приложении с одной базой данных, но разделены на модули.

Плюсы: один деплой, простые транзакции между заказом и платежом, меньше инфраструктуры. Минусы: общая схема данных
размывает владение, выпуск и масштабирование ленты связаны с каталогом и заказами, изменение одного модуля может
затронуть всё приложение.

### Вариант 2: доменные сервисы с отдельными базами

Users, Catalog, Feed, Orders, Payments и Notifications — отдельные сервисы, которые обмениваются HTTP-вызовами и
событиями.

Плюсы: явное владение данными, независимое масштабирование ленты и каталога, изоляция изменений. Минусы: нужны брокер,
мониторинг и обслуживание пяти баз; проекции обновляются не мгновенно, а часть операций проходит через несколько
сервисов.

### Выбранный вариант

Выбран вариант 2: ленту можно масштабировать независимо от оформления заказов, а платёжные данные и операции остаются
в отдельном домене `Payments`. Собственные базы делают границы ответственности явными; события позволяют обновлять
ленту и отправлять уведомления без ожидания в запросе пользователя. Цена выбора — усложнение эксплуатации и задержка
между изменением данных и обновлением их проекций.

## Что реализовано сейчас

Работает только `catalog-service`: `GET /health` на порту `8080` возвращает `200 OK` и тело `ok\n`. У него нет
бизнес-эндпоинтов, логирования, базы данных и интеграций. Остальные элементы на диаграмме — целевая архитектура, а не
поднятые контейнеры.

## Запуск в Docker

Для стандартного запуска нужны Docker CLI и Colima. Из корня репозитория:

```bash
make up
```

Проверка и остановка:

```bash
curl -i http://localhost:8080/health
make down
```

Проверьте статус и тело ответа (`curl -i` также выведет служебные заголовки):

```text
HTTP/1.1 200 OK
...

ok
```

По умолчанию `make up` использует Docker context `colima-distsys`. Если профиль Colima `distsys` ещё не запущен,
команда спросит подтверждение перед его созданием или запуском. Для другого Docker context укажите его явно, например
`make DOCKER_CONTEXT=desktop-linux up` (и аналогично для `down`).

Если установлен `direnv`, после клонирования выполните `direnv allow`: `.envrc` направит обычные команды `docker` в
`colima-distsys`. Вход в каталог сам по себе не запускает виртуальную машину.
