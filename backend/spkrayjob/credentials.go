package spkrayjob

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

const maxCredentialBytes = 8192

// readCredentialLine reads exactly one line.
//
// This used to be io.ReadAll, which waits for EOF. A terminal never reaches
// EOF, so a user who typed a password and pressed Enter saw the command hang
// with no prompt and no error until they found Ctrl-D. A credential is one
// line; reading one line works for both a pipe and a terminal.
func readCredentialLine(reader io.Reader) (string, error) {
	buffered := bufio.NewReader(io.LimitReader(reader, maxCredentialBytes+1))
	line, err := buffered.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read credential: %w", err)
	}
	if len(line) > maxCredentialBytes {
		return "", errors.New("credential is too large; check that you piped a single value")
	}
	// Only the line ending and surrounding blanks are removed: a password may
	// legitimately contain inner spaces.
	value := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
	if value == "" {
		return "", errors.New("credential is empty")
	}
	return value, nil
}

// promptSecret asks for a credential on the terminal with echo disabled. It is
// used when stdin is interactive so `spk-rayjob login` behaves like every other
// CLI a user has met, instead of demanding a shell pipeline.
func promptSecret(prompt string, stdin io.Reader, stdout io.Writer) (string, error) {
	file, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return readCredentialLine(stdin)
	}
	fmt.Fprint(stdout, prompt)
	secret, err := term.ReadPassword(int(file.Fd()))
	fmt.Fprintln(stdout)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.TrimSuffix(prompt, ": "), err)
	}
	value := strings.TrimSpace(string(secret))
	if value == "" {
		return "", errors.New("credential is empty")
	}
	return value, nil
}

// promptLine asks for a visible value, such as a username.
func promptLine(prompt string, stdin io.Reader, stdout io.Writer) (string, error) {
	fmt.Fprint(stdout, prompt)
	return readCredentialLine(stdin)
}

// isInteractive reports whether the process can prompt the user.
func isInteractive(stdin io.Reader) bool {
	file, ok := stdin.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}
