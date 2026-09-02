package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	ManifestYAML         string `json:"manifest_yaml"`
	ManifestURL          string `json:"manifest_url"`
	Image                string `json:"image"`
	CallTimeoutSeconds   int    `json:"call_timeout_seconds"`
	PluginID             string `json:"plugin_id"`
	PermissionsConfirmed bool   `json:"permissions_confirmed"`
	PermissionDigest     string `json:"permission_digest"`
}

func (h *PluginHandler) InspectManifest(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	inspection, err := h.service.InspectManifest(c.Request.Context(), request.PluginID, pluginInstallInput(request, actorUserID(c)))
	if err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, inspection)
}

func (h *PluginHandler) TypeSpecifications(c *gin.Context) {
	c.JSON(http.StatusOK, h.service.TypeSpecifications())
}

func (h *PluginHandler) Install(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	plugin, err := h.service.Install(c.Request.Context(), pluginInstallInput(request, actorUserID(c)))
	if err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusCreated, plugin)
}

func (h *PluginHandler) StartInstallOperation(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	operation := h.service.StartInstallOperation(pluginInstallInput(request, actorUserID(c)))
	c.JSON(http.StatusAccepted, operation)
}

func (h *PluginHandler) Upgrade(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	plugin, err := h.service.Upgrade(c.Request.Context(), c.Param("id"), pluginInstallInput(request, actorUserID(c)))
	if err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) StartUpgradeOperation(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	operation := h.service.StartUpgradeOperation(
		c.Param("id"),
		pluginInstallInput(request, actorUserID(c)),
	)
	c.JSON(http.StatusAccepted, operation)
}

func (h *PluginHandler) Operation(c *gin.Context) {
	operation, err := h.service.GetOperation(c.Param("operation_id"))
	if err != nil {
		writePluginError(c, err, http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, operation)
}

func (h *PluginHandler) List(c *gin.Context) {
	plugins, err := h.service.List(c.Request.Context())
	if err != nil {
		writePluginError(c, err, http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, plugins)
}

func (h *PluginHandler) Marketplace(c *gin.Context) {
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > 80 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "plugin marketplace search must not exceed 80 characters"})
		return
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "12"))
	refresh, _ := strconv.ParseBool(c.DefaultQuery("refresh", "false"))
	result, err := h.service.Marketplace(c.Request.Context(), service.PluginMarketplaceQuery{
		Search: search, Type: c.Query("type"), Certification: c.Query("certification"),
		Sort: c.Query("sort"), CompatibleOnly: c.Query("compatible_only") == "true",
		Page: page, PageSize: pageSize, Refresh: refresh,
	})
	if err != nil {
		writePluginError(c, err, http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *PluginHandler) Get(c *gin.Context) {
	plugin, err := h.service.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		writePluginError(c, err, http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) History(c *gin.Context) {
	history, err := h.service.History(c.Request.Context(), c.Param("id"))
	if err != nil {
		writePluginError(c, err, http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, history)
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

func (h *PluginHandler) CheckUpdate(c *gin.Context) {
	plugin, err := h.service.CheckUpdate(c.Request.Context(), c.Param("id"))
	if err != nil {
		writePluginError(c, err, http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusOK, plugin)
}

func (h *PluginHandler) UpgradeLatest(c *gin.Context) {
	var request pluginInstallRequest
	if !bindLimitedJSON(c, &request) {
		return
	}
	operation, err := h.service.StartLatestUpgradeOperation(c.Request.Context(), c.Param("id"), pluginInstallInput(request, actorUserID(c)))
	if err != nil {
		writePluginError(c, err, http.StatusBadRequest)
		return
	}
	c.JSON(http.StatusAccepted, operation)
}

func (h *PluginHandler) Events(c *gin.Context) {
	after, _ := strconv.ParseUint(c.Query("after_id"), 10, 64)
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	query := service.PluginRuntimeEventQuery{
		AfterID: after, Limit: limit,
		Kind:    strings.TrimSpace(c.Query("kind")),
		Outcome: types.AuditOutcome(strings.TrimSpace(c.Query("outcome"))),
	}
	if value, err := time.Parse(time.RFC3339, c.Query("from")); err == nil {
		query.CreatedAfter = &value
	}
	if value, err := time.Parse(time.RFC3339, c.Query("to")); err == nil {
		query.CreatedBefore = &value
	}
	events, err := h.service.ListPersistedRuntimeEvents(c.Request.Context(), c.Param("id"), query)
	if err != nil {
		writePluginError(c, err, http.StatusBadGateway)
		return
	}
	c.JSON(http.StatusOK, events)
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

func pluginInstallInput(request pluginInstallRequest, actor string) service.PluginInstallInput {
	return service.PluginInstallInput{
		ManifestYAML:         []byte(request.ManifestYAML),
		ManifestURL:          request.ManifestURL,
		Image:                request.Image,
		CallTimeoutSeconds:   request.CallTimeoutSeconds,
		ActorUserID:          actor,
		PermissionsConfirmed: request.PermissionsConfirmed,
		PermissionDigest:     request.PermissionDigest,
	}
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
	case errors.Is(err, service.ErrPluginOperationNotFound):
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
