package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/relayguard"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetRelayGuard(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": operation_setting.RelayGuardSettings()})
}
func UpdateRelayGuard(c *gin.Context) {
	cfg := operation_setting.DefaultRelayGuardConfig()
	if err := c.ShouldBindJSON(&cfg); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid request settings"))
		return
	}
	if err := operation_setting.ValidateRelayGuardConfig(cfg); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid request settings"))
		return
	}
	if err := service.RelayRequestBudget().ValidateClassroomLimits(cfg); err != nil {
		classroomAPIError(c, err)
		return
	}
	data, err := common.Marshal(cfg)
	if err == nil {
		err = model.UpdateOptionsBulk(map[string]string{operation_setting.RelayGuardOption: string(data)})
	}
	if err != nil {
		common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Failed to save settings"))
		return
	}
	service.RelayResponseCache.Clear()
	GetRelayGuard(c)
}
func GetRelayGuardStats(c *gin.Context) {
	usage, err := service.RelayRequestBudget().Stats(time.Now())
	classes, classErr := service.RelayRequestBudget().Classrooms()
	names := map[string]string{}
	for _, classroom := range classes {
		names[classroom.ID] = classroom.Name
	}
	if err == nil {
		err = classErr
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"cache": service.RelayResponseCache.Stats(), "usage": usage, "classroom_names": names, "storage_error": message, "quota_per_unit": common.QuotaPerUnit}})
}
func ClearRelayResponseCache(c *gin.Context) {
	service.RelayResponseCache.Clear()
	c.JSON(http.StatusOK, gin.H{"success": true})
}
func ResetRelayGuardUsage(c *gin.Context) {
	var request struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Key == "" {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid request settings"))
		return
	}
	if err := service.RelayRequestBudget().Reset(request.Key); err != nil {
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Cannot reset usage while requests are active"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func GetRelayClassrooms(c *gin.Context) {
	classes, err := service.RelayRequestBudget().Classrooms()
	if err != nil {
		classroomAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": classes})
}

func StartRelayClassroom(c *gin.Context) {
	var input relayguard.ClassroomInput
	if err := c.ShouldBindJSON(&input); err != nil {
		classroomAPIError(c, relayguard.ErrClassroomSettings)
		return
	}
	cfg := operation_setting.RelayGuardSettings()
	if cfg.Invalid || !cfg.Limits.Enabled {
		common.ApiErrorStatus(c, http.StatusConflict, common.NewMessage("Enable IP and API key limits before starting a classroom"))
		return
	}
	if _, err := model.GetTokenById(input.KeyID); err != nil {
		classroomAPIError(c, relayguard.ErrClassroomSettings)
		return
	}
	classroom, err := service.RelayRequestBudget().StartClassroom(input, cfg.Limits.Rules, time.Now())
	if err != nil {
		classroomAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": classroom})
}

func ResetRelayClassroom(c *gin.Context) {
	subjects, err := service.RelayRequestBudget().ResetClassroom(c.Param("id"), time.Now())
	if err != nil {
		classroomAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"subjects": subjects}})
}

func EndRelayClassroom(c *gin.Context) {
	if err := service.RelayRequestBudget().EndClassroom(c.Param("id"), time.Now()); err != nil {
		classroomAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true})
}

func GetRelayClassroomBill(c *gin.Context) {
	bill, err := service.RelayRequestBudget().ClassroomReport(c.Param("id"), c.Query("from"), c.Query("to"))
	if err != nil {
		classroomAPIError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": bill})
}

func classroomAPIError(c *gin.Context, err error) {
	status, message := http.StatusServiceUnavailable, "Teaching archive storage is unavailable"
	for _, known := range []struct {
		err    error
		status int
	}{
		{relayguard.ErrClassroomSettings, http.StatusBadRequest},
		{relayguard.ErrBillingDates, http.StatusBadRequest},
		{relayguard.ErrClassroomBusy, http.StatusConflict},
		{relayguard.ErrClassroomOverlap, http.StatusConflict},
		{relayguard.ErrClassroomNotFound, http.StatusNotFound},
		{relayguard.ErrTeachingCapacity, http.StatusConflict},
		{relayguard.ErrClassroomRules, http.StatusConflict},
	} {
		if errors.Is(err, known.err) {
			status, message = known.status, known.err.Error()
			break
		}
	}
	if status == http.StatusServiceUnavailable {
		logger.LogError(c, "teaching archive operation failed: "+err.Error())
	}
	common.ApiErrorStatus(c, status, common.NewMessage(message))
}
