package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"time"

	"github.com/boltdb/bolt"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
)

const (
	maxQueueLen  = 100          // maxQueueLen is the maximum length of the queue
	dbFilename   = "results.db" // dbFilename is the name of the bolt database
	resultBucket = "results"    // resultBucket is the name of the bolt bucket containing results
)

// Globals
var (
	queue chan string        // queue contains the names of all the jobs that need to be processed
	db    *bolt.DB           // db is bolt db for persistent storage
	tmpls *template.Template // tmpls contains all the html templates
)

type result struct {
	Finished bool   // whether the job has finished
	Results  string // partial or full output of the job
}

func init() {
	queue = make(chan string, maxQueueLen)
}

func main() {
	log.Println("Starting...")

	// open database
	var err error
	db, err = bolt.Open("results.db", 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// initialise buckets
	if err := db.Update(boltInitialise()); err != nil {
		log.Fatalf("count not initalise %s: %s", dbFilename, err)
	}

	// initialise html templates
	if tmpls, err = template.ParseGlob("tmpl/*.tmpl"); err != nil {
		log.Fatalf("could not parse html templates: %s", err)
	}

	// Start the runner
	go runner()

	r := mux.NewRouter()
	r.HandleFunc("/", homeHandler)
	r.HandleFunc("/submit", submitHandler)
	r.HandleFunc("/result/{pkg}", resultHandler)
	r.HandleFunc("/api/status/{pkg}", statusHandler)

	// TODO panic handler? per ip (and package?) rate limiter?
	h := handlers.CombinedLoggingHandler(os.Stdout, r)
	h = handlers.CompressHandler(h)
	log.Println("Listening...")
	log.Fatal(http.ListenAndServe(":3003", h))
}

// runner listens for jobs in the queue and runs them
func runner() {
	log.Println("Starting runner")
	for {
		// block waiting for items from the queue
		pkg := <-queue
		log.Println("Running pkg:", pkg)

		// Run
		out, err := exec.Command("vmstat", "1", "3").CombinedOutput()
		if err != nil {
			log.Println("error running godzilla:", err)
		}

		result := result{
			Finished: true,
			Results:  string(out),
		}

		// Put Result
		if err := db.Update(boltPutResult(pkg, result)); err != nil {
			log.Printf("count not put result: %s", err)
		}

		log.Println("finished:", pkg)
	}
}

// homeHandler displays the home page
func homeHandler(w http.ResponseWriter, r *http.Request) {
	page := struct {
		Title string
	}{"Mutation Testing Tool for Go"}

	if err := tmpls.ExecuteTemplate(w, "home.tmpl", page); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing home template: %s", err)
	}
}

// submitHandler handles submissions of packages to be checked and places them
// on the queue, redirecting clients to the results page
func submitHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		panic("err")
	}

	pkg := r.Form.Get("pkg")

	if pkg == "" {
		// TODO errorHandler
		panic("bad request: pkg not set")
	}

	// there's obviously a race here, where checking the length of the queue and
	// adding to the queue are different operations, this isn't a big concern atm
	if len(queue) > maxQueueLen*0.75 {
		// TODO errorHandler
		panic("too many items in the queue")
	}

	// remove old entry and show placeholder for new entry
	boltPutResult(pkg, result{})

	// add to the queue
	queue <- pkg

	// return with a redirect to the result page
	redirect := url.URL{
		Scheme: r.URL.Scheme,
		Host:   r.URL.Host,
		Path:   fmt.Sprintf("/result/%s", pkg),
	}
	http.Redirect(w, r, redirect.String(), 302)
}

// resultHandler shows the result which maybe still running or finished
func resultHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	res, err := getResult(vars["pkg"])
	if err != nil {
		// TOOD errorHandler
		panic("err: " + err.Error())
	}

	page := struct {
		Title  string
		Result *result
	}{vars["pkg"], res}

	// return html
	if err := tmpls.ExecuteTemplate(w, "results.tmpl", page); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing results template: %s", err)
	}
}

// statusHandler is the API endpoint to check on the status of a job
func statusHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	res, err := getResult(vars["pkg"])
	if err != nil {
		// TOOD errorHandler
		panic("err: " + err.Error())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct{ Finished bool }{Finished: res.Finished})
}
