package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/logger"

	"github.com/gin-gonic/gin"
	"github.com/op/go-logging"
)

func newNodeTestRouter(t *testing.T) *gin.Engine {
	t.Helper()
	logger.InitLogger(logging.CRITICAL)
	if err := database.InitDB(filepath.Join(t.TempDir(), "node.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Mount without permission middleware — these tests pin form binding only.
	n := &NodeController{}
	g := r.Group("/panel")
	g.GET("/nodes/list", n.list)
	g.POST("/nodes/add", n.add)
	return r
}

func TestCreateNodeAcceptsFormEncodedBody(t *testing.T) {
	r := newNodeTestRouter(t)
	body := strings.Join([]string{
		"name=edge1",
		"scheme=https",
		"address=1.2.3.4",
		"port=2053",
		"basePath=/",
		"apiToken=tokensecret",
		"enable=true",
		"tlsVerifyMode=skip",
		"inboundSyncMode=all",
	}, "&")
	req := httptest.NewRequest(http.MethodPost, "/panel/nodes/add", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var reply struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		Obj     struct {
			Id          int    `json:"id"`
			Name        string `json:"name"`
			HasApiToken bool   `json:"hasApiToken"`
			ApiToken    string `json:"apiToken"`
		} `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Success {
		t.Fatalf("create failed: %s", reply.Msg)
	}
	if reply.Obj.Name != "edge1" {
		t.Errorf("name=%q", reply.Obj.Name)
	}
	if !reply.Obj.HasApiToken {
		t.Error("hasApiToken should be true")
	}
	if reply.Obj.ApiToken != "" {
		t.Error("plaintext token must not be returned on node views")
	}
}
