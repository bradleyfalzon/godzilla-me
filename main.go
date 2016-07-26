package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/boltdb/bolt"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/shurcooL/github_flavored_markdown"
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
	Package  string // Package is the name of the package being tested
	Finished bool   // whether the job has finished
	Results  string // partial or full output of the job
}

func init() {
	queue = make(chan string, maxQueueLen)
}

func main() {
	log.Println("Starting...")

	// open database
	log.Println("Opening database...")
	var err error
	db, err = bolt.Open("results.db", 0600, &bolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// initialise buckets
	log.Println("Initialising buckets...")
	if err := db.Update(boltInitialise()); err != nil {
		log.Fatalf("count not initalise %s: %s", dbFilename, err)
	}

	// initialise html templates
	log.Println("Parsing templates...")
	if tmpls, err = template.ParseGlob("tmpl/*.tmpl"); err != nil {
		log.Fatalf("could not parse html templates: %s", err)
	}

	// fetch readme.md
	log.Println("Fetching README.md...")
	if err := generateReadme(); err != nil {
		log.Fatalf("could not fetch readme: %s", err)
	}

	// Start the runner
	go runner()

	r := mux.NewRouter()
	r.NotFoundHandler = http.HandlerFunc(notFoundHandler)
	r.HandleFunc("/", homeHandler)
	r.HandleFunc("/submit", submitHandler)
	r.HandleFunc("/result/{pkg:.+}", resultHandler)
	r.HandleFunc("/api/status/{pkg:.+}", statusHandler)
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("./static/"))))

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
		out, err := exec.Command("vmstat", "1", "5").CombinedOutput()
		if err != nil {
			log.Println("error running godzilla:", err)
		}

		result := result{
			Package:  pkg,
			Finished: true,
			Results:  string(out),
		}

		// Put Result
		if err := putResult(pkg, result); err != nil {
			log.Printf("count not put result: %s", err)
		}

		log.Println("finished:", pkg)
	}
}

// generateReadme gets the README.md file, converts to HTML and writes out to a template
func generateReadme() error {
	log.Println("GOPATH:", os.Getenv("GOPATH"))
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	log.Println("CWD:", wd)

	md, err := ioutil.ReadFile(filepath.Join(os.Getenv("GOPATH"), "src/github.com/hydroflame/godzilla/README.md"))
	if err != nil {
		return err
	}

	html := []byte(`{{define "generated-readme"}}`)
	html = append(html, github_flavored_markdown.Markdown(md)...)
	html = append(html, []byte(`{{- end}}`)...)

	return ioutil.WriteFile("tmpl/generated-readme.tmpl", html, 0644)
}

// notFoundHandler displays a 404 not found error
func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	errorHandler(w, r, http.StatusNotFound, "")
}

// errorHandler handles an error message, with an optional description
func errorHandler(w http.ResponseWriter, r *http.Request, code int, desc string) {
	page := struct {
		Title  string
		Code   string // eg 400
		Status string // eg Bad Request
		Desc   string // eg Missing key foo
	}{fmt.Sprintf("%d - %s", code, http.StatusText(code)), strconv.Itoa(code), http.StatusText(code), desc}

	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(code)
	if err := tmpls.ExecuteTemplate(w, "error.tmpl", page); err != nil {
		fmt.Fprintf(os.Stderr, "error parsing home template: %s", err)
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
		errorHandler(w, r, http.StatusInternalServerError, "")
		return
	}

	pkg := r.Form.Get("pkg")
	if pkg == "" {
		errorHandler(w, r, http.StatusBadRequest, "pkg not set")
		return
	}

	// there's obviously a race here, where checking the length of the queue and
	// adding to the queue are different operations, this isn't a big concern atm
	if len(queue) > maxQueueLen*0.75 {
		errorHandler(w, r, http.StatusInternalServerError, "server too busy")
		return
	}

	// remove old entry and show placeholder for new entry
	if err := putResult(pkg, result{Package: pkg}); err != nil {
		errorHandler(w, r, http.StatusInternalServerError, "could not store placeholder result")
		return
	}

	// add to the queue
	queue <- pkg

	// return with a redirect to the result page
	redirect := url.URL{
		Scheme: r.URL.Scheme,
		Host:   r.URL.Host,
		Path:   fmt.Sprintf("/result/%s", pkg),
	}
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

// resultHandler shows the result which maybe still running or finished
func resultHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	res, err := getResult(vars["pkg"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error fetching result:", err)
		errorHandler(w, r, http.StatusInternalServerError, "error fetching result")
		return
	}

	page := struct {
		Title  string
		Result *result
	}{vars["pkg"], res}

	// return html
	if err := tmpls.ExecuteTemplate(w, "results.tmpl", page); err != nil {
		fmt.Fprintln(os.Stderr, "error parsing results template:", err)
	}
}

// statusHandler is the API endpoint to check on the status of a job
func statusHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	res, err := getResult(vars["pkg"])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error fetching result:", err)
		errorHandler(w, r, http.StatusInternalServerError, "error fetching result")
		return
	}

	status := struct {
		Finished bool
		HTMLURL  string
	}{
		Finished: res.Finished,
		HTMLURL:  "/result/" + vars["pkg"],
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}
