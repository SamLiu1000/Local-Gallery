@echo off
setlocal
chcp 65001 >nul 2>nul

cd /d "%~dp0"

echo ========================================
echo   Local Gallery - Wails Launcher
echo ========================================
echo.

where wails >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Wails CLI not found. Install it with:
    echo   go install github.com/wailsapp/wails/v2/cmd/wails@latest
    echo.
    pause
    exit /b 1
)

if not exist "go.mod" (
    echo [ERROR] go.mod not found. Run this script from the project root.
    echo.
    pause
    exit /b 1
)

REM libvips CGO config (scoop)
set "VIPS_ROOT=%USERPROFILE%\scoop\apps\libvips\current"

if not exist "%VIPS_ROOT%" (
    echo [ERROR] libvips not found. Install it with:
    echo   scoop install libvips
    echo.
    pause
    exit /b 1
)

set CGO_ENABLED=1
set CGO_CFLAGS=-I%VIPS_ROOT%\include
set CGO_LDFLAGS=-L%VIPS_ROOT%\lib
set PKG_CONFIG_PATH=%VIPS_ROOT%\lib\pkgconfig
set PATH=%VIPS_ROOT%\bin;%PATH%

echo [1/2] Checking Go dependencies...
go mod tidy
if errorlevel 1 (
    echo [ERROR] go mod tidy failed
    echo.
    pause
    exit /b 1
)

echo.
echo [2/2] Starting Wails dev mode...
echo      Close this window to stop the app.
echo.

wails dev -ldflags="-w -s"

if errorlevel 1 (
    echo.
    echo [ERROR] Wails dev failed
    pause
    exit /b 1
)

endlocal
