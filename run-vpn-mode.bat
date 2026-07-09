@echo off
REM ============================================================================
REM VPN / ExitLag / NoPing / WTFast FORCE launcher for the Albion Market
REM Analyzer client.
REM
REM NOTE: since 2026-07-09 the client handles VPNs AUTOMATICALLY — if it sees
REM no game traffic on the physical adapters within 60s it widens capture to
REM every adapter on its own, and it remembers your game server from previous
REM sessions. Normal users can just run the client directly.
REM
REM Use this launcher only to FORCE VPN mode immediately (skips the 60s wait)
REM or when the auto-detection doesn't kick in:
REM   -la              listens on EVERY up adapter (tunnel/TAP/virtual included)
REM   -force-server    pins the game server explicitly
REM
REM Change "europe" to "west" or "east" if you play on those servers.
REM Run as ADMINISTRATOR (right-click -> Run as administrator).
REM ============================================================================
cd /d "%~dp0"
albiondata-client-coldtouch.exe -la -force-server europe
pause
