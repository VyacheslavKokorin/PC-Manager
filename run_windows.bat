@echo off
setlocal
cd /d %~dp0
if not exist ip-monitor.exe (
  echo Building ip-monitor.exe ...
  go build -o ip-monitor.exe .
  if errorlevel 1 (
    echo Build failed. Install Go 1.22+ and retry.
    pause
    exit /b 1
  )
)
start "IP Monitor" http://localhost:8080
ip-monitor.exe
