package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/January-ai/january-server-sdk-go/january"
	"github.com/joho/godotenv"
)

func main() {
	os.Exit(run(os.Stdout, os.Stderr))
}

func run(stdout, stderr io.Writer) int {
	// Load only the working directory's .env; existing environment values win.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "Unable to load .env. Check that it is readable and contains valid KEY=value entries.")
		return 1
	}
	key := strings.TrimSpace(os.Getenv("JANUARY_API_KEY"))
	if key == "" {
		fmt.Fprintln(stderr, "Set JANUARY_API_KEY in .env or your environment before running this example.")
		return 1
	}
	client, err := january.NewClient(january.Config{
		SecretKey: key,
		Timeout:   30 * time.Second,
	})
	if err != nil {
		fmt.Fprintln(stderr, "Invalid January client configuration. Use a server sk- API key, not a ct- client token.")
		return 1
	}
	// Synthetic demo identity; use your authenticated user's ID in production.
	user, err := client.ForUser("january-quickstart", "UTC")
	if err != nil {
		fmt.Fprintln(stderr, "Unable to configure the January user context.")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	foods, _, err := user.Foods.Search(ctx, january.SearchFoodsRequest{Query: "banana"})
	if err != nil {
		printSearchFailure(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Found %.0f foods.\n", foods.TotalCount)
	if len(foods.Items) == 0 {
		fmt.Fprintln(stdout, "No results.")
	} else {
		fmt.Fprintf(stdout, "First food: %s\n", foods.Items[0].Name)
	}
	return 0
}

func printSearchFailure(stderr io.Writer, err error) {
	var apiError *january.APIError
	hint := "Contact support@january.ai with these safe diagnostic fields."
	switch {
	case errors.As(err, &apiError):
		// Use SDK-sanitized fields, not raw Response headers or error/body text.
		// Quoting also escapes control characters in server-supplied identifiers.
		fmt.Fprintf(stderr, "Food search failed: status=%d code=%q request_id=%q.\n",
			apiError.StatusCode, apiError.Code, apiError.RequestID)
		switch {
		case apiError.StatusCode == 401:
			hint = "Check that JANUARY_API_KEY is the full, active server sk- key for your organization."
		case apiError.StatusCode == 403:
			hint = "Check your organization's access and the key's permissions; client tokens are not needed for server food search."
		case apiError.StatusCode == 429 && apiError.Code == "rate_limited":
			hint = "Reduce request frequency and wait before explicitly trying again; this example does not retry."
		case apiError.StatusCode == 429 && apiError.Code == "credit_limit_exceeded":
			hint = "Check your plan and credit allowance at https://dashboard.january.ai/billing; this example does not retry."
		case apiError.StatusCode == 429:
			hint = "Check rate limits and your plan's credit allowance before explicitly trying again."
		}
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "Food search failed: transport timeout.")
		hint = "Check network access; review the 30-second deadline before trying again."
	default:
		fmt.Fprintln(stderr, "Food search failed: transport or response error.")
		hint = "Check network access; contact support@january.ai if the problem persists."
	}
	fmt.Fprintln(stderr, hint)
}
