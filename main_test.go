package main_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const ms = int64(time.Millisecond)

func TestOK(t *testing.T) {
	t.Parallel()
	res := test(t, testcase{
		env: []string{"BUILD_MANIFEST=not-a-file"},
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
			{TimeNano: 5 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0", ExitCode: "0"}}},
			{TimeNano: 50 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_1"}}},
			{TimeNano: 55 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_2"}}},
			{TimeNano: 80 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_3"}}},
			{TimeNano: 10_200 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_1", ExitCode: "0"}}},
			{TimeNano: 10_300 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_3", ExitCode: "1"}}},
			{TimeNano: 10_301 * ms, Type: "container", Action: "kill", Actor: dockerActor{Attributes: dockerAttr{Name: "step_2", Signal: "9"}}},
			{TimeNano: 10_302 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_2", ExitCode: "1"}}},
		},
	})
	exp := []commitStatus{
		{Context: "gcb", State: "success", Description: "Done: step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=0?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: step_2, step_1; Done: step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=2?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: step_3, step_2, step_1; Done: step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: step_3 10s, step_2 10s, step_1 10s; Done: step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: step_3 10s, step_2 10s; Done: step_1 10s, step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
		{Context: "gcb", State: "error", Description: "Error: step_3 10s; Cancelled: step_2 10s; Done: step_1 10s, step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
	}
	if diff := cmp.Diff(exp, res.statuses); diff != "" {
		t.Errorf("Expected GitHub updates (-) but got (+):\n%s", diff)
	}
}

func TestOKManifest(t *testing.T) {
	t.Parallel()

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	res := test(t, testcase{
		env: []string{"BUILD_MANIFEST=" + filepath.Join(wd, "testdata/gcbtest.yaml")},
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
			{TimeNano: 5 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0", ExitCode: "0"}}},
			{TimeNano: 50 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_1"}}},
			{TimeNano: 55 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_2"}}},
			{TimeNano: 80 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_3"}}},
			{TimeNano: 10_200 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_1", ExitCode: "0"}}},
			{TimeNano: 10_300 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_3", ExitCode: "1"}}},
			{TimeNano: 10_301 * ms, Type: "container", Action: "kill", Actor: dockerActor{Attributes: dockerAttr{Name: "step_2", Signal: "9"}}},
			{TimeNano: 10_302 * ms, Type: "container", Action: "die", Actor: dockerActor{Attributes: dockerAttr{Name: "step_2", ExitCode: "1"}}},
		},
	})
	exp := []commitStatus{
		{Context: "gcb", State: "pending", Description: "Done: quick", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=0?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: incomplete, slow; Done: quick", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=2?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: failure, incomplete, slow; Done: quick", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: failure 10s, incomplete 10s, slow 10s; Done: quick", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
		{Context: "gcb", State: "pending", Description: "Running: failure 10s, incomplete 10s; Done: slow 10s, quick", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
		{Context: "gcb", State: "error", Description: "Error: failure 10s; Cancelled: incomplete 10s; Done: slow 10s, quick", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=3?project=gcb-project"},
	}
	if diff := cmp.Diff(exp, res.statuses); diff != "" {
		t.Errorf("Expected GitHub updates (-) but got (+):\n%s", diff)
	}
}

func TestContextName(t *testing.T) {
	t.Parallel()
	res := test(t, testcase{
		env: []string{"STATUS_CONTEXT=gcb-test"}, // As opposed to "user:pass".
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
		},
	})
	exp := []commitStatus{
		{Context: "gcb-test", State: "pending", Description: "Running: step_0", TargetURL: "https://console.cloud.google.com/cloud-build/builds/build-123;step=0?project=gcb-project"},
	}
	if diff := cmp.Diff(exp, res.statuses); diff != "" {
		t.Errorf("Expected GitHub updates (-) but got (+):\n%s", diff)
	}
}

func TestGitHubShortToken(t *testing.T) {
	test(t, testcase{
		env: []string{"GITHUB_TOKEN=token"}, // As opposed to "user:pass".
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
		},
	})
}

func TestBadGitHubRepo(t *testing.T) {
	t.Parallel()
	res := test(t, testcase{
		fail: true,
		env:  []string{"GITHUB_REPO=unknown"},
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
		},
	})
	if res.err == nil {
		t.Fatal("Expected error but received none.")
	}
	requireLogsContain(t, res.logs, `404 Not Found`)
}

func TestBadGitHubToken(t *testing.T) {
	t.Parallel()
	res := test(t, testcase{
		fail: true,
		env:  []string{"GITHUB_TOKEN=bad-token"},
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
		},
	})
	if res.err == nil {
		t.Fatal("Expected error but received none.")
	}
	requireLogsContain(t, res.logs, `Expected token "token" but got "bad-token".`)
}

func TestBadDockerHost(t *testing.T) {
	t.Parallel()
	res := test(t, testcase{
		fail: true,
		env:  []string{"DOCKER_HOST=unix:///dev/null"},
		docker: []dockerEvent{
			{TimeNano: 1 * ms, Type: "container", Action: "start", Actor: dockerActor{Attributes: dockerAttr{Name: "step_0"}}},
		},
	})
	if res.err == nil {
		t.Fatal("Expected error but received none.")
	}
	requireLogsContain(t, res.logs, `dial unix /dev/null: connect: connection refused`)
}

type testcase struct {
	fail   bool
	env    []string
	docker []dockerEvent
}

type testres struct {
	err      error
	statuses []commitStatus
	logs     bytes.Buffer
}

func test(t *testing.T, tc testcase) (tr testres) {
	// Catch failures and abort the test.
	defer func() {
		t.Cleanup(func() {
			if t.Failed() {
				t.Logf("Logs:\n%s", tr.logs.Bytes())
			}
		})
		if !tc.fail && tr.err != nil {
			t.Fatalf("Error running gcb2gh: %s", tr.err)
		}
	}()

	// Create a fake GitHub API that logs updates.
	var updLock sync.Mutex
	var updates []commitStatus
	gmux := http.NewServeMux()
	gmux.HandleFunc("/repos/unravelin/gcb2gh-test/statuses/abc123", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, fmt.Sprintf("Expected a POST request but got %s.", r.Method), http.StatusMethodNotAllowed)
			return
		}

		// Validate the token.
		const expTok = "token"
		if _, tok, ok := r.BasicAuth(); !ok || tok != expTok {
			http.Error(w, fmt.Sprintf("Expected token %q but got %q.", expTok, tok), http.StatusUnauthorized)
			return
		}

		// Parse the update.
		var upd commitStatus
		err := json.NewDecoder(r.Body).Decode(&upd)
		if err != nil {
			http.Error(w, fmt.Sprintf("Error decoding request: %s", err), http.StatusBadRequest)
			return
		}
		if len(upd.Description) > 140 {
			http.Error(w, "Description too long.", http.StatusUnprocessableEntity)
			return
		}

		// Respond with success.
		w.WriteHeader(http.StatusCreated)

		// Store the update.
		updLock.Lock()
		updates = append(updates, upd)
		updLock.Unlock()
	})
	gh := httptest.NewServer(gmux)
	defer gh.Close()

	// Fake a Docker daemon to produce our test set of events.
	dmux := http.NewServeMux()
	dmux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		// Validate we've got the required filters.
		q := r.URL.Query()
		if exp, act := "10", q.Get("since"); exp != act {
			t.Errorf("Expected docker query param since=%q but got %q.", exp, act)
		}
		if exp, act := "container", q.Get("type"); exp != act {
			t.Errorf("Expected docker query param since=%q but got %q.", exp, act)
		}

		// Send back the events.
		w.Header().Set("Content-Type", "application/json")
		s := json.NewEncoder(w)
		now := time.Now().UnixNano()
		ts := now
		for _, e := range tc.docker {
			// Sleep between events.
			e.TimeNano += now
			time.Sleep(time.Duration(e.TimeNano - ts))
			ts = e.TimeNano

			// Send.
			err := s.Encode(e)
			if err != nil {
				t.Errorf("Error sending back docker event: %s", err)
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	dsock := filepath.Join(t.TempDir(), "docker.sock")
	serveSocket(t, dsock, dmux)

	// Run gcb2gh.
	run := exec.Command("go", "run", ".")
	run.Stderr = &tr.logs
	run.Stdout = os.Stdout
	run.Env = append(
		os.Environ(),
		"PROJECT_ID=gcb-project",
		"BUILD_ID=build-123",
		"COMMIT_SHA=abc123",
		"DOCKER_HOST=unix://"+dsock,
		"GITHUB_API="+gh.URL,
		"GITHUB_TOKEN=user:token",
		"GITHUB_USER=unravelin",
		"GITHUB_REPO=gcb2gh-test",
	)
	run.Env = append(run.Env, tc.env...)
	tr.err = run.Run()
	tr.statuses = updates
	return tr
}

func requireLogsContain(t *testing.T, logs bytes.Buffer, find string) {
	s := logs.String()
	if !strings.Contains(s, find) {
		t.Fatalf("Expected logs to contain %q but couldn't find it.", find)
		// t.Fatalf("Logs:\n%s", s)
	}
}

func serveSocket(t *testing.T, sockfile string, h http.Handler) {
	sock, err := net.Listen("unix", sockfile)
	if err != nil {
		t.Fatalf("Failed to listen on socket: %s", err)
	}

	s := &http.Server{Handler: h}
	t.Cleanup(func() {
		s.Close()
	})
	go func() {
		err := s.Serve(sock)
		if err != nil && err != http.ErrServerClosed {
			t.Errorf("Error serving socket: %s", err)
		}
	}()
}

type commitStatus struct {
	State       string `json:"state,omitempty"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context,omitempty"`
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
