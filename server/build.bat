@echo off
chcp 65001 >nul
REM ============================================================
REM  游聚挂机程序 构建脚本（兼容 Windows 7/10）
REM  使用 Go 1.21 工具链编译（Go 1.21 起官方停止 Win7 支持声明，
REM  但编译产物仍可在 Win7 运行；1.22+ 产物可能无法在 Win7 启动）
REM  通过 rsrc 嵌入 Common Controls 6.0 manifest（walk 托盘必需），
REM  -H windowsgui 隐藏控制台窗口，日志写入 logs\youjuhang.log
REM ============================================================
setlocal

set "GO="
if exist "D:\Git\tools\go-sdk\go121\bin\go.exe" set "GO=D:\Git\tools\go-sdk\go121\bin\go.exe"
if not defined GO if exist "D:\Git\tools\go-sdk\go\bin\go.exe" set "GO=D:\Git\tools\go-sdk\go\bin\go.exe"
if not defined GO (
  where go >nul 2>nul && set "GO=go"
)
if not defined GO (
  echo [错误] 未找到 Go 1.21 工具链，请安装到 D:\Git\tools\go-sdk\go121 或加入 PATH
  pause
  exit /b 1
)
echo 使用 Go: %GO%
%GO% version

cd /d "%~dp0"
if not exist dist mkdir dist

echo.
echo == 生成资源文件（manifest）==
set "RSRC="
if exist "%USERPROFILE%\go\bin\rsrc.exe" set "RSRC=%USERPROFILE%\go\bin\rsrc.exe"
if not defined RSRC (
  where rsrc >nul 2>nul && set "RSRC=rsrc"
)
if not defined RSRC (
  echo 未找到 rsrc，正在安装...
  %GO% install github.com/akavel/rsrc@latest
  if errorlevel 1 goto :err
  if exist "%USERPROFILE%\go\bin\rsrc.exe" set "RSRC=%USERPROFILE%\go\bin\rsrc.exe"
)
if not defined RSRC (
  echo [错误] rsrc 工具不可用，无法生成 manifest 资源
  goto :err
)
"%RSRC%" -manifest app.manifest -ico internal\tray\app.ico -o cmd\youjuhang\rsrc.syso
if errorlevel 1 goto :err
echo 资源文件生成完成：cmd\youjuhang\rsrc.syso

set "CGO_ENABLED=0"
set "GOOS=windows"

echo.
echo == 构建 64 位版本 ==
set "GOARCH=amd64"
%GO% build -trimpath -ldflags "-s -w -H windowsgui" -o dist\youjuhang-win64.exe ./cmd\youjuhang
if errorlevel 1 goto :err

echo.
echo == 构建 32 位版本 ==
set "GOARCH=386"
%GO% build -trimpath -ldflags "-s -w -H windowsgui" -o dist\youjuhang-win32.exe ./cmd\youjuhang
if errorlevel 1 goto :err

echo.
echo == 守护进程（与主程序同目录发布，可选组件）==
REM 守护进程逻辑稳定，源码没变化时直接复用上一次的产物，不必每次重编。
set "GUARD_BUILD=1"
if exist "dist\youjuhang-guard.exe" (
    powershell -NoProfile -Command "$exe=(Get-Item 'dist\youjuhang-guard.exe').LastWriteTime; $src=Get-ChildItem 'cmd\guard\*.go' -ErrorAction SilentlyContinue; if (-not $src) { exit 0 }; $newest=($src | Sort-Object LastWriteTime -Descending | Select-Object -First 1).LastWriteTime; if ($newest -le $exe) { exit 0 } else { exit 1 }"
    if not errorlevel 1 set "GUARD_BUILD=0"
)
if "%GUARD_BUILD%"=="0" (
    echo 守护进程源码未变化，复用已有的 dist\youjuhang-guard.exe
) else (
    echo 源码有变化，重新构建守护进程...
    set "GOARCH=amd64"
    %GO% build -trimpath -ldflags "-s -w -H windowsgui" -o dist\youjuhang-guard.exe ./cmd\guard
    if errorlevel 1 goto :err
    echo dist\youjuhang-guard.exe（64 位；32 位主程序不支持守护，缺失时主程序独立运行）
)

echo.
echo == 复制配置样例到 dist ==
REM 仅当 dist 下尚无配置时才复制样例。
REM 否则会拿脱敏样例覆盖掉用户已配好的真实账号（含密码）——2026-09-02 修正。
set "COPY_CFG=1"
if exist dist\accounts.yaml set "COPY_CFG=0"
if "%COPY_CFG%"=="1" (
    copy configs\accounts.yaml dist\accounts.yaml >nul
    if errorlevel 1 goto :err
    echo dist\accounts.yaml（已由样例生成，请改成你的真实账号）
) else (
    echo dist\accounts.yaml 已存在，保留现有配置不覆盖
)

echo.
echo 构建完成：
echo   dist\youjuhang-win64.exe   64 位（Win7 x64 / Win10 x64，GUI 无控制台窗口）
echo   dist\youjuhang-win32.exe   32 位（Win7 x86 / x64 均可运行，GUI 无控制台窗口）
echo   dist\youjuhang-guard.exe   守护进程（32 位主程序尚不支持守护，缺失时主程序独立运行）
echo   dist\accounts.yaml         账号配置（已存在则保留；否则由 configs 样例生成，需填真实账号）
echo 日志写入 exe 目录下 logs\youjuhang.log；加 -console 参数可输出到控制台调试
goto :end

:err
echo.
echo [错误] 构建失败
exit /b 1

:end
endlocal
pause
