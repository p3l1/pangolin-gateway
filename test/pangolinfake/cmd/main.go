// SPDX-License-Identifier: AGPL-3.0-only

// Command pangolin-fake serves the fake Integration API as a standalone process,
// so e2e can run it in the cluster the controller under test points at.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/p3l1/pangolin-gateway/test/pangolinfake"
)

func main() {
	addr := flag.String("listen", ":8080", "address to listen on")
	flag.Parse()

	srv := &http.Server{
		Addr:              *addr,
		Handler:           pangolinfake.New().Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("pangolin-fake listening on %s", *addr)
	log.Fatal(srv.ListenAndServe())
}
