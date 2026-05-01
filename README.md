# Portable Desktop Site: IP Monitor Panel

Локальный "сайт-панель" для Windows: добавляете IP-адреса, сервер пингует каждую секунду и показывает статистику (UP/DOWN, задержка, потери).

## Быстрый старт (Windows)
1. Установите Go 1.22+ (один раз).
2. `git clone <repo>`
3. Двойной клик по `run_windows.bat`

Скрипт соберёт один файл `ip-monitor.exe` и запустит его.

## Что внутри
- `main.go` — HTTP сервер + ping мониторинг.
- `web/` — фронтенд панели.
- `run_windows.bat` — запуск "в один файл" для Windows.
