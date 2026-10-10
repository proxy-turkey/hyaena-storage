// Package core — sistem metrikleri: disk doluluk ölçümü (B-04 izleme).
package core

import (
	"syscall"
)

// DiskUsagePct, path'in bulunduğu dosya sisteminde kullanılan yüzdelik döndürür.
func DiskUsagePct(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	if total == 0 {
		return 0, nil
	}
	return (float64(total-free) / float64(total)) * 100, nil
}

// DiskFreeBytes, path'in dosya sisteminde ayrıcalıksız kullanıcıya kalan
// baytı döndürür — upload kabul kapısı (F-05) bunu parça yazmadan önce sınar.
func DiskFreeBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
