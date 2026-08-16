package controller

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/session"

	"github.com/gin-gonic/gin"
)

// GroupController serves /panel/api/clients/groups/* (Sanaei client-groups API).
type GroupController struct {
	groups      service.ClientGroupService
	xrayService service.XrayService
}

// NewGroupController registers group routes on the clients API group.
func NewGroupController(g *gin.RouterGroup) *GroupController {
	a := &GroupController{}
	a.groups.Inbound = service.InboundService{}
	a.initRouter(g)
	return a
}

func (a *GroupController) initRouter(g *gin.RouterGroup) {
	// Read: anyone who can open Inbounds. Mutations need edit-client (or create for assign).
	g.GET("/groups", requirePerm(model.PermAccessInbounds), a.list)
	g.GET("/groups/:name/emails", requirePerm(model.PermAccessInbounds), a.emails)

	g.POST("/groups/create", requirePerm(model.PermEditClient), a.create)
	g.POST("/groups/rename", requirePerm(model.PermEditClient), a.rename)
	g.POST("/groups/delete", requirePerm(model.PermEditClient), a.delete)
	g.POST("/groups/resetTraffic", requirePerm(model.PermEditClient), a.resetTraffic)
	g.POST("/groups/bulkAdd", requirePerm(model.PermEditClient), a.bulkAdd)
	g.POST("/groups/bulkRemove", requirePerm(model.PermEditClient), a.bulkRemove)
}

func (a *GroupController) actor(c *gin.Context) *model.User {
	return session.GetLoginUser(c)
}

func (a *GroupController) list(c *gin.Context) {
	rows, err := a.groups.ListGroups(a.actor(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.title"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *GroupController) emails(c *gin.Context) {
	emails, err := a.groups.EmailsByGroup(a.actor(c), c.Param("name"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.title"), err)
		return
	}
	jsonObj(c, emails, nil)
}

type groupNameBody struct {
	Name string `json:"name" form:"name"`
}

func (a *GroupController) create(c *gin.Context) {
	var body groupNameBody
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.add"), err)
		return
	}
	if err := a.groups.CreateGroup(body.Name); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.add"), err)
		return
	}
	jsonObj(c, gin.H{"name": strings.TrimSpace(body.Name)}, nil)
}

type groupRenameBody struct {
	OldName string `json:"oldName" form:"oldName"`
	NewName string `json:"newName" form:"newName"`
}

func (a *GroupController) rename(c *gin.Context) {
	var body groupRenameBody
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.rename"), err)
		return
	}
	affected, err := a.groups.RenameGroup(a.actor(c), body.OldName, body.NewName)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.rename"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonObj(c, gin.H{"affected": affected}, nil)
}

func (a *GroupController) delete(c *gin.Context) {
	var body groupNameBody
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.delete"), err)
		return
	}
	affected, err := a.groups.DeleteGroup(a.actor(c), body.Name)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.delete"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonObj(c, gin.H{"affected": affected}, nil)
}

func (a *GroupController) resetTraffic(c *gin.Context) {
	var body groupNameBody
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.resetTraffic"), err)
		return
	}
	if err := a.groups.ResetGroupTraffic(a.actor(c), body.Name); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.resetTraffic"), err)
		return
	}
	jsonObj(c, gin.H{"name": strings.TrimSpace(body.Name)}, nil)
}

type bulkAddToGroupBody struct {
	Emails []string `json:"emails" form:"emails"`
	Group  string   `json:"group" form:"group"`
}

func (a *GroupController) bulkAdd(c *gin.Context) {
	var body bulkAddToGroupBody
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.addClients"), err)
		return
	}
	if strings.TrimSpace(body.Group) == "" {
		jsonMsg(c, I18nWeb(c, "pages.groups.addClients"), common.NewError("group name is required"))
		return
	}
	affected, err := a.groups.AddToGroup(a.actor(c), body.Emails, body.Group)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.addClients"), err)
		return
	}
	jsonObj(c, gin.H{"affected": affected}, nil)
}

type bulkRemoveFromGroupBody struct {
	Emails []string `json:"emails" form:"emails"`
}

func (a *GroupController) bulkRemove(c *gin.Context) {
	var body bulkRemoveFromGroupBody
	if err := c.ShouldBind(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.removeClients"), err)
		return
	}
	affected, err := a.groups.RemoveFromGroup(a.actor(c), body.Emails)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.groups.removeClients"), err)
		return
	}
	a.xrayService.SetToNeedRestart()
	jsonObj(c, gin.H{"affected": affected}, nil)
}
