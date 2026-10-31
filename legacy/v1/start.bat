@echo off
setlocal EnableExtensions EnableDelayedExpansion
title Apple Music Downloader

set "PROJECT_DIR=%~dp0"
set "DOCKER_TIMEOUT=180"
set "READY_TIMEOUT=30"

pushd "%PROJECT_DIR%" >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Cannot open the project directory: %PROJECT_DIR%
    endlocal & exit /b 1
)

echo ========================================
echo   Apple Music Downloader
echo ========================================
echo.

where docker >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Docker Desktop or Docker Engine is not installed or is not on PATH.
    call :diagnostics
    popd
    endlocal & exit /b 1
)

docker compose version >nul 2>&1
if errorlevel 1 (
    echo [ERROR] Docker Compose v2 is required. Install or update Docker Desktop.
    popd
    endlocal & exit /b 1
)

set /a docker_waited=0
:wait_docker
docker info >nul 2>&1
if not errorlevel 1 goto docker_ready

if !docker_waited! EQU 0 if exist "%ProgramFiles%\Docker\Docker\Docker Desktop.exe" (
    echo [INFO] Docker is not running. Starting Docker Desktop...
    start "" "%ProgramFiles%\Docker\Docker\Docker Desktop.exe"
)
if !docker_waited! GEQ %DOCKER_TIMEOUT% (
    echo [ERROR] Docker did not become ready within %DOCKER_TIMEOUT% seconds.
    call :diagnostics
    popd
    endlocal & exit /b 1
)

echo [INFO] Waiting for Docker... !docker_waited!/%DOCKER_TIMEOUT%s
timeout /t 5 /nobreak >nul
set /a docker_waited+=5
goto wait_docker

:docker_ready
echo [INFO] Validating Docker Compose configuration...
docker compose config --quiet
if errorlevel 1 (
    echo [ERROR] Docker Compose configuration is invalid.
    popd
    endlocal & exit /b 1
)

echo [INFO] Building and starting services (up to %DOCKER_TIMEOUT% seconds)...
docker compose up -d --build --wait --wait-timeout %DOCKER_TIMEOUT%
if errorlevel 1 (
    echo [ERROR] Services did not become healthy.
    call :diagnostics
    popd
    endlocal & exit /b 1
)

set /a ready_waited=0
:wait_ready
call :check_ready
if not errorlevel 1 goto service_ready
if !ready_waited! GEQ %READY_TIMEOUT% (
    echo [ERROR] The HTTP service did not become ready.
    call :diagnostics
    popd
    endlocal & exit /b 1
)
timeout /t 2 /nobreak >nul
set /a ready_waited+=2
goto wait_ready

:service_ready
echo [INFO] Opening browser...
start "" http://127.0.0.1:8080
echo.
echo ========================================
echo   Server running at http://127.0.0.1:8080
echo   Press any key to stop services
echo ========================================
pause >nul

echo [INFO] Stopping services. Persistent data is kept in downloads, data, and wrapper-data.
docker compose down
set "stop_status=!errorlevel!"
popd
endlocal & exit /b %stop_status%

:diagnostics
echo.
echo [DIAGNOSTICS] Docker Compose service status:
docker compose ps
echo.
echo [DIAGNOSTICS] Last 200 service log lines:
docker compose logs --tail 200 app
exit /b 0

:check_ready
where curl.exe >nul 2>&1
if not errorlevel 1 (
    curl.exe -fsS --max-time 5 http://127.0.0.1:8080/api/v1/health/ready >nul 2>&1
    exit /b !errorlevel!
)

powershell.exe -NoProfile -NonInteractive -Command "try { $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 5 -Uri 'http://127.0.0.1:8080/api/v1/health/ready'; if ($response.StatusCode -eq 200) { exit 0 }; exit 1 } catch { exit 1 }" >nul 2>&1
exit /b %errorlevel%
