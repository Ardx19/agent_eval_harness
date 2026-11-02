package proxy

import (
	"context"
	"errors"
	"os"
	"strings"

	"agentevalharness/internal/sandbox"
)

// FetchKey returns the provider API key for the host proxy. pi already holds
// the key, so it is asked for it at startup; nothing is copied from pi's files.
// The key lives in memory only. Errors never include command output, which is
// where a key would appear.
func FetchKey(ctx context.Context, sys sandbox.System, provider, model string) (string, error) {
	args := []string{"auth", "print-api-key", "--provider", provider}
	if model != "" {
		args = append(args, "--model", model)
	}
	if out, err := sys.Run(ctx, "pi", args...); err == nil {
		if k := lastLine(out); k != "" {
			return k, nil
		}
	}
	for _, env := range envNames(provider) {
		if k := strings.TrimSpace(os.Getenv(env)); k != "" {
			return k, nil
		}
	}
	return "", errors.New("no API key for provider " + provider + ": `pi auth print-api-key` failed and no fallback environment variable is set")
}

func envNames(provider string) []string {
	switch provider {
	case "google":
		return []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}
	}
	return nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
