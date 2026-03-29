// 遵循产品需求 v1.0
package admin

import (
	"fmt"
	"math"
	"runtime"
	"runtime/metrics"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

// adminSystemStats holds lightweight process + DB size metrics for the SysAdmin dashboard.
type adminSystemStats struct {
	CPUPercent          float64
	MemoryMB            float64
	DatabaseSize        string
	StorageUsedBytes    int64
	StorageUsedReadable string
}

func (st adminSystemStats) formatCPU() string {
	return fmt.Sprintf("%.1f%%", st.CPUPercent)
}

func (st adminSystemStats) formatMemoryMB() string {
	return fmt.Sprintf("%.1f MB", st.MemoryMB)
}

// attachmentsStorageUsedBytes returns total stored bytes from the future attachments table.
// When the table or column does not exist yet, it returns 0. Extend the query or joins here
// when the file/attachment feature is implemented.
func attachmentsStorageUsedBytes(db *gorm.DB) int64 {
	if db == nil {
		return 0
	}
	var total int64
	err := db.Raw(`SELECT COALESCE(SUM(file_size_bytes), 0) FROM attachments`).Scan(&total).Error
	if err != nil {
		return 0
	}
	if total < 0 {
		return 0
	}
	return total
}

func formatStorageUsedHuman(n int64) string {
	if n < 0 {
		n = 0
	}
	const kb = 1024
	x := float64(n)
	switch {
	case n < kb:
		return fmt.Sprintf("%d B", n)
	case n < kb*kb:
		return fmt.Sprintf("%.1f KB", x/kb)
	case n < kb*kb*kb:
		return fmt.Sprintf("%.1f MB", x/(kb*kb))
	default:
		return fmt.Sprintf("%.2f GB", x/(kb*kb*kb))
	}
}

// readBusyCPUSeconds returns non-idle Go runtime CPU-seconds (total − idle from /cpu/classes metrics).
func readBusyCPUSeconds() (busy float64, ok bool) {
	samples := []metrics.Sample{
		{Name: "/cpu/classes/total:cpu-seconds"},
		{Name: "/cpu/classes/idle:cpu-seconds"},
	}
	metrics.Read(samples)
	var total, idle float64
	for i := range samples {
		switch samples[i].Value.Kind() {
		case metrics.KindFloat64:
			if i == 0 {
				total = samples[i].Value.Float64()
			} else {
				idle = samples[i].Value.Float64()
			}
		default:
			return 0, false
		}
	}
	busy = total - idle
	if busy < 0 {
		busy = 0
	}
	return busy, true
}

// collectAdminSystemStats samples CPU over a short window, reads Go heap memory, and queries DB size on PostgreSQL.
func collectAdminSystemStats(s *Server) adminSystemStats {
	out := adminSystemStats{DatabaseSize: "—"}

	t0 := time.Now()
	b0, ok0 := readBusyCPUSeconds()
	time.Sleep(100 * time.Millisecond)
	b1, ok1 := readBusyCPUSeconds()
	dt := time.Since(t0).Seconds()
	if ok0 && ok1 && dt > 0 {
		delta := b1 - b0
		n := float64(runtime.GOMAXPROCS(0))
		if n < 1 {
			n = 1
		}
		pct := (delta / dt / n) * 100
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		out.CPUPercent = math.Round(pct*10) / 10
	}

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	out.MemoryMB = math.Round(float64(ms.Alloc)/(1024*1024)*10) / 10

	if s.DB != nil && s.DB.Dialector.Name() == "postgres" {
		var pretty string
		row := s.DB.Raw(`SELECT pg_size_pretty(pg_database_size(current_database()))`).Row()
		if err := row.Scan(&pretty); err == nil && pretty != "" {
			out.DatabaseSize = pretty
		}
	} else if s.DB != nil {
		out.DatabaseSize = "N/A"
	}

	out.StorageUsedBytes = attachmentsStorageUsedBytes(s.DB)
	out.StorageUsedReadable = formatStorageUsedHuman(out.StorageUsedBytes)

	return out
}

func (s *Server) handleAdminSystemStats(c *fiber.Ctx) error {
	st := collectAdminSystemStats(s)
	return c.JSON(fiber.Map{
		"cpuPercent":           st.CPUPercent,
		"memoryMb":             st.MemoryMB,
		"databaseSize":         st.DatabaseSize,
		"storage_used":         st.StorageUsedReadable,
		"storage_used_bytes":   st.StorageUsedBytes,
	})
}
