@echo off
setlocal

set "ROOT=%~dp0"
set "GO=%LOCALAPPDATA%\Programs\Go\go\bin\go.exe"

if not exist "%GO%" (
	for %%G in (go.exe) do set "GO=%%~$PATH:G"
)
if not defined GO (
	echo No encuentro go.exe. Instala Go 1.22+ o agrega su carpeta al PATH.
	exit /b 1
)

pushd "%ROOT%"

echo [1/4] formato
"%GO%" fmt ./... >nul || goto :fail

echo [2/4] pruebas de compilacion y vet
"%GO%" build ./... || goto :fail
"%GO%" vet ./... || goto :fail

echo [3/4] binario
"%GO%" build -trimpath -ldflags="-s -w -H=windowsgui" -o GoVoidStudio.exe ./cmd/studio || goto :fail

echo [4/4] recursos
"%GO%" run ./cmd/winres -exe GoVoidStudio.exe -name "GoVoid Studio" -company "GoVoid" -version "1.0.0.0" || goto :fail

if not exist dist mkdir dist
copy /y GoVoidStudio.exe dist\GoVoidStudio.exe >nul

for %%F in (GoVoidStudio.exe) do echo Listo: %%~fF  %%~zF bytes
popd
exit /b 0

:fail
echo.
echo Fallo la compilacion.
popd
exit /b 1
