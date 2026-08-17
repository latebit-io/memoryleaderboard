// Command eval runs local, non-official Agent Memory Leaderboard checks.
package main

import (
	"flag"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const label = "LOCAL / NON-OFFICIAL AML EVALUATION"

const maxEvalTimeout = 24 * time.Hour

type config struct {
	baseURL string
	apiKey  string
	timeout time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := flag.NewFlagSet("eval", flag.ContinueOnError)
	root.SetOutput(stderr)
	baseURL := root.String("base-url", env("AML_BASE_URL", "http://127.0.0.1:8080"), "adapter base URL")
	apiKey := root.String("api-key", os.Getenv("AML_API_KEY"), "adapter API key")
	timeoutSeconds := root.Float64("timeout", 30, "request timeout in seconds")
	root.Usage = func() {
		writeLine(stderr, "usage: eval [global flags] <conformance|quality|load|prefixes|nav-check> [mode flags]")
		root.PrintDefaults()
	}
	if err := root.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	remaining := root.Args()
	if len(remaining) == 0 {
		root.Usage()
		return 2
	}
	timeout := time.Duration(*timeoutSeconds * float64(time.Second))
	if *timeoutSeconds <= 0 || math.IsNaN(*timeoutSeconds) || math.IsInf(*timeoutSeconds, 0) ||
		timeout <= 0 || timeout > maxEvalTimeout {
		writeLine(stderr, "ERROR --timeout must be between 1ns and 24h")
		return 2
	}

	mode, modeArgs := remaining[0], remaining[1:]
	if mode == "prefixes" {
		if len(modeArgs) != 0 {
			writeLine(stderr, "ERROR prefixes takes no arguments")
			return 2
		}
		return runPrefixes(stdin, stdout, stderr)
	}
	if mode == "nav-check" {
		if len(modeArgs) != 0 {
			writeLine(stderr, "ERROR nav-check takes no arguments")
			return 2
		}
		return runNavCheck(stdin, stdout, stderr)
	}

	cfg := config{
		baseURL: *baseURL,
		apiKey:  *apiKey,
		timeout: timeout,
	}
	e := evaluator{client: newClient(cfg), out: stdout, err: stderr}

	switch mode {
	case "conformance":
		flags := flag.NewFlagSet(mode, flag.ContinueOnError)
		flags.SetOutput(stderr)
		strictInvalid := flags.Bool("strict-invalid", false, "fail when unknown or trailing fields are accepted")
		if done, code := parseModeFlags(flags, modeArgs, stderr); done {
			return code
		}
		printTarget(stdout, cfg.baseURL, mode)
		return e.conformance(cfg.apiKey, *strictInvalid)
	case "quality":
		flags := flag.NewFlagSet(mode, flag.ContinueOnError)
		flags.SetOutput(stderr)
		fixture := flags.String("fixture", defaultFixturePath(), "evaluation fixture")
		topK := flags.Int("top-k", 5, "default result cutoff")
		if done, code := parseModeFlags(flags, modeArgs, stderr); done {
			return code
		}
		if !validTopK(*topK) {
			writeLine(stderr, "ERROR --top-k must be between 1 and 100")
			return 2
		}
		printTarget(stdout, cfg.baseURL, mode)
		return e.quality(*fixture, *topK)
	case "load":
		flags := flag.NewFlagSet(mode, flag.ContinueOnError)
		flags.SetOutput(stderr)
		adds := flags.Int("adds", 20, "number of Add requests")
		users := flags.Int("users", 0, "user scopes; default one user per Add")
		searches := flags.Int("searches", 40, "number of Search requests")
		concurrency := flags.Int("concurrency", 8, "maximum concurrent requests")
		topK := flags.Int("top-k", 5, "Search result cutoff")
		if done, code := parseModeFlags(flags, modeArgs, stderr); done {
			return code
		}
		if !validTopK(*topK) {
			writeLine(stderr, "ERROR --top-k must be between 1 and 100")
			return 2
		}
		printTarget(stdout, cfg.baseURL, mode)
		return e.load(loadConfig{adds: *adds, users: *users, searches: *searches, concurrency: *concurrency, topK: *topK})
	default:
		writef(stderr, "ERROR unknown mode %q\n", mode)
		root.Usage()
		return 2
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func validTopK(value int) bool {
	return value >= 1 && value <= 100
}

func printTarget(out io.Writer, baseURL, mode string) {
	writeLine(out, label)
	writef(out, "TARGET %s mode=%s\n", baseURL, mode)
}

func parseModeFlags(flags *flag.FlagSet, args []string, stderr io.Writer) (bool, int) {
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return true, 0
		}
		return true, 2
	}
	if flags.NArg() != 0 {
		writef(stderr, "ERROR unexpected arguments: %v\n", flags.Args())
		return true, 2
	}
	return false, 0
}

func defaultFixturePath() string {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return "eval/fixtures/synthetic.json"
	}
	return filepath.Join(filepath.Dir(source), "..", "..", "eval", "fixtures", "synthetic.json")
}
