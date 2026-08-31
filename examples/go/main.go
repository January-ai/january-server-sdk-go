package main

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/January-ai/january-server-sdk-go/january"
	"github.com/joho/godotenv"
)

func main() {
	// Load only the working directory's .env; existing environment values win.
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("Unable to load .env. Check that it is readable and contains valid KEY=value entries.")
	}
	secretKey := strings.TrimSpace(os.Getenv("JANUARY_API_KEY"))
	if secretKey == "" {
		log.Fatal("Set JANUARY_API_KEY in .env or your environment before running this example.")
	}
	client, err := january.NewClient(january.Config{
		SecretKey: secretKey,
	})
	if err != nil {
		log.Fatal("Invalid January client configuration. Use a server sk- API key, not a ct- client token.")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/january/token", func(response http.ResponseWriter, request *http.Request) {
		// LOCAL DEMO ONLY. Replace this with the application's verified session/JWT.
		endUserID := strings.TrimSpace(request.Header.Get("x-demo-user-id"))
		if endUserID == "" {
			writeJSON(response, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		// Identity comes from the demo header, never the request body. The server
		// selects the grant; callers cannot supply scopes or a token lifetime.
		token, _, err := client.MintClientToken(request.Context(), january.MintClientTokenRequest{
			EndUserID:  endUserID,
			Scopes:     january.Value([]string{"foods:read"}),
			TTLSeconds: january.Value(float64(1800)),
		})
		if err != nil {
			writeJSON(response, http.StatusBadGateway, map[string]string{"error": "Unable to mint client token."})
			return
		}
		// Preserve the client SDK relay shape, not the upstream snake_case DTO.
		writeJSON(response, http.StatusOK, struct {
			Token     string  `json:"token"`
			ExpiresIn float64 `json:"expiresIn"`
		}{Token: token.Token, ExpiresIn: token.ExpiresIn})
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "4030"
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		log.Fatal("Unable to listen on the local demo port. Check PORT and whether it is already in use.")
	}
	log.Printf("January Go partner example listening on http://%s", listener.Addr())
	if err := http.Serve(listener, mux); err != nil {
		log.Fatal("The local demo server stopped.")
	}
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("content-type", "application/json")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		log.Print("Unable to write the JSON response.")
	}
}
