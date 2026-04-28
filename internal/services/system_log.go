// 遵循project_guide.md
package services

import (
	"time"

	"balanciz/internal/models"

	"gorm.io/gorm"
)

// WriteSystemLog 将运行时日志条目持久化到 system_logs 表。
// 写入错误被静默忽略，以避免掩盖原始错误（日志写入失败不应导致请求进一步失败）。
func WriteSystemLog(db *gorm.DB, entry models.SystemLog) {
	_ = db.Create(&entry).Error
}

// CleanupSystemLogs 删除超过指定保留期的 system_logs 条目。
// 使用 created_at 索引执行范围删除，不影响在线查询性能。
// 返回删除的行数和可能的错误（错误由调用方记录，不中断主流程）。
func CleanupSystemLogs(db *gorm.DB, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan)
	result := db.Where("created_at < ?", cutoff).Delete(&models.SystemLog{})
	return result.RowsAffected, result.Error
}
