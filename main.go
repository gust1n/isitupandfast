package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/influxdb/influxdb/client/v2"
)

var (
	listURL               = flag.String("list-url", "", "URL to JSON-formatted list of URLs to check")
	pollInterval          = flag.Uint("poll-interval", 60, "Polling interval (in seconds)")
	requestTimeout        = flag.Uint("req-timeout", 20, "Timeout for each HTTP request, must be < than poll-interval")
	debug                 = flag.Bool("debug", false, "To enable a more verbose mode for debugging")
	influxHost            = flag.String("influx-host", "http://localhost:8086", "InfluxDB host")
	influxDBName          = flag.String("influx-db", "uptime", "InfluxDB database name")
	influxMeasurementName = flag.String("influx-measurement", "page_load_time", "InfluxDB measurement to write to")

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

	if *debug {
		fmt.Printf("Connecting to DB '%s' at %s\n", *influxDBName, *influxHost)
	}
	influxClient, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: *influxHost,
	})
	if err != nil {
		fmt.Println("Error creating InfluxDB client:", err)
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

	// Channel to send concurrent responses on
	respCh := make(chan request, 10)

	// Read responses (concurrently) as they come in
	go handleReponses(influxClient, len(list.urls), respCh)

	// The actual fetching of URLs
	for {
		list.lock.Lock()
		for _, u := range list.urls {
			// Setup request object
			req := request{
				URL:  u.URL,
				done: make(chan struct{}),
			}
			// Send it in it's own goroutine
			go send(req, time.Second*time.Duration(int(*requestTimeout)), respCh)
		}
		list.lock.Unlock()
		time.Sleep(time.Second * time.Duration(int(*pollInterval)))
	}
}

func send(req request, timeout time.Duration, resp chan request) {
	if *debug {
		fmt.Println("Sending a request to", req.URL)
	}
	start := time.Now()
	// Send the HTTP request in it's own goroutine to be able to abort the request if reached timeout
	go func() {
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
	case <-time.After(timeout):
		req.Duration = timeout
		req.err = errTimeout
	}
	// Send back on the response channel
	resp <- req
}

// Handles responses sent on passed channel
func handleReponses(cl client.Client, batchSize int, ch chan request) {
	bpConfig := client.BatchPointsConfig{
		Database:  *influxDBName,
		Precision: "ns",
	}
	// Create initial batch
	bp, _ := client.NewBatchPoints(bpConfig)
	for {
		select {
		case req := <-ch:
			tags := map[string]string{"url": req.URL}
			fields := map[string]interface{}{
				"value": strconv.FormatInt(req.Duration.Nanoseconds(), 10),
			}
			if req.err != nil {
				if req.err == errTimeout {
					tags["status_code"] = strconv.Itoa(408)
				} else {
					// If error from the HTTP request, print to stdout and dont add new point
					fmt.Printf("Error for %s: %s\n", req.URL, req.err)
					continue
				}
			} else {
				tags["status_code"] = strconv.Itoa(req.StatusCode)
			}
			pt, err := client.NewPoint(*influxMeasurementName, tags, fields)
			if err != nil {
				fmt.Println("Error creating InfluxDB point: ", err.Error())
			}
			// Add point to current batch
			bp.AddPoint(pt)
			if *debug {
				fmt.Printf("%s\t%d\t%s\n", req.URL, req.StatusCode, req.Duration)
			}
			// If should send current batch
			if len(bp.Points()) == batchSize {
				if *debug {
					fmt.Println("Writing points to InfluxDB")
				}
				cl.Write(bp)
				// Create a new batch
				bp, _ = client.NewBatchPoints(bpConfig)
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
