package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/plugin/runtimeclient"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

const maxPluginInstallBodyBytes = 1024 * 1024

type PluginHandler struct {
	service *service.PluginService
}

func NewPluginHandler(service *service.PluginService) *PluginHandler {
	return &PluginHandler{service: service}
}

type pluginInstallRequest struct {
	ManifestYAML       string `json:"manifest_yaml" binding:"required"`
	Image              string `json:"image"`
	CallTimeoutSeconds int    `json:"call_timeout_seconds"`
}

func (h *PluginHandler) Install(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	plugin, err := h.service.Install(c.Request.Context(), service.PluginInstallInput{
		ManifestYAML:       []byte(request.ManifestYAML),
		Image:              request.Image,
		CallTimeoutSeconds: request.CallTimeoutSeconds,
		ActorUserID:        actorUserID(c),
	})
	if err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusCreated, plugin)
}

func (h *PluginHandler) Upgrade(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	plugin, err := h.service.Upgrade(c.Request.Context(), c.Param("id"), service.PluginInstallInput{
		ManifestYAML:       []byte(request.ManifestYAML),
		Image:              request.Image,
		CallTimeoutSeconds: request.CallTimeoutSeconds,
		ActorUserID:        actorUserID(c),
	})
	if err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) List(c *gin.Context) {
	plugins, err := h.service.List(c.Request.Context())
	if err != nil {
		writePluginError(c, err, http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, plugins)
}

func (h *PluginHandler) Get(c *gin.Context) {
	plugin, err := h.service.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writePluginError(c, err, http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) Enable(c *gin.Context) {
	plugin, err := h.service.Enable(c.Request.Context(), c.Param("id"), actorUserID(c))
	if err != nil {
		writePluginError(c, err, http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) Disable(c *gin.Context) {
	plugin, err := h.service.Disable(c.Request.Context(), c.Param("id"), actorUserID(c))
	if err != nil {
		writePluginError(c, err, http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) Health(c *gin.Context) {
	plugin, err := h.service.RefreshHealth(c.Request.Context(), c.Param("id"))
	if err != nil {
		writePluginError(c, err, http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) Uninstall(c *gin.Context) {
	if err := h.service.Uninstall(c.Request.Context(), c.Param("id"), actorUserID(c)); err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.Status(http.StatusNoContent)
}

func bindLimitedJSON(c *gin.Context, destination any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPluginInstallBodyBytes)
	if err := c.ShouldBindJSON(destination); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid plugin request: " + err.Error()})
		return false
	}
	return true
}

func actorUserID(c *gin.Context) string {
	actor, _ := types.UserIDFromContext(c.Request.Context())
	return actor
}

func writePluginError(c *gin.Context, err error, fallback int) {
	status := fallback
	switch {
	case errors.Is(err, service.ErrPluginNotFound):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrPluginInUse):
		status = http.StatusConflict
	case errors.Is(err, runtimeclient.ErrDisabled):
		status = http.StatusServiceUnavailable
	case strings.Contains(strings.ToLower(err.Error()), "already installed"):
		status = http.StatusConflict
	}
	c.JSON(status, gin.H{"error": err.Error()})
}
