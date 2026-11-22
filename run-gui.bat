@echo off
setlocal
echo =======================================================================
echo  UniCompute Desktop Control Room (Objectives 1, 2, 3)
echo =======================================================================
powershell -ExecutionPolicy Bypass -File "%~dp0run-gui.ps1" %*
