@echo off
REM Combat Meter — Phase 0 verification (damage meter)
REM Runs the client with debug + unknown-event logging so client/combat_diag.go
REM dumps the suspected combat events (HealthUpdate 6/7, party 231-243,
REM InCombatStateUpdate 278).
REM
REM Steps:
REM   1. Launch Albion Online and log in
REM   2. Run this .bat as Administrator (required for packet capture)
REM   3. Form or join a PARTY (party events need a real party)
REM   4. Do one dungeon pull / kill a mob camp (30s of combat is plenty)
REM   5. Close this window - then look for [COMBAT-DIAG] lines in the console
REM      output / log; the expected param layout is documented in
REM      client/combat_diag.go
cd /d "%~dp0"
albiondata-client-coldtouch.exe --log-unknown-events --debug
pause
