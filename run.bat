@echo off
setlocal enabledelayedexpansion

rem Initialize the FILES variable.
set FILES=

rem List all .go files in the current directory, filter out filenames containing "signals_other"
for /f "delims=" %%F in ('dir /b *.go ^| findstr /v "signals_other"') do (
    set FILES=!FILES! %%F
)

rem Run go run with the accumulated file names.
go run -race %FILES%
