package rayctl

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ray-train-platform-backend/domain"
)

// Run executes one rayctl subcommand. Credentials are read only from the
// environment or an owner-only configuration file, never command arguments.
func Run(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	if len(arguments) == 0 {
		return errors.New("command is required")
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	switch arguments[0] {
	case "login-check":
		return runLoginCheck(ctx, arguments[1:], stdout, stderr, getenv)
	case "package":
		return runPackage(arguments[1:], stdout)
	case "submit":
		return runSubmit(ctx, arguments[1:], stdout, stderr, getenv)
	case "status":
		return runStatus(ctx, arguments[1:], stdout, stderr, getenv)
	case "logs":
		return runLogs(ctx, arguments[1:], stdout, stderr, getenv)
	case "cancel":
		return runCancel(ctx, arguments[1:], stdout, stderr, getenv)
	default:
		return fmt.Errorf("unknown command")
	}
}

type connectionFlags struct {
	server string
	caFile string
	config string
	debug  bool
}

func bindConnectionFlags(set *flag.FlagSet, flags *connectionFlags) {
	set.StringVar(&flags.server, "server", "", "platform API base URL")
	set.StringVar(&flags.caFile, "ca-file", "", "PEM file containing the private CA")
	set.StringVar(&flags.config, "config", "", "owner-only rayctl config file")
	set.BoolVar(&flags.debug, "debug", false, "write redacted request diagnostics to stderr")
}

func newCommandClient(flags connectionFlags, getenv func(string) string, stderr io.Writer) (*Client, error) {
	server := strings.TrimSpace(flags.server)
	if server == "" {
		server = strings.TrimSpace(getenv("RAY_PLATFORM_URL"))
	}
	needsConfig := server == "" || strings.TrimSpace(getenv("RAY_PLATFORM_TOKEN")) == ""
	var config configFile
	if needsConfig {
		loaded, err := loadConfig(flags.config)
		if err != nil {
			return nil, err
		}
		config = loaded
		if server == "" {
			server = config.Server
		}
	}
	token, err := LoadToken(getenv("RAY_PLATFORM_TOKEN"), flags.config)
	if err != nil {
		return nil, err
	}
	caFile := strings.TrimSpace(flags.caFile)
	if caFile == "" {
		caFile = strings.TrimSpace(getenv("SSL_CERT_FILE"))
	}
	var debugWriter io.Writer
	if flags.debug {
		debugWriter = stderr
	}
	return NewClient(ClientOptions{ServerURL: server, Token: token, CAFile: caFile, DebugWriter: debugWriter})
}

func runLoginCheck(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("login-check", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 {
		return errors.New("invalid login-check arguments")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	_, err = client.LoginCheck(ctx)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "login ok")
	return err
}

func runPackage(arguments []string, stdout io.Writer) error {
	set := flag.NewFlagSet("package", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	directory := set.String("dir", ".", "source directory")
	output := set.String("output", "", "archive output path")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 || strings.TrimSpace(*output) == "" {
		return errors.New("package requires --output")
	}
	if _, err := os.Lstat(*output); err == nil {
		return errors.New("package output already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect package output: %w", err)
	}
	archive, err := BuildArchive(*directory)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o700); err != nil {
		_ = os.Remove(archive.Path)
		return fmt.Errorf("create package output directory: %w", err)
	}
	if err := os.Rename(archive.Path, *output); err != nil {
		_ = os.Remove(archive.Path)
		return fmt.Errorf("write package: %w", err)
	}
	archive.Path = *output
	return writeJSON(stdout, archive)
}

func runSubmit(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("submit", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	directory := set.String("dir", ".", "source directory")
	name := set.String("name", "", "job DNS name")
	image := set.String("image", "", "pinned training image")
	entrypoint := set.String("entrypoint", "", "shell command to run")
	queue := set.String("queue", "", "authenticated tenant queue")
	workers := set.Int("workers", 1, "worker replicas")
	gpus := set.Int("gpus-per-worker", 1, "GPUs per worker")
	cpu := set.Int64("cpu-per-worker", 8, "CPUs per worker")
	memory := set.String("memory-per-worker", "32Gi", "memory per worker")
	if err := set.Parse(arguments); err != nil || set.NArg() != 0 || strings.TrimSpace(*name) == "" || strings.TrimSpace(*image) == "" || strings.TrimSpace(*entrypoint) == "" || strings.TrimSpace(*queue) == "" {
		return errors.New("submit requires --name, --image, --entrypoint, and --queue")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	job, err := client.SubmitDirectory(ctx, *directory, domain.JobSpec{
		Name: *name, Image: *image, Queue: *queue,
		Entrypoint: domain.Entrypoint{Command: []string{"/bin/sh", "-lc", *entrypoint}},
		Resources:  domain.Resources{WorkerReplicas: *workers, GPUsPerWorker: *gpus, CPUPerWorker: *cpu, MemoryPerWorker: *memory},
	})
	if err != nil {
		return err
	}
	return writeJSON(stdout, job.Raw)
}

func runStatus(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	client, jobID, err := parseJobCommand("status", arguments, getenv, stderr)
	if err != nil {
		return err
	}
	job, err := client.Status(ctx, jobID)
	if err != nil {
		return err
	}
	return writeJSON(stdout, job.Raw)
}

func runLogs(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	set := flag.NewFlagSet("logs", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	limit := set.Int("limit", 1000, "maximum log lines")
	if err := set.Parse(arguments); err != nil || set.NArg() != 1 || *limit < 1 || *limit > 10000 {
		return errors.New("logs requires a job ID and a limit between 1 and 10000")
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return err
	}
	logs, err := client.Logs(ctx, set.Arg(0), *limit)
	if err != nil {
		return err
	}
	return writeJSON(stdout, logs)
}

func runCancel(ctx context.Context, arguments []string, stdout, stderr io.Writer, getenv func(string) string) error {
	client, jobID, err := parseJobCommand("cancel", arguments, getenv, stderr)
	if err != nil {
		return err
	}
	result, err := client.Cancel(ctx, jobID)
	if err != nil {
		return err
	}
	return writeJSON(stdout, result)
}

func parseJobCommand(command string, arguments []string, getenv func(string) string, stderr io.Writer) (*Client, string, error) {
	set := flag.NewFlagSet(command, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	var connection connectionFlags
	bindConnectionFlags(set, &connection)
	if err := set.Parse(arguments); err != nil || set.NArg() != 1 || strings.TrimSpace(set.Arg(0)) == "" {
		return nil, "", fmt.Errorf("%s requires a job ID", command)
	}
	client, err := newCommandClient(connection, getenv, stderr)
	if err != nil {
		return nil, "", err
	}
	return client, set.Arg(0), nil
}

func writeJSON(writer io.Writer, value any) error {
	if raw, ok := value.(json.RawMessage); ok {
		_, err := fmt.Fprintln(writer, string(raw))
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(writer, string(encoded))
	return err
}
