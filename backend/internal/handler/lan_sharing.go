package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"infinite-canvas/backend/internal/app"
	"infinite-canvas/backend/internal/lansharing"
	"infinite-canvas/backend/internal/model"

	"github.com/gin-gonic/gin"
)

const lanSharingUpdateBodyLimit = 6 << 20

type lanSharingAPI interface {
	Status() lansharing.Status
	SetLocalNickname(context.Context, string) (lansharing.Status, error)
	Start(context.Context) (lansharing.Status, error)
	Stop(context.Context) (lansharing.Status, error)
	DiscoveredPeers() []lansharing.Peer
	TrustedPeers(context.Context) ([]lansharing.PeerSummary, error)
	PairPeer(context.Context, lansharing.PairRequest) (lansharing.PeerSummary, error)
	RevokePeer(context.Context, string) error
	Catalogs(context.Context) ([]lansharing.CatalogSummary, error)
	CreateCatalog(context.Context, string) (lansharing.CatalogSummary, error)
	CatalogUpdates(context.Context, string, string, int) (lansharing.CatalogUpdatePage, error)
	AppendCatalogUpdate(context.Context, string, []byte, string) (bool, error)
	CollaborationCanvases(context.Context) ([]lansharing.CollaborationCanvasSummary, error)
	CreateCollaborationCanvas(context.Context, string, string, []byte) (lansharing.CollaborationCanvasSummary, error)
	UpdateCollaborationCanvas(context.Context, string, *string, *bool) (lansharing.CollaborationCanvasSummary, error)
	DeleteCollaborationCanvas(context.Context, string) error
	CollaborationCanvasUpdates(context.Context, string, string, int) (lansharing.CollaborationCanvasUpdatePage, error)
	AppendCollaborationCanvasUpdate(context.Context, string, []byte) (bool, error)
	CreateCollaborationCanvasStreamTicket(context.Context, string) (string, error)
	HandleCollaborationCanvasStream(http.ResponseWriter, *http.Request, string)
}

type lanRoomAPI interface {
	Rooms(context.Context) ([]lansharing.RoomSummary, error)
	CreateRoom(context.Context, string) (lansharing.RoomState, error)
	DiscoverRooms(context.Context, string) ([]lansharing.RoomLocation, error)
	RequestRoomJoin(context.Context, lansharing.RoomJoinInput) (model.LANRoomJoin, error)
	RoomRequests(context.Context) ([]model.LANRoomJoin, error)
	ApproveRoomJoin(context.Context, string, bool) (lansharing.RoomState, error)
	RoomAction(context.Context, string, string, string) (lansharing.RoomState, error)
}

type roomCatalogAPI interface {
	CreateRoomCatalog(context.Context, string, string) (lansharing.CatalogSummary, error)
}
type roomCanvasAPI interface {
	CreateRoomCollaborationCanvas(context.Context, string, string, string, []byte) (lansharing.CollaborationCanvasSummary, error)
}

func RegisterLANSharingRoutes(api *gin.RouterGroup, _ *app.Service, manager lanSharingAPI) {
	if api == nil || manager == nil {
		return
	}
	routes := api.Group("/lan-sharing")
	routes.Use(func(c *gin.Context) {
		if !requireTrustedDesktopWritePrincipal(c, "局域网共享只能由受信任的桌面界面操作") {
			c.Abort()
			return
		}
		c.Next()
	})

	routes.GET("/status", func(c *gin.Context) { ok(c, manager.Status()) })
	routes.PUT("/nickname", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<10)
		var request struct {
			Nickname string `json:"nickname"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("设备名称无效"))
			return
		}
		status, err := manager.SetLocalNickname(c.Request.Context(), request.Nickname)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, status)
	})
	routes.POST("/start", func(c *gin.Context) {
		status, err := manager.Start(c.Request.Context())
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, status)
	})
	routes.POST("/stop", func(c *gin.Context) {
		status, err := manager.Stop(c.Request.Context())
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, status)
	})
	routes.GET("/peers", func(c *gin.Context) {
		trusted, err := manager.TrustedPeers(c.Request.Context())
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		discovered := make([]gin.H, 0)
		for _, peer := range manager.DiscoveredPeers() {
			for _, address := range peer.Addresses {
				discovered = append(discovered, gin.H{
					"peerId": peer.ID, "nodeId": peer.ID, "nickname": peer.Name,
					"address": address, "port": peer.Port,
				})
			}
		}
		ok(c, gin.H{"discovered": discovered, "trusted": trusted})
	})
	routes.POST("/pair", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
		var request lansharing.PairRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("配对请求无效"))
			return
		}
		peer, err := manager.PairPeer(c.Request.Context(), request)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"peer": peer})
	})
	routes.DELETE("/peers/:peerId", func(c *gin.Context) {
		if err := manager.RevokePeer(c.Request.Context(), c.Param("peerId")); err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"peerId": c.Param("peerId")})
	})
	routes.GET("/catalogs", func(c *gin.Context) {
		catalogs, err := manager.Catalogs(c.Request.Context())
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"catalogs": catalogs})
	})
	routes.POST("/catalogs", func(c *gin.Context) {
		var request struct {
			Name   string `json:"name"`
			RoomID string `json:"roomId,omitempty"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("清单名称无效"))
			return
		}
		var catalog lansharing.CatalogSummary
		var err error
		if request.RoomID != "" {
			roomManager, supported := manager.(roomCatalogAPI)
			if !supported {
				writeLANSharingError(c, lansharing.ErrInvalid)
				return
			}
			catalog, err = roomManager.CreateRoomCatalog(c.Request.Context(), request.RoomID, request.Name)
		} else {
			catalog, err = manager.CreateCatalog(c.Request.Context(), request.Name)
		}
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"catalog": catalog})
	})
	routes.GET("/catalogs/:catalogId/updates", func(c *gin.Context) {
		limit := 100
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 500 {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("分页大小无效"))
				return
			}
			limit = parsed
		}
		page, err := manager.CatalogUpdates(c.Request.Context(), c.Param("catalogId"), c.Query("after"), limit)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		updates := make([]gin.H, 0, len(page.Updates))
		for _, update := range page.Updates {
			updates = append(updates, gin.H{
				"id": update.ID, "updateBase64": base64.StdEncoding.EncodeToString(update.Update),
				"originNodeId": update.OriginNodeID, "createdAt": update.CreatedAt,
			})
		}
		ok(c, gin.H{"updates": updates, "cursor": page.Cursor, "hasMore": page.HasMore})
	})
	routes.POST("/catalogs/:catalogId/updates", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lanSharingUpdateBodyLimit)
		var request struct {
			UpdateBase64 string `json:"updateBase64"`
		}
		if err := c.ShouldBindJSON(&request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("清单更新无效"))
			return
		}
		update, err := base64.StdEncoding.DecodeString(request.UpdateBase64)
		if err != nil || len(update) == 0 {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("清单更新编码无效"))
			return
		}
		accepted, err := manager.AppendCatalogUpdate(c.Request.Context(), c.Param("catalogId"), update, manager.Status().NodeID)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"accepted": accepted})
	})

	routes.GET("/canvases", func(c *gin.Context) {
		canvases, err := manager.CollaborationCanvases(c.Request.Context())
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"canvases": canvases})
	})
	routes.POST("/canvases", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lanSharingUpdateBodyLimit)
		var request struct {
			Name                string `json:"name"`
			AssetCatalogID      string `json:"assetCatalogId,omitempty"`
			RoomID              string `json:"roomId,omitempty"`
			InitialUpdateBase64 string `json:"initialUpdateBase64,omitempty"`
		}
		if err := bindStrictJSON(c, &request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("协作画布请求无效"))
			return
		}
		initialUpdate := []byte(nil)
		if request.InitialUpdateBase64 != "" {
			var err error
			initialUpdate, err = base64.StdEncoding.DecodeString(request.InitialUpdateBase64)
			if err != nil || len(initialUpdate) == 0 || len(initialUpdate) > 4<<20 {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("协作画布初始文档无效"))
				return
			}
		}
		var canvas lansharing.CollaborationCanvasSummary
		var err error
		if request.RoomID != "" {
			roomManager, supported := manager.(roomCanvasAPI)
			if !supported {
				writeLANSharingError(c, lansharing.ErrInvalid)
				return
			}
			canvas, err = roomManager.CreateRoomCollaborationCanvas(c.Request.Context(), request.RoomID, request.Name, request.AssetCatalogID, initialUpdate)
		} else {
			canvas, err = manager.CreateCollaborationCanvas(c.Request.Context(), request.Name, request.AssetCatalogID, initialUpdate)
		}
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"canvas": canvas})
	})
	routes.PATCH("/canvases/:canvasId", func(c *gin.Context) {
		var request struct {
			Name     *string `json:"name"`
			Archived *bool   `json:"archived"`
		}
		if err := bindStrictJSON(c, &request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("协作画布更新无效"))
			return
		}
		canvas, err := manager.UpdateCollaborationCanvas(c.Request.Context(), c.Param("canvasId"), request.Name, request.Archived)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"canvas": canvas})
	})
	routes.DELETE("/canvases/:canvasId", func(c *gin.Context) {
		if err := manager.DeleteCollaborationCanvas(c.Request.Context(), c.Param("canvasId")); err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"canvasId": c.Param("canvasId")})
	})
	routes.GET("/canvases/:canvasId/updates", func(c *gin.Context) {
		limit := 100
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 500 {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("分页大小无效"))
				return
			}
			limit = parsed
		}
		page, err := manager.CollaborationCanvasUpdates(c.Request.Context(), c.Param("canvasId"), c.Query("after"), limit)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		updates := make([]gin.H, 0, len(page.Updates))
		for _, update := range page.Updates {
			updates = append(updates, gin.H{
				"id": update.ID, "updateBase64": base64.StdEncoding.EncodeToString(update.Update),
				"originNodeId": update.OriginNodeID, "createdAt": update.CreatedAt,
			})
		}
		ok(c, gin.H{"updates": updates, "cursor": page.Cursor, "hasMore": page.HasMore})
	})
	routes.POST("/canvases/:canvasId/updates", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, lanSharingUpdateBodyLimit)
		var request struct {
			UpdateBase64 string `json:"updateBase64"`
		}
		if err := bindStrictJSON(c, &request); err != nil {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("协作画布更新无效"))
			return
		}
		update, err := base64.StdEncoding.DecodeString(request.UpdateBase64)
		if err != nil || len(update) == 0 || len(update) > 4<<20 {
			fail(c, http.StatusBadRequest, app.BadAuthRequest("协作画布更新编码无效"))
			return
		}
		accepted, err := manager.AppendCollaborationCanvasUpdate(c.Request.Context(), c.Param("canvasId"), update)
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"accepted": accepted})
	})
	routes.POST("/canvases/:canvasId/stream-ticket", func(c *gin.Context) {
		ticket, err := manager.CreateCollaborationCanvasStreamTicket(c.Request.Context(), c.Param("canvasId"))
		if err != nil {
			writeLANSharingError(c, err)
			return
		}
		ok(c, gin.H{"ticket": ticket, "expiresInSeconds": 30})
	})
	api.GET("/lan-sharing/canvases/:canvasId/stream", func(c *gin.Context) {
		manager.HandleCollaborationCanvasStream(c.Writer, c.Request, c.Param("canvasId"))
	})
	if roomManager, supported := manager.(lanRoomAPI); supported {
		routes.GET("/rooms", func(c *gin.Context) {
			rooms, err := roomManager.Rooms(c.Request.Context())
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"rooms": rooms})
		})
		routes.POST("/rooms", func(c *gin.Context) {
			var body struct {
				Name string `json:"name"`
			}
			if err := bindStrictJSON(c, &body); err != nil {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("房间名称无效"))
				return
			}
			room, err := roomManager.CreateRoom(c.Request.Context(), body.Name)
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"room": room})
		})
		routes.GET("/rooms/discover", func(c *gin.Context) {
			rooms, err := roomManager.DiscoverRooms(c.Request.Context(), strings.TrimSpace(c.Query("code")))
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"rooms": rooms})
		})
		routes.POST("/rooms/join", func(c *gin.Context) {
			var input lansharing.RoomJoinInput
			if err := bindStrictJSON(c, &input); err != nil {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("加入房间请求无效"))
				return
			}
			request, err := roomManager.RequestRoomJoin(c.Request.Context(), input)
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"request": request})
		})
		routes.GET("/rooms/requests", func(c *gin.Context) {
			requests, err := roomManager.RoomRequests(c.Request.Context())
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"requests": requests})
		})
		routes.POST("/rooms/requests/:requestId", func(c *gin.Context) {
			var body struct {
				Approve bool `json:"approve"`
			}
			if err := bindStrictJSON(c, &body); err != nil {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("房间申请决定无效"))
				return
			}
			room, err := roomManager.ApproveRoomJoin(c.Request.Context(), c.Param("requestId"), body.Approve)
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"room": room})
		})
		routes.POST("/rooms/:roomId/actions", func(c *gin.Context) {
			var body struct {
				Kind   string `json:"kind"`
				Target string `json:"target,omitempty"`
			}
			if err := bindStrictJSON(c, &body); err != nil {
				fail(c, http.StatusBadRequest, app.BadAuthRequest("房间操作无效"))
				return
			}
			room, err := roomManager.RoomAction(c.Request.Context(), c.Param("roomId"), body.Kind, body.Target)
			if err != nil {
				writeLANSharingError(c, err)
				return
			}
			ok(c, gin.H{"room": room})
		})
	}
}

func writeLANSharingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, lansharing.ErrInvalid), errors.Is(err, lansharing.ErrCatalogUpdateBig):
		fail(c, http.StatusBadRequest, app.BadAuthRequest("局域网共享请求无效"))
	case errors.Is(err, lansharing.ErrNotFound):
		fail(c, http.StatusNotFound, app.NotFound("局域网共享记录不存在"))
	case errors.Is(err, lansharing.ErrPairingCode):
		fail(c, http.StatusConflict, app.NewAppError(http.StatusConflict, "配对码无效或已过期，请刷新对端配对码后重试"))
	case errors.Is(err, lansharing.ErrPeerUnreachable):
		fail(c, http.StatusGatewayTimeout, app.NewAppError(http.StatusGatewayTimeout, "对端未响应配对查询，请检查 IP、端口、配对码有效期和网络连通性"))
	case errors.Is(err, lansharing.ErrConflict):
		fail(c, http.StatusConflict, app.NewAppError(http.StatusConflict, "局域网共享状态已变化，请刷新后重试"))
	case errors.Is(err, lansharing.ErrPeerNotTrusted):
		fail(c, http.StatusForbidden, app.Forbidden("设备尚未建立信任关系"))
	case errors.Is(err, lansharing.ErrManagerClosed):
		fail(c, http.StatusServiceUnavailable, app.NewAppError(http.StatusServiceUnavailable, "局域网共享当前不可用"))
	default:
		failInternal(c, http.StatusInternalServerError, err)
	}
}
