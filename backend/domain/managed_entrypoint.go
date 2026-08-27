package domain

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	managedPythonModule = regexp.MustCompile(`^[A-Za-z_]\w*(?:\.[A-Za-z_]\w*)*$`)
	managedShellModule  = regexp.MustCompile(`^python[ \t]+-m[ \t]+([^ \t'"]+)(?:[ \t]|$)`)
	managedShellScript  = regexp.MustCompile(`^python[ \t]+([^ \t'"]+\.py)(?:[ \t]|$)`)
	managedTorchrunWord = regexp.MustCompile(`(?:^|[ \t])torchrun(?:[ \t]|$)`)
)

func validateManagedEntrypoint(entrypoint Entrypoint) error {
	command := append([]string(nil), entrypoint.Command...)
	if len(command) == 3 && command[0] == "/bin/sh" && command[1] == "-lc" {
		if len(entrypoint.Args) != 0 {
			return fmt.Errorf("managed entrypoint shell wrapper must not have separate arguments")
		}
		return validateManagedShellCommand(command[2])
	}
	if len(command) > 0 && command[0] == "/bin/sh" {
		return fmt.Errorf("managed entrypoint supports only the exact /bin/sh -lc compatibility wrapper")
	}
	command = append(command, entrypoint.Args...)
	return validateManagedPythonWords(command)
}

// validateManagedShellCommand recognizes the legacy transport shape without
// interpreting or tokenizing shell syntax. Shell control characters and
// escapes are rejected first; a narrow prefix then identifies only the Python
// script or module target. The managed runtime performs the final argv decode
// with Python's shlex, but this boundary never invokes a shell.
func validateManagedShellCommand(raw string) error {
	command := strings.TrimSpace(raw)
	if strings.ContainsAny(command, "\\\r\n`;&|<>()") || strings.Count(command, "'")%2 != 0 || strings.Count(command, `"`)%2 != 0 {
		return fmt.Errorf("managed entrypoint must not contain shell operators or malformed quoting")
	}
	if managedTorchrunWord.MatchString(command) {
		return fmt.Errorf("managed entrypoint must not contain torchrun")
	}
	if match := managedShellModule.FindStringSubmatch(command); len(match) == 2 {
		if !managedPythonModule.MatchString(match[1]) {
			return fmt.Errorf("managed entrypoint Python module must be a dotted module name")
		}
		return nil
	}
	if match := managedShellScript.FindStringSubmatch(command); len(match) == 2 && safeManagedPythonPath(match[1]) {
		return nil
	}
	return fmt.Errorf("managed entrypoint must use python file.py or python -m module with a safe target")
}

func validateManagedPythonWords(words []string) error {
	if len(words) == 0 {
		return fmt.Errorf("managed entrypoint is required")
	}
	for _, word := range words {
		if word == "torchrun" {
			return fmt.Errorf("managed entrypoint must not contain torchrun")
		}
		if strings.ContainsAny(word, "\r\n`;&|<>()") {
			return fmt.Errorf("managed entrypoint must not contain shell operators")
		}
	}
	if words[0] != "python" {
		return fmt.Errorf("managed entrypoint must start with python")
	}
	if len(words) >= 3 && words[1] == "-m" {
		if !managedPythonModule.MatchString(words[2]) {
			return fmt.Errorf("managed entrypoint Python module must be a dotted module name")
		}
		return nil
	}
	if len(words) < 2 || strings.HasPrefix(words[1], "-") || !safeManagedPythonPath(words[1]) {
		return fmt.Errorf("managed entrypoint must use python file.py or python -m module with a safe target")
	}
	return nil
}

func safeManagedPythonPath(target string) bool {
	if !strings.HasSuffix(target, ".py") || path.IsAbs(target) || strings.Contains(target, `\`) {
		return false
	}
	for _, segment := range strings.Split(target, "/") {
		if segment == ".." {
			return false
		}
	}
	return target != "" && !strings.HasPrefix(target, "-")
}
