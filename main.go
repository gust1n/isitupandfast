package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"
)

var (
	listURL        = flag.String("list-url", "", "URL to JSON-formatted list of URLs to check")
	pollInterval   = flag.Uint("poll-interval", 60, "Polling interval (in seconds)")
	requestTimeout = flag.Uint("req-timeout", 20, "Timeout for each HTTP request, must be < than poll-interval")
	debug          = flag.Bool("debug", false, "To enable a more verbose mode for debugging")

	// Global list of urls to fetch
	list *urlList

	errTimeout = errors.New("Request timed out")
)

type fetchableURL struct {
	URL         string
	Description string
	// More fields to come
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

	// Initialize global list
	list = &urlList{
		lock: sync.Mutex{},
	}

	// Fetch the initial list of URLs
	if err := updateURLsToFecth(*listURL, list); err != nil {
		fmt.Println("Error getting list of URLs:", err)
		os.Exit(1)
	}

	// Update the list of URLs to fetch periodically in a separate goroutine
	go func() {
		c := time.Tick(1 * time.Minute)
		for range c {
			if err := updateURLsToFecth(*listURL, list); err != nil {
				fmt.Println("Error updating list of URLs:", err)
			}
		}
	}()

	// Keep a count of which run this is
	var count int

	// Channel to send concurrent responses on
	respCh := make(chan request)

	// Read responses (concurrently) as they come in
	go handleReponses(respCh)

	// The actual fetching of URLs
	for {
		count++
		if *debug {
			fmt.Printf("Round #%d\n", count)
		}
		list.lock.Lock()
		for _, u := range list.urls {
			start := time.Now()
			// Setup request object
			req := request{
				URL:  u.URL,
				done: make(chan struct{}),
			}
			// Send each request in it's own goroutine, writing to
			go func() {
				if *debug {
					fmt.Println("Sending a request to", req.URL)
				}
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
				req.err = errTimeout
			}
			// Send back on the response channel
			respCh <- req
		}
		list.lock.Unlock()
		if *debug {
			fmt.Printf("Round #%d sent, sleeping %ds\n", count, *pollInterval)
		}
		time.Sleep(time.Second * time.Duration(int(*pollInterval)))
	}
}

// Handles responses sent on passed channel
func handleReponses(ch chan request) {
	for {
		// Print to stdout for now
		select {
		case req := <-ch:
			if req.err != nil {
				if req.err == errTimeout {
					fmt.Printf("%s\tTIMEOUT\n", req.URL)
				} else {
					fmt.Printf("Error for %s: %s\n", req.URL, req.err)
				}
			} else {
				fmt.Printf("%s\t%d\t%s\n", req.URL, req.StatusCode, req.Duration)
			}
		}
	}
}

// Update the list of urls to fecth
func updateURLsToFecth(url string, ul *urlList) error {
	if *debug {
		fmt.Println("Checking for URLs to fetch...")
	}
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	// Decode the JSON response
	decoder := json.NewDecoder(resp.Body)
	fetchableURLs := []fetchableURL{}
	if err := decoder.Decode(&fetchableURLs); err != nil {
		return err
	}
	// If new URLs were fetched, clear out the old ones and add the new ones
	list.lock.Lock()
	list.urls = fetchableURLs
	list.lock.Unlock()
	if *debug {
		fmt.Println("Current list of URLs is:")
		for _, u := range list.urls {
			fmt.Println(u.URL)
		}
	}
	return nil
}

func init() {
	flag.Parse()
}
