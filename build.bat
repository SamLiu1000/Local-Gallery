@echo off
cd /d "%~dp0"
echo ========================================
echo   Local Gallery - Build Script
echo ========================================
echo.
wails build
echo.
if %ERRORLEVEL% EQU 0 (
    echo Build success! Output: build\bin\LocalGallery.exe
) else (
    echo Build failed!
)
pause
