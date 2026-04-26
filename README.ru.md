# mytonprovider-backend

Backend сервис для mytonprovider.org - сервис мониторинга провайдеров TON Storage.

## Описание

Данный backend сервис:
- Взаимодействует с провайдерами хранилища через ADNL протокол
- Мониторит производительность, доступность провайдеров, доступность хранимых файлов, проводит проверки здоровья
- Обрабатывает телеметрию от провайдеров
- Предоставляет API эндпоинты для фронтенда
- Вычисляет рейтинг, аптайм, статус провайдеров
- Собирает собственные метрики через **Prometheus**

## Продакшен установка

Автоматические установочные скрипты рассчитаны на чистый Debian 12 сервер с root-доступом.

1. Скачайте bootstrap-скрипт для SSH на своей локальной машине:

```bash
wget https://raw.githubusercontent.com/dearjohndoe/mytonprovider-backend/refs/heads/master/scripts/init_server_connection.sh
```

2. Пробросьте SSH ключ и отключите вход по паролю:

```bash
USERNAME=root PASSWORD=supersecretpassword HOST=123.45.67.89 bash init_server_connection.sh
```

3. Подключитесь к серверу и скачайте установщик:

```bash
ssh root@123.45.67.89
wget https://raw.githubusercontent.com/dearjohndoe/mytonprovider-backend/refs/heads/master/scripts/setup_server.sh
```

4. Запустите полную установку:

```bash
PG_USER=appuser PG_PASSWORD=secret PG_DB=providerdb NEWFRONTENDUSER=jdfront NEWSUDOUSER=johndoe NEWUSER_PASSWORD=newsecurepassword bash ./setup_server.sh
```

Примечания:
- `PG_USER` больше не обязан быть `pguser`; владелец схем подставляется из env при инициализации БД.
- PostgreSQL по умолчанию настраивается только для локального доступа, backend подключается к `127.0.0.1:5432`.
- `build_frontend.sh` больше не патчит frontend исходники. Если нужен абсолютный API URL на этапе сборки, передайте `FRONTEND_API_BASE_URL=...`.

## Локальная разработка

### Требования

- Docker и плагин Docker Compose

### 1. Поднимите PostgreSQL

```bash
docker compose up -d postgres
```

### 2. Инициализируйте схему БД

```bash
docker compose run --rm db-init
```

### 3. Запустите backend в Docker

```bash
docker compose up backend
```

Если compose-файл менялся или backend остался в старом состоянии, пересоздайте сервис:

```bash
docker compose rm -sf backend
docker compose up --force-recreate backend
```

Полезные локальные эндпоинты:
- `http://localhost:9090/health`
- `http://localhost:9090/api/v1/providers/filters`
- `http://localhost:9090/metrics`

### Полный локальный стек с frontend

Чтобы поднять PostgreSQL, backend и frontend вместе:

```bash
docker compose up -d postgres
docker compose run --rm db-init
docker compose -f docker-compose.yml -f docker-compose.full.yml up backend frontend
```

Frontend будет доступен на `http://localhost:3000` и внутри Docker использует `http://backend:9090`.

Если frontend контейнер уже падал во время установки зависимостей, перед повторным запуском пересоздайте его volumes:

```bash
docker compose -f docker-compose.yml -f docker-compose.full.yml down -v
```

### Альтернатива: запуск backend на хосте

```bash
cp .env.example .env
bash scripts/init_local_db.sh
bash scripts/dev_backend.sh
```

В этом варианте PostgreSQL остается в Docker, а backend запускается командой `go run -tags=debug ./cmd` на хосте и локально обрабатывает CORS и `OPTIONS` без nginx.

### VS Code

Если удобнее запускать из VS Code, используйте те же значения из `.env` и оставьте `buildFlags: "-tags=debug"`.

## Структура проекта

```
├── cmd/                   # Точка входа приложения, конфиги, инициализация
├── pkg/                   # Пакеты приложения
│   ├── cache/             # Кастомный кеш
│   ├── httpServer/        # Fiber хандлеры сервера
│   ├── models/            # Модели данных для БД и API
│   ├── repositories/      # Вся работа с postgres здесь
│   ├── services/          # Бизнес логика
│   ├── tonclient/         # TON blockchain клиент, обертка для нескольких полезных функций
│   └── workers/           # Воркеры
├── db/                    # Схема базы данных
├── scripts/               # Скрипты настройки и утилиты
```

## API Эндпоинты

Сервер предоставляет REST API эндпоинты для:
- Сбора телеметрии провайдеров
- Информации о провайдерах и инструменты фильтрации
- Метрик

## Воркеры

Приложение запускает несколько фоновых воркеров:
- **Providers Master**: Управляет жизненным циклом провайдеров, проверками здоровья и хранимых файлов
- **Telemetry Worker**: Обрабатывает входящюю телеметрию
- **Cleaner Worker**: Чистит базу данных от устаревшей информации

## Режим агента

Backend поддерживает отдельный запуск агента проверки storage proofs. В этом режиме процесс не подключается к Postgres и не поднимает публичный API, а получает задачи от координатора по HTTP.

На координаторе оставьте обычный запуск и задайте `SYSTEM_ACCESS_TOKENS` как md5-хеш токена агента. Агент отправляет исходный токен в `Authorization: Bearer ...`.

Пример env для агента:

```bash
APP_ROLE=agent
AGENT_ID=vps-1
AGENT_COORDINATOR_URL=https://mytonprovider.org
AGENT_ACCESS_TOKEN=raw-agent-token
AGENT_BATCH_SIZE=100
AGENT_POLL_INTERVAL_SECONDS=30
SYSTEM_ADNL_PORT=16167
TON_CONFIG_URL=https://ton-blockchain.github.io/global.config.json
```

Текущий агент выполняет `storage_proof_check`: получает контракты от координатора, находит ADNL/IP данные провайдеров, проверяет proofs и отправляет назад найденные IP и статусы контрактов.

## Лицензия

Apache-2.0



Этот проект был создан по заказу участника сообщества TON Foundation.
