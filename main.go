package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/valyala/fasthttp"
)

var timeout, _ = strconv.Atoi(os.Getenv("TIMEOUT"))
var retries, _ = strconv.Atoi(os.Getenv("RETRIES"))
var port = os.Getenv("PORT")

var client *fasthttp.Client

func main() {
	if port == "" {
		port = "8080" // default port just in case
	}
	log.Printf("Starting proxy server on port %s...", port)

	client = &fasthttp.Client{
		ReadTimeout:        time.Duration(timeout) * time.Second,
		MaxIdleConnDuration: 60 * time.Second,
	}

	if err := fasthttp.ListenAndServe(":"+port, requestHandler); err != nil {
		log.Fatalf("Error in ListenAndServe: %s", err)
	}
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	val, ok := os.LookupEnv("KEY")

	if ok && string(ctx.Request.Header.Peek("PROXYKEY")) != val {
		log.Printf("Invalid or missing PROXYKEY from %s", ctx.RemoteAddr())
		ctx.SetStatusCode(407)
		ctx.SetBody([]byte("Missing or invalid PROXYKEY header."))
		return
	}

	uri := string(ctx.Request.Header.RequestURI())
	log.Printf("Incoming request URI: %s from %s", uri, ctx.RemoteAddr())

	parts := strings.SplitN(uri[1:], "/", 2)
	if len(parts) < 2 {
		log.Printf("Invalid URL format: %s", uri)
		ctx.SetStatusCode(400)
		ctx.SetBody([]byte("URL format invalid."))
		return
	}

	response := makeRequest(ctx, 1)
	defer fasthttp.ReleaseResponse(response)

	encoding := string(response.Header.Peek("Content-Encoding"))
	log.Printf("Response status: %d, Content-Encoding: %s", response.StatusCode(), encoding)

	var body []byte
	if encoding == "gzip" {
		reader, err := gzip.NewReader(bytes.NewReader(response.Body()))
		if err != nil {
			log.Printf("Failed to decompress gzip response: %v", err)
			ctx.SetStatusCode(500)
			ctx.SetBody([]byte("Failed to decompress gzip response"))
			return
		}
		defer reader.Close()

		decompressed, err := io.ReadAll(reader)
		if err != nil {
			log.Printf("Failed to read decompressed data: %v", err)
			ctx.SetStatusCode(500)
			ctx.SetBody([]byte("Failed to read decompressed data"))
			return
		}
		body = decompressed

		// Remove headers that don't match decompressed body
		response.Header.Del("Content-Encoding")
		response.Header.Del("Content-Length")
	} else if encoding == "br" {
		// Brotli decompression not implemented; forward as-is but log warning
		log.Printf("Warning: Brotli encoding detected, forwarding raw response (may cause issues)")
		body = response.Body()
	} else {
		body = response.Body()
	}

	ctx.SetBody(body)
	ctx.SetStatusCode(response.StatusCode())

	// Hop-by-hop headers to skip
	bannedHeaders := map[string]bool{
		"Content-Encoding":    true,
		"Content-Length":      true,
		"Transfer-Encoding":   true,
		"Connection":          true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"TE":                  true,
		"Trailer":             true,
		"Upgrade":             true,
	}

	response.Header.VisitAll(func(key, value []byte) {
		k := string(key)
		if !bannedHeaders[k] {
			ctx.Response.Header.Set(k, string(value))
		}
	})
}

func makeRequest(ctx *fasthttp.RequestCtx, attempt int) *fasthttp.Response {
	if attempt > retries {
		log.Printf("Proxy failed after %d attempts", attempt-1)
		resp := fasthttp.AcquireResponse()
		resp.SetBody([]byte("Proxy failed to connect. Please try again."))
		resp.SetStatusCode(500)
		return resp
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(string(ctx.Method()))
	urlParts := strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)
	targetURL := "https://" + urlParts[0] + ".roblox.com/" + urlParts[1]
	req.SetRequestURI(targetURL)
	req.SetBody(ctx.Request.Body())

	// Copy all headers except hop-by-hop and any weird stuff
	ctx.Request.Header.VisitAll(func(key, value []byte) {
		k := string(key)
		// Don't forward 'Host' header (fasthttp will set it automatically)
		if strings.ToLower(k) != "host" && strings.ToLower(k) != "proxykey" {
			req.Header.Set(k, string(value))
		}
	})

	// Force Accept-Encoding to gzip to reduce complexity or identity for no compression:
	req.Header.Set("Accept-Encoding", "gzip")

	// User-Agent required by Roblox API
	req.Header.Set("User-Agent", "RoProxy")
	req.Header.Del("Roblox-Id")

	resp := fasthttp.AcquireResponse()
	err := client.Do(req, resp)

	if err != nil {
		log.Printf("Request error: %v, retrying attempt %d", err, attempt)
		fasthttp.ReleaseResponse(resp)
		return makeRequest(ctx, attempt+1)
	} else {
		return resp
	}
}
