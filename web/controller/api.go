package controller

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/session"
)

// APIController handles the main API routes for the vpn-ui panel, including inbounds and server management.
type APIController struct {
	BaseController
	inboundController *InboundController
	serverController  *ServerController
	Tgbot             service.Tgbot
	apiTokenService   service.ApiTokenService
}

// NewAPIController creates a new APIController instance and initializes its routes.
func NewAPIController(g *gin.RouterGroup, customGeo *service.CustomGeoService) *APIController {
	a := &APIController{}
	a.initRouter(g, customGeo)
	return a
}

// checkAPIAuth accepts either a logged-in panel session or a valid Bearer API token
// (mirzabot / scripts). Unauthenticated callers get 404 to hide the API surface.
func (a *APIController) checkAPIAuth(c *gin.Context) {
	if session.IsLogin(c) {
		c.Set("api_authed", false)
		c.Next()
		return
	}
	auth := c.GetHeader("Authorization")
	if after, ok := strings.CutPrefix(auth, "Bearer "); ok {
		tok := strings.TrimSpace(after)
		if tok != "" && a.apiTokenService.Match(tok) {
			c.Set("api_authed", true)
			c.Next()
			return
		}
	}
	c.AbortWithStatus(http.StatusNotFound)
}

// initRouter sets up the API routes for inbounds, server, and other endpoints.
func (a *APIController) initRouter(g *gin.RouterGroup, customGeo *service.CustomGeoService) {
	api := g.Group("/panel/api")
	api.Use(a.checkAPIAuth)

	inbounds := api.Group("/inbounds")
	a.inboundController = NewInboundController(inbounds)

	server := api.Group("/server")
	a.serverController = NewServerController(server)

	clients := api.Group("/clients")
	NewClientController(clients, a.inboundController)

	customGeoGroup := api.Group("/custom-geo")
	customGeoGroup.Use(requireXrayOrOverviewManage())
	NewCustomGeoController(customGeoGroup, customGeo)

	api.GET("/backuptotgbot", requireOverviewManage(), a.BackuptoTgbot)
}

// BackuptoTgbot sends a backup of the panel data to Telegram bot admins.
func (a *APIController) BackuptoTgbot(c *gin.Context) {
	a.Tgbot.SendBackupToAdmins()
}

// ApiTokenController manages Bearer tokens under /panel/setting (session + PermPanelSettings only).
type ApiTokenController struct {
	service service.ApiTokenService
}

// NewApiTokenController registers token CRUD on the settings group.
func NewApiTokenController(g *gin.RouterGroup) *ApiTokenController {
	a := &ApiTokenController{}
	g.GET("/apiTokens", a.list)
	g.POST("/apiTokens", a.create)
	g.POST("/apiTokens/del/:id", a.delete)
	g.POST("/apiTokens/enable/:id", a.setEnabled)
	return a
}

func (a *ApiTokenController) list(c *gin.Context) {
	rows, err := a.service.List()
	if err != nil {
		jsonMsg(c, "list", err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *ApiTokenController) create(c *gin.Context) {
	// The panel's axios interceptor Qs.stringify's every body, so this arrives as
	// form-urlencoded; external callers may still send JSON. ShouldBind picks the
	// binding from Content-Type, so both tags are required here.
	var body struct {
		Name string `json:"name" form:"name"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "create", err)
		return
	}
	view, err := a.service.Create(body.Name)
	if err != nil {
		jsonMsg(c, "create", err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *ApiTokenController) delete(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "delete", err)
		return
	}
	if err := a.service.Delete(id); err != nil {
		jsonMsg(c, "delete", err)
		return
	}
	jsonMsg(c, "ok", nil)
}

func (a *ApiTokenController) setEnabled(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "enable", err)
		return
	}
	var body struct {
		Enabled bool `json:"enabled" form:"enabled"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, "enable", err)
		return
	}
	if err := a.service.SetEnabled(id, body.Enabled); err != nil {
		jsonMsg(c, "enable", err)
		return
	}
	jsonMsg(c, "ok", nil)
}
