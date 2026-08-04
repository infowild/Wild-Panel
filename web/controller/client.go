package controller

import (
	"net"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

// ClientController serves /panel/api/clients/* for mirzabot (Bearer) and session users.
type ClientController struct {
	clientService     service.ApiClientService
	xrayService       service.XrayService
	inboundController *InboundController
}

// NewClientController registers client routes on g (already under /panel/api + auth).
func NewClientController(g *gin.RouterGroup, inboundCtrl *InboundController) *ClientController {
	a := &ClientController{
		inboundController: inboundCtrl,
		xrayService:       service.XrayService{},
	}
	a.clientService.Inbound = service.InboundService{}
	a.clientService.Mtproto = service.MtprotoService{}
	a.clientService.Setting = service.SettingService{}
	a.clientService.SubLinksBuilder = a.buildSubLinks
	a.initRouter(g)
	return a
}

func (a *ClientController) initRouter(g *gin.RouterGroup) {
	g.GET("/get/:email", a.get)
	g.GET("/traffic/:email", a.getTraffic)
	g.GET("/search", a.search)
	g.GET("/subLinks/:subId", a.getSubLinks)

	g.POST("/add", a.create)
	g.POST("/update/:email", a.update)
	g.POST("/del/:email", a.delete)
	g.POST("/resetTraffic/:email", a.resetTraffic)
}

func (a *ClientController) get(c *gin.Context) {
	view, err := a.clientService.GetFlat(c.Param("email"))
	if err != nil {
		jsonMsg(c, "obtain", err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *ClientController) getTraffic(c *gin.Context) {
	traffic, err := a.clientService.GetTraffic(c.Param("email"))
	if err != nil {
		jsonMsg(c, "traffic", err)
		return
	}
	jsonObj(c, traffic, nil)
}

func (a *ClientController) search(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "25"))
	resp, err := a.clientService.Search(c.Query("search"), page, size)
	if err != nil {
		jsonMsg(c, "obtain", err)
		return
	}
	jsonObj(c, resp, nil)
}

func (a *ClientController) create(c *gin.Context) {
	var payload service.ClientCreatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		jsonMsg(c, "create", err)
		return
	}
	protocol, needRestart, err := a.clientService.Create(&payload)
	if err != nil {
		jsonMsg(c, "create", err)
		return
	}
	a.applySideEffects(protocol, needRestart)
	jsonMsg(c, "ok", nil)
}

func (a *ClientController) update(c *gin.Context) {
	var patch map[string]any
	if err := c.ShouldBindJSON(&patch); err != nil {
		jsonMsg(c, "update", err)
		return
	}
	protocol, needRestart, err := a.clientService.Update(c.Param("email"), patch)
	if err != nil {
		jsonMsg(c, "update", err)
		return
	}
	a.applySideEffects(protocol, needRestart)
	jsonMsg(c, "ok", nil)
}

func (a *ClientController) delete(c *gin.Context) {
	keep := c.Query("keepTraffic") == "1"
	protocol, needRestart, err := a.clientService.Delete(c.Param("email"), keep)
	if err != nil {
		jsonMsg(c, "delete", err)
		return
	}
	a.applySideEffects(protocol, needRestart)
	jsonMsg(c, "ok", nil)
}

func (a *ClientController) resetTraffic(c *gin.Context) {
	if err := a.clientService.ResetTraffic(c.Param("email")); err != nil {
		jsonMsg(c, "reset", err)
		return
	}
	jsonMsg(c, "ok", nil)
}

func (a *ClientController) getSubLinks(c *gin.Context) {
	links, err := a.clientService.GetSubLinks(resolveRequestHost(c), c.Param("subId"))
	if err != nil {
		jsonMsg(c, "obtain", err)
		return
	}
	jsonObj(c, links, nil)
}

func (a *ClientController) buildSubLinks(host, subId string) ([]string, error) {
	// Do not import package sub here: sub → web → controller would create an
	// import cycle. Subscription page URLs from settings are what sales bots
	// (mirzabot) need; per-protocol share links stay on the subscription server.
	_ = host
	out := make([]string, 0, 3)
	subURI, _ := a.clientService.Setting.GetSubURI()
	subJsonURI, _ := a.clientService.Setting.GetSubJsonURI()
	subClashURI, _ := a.clientService.Setting.GetSubClashURI()
	appendURI := func(uri string) {
		if uri == "" {
			return
		}
		if !strings.HasSuffix(uri, "/") {
			uri += "/"
		}
		out = append(out, uri+subId)
	}
	appendURI(subURI)
	appendURI(subJsonURI)
	appendURI(subClashURI)
	return out, nil
}

func (a *ClientController) applySideEffects(protocol model.Protocol, needRestart bool) {
	if a.inboundController == nil {
		if needRestart {
			a.xrayService.SetToNeedRestart()
		}
		return
	}
	switch protocol {
	case model.L2TP:
		a.inboundController.onL2tpClientChanged()
	case model.PPTP:
		a.inboundController.onPptpClientChanged()
	case model.OPENVPN:
		a.inboundController.onOpenVpnClientChanged()
	case model.OPENCONNECT:
		a.inboundController.onOcservClientChanged()
	case model.SSTP:
		a.inboundController.onSstpClientChanged()
	case model.IKEV2:
		a.inboundController.onIkev2ClientChanged()
	case model.WGC:
		a.inboundController.onWgcClientChanged()
	case model.AWG:
		a.inboundController.onAwgClientChanged()
	case model.GRE:
		a.inboundController.onGreClientChanged()
	case model.MTPROTO:
		a.inboundController.onMtprotoClientChanged()
	case model.SSH:
		a.inboundController.onSshClientChanged()
	default:
		if needRestart {
			a.xrayService.SetToNeedRestart()
		}
	}
}

func resolveRequestHost(c *gin.Context) string {
	h := c.Request.Host
	if h == "" {
		return "localhost"
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
