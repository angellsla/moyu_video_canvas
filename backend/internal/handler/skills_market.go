package handler

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"infinite-canvas/backend/internal/kernel"
	"infinite-canvas/backend/internal/skillhub"
	"infinite-canvas/backend/internal/skills"
)

func marketReply(c *gin.Context, value any, err error) {
	if err != nil {
		var remote *skillhub.Error
		if errors.As(err, &remote) {
			status := remote.Status
			if status < 400 || status > 599 {
				status = 502
			}
			if remote.RetryAfter != "" {
				c.Header("Retry-After", remote.RetryAfter)
			}
			c.JSON(status, gin.H{"code": status, "msg": remote.Error(), "reason": remote.Reason})
			return
		}
		failService(c, err)
		return
	}
	ok(c, value)
}
func registerSkillMarketRoutes(r *gin.RouterGroup, owners workspaceOwners, domain *skills.Service) {
	m := r.Group("/skills/market")
	m.Use(func(c *gin.Context) {
		user, e := currentUser(c, owners)
		if e != nil {
			failService(c, e)
			c.Abort()
			return
		}
		c.Set("skill-market-owner", user.ID)
	})
	owner := func(c *gin.Context) string { return c.GetString("skill-market-owner") }
	m.GET("/search", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
		value, err := domain.MarketSearch(c.Request.Context(), c.Query("q"), page, size)
		marketReply(c, value, err)
	})
	m.GET("/catalog", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		size, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
		if page < 1 {
			page = 1
		}
		if size < 1 || size > 100 {
			size = 20
		}
		v, e := domain.MarketCatalog(c.Request.Context(), c.Query("q"), page, size)
		marketReply(c, v, e)
	})
	m.GET("/releases/:skillId/:releaseId", func(c *gin.Context) {
		v, e := domain.MarketRelease(c.Request.Context(), c.Param("skillId"), c.Param("releaseId"))
		marketReply(c, v, e)
	})
	m.GET("/releases/:skillId/:releaseId/file", func(c *gin.Context) {
		offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
		if err != nil {
			marketReply(c, nil, kernel.BadAuthRequest("文件读取范围无效"))
			return
		}
		value, err := domain.MarketFile(c.Request.Context(), c.Param("skillId"), c.Param("releaseId"), c.Query("path"), offset, 32<<10)
		marketReply(c, value, err)
	})
	m.POST("/install", func(c *gin.Context) {
		var a struct {
			SkillID   string `json:"skillId"`
			ReleaseID string `json:"releaseId"`
		}
		if e := c.ShouldBindJSON(&a); e != nil {
			marketReply(c, nil, kernel.BadAuthRequest("请选择技能版本"))
			return
		}
		v, e := domain.InstallMarket(c.Request.Context(), owner(c), a.SkillID, a.ReleaseID)
		marketReply(c, gin.H{"skill": v}, e)
	})
	m.GET("/versions/:skillId", func(c *gin.Context) {
		v, e := domain.MarketVersions(owner(c), c.Param("skillId"))
		marketReply(c, gin.H{"versions": v}, e)
	})
	m.POST("/fork", func(c *gin.Context) {
		var a struct {
			SkillID string `json:"skillId"`
		}
		if e := c.ShouldBindJSON(&a); e != nil {
			marketReply(c, nil, kernel.BadAuthRequest("请选择技能"))
			return
		}
		v, e := domain.ForkMarket(owner(c), a.SkillID)
		marketReply(c, gin.H{"skill": v}, e)
	})
	m.POST("/rollback", func(c *gin.Context) {
		var a struct {
			SkillID   string `json:"skillId"`
			VersionID string `json:"versionId"`
		}
		if e := c.ShouldBindJSON(&a); e != nil {
			marketReply(c, nil, kernel.BadAuthRequest("请选择要恢复的版本"))
			return
		}
		v, e := domain.RollbackMarket(owner(c), a.SkillID, a.VersionID)
		marketReply(c, gin.H{"skill": v}, e)
	})
}
