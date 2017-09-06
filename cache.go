// Package cachedurlresource downloads and caches a file from the given URL,
// manages periodic background updates.
package cachedurlresource

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Options struct {
	DebugName string

	CacheFile string

	URL string

	Eager bool

	CacheValidity time.Duration

	RetryInterval        time.Duration
	InitialRetryInterval time.Duration
	InitialAttemptLimit  int

	Parse func(raw []byte) (interface{}, error)

	HTTPClient *http.Client
}

type Cache struct {
	debugName string

	fn  string
	url string

	validity time.Duration

	retryInterval        time.Duration
	initialRetryInterval time.Duration
	initialAttemptLimit  int

	parse func(raw []byte) (interface{}, error)

	current interface{}
	err     error
	once    sync.Once
	mut     sync.Mutex

	shutdownc chan struct{}
	started   bool
	shutdown  bool
}

func New(opt Options) *Cache {
	if opt.URL == "" {
		panic("URL is required")
	}
	if opt.DebugName == "" {
		opt.DebugName = "cachedurlresource"
	}
	if opt.CacheValidity == 0 {
		opt.CacheValidity = 24 * time.Hour
	}
	if opt.RetryInterval == 0 {
		opt.RetryInterval = 5 * time.Minute
	}
	if opt.InitialRetryInterval == 0 {
		opt.InitialRetryInterval = 2 * time.Second
	}
	if opt.InitialAttemptLimit == 0 {
		opt.InitialAttemptLimit = 5
	}
	if opt.Parse == nil {
		opt.Parse = func(raw []byte) (interface{}, error) {
			return raw, nil
		}
	}

	cache := &Cache{
		debugName:            opt.DebugName,
		fn:                   opt.CacheFile,
		url:                  opt.URL,
		validity:             opt.CacheValidity,
		retryInterval:        opt.RetryInterval,
		initialRetryInterval: opt.InitialRetryInterval,
		initialAttemptLimit:  opt.InitialAttemptLimit,

		parse: opt.Parse,

		shutdownc: make(chan struct{}),
	}
	if opt.Eager {
		cache.mut.Lock()
		cache.started = true
		go cache.updater()
	}
	return cache
}

func (cache *Cache) Shutdown() {
	cache.mut.Lock()
	defer cache.mut.Unlock()

	if !cache.shutdown {
		close(cache.shutdownc)
		cache.shutdown = true
	}
}

func (cache *Cache) Get() interface{} {
	cache.mut.Lock()
	if !cache.started {
		cache.started = true
		go cache.updater()
		cache.mut.Lock()
	}
	defer cache.mut.Unlock()
	// log.Printf("%s: value obtained: %v", cache.debugName, cache.current)
	return cache.current
}

func (cache *Cache) updater() {
	isInitializing := true

	var last time.Time

	if cache.fn != "" {
		stat, _ := os.Stat(cache.fn)
		data, _ := ioutil.ReadFile(cache.fn)
		if data != nil {
			log.Printf("%s: loaded cached data from %v", cache.debugName, cache.fn)
			value, err := cache.parse(data)
			if err == nil {
				last = stat.ModTime()
				cache.current, cache.err = value, nil
				cache.mut.Unlock()
				isInitializing = false
			}
		}
	}

	attempts := 0
	for {
		elapsed := time.Since(last)
		if elapsed < cache.validity {
			log.Printf("%s: will update in %v", cache.debugName, (cache.validity - elapsed))
			select {
			case <-cache.shutdownc:
				return
			case <-time.After(cache.validity - elapsed):
				break
			}
		}

		log.Printf("%s: downloading from %v", cache.debugName, cache.url)
		data, err := download(cache.url)
		attempts++

		if err != nil {
			if isInitializing && attempts >= cache.initialAttemptLimit {
				log.Printf("ERROR: %s: giving up on initial attempts", cache.debugName)
				cache.current, cache.err = nil, fmt.Errorf("download failed")
				cache.mut.Unlock()
				isInitializing = false
			}

			interval := cache.retryInterval
			if isInitializing {
				interval = cache.initialRetryInterval
			}
			log.Printf("WARNING: %s: download from %v failed (attempt %v, retrying in %v): %v", cache.debugName, cache.url, attempts, interval, err)
			select {
			case <-cache.shutdownc:
				return
			case <-time.After(interval):
				break
			}
			continue
		}
		attempts = 0
		last = time.Now()

		value, err := cache.parse(data)
		if err != nil {
			log.Printf("WARNING: %s: parsing failed: %v", cache.debugName, err)
		}

		if !isInitializing {
			cache.mut.Lock()
		}
		if err == nil || cache.current == nil {
			cache.current, cache.err = value, err
		}
		cache.mut.Unlock()
		// log.Printf("%s: value updated to %v", cache.debugName, value)
		isInitializing = false

		if cache.fn != "" && err == nil {
			err = os.MkdirAll(filepath.Dir(cache.fn), 0700)
			if err != nil {
				log.Printf("WARNING: %s: failed to make cache dir: %v", cache.debugName, err)
			} else {
				err = ioutil.WriteFile(cache.fn, data, 0600)
				if err != nil {
					log.Printf("WARNING: %s: failed to save cache: %v", cache.debugName, err)
				}
			}
		}
	}
}

func download(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return ioutil.ReadAll(resp.Body)
}
