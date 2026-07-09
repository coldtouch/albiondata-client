@echo off
REM ============================================================================
REM VPN / ExitLag / NoPing / WTFast FORCE launcher for the Albion Market
REM Analyzer client.
REM
REM NOTE: the client handles VPNs AUTOMATICALLY — if it sees no game traffic
REM within 60s it widens capture to every adapter, and after 120s it switches
REM to kernel-level (WinDivert) capture, which works below ANY VPN driver.
REM Normal users can just run the client directly.
REM
REM This launcher FORCES kernel-level capture immediately (skips the waiting).
REM It self-elevates because the WinDivert driver requires Administrator.
REM
REM Change "europe" to "west" or "east" if you play on those servers.
REM ============================================================================

net session >nul 2>&1
if %errorlevel% neq 0 (
    echo Kernel-level capture needs Administrator - requesting elevation...
    powershell -Command "Start-Process -FilePath '%~dpnx0' -Verb RunAs"
    exit /b
)

cd /d "%~dp0"
if exist albiondata-client.exe (
    albiondata-client.exe -windivert -force-server europe
) else (
    albiondata-client-coldtouch.exe -windivert -force-server europe
)
pause
