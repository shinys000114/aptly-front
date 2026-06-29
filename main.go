package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"aptly-front/internal/aptly"
	"aptly-front/internal/web"
)

func main() {
	listen := getenv("APTLY_FRONT_LISTEN", ":8088")
	apiURL := getenv("APTLY_API_URL", "http://127.0.0.1:8080")

	client, err := aptly.NewClient(apiURL, 30*time.Second)
	if err != nil {
		log.Fatalf("aptly client: %v", err)
	}

	server, err := web.NewServer(client)
	if err != nil {
		log.Fatalf("web server: %v", err)
	}

	log.Printf("aptly-front listening on %s, aptly API %s", listen, apiURL)
	if err := http.ListenAndServe(listen, server.Routes()); err != nil {
		log.Fatal(err)
	}
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
