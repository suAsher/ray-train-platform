package spkrayjob

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// All paths are separate process arguments, used only with LiteralPath in the
// fixed script. No path, server value or manifest text is evaluated as code.
const windowsUpgradeScript = `param([string]$Target,[string]$Staged,[string]$Lock,[int]$ParentID)
$ErrorActionPreference = 'Stop'
$Backup = $Target + '.previous'
try {
  Wait-Process -Id $ParentID -ErrorAction SilentlyContinue
  if (Test-Path -LiteralPath $Backup) { throw 'Rollback file already exists; preserve it or remove it before upgrade.' }
  $moved = $false
  for ($i = 0; $i -lt 50; $i++) {
    try { Move-Item -LiteralPath $Target -Destination $Backup; $moved = $true; break }
    catch { Start-Sleep -Milliseconds 100 }
  }
  if (-not $moved) { throw 'Cannot move running executable.' }
  try { Move-Item -LiteralPath $Staged -Destination $Target }
  catch { Move-Item -LiteralPath $Backup -Destination $Target; throw }
} catch {
  $_ | Out-String | Set-Content -LiteralPath ($Target + '.upgrade-error.txt')
} finally {
  if (Test-Path -LiteralPath $Staged) { Remove-Item -LiteralPath $Staged }
  Remove-Item -LiteralPath $PSCommandPath
  Remove-Item -LiteralPath $Lock
}
`

func replaceExecutable(staged, target, lock string) (bool, error) {
	script := filepath.Join(lock, "replace.ps1")
	if err := os.WriteFile(script, []byte(windowsUpgradeScript), 0600); err != nil {
		return false, err
	}
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	command := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script, target, staged, lock, strconv.Itoa(os.Getpid()))
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200, HideWindow: true}
	if err := command.Start(); err != nil {
		os.Remove(script)
		return false, err
	}
	// The detached helper holds the lock and stages until the parent exits.
	command.Process.Release()
	return true, nil
}
