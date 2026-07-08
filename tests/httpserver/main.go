package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintln(w, "ok")
	})
	socketPath := os.Getenv("LTM_HTTP_SOCKET")
	if socketPath == "" {
		socketPath = "/tmp/ltm-http.sock"
	}
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatal(err)
	}
	defer ln.Close()
	log.Fatal((&http.Server{Handler: mux}).Serve(ln))
}
