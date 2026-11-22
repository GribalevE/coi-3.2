@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

if not exist "coi-launcher.exe" (
    echo.
    echo [FAIL] рядом с START.cmd нет файла coi-launcher.exe
    echo        Распакуйте архив целиком, не перетаскивая файлы по одному.
    echo.
    pause
    exit /b 2
)

coi-launcher.exe %*
set "code=%errorlevel%"

if not "%code%"=="0" (
    echo.
    echo Код завершения: %code%
    echo Выше указано, что именно не так. Исправьте и запустите START.cmd снова.
    echo.
    pause
)

exit /b %code%
