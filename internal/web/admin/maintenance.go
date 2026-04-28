// 遵循project_guide.md
package admin

import (
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"balanciz/internal/logging"
	"balanciz/internal/models"
)

// maintenanceMode 是内存缓存层（atomic 读，无锁，零 DB 开销）。
// 真实状态持久化在 system_settings 表中（key="maintenance_mode"），
// 服务器启动时由 initMaintenanceMode 加载，变更时由 setMaintenanceMode 双写。
var maintenanceMode atomic.Bool

// IsMaintenanceMode 返回当前维护模式状态。
// 由 middleware 在每次请求时调用，必须是零开销路径——只读 atomic，不访问 DB。
func IsMaintenanceMode() bool {
	return maintenanceMode.Load()
}

// initMaintenanceMode 在服务器启动时从数据库加载维护模式状态。
// 找不到记录时默认为 false（关闭）；DB 错误时也默认关闭并记录警告。
// 仅在 NewServer 中调用一次。
func initMaintenanceMode(db *gorm.DB) {
	var s models.SystemSetting
	err := db.First(&s, "key = ?", "maintenance_mode").Error
	if err != nil {
		maintenanceMode.Store(false)
		if err != gorm.ErrRecordNotFound {
			logging.L().Warn("maintenance mode load failed, defaulting to off", "err", err)
		}
		return
	}
	maintenanceMode.Store(s.Value == "true")
	logging.L().Info("maintenance mode loaded from db", "enabled", s.Value == "true")
}

// setMaintenanceMode 持久化维护模式状态到数据库，然后更新内存缓存。
// 使用 INSERT ... ON CONFLICT ... DO UPDATE 确保幂等性（行不存在时自动创建）。
// 写 DB 失败时返回错误，不更新内存缓存（保持一致）。
func (s *Server) setMaintenanceMode(on bool) error {
	val := "false"
	if on {
		val = "true"
	}
	err := s.DB.Exec(
		`INSERT INTO system_settings (key, value, updated_at)
		 VALUES ('maintenance_mode', ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		val, time.Now().UTC(),
	).Error
	if err != nil {
		return err
	}
	maintenanceMode.Store(on)
	return nil
}
