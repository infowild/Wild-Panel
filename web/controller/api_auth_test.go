package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/locale"
	"github.com/mhsanaei/3x-ui/v2/web/service"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/op/go-logging"
)

// newBearerInboundRouter mounts ONLY the inbound API behind checkAPIAuth.
// Production also mounts /server (which starts a cron) and custom-geo; those are
// irrelevant to the Bearer→requirePerm regression and would panic in a unit test
// without the panel's global scheduler.
func newBearerInboundRouter(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	logger.InitLogger(logging.CRITICAL)
	if err := database.InitDB(filepath.Join(t.TempDir(), "apiauth.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()
	admin := &model.User{
		Username: "api-admin", Password: "x", Enable: true, IsSuperAdmin: true,
	}
	if err := db.Create(admin).Error; err != nil {
		t.Fatalf("create admin: %v", err)
	}
	if err := db.Create(&model.Inbound{
		UserId: admin.Id, Remark: "api-in", Port: 44301, Protocol: model.VMESS,
		Tag: "inbound-44301", Enable: true, Settings: `{"clients":[]}`,
	}).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	var tokens service.ApiTokenService
	view, err := tokens.Create("mirzabot")
	if err != nil {
		t.Fatalf("Create token: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(sessions.Sessions("vpn-ui", cookie.NewStore([]byte("test-secret"))))
	r.Use(func(c *gin.Context) {
		c.Set("base_path", "/")
		c.Set("I18n", func(i18nType locale.I18nType, key string, keyParams ...string) string { return key })
		c.Next()
	})

	api := &APIController{}
	group := r.Group("/panel/api")
	group.Use(api.checkAPIAuth)
	NewInboundController(group.Group("/inbounds"))
	return r, view.Token
}

func TestBearerTokenCanListInbounds(t *testing.T) {
	r, token := newBearerInboundRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/panel/api/inbounds/list", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s; Bearer must clear requirePerm, not 401/404", w.Code, w.Body.String())
	}
	var reply struct {
		Success bool            `json:"success"`
		Msg     string          `json:"msg"`
		Obj     json.RawMessage `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Success {
		t.Fatalf("list failed after a valid Bearer token: %s (this is the mirzabot disconnect)", reply.Msg)
	}
	if len(reply.Obj) == 0 || string(reply.Obj) == "null" {
		t.Fatalf("expected seeded inbounds in the response, got %s", reply.Obj)
	}
}

func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	r, token := newBearerInboundRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/api/inbounds/list", nil)
	req.Header.Set("Authorization", "bearer "+token)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("lowercase bearer scheme rejected: status=%d body=%s", w.Code, w.Body.String())
	}
	var reply struct {
		Success bool `json:"success"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &reply)
	if !reply.Success {
		t.Fatalf("lowercase bearer authenticated but requirePerm still refused: %s", w.Body.String())
	}
}

func TestMissingBearerStillHidesAPI(t *testing.T) {
	r, _ := newBearerInboundRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/panel/api/inbounds/list", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unauthenticated status=%d, want 404 to hide the API surface", w.Code)
	}
}

func TestBearerTokenWithoutActorInjectionIsTheOldBug(t *testing.T) {
	// Documents the failure mode this fix closes: Match succeeds, api_authed is
	// set, but requirePerm still sees no login user and answers 401.
	logger.InitLogger(logging.CRITICAL)
	if err := database.InitDB(filepath.Join(t.TempDir(), "oldbug.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	db := database.GetDB()
	if err := db.Create(&model.User{
		Username: "a", Password: "x", Enable: true, IsSuperAdmin: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	var tokens service.ApiTokenService
	view, err := tokens.Create("bot")
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(sessions.Sessions("vpn-ui", cookie.NewStore([]byte("test-secret"))))
	r.Use(func(c *gin.Context) {
		c.Set("base_path", "/")
		c.Set("I18n", func(i18nType locale.I18nType, key string, keyParams ...string) string { return key })
		c.Next()
	})
	// Intentionally the OLD gate: Match only, no LOGIN_USER_ROW injection.
	r.GET("/panel/api/inbounds/list", func(c *gin.Context) {
		tok := bearerToken(c.GetHeader("Authorization"))
		if tok == "" || !tokens.Match(tok) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Set("api_authed", true)
		c.Next()
	}, requirePerm(model.PermAccessInbounds), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/panel/api/inbounds/list", nil)
	req.Header.Set("Authorization", "Bearer "+view.Token)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("old gate status=%d, want 401 — regression fixture broken", w.Code)
	}
}
