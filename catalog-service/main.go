package main

import (
	"fmt"
	"net/http"
)

func main() {
	http.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	panic(http.ListenAndServe(":8080", nil))
}
