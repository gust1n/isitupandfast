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

	"github.com/influxdb/influxdb/client/v2"
)

var (
	listURL               = flag.String("list-url", "", "URL to JSON-formatted list of URLs to check")
	pollInterval          = flag.Uint("poll-interval", 60, "Polling interval (in seconds)")
	requestTimeout        = flag.Uint("req-timeout", 20, "Timeout for each HTTP request (in seconds)")
	precision             = flag.String("precision", "ns", "Precision, valid values are ns and ms")
	debug                 = flag.Bool("debug", false, "To enable a more verbose mode for debugging")
	influxBatchSize       = flag.Int("influx-batch", 0, "Batch size to write to Influx, 0 means batch size = number of URLs")
	influxHost            = flag.String("influx-host", "http://localhost:8086", "InfluxDB host")
	influxDBName          = flag.String("influx-db", "uptime", "InfluxDB database name")
	influxMeasurementName = flag.String("influx-measurement", "page_load_time", "InfluxDB measurement to write to")

	// Global list of urls to fetch
	list *urlList

	errTimeout = errors.New("Request timed out")
)

type fetchableURL struct {
	URL string
}

type urlList struct {
	urls []fetchableURL
	lock sync.Mutex
}

type request struct {
	httpReq    *http.Request
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
	if *precision != "ns" && *precision != "ms" {
		fmt.Println("precision must be ms or ns")
		os.Exit(1)
	}

	if *debug {
		fmt.Printf("Connecting to InfluxDB %s@%s\n", *influxDBName, *influxHost)
	}
	influxClient, err := client.NewHTTPClient(client.HTTPConfig{
		Addr: *influxHost,
	})
	if err != nil {
		fmt.Println("Error creating InfluxDB client:", err)
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

	// Channel to send concurrent responses on
	respCh := make(chan request)

	// Read responses (concurrently) as they come in
	go handleReponses(influxClient, *influxBatchSize, respCh)

	// Setup HTTP client to use to send the requests
	tr := &http.Transport{
		DisableKeepAlives: true,
	}
	httpClient := &http.Client{Transport: tr}

	// The actual fetching of URLs
	for {
		// Make sure the list is not tampered with when iterating over it
		list.lock.Lock()
		for _, u := range list.urls {
			// Setup underlying http request
			httpReq, err := http.NewRequest("GET", u.URL, nil)
			if err != nil {
				fmt.Println(err)
				continue
			}

			// Setup request object
			req := request{
				done:    make(chan struct{}),
				httpReq: httpReq,
			}
			// Send it in it's own goroutine
			go send(httpClient, tr, req, time.Second*time.Duration(int(*requestTimeout)), respCh)
		}
		list.lock.Unlock()
		time.Sleep(time.Second * time.Duration(int(*pollInterval)))
	}
}

// send sends a request, kills it after passed timeout and writes the result to passed channel
func send(httpClient *http.Client, tr *http.Transport, req request, timeout time.Duration, resp chan request) {
	if *debug {
		fmt.Println("Sending a request to", req.httpReq.URL)
	}
	// Send the HTTP request in it's own goroutine using a HTTP client to be able to abort the request if reached timeout
	go func() {
		start := time.Now()
		resp, err := httpClient.Do(req.httpReq)
		if err != nil {
			req.err = err
		} else {
			req.StatusCode = resp.StatusCode
			req.Duration = time.Since(start)
			resp.Body.Close()
		}
		// Indicate the request is finished
		close(req.done)
	}()
	// Either the request finished, or the timeout triggers
	select {
	case <-req.done:
		// Request is done, please continue
	case <-time.After(timeout):
		req.Duration = timeout
		// Manually cancel the request in flight
		tr.CancelRequest(req.httpReq)
		req.err = errTimeout
	}
	// Send back on the response channel
	resp <- req
}

// Handles responses sent on passed channel
func handleReponses(cl client.Client, batchSize int, ch chan request) {
	bpConfig := client.BatchPointsConfig{
		Database:  *influxDBName,
		Precision: *precision,
	}
	// Create initial batch
	bp, _ := client.NewBatchPoints(bpConfig)
	for {
		select {
		case req := <-ch:
			tags := map[string]string{"url": req.httpReq.URL.String()}
			dur := req.Duration.Nanoseconds()
			if *precision == "ms" {
				dur = dur / 1000000
			}
			fields := map[string]interface{}{
				"value": dur,
			}
			if req.err != nil {
				if req.err == errTimeout {
					fields["status_code"] = 408
				} else {
					// If error from the HTTP request, print to stdout and dont add new point
					fmt.Printf("Error for %s: %s\n", req.httpReq.URL, req.err)
					continue
				}
			} else {
				fields["status_code"] = req.StatusCode
			}
			pt, err := client.NewPoint(*influxMeasurementName, tags, fields)
			if err != nil {
				fmt.Println("Error creating InfluxDB point: ", err.Error())
			}
			// Add point to current batch
			bp.AddPoint(pt)
			if *debug {
				fmt.Printf("%s\t%d\t%s\n", req.httpReq.URL, req.StatusCode, req.Duration)
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
	// Update InfluxDB batch size (if not manually set)
	if *influxBatchSize == 0 {
		*influxBatchSize = len(list.urls)
	}

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
