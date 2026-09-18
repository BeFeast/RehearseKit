package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	addr := flag.String("addr", envOr("STEMD_ADDR", ":8010"), "listen address")
	root := flag.String("root", envOr("STEMD_ROOT", "/tmp/storage"), "storage root (stems under <root>/stems/<job>/)")
	origins := flag.String("cors-origins", envOr("STEMD_CORS_ORIGINS", "*"), "comma-separated CORS origins, or *")
	flag.Parse()

	var allowed []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed = append(allowed, o)
		}
	}
	logger := log.New(os.Stderr, "stemd ", log.LstdFlags)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           NewServer(Config{Root: *root, AllowedOrigins: allowed}, logger),
		ReadHeaderTimeout: 10 * time.Second,
	}
	logger.Printf("listening on %s, root=%s, origins=%v", *addr, *root, allowed)
	if err := srv.ListenAndServe(); err != nil {
		logger.Fatal(err)
	}
}
