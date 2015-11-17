package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	listURL        = flag.String("list-url", "", "URL to list of URLs to check")
	pollInterval   = flag.Uint("poll-interval", 60, "Polling interval (in seconds)")
	requestTimeout = flag.Uint("req-timeout", 20, "Timeout for each HTTP request, must be < than poll-interval")

	urls urlList = urlList{
		urls: []fetchableURL{
			fetchableURL{
				url: "https://www.google.com",
			},
			fetchableURL{
				url: "https://www.golang.org",
			},
		},
		lock: sync.Mutex{},
	} // Global list of urls to fetch
)

type fetchableURL struct {
	description string
	url         string
	// projekt     string
	// id          int
	// minute      string
	// hour        string
	// day         string
	// month       string
	// dayOfWeek   string
}

type urlList struct {
	urls []fetchableURL
	lock sync.Mutex
}

type request struct {
	URL        string
	Duration   time.Duration
	StatusCode int
	done       chan struct{} // closes when request is done
	err        error
}

func main() {
	// Validate command line arguments
	if len(*listURL) == 0 {
		fmt.Println("Must pass list-url")
		os.Exit(1)
	}
	if *requestTimeout >= *pollInterval {
		fmt.Println("req-timeout must be < than poll-interval")
		os.Exit(1)
	}

	// Fetch the list of URLs periodically in a separate goroutine
	// go func() {
	// Check for new URLs
	// for {
	// fmt.Println("Checking for URLs to fetch...")
	// resp, err := http.Get(*listURL)
	// if err != nil {
	// 	fmt.Println("Error fetching list of URLs:", err)
	// 	os.Exit(1)
	// continue
	// }
	// Decode the JSON response
	// decoder := json.NewDecoder(resp.Body)
	// fetchableURLs := make(map[string]map[string]interface{})
	// if err := decoder.Decode(&fetchableURLs); err != nil {
	// 	fmt.Println("Error parsing JSON:", err)
	// 	os.Exit(1)
	// continue
	// }
	// If new URLs were fetched, clear out the old ones and add the new ones
	// fmt.Printf("Found %d URLs to check", len(fetchableURLs))
	// urls.urls = urls.urls[:0]
	// for _, u := range fetchableURLs {
	// 	urls.urls = append(urls.urls, fetchableURL{
	// 		url: u["url"].(string),
	// 	})
	// }
	// }
	// }()

	// Wait until the global list of urls is populated
	for {
		time.Sleep(time.Second)
		urls.lock.Lock()
		defer urls.lock.Unlock()
		if len(urls.urls) > 0 {
			break
		}
		fmt.Println("Waiting for URLs to fetch...")
	}

	// Keep a count of which run this is
	var count int

	// Channel to send concurrent responses on
	respCh := make(chan request)

	// Read responses (concurrently) as they come in
	go func() {
		for {
			// Print to stdout for now
			select {
			case req := <-respCh:
				if req.err != nil {
					fmt.Printf("Error from %s: %s", req.URL, req.err)
				} else {
					fmt.Printf("%s\t%d\t%s\n", req.URL, req.StatusCode, req.Duration)
				}
			}
		}
	}()

	// The actual fetching of URLs
	for {
		count++
		fmt.Printf("Round #%d\n", count)
		// urls.lock.Lock()
		// defer urls.lock.Unlock()
		for _, u := range urls.urls {
			start := time.Now()
			// Setup request object
			req := request{
				URL:  u.url,
				done: make(chan struct{}),
			}
			// Send each request in it's own goroutine, writing to
			go func() {
				fmt.Println("Sending a request to", req.URL)
				resp, err := http.Get(req.URL)
				if err != nil {
					req.err = err
				} else {
					req.StatusCode = resp.StatusCode
				}
				// Indicate the request is finished
				close(req.done)

			}()
			// Either the request finished, or the timeout triggers
			select {
			case <-req.done:
				// How long did the request take, only applies if not timed out
				req.Duration = time.Since(start)
			case <-time.After(time.Second * time.Duration(int(*requestTimeout))):
				req.err = errors.New("Request timed out")
			}
			// Send back on the response channel
			respCh <- req
		}
		fmt.Printf("Round #%d sent, sleeping %ds\n", count, *pollInterval)
		time.Sleep(time.Second * time.Duration(int(*pollInterval)))
	}
}

func init() {
	flag.Parse()
}
