package controller

import (
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/web/service"

	"github.com/gin-gonic/gin"
)

// nodeForm is the add/edit/test payload. Bound via ShouldBind + form tags because
// the panel axios interceptor Qs.stringify's every body.
type nodeForm struct {
	Id                  int      `json:"id" form:"id"`
	Name                string   `json:"name" form:"name"`
	Remark              string   `json:"remark" form:"remark"`
	Scheme              string   `json:"scheme" form:"scheme"`
	Address             string   `json:"address" form:"address"`
	Port                int      `json:"port" form:"port"`
	BasePath            string   `json:"basePath" form:"basePath"`
	ApiToken            string   `json:"apiToken" form:"apiToken"`
	Enable              bool     `json:"enable" form:"enable"`
	AllowPrivateAddress bool     `json:"allowPrivateAddress" form:"allowPrivateAddress"`
	TlsVerifyMode       string   `json:"tlsVerifyMode" form:"tlsVerifyMode"`
	PinnedCertSha256    string   `json:"pinnedCertSha256" form:"pinnedCertSha256"`
	InboundSyncMode     string   `json:"inboundSyncMode" form:"inboundSyncMode"`
	InboundTags         []string `json:"inboundTags" form:"inboundTags"`
}

func (f *nodeForm) spec() service.NodeSpec {
	return service.NodeSpec{
		Name:                f.Name,
		Remark:              f.Remark,
		Scheme:              f.Scheme,
		Address:             f.Address,
		Port:                f.Port,
		BasePath:            f.BasePath,
		ApiToken:            f.ApiToken,
		Enable:              f.Enable,
		AllowPrivateAddress: f.AllowPrivateAddress,
		TlsVerifyMode:       f.TlsVerifyMode,
		PinnedCertSha256:    f.PinnedCertSha256,
		InboundSyncMode:     f.InboundSyncMode,
		InboundTags:         f.InboundTags,
	}
}

// NodeController serves the Nodes page and CRUD/probe/sync APIs.
type NodeController struct {
	BaseController
	nodeService service.NodeService
}

// NewNodeController registers node routes on the panel group.
func NewNodeController(g *gin.RouterGroup) *NodeController {
	a := &NodeController{}
	a.initRouter(g)
	return a
}

func (a *NodeController) initRouter(g *gin.RouterGroup) {
	// Inbound editors need the id/name list to assign NodeId without holding ManageNodes.
	g.GET("/nodes/options", requirePerm(model.PermAccessInbounds), a.options)

	g = g.Group("/nodes")
	g.Use(requirePerm(model.PermManageNodes))

	g.GET("/list", a.list)
	g.POST("/add", a.add)
	g.POST("/update/:id", a.update)
	g.POST("/del/:id", a.del)
	g.POST("/setEnable/:id", a.setEnable)
	g.POST("/test", a.test)
	g.POST("/probe/:id", a.probe)
	g.POST("/certFingerprint", a.certFingerprint)
	g.POST("/inbounds", a.remoteInbounds)
	g.POST("/sync/:id", a.sync)
}

func (a *NodeController) options(c *gin.Context) {
	rows, err := a.nodeService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.title"), err)
		return
	}
	type opt struct {
		Id     int    `json:"id"`
		Name   string `json:"name"`
		Enable bool   `json:"enable"`
	}
	out := make([]opt, 0, len(rows))
	for _, n := range rows {
		out = append(out, opt{Id: n.Id, Name: n.Name, Enable: n.Enable})
	}
	jsonObj(c, out, nil)
}

func (a *NodeController) list(c *gin.Context) {
	rows, err := a.nodeService.List()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.title"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *NodeController) add(c *gin.Context) {
	form := &nodeForm{Enable: true, Scheme: "https", Port: 2053, BasePath: "/", TlsVerifyMode: "verify", InboundSyncMode: "all"}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.add"), err)
		return
	}
	view, err := a.nodeService.Add(form.spec())
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.add"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *NodeController) update(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.edit"), err)
		return
	}
	form := &nodeForm{}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.edit"), err)
		return
	}
	view, err := a.nodeService.Update(id, form.spec())
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.edit"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *NodeController) del(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "delete"), err)
		return
	}
	if err := a.nodeService.Delete(id); err != nil {
		jsonMsg(c, I18nWeb(c, "delete"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "delete"), nil)
}

func (a *NodeController) setEnable(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.enable"), err)
		return
	}
	var body struct {
		Enable bool `json:"enable" form:"enable"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.enable"), err)
		return
	}
	if err := a.nodeService.SetEnable(id, body.Enable); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.enable"), err)
		return
	}
	jsonMsg(c, "ok", nil)
}

func (a *NodeController) test(c *gin.Context) {
	form := &nodeForm{Scheme: "https", Port: 2053, BasePath: "/", TlsVerifyMode: "verify"}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.test"), err)
		return
	}
	spec := form.spec()
	// Editing an existing node leaves the token field blank on purpose (we never
	// echo the secret back). Fall back to the stored token so "Test connection"
	// works on edit instead of failing with "api token is required to test".
	if strings.TrimSpace(spec.ApiToken) == "" && form.Id > 0 {
		if tok, terr := a.nodeService.StoredToken(form.Id); terr == nil {
			spec.ApiToken = tok
		}
	}
	st, latency, err := a.nodeService.TestConnection(spec)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.test"), err)
		return
	}
	jsonObj(c, gin.H{"status": st, "latencyMs": latency}, nil)
}

func (a *NodeController) probe(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.probe"), err)
		return
	}
	view, err := a.nodeService.Probe(id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.probe"), err)
		return
	}
	jsonObj(c, view, nil)
}

func (a *NodeController) certFingerprint(c *gin.Context) {
	form := &nodeForm{Scheme: "https", Port: 2053, BasePath: "/"}
	if err := c.ShouldBind(form); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.certPin"), err)
		return
	}
	fp, err := a.nodeService.CertFingerprint(form.spec())
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.certPin"), err)
		return
	}
	jsonObj(c, gin.H{"fingerprint": fp}, nil)
}

func (a *NodeController) remoteInbounds(c *gin.Context) {
	form := &nodeForm{Scheme: "https", Port: 2053, BasePath: "/", TlsVerifyMode: "verify"}
	_ = c.ShouldBind(form)
	id, _ := strconv.Atoi(c.PostForm("id"))
	var spec *service.NodeSpec
	if id <= 0 {
		s := form.spec()
		spec = &s
	}
	rows, err := a.nodeService.FetchRemoteInbounds(id, spec)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.fetchInbounds"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *NodeController) sync(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.sync"), err)
		return
	}
	if err := a.nodeService.MarkDirty(id); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.sync"), err)
		return
	}
	if err := a.nodeService.ReconcileNode(id); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.nodes.sync"), err)
		return
	}
	_ = a.nodeService.PullTraffic(id)
	view, _ := a.nodeService.Get(id)
	jsonObj(c, view, nil)
}
