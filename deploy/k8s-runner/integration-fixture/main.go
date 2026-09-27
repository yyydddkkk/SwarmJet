// A deterministic, credential-free image for isolated integration validation.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: fixture server|check|sleep [port]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		port := portArg()
		http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ready\n") })
		if err := http.ListenAndServe(":"+strconv.Itoa(port), nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "check":
		port := portArg()
		host := os.Getenv("SWARMJET_SERVICE_HOST")
		if host == "" {
			fmt.Fprintln(os.Stderr, "service host missing")
			os.Exit(1)
		}
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(fmt.Sprintf("http://%s:%d/health", host, port))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 100))
		if resp.StatusCode != http.StatusOK || string(body) != "ready\n" {
			fmt.Fprintf(os.Stderr, "unhealthy: %d %q\n", resp.StatusCode, body)
			os.Exit(1)
		}
		fmt.Printf("verified service port %d\n", port)
	case "sleep":
		time.Sleep(5 * time.Minute)
	default:
		fmt.Fprintln(os.Stderr, "invalid mode")
		os.Exit(2)
	}
}
func portArg() int {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "port required")
		os.Exit(2)
	}
	p, e := strconv.Atoi(os.Args[2])
	if e != nil || p < 1 || p > 65535 {
		fmt.Fprintln(os.Stderr, "invalid port")
		os.Exit(2)
	}
	return p
}
