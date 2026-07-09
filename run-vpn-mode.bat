@echo off
REM ============================================================================
REM VPN / ExitLag / NoPing / WTFast launcher for the Albion Market Analyzer
REM client.
REM
REM Why this exists: tunneling software moves the game's decrypted UDP traffic
REM onto a virtual/tunnel network adapter that the client's default
REM physical-adapters-only capture filter skips — so loot logging, chest
REM capture and the damage meter go silent. This launcher:
REM   -la              listens on EVERY up adapter (tunnel/TAP/virtual included)
REM   -force-server    pins the game server, because with a VPN the packets
REM                    come from relay IPs and auto-detection can't work
REM
REM Change "europe" to "west" or "east" if you play on those servers.
REM Run as ADMINISTRATOR (right-click -> Run as administrator).
REM ============================================================================
cd /d "%~dp0"
albiondata-client-coldtouch.exe -la -force-server europe
pause
