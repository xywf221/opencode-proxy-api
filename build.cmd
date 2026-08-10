@echo off
setlocal EnableExtensions EnableDelayedExpansion

cd /d "%~dp0"

set "TARGET_OS="
set "TARGET_ARCH="
set "CGO_ENABLED_VALUE=0"
set "OUT_DIR=dist"
set "NAME=opencode-proxy"
set "PKG=./cmd/server"
set "LDFLAGS=-s -w -buildid="
set "BUILD_ARGS="
set "ALSO_LOCAL=0"

for /f "usebackq delims=" %%i in (`go env GOOS`) do if not defined TARGET_OS set "TARGET_OS=%%i"
for /f "usebackq delims=" %%i in (`go env GOARCH`) do if not defined TARGET_ARCH set "TARGET_ARCH=%%i"

:parse
if "%~1"=="" goto start_build
if "%~1"=="--os" (
  set "TARGET_OS=%~2"
  shift & shift & goto parse
)
if "%~1"=="--arch" (
  set "TARGET_ARCH=%~2"
  shift & shift & goto parse
)
if "%~1"=="--cgo" (
  set "CGO_ENABLED_VALUE=%~2"
  shift & shift & goto parse
)
if "%~1"=="--out" (
  set "OUT_DIR=%~2"
  shift & shift & goto parse
)
if "%~1"=="--name" (
  set "NAME=%~2"
  shift & shift & goto parse
)
if "%~1"=="--pkg" (
  set "PKG=%~2"
  shift & shift & goto parse
)
if "%~1"=="--ldflags" (
  set "LDFLAGS=%LDFLAGS% %~2"
  shift & shift & goto parse
)
if "%~1"=="--local" (
  set "ALSO_LOCAL=1"
  shift & goto parse
)
if "%~1"=="--help" goto usage
if "%~1"=="-h" goto usage
if "%~1"=="--" (
  shift
  goto collect_build_args
)
echo unknown argument: %~1 1>&2
goto usage_error

:collect_build_args
if "%~1"=="" goto start_build
if defined BUILD_ARGS (
  set "BUILD_ARGS=%BUILD_ARGS% %~1"
) else (
  set "BUILD_ARGS=%~1"
)
shift
goto collect_build_args

:start_build
where go >nul 2>nul
if errorlevel 1 (
  echo go not found 1>&2
  exit /b 1
)

if not exist "%OUT_DIR%" mkdir "%OUT_DIR%"

set "SUFFIX="
if /i "%TARGET_OS%"=="windows" set "SUFFIX=.exe"
set "OUTPUT=%OUT_DIR%\%NAME%-%TARGET_OS%-%TARGET_ARCH%%SUFFIX%"

echo ==^> building go: GOOS=%TARGET_OS% GOARCH=%TARGET_ARCH% CGO_ENABLED=%CGO_ENABLED_VALUE%
echo go build -trimpath -buildvcs=false -ldflags "%LDFLAGS%" -o "%OUTPUT%" %BUILD_ARGS% "%PKG%"
set "GOOS=%TARGET_OS%"
set "GOARCH=%TARGET_ARCH%"
set "CGO_ENABLED=%CGO_ENABLED_VALUE%"
go build -trimpath -buildvcs=false -ldflags "%LDFLAGS%" -o "%OUTPUT%" %BUILD_ARGS% "%PKG%"
if errorlevel 1 exit /b %errorlevel%
echo built: %OUTPUT%

if exist ".env.example" if not exist "%OUT_DIR%\.env.example" (
  copy /y ".env.example" "%OUT_DIR%\.env.example" >nul
)
if exist "config.example.json" if not exist "%OUT_DIR%\config.example.json" (
  copy /y "config.example.json" "%OUT_DIR%\config.example.json" >nul
)

if not "%ALSO_LOCAL%"=="1" goto done

set "HOST_OS="
set "HOST_ARCH="
for /f "usebackq delims=" %%i in (`go env GOOS`) do set "HOST_OS=%%i"
for /f "usebackq delims=" %%i in (`go env GOARCH`) do set "HOST_ARCH=%%i"
if /i not "%TARGET_OS%"=="%HOST_OS%" goto skip_local
if /i not "%TARGET_ARCH%"=="%HOST_ARCH%" goto skip_local

set "LOCAL_BIN=%NAME%%SUFFIX%"
set "LOCAL_NEW=%NAME%_new%SUFFIX%"
copy /y "%OUTPUT%" "%LOCAL_BIN%" >nul 2>nul
if errorlevel 1 (
  copy /y "%OUTPUT%" "%LOCAL_NEW%" >nul
  echo local locked, wrote: %LOCAL_NEW%
  echo   stop the running server, then: move /y %LOCAL_NEW% %LOCAL_BIN%
) else (
  echo local: %LOCAL_BIN%
)
goto done

:skip_local
echo skip --local: cross-compile %TARGET_OS%/%TARGET_ARCH% != host %HOST_OS%/%HOST_ARCH%

:done
echo.
echo done.
echo artifact: %OUTPUT%
echo run examples:
echo   %OUTPUT%
echo   set OPCODE_LISTEN=:8080 ^&^& set OPCODE_PROXY=socks5://127.0.0.1:1080 ^&^& %OUTPUT%
exit /b 0

:usage_error
call :print_usage 1>&2
exit /b 2

:usage
call :print_usage
exit /b 0

:print_usage
echo Usage: build.cmd [options] [-- go build args...]
echo.
echo Builds the opencode-proxy binary ^(main package at ./cmd/server^).
echo.
echo Options:
echo   --os GOOS        Target OS. Defaults to current go env GOOS.
echo   --arch GOARCH    Target architecture. Defaults to current go env GOARCH.
echo   --cgo 0^|1        CGO_ENABLED. Defaults to 0.
echo   --out DIR        Output directory. Defaults to dist.
echo   --name NAME      Binary base name. Defaults to opencode-proxy.
echo   --pkg PACKAGE    Package to build. Defaults to ./cmd/server.
echo   --ldflags VALUE  Extra value appended to release ldflags.
echo   --local          Also write/replace .\opencode-proxy[.exe] in project root
echo                    ^(if locked, writes opencode-proxy_new[.exe] instead^).
echo   -h, --help       Show this help.
echo.
echo Examples:
echo   build.cmd
echo   build.cmd --local
echo   build.cmd --os linux --arch amd64
echo   build.cmd --os windows --arch amd64 --local
exit /b 0
