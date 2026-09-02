@echo off
chcp 65001 >nul
REM ============================================================
REM  游聚挂机程序 —— Linux 交叉编译脚本（在 Windows 上执行）
REM
REM  产出：
REM    dist\youjuhang-linux-amd64   64 位 x86 服务器（绝大多数云主机）
REM    dist\youjuhang-linux-arm64   ARM64 服务器（甲骨文云 ARM / 树莓派 / 国产化平台）
REM
REM  为什么需要单独一个脚本（不复用 build.bat）：
REM    cmd\youjuhang\rsrc.syso 是 Windows PE 资源文件（托盘图标 + Common Controls
REM    manifest，由 rsrc 工具生成）。Go 链接器在交叉编译时也会把它塞进去，
REM    amd64/386 侥幸能过，但 arm64 会直接失败：
REM      _pkg_.a(rsrc.syso): unknown ARM64 relocation type 3
REM    所以构建前必须临时移走它，构建完再恢复（Windows 托盘仍然需要）。
REM
REM  部署到 Linux 后：
REM    1. chmod +x youjuhang-linux-amd64
REM    2. 同目录放 accounts.yaml（或 -config 指定路径）
REM    3. 服务器无桌面环境，务必加 -no-browser，否则 xdg-open 会报一条 Warn（不影响运行）
REM    4. 建议 -console 配合 systemd / nohup，日志走标准输出由 journald 收集
REM    5. 监听地址：想外网访问需 -web 0.0.0.0:29090（见下方安全警告）
REM
REM  【安全警告】Web 控制台当前没有任何认证机制，任何能访问该端口的人都可以
REM  启停账号、增删账号、改密码、读日志。用 0.0.0.0 暴露到公网前，请先加
REM  Basic Auth / 反向代理鉴权 / SSH 隧道，切勿直接裸奔。
REM ============================================================
setlocal

set "GO=D:\Git\tools\go-sdk\go121\bin\go.exe"
if not exist "%GO%" (
  where go >nul 2>nul && (set "GO=go") || (
    echo [错误] 未找到 Go 工具链，请安装到 D:\Git\tools\go-sdk\go121 或加入 PATH
    pause
    exit /b 1
  )
)
echo 使用 Go: %GO%
%GO% version

cd /d "%~dp0"
if not exist dist mkdir dist

REM ---- 临时移走 Windows 资源文件 ----
set "SYSO=cmd\youjuhang\rsrc.syso"
set "SYSO_MOVED="
if exist "%SYSO%" (
  move /y "%SYSO%" "%SYSO%.bak" >nul
  set "SYSO_MOVED=1"
  echo 已临时移走 rsrc.syso（Windows PE 资源，Linux 链接不兼容）
)

set "CGO_ENABLED=0"
set "GOOS=linux"
set "BUILD_ERR=0"

echo.
echo == 构建 Linux amd64 ==
set "GOARCH=amd64"
%GO% build -trimpath -ldflags "-s -w" -o dist\youjuhang-linux-amd64 ./cmd\youjuhang
if errorlevel 1 (
  echo [错误] Linux amd64 构建失败
  set "BUILD_ERR=1"
  goto :restore
)
echo dist\youjuhang-linux-amd64

echo.
echo == 构建 Linux arm64 ==
set "GOARCH=arm64"
%GO% build -trimpath -ldflags "-s -w" -o dist\youjuhang-linux-arm64 ./cmd\youjuhang
if errorlevel 1 (
  echo [错误] Linux arm64 构建失败
  set "BUILD_ERR=1"
  goto :restore
)
echo dist\youjuhang-linux-arm64

:restore
REM ---- 恢复 Windows 资源文件 ----
if defined SYSO_MOVED (
  if exist "%SYSO%.bak" move /y "%SYSO%.bak" "%SYSO%" >nul
  echo 已恢复 rsrc.syso
)

echo.
echo == 复制配置文件到 dist ==
copy /y configs\accounts.yaml dist\accounts.yaml >nul
echo dist\accounts.yaml

if "%BUILD_ERR%"=="1" goto :err

echo.
echo 构建完成：
echo   dist\youjuhang-linux-amd64   64 位 x86
echo   dist\youjuhang-linux-arm64   ARM64
echo   dist\accounts.yaml           账号配置
echo.
echo Linux 上运行示例：
echo   chmod +x youjuhang-linux-amd64
echo   ./youjuhang-linux-amd64 -config accounts.yaml -console -no-browser
echo 外网访问（务必先做鉴权）：
echo   ./youjuhang-linux-amd64 -console -no-browser -web 0.0.0.0:29090
goto :end

:err
echo.
echo [错误] 构建失败
exit /b 1

:end
endlocal
pause
