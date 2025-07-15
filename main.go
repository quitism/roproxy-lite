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
	h := requestHandler

	client = &fasthttp.Client{
		ReadTimeout:        time.Duration(timeout) * time.Second,
		MaxIdleConnDuration: 60 * time.Second,
	}

	if err := fasthttp.ListenAndServe(":"+port, h); err != nil {
		log.Fatalf("Error in ListenAndServe: %s", err)
	}
}

func requestHandler(ctx *fasthttp.RequestCtx) {
	val, ok := os.LookupEnv("KEY")

	if ok && string(ctx.Request.Header.Peek("PROXYKEY")) != val {
		ctx.SetStatusCode(407)
		ctx.SetBody([]byte("Missing or invalid PROXYKEY header."))
		return
	}

	if len(strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)) < 2 {
		ctx.SetStatusCode(400)
		ctx.SetBody([]byte("URL format invalid."))
		return
	}

	response := makeRequest(ctx, 1)
	defer fasthttp.ReleaseResponse(response)

	var body []byte
	if string(response.Header.Peek("Content-Encoding")) == "gzip" {
		reader, err := gzip.NewReader(bytes.NewReader(response.Body()))
		if err != nil {
			ctx.SetStatusCode(500)
			ctx.SetBody([]byte("Failed to decompress gzip response"))
			return
		}
		defer reader.Close()

		decompressed, err := io.ReadAll(reader)
		if err != nil {
			ctx.SetStatusCode(500)
			ctx.SetBody([]byte("Failed to read decompressed data"))
			return
		}
		body = decompressed
		ctx.Response.Header.Del("Content-Encoding") // prevent misleading the browser
	} else {
		body = response.Body()
	}

	ctx.SetBody(body)
	ctx.SetStatusCode(response.StatusCode())

	response.Header.VisitAll(func(key, value []byte) {
		if string(key) != "Content-Encoding" { // skip if we decompressed
			ctx.Response.Header.Set(string(key), string(value))
		}
	})
}

func makeRequest(ctx *fasthttp.RequestCtx, attempt int) *fasthttp.Response {
	if attempt > retries {
		resp := fasthttp.AcquireResponse()
		resp.SetBody([]byte("Proxy failed to connect. Please try again."))
		resp.SetStatusCode(500)
		return resp
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)

	req.Header.SetMethod(string(ctx.Method()))
	url := strings.SplitN(string(ctx.Request.Header.RequestURI())[1:], "/", 2)
	req.SetRequestURI("https://" + url[0] + ".roblox.com/" + url[1])
	req.SetBody(ctx.Request.Body())

	ctx.Request.Header.VisitAll(func(key, value []byte) {
		req.Header.Set(string(key), string(value))
	})

	req.Header.Set("User-Agent", "RoProxy")
	req.Header.Del("Roblox-Id")

	resp := fasthttp.AcquireResponse()
	err := client.Do(req, resp)

	if err != nil {
		fasthttp.ReleaseResponse(resp)
		return makeRequest(ctx, attempt+1)
	} else {
		return resp
	}
}
