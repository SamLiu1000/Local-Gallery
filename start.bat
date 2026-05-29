@echo off
setlocal
chcp 65001 >nul

cd /d "%~dp0"

echo ========================================
echo   Local Gallery - Wails 启动器
echo ========================================
echo.

where wails >nul 2>nul
if errorlevel 1 (
    echo [错误] 未检测到 Wails CLI，请先安装：
    echo   go install github.com/wailsapp/wails/v2/cmd/wails@latest
    echo.
    pause
    exit /b 1
)

if not exist "go.mod" (
    echo [错误] 当前目录不是 Local Gallery 项目根目录
    echo.
    pause
    exit /b 1
)

REM libvips CGO 配置 (scoop 安装路径)
set CGO_ENABLED=1
set CGO_CFLAGS=-I%USERPROFILE%\scoop\apps\libvips\current\include
set CGO_LDFLAGS=-L%USERPROFILE%\scoop\apps\libvips\current\lib
set PKG_CONFIG_PATH=%USERPROFILE%\scoop\apps\libvips\current\lib\pkgconfig
set PATH=%USERPROFILE%\scoop\apps\libvips\current\bin;%PATH%

REM 检查 pkg-config 是否可用
where pkg-config >nul 2>nul
if errorlevel 1 (
    echo [错误] 未找到 pkg-config，请将 pkg-config-lite 放入以下路径：
    echo   %USERPROFILE%\scoop\apps\libvips\current\bin\pkg-config.exe
    echo   下载: https://sourceforge.net/projects/pkgconfiglite/
    pause
    exit /b 1
)

echo [1/3] 检查 Go 依赖...
go mod tidy
if errorlevel 1 (
    echo [错误] go mod tidy 执行失败
    echo.
    pause
    exit /b 1
)

echo.
echo [2/3] 启动 Wails 开发窗口...
echo     关闭窗口即可退出程序
echo.

wails dev -ldflags="-w -s"

if errorlevel 1 (
    echo.
    echo [错误] Wails 启动失败
    pause
    exit /b 1
)

endlocal