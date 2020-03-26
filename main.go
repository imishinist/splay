package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"golang.org/x/time/rate"
	"golang.org/x/xerrors"
	yaml "gopkg.in/yaml.v2"
)

/*
scenarios:
  - name: ping
    url: https://google.com
    throughput: 1
    validates:
    - name: status_code=200
      status_code: 200
*/

var (
	httpWorkerNum = 20
	httpTimeout   = 10
)

func init() {
	http.DefaultTransport.(*http.Transport).MaxIdleConns = 0
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = 3000
}

// ScenarioData is scenario yaml file structure
type ScenarioData struct {
	Scenarios []Scenario `yaml:",flow"`
}

// Scenario is scenario data
type Scenario struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`

	Period     *int    `yaml:"period"`
	Count      *int    `yaml:"count"`
	Throughput float64 `yaml:"throughput"`

	Validates []Validate `yaml:",flow"`
}

// Validate is scenario validation structure
type Validate struct {
	Name string `yaml:"name"`

	StatusCode *int `yaml:"status_code"`
}

// ResultState is state of scenario result
type ResultState int

// Result enum
const (
	ResultOK ResultState = iota
	ResultValidationFail
	ResultRequestFail
)

// LoadScenarioFile read file and map ScenarioData
func LoadScenarioFile(in io.Reader) (*ScenarioData, error) {
	bytes, err := ioutil.ReadAll(in)
	if err != nil {
		return nil, err
	}

	s := ScenarioData{}

	if err := yaml.Unmarshal(bytes, &s); err != nil {
		return nil, err
	}

	return &s, nil
}

func runHttpRequest(ctx context.Context, s Scenario) (*http.Response, error) {
	req, err := http.NewRequest("GET", s.URL, nil)
	if err != nil {
		log.Printf("[%s] Error: %s", s.Name, err)
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(httpTimeout)*time.Second)
	defer cancel()

	req = req.WithContext(ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[%s] Error: %s", s.Name, err)
		return nil, err
	}
	_ = resp.Body.Close()
	return resp, nil
}

func checkResponse(ctx context.Context, s Scenario, res *http.Response) error {
	for _, v := range s.Validates {
		if v.StatusCode != nil && res.StatusCode != *v.StatusCode {
			return xerrors.Errorf("%s: status code is invalid: expected: %v, got: %v", v.Name, *v.StatusCode, res.StatusCode)
		}
	}
	return nil
}

func syncWorker(ctx context.Context, s Scenario) ResultState {
	res, err := runHttpRequest(ctx, s)
	if err != nil {
		return ResultRequestFail
	}
	if err := checkResponse(ctx, s, res); err != nil {
		log.Printf("[%s] error: %v", s.Name, err)
		return ResultValidationFail
	}
	log.Printf("[%s] Success", s.Name)

	return ResultOK
}

func scenarioWorker(ctx context.Context, scenarioCh <-chan Scenario) <-chan ResultState {
	reportCh := make(chan ResultState)

	go func() {
		defer close(reportCh)
		for s := range scenarioCh {
			log.Println("scenario received")
			reportCh <- syncWorker(ctx, s)
			log.Println("reported")
		}
	}()
	return reportCh
}

// ScenarioReport is aggregated scenario result
type ScenarioReport struct {
	SuccessCount        int
	ValidationFailCount int
	RequestFailCount    int
}

func merge(channels ...<-chan ResultState) <-chan ResultState {
	var wg sync.WaitGroup
	ret := make(chan ResultState)

	wg.Add(len(channels))
	for _, c := range channels {
		c := c
		go func() {
			for i := range c {
				ret <- i
			}
			wg.Done()
		}()
	}

	go func() {
		wg.Wait()
		close(ret)
	}()

	return ret
}

// ScenarioRun runs scenario with context
func ScenarioRun(ctx context.Context, s Scenario) ScenarioReport {
	rl := rate.NewLimiter(rate.Limit(s.Throughput), 1)

	rlCh := make(chan struct{})
	go func() {
		defer close(rlCh)
		for {
			err := rl.Wait(ctx)
			if err != nil {
				return
			}
			rlCh <- struct{}{}
		}
	}()

	var count int
	if s.Period != nil {
		count = int(math.Ceil(float64(*s.Period) * s.Throughput))
	} else {
		count = *s.Count
	}

	log.Println("scenario worker starting")
	scenarioCh := make(chan Scenario)
	chs := make([]<-chan ResultState, 0, httpWorkerNum)
	for i := 0; i < httpWorkerNum; i++ {
		chs = append(chs, scenarioWorker(ctx, scenarioCh))
	}
	log.Println("scenario worker started")

	go func() {
		defer close(scenarioCh)
		var c int
		for i := 1; i <= count; i++ {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-rlCh:
				if !ok {
					return
				}
				c++
				log.Println("scenario inserting")
				scenarioCh <- s
			}
		}
		log.Println("scenario ch closing")
	}()

	reportCh := merge(chs...)
	var success, validationFail, requestFail int
	for state := range reportCh {
		switch state {
		case ResultOK:
			success++
		case ResultValidationFail:
			validationFail++
		case ResultRequestFail:
			requestFail++
		default:
		}
	}
	log.Println("Report Ch Closed")
	return ScenarioReport{
		SuccessCount:        success,
		ValidationFailCount: validationFail,
		RequestFailCount:    requestFail,
	}
}

func main() {
	scenarioFileName := flag.String("f", "scenario.yml", "scenario file")
	flag.IntVar(&httpWorkerNum, "c", 100, "http request concurrency per scenario")
	flag.Parse()

	f, err := os.Open(*scenarioFileName)
	if err != nil {
		log.Fatal(err)
	}

	scenario, err := LoadScenarioFile(f)
	_ = f.Close()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		sig := <-c
		switch {
		case sig == os.Interrupt:
			fmt.Println("stop")
			cancel()
		default:
			log.Println("Unknown")
		}
	}()

	wg := sync.WaitGroup{}
	mutex := sync.Mutex{}
	reports := make(map[string]ScenarioReport)
	for _, s := range scenario.Scenarios {
		s := s
		wg.Add(1)
		go func(s Scenario) {
			defer wg.Done()
			defer mutex.Unlock()

			log.Printf("%s scenario start\n", s.Name)
			report := ScenarioRun(ctx, s)
			mutex.Lock()
			reports[s.Name] = report
		}(s)
	}

	log.Println("Running")
	wg.Wait()
	log.Println("--------------------Result--------------------")

	for name, report := range reports {
		var (
			success        = report.SuccessCount
			validationFail = report.ValidationFailCount
			requestFail    = report.RequestFailCount
		)
		log.Printf("finished|[%s]\tsuccess: %d, validation fail: %d, request fail: %d",
			name, success, validationFail, requestFail)
	}
}
