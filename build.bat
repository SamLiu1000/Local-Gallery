@echo off
setlocal
chcp 65001 >nul 2>nul

cd /d "%~dp0"

echo ========================================
echo   Local Gallery - Build Script
echo ========================================
echo.

REM Check wails
where wails >nul 2>nul
if errorlevel 1 (
    echo [ERROR] Wails CLI not found. Install it with:
    echo   go install github.com/wailsapp/wails/v2/cmd/wails@latest
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
    echo Expected path: %VIPS_ROOT%
    pause
    exit /b 1
)

set CGO_ENABLED=1
set CGO_CFLAGS=-I%VIPS_ROOT%\include
set CGO_LDFLAGS=-L%VIPS_ROOT%\lib
set PKG_CONFIG_PATH=%VIPS_ROOT%\lib\pkgconfig
set PATH=%VIPS_ROOT%\bin;%PATH%

echo [1/3] Building (wails build)...
wails build
if errorlevel 1 (
    echo.
    echo [ERROR] Build failed
    pause
    exit /b 1
)

echo.
echo [2/3] Copying libvips DLLs to output directory...

set "VIPS_BIN=%VIPS_ROOT%\bin"
set "OUT_DIR=build\bin"

if exist "%VIPS_BIN%\*.dll" (
    xcopy /y /q "%VIPS_BIN%\*.dll" "%OUT_DIR%\" >nul
    echo       Copied libvips DLL files
) else (
    echo [WARNING] No DLL files found in %VIPS_BIN%
)

echo.
echo ========================================
echo   Build succeeded!
echo   Output: %~dp0build\bin\LocalGallery.exe
echo   libvips DLLs are bundled alongside.
echo   Distribute the entire build\bin folder.
echo ========================================
echo.

endlocal
pause
