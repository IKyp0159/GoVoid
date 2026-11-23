@echo off
rem Compila TU copia del agente con tus credenciales, sin escribirlas en el
rem código ni en el repo. Lee agent.local.env (ignorado por git).
rem
rem   agent.local.env:
rem     GOVOID_TOKEN=...
rem     GOVOID_GUILD=...
rem     GOVOID_WEBHOOK=...
setlocal enabledelayedexpansion

set "ROOT=%~dp0"
set "GO=%LOCALAPPDATA%\Programs\Go\go\bin\go.exe"
if not exist "%GO%" for %%G in (go.exe) do set "GO=%%~$PATH:G"
if not defined GO (
	echo No encuentro go.exe.
	exit /b 1
)

if not exist "%ROOT%agent.local.env" (
	echo Falta agent.local.env con GOVOID_TOKEN, GOVOID_GUILD y GOVOID_WEBHOOK.
	exit /b 1
)

for /f "usebackq tokens=1,* delims==" %%A in ("%ROOT%agent.local.env") do (
	if /i "%%A"=="GOVOID_TOKEN" set "GV_TOKEN=%%B"
	if /i "%%A"=="GOVOID_GUILD" set "GV_GUILD=%%B"
	if /i "%%A"=="GOVOID_WEBHOOK" set "GV_HOOK=%%B"
)

if not defined GV_TOKEN goto :missing
if not defined GV_GUILD goto :missing
if not defined GV_HOOK goto :missing

pushd "%ROOT%"
"%GO%" build -trimpath ^
	-ldflags "-s -w -H=windowsgui -X github.com/local/govoid/internal/config.BotToken=%GV_TOKEN% -X github.com/local/govoid/internal/config.GuildID=%GV_GUILD% -X github.com/local/govoid/internal/config.WebhookURL=%GV_HOOK%" ^
	-o agent.exe ./cmd/agent || goto :fail
popd

for %%F in ("%ROOT%agent.exe") do echo Listo: %%~fF  %%~zF bytes
exit /b 0

:missing
echo agent.local.env incompleto.
exit /b 1

:fail
popd
echo Fallo la compilacion.
exit /b 1
