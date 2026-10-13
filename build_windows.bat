@echo off
setlocal
cd /d "%~dp0"
where go >nul 2>nul
if errorlevel 1 (
  echo [ERROR] Go is not installed or not on PATH.
  exit /b 1
)

echo Installing Wails CLI...
go install github.com/wailsapp/wails/v2/cmd/wails@v2.15.0
if errorlevel 1 exit /b 1
set "PATH=%PATH%;%USERPROFILE%\go\bin"

echo Downloading Go modules...
go mod tidy
if errorlevel 1 exit /b 1

echo Building SatAPRS-Go for Windows AMD64...
wails build -platform windows/amd64 -o SatAPRS-Go.exe
if errorlevel 1 exit /b 1

echo.
echo Build complete: build\bin\SatAPRS-Go.exe
endlocal
