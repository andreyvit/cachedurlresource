package cachedurlresource

import (
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSimple(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "Hello")
	}))
	defer ts.Close()

	cache := New(Options{
		URL:       ts.URL,
		DebugName: "TestSimple",
		Parse:     parse,
	})
	defer cache.Shutdown()

	value := cache.Get().(string)

	if a, e := value, "Hello\n"; a != e {
		t.Errorf("** Invalid value, got %q, wanted %q", a, e)
	}
}

func TestInitialRetry(t *testing.T) {
	var log []string
	var mut sync.Mutex
	var attempt int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mut.Lock()
		defer mut.Unlock()
		attempt++
		if attempt == 5 {
			fmt.Fprint(w, "Hello")
		} else {
			log = append(log, fmt.Sprintf("fail %d", attempt))
			http.Error(w, "Oops", http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	cache := New(Options{
		URL:                  ts.URL,
		DebugName:            "TestInitialRetry",
		InitialRetryInterval: 1 * time.Millisecond,
		InitialAttemptLimit:  5,
		Parse:                parse,
		Eager:                false,
	})
	defer cache.Shutdown()

	value := cache.Get().(string)

	mut.Lock()
	defer mut.Unlock()
	log = append(log, value)

	if a, e := strings.Join(log, " / "), "fail 1 / fail 2 / fail 3 / fail 4 / Hello"; a != e {
		t.Errorf("** Got %q, wanted %q", a, e)
	}
}

func TestInitialFailure(t *testing.T) {
	var log []string
	var mut sync.Mutex
	var attempt int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mut.Lock()
		defer mut.Unlock()
		attempt++
		if attempt == 5 {
			fmt.Fprint(w, "Hello")
		} else {
			log = append(log, fmt.Sprintf("fail %d", attempt))
			http.Error(w, "Oops", http.StatusInternalServerError)
		}
	}))
	defer ts.Close()

	cache := New(Options{
		URL:                  ts.URL,
		DebugName:            "TestInitialFailure",
		RetryInterval:        1 * time.Hour,
		InitialRetryInterval: 1 * time.Millisecond,
		InitialAttemptLimit:  4,
		Parse:                parse,
		Eager:                false,
	})
	defer cache.Shutdown()

	value := cache.Get()

	mut.Lock()
	defer mut.Unlock()
	log = append(log, fmt.Sprint(value))

	if a, e := strings.Join(log, " / "), "fail 1 / fail 2 / fail 3 / fail 4 / <nil>"; a != e {
		t.Errorf("** Got %q, wanted %q", a, e)
	}
}

func TestUpdate(t *testing.T) {
	var mut sync.Mutex
	var attempt int
	attemptc := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mut.Lock()
		defer mut.Unlock()
		attempt++
		fmt.Fprintf(w, "Hello %d", attempt)
		if attempt <= 6 {
			attemptc <- struct{}{}
		}
	}))
	defer ts.Close()

	cache := New(Options{
		URL:           ts.URL,
		DebugName:     "TestUpdate",
		Eager:         true,
		CacheValidity: 100 * time.Millisecond,
		Parse:         parse,
	})
	defer cache.Shutdown()

	var log []string

	for i := 0; i < 5; i++ {
		<-attemptc
		time.Sleep(10 * time.Millisecond) // give it some time to finish all the HTTP stuff
		value := cache.Get().(string)
		log = append(log, value)
	}
	<-attemptc

	if a, e := strings.Join(log, " / "), "Hello 1 / Hello 2 / Hello 3 / Hello 4 / Hello 5"; a != e {
		t.Errorf("** Got %q, wanted %q", a, e)
	}
}

func TestLoadCache(t *testing.T) {
	file, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatal(err)
	}
	fn := file.Name()
	defer os.Remove(fn)

	file.Write([]byte("Hello"))
	file.Close()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("oops")
	}))
	defer ts.Close()

	cache := New(Options{
		URL:       ts.URL,
		CacheFile: fn,
		DebugName: "TestLoadCache",
		Parse:     parse,
	})
	defer cache.Shutdown()

	value := cache.Get().(string)

	if a, e := value, "Hello"; a != e {
		t.Errorf("** Invalid value, got %q, wanted %q", a, e)
	}
}

func TestSaveCache(t *testing.T) {
	file, err := ioutil.TempFile("", "")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	fn := file.Name()
	os.Remove(fn)
	defer os.Remove(fn)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Hello")
	}))
	defer ts.Close()

	cache := New(Options{
		URL:       ts.URL,
		CacheFile: fn,
		DebugName: "TestSaveCache",
		Parse:     parse,
	})
	defer cache.Shutdown()

	value := cache.Get().(string)
	if a, e := value, "Hello"; a != e {
		t.Errorf("** Invalid value, got %q, wanted %q", a, e)
	}

	time.Sleep(10 * time.Millisecond) // give it some time to finish saving the file

	data, err := ioutil.ReadFile(fn)
	if err != nil {
		t.Fatal(err)
	}
	if a, e := string(data), "Hello"; a != e {
		t.Errorf("** Invalid cached value, got %q, wanted %q", a, e)
	}
}

func parse(raw []byte) (interface{}, error) {
	return string(raw), nil
}
