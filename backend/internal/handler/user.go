package handler

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/routerapi/backend/internal/model"
	"github.com/routerapi/backend/pkg/crypto"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// ---------- User 用户管理 ----------

type UserHandler struct {
	db *gorm.DB
}

func NewUserHandler(db *gorm.DB) *UserHandler {
	return &UserHandler{db: db}
}

// List 用户列表 (仅 admin)
func (h *UserHandler) List(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	query := h.db.Model(&model.User{})

	// 筛选
	if keyword := c.Query("keyword"); keyword != "" {
		like := "%" + escapeLike(keyword) + "%"
		query = query.Where("username LIKE ? ESCAPE '\\' OR email LIKE ? ESCAPE '\\'", like, like)
	}
	if role := c.Query("role"); role != "" {
		query = query.Where("role = ?", role)
	}
	if status := c.Query("status"); status != "" {
		query = query.Where("status = ?", status)
	}

	var total int64
	query.Count(&total)

	var users []model.User
	query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&users)

	c.JSON(http.StatusOK, gin.H{
		"data":      users,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// Create 创建用户 (仅 admin)
func (h *UserHandler) Create(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
		Email    string `json:"email"`
		Role     string `json:"role"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	// 检查用户名是否已存在
	var count int64
	h.db.Model(&model.User{}).Where("username = ?", req.Username).Count(&count)
	if count > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "用户名已存在"})
		return
	}

	// 校验角色
	role := req.Role
	if role == "" {
		role = "viewer"
	}
	if role != "admin" && role != "viewer" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色必须为 admin 或 viewer"})
		return
	}

	// 加密密码
	hashed, err := crypto.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	user := model.User{
		Username: req.Username,
		Password: hashed,
		Email:    req.Email,
		Role:     role,
		Status:   1,
	}

	if err := h.db.Create(&user).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建失败: " + err.Error()})
		return
	}

	logrus.Infof("用户 %s 已创建 (角色: %s, 操作者: %s)", user.Username, user.Role, c.GetString("username"))
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// Update 更新用户信息 (仅 admin)
func (h *UserHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var req struct {
		Email   *string `json:"email"`
		Role    *string `json:"role"`
		Status  *int    `json:"status"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	// 校验角色
	if req.Role != nil && *req.Role != "admin" && *req.Role != "viewer" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "角色必须为 admin 或 viewer"})
		return
	}

	// 不允许最后一个 admin 降级
	if req.Role != nil && *req.Role != "admin" && user.Role == "admin" {
		var adminCount int64
		h.db.Model(&model.User{}).Where("role = ? AND status = 1", "admin").Count(&adminCount)
		if adminCount <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不允许降级最后一个管理员"})
			return
		}
	}

	// 不允许最后一个 admin 禁用
	if req.Status != nil && *req.Status == 0 && user.Role == "admin" && user.Status == 1 {
		var adminCount int64
		h.db.Model(&model.User{}).Where("role = ? AND status = 1", "admin").Count(&adminCount)
		if adminCount <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不允许禁用最后一个管理员"})
			return
		}
	}

	updates := map[string]interface{}{}
	if req.Email != nil {
		updates["email"] = *req.Email
	}
	if req.Role != nil {
		updates["role"] = *req.Role
	}
	if req.Status != nil {
		updates["status"] = *req.Status
		if *req.Status == 0 {
			// 禁用账户时递增 TokenVersion，立即撤销其所有已签发令牌
			updates["token_version"] = gorm.Expr("token_version + 1")
		}
	}

	if len(updates) > 0 {
		h.db.Model(&user).Updates(updates)
	}

	// 重新查询
	h.db.First(&user, id)
	logrus.Infof("用户 %s 已更新 (操作者: %s)", user.Username, c.GetString("username"))
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// Delete 删除用户 (仅 admin)
func (h *UserHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	// 不允许删除最后一个 admin
	if user.Role == "admin" {
		var adminCount int64
		h.db.Model(&model.User{}).Where("role = ? AND status = 1", "admin").Count(&adminCount)
		if adminCount <= 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "不允许删除最后一个管理员"})
			return
		}
	}

	// 不允许删除自己
	currentUserID := c.GetUint("user_id")
	if user.ID == currentUserID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不允许删除当前登录用户"})
		return
	}

	h.db.Delete(&user)
	logrus.Infof("用户 %s 已删除 (操作者: %s)", user.Username, c.GetString("username"))
	c.JSON(http.StatusOK, gin.H{"message": "已删除"})
}

// ResetPassword 重置用户密码 (仅 admin)
func (h *UserHandler) ResetPassword(c *gin.Context) {
	id := c.Param("id")
	var user model.User
	if err := h.db.First(&user, id).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var req struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	hashed, err := crypto.HashPassword(req.Password)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	h.db.Model(&user).Updates(map[string]interface{}{
		"password":      hashed,
		"token_version": gorm.Expr("token_version + 1"),
	})
	logrus.Infof("用户 %s 的密码已被重置 (操作者: %s)", user.Username, c.GetString("username"))
	c.JSON(http.StatusOK, gin.H{"message": "密码已重置"})
}

// ---------- 当前用户操作 ----------

// GetProfile 获取当前登录用户信息
func (h *UserHandler) GetProfile(c *gin.Context) {
	userID := c.GetUint("user_id")
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// ChangePassword 修改自己的密码
func (h *UserHandler) ChangePassword(c *gin.Context) {
	userID := c.GetUint("user_id")
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var req struct {
		OldPassword string `json:"old_password" binding:"required"`
		NewPassword string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	// 校验旧密码
	if err := crypto.CheckPassword(user.Password, req.OldPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "原密码错误"})
		return
	}

	// 加密新密码
	hashed, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "密码加密失败"})
		return
	}

	// 改密后递增 TokenVersion，撤销所有已签发令牌（含当前会话），强制重新登录
	h.db.Model(&user).Updates(map[string]interface{}{
		"password":      hashed,
		"token_version": gorm.Expr("token_version + 1"),
	})
	logrus.Infof("用户 %s 修改了密码（已撤销旧登录令牌）", user.Username)
	c.JSON(http.StatusOK, gin.H{"message": "密码修改成功，请重新登录"})
}

// UpdateProfile 修改自己的资料 (邮箱)
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	userID := c.GetUint("user_id")
	var user model.User
	if err := h.db.First(&user, userID).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}

	var req struct {
		Email *string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	updates := map[string]interface{}{}
	if req.Email != nil {
		updates["email"] = *req.Email
	}

	if len(updates) > 0 {
		h.db.Model(&user).Updates(updates)
	}

	h.db.First(&user, userID)
	c.JSON(http.StatusOK, gin.H{"data": user})
}

// UpdateLastLogin 更新最后登录时间
func (h *UserHandler) UpdateLastLogin(userID uint) {
	now := time.Now()
	h.db.Model(&model.User{}).Where("id = ?", userID).Update("last_login_at", now)
}
