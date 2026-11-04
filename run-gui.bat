@echo off
setlocal
echo =======================================================================
echo  UniCompute Desktop Control Room (Objectives 1, 2, 3)
echo =======================================================================
echo Starting daemon and launching standalone desktop app window...
go run ./cmd/unicompute-gui %*
