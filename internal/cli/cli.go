// Package cli parses the public command line and dispatches commands.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/diakovliev/doit/internal/apperr"
)

const defaultVersion = "dev"

// Invocation is the parsed command and its global options.
type Invocation struct {
	Command   string
	Arguments []string
	Request   string
	Directory string
	Profile   string
	Model     string
	Format    string
	Timeout   time.Duration
	Ephemeral bool
	NoColor   bool
	Quiet     bool
	Verbose   bool
}

// Handler executes parsed commands. The foundation leaves model execution
// injectable until the orchestration layer is implemented.
type Handler interface {
	Run(context.Context, Invocation, io.Writer) error
	Agent(context.Context, Invocation, io.Writer) error
}

// Application owns CLI versioning and command dispatch.
type Application struct {
	Version string
	Handler Handler
}

var booleanOptionHandlers = map[string]func(*Invocation){
	"-h":          func(invocation *Invocation) { invocation.Command = "help" },
	"--help":      func(invocation *Invocation) { invocation.Command = "help" },
	"--version":   func(invocation *Invocation) { invocation.Command = "version" },
	"--ephemeral": func(invocation *Invocation) { invocation.Ephemeral = true },
	"--no-color":  func(invocation *Invocation) { invocation.NoColor = true },
	"--quiet":     func(invocation *Invocation) { invocation.Quiet = true },
	"--verbose":   func(invocation *Invocation) { invocation.Verbose = true },
}

var valueOptionNames = map[string]struct{}{
	"-C":          {},
	"--directory": {},
	"-p":          {},
	"--profile":   {},
	"-m":          {},
	"--model":     {},
	"--format":    {},
	"--timeout":   {},
}

// Run executes the default CLI with an unavailable handler.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return (Application{Version: defaultVersion, Handler: unavailableHandler{}}).Execute(args, stdin, stdout, stderr)
}

// Execute parses args, handles built-in commands, and dispatches tasks.
func (application Application) Execute(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	invocation, err := Parse(args)
	if err != nil {
		writeError(stderr, err)
		return ExitCode(err)
	}
	if invocation.Command == "help" {
		writeHelp(stdout)
		return 0
	}
	if invocation.Command == "version" {
		version := application.Version
		if version == "" {
			version = defaultVersion
		}
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	}
	if application.Handler == nil {
		executeErr := apperr.New(apperr.KindInternal, "cli.execute", "command handler is not configured")
		writeError(stderr, executeErr)
		return ExitCode(executeErr)
	}

	ctx := context.Background()
	if invocation.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, invocation.Timeout)
		defer cancel()
	}
	var executeErr error
	if invocation.Command == "agent" {
		executeErr = application.Handler.Agent(ctx, invocation, stdout)
	} else {
		executeErr = application.Handler.Run(ctx, invocation, stdout)
	}
	if executeErr != nil {
		writeError(stderr, executeErr)
	}
	return ExitCode(executeErr)
}

// Parse converts Git-like command-line arguments into an invocation.
func Parse(args []string) (Invocation, error) {
	invocation := Invocation{Format: "human"}
	index, err := parseGlobalOptions(&invocation, args)
	if err != nil {
		return Invocation{}, err
	}

	if index == len(args) {
		if invocation.Command == "" {
			invocation.Command = "agent"
		}
		return invocation, nil
	}

	invocation.Command = args[index]
	index++
	invocation.Arguments = append([]string(nil), args[index:]...)
	if err := validateCommand(&invocation); err != nil {
		return Invocation{}, err
	}
	return invocation, nil
}

func parseGlobalOptions(invocation *Invocation, args []string) (int, error) {
	index := 0
	for index < len(args) {
		argument := args[index]
		if argument == "--" {
			return index + 1, nil
		}
		if !strings.HasPrefix(argument, "-") {
			return index, nil
		}
		consumed, err := parseGlobalOption(invocation, args[index:])
		if err != nil {
			return 0, err
		}
		index += consumed
	}
	return index, nil
}

func validateCommand(invocation *Invocation) error {
	switch invocation.Command {
	case "agent":
		if len(invocation.Arguments) > 0 {
			return usageError("agent does not accept positional arguments")
		}
	case "run":
		if len(invocation.Arguments) == 0 {
			return usageError("run requires a request")
		}
		invocation.Request = strings.Join(invocation.Arguments, " ")
	case "help", "version":
		if len(invocation.Arguments) > 0 {
			return usageError(invocation.Command + " does not accept arguments")
		}
	case "develop", "review", "test", "status", "model", "config", "session", "doctor":
		// These commands are reserved by the design and will be wired by later tasks.
	default:
		return usageError("unknown command: " + invocation.Command)
	}
	return nil
}

func parseGlobalOption(invocation *Invocation, args []string) (int, error) {
	argument := args[0]
	name, value, hasValue := strings.Cut(argument, "=")
	if handler, exists := booleanOptionHandlers[name]; exists {
		handler(invocation)
		return 1, nil
	}
	if _, exists := valueOptionNames[name]; !exists {
		return 0, usageError("unknown option: " + argument)
	}
	if !hasValue {
		if len(args) < 2 {
			return 0, usageError("option requires a value: " + name)
		}
		value = args[1]
		if err := setOptionValue(invocation, name, value); err != nil {
			return 0, err
		}
		return 2, nil
	}
	if err := setOptionValue(invocation, name, value); err != nil {
		return 0, err
	}
	return 1, nil
}

func setOptionValue(invocation *Invocation, name, value string) error {
	switch name {
	case "-C", "--directory":
		invocation.Directory = value
	case "-p", "--profile":
		invocation.Profile = value
	case "-m", "--model":
		invocation.Model = value
	case "--format":
		if value != "human" && value != "json" {
			return usageError("format must be human or json")
		}
		invocation.Format = value
	case "--timeout":
		timeout, err := time.ParseDuration(value)
		if err != nil || timeout <= 0 {
			return usageError("timeout must be a positive duration")
		}
		invocation.Timeout = timeout
	default:
		return usageError("unsupported option: " + name)
	}
	return nil
}

func usageError(message string) error {
	return apperr.New(apperr.KindUsage, "cli.parse", message)
}

// ExitCode maps an application error to the public CLI status contract.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch apperr.KindOf(err) {
	case apperr.KindUsage, apperr.KindConfig:
		return 2
	case apperr.KindBackend:
		return 3
	case apperr.KindPolicy, apperr.KindCancelled:
		return 4
	default:
		return 1
	}
}

func writeError(writer io.Writer, err error) {
	if writer != nil && err != nil {
		_, _ = fmt.Fprintln(writer, "doit: "+err.Error())
	}
}

func writeHelp(writer io.Writer) {
	_, _ = fmt.Fprintln(writer, "Usage: doit [global options] <command> [command options] [arguments]")
	_, _ = fmt.Fprintln(writer, "")
	_, _ = fmt.Fprintln(writer, "Commands: agent, run, develop, review, test, status, model, config, session, doctor, version")
	_, _ = fmt.Fprintln(writer, "Global options: -C, --directory; -p, --profile; -m, --model; --format; --ephemeral; --timeout")
}

type unavailableHandler struct{}

func (unavailableHandler) Run(context.Context, Invocation, io.Writer) error {
	return apperr.New(apperr.KindInternal, "cli.run", "model execution is not wired yet")
}

func (unavailableHandler) Agent(context.Context, Invocation, io.Writer) error {
	return apperr.New(apperr.KindInternal, "cli.agent", "agent execution is not wired yet")
}
