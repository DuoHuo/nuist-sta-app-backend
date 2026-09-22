@echo off
REM 本地测试后端一键启停（不用 Docker）：
REM   scripts\local-test.cmd start   启动 PostgreSQL(5433) + API(:8080)
REM   scripts\local-test.cmd stop    停止两者
REM   scripts\local-test.cmd status  查看运行状态
REM
REM 依赖：本机 PostgreSQL 18（含 PostGIS/pgRouting 扩展包）与 bin\campus-api.exe
REM（go build -o bin\campus-api.exe ./cmd/server）。
REM 数据目录 %LOCALAPPDATA%\campus-map-test\pgdata（独立实例，不动系统 5432 服务），
REM trust 认证仅监听 127.0.0.1:5433，密码任意/无需密码。

setlocal
set PGBIN=C:\Program Files\PostgreSQL\18\bin
set BASE=%LOCALAPPDATA%\campus-map-test
set REPO=%~dp0..

if "%1"=="start"   goto start
if "%1"=="stop"    goto stop
if "%1"=="status"  goto status
echo 用法: %~nx0 start^|stop^|status
exit /b 1

:start
if not exist "%BASE%\pgdata\PG_VERSION" (
  echo [init] 初始化测试实例 %BASE%\pgdata
  "%PGBIN%\initdb" -D "%BASE%\pgdata" -U campus -A trust -E UTF8 --locale=C || exit /b 1
  printf "\nport = 5433\nlisten_addresses = '127.0.0.1'\n" >> "%BASE%\pgdata\postgresql.conf"
)
"%PGBIN%\pg_ctl" -D "%BASE%\pgdata" -l "%BASE%\pg.log" -w start
"%PGBIN%\psql" -h 127.0.0.1 -p 5433 -U campus -d postgres -tc "SELECT 1 FROM pg_database WHERE datname='campus'" | findstr campus >nul || "%PGBIN%\psql" -h 127.0.0.1 -p 5433 -U campus -d postgres -c "CREATE DATABASE campus"
if not exist "%REPO%\bin\campus-api.exe" (
  echo [build] 编译 campus-api
  cd /d "%REPO%" && go build -o bin\campus-api.exe ./cmd/server || exit /b 1
)
powershell -NoProfile -Command "Start-Process -FilePath '%REPO%\bin\campus-api.exe' -ArgumentList '-config','configs\config.yaml','-auto-migrate' -WorkingDirectory '%REPO%' -WindowStyle Hidden -RedirectStandardError '%BASE%\api-err.log' -RedirectStandardOutput '%BASE%\api-out.log'"
timeout /t 3 >nul
goto status

:stop
taskkill /IM campus-api.exe /F 2>nul
"%PGBIN%\pg_ctl" -D "%BASE%\pgdata" -m fast stop
exit /b 0

:status
powershell -NoProfile -Command "(Test-NetConnection 127.0.0.1 -Port 5433 -WarningAction SilentlyContinue).TcpTestSucceeded" | findstr True >nul && (echo [ok] PostgreSQL :5433) || (echo [--] PostgreSQL :5433 未运行)
powershell -NoProfile -Command "(Test-NetConnection 127.0.0.1 -Port 8080 -WarningAction SilentlyContinue).TcpTestSucceeded" | findstr True >nul && (echo [ok] API :8080  ^| curl http://127.0.0.1:8080/healthz) || (echo [--] API :8080 未运行)
endlocal
