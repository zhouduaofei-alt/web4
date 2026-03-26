@echo off
setlocal enabledelayedexpansion

cd /d "%~dp0"

git rev-parse --is-inside-work-tree >nul 2>nul
if errorlevel 1 (
    echo Not a git repository.
    pause
    exit /b 1
)

for /f "delims=" %%i in ('git symbolic-ref --short HEAD 2^>nul') do set BRANCH=%%i
if not defined BRANCH (
    echo Unable to detect current branch.
    pause
    exit /b 1
)

set /p COMMIT_MSG=Commit message (leave blank to use timestamp): 
if not defined COMMIT_MSG (
    for /f "delims=" %%i in ('powershell -NoProfile -Command "Get-Date -Format \"yyyy-MM-dd HH:mm:ss\""') do set COMMIT_TIME=%%i
    set "COMMIT_MSG=auto update !COMMIT_TIME!"
)

echo.
echo Current branch: !BRANCH!
echo Commit message: !COMMIT_MSG!
echo.

git add .
git diff --cached --quiet
if not errorlevel 1 (
    echo No changes to commit.
    pause
    exit /b 0
)

git commit -m "!COMMIT_MSG!"
if errorlevel 1 (
    echo Commit failed.
    pause
    exit /b 1
)

git push -u origin !BRANCH!
if errorlevel 1 (
    echo Push failed.
    pause
    exit /b 1
)

echo.
echo Push completed successfully.
pause
