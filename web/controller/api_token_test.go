package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"

	"github.com/gin-gonic/gin"
	"github.com/op/go-logging"
)

// newApiTokenTestRouter mounts the token CRUD with no session or permission
// middleware in front of it. What these tests pin down is the request BINDING,
// not the guards: the panel's axios interceptor Qs.stringify's every body, so a
// handler that insists on a JSON body can never be driven from the UI.
func newApiTokenTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	// jsonMsg logs every failure it reports, and the package logger is a nil
	// pointer until something initialises it, which would turn an error response
	// into a segfault rather than a test failure.
	logger.InitLogger(logging.CRITICAL)
	if err := database.InitDB(filepath.Join(t.TempDir(), "apitoken.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	NewApiTokenController(r.Group("/setting"))
	return r
}

type apiTokenReply struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     struct {
		Id      int    `json:"id"`
		Name    string `json:"name"`
		Token   string `json:"token"`
		Enabled bool   `json:"enabled"`
	} `json:"obj"`
}

func postApiToken(t *testing.T, r *gin.Engine, path, contentType, body string) apiTokenReply {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s: status = %d, want 200", path, w.Code)
	}
	var reply apiTokenReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("POST %s: decode %q: %v", path, w.Body.String(), err)
	}
	return reply
}

// The panel sends form-urlencoded because axios-init.js rewrites every request
// body through Qs.stringify. Binding the create handler as JSON-only made the
// Generate button fail with a parse error for every operator.
func TestCreateApiTokenAcceptsFormEncodedBody(t *testing.T) {
	r := newApiTokenTestRouter(t)

	reply := postApiToken(t, r, "/setting/apiTokens",
		"application/x-www-form-urlencoded; charset=UTF-8", "name=mirzabot")

	if !reply.Success {
		t.Fatalf("create from a form body failed: %s", reply.Msg)
	}
	if reply.Obj.Name != "mirzabot" {
		t.Errorf("name = %q, want %q", reply.Obj.Name, "mirzabot")
	}
	if reply.Obj.Token == "" {
		t.Error("plaintext token is empty; it is only ever returned at creation")
	}
	if !reply.Obj.Enabled {
		t.Error("a freshly created token should be enabled")
	}
}

// Bots and scripts POST real JSON. Fixing the form case must not cost them.
func TestCreateApiTokenAcceptsJSONBody(t *testing.T) {
	r := newApiTokenTestRouter(t)

	reply := postApiToken(t, r, "/setting/apiTokens",
		"application/json", `{"name":"scripted"}`)

	if !reply.Success {
		t.Fatalf("create from a JSON body failed: %s", reply.Msg)
	}
	if reply.Obj.Name != "scripted" {
		t.Errorf("name = %q, want %q", reply.Obj.Name, "scripted")
	}
	if reply.Obj.Token == "" {
		t.Error("plaintext token is empty")
	}
}

// The enable switch rides the same axios path and had the same binding bug.
func TestSetApiTokenEnabledAcceptsFormEncodedBody(t *testing.T) {
	r := newApiTokenTestRouter(t)

	created := postApiToken(t, r, "/setting/apiTokens",
		"application/x-www-form-urlencoded", "name=togglable")
	if !created.Success {
		t.Fatalf("setup: create failed: %s", created.Msg)
	}

	path := "/setting/apiTokens/enable/" + strconv.Itoa(created.Obj.Id)
	reply := postApiToken(t, r, path,
		"application/x-www-form-urlencoded", "enabled=false")
	if !reply.Success {
		t.Fatalf("disable from a form body failed: %s", reply.Msg)
	}

	var svc service.ApiTokenService
	rows, err := svc.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d tokens, want 1", len(rows))
	}
	if rows[0].Enabled {
		t.Error("token is still enabled; the enabled=false form value was dropped")
	}
}

// An empty name is the one create the service must refuse, and it has to say so
// rather than mint a nameless credential.
func TestCreateApiTokenRejectsEmptyName(t *testing.T) {
	r := newApiTokenTestRouter(t)

	reply := postApiToken(t, r, "/setting/apiTokens",
		"application/x-www-form-urlencoded", "name=")

	if reply.Success {
		t.Fatal("create accepted an empty name")
	}
}
