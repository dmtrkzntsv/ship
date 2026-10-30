// Command fakes3 serves an in-memory S3 API (gofakes3) for ship's
// end-to-end test, with one bucket created at startup. It accepts any
// credentials.
package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9000", "listen address")
	bucket := flag.String("bucket", "lab", "bucket to create at startup")
	flag.Parse()

	backend := s3mem.New()
	if err := backend.CreateBucket(*bucket); err != nil {
		log.Fatalf("fakes3: create bucket %s: %v", *bucket, err)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           gofakes3.New(backend).Server(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("fakes3: serving bucket %s on %s", *bucket, *addr)
	log.Fatal(srv.ListenAndServe())
}
