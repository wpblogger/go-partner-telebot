# Сервис c telegram ботом для партнеров
Для запуска нужна база данных Postgres

## Запуск сервиса:
```bash
docker build -t go-partner-bot .
docker run -it -p 8012:8012 go-partner-bot:latest
```

## Настройки сервиса:
1. Урл для получения версии - /api/system/version
2. Переменная среды GO_TELEGRAM_BOT_PORT - порт на котором будет слушать приложение (по умолчанию: 8012)
3. Переменная среды SENTRYURL - урл на Sentry для логирования ошибок
4. Переменная среды GO_TELEGRAM_BOT_BRANCH - версия сервиса
5. Переменная среды POSTGRES_HOST - Хост с базой
6. Переменная среды POSTGRES_PORT - Порт для подключения к базе
7. Переменная среды POSTGRES_USER - Пользователь 
8. Переменная среды POSTGRES_PASSWORD - Пароль
9. Переменная среды POSTGRES_DATABASE - Имя базы
10. Переменная среды TELEGRAM_TOKEN - telegram токен
11. Переменная среды WALLET_API_URL - url для подключения к сервису кошелек для юникоинов
12. Переменная среды DJANGO_API_URL - url для подключения к сервису монолита
