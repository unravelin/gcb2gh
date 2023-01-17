// Program gcb2gh watches the Docker events in Google Cloud Build for the
// starting and stopping of containers named "step_[0-9]". With each, we update
// the GitHub Commit Status describing which steps are running, errored or done.
// The status check links directly to the build or first erroring build step.
//
// For example usage in GCB see https://github.com/unravelin/gcb2gh.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	yaml "gopkg.in/yaml.v3"
)

func main() {
	// Give ourselves the best chance to finish updating GitHub.
	signal.Ignore(syscall.SIGTERM)
	// Show the microseconds in the time.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)

	err := run(context.Background())
	if err != nil {
		log.Println("Error:", err)
		os.Exit(exitCode(err))
	}
}

func run(ctx context.Context) (err error) {
	// Read the envvars.
	build := buildContext{
		Docker: os.Getenv("DOCKER_HOST"),

		Project:  os.Getenv("PROJECT_ID"),
		ID:       os.Getenv("BUILD_ID"),
		Manifest: os.Getenv("BUILD_MANIFEST"),

		GitHub:  os.Getenv("GITHUB_API"),
		Token:   os.Getenv("GITHUB_TOKEN"),
		User:    os.Getenv("GITHUB_USER"),
		Repo:    os.Getenv("GITHUB_REPO"),
		SHA:     os.Getenv("COMMIT_SHA"),
		Context: os.Getenv("STATUS_CONTEXT"),
	}
	if build.Token == "" {
		return errors.New(`envvar GITHUB_TOKEN ("user:token", ":token" or "token") is required`)
	}
	if build.User == "" {
		return errors.New(`envvar GITHUB_USER (the "user" in "github.com/user/repo") is required`)
	}
	if build.Repo == "" {
		return errors.New(`envvar GITHUB_REPO (the "repo" in "github.com/user/repo") is required`)
	}
	if build.SHA == "" {
		return errors.New(`envvar COMMIT_SHA is required`)
	}
	if build.Docker == "" {
		build.Docker = "unix:///var/run/docker.sock"
	}
	if build.GitHub == "" {
		build.GitHub = "https://api.github.com"
	}
	if build.Context == "" {
		build.Context = "gcb"
	}

	// Parse the build manifest for pretty step names.
	ids := readManifestIDs(build.Manifest)

	// Get a stream of GCB step events.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	dockerErrs := make(chan error, 1)
	gcbUpdates := make(chan gcbStep, 10)
	go func() {
		defer close(dockerErrs)
		dockerErrs <- dockerUpdates(ctx, build.Docker, gcbUpdates, ids)
	}()

	// Send updates to GitHub after each change, or every 10 seconds.
	numSteps := len(ids)
	steps := make(map[int]gcbStep, numSteps+10)
	kick := time.NewTimer(time.Hour)
	for {
		select {
		case s := <-gcbUpdates:
			if s.status == gcbStatusUndef {
				// No more GCB updates.
				gcbUpdates = nil
				break
			}
			if steps[s.num].status == gcbStatusCancelled {
				// Each step dies with a nonzero exit code after being
				// cancelled, appearing as an error. Leave it as cancelled.
				continue
			}

			// Update this step.
			if s.startNano == 0 {
				s.startNano = steps[s.num].startNano
			}
			steps[s.num] = s
			log.Printf("GCB step: %#v.", s)

			// If this build step was killed, mark anything still running as
			// cancelled. This would happen anyway - we'd see cancellations
			// coming from Docker - but we want the first failure to be our last
			// update to GitHub so that it doesn't send many slack messages.
			if s.status == gcbStatusError {
				for n, step := range steps {
					if step.status != gcbStatusRunning {
						continue
					}
					step.status = gcbStatusCancelled
					step.endNano = s.endNano
					steps[n] = step
				}
			}

			// Schedule an update to GitHub, if nothing else happens first.
			// Debounces the initial requests.
			if !kick.Stop() {
				<-kick.C
			}
			kick.Reset(20 * time.Millisecond)
			continue

		case err = <-dockerErrs:
			if err != nil {
				return err
			}
			dockerErrs = nil
			close(gcbUpdates)
			continue

		case <-kick.C:
			kick.Reset(10 * time.Second)
		}

		// Update GitHub.
		gh := gcb2gh(build, steps, numSteps)
		log.Printf("GH update: %#v.", gh)
		err := updateGitHub(build, gh)
		if err != nil {
			log.Print("Error: ", err)
		} else {
			log.Print("GH updated.")
		}

		// Error.
		if gh.State == ghCommitStateError {
			return err
		}
		// Cancellation.
		if err := ctx.Err(); err != nil {
			return err
		}
		// Completion.
		if gcbUpdates == nil {
			return err
		}
	}
}

// readManifestIDs parses the google cloud build manifest at mani and returns
// the explicit id indexed against the step number. Returns an empty but non-nil
// map if any error occurs reading the file.
func readManifestIDs(mani string) map[int]string {
	ids := make(map[int]string, 20)
	if mani == "" {
		return ids
	}

	// Open the build manifest.
	f, err := os.Open(mani)
	if err != nil {
		log.Printf("Opening build manifest: %s", err)
		return ids
	}
	defer f.Close()

	// Parse the manifest step IDs.
	type step struct {
		ID   string `yaml:"id"`
		Name string `yaml:"name"`
	}
	var c struct {
		Steps []step `yaml:"steps"`
	}
	d := yaml.NewDecoder(f)
	if err := d.Decode(&c); err != nil {
		log.Printf("Reading build manifest %q: %s", mani, err)
		return ids
	}

	// Build the ID map.
	for n, s := range c.Steps {
		ids[n] = s.ID
	}
	return ids
}

// dockerUpdates connects to Docker daemon at dockerHost monitors container
// events, sending them back on the updates channel.
func dockerUpdates(ctx context.Context, dockerHost string, updates chan<- gcbStep, ids map[int]string) error {
	// Swap out the HTTP client if we're using a unix socket.
	docker := http.DefaultClient
	if strings.HasPrefix(dockerHost, "unix:///") {
		path := strings.TrimPrefix(dockerHost, "unix://")
		dockerHost = "http://docker"
		docker = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", path)
				},
			},
		}
	}

	// Start the docker events stream.
	res, err := docker.Get(dockerHost + "/events?type=container&since=10")
	if err != nil {
		log.Fatalf("Error requesting docker events: %s", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		h, _ := httputil.DumpResponse(res, true)
		return exit(3, fmt.Errorf("%s fetching docker events:\n%s", res.Status, h))
	}

	// Loop over the events coming back from docker.
	r := json.NewDecoder(res.Body)
	for {
		// Read the next event.
		var e dockerEvent
		err := r.Decode(&e)
		switch err {
		case nil:
			// Continue.
		case io.EOF:
			return nil
		default:
			return fmt.Errorf("decoding event: %w", err)
		}

		// Filter for step container events.
		if !strings.HasPrefix(e.Actor.Attributes.Name, "step_") {
			continue
		}

		// Update the build process steps.
		s := gcbStep{
			num: atoi(strings.TrimPrefix(e.Actor.Attributes.Name, "step_")),
		}
		switch e.Action {
		case "start":
			s.status = gcbStatusRunning
			s.startNano = e.TimeNano
		case "kill":
			s.status = gcbStatusCancelled
			s.endNano = e.TimeNano
		case "die":
			s.endNano = e.TimeNano
			s.exit = atoi(e.Actor.Attributes.ExitCode)
			if s.exit == 0 {
				s.status = gcbStatusDone
			} else {
				s.status = gcbStatusError
			}
		default:
			// Skip this event.
			continue
		}
		s.id = ids[s.num]
		if s.id == "" {
			s.id = e.Actor.Attributes.Name
		}
		updates <- s

		// Cancelled.
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func gcb2gh(build buildContext, steps map[int]gcbStep, numSteps int) ghStatusUpdate {
	// Build a description of the steps.
	st := make([]gcbStep, 0, len(steps))
	for _, s := range steps {
		st = append(st, s)
	}
	sort.Slice(st, func(i, j int) bool {
		if st[i].status != st[j].status {
			return st[i].status < st[j].status
		}
		if st[i].endNano != st[j].endNano {
			// Show most recently ended first.
			return st[i].endNano >= st[j].endNano
		}
		if st[i].startNano != st[j].startNano {
			// Show first started first.
			return st[i].startNano >= st[j].startNano
		}
		return st[i].num < st[j].num
	})
	var stPrev gcbStatus
	var sb strings.Builder
	nowNano := time.Now().UnixNano()
	for _, s := range st {
		if s.status != stPrev {
			if stPrev != 0 {
				sb.WriteString("; ")
			}
			sb.WriteString(s.status.String())
			sb.WriteString(": ")
		} else {
			sb.WriteString(", ")
		}
		sb.WriteString(s.id)
		e := s.endNano
		if e == 0 {
			e = nowNano
		}
		d := time.Duration(e - s.startNano)
		if d > 10*time.Second {
			sb.WriteString(" ")
			sb.WriteString(fmtDuration(d))
		}
		stPrev = s.status
	}

	// Trim the status.
	status := sb.String()
	if len(status) >= 140 {
		status = status[:140]
	}

	// Convert build status to github status.
	s0 := st[0]
	var commitState ghCommitState
	switch s0.status {
	case gcbStatusError:
		commitState = ghCommitStateError
	case gcbStatusDone:
		if numSteps == 0 || len(st) == numSteps {
			// If the most recent step is done, we can perhaps assume that we're
			// finished. If we don't have a build manifest there may be another
			// step yet to start. We'll switch back to "pending" when the next
			// step starts, but the debouncing in run() should make it very
			// unlikely that we send a success on anything other than the last
			// step.
			commitState = ghCommitStateSuccess
			break
		}
		fallthrough
	case gcbStatusRunning:
		commitState = ghCommitStatePending
	}

	// Link to the build and directly to the first step in our sorted list,
	// which will always be an error if a step failed.
	target := "https://console.cloud.google.com/cloud-build/builds/" + url.PathEscape(build.ID)
	target += ";step=" + strconv.Itoa(s0.num)
	target += "?project=" + url.QueryEscape(build.Project)

	// Update the commit status in GitHub.
	return ghStatusUpdate{
		Context:     build.Context,
		Description: status,
		State:       commitState,
		TargetURL:   target,
	}
}

type ghCommitState string

const (
	ghCommitStateError   = "error"
	ghCommitStateSuccess = "success"
	ghCommitStatePending = "pending"
)

func updateGitHub(build buildContext, status ghStatusUpdate) error {
	// Build the request.
	req, err := newGHStatusUpdateReq(build, status)
	if err != nil {
		return fmt.Errorf("building github status request: %w", err)
	}

	// Send to GitHub.
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("updating github status: %w", err)
	}
	defer res.Body.Close()

	// Validate everything went OK.
	if res.StatusCode != http.StatusCreated {
		b, _ := httputil.DumpResponse(res, true)
		return fmt.Errorf("%s response from github:\n%s", res.Status, b)
	}
	if _, err := io.Copy(io.Discard, res.Body); err != nil {
		return fmt.Errorf("discarding github response body: %w", err)
	}
	return nil
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// exitCode returns the process exit code for an error. If err can be unwrapped
// to an interface { Code() int } then Code() is returned, otherwise 0 if err
// is nil, or 1.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var x interface{ Code() int }
	if errors.As(err, &x) {
		return x.Code()
	}
	return 1
}

// exit returns err wrapped with an exit code.
func exit(code int, err error) error {
	return &exitError{code, err}
}

type exitError struct {
	code int
	error
}

func (err *exitError) Code() int {
	return err.code
}

func (err *exitError) Unwrap() error {
	return err.error
}

type buildContext struct {
	Docker string

	Project  string
	ID       string
	Manifest string

	GitHub  string
	Token   string
	User    string
	Repo    string
	SHA     string
	Context string
}

type gcbStep struct {
	status    gcbStatus
	num       int
	id        string
	exit      int
	startNano int64
	endNano   int64
}

type gcbStatus int

const (
	gcbStatusUndef gcbStatus = iota
	gcbStatusError
	gcbStatusCancelled
	gcbStatusRunning
	gcbStatusDone
)

func (s gcbStatus) String() string {
	return [...]string{"Unknown", "Error", "Cancelled", "Running", "Done"}[s]
}

type dockerEvent struct {
	Type     string      `json:"Type,omitempty"`
	Action   string      `json:"Action,omitempty"`
	Actor    dockerActor `json:"Actor,omitempty"`
	Scope    string      `json:"scope,omitempty"`
	Time     int64       `json:"time,omitempty"`
	TimeNano int64       `json:"timeNano,omitempty"`
	Status   string      `json:"status,omitempty"`
	ID       string      `json:"id,omitempty"`
	From     string      `json:"from,omitempty"`
}

type dockerActor struct {
	ID         string     `json:"ID,omitempty"`
	Attributes dockerAttr `json:"Attributes"`
}

type dockerAttr struct {
	Driver      string `json:"driver,omitempty"`
	Image       string `json:"image,omitempty"`
	Name        string `json:"name,omitempty"`
	Container   string `json:"container,omitempty"`
	Type        string `json:"type,omitempty"`
	Destination string `json:"destination,omitempty"`
	Propagation string `json:"propagation,omitempty"`
	ReadWrite   string `json:"read/write,omitempty"`
	ExitCode    string `json:"exitCode,omitempty"`
	Signal      string `json:"signal,omitempty"`
}

type ghStatusUpdate struct {
	State       ghCommitState `json:"state,omitempty"`
	TargetURL   string        `json:"target_url,omitempty"`
	Description string        `json:"description,omitempty"`
	Context     string        `json:"context,omitempty"`
}

// newGHStatusUpdateReq returns an authenticated *http.Request to set the
// status of the commit c.SHA to s.State.
func newGHStatusUpdateReq(c buildContext, s ghStatusUpdate) (*http.Request, error) {
	// Marshal the body.
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(s); err != nil {
		return nil, err
	}

	// Construct the request.
	uri := c.GitHub + "/repos/" + url.PathEscape(c.User) + "/" + url.PathEscape(c.Repo) + "/statuses/" + url.PathEscape(c.SHA)
	r, err := http.NewRequest(http.MethodPost, uri, &body)
	if err != nil {
		return r, err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/vnd.github.v3+json")

	// Add authentication.
	r.SetBasicAuth(splitUserPass(c.Token))
	return r, nil
}

// splitUserPass usernames and passwords of the form "user:pass" or just "pass"
// and returns them as "user", "pass".
func splitUserPass(userPass string) (user, pass string) {
	col := strings.Index(userPass, ":")
	if col == -1 {
		return "", userPass
	}
	return userPass[:col], userPass[col+1:]
}

// fmtDuration returns the duration d formatted to show only the two most
// significant units of time from year, days, hours, minutes, seconds.
func fmtDuration(d time.Duration) string {
	const (
		Day  = 24 * time.Hour
		Year = 365 * Day
	)
	if d > Year {
		return fmt.Sprintf("%dy%dd", d/Year, (d%Year)/Day)
	}
	if d > Day {
		return fmt.Sprintf("%dd%dh", d/Day, (d%Day)/time.Hour)
	}
	if d > time.Hour {
		return fmt.Sprintf("%dh%dm", d/time.Hour, (d%time.Hour)/time.Minute)
	}
	if d > time.Minute {
		return fmt.Sprintf("%dm%ds", d/time.Minute, (d%time.Minute)/time.Second)
	}
	return fmt.Sprintf("%ds", (d%time.Minute)/time.Second)
}
